package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestChatCompletionsRequestToResponsesRequestPreservesFunctionStrict(t *testing.T) {
	request := &dto.GeneralOpenAIRequest{
		Model:    "gpt-5.4",
		Messages: []dto.Message{{Role: "user", Content: "生成第一集"}},
		Tools: []dto.ToolCallRequest{{
			Type: "function",
			Function: dto.FunctionRequest{
				Name:       "propose_scene_batch",
				Strict:     json.RawMessage("true"),
				Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			},
		}},
	}

	converted, err := ChatCompletionsRequestToResponsesRequest(request)

	require.NoError(t, err)
	var tools []map[string]interface{}
	require.NoError(t, common.Unmarshal(converted.Tools, &tools))
	require.Len(t, tools, 1)
	require.Equal(t, true, tools[0]["strict"])
}
