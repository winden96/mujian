package claude

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestRequestOpenAI2ClaudeMessageRejectsMalformedDynamicFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "tool parameters must be object",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":"object"}}]}`,
			want: "function.parameters must be an object",
		},
		{
			name: "tool schema type must be string",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":1}}}]}`,
			want: "function.parameters.type must be a non-empty string",
		},
		{
			name: "stop array values must be strings",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"x"}],"stop":["ok",1]}`,
			want: "stop sequence at index 1",
		},
		{
			name: "stop must use supported shape",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"x"}],"stop":{"value":"x"}}`,
			want: "string or an array of strings",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var request dto.GeneralOpenAIRequest
			require.NoError(t, common.Unmarshal([]byte(test.body), &request))

			converted, err := RequestOpenAI2ClaudeMessage(nil, request)

			require.Nil(t, converted)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestRequestOpenAI2ClaudeMessageAcceptsProgrammaticStopSlice(t *testing.T) {
	request := dto.GeneralOpenAIRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []dto.Message{{Role: "user", Content: "x"}},
		Stop:     []string{"first", "second"},
	}

	converted, err := RequestOpenAI2ClaudeMessage(nil, request)

	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, converted.StopSequences)
}
