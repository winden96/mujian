package gemini

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIToGeminiDropsUnsupportedFunctionStrict(t *testing.T) {
	request := dto.GeneralOpenAIRequest{
		Model:    "gemini-2.5-pro",
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
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{
		ChannelType: constant.ChannelTypeGemini, UpstreamModelName: request.Model,
	}}

	converted, err := CovertOpenAI2Gemini(nil, request, info)

	require.NoError(t, err)
	tools := converted.GetTools()
	require.Len(t, tools, 1)
	encoded, err := common.Marshal(converted)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"strict"`)
}
