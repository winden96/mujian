package claude

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type failOnWriteResponseWriter struct {
	gin.ResponseWriter
	failAt int
	writes int
}

type panicOnWriteResponseWriter struct {
	gin.ResponseWriter
	panicAt int
	writes  int
}

func (writer *panicOnWriteResponseWriter) Write(data []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.panicAt {
		panic("downstream writer panic")
	}
	return writer.ResponseWriter.Write(data)
}

func (writer *failOnWriteResponseWriter) Write(data []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failAt {
		return 0, errors.New("downstream write failed")
	}
	return writer.ResponseWriter.Write(data)
}

func TestGeneralOpenAIRequestPreservesClaudeNativeReasoningControls(t *testing.T) {
	var request dto.GeneralOpenAIRequest
	require.NoError(t, common.Unmarshal([]byte(`{
		"model":"claude-opus-5",
		"messages":[{"role":"user","content":"hello"}],
		"thinking":{"type":"adaptive","display":"summarized"},
		"output_config":{"effort":"high"},
		"max_tokens":8192
	}`), &request))

	require.JSONEq(t, `{"type":"adaptive","display":"summarized"}`, string(request.THINKING))
	require.JSONEq(t, `{"effort":"high"}`, string(request.OutputConfig))
	encoded, err := common.Marshal(request)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"thinking":{"type":"adaptive","display":"summarized"}`)
	require.Contains(t, string(encoded), `"output_config":{"effort":"high"}`)
}

func TestRequestOpenAI2ClaudeMessageExplicitAdaptiveThinkingWins(t *testing.T) {
	maxTokens := uint(8192)
	temperature := 0.3
	topP := 0.8
	topK := 5
	stream := true
	request := dto.GeneralOpenAIRequest{
		Model:           "claude-opus-5",
		Messages:        []dto.Message{{Role: "user", Content: "hello"}},
		Stream:          &stream,
		MaxTokens:       &maxTokens,
		Temperature:     &temperature,
		TopP:            &topP,
		TopK:            &topK,
		ReasoningEffort: "high",
		Reasoning:       json.RawMessage(`{"max_tokens":5000}`),
		THINKING:        json.RawMessage(`{"type":"adaptive","display":"summarized"}`),
		OutputConfig:    json.RawMessage(`{"effort":"high"}`),
	}

	converted, err := RequestOpenAI2ClaudeMessage(nil, request)

	require.NoError(t, err)
	require.NotNil(t, converted.Thinking)
	require.Equal(t, "adaptive", converted.Thinking.Type)
	require.Equal(t, "summarized", converted.Thinking.Display)
	require.Nil(t, converted.Thinking.BudgetTokens)
	require.JSONEq(t, `{"effort":"high"}`, string(converted.OutputConfig))
	require.NotNil(t, converted.MaxTokens)
	require.Equal(t, maxTokens, *converted.MaxTokens)
	require.Nil(t, converted.Temperature)
	require.Nil(t, converted.TopP)
	require.Nil(t, converted.TopK)
	encoded, err := common.Marshal(converted)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"reasoning_effort"`)
	require.NotContains(t, string(encoded), `"temperature"`)
}

func TestRequestOpenAI2ClaudeMessagePreservesNestedArrayToolSchema(t *testing.T) {
	var request dto.GeneralOpenAIRequest
	require.NoError(t, common.Unmarshal([]byte(`{
		"model":"claude-opus-5",
		"messages":[{"role":"user","content":"生成第一集"}],
		"tools":[{"type":"function","function":{
			"name":"propose_scene_batch",
			"strict":true,
			"parameters":{
				"type":"object",
				"properties":{"scenes":{
					"type":"array",
					"items":{"type":"object","properties":{"dialogues":{
						"type":"array",
						"items":{"type":"object","properties":{"character":{"type":"string"},"line":{"type":"string"}}}
					}}}
				}},
				"required":["scenes"]
			}
		}}]
	}`), &request))

	parameters := request.Tools[0].Function.Parameters.(map[string]interface{})
	converted, err := RequestOpenAI2ClaudeMessage(nil, request)

	require.NoError(t, err)
	tools := converted.GetTools()
	require.Len(t, tools, 1)
	tool, ok := tools[0].(*dto.Tool)
	require.True(t, ok)
	require.JSONEq(t, "true", string(tool.Strict))
	require.Equal(t, parameters, tool.InputSchema)
	properties := tool.InputSchema["properties"].(map[string]interface{})
	scenes := properties["scenes"].(map[string]interface{})
	require.Equal(t, "array", scenes["type"])
	sceneItems := scenes["items"].(map[string]interface{})
	sceneProperties := sceneItems["properties"].(map[string]interface{})
	dialogues := sceneProperties["dialogues"].(map[string]interface{})
	require.Equal(t, "array", dialogues["type"])
}

func TestRequestOpenAI2ClaudeMessageDefaultsEmptyRoleWithoutMutatingInput(t *testing.T) {
	request := dto.GeneralOpenAIRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []dto.Message{{Content: "hello"}},
	}

	converted, err := RequestOpenAI2ClaudeMessage(nil, request)

	require.NoError(t, err)
	require.Len(t, converted.Messages, 1)
	require.Equal(t, "user", converted.Messages[0].Role)
	require.Equal(t, "", request.Messages[0].Role)
}

func TestRequestOpenAI2ClaudeMessageRejectsMalformedMessageWithoutPanic(t *testing.T) {
	for _, test := range []struct {
		name     string
		messages []dto.Message
	}{
		{
			name:     "unsupported role",
			messages: []dto.Message{{Role: "invalid", Content: "hello"}},
		},
		{
			name: "object content before tool",
			messages: []dto.Message{
				{Role: "user", Content: map[string]any{"text": "hello"}},
				{Role: "tool", ToolCallId: "tool_1", Content: "result"},
			},
		},
		{
			name: "numeric content before tool",
			messages: []dto.Message{
				{Role: "user", Content: 42},
				{Role: "tool", ToolCallId: "tool_1", Content: "result"},
			},
		},
		{
			name:     "non-object array item",
			messages: []dto.Message{{Role: "user", Content: []any{"hello"}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := dto.GeneralOpenAIRequest{Model: "claude-sonnet-4-6", Messages: test.messages}

			require.NotPanics(t, func() {
				converted, err := RequestOpenAI2ClaudeMessage(nil, request)
				require.Error(t, err)
				require.Nil(t, converted)
			})
		})
	}
}

func TestStreamResponseClaude2OpenAISeparatesThinkingAndSignature(t *testing.T) {
	t.Run("thinking delta becomes reasoning content", func(t *testing.T) {
		thinking := "先分析创作要求。"
		response := StreamResponseClaude2OpenAI(&dto.ClaudeResponse{
			Type:  "content_block_delta",
			Delta: &dto.ClaudeMediaMessage{Type: "thinking_delta", Thinking: &thinking},
		})

		require.NotNil(t, response)
		require.Len(t, response.Choices, 1)
		require.Equal(t, thinking, response.Choices[0].Delta.GetReasoningContent())
		require.Empty(t, response.Choices[0].Delta.GetContentString())
	})

	t.Run("signature delta remains protocol metadata", func(t *testing.T) {
		response := StreamResponseClaude2OpenAI(&dto.ClaudeResponse{
			Type:  "content_block_delta",
			Delta: &dto.ClaudeMediaMessage{Type: "signature_delta", Signature: "signed-bytes"},
		})

		require.NotNil(t, response)
		require.Len(t, response.Choices, 1)
		require.Empty(t, response.Choices[0].Delta.GetReasoningContent())
		require.Empty(t, response.Choices[0].Delta.GetContentString())
	})
}

func TestResponseClaude2OpenAIConcatenatesNonStreamContentBlocks(t *testing.T) {
	firstText, secondText := "first ", "second"
	firstThinking, secondThinking := "think ", "again"
	response := ResponseClaude2OpenAI(&dto.ClaudeResponse{
		Type: "message",
		Content: []dto.ClaudeMediaMessage{
			{Type: "text", Text: &firstText},
			{Type: "thinking", Thinking: &firstThinking},
			{Type: "text", Text: &secondText},
			{Type: "thinking", Thinking: &secondThinking},
		},
	})

	require.Len(t, response.Choices, 1)
	require.Equal(t, firstText+secondText, response.Choices[0].Message.StringContent())
	require.Equal(t, firstThinking+secondThinking, response.Choices[0].Message.ReasoningContent)
}

func TestStreamResponseClaude2OpenAIPreservesToolCalls(t *testing.T) {
	index := 1
	started := StreamResponseClaude2OpenAI(&dto.ClaudeResponse{
		Type:  "content_block_start",
		Index: &index,
		ContentBlock: &dto.ClaudeMediaMessage{
			Type: "tool_use",
			Id:   "tool_123",
			Name: "propose_scene_batch",
		},
	})
	require.NotNil(t, started)
	require.Len(t, started.Choices, 1)
	require.Len(t, started.Choices[0].Delta.ToolCalls, 1)
	require.Equal(t, "tool_123", started.Choices[0].Delta.ToolCalls[0].ID)
	require.Equal(t, "propose_scene_batch", started.Choices[0].Delta.ToolCalls[0].Function.Name)

	partialJSON := `{"scenes":[{`
	delta := StreamResponseClaude2OpenAI(&dto.ClaudeResponse{
		Type:  "content_block_delta",
		Index: &index,
		Delta: &dto.ClaudeMediaMessage{
			Type:        "input_json_delta",
			PartialJson: &partialJSON,
		},
	})
	require.NotNil(t, delta)
	require.Len(t, delta.Choices[0].Delta.ToolCalls, 1)
	require.Equal(t, partialJSON, delta.Choices[0].Delta.ToolCalls[0].Function.Arguments)

	require.NotPanics(t, func() {
		StreamResponseClaude2OpenAI(&dto.ClaudeResponse{
			Type:  "content_block_delta",
			Index: &index,
			Delta: &dto.ClaudeMediaMessage{Type: "input_json_delta"},
		})
	})
}

func TestClaudeStreamHandlerAttestsSeparatedReasoningContent(t *testing.T) {
	for _, test := range []struct {
		name           string
		relayFormat    types.RelayFormat
		expectedHeader string
	}{
		{name: "OpenAI conversion", relayFormat: types.RelayFormatOpenAI, expectedHeader: "true"},
		{name: "native Claude response", relayFormat: types.RelayFormatClaude},
	} {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			response := &http.Response{Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_header\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":1}}}\n\n" +
					"data: {\"type\":\"message_stop\"}\n\n",
			))}
			info := &relaycommon.RelayInfo{
				IsStream:    true,
				RelayFormat: test.relayFormat,
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-opus-5"},
			}

			_, relayErr := ClaudeStreamHandler(context, response, info)

			require.Nil(t, relayErr)
			require.Equal(t, test.expectedHeader, recorder.Header().Get(common.ReasoningContentSeparatedHeader))
		})
	}
}

