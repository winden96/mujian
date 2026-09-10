package common

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func UnmarshalJsonStr(data string, v any) error {
	return json.Unmarshal(StringToByteSlice(data), v)
}

func DecodeJson(reader io.Reader, v any) error {
	return json.NewDecoder(reader).Decode(v)
}

func Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func GetJsonType(data json.RawMessage) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "unknown"
	}
	firstChar := trimmed[0]
	switch firstChar {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

// DecodeUniqueTopLevelStringField reads one exact, case-sensitive string field
// while rejecting case variants, duplicates, non-object roots, and trailing
// JSON. Protocol parsers use this helper when a regular map decode would hide
// duplicate security-sensitive discriminator keys.
func DecodeUniqueTopLevelStringField(data []byte, field string) (string, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return "", false, fmt.Errorf("decode JSON object: %w", err)
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return "", false, errors.New("JSON value must be an object")
	}

	value := ""
	found := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", false, fmt.Errorf("decode JSON object key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return "", false, errors.New("JSON object key must be a string")
		}

		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return "", false, fmt.Errorf("decode JSON object field: %w", err)
		}
		if !strings.EqualFold(key, field) {
			continue
		}
		if key != field {
			return "", false, fmt.Errorf("JSON field %q must use exact lowercase spelling", field)
		}
		if found {
			return "", false, fmt.Errorf("JSON object contains duplicate %q fields", field)
		}
		found = true
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '"' {
			return "", false, fmt.Errorf("JSON field %q must be a string", field)
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", false, fmt.Errorf("decode JSON string field %q: %w", field, err)
		}
	}

	closing, err := decoder.Token()
	if err != nil {
		return "", false, fmt.Errorf("close JSON object: %w", err)
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return "", false, errors.New("JSON object was not closed")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return "", false, errors.New("JSON value contains trailing data")
		}
		return "", false, fmt.Errorf("decode trailing JSON data: %w", err)
	}
	return value, found, nil
}
