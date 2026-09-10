package common

import (
	"fmt"
	"strings"
	"testing"

	corecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func authorizedClaudeRelayInfo(provider string) *RelayInfo {
	return &RelayInfo{
		AuthorizedPromptTokens:     2048,
		AuthorizedCompletionTokens: 1024,
		ChannelMeta:                &ChannelMeta{UpstreamModelName: "claude-sonnet-4-6"},
		PriceData: types.PriceData{
			ChannelSpecific: true,
			PriceProvider:   provider,
		},
	}
}

func TestValidateClaudeRequestPriceAuthorizationAcceptsFrozenRequest(t *testing.T) {
	info := authorizedClaudeRelayInfo(types.PriceProviderZenMux)

	err := ValidateClaudeRequestPriceAuthorization(
		[]byte(`{"model":"claude-sonnet-4-6","max_tokens":1024}`), info,
	)

	require.NoError(t, err)
}

func TestValidateClaudeRequestPriceAuthorizationRejectsBillingDimensionOverride(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "larger max tokens", body: `{"model":"claude-sonnet-4-6","max_tokens":1025}`, want: "max_tokens"},
		{name: "missing max tokens", body: `{"model":"claude-sonnet-4-6"}`, want: "max_tokens"},
		{name: "different model", body: `{"model":"claude-opus-5","max_tokens":1024}`, want: "model"},
		{name: "regional inference", body: `{"model":"claude-sonnet-4-6","max_tokens":1024,"inference_geo":"us"}`, want: "inference_geo"},
		{name: "fast speed", body: `{"model":"claude-sonnet-4-6","max_tokens":1024,"speed":"fast"}`, want: "speed"},
		{name: "service tier", body: `{"model":"claude-sonnet-4-6","max_tokens":1024,"service_tier":"auto"}`, want: "service_tier"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateClaudeRequestPriceAuthorization([]byte(test.body), authorizedClaudeRelayInfo(types.PriceProviderZenMux))
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestValidateClaudeRequestPriceAuthorizationRejectsExpandedBodyBeyondPromptBound(t *testing.T) {
	info := authorizedClaudeRelayInfo(types.PriceProviderZenMux)
	info.AuthorizedPromptTokens = 300
	body := `{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"` +
		strings.Repeat("a", 100) + `"}]}`

	err := ValidateClaudeRequestPriceAuthorization([]byte(body), info)

	require.ErrorContains(t, err, "输入边界")
}

func TestValidateClaudeRequestPriceAuthorizationRejectsManagedWebSearch(t *testing.T) {
	for _, provider := range []string{types.PriceProviderZenMux, types.PriceProviderTabCode} {
		t.Run(provider, func(t *testing.T) {
			info := authorizedClaudeRelayInfo(provider)
			info.AuthorizedPromptTokens = 4096
			err := ValidateClaudeRequestPriceAuthorization(
				[]byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"tools":[{"type":"web_search_20250305","name":"web_search"}]}`),
				info,
			)

			require.ErrorContains(t, err, "web_search")
		})
	}
}

func TestClaudeRequestUsesUnboundedMedia(t *testing.T) {
	tests := []struct {
		name    string
		request dto.Request
		want    bool
	}{
		{
			name: "native system image URL",
			request: &dto.ClaudeRequest{System: []any{map[string]any{
				"type": "image", "source": map[string]any{"type": "url", "url": "https://media.example/image.png"},
			}}},
			want: true,
		},
		{
			name: "native message document file id",
			request: &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Content: []any{map[string]any{
				"type": "document", "source": map[string]any{"type": "file", "file_id": "file_123"},
			}}}}},
			want: true,
		},
		{
			name: "native nested tool result URL",
			request: &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Content: []any{map[string]any{
				"type": "tool_result", "content": []any{map[string]any{
					"type": "document", "source": map[string]any{"type": "url", "url": "https://media.example/doc.pdf"},
				}},
			}}}}},
			want: true,
		},
		{
			name: "native document content cannot hide nested URL",
			request: &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Content: []any{map[string]any{
				"type": "document", "source": map[string]any{"type": "content", "content": []any{map[string]any{
					"type": "image", "source": map[string]any{"type": "url", "url": "https://media.example/image.png"},
				}}},
			}}}}},
			want: true,
		},
		{
			name: "native image base64 must be scalar",
			request: &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Content: []any{map[string]any{
				"type": "image", "source": map[string]any{"type": "base64", "data": map[string]any{"url": "https://media.example/image.png"}},
			}}}}},
			want: true,
		},
		{
			name: "native inline base64",
			request: &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Content: []any{map[string]any{
				"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "aGVsbG8="},
			}}}}},
			want: true,
		},
		{
			name: "native inline text may mention URL",
			request: &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Content: []any{map[string]any{
				"type": "document", "source": map[string]any{"type": "text", "data": "See https://example.com in this inline document."},
			}}}}},
		},
		{
			name: "native inline document content",
			request: &dto.ClaudeRequest{Messages: []dto.ClaudeMessage{{Content: []any{map[string]any{
				"type": "document", "source": map[string]any{"type": "content", "content": []any{map[string]any{
					"type": "text", "text": "See https://example.com in this inline block.",
				}}},
			}}}}},
		},
		{
			name: "OpenAI image URL",
			request: &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Content: []any{map[string]any{
				"type": "image_url", "image_url": map[string]any{"url": "https://media.example/image.png"},
			}}}}},
			want: true,
		},
		{
			name: "OpenAI file id",
			request: &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Content: []any{map[string]any{
				"type": "file", "file": map[string]any{"file_id": "file_123"},
			}}}}},
			want: true,
		},
		{
			name: "OpenAI remote file data",
			request: &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Content: []any{map[string]any{
				"type": "file", "file": map[string]any{"filename": "doc.pdf", "file_data": "https://media.example/doc.pdf"},
			}}}}},
			want: true,
		},
		{
			name: "OpenAI video URL",
			request: &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Content: []any{map[string]any{
				"type": "video_url", "video_url": "https://media.example/video.mp4",
			}}}}},
			want: true,
		},
		{
			name: "OpenAI audio URL in data",
			request: &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Content: []any{map[string]any{
				"type": "input_audio", "input_audio": map[string]any{"format": "wav", "data": "https://media.example/audio.wav"},
			}}}}},
			want: true,
		},
		{
			name: "OpenAI inline data URL",
			request: &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Content: []any{map[string]any{
				"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,aGVsbG8="},
			}}}}},
			want: true,
		},
		{
			name: "OpenAI inline PDF data",
			request: &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Content: []any{map[string]any{
				"type": "file", "file": map[string]any{"filename": "doc.pdf", "file_data": "data:application/pdf;base64,aGVsbG8="},
			}}}}},
			want: true,
		},
		{
			name: "OpenAI inline audio data",
			request: &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Content: []any{map[string]any{
				"type": "input_audio", "input_audio": map[string]any{"format": "wav", "data": "aGVsbG8="},
			}}}}},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ClaudeRequestUsesUnboundedMedia(test.request)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestValidateClaudeRequestPriceAuthorizationRejectsUnboundedMedia(t *testing.T) {
	for _, body := range []string{
		`{"model":"claude-sonnet-4-6","max_tokens":1024,"system":[{"type":"image","source":{"type":"url","url":"https://media.example/image.png"}}]}`,
		`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file_123"}}]}]}`,
		`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"content","content":[{"type":"image","source":{"type":"url","url":"https://media.example/image.png"}}]}}]}]}`,
	} {
		info := authorizedClaudeRelayInfo(types.PriceProviderZenMux)
		info.AuthorizedPromptTokens = 4096

		err := ValidateClaudeRequestPriceAuthorization([]byte(body), info)

		require.ErrorContains(t, err, "file_id")
	}
}

func TestValidateClaudeRequestPriceAuthorizationRejectsInlineBinaryMedia(t *testing.T) {
	for _, body := range []string{
		`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`,
		`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"aGVsbG8="}}]}]}`,
	} {
		info := authorizedClaudeRelayInfo(types.PriceProviderZenMux)
		info.AuthorizedPromptTokens = 4096
		info.ChannelMeta.ManagedProvider = true

		err := ValidateClaudeRequestPriceAuthorization([]byte(body), info)

		require.ErrorContains(t, err, "二进制媒体")
		require.Zero(t, info.ClaudeInputEvidenceBytes)
	}
}

func TestValidateClaudeRequestPriceAuthorizationAcceptsInlineTextContainingURL(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","data":"See https://example.com in this inline document."}}]}]}`)
	info := authorizedClaudeRelayInfo(types.PriceProviderZenMux)
	info.AuthorizedPromptTokens = 4096

	err := ValidateClaudeRequestPriceAuthorization(body, info)

	require.NoError(t, err)
}

func TestValidateClaudeRequestPriceAuthorizationRecordsSemanticInputEvidenceBytes(t *testing.T) {
	for _, size := range []int{128, 129} {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			text := strings.Repeat("a", size)
			body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"` + text + `"}]}`)
			info := authorizedClaudeRelayInfo(types.PriceProviderZenMux)
			info.AuthorizedPromptTokens = 4096
			info.ChannelMeta.ManagedProvider = true

			err := ValidateClaudeRequestPriceAuthorization(body, info)

			require.NoError(t, err)
			require.Equal(t, int64(size), info.ClaudeInputEvidenceBytes)
		})
	}
}

func TestValidateClaudeRequestPriceAuthorizationIncludesLegacyPromptEvidence(t *testing.T) {
	prompt := strings.Repeat("p", 129)
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"prompt":"` + prompt + `"}`)
	info := authorizedClaudeRelayInfo(types.PriceProviderZenMux)
	info.AuthorizedPromptTokens = 4096
	info.ChannelMeta.ManagedProvider = true

	err := ValidateClaudeRequestPriceAuthorization(body, info)

	require.NoError(t, err)
	require.Equal(t, int64(len(prompt)), info.ClaudeInputEvidenceBytes)
}

func TestValidateClaudeRequestPriceAuthorizationMeasuresStructuredInputEvidence(t *testing.T) {
	toolInput := map[string]any{"q": "值"}
	toolResult := map[string]any{"answer": "好"}
	toolInputJSON, err := corecommon.Marshal(toolInput)
	require.NoError(t, err)
	toolResultJSON, err := corecommon.Marshal(toolResult)
	require.NoError(t, err)
	toolsJSON := `[{"name":"fn","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]`
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,` +
		`"system":"系统","messages":[{"role":"assistant","content":[` +
		`{"type":"text","text":"hi"},` +
		`{"type":"thinking","thinking":"思考"},` +
		`{"type":"redacted_thinking","data":"redacted"},` +
		`{"type":"tool_use","name":"lookup","input":{"q":"值"}},` +
		`{"type":"tool_result","content":{"answer":"好"}},` +
		`{"type":"document","source":{"type":"text","data":"doc"}},` +
		`{"type":"document","source":{"type":"content","content":[{"type":"text","text":"nested"}]}}` +
		`]}],"tools":` + toolsJSON + `}`)
	info := authorizedClaudeRelayInfo(types.PriceProviderZenMux)
	info.AuthorizedPromptTokens = 16384
	info.ChannelMeta.ManagedProvider = true
	expected := len("系统") + len("hi") + len("思考") + len("redacted") + len("lookup") +
		len(toolInputJSON) + len(toolResultJSON) + len("doc") + len("nested") + len(toolsJSON)

	err = ValidateClaudeRequestPriceAuthorization(body, info)

	require.NoError(t, err)
	require.Equal(t, int64(expected), info.ClaudeInputEvidenceBytes)
}

func TestValidateClaudeRequestPriceAuthorizationRejectsBinaryMediaBeforeRecordingEvidence(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":[` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("A", 4096) + `"}},` +
		`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + strings.Repeat("B", 4096) + `"}}` +
		`]}]}`)
	info := authorizedClaudeRelayInfo(types.PriceProviderZenMux)
	info.AuthorizedPromptTokens = 16384
	info.ChannelMeta.ManagedProvider = true

	err := ValidateClaudeRequestPriceAuthorization(body, info)

	require.ErrorContains(t, err, "二进制媒体")
	require.Zero(t, info.ClaudeInputEvidenceBytes)
}

func TestClaudeToolPromptAllowanceMustBePreauthorized(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`)
	info := authorizedClaudeRelayInfo(types.PriceProviderZenMux)
	info.AuthorizedPromptTokens = len(body) + int(claudeRequestFramingAllowance)

	err := ValidateClaudeRequestPriceAuthorization(body, info)

	require.ErrorContains(t, err, "输入边界")
}

func TestValidateClaudeRequestPriceAuthorizationFailsClosedWithoutBounds(t *testing.T) {
	info := &RelayInfo{
		ChannelMeta: &ChannelMeta{
			ManagedProvider:   true,
			UpstreamModelName: "claude-sonnet-4-6",
		},
	}

	err := ValidateClaudeRequestPriceAuthorization(
		[]byte(`{"model":"claude-sonnet-4-6","max_tokens":1024}`), info,
	)

	require.ErrorContains(t, err, "预授权边界")
}