func TestClaudeStreamHandlerIgnoresSSEComments(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	response := &http.Response{Body: io.NopCloser(strings.NewReader(
		": keep-alive\n\n" +
			"event: message_start\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_comment\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":1}}}\n\n" +
			": upstream heartbeat\n\n" +
			"event: message_stop\n" +
			"data: {\"type\":\"message_stop\"}\n\n",
	))}
	info := &relaycommon.RelayInfo{
		IsStream: true, RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
	}

	usage, relayErr := ClaudeStreamHandler(context, response, info)

	require.Nil(t, relayErr)
	require.NotNil(t, usage)
	require.Equal(t, 1, usage.PromptTokens)
	require.NotContains(t, recorder.Body.String(), "keep-alive")
	require.NotContains(t, recorder.Body.String(), "upstream heartbeat")
}

func TestClaudeStreamHandlerRejectsCumulativeEnvelopeOverflowBeforeEventMutation(t *testing.T) {
	messageStart := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_limit\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":1}}}\n\n"
	delta := "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"must-not-commit\"}}\n\n"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	response := &http.Response{Body: io.NopCloser(strings.NewReader(messageStart + delta))}
	info := &relaycommon.RelayInfo{
		IsStream:                   true,
		RelayFormat:                types.RelayFormatClaude,
		MaxStreamResponseBytes:     int64(len(messageStart)),
		AuthorizedPromptTokens:     100,
		AuthorizedCompletionTokens: 100,
		ChannelMeta: &relaycommon.ChannelMeta{
			ManagedProvider:   true,
			UpstreamModelName: "claude-sonnet-4-6",
		},
	}

	usage, relayErr := ClaudeStreamHandler(context, response, info)

	require.NotNil(t, relayErr)
	require.Equal(t, types.ErrorCodeBadResponse, relayErr.GetErrorCode())
	require.Equal(t, http.StatusBadGateway, relayErr.StatusCode)
	require.NotContains(t, relayErr.Error(), "must-not-commit")
	require.NotContains(t, recorder.Body.String(), "must-not-commit")
	require.NotNil(t, usage)
	require.Zero(t, usage.CompletionTokens)
}

func TestClaudeStreamHandlerAllowsExactCumulativeEnvelopeBoundary(t *testing.T) {
	body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_boundary\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":1}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"at-boundary\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\ndata: [DONE]\n"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	response := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	info := &relaycommon.RelayInfo{
		IsStream:                   true,
		RelayFormat:                types.RelayFormatClaude,
		MaxStreamResponseBytes:     int64(len(body)),
		AuthorizedPromptTokens:     100,
		AuthorizedCompletionTokens: 100,
		ChannelMeta: &relaycommon.ChannelMeta{
			ManagedProvider:   true,
			UpstreamModelName: "claude-sonnet-4-6",
		},
	}

	usage, relayErr := ClaudeStreamHandler(context, response, info)

	require.Nil(t, relayErr)
	require.NotNil(t, usage)
	require.Equal(t, 1, usage.PromptTokens)
	require.Equal(t, 1, usage.CompletionTokens)
	require.Contains(t, recorder.Body.String(), "at-boundary")
	require.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
}

func TestClaudeOutputEvidenceTokenBoundary(t *testing.T) {
	require.NoError(t, validateClaudeOutputEvidenceUsage(128, 1))
	require.Error(t, validateClaudeOutputEvidenceUsage(129, 1))
	require.NoError(t, validateClaudeOutputEvidenceUsage(129, 2))
}

func TestClaudeStreamBillableEvidenceCoversSemanticOutput(t *testing.T) {
	toolInput := map[string]any{"city": "杭州"}
	toolInputJSON, err := common.Marshal(toolInput)
	require.NoError(t, err)
	text := strings.Repeat("a", 129)
	thinking := "思考"
	partialJSON := `{"city":"杭州"}`

	for _, test := range []struct {
		name     string
		response *dto.ClaudeResponse
		want     string
	}{
		{
			name: "repeated ASCII text",
			response: &dto.ClaudeResponse{Delta: &dto.ClaudeMediaMessage{
				Type: "text_delta", Text: &text,
			}},
			want: text,
		},
		{
			name: "multibyte thinking",
			response: &dto.ClaudeResponse{Delta: &dto.ClaudeMediaMessage{
				Type: "thinking_delta", Thinking: &thinking,
			}},
			want: thinking,
		},
		{
			name: "partial tool JSON",
			response: &dto.ClaudeResponse{Delta: &dto.ClaudeMediaMessage{
				Type: "input_json_delta", PartialJson: &partialJSON,
			}},
			want: partialJSON,
		},
		{
			name: "tool name and initial input",
			response: &dto.ClaudeResponse{ContentBlock: &dto.ClaudeMediaMessage{
				Type: "tool_use", Name: "weather", Input: toolInput,
			}},
			want: "weather" + string(toolInputJSON),
		},
		{
			name: "redacted thinking",
			response: &dto.ClaudeResponse{ContentBlock: &dto.ClaudeMediaMessage{
				Type: "redacted_thinking", Data: "encrypted-thinking",
			}},
			want: "encrypted-thinking",
		},
		{
			name: "signature is not billable",
			response: &dto.ClaudeResponse{Delta: &dto.ClaudeMediaMessage{
				Type: "signature_delta", Signature: strings.Repeat("s", 1024),
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence, evidenceErr := claudeStreamBillableEvidence(test.response)

			require.NoError(t, evidenceErr)
			require.Equal(t, test.want, evidence.text)
			require.Equal(t, int64(len(test.want)), evidence.bytes)
		})
	}
}

func TestClaudeNonStreamBillableEvidenceCoversContentKinds(t *testing.T) {
	text := "正文"
	thinking := "analysis"
	toolInput := map[string]any{"episode": 1}
	toolInputJSON, err := common.Marshal(toolInput)
	require.NoError(t, err)
	response := &dto.ClaudeResponse{Content: []dto.ClaudeMediaMessage{
		{Type: "text", Text: &text},
		{Type: "thinking", Thinking: &thinking, Signature: strings.Repeat("s", 512)},
		{Type: "tool_use", Name: "write_scene", Input: toolInput},
		{Type: "redacted_thinking", Data: "redacted-data"},
	}}
	want := text + thinking + "write_scene" + string(toolInputJSON) + "redacted-data"

	evidence, evidenceErr := claudeNonStreamBillableEvidence(response)

	require.NoError(t, evidenceErr)
	require.Equal(t, want, evidence.text)
	require.Equal(t, int64(len(want)), evidence.bytes)
}

func TestClaudeInputEvidenceUsageBoundaryAndCacheSum(t *testing.T) {
	info := &relaycommon.RelayInfo{
		ClaudeInputEvidenceBytes: 128,
		ChannelMeta:              &relaycommon.ChannelMeta{ManagedProvider: true},
	}
	require.NoError(t, validateClaudeInputEvidenceUsage(info, &dto.ClaudeUsage{InputTokens: 1}))

	info.ClaudeInputEvidenceBytes = 129
	require.Error(t, validateClaudeInputEvidenceUsage(info, &dto.ClaudeUsage{InputTokens: 1}))
	require.NoError(t, validateClaudeInputEvidenceUsage(info, &dto.ClaudeUsage{CacheReadInputTokens: 2}))

	info.ClaudeInputEvidenceBytes = 257
	require.NoError(t, validateClaudeInputEvidenceUsage(info, &dto.ClaudeUsage{
		InputTokens: 1, CacheReadInputTokens: 1, CacheCreationInputTokens: 1,
	}))
}

func TestClaudeManagedStreamUnderreportedOutputUsesConservativeFailedUsage(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat:            types.RelayFormatClaude,
		AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 2,
		ChannelMeta: &relaycommon.ChannelMeta{
			ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6",
		},
	}
	claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}
	content := strings.Repeat("a", 129)
	events := []string{
		`{"type":"message_start","message":{"id":"msg_integrity","model":"claude-sonnet-4-6","usage":{"input_tokens":1}}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"` + content + `"}}`,
	}
	for _, event := range events {
		require.Nil(t, HandleStreamResponseData(context, info, claudeInfo, event))
	}
	bodyBeforeTerminal := recorder.Body.String()

	relayErr := HandleStreamResponseData(context, info, claudeInfo,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)

	require.NotNil(t, relayErr)
	require.Equal(t, types.ErrorCodeBadResponseBody, relayErr.GetErrorCode())
	require.False(t, claudeInfo.Done)
	require.False(t, claudeInfo.SawTerminalUsage)
	require.Equal(t, 1, claudeInfo.FailedReportedCompletionTokens)
	require.Equal(t, bodyBeforeTerminal, recorder.Body.String(), "terminal event must not be written")
	failedUsage := failedClaudeStreamUsage(context, info, claudeInfo)
	require.NotNil(t, failedUsage)
	require.Equal(t, 2, failedUsage.CompletionTokens, "failed usage must be conservative and capped by authorization")
}

