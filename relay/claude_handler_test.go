package relay

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestManagedAndPriceAuthorizedClaudeBypassResponsesTranslationPolicy(t *testing.T) {
	global := model_setting.GetGlobalSettings()
	previous := global.ChatCompletionsToResponsesPolicy
	global.ChatCompletionsToResponsesPolicy = model_setting.ChatCompletionsToResponsesPolicy{
		Enabled: true, AllChannels: true, ModelPatterns: []string{`^claude-.*$`},
	}
	t.Cleanup(func() { global.ChatCompletionsToResponsesPolicy = previous })

	ordinary := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 17, ChannelType: constant.ChannelTypeAnthropic,
		},
	}
	require.True(t, shouldUseChatCompletionsViaResponses(ordinary))

	managed := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 17, ChannelType: constant.ChannelTypeAnthropic, ManagedProvider: true,
		},
	}
	require.False(t, shouldUseChatCompletionsViaResponses(managed))

	priceAuthorized := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 17, ChannelType: constant.ChannelTypeAnthropic,
		},
		PriceData: types.PriceData{ChannelSpecific: true},
	}
	require.False(t, shouldUseChatCompletionsViaResponses(priceAuthorized))
}

func TestShouldSettleClaudePartialUsage(t *testing.T) {
	newContext := func(written bool) *gin.Context {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		if written {
			_, _ = context.Writer.Write([]byte("data: partial\n\n"))
		}
		return context
	}

	usage := &dto.Usage{PromptTokens: 7, CompletionTokens: 2, TotalTokens: 9}
	require.True(t, shouldSettleClaudePartialUsage(
		newContext(true),
		&relaycommon.RelayInfo{IsStream: true},
		usage,
	))
	require.False(t, shouldSettleClaudePartialUsage(
		newContext(false),
		&relaycommon.RelayInfo{IsStream: true},
		usage,
	), "a pre-first-byte failure must remain retryable and refundable")
	require.False(t, shouldSettleClaudePartialUsage(
		newContext(true),
		&relaycommon.RelayInfo{IsStream: false},
		usage,
	), "the partial settlement path is stream-only")
	require.False(t, shouldSettleClaudePartialUsage(
		newContext(true),
		&relaycommon.RelayInfo{IsStream: true},
		&dto.Usage{},
	), "zero usage must not settle the pre-charge")
}

func TestFinishTextBillingResponseFlushesStrictNonStreamOnlyAfterSettlement(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	info := &relaycommon.RelayInfo{
		ForcePreConsume: true,
		PriceData:       types.PriceData{ChannelSpecific: true},
	}
	service.StageResponseBytes(context, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, []byte(`{"type":"message"}`))

	require.False(t, context.Writer.Written())
	require.Nil(t, finishTextBillingResponse(context, info, nil))
	require.True(t, context.Writer.Written())
	require.Equal(t, `{"type":"message"}`, recorder.Body.String())
}

func TestFinishTextBillingResponseDiscardsStrictNonStreamOnSettlementFailure(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	info := &relaycommon.RelayInfo{
		ForcePreConsume: true,
		PriceData:       types.PriceData{ChannelSpecific: true},
	}
	service.StageResponseBytes(context, nil, []byte(`{"type":"message"}`))

	relayErr := finishTextBillingResponse(context, info, errors.New("injected settlement failure"))

	require.NotNil(t, relayErr)
	require.Equal(t, http.StatusInternalServerError, relayErr.StatusCode)
	require.True(t, types.IsSkipRetryError(relayErr))
	require.Contains(t, relayErr.Error(), "manual_reconciliation_required")
	require.Contains(t, relayErr.Error(), "automatic_settlement_retry=false")
	require.False(t, context.Writer.Written(), "the controller must still own the first and only error write")
	require.Empty(t, recorder.Body.String(), "the successful upstream JSON must be discarded")
	require.False(t, service.FlushStagedResponseBytes(context))

	context.JSON(relayErr.StatusCode, gin.H{"type": "error", "error": relayErr.ToClaudeError()})
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"type":"error"`)
	require.NotContains(t, recorder.Body.String(), `"type":"message"`, "success and error JSON must never be concatenated")
}

func TestFinishTextBillingResponseKeepsWrittenStrictStreamAndBlocksReplay(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	_, writeErr := context.Writer.Write([]byte("data: partial\n\n"))
	require.NoError(t, writeErr)
	info := &relaycommon.RelayInfo{
		IsStream:        true,
		ForcePreConsume: true,
		PriceData:       types.PriceData{ChannelSpecific: true},
	}

	relayErr := finishTextBillingResponse(context, info, errors.New("injected settlement failure"))

	require.NotNil(t, relayErr)
	require.True(t, types.IsSkipRetryError(relayErr))
	require.Contains(t, relayErr.Error(), "manual_reconciliation_required")
	require.Contains(t, relayErr.Error(), "automatic_settlement_retry=false")
	require.Equal(t, "data: partial\n\n", recorder.Body.String())
}
