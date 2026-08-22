package mujianobject

import (
	"errors"
	"fmt"
	"mime"
	"strings"

	"github.com/google/uuid"
)

var imageExtensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// 191 keeps indexed object-key columns within the legacy InnoDB utf8mb4 index
// limit while leaving ample room for the fixed prefix and UUID-only layout.
const maxObjectKeyBytes = 191

func BuildObjectKey(prefix string, kind ObjectKind, id, contentType string) (string, error) {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if err := validateObjectPath(prefix); err != nil {
		return "", fmt.Errorf("invalid object prefix: %w", err)
	}
	parsedID, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return "", errors.New("object id must be a UUID")
	}
	extension, err := imageExtension(contentType)
	if err != nil {
		return "", err
	}
	canonicalID := parsedID.String()
	var objectKey string
	switch kind {
	case ObjectKindGeneration:
		objectKey = prefix + "/generations/" + canonicalID + "/result" + extension
	case ObjectKindReference:
		objectKey = prefix + "/references/" + canonicalID + extension
	case ObjectKindShot:
		objectKey = prefix + "/shots/" + canonicalID + "/result" + extension
	default:
		return "", fmt.Errorf("unsupported object kind %q", kind)
	}
	if len(objectKey) > maxObjectKeyBytes {
		return "", fmt.Errorf("object key exceeds %d bytes", maxObjectKeyBytes)
	}
	return objectKey, nil
}

func imageExtension(contentType string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return "", fmt.Errorf("invalid image content type: %w", err)
	}
	extension, ok := imageExtensions[strings.ToLower(mediaType)]
	if !ok {
		return "", fmt.Errorf("unsupported image content type %q", mediaType)
	}
	return extension, nil
}

func validateObjectPath(value string) error {
	if value == "" {
		return errors.New("path is empty")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errors.New("path contains an empty or relative segment")
		}
	}
	return nil
}