func TestClaudeManagedRejectedTerminalEvidenceIsNotCommitted(t *testing.T) {
	stopReason := "end_turn"
	terminalText := strings.Repeat("x", 129)
	terminalEvent, err := common.Marshal(dto.ClaudeResponse{
		Type: "message_delta",
		Delta: &dto.ClaudeMediaMessage{
			StopReason: &stopReason,
			Text:       &terminalText,
		},
		Usage: &dto.ClaudeUsage{OutputTokens: 1},
	})
	require.NoError(t, err)

	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat:            relayFormat,
				AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
				ChannelMeta: &relaycommon.ChannelMeta{
					ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6",
				},
			}
			claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}
			require.Nil(t, HandleStreamResponseData(context, info, claudeInfo,
				`{"type":"message_start","message":{"id":"msg_terminal","model":"claude-sonnet-4-6","usage":{"input_tokens":1}}}`))
			require.Nil(t, HandleStreamResponseData(context, info, claudeInfo,
				`{"type":"content_block_delta","delta":{"type":"text_delta","text":"sent"}}`))
			bodyBeforeTerminal := recorder.Body.String()

			relayErr := HandleStreamResponseData(context, info, claudeInfo, string(terminalEvent))

			require.NotNil(t, relayErr)
			require.Equal(t, bodyBeforeTerminal, recorder.Body.String())
			require.Equal(t, int64(len("sent")), claudeInfo.UpstreamBillableOutputBytes)
			require.Equal(t, int64(len("sent")), claudeInfo.BillableOutputBytes)
			require.Equal(t, "sent", claudeInfo.BillableOutputText.String())
			failedUsage := failedClaudeStreamUsage(context, info, claudeInfo)
			require.NotNil(t, failedUsage)
			// The surviving English word is estimated as ceil(1.13) tokens.
			require.Equal(t, 2, failedUsage.CompletionTokens)
		})
	}
}

func TestClaudeManagedStreamKeepsValidReportedCompletionUsage(t *testing.T) {
	content := strings.Repeat("a", 129)
	body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_usage\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":1}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" + content + "\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\ndata: [DONE]\n"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		IsStream: true, RelayFormat: types.RelayFormatClaude,
		AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
		ChannelMeta: &relaycommon.ChannelMeta{
			ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6",
		},
	}

	usage, relayErr := ClaudeStreamHandler(context, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, info)

	require.Nil(t, relayErr)
	require.NotNil(t, usage)
	require.Equal(t, 2, usage.CompletionTokens)
}

func TestClaudeManagedStreamRejectsUnderreportedInputBeforeFirstWrite(t *testing.T) {
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat: relayFormat, ClaudeInputEvidenceBytes: 129,
				AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
				ChannelMeta: &relaycommon.ChannelMeta{
					ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6",
				},
			}
			claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}

			relayErr := HandleStreamResponseData(context, info, claudeInfo,
				`{"type":"message_start","message":{"id":"msg_input","model":"claude-sonnet-4-6","usage":{"input_tokens":1}}}`)

			require.NotNil(t, relayErr)
			require.Equal(t, types.ErrorCodeBadResponseBody, relayErr.GetErrorCode())
			require.False(t, claudeInfo.SawMessageStart)
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestClaudeManagedNonStreamRejectsUnderreportedEvidenceWithoutWriting(t *testing.T) {
	text := strings.Repeat("a", 129)
	responseBody, err := common.Marshal(dto.ClaudeResponse{
		Type: "message", Model: "claude-sonnet-4-6", StopReason: "end_turn",
		Content: []dto.ClaudeMediaMessage{{Type: "text", Text: &text}},
		Usage:   &dto.ClaudeUsage{InputTokens: 1, OutputTokens: 1},
	})
	require.NoError(t, err)

	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat: relayFormat, ClaudeInputEvidenceBytes: 128,
				AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
				ChannelMeta: &relaycommon.ChannelMeta{
					ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6",
				},
			}
			claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}

			relayErr := HandleClaudeResponseData(context, info, claudeInfo, &http.Response{}, responseBody)

			require.NotNil(t, relayErr)
			require.Equal(t, types.ErrorCodeBadResponseBody, relayErr.GetErrorCode())
			require.Empty(t, recorder.Body.String())
			require.Zero(t, claudeInfo.BillableOutputBytes)
		})
	}
}

func TestClaudeManagedStreamBlocksAPIKeyBeforeCurrentChunkWrite(t *testing.T) {
	const apiKey = "sk-secret"
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat:            relayFormat,
				AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
				ChannelMeta: &relaycommon.ChannelMeta{
					ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6", ApiKey: "  " + apiKey + "  ",
				},
			}
			claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}
			require.Nil(t, HandleStreamResponseData(context, info, claudeInfo,
				`{"type":"message_start","message":{"id":"msg_secret","model":"claude-sonnet-4-6","usage":{"input_tokens":1}}}`))
			bodyBeforeLeak := recorder.Body.String()

			relayErr := HandleStreamResponseData(context, info, claudeInfo,
				`{"type":"content_block_delta","delta":{"type":"text_delta","text":"sk-\u0073ecret"}}`)

			require.NotNil(t, relayErr)
			require.Equal(t, types.ErrorCodeBadResponseBody, relayErr.GetErrorCode())
			require.NotContains(t, relayErr.Error(), apiKey)
			require.Equal(t, bodyBeforeLeak, recorder.Body.String())
			require.NotContains(t, recorder.Body.String(), apiKey)
		})
	}
}

func TestClaudeManagedStreamBlocksAPIKeySplitAcrossWrittenDeltas(t *testing.T) {
	const (
		apiKey = "provider-secret-key"
		prefix = "provider-secret-"
		suffix = "key"
	)
	marshalDelta := func(t *testing.T, delta dto.ClaudeMediaMessage) string {
		t.Helper()
		data, err := common.Marshal(dto.ClaudeResponse{Type: "content_block_delta", Delta: &delta})
		require.NoError(t, err)
		return string(data)
	}
	firstToolJSON := `{"credential":"` + prefix
	secondToolJSON := suffix + `"}`
	fragments := []struct {
		name   string
		first  dto.ClaudeMediaMessage
		second dto.ClaudeMediaMessage
	}{
		{
			name:   "text",
			first:  dto.ClaudeMediaMessage{Type: "text_delta", Text: common.GetPointer(prefix)},
			second: dto.ClaudeMediaMessage{Type: "text_delta", Text: common.GetPointer(suffix)},
		},
		{
			name:   "thinking",
			first:  dto.ClaudeMediaMessage{Type: "thinking_delta", Thinking: common.GetPointer(prefix)},
			second: dto.ClaudeMediaMessage{Type: "thinking_delta", Thinking: common.GetPointer(suffix)},
		},
		{
			name:   "tool JSON",
			first:  dto.ClaudeMediaMessage{Type: "input_json_delta", PartialJson: &firstToolJSON},
			second: dto.ClaudeMediaMessage{Type: "input_json_delta", PartialJson: &secondToolJSON},
		},
	}
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		for _, fragment := range fragments {
			t.Run(string(relayFormat)+"/"+fragment.name, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				context, _ := gin.CreateTestContext(recorder)
				context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				info := &relaycommon.RelayInfo{
					RelayFormat:            relayFormat,
					AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
					ChannelMeta: &relaycommon.ChannelMeta{
						ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6", ApiKey: apiKey,
					},
				}
				claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}
				require.Nil(t, HandleStreamResponseData(context, info, claudeInfo,
					`{"type":"message_start","message":{"id":"msg_split","model":"claude-sonnet-4-6","usage":{"input_tokens":1}}}`))
				require.Nil(t, HandleStreamResponseData(context, info, claudeInfo, marshalDelta(t, fragment.first)))
				bodyBeforeSecondFragment := recorder.Body.String()

				relayErr := HandleStreamResponseData(context, info, claudeInfo, marshalDelta(t, fragment.second))

				require.NotNil(t, relayErr)
				require.Equal(t, types.ErrorCodeBadResponseBody, relayErr.GetErrorCode())
				require.NotContains(t, relayErr.Error(), apiKey)
				require.Equal(t, bodyBeforeSecondFragment, recorder.Body.String())
				require.NotContains(t, recorder.Body.String(), apiKey)
				require.Equal(t, prefix, claudeInfo.APIKeyOutputSuffix)
				partialUsage := failedClaudeStreamUsage(context, info, claudeInfo)
				require.NotNil(t, partialUsage)
				require.Positive(t, partialUsage.CompletionTokens)
			})
		}
	}
}

func TestClaudeManagedStreamCountsMessageStartContentBeforeTerminalUsage(t *testing.T) {
	content := strings.Repeat("m", 4096)
	messageStart, err := common.Marshal(dto.ClaudeResponse{
		Type: "message_start",
		Message: &dto.ClaudeMediaMessage{
			Id: "msg_start_content", Model: "claude-sonnet-4-6",
			Usage:   &dto.ClaudeUsage{InputTokens: 1},
			Content: []dto.ClaudeMediaMessage{{Type: "text", Text: &content}},
		},
	})
	require.NoError(t, err)
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat:            relayFormat,
				AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
				ChannelMeta: &relaycommon.ChannelMeta{
					ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6",
				},
			}
			claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}
			require.Nil(t, HandleStreamResponseData(context, info, claudeInfo, string(messageStart)))
			require.Equal(t, int64(len(content)), claudeInfo.UpstreamBillableOutputBytes)
			if relayFormat == types.RelayFormatClaude {
				require.Equal(t, int64(len(content)), claudeInfo.BillableOutputBytes)
			} else {
				require.Zero(t, claudeInfo.BillableOutputBytes)
			}
			bodyBeforeTerminal := recorder.Body.String()

			relayErr := HandleStreamResponseData(context, info, claudeInfo,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)

			require.NotNil(t, relayErr)
			require.Equal(t, types.ErrorCodeBadResponseBody, relayErr.GetErrorCode())
			require.False(t, claudeInfo.Done)
			require.Equal(t, bodyBeforeTerminal, recorder.Body.String())
			failedUsage := failedClaudeStreamUsage(context, info, claudeInfo)
			require.NotNil(t, failedUsage)
			if relayFormat == types.RelayFormatOpenAI {
				require.Equal(t, 1, failedUsage.CompletionTokens, "unseen message_start content must not be charged")
			} else {
				require.Greater(t, failedUsage.CompletionTokens, 1)
			}
		})
	}
}

