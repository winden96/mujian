package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeUniqueTopLevelStringField(t *testing.T) {
	value, found, err := DecodeUniqueTopLevelStringField(
		[]byte(`{"nested":{"type":"ignored"},"type":"message_start"}`),
		"type",
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "message_start", value)

	value, found, err = DecodeUniqueTopLevelStringField([]byte(`{"other":1}`), "type")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, value)
}

func TestDecodeUniqueTopLevelStringFieldRejectsAmbiguousJSON(t *testing.T) {
	for _, data := range []string{
		`[]`,
		`{"TYPE":"ping"}`,
		`{"type":"message_start","type":"ping"}`,
		`{"type":null}`,
		`{"type":"ping"}{}`,
	} {
		_, _, err := DecodeUniqueTopLevelStringField([]byte(data), "type")
		require.Error(t, err, data)
	}
}