func TestClaudeManagedNonStreamBlocksAPIKeyWithoutWriting(t *testing.T) {
	const apiKey = "sk-secret"
	responseBody := []byte(`{"id":"msg_secret","type":"message","model":"claude-sonnet-4-6","stop_reason":"end_turn","content":[{"type":"text","text":"sk-\u0073ecret"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat:            relayFormat,
				AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
				ChannelMeta: &relaycommon.ChannelMeta{
					ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6", ApiKey: " " + apiKey + " ",
				},
			}

			relayErr := HandleClaudeResponseData(context, info, &ClaudeResponseInfo{Usage: &dto.Usage{}}, &http.Response{}, responseBody)

			require.NotNil(t, relayErr)
			require.Equal(t, types.ErrorCodeBadResponseBody, relayErr.GetErrorCode())
			require.NotContains(t, relayErr.Error(), apiKey)
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestClaudeManagedNonStreamBlocksAPIKeySplitAcrossContentBlocks(t *testing.T) {
	const apiKey = "provider-secret-key"
	first, second := "provider-secret-", "key"
	responseBody, err := common.Marshal(dto.ClaudeResponse{
		Id: "msg_split", Type: "message", Model: "claude-sonnet-4-6", StopReason: "end_turn",
		Content: []dto.ClaudeMediaMessage{
			{Type: "text", Text: &first},
			{Type: "text", Text: &second},
		},
		Usage: &dto.ClaudeUsage{InputTokens: 1, OutputTokens: 1},
	})
	require.NoError(t, err)
	require.NotContains(t, string(responseBody), apiKey, "raw JSON deliberately separates the key")

	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat:            relayFormat,
				AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
				ChannelMeta: &relaycommon.ChannelMeta{
					ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6", ApiKey: apiKey,
				},
			}

			relayErr := HandleClaudeResponseData(context, info, &ClaudeResponseInfo{Usage: &dto.Usage{}}, &http.Response{}, responseBody)

			require.NotNil(t, relayErr)
			require.Equal(t, types.ErrorCodeBadResponseBody, relayErr.GetErrorCode())
			require.NotContains(t, relayErr.Error(), apiKey)
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestClaudeManagedNativeNonStreamBlocksAPIKeySplitAcrossSignatures(t *testing.T) {
	const apiKey = "provider-secret-key"
	responseBody, err := common.Marshal(dto.ClaudeResponse{
		Id: "msg_signature", Type: "message", Model: "claude-sonnet-4-6", StopReason: "end_turn",
		Content: []dto.ClaudeMediaMessage{
			{Type: "thinking", Signature: "provider-secret-"},
			{Type: "thinking", Signature: "key"},
		},
		Usage: &dto.ClaudeUsage{InputTokens: 1, OutputTokens: 1},
	})
	require.NoError(t, err)
	require.NotContains(t, string(responseBody), apiKey, "raw JSON deliberately separates the key")
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat:            types.RelayFormatClaude,
		AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
		ChannelMeta: &relaycommon.ChannelMeta{
			ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6", ApiKey: apiKey,
		},
	}

	relayErr := HandleClaudeResponseData(context, info, &ClaudeResponseInfo{Usage: &dto.Usage{}}, &http.Response{}, responseBody)

	require.NotNil(t, relayErr)
	require.Equal(t, types.ErrorCodeBadResponseBody, relayErr.GetErrorCode())
	require.NotContains(t, relayErr.Error(), apiKey)
	require.Empty(t, recorder.Body.String())
}

func TestClaudeStreamHandlerRejectsPrematureEOFWithoutSyntheticDone(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantWritten bool
		wantUsage   bool
	}{
		{name: "before first response byte", body: ""},
		{
			name: "after partial content",
			body: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_partial\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n",
			wantWritten: true,
			wantUsage:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			response := &http.Response{Body: io.NopCloser(strings.NewReader(test.body))}
			info := &relaycommon.RelayInfo{
				IsStream:    true,
				RelayFormat: types.RelayFormatOpenAI,
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
			}

			usage, relayErr := ClaudeStreamHandler(context, response, info)

			if test.wantUsage {
				require.NotNil(t, usage)
				require.Equal(t, 3, usage.PromptTokens)
				require.Positive(t, usage.CompletionTokens)
				require.Equal(t, "anthropic", usage.UsageSemantic)
			} else {
				require.Nil(t, usage)
			}
			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "完成事件前中断")
			require.Equal(t, types.ErrorCodeBadResponse, relayErr.GetErrorCode())
			require.Equal(t, http.StatusBadGateway, relayErr.StatusCode)
			require.Equal(t, test.wantWritten, recorder.Body.Len() > 0)
			require.NotContains(t, recorder.Body.String(), "[DONE]")
		})
	}
}

func TestClaudeStreamHandlerRejectsTransportDoneWithoutSemanticCompletion(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "partial content then done",
			body: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_partial\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial output\"}}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name: "usage-only message delta then done",
			body: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_malformed\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
				"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name: "empty stop reason then done",
			body: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_empty_stop\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\" \"},\"usage\":{\"output_tokens\":2}}\n\n" +
				"data: [DONE]\n\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			response := &http.Response{Body: io.NopCloser(strings.NewReader(test.body))}
			info := &relaycommon.RelayInfo{
				IsStream:    true,
				RelayFormat: types.RelayFormatOpenAI,
				ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
			}

			usage, relayErr := ClaudeStreamHandler(context, response, info)

			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "完成事件前中断")
			require.NotNil(t, usage)
			require.Equal(t, 3, usage.PromptTokens)
			require.Equal(t, "anthropic", usage.UsageSemantic)
			require.NotContains(t, recorder.Body.String(), "data: [DONE]")
		})
	}
}

func TestClaudeStreamHandlerReturnsPartialUsageOnUpstreamError(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_partial_error\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":7}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"a partial response before failure\"}}\n\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"upstream interrupted\"}}\n\n"
	response := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
	}

	usage, relayErr := ClaudeStreamHandler(context, response, info)

	require.NotNil(t, relayErr)
	require.NotNil(t, usage)
	require.Equal(t, 7, usage.PromptTokens)
	require.Positive(t, usage.CompletionTokens)
	require.Equal(t, usage.PromptTokens+usage.CompletionTokens, usage.TotalTokens)
	require.Equal(t, "anthropic", usage.UsageSemantic)
	require.NotContains(t, recorder.Body.String(), "[DONE]")
}

func TestClaudeStreamHandlerSanitizesHTTP200Error(t *testing.T) {
	const secret = "provider-secret-key"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(context, constant.ContextKeyChannelKey, secret)
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		"data: {\"type\":\"error\",\"error\":{\"type\":\"authentication_provider-secret-key\",\"message\":\"invalid provider-secret-key\"}}\n\n",
	))}
	info := &relaycommon.RelayInfo{
		IsStream:    true,
		RelayFormat: types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
	}

	usage, relayErr := ClaudeStreamHandler(context, response, info)

	require.Nil(t, usage)
	require.NotNil(t, relayErr)
	require.Contains(t, relayErr.Error(), "invalid ***")
	require.NotContains(t, relayErr.Error(), secret)
	require.NotContains(t, string(relayErr.GetErrorCode()), secret)
	clientError := relayErr.ToClaudeError()
	require.NotContains(t, clientError.Type, secret)
	require.NotContains(t, clientError.Message, secret)
	require.Empty(t, recorder.Body.String())
}

func TestClaudeHandlerSanitizesHTTP200Error(t *testing.T) {
	const secret = "provider-secret-key"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	common.SetContextKey(context, constant.ContextKeyChannelKey, secret)
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
		`{"type":"error","error":{"type":"authentication_provider-secret-key","message":"invalid provider-secret-key"}}`,
	))}
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
	}

	usage, relayErr := ClaudeHandler(context, response, info)

	require.Nil(t, usage)
	require.NotNil(t, relayErr)
	require.Contains(t, relayErr.Error(), "invalid ***")
	require.NotContains(t, relayErr.Error(), secret)
	require.NotContains(t, string(relayErr.GetErrorCode()), secret)
	clientError := relayErr.ToClaudeError()
	require.NotContains(t, clientError.Type, secret)
	require.NotContains(t, clientError.Message, secret)
	require.Empty(t, recorder.Body.String())
}

func TestClaudeHandlerRequiresUsageBeforeWritingManagedResponse(t *testing.T) {
	for _, test := range []struct {
		name    string
		usage   string
		managed bool
		wantErr bool
	}{
		{name: "managed missing usage", managed: true, wantErr: true},
		{name: "managed empty usage", usage: `,"usage":{}`, managed: true, wantErr: true},
		{name: "managed negative usage", usage: `,"usage":{"input_tokens":-1}`, managed: true, wantErr: true},
		{name: "managed zero output usage", usage: `,"usage":{"input_tokens":1,"output_tokens":0}`, managed: true, wantErr: true},
		{name: "managed valid usage", usage: `,"usage":{"input_tokens":1,"output_tokens":1}`, managed: true},
		{name: "ordinary missing usage", managed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4-6","stop_reason":"end_turn"` + test.usage + `}`
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
			info := &relaycommon.RelayInfo{
				RelayFormat:                types.RelayFormatClaude,
				AuthorizedPromptTokens:     4096,
				AuthorizedCompletionTokens: 4096,
				ChannelMeta:                &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6", ManagedProvider: test.managed},
			}

			usage, relayErr := ClaudeHandler(context, response, info)

			if test.wantErr {
				require.Nil(t, usage)
				require.NotNil(t, relayErr)
				require.Empty(t, recorder.Body.String())
				return
			}
			require.Nil(t, relayErr)
			require.NotNil(t, usage)
			require.JSONEq(t, body, recorder.Body.String())
		})
	}
}

func TestClaudeManagedStreamRequiresValidUsageBeforeFirstWrite(t *testing.T) {
	for _, test := range []struct {
		name  string
		usage string
	}{
		{name: "missing usage"},
		{name: "empty usage", usage: `,"usage":{}`},
		{name: "negative usage", usage: `,"usage":{"input_tokens":-1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := `data: {"type":"message_start","message":{"id":"msg_invalid","model":"claude-sonnet-4-6"` + test.usage + `}}` + "\n\n" +
				`data: {"type":"message_stop"}` + "\n\n"
			response := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: types.RelayFormatClaude,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(context, response, info)

			require.Nil(t, usage)
			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "usage")
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestClaudeManagedStreamRequiresUsageAtSemanticCompletion(t *testing.T) {
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			response := &http.Response{Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_no_terminal_usage\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
					"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n" +
					"data: {\"type\":\"message_stop\"}\n\n",
			))}
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: relayFormat,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(context, response, info)

			require.NotNil(t, usage)
			require.Equal(t, 3, usage.PromptTokens)
			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "terminal usage")
			require.NotEmpty(t, recorder.Body.String())
			require.NotContains(t, recorder.Body.String(), "message_stop")
			require.NotContains(t, recorder.Body.String(), "[DONE]")
		})
	}
}

func TestClaudeManagedStreamAcceptsAttestedTerminalUsage(t *testing.T) {
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_attested\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"complete\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n"
			response := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: relayFormat,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(context, response, info)

			require.Nil(t, relayErr)
			require.NotNil(t, usage)
			require.Equal(t, 3, usage.PromptTokens)
			require.Equal(t, 2, usage.CompletionTokens)
			if relayFormat == types.RelayFormatOpenAI {
				require.Contains(t, recorder.Body.String(), "[DONE]")
			} else {
				require.Contains(t, recorder.Body.String(), "message_stop")
			}
		})
	}
}

func TestClaudeManagedNonStreamRejectsUsageOutsideAuthorizationBeforeWrite(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, test := range []struct {
		name  string
		usage string
		want  string
	}{
		{name: "input", usage: `{"input_tokens":101,"output_tokens":1}`, want: "input usage"},
		{name: "output", usage: `{"input_tokens":1,"output_tokens":11}`, want: "output usage"},
		{name: "cache aggregate", usage: `{"input_tokens":20,"cache_read_input_tokens":50,"cache_creation_input_tokens":40,"output_tokens":1}`, want: "input usage"},
		{name: "cache split mismatch", usage: `{"input_tokens":1,"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":50,"ephemeral_1h_input_tokens":25},"output_tokens":1}`, want: "inconsistent cache"},
		{name: "usage overflow", usage: fmt.Sprintf(`{"input_tokens":%d,"cache_read_input_tokens":1,"output_tokens":1}`, maxInt), want: "overflowed"},
		{name: "unpriced web search", usage: `{"input_tokens":1,"output_tokens":1,"server_tool_use":{"web_search_requests":1}}`, want: "web_search"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat:            types.RelayFormatClaude,
				AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 10,
				PriceData:   types.PriceData{ChannelSpecific: true, PriceProvider: types.PriceProviderZenMux},
				ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6"},
			}
			body := []byte(`{"id":"msg_bound","type":"message","role":"assistant","content":[{"type":"text","text":"blocked"}],"stop_reason":"end_turn","model":"claude-sonnet-4-6","usage":` + test.usage + `}`)

			relayErr := HandleClaudeResponseData(context, info, &ClaudeResponseInfo{Usage: &dto.Usage{}}, &http.Response{}, body)

			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, test.want)
			require.False(t, context.Writer.Written())
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestClaudeStrictNonStreamStagesValidatedResponseUntilBillingCommits(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	info := &relaycommon.RelayInfo{
		RelayFormat:                types.RelayFormatClaude,
		ForcePreConsume:            true,
		AuthorizedPromptTokens:     100,
		AuthorizedCompletionTokens: 10,
		PriceData: types.PriceData{
			ChannelSpecific: true,
			PriceProvider:   types.PriceProviderZenMux,
		},
		ChannelMeta: &relaycommon.ChannelMeta{
			ManagedProvider:   true,
			UpstreamModelName: "claude-sonnet-4-6",
		},
	}
	body := []byte(`{"id":"msg_staged","type":"message","role":"assistant","content":[{"type":"text","text":"ready"}],"stop_reason":"end_turn","model":"claude-sonnet-4-6","usage":{"input_tokens":3,"output_tokens":2}}`)

	relayErr := HandleClaudeResponseData(
		context,
		info,
		&ClaudeResponseInfo{Usage: &dto.Usage{}},
		&http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}},
		body,
	)

	require.Nil(t, relayErr)
	require.False(t, context.Writer.Written(), "strict success must remain behind the settlement boundary")
	require.Empty(t, recorder.Body.String())
	require.True(t, service.FlushStagedResponseBytes(context))
	require.Equal(t, string(body), recorder.Body.String())
}

func TestClaudeStrictProvidersRejectUnpricedWebSearchContentBeforeWrite(t *testing.T) {
	blocks := []string{
		`{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"test"}}`,
		`{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[]}`,
	}
	for _, provider := range []string{types.PriceProviderZenMux, types.PriceProviderTabCode} {
		for index, block := range blocks {
			t.Run(fmt.Sprintf("non-stream/%s/%d", provider, index), func(t *testing.T) {
				recorder := httptest.NewRecorder()
				context, _ := gin.CreateTestContext(recorder)
				context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				info := &relaycommon.RelayInfo{
					RelayFormat:            types.RelayFormatClaude,
					AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 10,
					PriceData:   types.PriceData{ChannelSpecific: true, PriceProvider: provider},
					ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6"},
				}
				body := []byte(`{"id":"msg_search","type":"message","role":"assistant","content":[` + block + `],"stop_reason":"end_turn","model":"claude-sonnet-4-6","usage":{"input_tokens":1,"output_tokens":1}}`)

				relayErr := HandleClaudeResponseData(context, info, &ClaudeResponseInfo{Usage: &dto.Usage{}}, &http.Response{}, body)

				require.NotNil(t, relayErr)
				require.ErrorContains(t, relayErr, "web_search")
				require.False(t, context.Writer.Written())
				require.Empty(t, recorder.Body.String())
			})

			t.Run(fmt.Sprintf("stream/%s/%d", provider, index), func(t *testing.T) {
				recorder := httptest.NewRecorder()
				context, _ := gin.CreateTestContext(recorder)
				context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				info := &relaycommon.RelayInfo{
					RelayFormat:            types.RelayFormatClaude,
					AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 10,
					PriceData:   types.PriceData{ChannelSpecific: true, PriceProvider: provider},
					ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6"},
				}
				claudeInfo := &ClaudeResponseInfo{
					Usage: &dto.Usage{PromptTokens: 1}, SawMessageStart: true, SawValidInputUsage: true,
				}

				relayErr := HandleStreamResponseData(context, info, claudeInfo,
					`{"type":"content_block_start","index":0,"content_block":`+block+`}`)

				require.NotNil(t, relayErr)
				require.ErrorContains(t, relayErr, "web_search")
				require.False(t, context.Writer.Written())
				require.Equal(t, 1, claudeInfo.Usage.PromptTokens)
			})

			t.Run(fmt.Sprintf("message-start-content/%s/%d", provider, index), func(t *testing.T) {
				recorder := httptest.NewRecorder()
				context, _ := gin.CreateTestContext(recorder)
				context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				info := &relaycommon.RelayInfo{
					RelayFormat:            types.RelayFormatClaude,
					AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 10,
					PriceData:   types.PriceData{ChannelSpecific: true, PriceProvider: provider},
					ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6"},
				}
				claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}

				relayErr := HandleStreamResponseData(context, info, claudeInfo,
					`{"type":"message_start","message":{"id":"msg_search","model":"claude-sonnet-4-6","content":[`+block+`],"usage":{"input_tokens":1}}}`)

				require.NotNil(t, relayErr)
				require.ErrorContains(t, relayErr, "web_search")
				require.False(t, context.Writer.Written())
				require.False(t, claudeInfo.SawMessageStart)
			})
		}
	}
}

func TestClaudeManagedNonStreamRejectsUnexpectedModelBeforeWrite(t *testing.T) {
	for _, modelName := range []string{"", "claude-opus-5"} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		info := &relaycommon.RelayInfo{
			RelayFormat:            types.RelayFormatClaude,
			AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 10,
			PriceData:   types.PriceData{ChannelSpecific: true, PriceProvider: types.PriceProviderZenMux},
			ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "anthropic/claude-sonnet-4.6"},
		}
		body := []byte(fmt.Sprintf(
			`{"id":"msg_model","type":"message","role":"assistant","content":[{"type":"text","text":"blocked"}],"stop_reason":"end_turn","model":%q,"usage":{"input_tokens":1,"output_tokens":1}}`,
			modelName,
		))

		relayErr := HandleClaudeResponseData(context, info, &ClaudeResponseInfo{Usage: &dto.Usage{}}, &http.Response{}, body)

		require.NotNil(t, relayErr)
		require.ErrorContains(t, relayErr, "response model")
		require.False(t, context.Writer.Written())
	}
}

func TestClaudeManagedStreamRejectsUnexpectedMessageStartModelBeforeWrite(t *testing.T) {
	for _, modelName := range []string{"", "anthropic/claude-opus-5"} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		info := &relaycommon.RelayInfo{
			RelayFormat:            types.RelayFormatClaude,
			AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 10,
			PriceData:   types.PriceData{ChannelSpecific: true, PriceProvider: types.PriceProviderZenMux},
			ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "anthropic/claude-sonnet-4.6"},
		}
		claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}
		data := fmt.Sprintf(
			`{"type":"message_start","message":{"id":"msg_model","model":%q,"usage":{"input_tokens":1}}}`,
			modelName,
		)

		relayErr := HandleStreamResponseData(context, info, claudeInfo, data)

		require.NotNil(t, relayErr)
		require.ErrorContains(t, relayErr, "response model")
		require.False(t, context.Writer.Written())
		require.False(t, claudeInfo.SawMessageStart)
	}
}

func TestClaudeManagedStreamRejectsCrossEventUsageOverflowWithoutCommittingIt(t *testing.T) {
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat:            relayFormat,
				AuthorizedPromptTokens: 500, AuthorizedCompletionTokens: 10,
				PriceData:   types.PriceData{ChannelSpecific: true, PriceProvider: types.PriceProviderZenMux},
				ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6"},
			}
			claudeInfo := &ClaudeResponseInfo{Usage: &dto.Usage{}}

			startErr := HandleStreamResponseData(context, info, claudeInfo,
				`{"type":"message_start","message":{"id":"msg_cache_bound","model":"claude-sonnet-4-6","usage":{"cache_read_input_tokens":400}}}`)
			require.Nil(t, startErr)
			require.Equal(t, 400, claudeInfo.Usage.PromptTokensDetails.CachedTokens)

			relayErr := HandleStreamResponseData(context, info, claudeInfo,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"cache_creation_input_tokens":400,"output_tokens":1}}`)

			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "input usage")
			require.Equal(t, 400, claudeInfo.Usage.PromptTokensDetails.CachedTokens)
			require.Zero(t, claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens)
			require.Zero(t, claudeInfo.Usage.CompletionTokens)
			require.False(t, claudeInfo.Done)
		})
	}
}

func TestClaudeManagedStreamRejectsOutputAboveAuthorizationWithoutTerminalWrite(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_output_bound\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":11}}\n\n"
	info := &relaycommon.RelayInfo{
		IsStream: true, RelayFormat: types.RelayFormatClaude,
		AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 10,
		PriceData:   types.PriceData{ChannelSpecific: true, PriceProvider: types.PriceProviderZenMux},
		ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6"},
	}

	usage, relayErr := ClaudeStreamHandler(context, &http.Response{Body: io.NopCloser(strings.NewReader(body))}, info)

	require.NotNil(t, relayErr)
	require.ErrorContains(t, relayErr, "output usage")
	require.NotNil(t, usage)
	require.LessOrEqual(t, usage.CompletionTokens, 10)
	require.Contains(t, recorder.Body.String(), "partial")
	require.NotContains(t, recorder.Body.String(), "output_tokens\":11")
	require.NotContains(t, recorder.Body.String(), "message_stop")
}

func TestClaudeManagedStreamForwardsHeartbeatAfterValidStart(t *testing.T) {
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_heartbeat\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
				"data: {\"type\":\"ping\"}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n"
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: relayFormat,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(
				context,
				&http.Response{Body: io.NopCloser(strings.NewReader(body))},
				info,
			)

			require.Nil(t, relayErr)
			require.NotNil(t, usage)
			require.Equal(t, 3, usage.PromptTokens)
			require.Equal(t, 1, usage.CompletionTokens)
			if relayFormat == types.RelayFormatClaude {
				require.Contains(t, recorder.Body.String(), "event: ping\ndata: {\"type\":\"ping\"}\n\n")
			} else {
				require.Contains(t, recorder.Body.String(), ": PING\n\n")
			}
		})
	}
}

func TestClaudeManagedStreamIgnoresHeartbeatBeforeValidStart(t *testing.T) {
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				IsStream:               true,
				RelayFormat:            relayFormat,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6"},
			}

			relayErr := HandleStreamResponseData(context, info, &ClaudeResponseInfo{}, `{"type":"ping"}`)

			require.Nil(t, relayErr)
			require.False(t, context.Writer.Written())
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestClaudeManagedStreamRejectsAmbiguousEventTypeBeforeWrite(t *testing.T) {
	for _, data := range []string{
		`{"TYPE":"ping"}`,
		`{"type":"message_start","type":"ping"}`,
		`{"type":"ping","type":"message_start"}`,
	} {
		t.Run(data, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				IsStream:               true,
				RelayFormat:            types.RelayFormatClaude,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6"},
			}

			relayErr := HandleStreamResponseData(context, info, &ClaudeResponseInfo{}, data)

			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "event type")
			require.False(t, context.Writer.Written())
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestManagedClaudeStreamEventTypeWhitelist(t *testing.T) {
	for _, eventType := range []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
		"ping",
		"error",
	} {
		require.True(t, isManagedClaudeStreamEventType(eventType), eventType)
	}
	require.False(t, isManagedClaudeStreamEventType("future_event"))
}

func TestClaudeManagedNativeStreamRejectsUnsafeEventType(t *testing.T) {
	for _, data := range []string{
		`{"type":"future_event"}`,
		`{"type":"x\n\ndata: [DONE]"}`,
		`{"type":"x\r\nevent: message_stop"}`,
	} {
		t.Run(data, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				IsStream:               true,
				RelayFormat:            types.RelayFormatClaude,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, UpstreamModelName: "claude-sonnet-4-6"},
			}
			claudeInfo := &ClaudeResponseInfo{
				Usage:              &dto.Usage{PromptTokens: 3},
				SawMessageStart:    true,
				SawValidInputUsage: true,
			}

			relayErr := HandleStreamResponseData(context, info, claudeInfo, data)

			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "event type")
			require.False(t, context.Writer.Written())
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestClaudeManagedHeartbeatPropagatesWriteFailure(t *testing.T) {
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Writer = &failOnWriteResponseWriter{ResponseWriter: context.Writer, failAt: 2}
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_heartbeat_failure\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
				"data: {\"type\":\"ping\"}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: relayFormat,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(
				context,
				&http.Response{Body: io.NopCloser(strings.NewReader(body))},
				info,
			)

			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "downstream write failed")
			require.NotNil(t, usage)
			require.Equal(t, 3, usage.PromptTokens)
		})
	}
}

func TestClaudeManagedStreamRejectsDuplicateStartAndUsageRegression(t *testing.T) {
	for _, test := range []struct {
		name           string
		body           string
		wantError      string
		wantPrompt     int
		wantCompletion int
	}{
		{
			name: "duplicate start",
			body: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_duplicate\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":100}}}\n\n" +
				"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_duplicate\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":1}}}\n\n",
			wantError:  "duplicate message_start",
			wantPrompt: 100,
		},
		{
			name: "cumulative usage regression",
			body: "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_regression\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":100}}}\n\n" +
				"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":100}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n\n",
			wantError:      "counters regressed",
			wantPrompt:     100,
			wantCompletion: 100,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: types.RelayFormatClaude,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(
				context,
				&http.Response{Body: io.NopCloser(strings.NewReader(test.body))},
				info,
			)

			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, test.wantError)
			require.NotNil(t, usage)
			require.Equal(t, test.wantPrompt, usage.PromptTokens)
			require.Equal(t, test.wantCompletion, usage.CompletionTokens)
		})
	}
}

func TestClaudeManagedPartialCacheOnlyUsageDoesNotSynthesizePrompt(t *testing.T) {
	for _, test := range []struct {
		name      string
		usageJSON string
		assert    func(*testing.T, *dto.Usage)
	}{
		{
			name:      "cache read",
			usageJSON: `{"input_tokens":0,"cache_read_input_tokens":100}`,
			assert: func(t *testing.T, usage *dto.Usage) {
				require.Equal(t, 100, usage.PromptTokensDetails.CachedTokens)
			},
		},
		{
			name:      "top-level cache write",
			usageJSON: `{"input_tokens":0,"claude_cache_creation_5_m_tokens":100}`,
			assert: func(t *testing.T, usage *dto.Usage) {
				require.Equal(t, 100, usage.ClaudeCacheCreation5mTokens)
			},
		},
		{
			name:      "nested cache write",
			usageJSON: `{"input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":100}}`,
			assert: func(t *testing.T, usage *dto.Usage) {
				require.Equal(t, 100, usage.ClaudeCacheCreation1hTokens)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_cache_only\",\"model\":\"claude-sonnet-4-6\",\"usage\":" + test.usageJSON + "}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n"
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: types.RelayFormatClaude,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(
				context,
				&http.Response{Body: io.NopCloser(strings.NewReader(body))},
				info,
			)

			require.NotNil(t, relayErr)
			require.NotNil(t, usage)
			require.Zero(t, usage.PromptTokens)
			test.assert(t, usage)
		})
	}
}

func TestClaudeManagedStreamRejectsContentAfterTerminalEvent(t *testing.T) {
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_post_terminal\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"before\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"must-not-escape\"}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n"
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: relayFormat,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(
				context,
				&http.Response{Body: io.NopCloser(strings.NewReader(body))},
				info,
			)

			require.NotNil(t, usage)
			require.Equal(t, 1, usage.CompletionTokens)
			require.NotContains(t, recorder.Body.String(), "must-not-escape")
			if relayFormat == types.RelayFormatClaude {
				require.NotNil(t, relayErr)
				require.ErrorContains(t, relayErr, "after its terminal event")
				require.NotContains(t, recorder.Body.String(), "[DONE]")
			} else {
				require.Nil(t, relayErr)
				require.Contains(t, recorder.Body.String(), "[DONE]")
			}
		})
	}
}

func TestClaudeStreamPropagatesChunkAndFinalWriteFailures(t *testing.T) {
	for _, test := range []struct {
		name         string
		failAt       int
		includeUsage bool
	}{
		{name: "content chunk", failAt: 2},
		{name: "final usage", failAt: 4, includeUsage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Writer = &failOnWriteResponseWriter{ResponseWriter: context.Writer, failAt: test.failAt}
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_write_failure\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: types.RelayFormatOpenAI, ShouldIncludeUsage: test.includeUsage,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(
				context,
				&http.Response{Body: io.NopCloser(strings.NewReader(body))},
				info,
			)

			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "downstream write failed")
			require.NotNil(t, usage)
			require.NotContains(t, recorder.Body.String(), "[DONE]")
		})
	}
}

func TestClaudeStreamPropagatesTerminalWritePanics(t *testing.T) {
	for _, test := range []struct {
		name        string
		relayFormat types.RelayFormat
		panicAt     int
	}{
		{name: "native message_stop", relayFormat: types.RelayFormatClaude, panicAt: 3},
		{name: "OpenAI terminal delta", relayFormat: types.RelayFormatOpenAI, panicAt: 2},
		{name: "OpenAI final done", relayFormat: types.RelayFormatOpenAI, panicAt: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Writer = &panicOnWriteResponseWriter{ResponseWriter: context.Writer, panicAt: test.panicAt}
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_writer_panic\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n"
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: test.relayFormat,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(
				context,
				&http.Response{Body: io.NopCloser(strings.NewReader(body))},
				info,
			)

			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "panic")
			require.NotNil(t, usage)
			require.NotContains(t, recorder.Body.String(), "[DONE]")
		})
	}
}

func TestClaudeStreamCompletionRejectsTerminalFlagsAfterTransportFailure(t *testing.T) {
	for _, reason := range []relaycommon.StreamEndReason{
		relaycommon.StreamEndReasonPanic,
		relaycommon.StreamEndReasonPingFail,
	} {
		t.Run(string(reason), func(t *testing.T) {
			status := relaycommon.NewStreamStatus()
			status.SetEndReason(reason, errors.New("stream transport failed"))
			info := &relaycommon.RelayInfo{
				RelayFormat:  types.RelayFormatClaude,
				StreamStatus: status,
				ChannelMeta:  &relaycommon.ChannelMeta{ManagedProvider: true},
			}
			claudeInfo := &ClaudeResponseInfo{
				Done:               true,
				SawMessageStop:     true,
				SawValidInputUsage: true,
				SawTerminalUsage:   true,
			}

			relayErr := validateClaudeStreamCompletion(info, claudeInfo)

			require.NotNil(t, relayErr)
			require.Equal(t, http.StatusBadGateway, relayErr.StatusCode)
			require.ErrorContains(t, relayErr, "stream transport failed")
		})
	}
}

func TestClaudeStreamStopsReadingAfterMessageStop(t *testing.T) {
	for _, test := range []struct {
		name        string
		relayFormat types.RelayFormat
		terminal    string
	}{
		{name: "native message stop", relayFormat: types.RelayFormatClaude, terminal: "data: {\"type\":\"message_stop\"}\n\n"},
		{name: "OpenAI terminal delta", relayFormat: types.RelayFormatOpenAI},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = writer.Close() })
			body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stop\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
				test.terminal
			writeResult := make(chan error, 1)
			go func() {
				_, err := io.WriteString(writer, body)
				writeResult <- err
			}()

			type streamResult struct {
				usage *dto.Usage
				err   *types.NewAPIError
			}
			result := make(chan streamResult, 1)
			go func() {
				recorder := httptest.NewRecorder()
				context, _ := gin.CreateTestContext(recorder)
				context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				info := &relaycommon.RelayInfo{
					IsStream: true, RelayFormat: test.relayFormat,
					ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
				}
				usage, relayErr := ClaudeStreamHandler(context, &http.Response{Body: reader}, info)
				result <- streamResult{usage: usage, err: relayErr}
			}()

			select {
			case got := <-result:
				require.Nil(t, got.err)
				require.NotNil(t, got.usage)
				require.Equal(t, 1, got.usage.CompletionTokens)
			case <-time.After(time.Second):
				t.Fatal("Claude stream remained blocked after its semantic terminal event")
			}
			select {
			case err := <-writeResult:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("upstream writer did not finish")
			}
		})
	}
}

func TestClaudeManagedStreamRejectsOutputBeforeUsageAttestation(t *testing.T) {
	for _, relayFormat := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatOpenAI} {
		t.Run(string(relayFormat), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			body := "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"must not escape\"}}\n\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n"
			response := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
			info := &relaycommon.RelayInfo{
				IsStream: true, RelayFormat: relayFormat,
				AuthorizedPromptTokens: 4096, AuthorizedCompletionTokens: 4096,
				ChannelMeta: &relaycommon.ChannelMeta{
					UpstreamModelName: "claude-sonnet-4-6",
					ManagedProvider:   true,
				},
			}

			usage, relayErr := ClaudeStreamHandler(context, response, info)

			require.Nil(t, usage)
			require.NotNil(t, relayErr)
			require.ErrorContains(t, relayErr, "before valid message_start usage")
			require.Empty(t, recorder.Body.String())
		})
	}
}

func TestClaudeStreamHandlerAcceptsMessageDeltaFollowedByEOF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_done\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"
	response := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	info := &relaycommon.RelayInfo{
		IsStream: true, RelayFormat: types.RelayFormatOpenAI, ShouldIncludeUsage: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
	}

	usage, relayErr := ClaudeStreamHandler(context, response, info)

	require.Nil(t, relayErr)
	require.NotNil(t, usage)
	require.Equal(t, 3, usage.PromptTokens)
	require.Equal(t, 1, usage.CompletionTokens)
	require.Contains(t, recorder.Body.String(), "[DONE]")
}

func TestClaudeNativeStreamRequiresMessageStopAfterMessageDelta(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_native_incomplete\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"
	response := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	info := &relaycommon.RelayInfo{
		IsStream: true, RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
	}

	usage, relayErr := ClaudeStreamHandler(context, response, info)

	require.NotNil(t, relayErr)
	require.Equal(t, types.ErrorCodeBadResponse, relayErr.GetErrorCode())
	require.NotNil(t, usage)
	require.Contains(t, recorder.Body.String(), "event: message_delta")
	require.NotContains(t, recorder.Body.String(), "event: message_stop")
}

func TestClaudeOpenAIToolIndexesIgnoreNonToolContentBlocks(t *testing.T) {
	claudeInfo := &ClaudeResponseInfo{}
	thinkingIndex, textIndex, firstToolBlock, secondToolBlock := 0, 1, 2, 4
	firstToolStart := &dto.ClaudeResponse{
		Type:  "content_block_start",
		Index: &firstToolBlock,
		ContentBlock: &dto.ClaudeMediaMessage{
			Type: "tool_use", Id: "tool_first", Name: "first_tool",
		},
	}
	firstToolDelta := &dto.ClaudeResponse{
		Type:  "content_block_delta",
		Index: &firstToolBlock,
		Delta: &dto.ClaudeMediaMessage{Type: "input_json_delta"},
	}
	secondToolStart := &dto.ClaudeResponse{
		Type:  "content_block_start",
		Index: &secondToolBlock,
		ContentBlock: &dto.ClaudeMediaMessage{
			Type: "tool_use", Id: "tool_second", Name: "second_tool",
		},
	}

	require.Zero(t, claudeInfo.openAIToolCallIndex(&dto.ClaudeResponse{
		Type: "content_block_start", Index: &thinkingIndex, ContentBlock: &dto.ClaudeMediaMessage{Type: "thinking"},
	}))
	require.Zero(t, claudeInfo.openAIToolCallIndex(&dto.ClaudeResponse{
		Type: "content_block_start", Index: &textIndex, ContentBlock: &dto.ClaudeMediaMessage{Type: "text"},
	}))
	firstIndex := claudeInfo.openAIToolCallIndex(firstToolStart)
	require.Zero(t, firstIndex)
	require.Equal(t, firstIndex, claudeInfo.openAIToolCallIndex(firstToolDelta))
	secondIndex := claudeInfo.openAIToolCallIndex(secondToolStart)
	require.Equal(t, 1, secondIndex)

	firstChunk := streamResponseClaude2OpenAI(firstToolStart, firstIndex)
	secondChunk := streamResponseClaude2OpenAI(secondToolStart, secondIndex)
	require.Equal(t, 0, *firstChunk.Choices[0].Delta.ToolCalls[0].Index)
	require.Equal(t, 1, *secondChunk.Choices[0].Delta.ToolCalls[0].Index)
}

func TestClaudeWebSearchUsageRecordingIsSymmetric(t *testing.T) {
	for _, transport := range []string{"non-stream", "stream"} {
		t.Run(transport, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			info := &relaycommon.RelayInfo{
				RelayFormat: types.RelayFormatClaude,
			}
			usageJSON := `{"input_tokens":1,"server_tool_use":{"web_search_requests":2}}`
			if transport == "stream" {
				relayErr := HandleStreamResponseData(context, info, &ClaudeResponseInfo{},
					`{"type":"message_delta","usage":`+usageJSON+`}`)
				require.Nil(t, relayErr)
			} else {
				relayErr := HandleClaudeResponseData(
					context,
					info,
					&ClaudeResponseInfo{Usage: &dto.Usage{}},
					&http.Response{StatusCode: http.StatusOK, Header: make(http.Header)},
					[]byte(`{"type":"message","usage":`+usageJSON+`}`),
				)
				require.Nil(t, relayErr)
			}
			require.Equal(t, 2, context.GetInt("claude_web_search_requests"))
		})
	}
}

func TestClaudeStreamHandlerAcceptsCanonicalMessageStop(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stop\",\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":3}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	response := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	info := &relaycommon.RelayInfo{
		IsStream: true, RelayFormat: types.RelayFormatOpenAI, ShouldIncludeUsage: true,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
	}

	usage, relayErr := ClaudeStreamHandler(context, response, info)

	require.Nil(t, relayErr)
	require.NotNil(t, usage)
	require.Equal(t, 3, usage.PromptTokens)
	require.Positive(t, usage.CompletionTokens)
	require.Contains(t, recorder.Body.String(), "data: [DONE]")
}

func TestReadClaudeResponseBodyRejectsOversizedPayload(t *testing.T) {
	_, err := readClaudeResponseBody(strings.NewReader(strings.Repeat("x", maxClaudeResponseBytes+1)))
	require.ErrorContains(t, err, "响应超过")
}

func TestFormatClaudeResponseInfo_MessageStart(t *testing.T) {
	claudeInfo := &ClaudeResponseInfo{
		Usage: &dto.Usage{},
	}
	claudeResponse := &dto.ClaudeResponse{
		Type: "message_start",
		Message: &dto.ClaudeMediaMessage{
			Id:    "msg_123",
			Model: "claude-3-5-sonnet",
			Usage: &dto.ClaudeUsage{
				InputTokens:              100,
				OutputTokens:             1,
				CacheCreationInputTokens: 50,
				CacheReadInputTokens:     30,
			},
		},
	}

	ok := FormatClaudeResponseInfo(claudeResponse, nil, claudeInfo)
	if !ok {
		t.Fatal("expected true")
	}
	if claudeInfo.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", claudeInfo.Usage.PromptTokens)
	}
	if claudeInfo.Usage.PromptTokensDetails.CachedTokens != 30 {
		t.Errorf("CachedTokens = %d, want 30", claudeInfo.Usage.PromptTokensDetails.CachedTokens)
	}
	if claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens != 50 {
		t.Errorf("CachedCreationTokens = %d, want 50", claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens)
	}
	if claudeInfo.ResponseId != "msg_123" {
		t.Errorf("ResponseId = %s, want msg_123", claudeInfo.ResponseId)
	}
	if claudeInfo.Model != "claude-3-5-sonnet" {
		t.Errorf("Model = %s, want claude-3-5-sonnet", claudeInfo.Model)
	}
}

func TestFormatClaudeResponseInfo_MessageDelta_FullUsage(t *testing.T) {
	// message_start 先积累 usage
	claudeInfo := &ClaudeResponseInfo{
		Usage: &dto.Usage{
			PromptTokens: 100,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens:         30,
				CachedCreationTokens: 50,
			},
			CompletionTokens: 1,
		},
	}

	// message_delta 带完整 usage（原生 Anthropic 场景）
	claudeResponse := &dto.ClaudeResponse{
		Type:  "message_delta",
		Delta: &dto.ClaudeMediaMessage{StopReason: common.GetPointer("end_turn")},
		Usage: &dto.ClaudeUsage{
			InputTokens:              100,
			OutputTokens:             200,
			CacheCreationInputTokens: 50,
			CacheReadInputTokens:     30,
		},
	}

	ok := FormatClaudeResponseInfo(claudeResponse, nil, claudeInfo)
	if !ok {
		t.Fatal("expected true")
	}
	if claudeInfo.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", claudeInfo.Usage.PromptTokens)
	}
	if claudeInfo.Usage.CompletionTokens != 200 {
		t.Errorf("CompletionTokens = %d, want 200", claudeInfo.Usage.CompletionTokens)
	}
	if claudeInfo.Usage.TotalTokens != 300 {
		t.Errorf("TotalTokens = %d, want 300", claudeInfo.Usage.TotalTokens)
	}
	if !claudeInfo.Done {
		t.Error("expected stop_reason to mark Done")
	}
}

func TestFormatClaudeResponseInfo_MessageDelta_OnlyOutputTokens(t *testing.T) {
	// 模拟 Bedrock: message_start 已积累 usage
	claudeInfo := &ClaudeResponseInfo{
		Usage: &dto.Usage{
			PromptTokens: 100,
			PromptTokensDetails: dto.InputTokenDetails{
				CachedTokens:         30,
				CachedCreationTokens: 50,
			},
			CompletionTokens:            1,
			ClaudeCacheCreation5mTokens: 10,
			ClaudeCacheCreation1hTokens: 20,
		},
	}

	// Bedrock 的 message_delta 只有 output_tokens，缺少 input_tokens 和 cache 字段
	claudeResponse := &dto.ClaudeResponse{
		Type: "message_delta",
		Usage: &dto.ClaudeUsage{
			OutputTokens: 200,
			// InputTokens, CacheCreationInputTokens, CacheReadInputTokens 都是 0
		},
	}

	ok := FormatClaudeResponseInfo(claudeResponse, nil, claudeInfo)
	if !ok {
		t.Fatal("expected true")
	}
	// PromptTokens 应保持 message_start 的值（因为 message_delta 的 InputTokens=0，不更新）
	if claudeInfo.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", claudeInfo.Usage.PromptTokens)
	}
	if claudeInfo.Usage.CompletionTokens != 200 {
		t.Errorf("CompletionTokens = %d, want 200", claudeInfo.Usage.CompletionTokens)
	}
	if claudeInfo.Usage.TotalTokens != 300 {
		t.Errorf("TotalTokens = %d, want 300", claudeInfo.Usage.TotalTokens)
	}
	// cache 字段应保持 message_start 的值
	if claudeInfo.Usage.PromptTokensDetails.CachedTokens != 30 {
		t.Errorf("CachedTokens = %d, want 30", claudeInfo.Usage.PromptTokensDetails.CachedTokens)
	}
	if claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens != 50 {
		t.Errorf("CachedCreationTokens = %d, want 50", claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens)
	}
	if claudeInfo.Usage.ClaudeCacheCreation5mTokens != 10 {
		t.Errorf("ClaudeCacheCreation5mTokens = %d, want 10", claudeInfo.Usage.ClaudeCacheCreation5mTokens)
	}
	if claudeInfo.Usage.ClaudeCacheCreation1hTokens != 20 {
		t.Errorf("ClaudeCacheCreation1hTokens = %d, want 20", claudeInfo.Usage.ClaudeCacheCreation1hTokens)
	}
	if claudeInfo.Done {
		t.Error("expected usage-only message_delta not to mark Done")
	}
}

func TestFormatClaudeResponseInfo_NilClaudeInfo(t *testing.T) {
	claudeResponse := &dto.ClaudeResponse{Type: "message_start"}
	ok := FormatClaudeResponseInfo(claudeResponse, nil, nil)
	if ok {
		t.Error("expected false for nil claudeInfo")
	}
}

func TestFormatClaudeResponseInfo_ContentBlockDelta(t *testing.T) {
	text := "hello"
	claudeInfo := &ClaudeResponseInfo{
		Usage:        &dto.Usage{},
		ResponseText: strings.Builder{},
	}
	claudeResponse := &dto.ClaudeResponse{
		Type: "content_block_delta",
		Delta: &dto.ClaudeMediaMessage{
			Text: &text,
		},
	}

	ok := FormatClaudeResponseInfo(claudeResponse, nil, claudeInfo)
	if !ok {
		t.Fatal("expected true")
	}
	if claudeInfo.ResponseText.String() != "hello" {
		t.Errorf("ResponseText = %q, want %q", claudeInfo.ResponseText.String(), "hello")
	}
}

func TestBuildOpenAIStyleUsageFromClaudeUsage(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 20,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         30,
			CachedCreationTokens: 50,
		},
		ClaudeCacheCreation5mTokens: 10,
		ClaudeCacheCreation1hTokens: 20,
		UsageSemantic:               "anthropic",
	}

	openAIUsage := buildOpenAIStyleUsageFromClaudeUsage(usage)

	if openAIUsage.PromptTokens != 180 {
		t.Fatalf("PromptTokens = %d, want 180", openAIUsage.PromptTokens)
	}
	if openAIUsage.InputTokens != 180 {
		t.Fatalf("InputTokens = %d, want 180", openAIUsage.InputTokens)
	}
	if openAIUsage.TotalTokens != 200 {
		t.Fatalf("TotalTokens = %d, want 200", openAIUsage.TotalTokens)
	}
	if openAIUsage.UsageSemantic != "openai" {
		t.Fatalf("UsageSemantic = %s, want openai", openAIUsage.UsageSemantic)
	}
	if openAIUsage.UsageSource != "anthropic" {
		t.Fatalf("UsageSource = %s, want anthropic", openAIUsage.UsageSource)
	}
}

func TestBuildOpenAIStyleUsageFromClaudeUsagePreservesCacheCreationRemainder(t *testing.T) {
	tests := []struct {
		name                    string
		cachedCreationTokens    int
		cacheCreationTokens5m   int
		cacheCreationTokens1h   int
		expectedTotalInputToken int
	}{
		{
			name:                    "prefers aggregate when it includes remainder",
			cachedCreationTokens:    50,
			cacheCreationTokens5m:   10,
			cacheCreationTokens1h:   20,
			expectedTotalInputToken: 180,
		},
		{
			name:                    "falls back to split tokens when aggregate missing",
			cachedCreationTokens:    0,
			cacheCreationTokens5m:   10,
			cacheCreationTokens1h:   20,
			expectedTotalInputToken: 160,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := &dto.Usage{
				PromptTokens:     100,
				CompletionTokens: 20,
				PromptTokensDetails: dto.InputTokenDetails{
					CachedTokens:         30,
					CachedCreationTokens: tt.cachedCreationTokens,
				},
				ClaudeCacheCreation5mTokens: tt.cacheCreationTokens5m,
				ClaudeCacheCreation1hTokens: tt.cacheCreationTokens1h,
				UsageSemantic:               "anthropic",
			}

			openAIUsage := buildOpenAIStyleUsageFromClaudeUsage(usage)

			if openAIUsage.PromptTokens != tt.expectedTotalInputToken {
				t.Fatalf("PromptTokens = %d, want %d", openAIUsage.PromptTokens, tt.expectedTotalInputToken)
			}
			if openAIUsage.InputTokens != tt.expectedTotalInputToken {
				t.Fatalf("InputTokens = %d, want %d", openAIUsage.InputTokens, tt.expectedTotalInputToken)
			}
		})
	}
}

func TestBuildOpenAIStyleUsageFromClaudeUsageDefaultsAggregateCacheCreationTo5m(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 20,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         30,
			CachedCreationTokens: 50,
		},
		UsageSemantic: "anthropic",
	}

	openAIUsage := buildOpenAIStyleUsageFromClaudeUsage(usage)

	require.Equal(t, 50, openAIUsage.ClaudeCacheCreation5mTokens)
	require.Equal(t, 0, openAIUsage.ClaudeCacheCreation1hTokens)
}

func TestRequestOpenAI2ClaudeMessage_IgnoresUnsupportedFileContent(t *testing.T) {
	request := dto.GeneralOpenAIRequest{
		Model: "claude-3-5-sonnet",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					dto.MediaContent{
						Type: dto.ContentTypeText,
						Text: "see attachment",
					},
					dto.MediaContent{
						Type: dto.ContentTypeFile,
						File: &dto.MessageFile{
							FileName: "blob.bin",
							FileData: "JVBERi0xLjQK",
						},
					},
				},
			},
		},
	}

	claudeRequest, err := RequestOpenAI2ClaudeMessage(nil, request)
	require.NoError(t, err)
	require.Len(t, claudeRequest.Messages, 1)

	content, ok := claudeRequest.Messages[0].Content.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	require.Len(t, content, 1)
	require.Equal(t, "text", content[0].Type)
	require.NotNil(t, content[0].Text)
	require.Equal(t, "see attachment", *content[0].Text)
}

func TestRequestOpenAI2ClaudeMessage_SupportsPDFFileContent(t *testing.T) {
	request := dto.GeneralOpenAIRequest{
		Model: "claude-3-5-sonnet",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					dto.MediaContent{
						Type: dto.ContentTypeFile,
						File: &dto.MessageFile{
							FileName: "spec.pdf",
							FileData: "JVBERi0xLjQK",
						},
					},
					dto.MediaContent{
						Type: dto.ContentTypeText,
						Text: "summarize it",
					},
				},
			},
		},
	}

	claudeRequest, err := RequestOpenAI2ClaudeMessage(nil, request)
	require.NoError(t, err)
	require.Len(t, claudeRequest.Messages, 1)

	content, ok := claudeRequest.Messages[0].Content.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	require.Len(t, content, 2)
	require.Equal(t, "document", content[0].Type)
	require.NotNil(t, content[0].Source)
	require.Equal(t, "base64", content[0].Source.Type)
	require.Equal(t, "application/pdf", content[0].Source.MediaType)
	require.Equal(t, "JVBERi0xLjQK", content[0].Source.Data)
	require.Equal(t, "text", content[1].Type)
	require.NotNil(t, content[1].Text)
	require.Equal(t, "summarize it", *content[1].Text)
}

func TestRequestOpenAI2ClaudeMessage_ConvertsTextFileContentToText(t *testing.T) {
	request := dto.GeneralOpenAIRequest{
		Model: "claude-3-5-sonnet",
		Messages: []dto.Message{
			{
				Role: "user",
				Content: []any{
					dto.MediaContent{
						Type: dto.ContentTypeFile,
						File: &dto.MessageFile{
							FileName: "notes.txt",
							FileData: base64.StdEncoding.EncodeToString([]byte("alpha\nbeta")),
						},
					},
				},
			},
		},
	}

	claudeRequest, err := RequestOpenAI2ClaudeMessage(nil, request)
	require.NoError(t, err)
	require.Len(t, claudeRequest.Messages, 1)

	content, ok := claudeRequest.Messages[0].Content.([]dto.ClaudeMediaMessage)
	require.True(t, ok)
	require.Len(t, content, 1)
	require.Equal(t, "text", content[0].Type)
	require.NotNil(t, content[0].Text)
	require.Equal(t, "alpha\nbeta", *content[0].Text)
}
