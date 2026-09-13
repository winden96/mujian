package service

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestQuotaDecimalToIntClampsUnsafeValues(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	require.Zero(t, quotaDecimalToInt(decimal.NewFromInt(-1)))
	require.Equal(t, maxInt, quotaDecimalToInt(decimal.New(1, 100)))
	require.Equal(t, 2, quotaDecimalToInt(decimal.NewFromFloat(1.6)))
}

func TestCalculateTextQuotaSummaryUnifiedForClaudeSemantic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	usage := &dto.Usage{
		PromptTokens:     1000,
		CompletionTokens: 200,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         100,
			CachedCreationTokens: 50,
		},
		ClaudeCacheCreation5mTokens: 10,
		ClaudeCacheCreation1hTokens: 20,
	}

	priceData := types.PriceData{
		ModelRatio:           1,
		CompletionRatio:      2,
		CacheRatio:           0.1,
		CacheCreationRatio:   1.25,
		CacheCreation5mRatio: 1.25,
		CacheCreation1hRatio: 2,
		GroupRatioInfo: types.GroupRatioInfo{
			GroupRatio: 1,
		},
	}

	chatRelayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         "claude-3-7-sonnet",
		PriceData:               priceData,
		StartTime:               time.Now(),
	}
	messageRelayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatClaude,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         "claude-3-7-sonnet",
		PriceData:               priceData,
		StartTime:               time.Now(),
	}

	chatSummary := calculateTextQuotaSummary(ctx, chatRelayInfo, usage)
	messageSummary := calculateTextQuotaSummary(ctx, messageRelayInfo, usage)

	require.Equal(t, messageSummary.Quota, chatSummary.Quota)
	require.Equal(t, messageSummary.CacheCreationTokens5m, chatSummary.CacheCreationTokens5m)
	require.Equal(t, messageSummary.CacheCreationTokens1h, chatSummary.CacheCreationTokens1h)
	require.True(t, chatSummary.IsClaudeUsageSemantic)
	require.Equal(t, 1488, chatSummary.Quota)
}

func TestCalculateTextQuotaSummaryUsesSplitClaudeCacheCreationRatios(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         "claude-3-7-sonnet",
		PriceData: types.PriceData{
			ModelRatio:           1,
			CompletionRatio:      1,
			CacheRatio:           0,
			CacheCreationRatio:   1,
			CacheCreation5mRatio: 2,
			CacheCreation1hRatio: 3,
			GroupRatioInfo: types.GroupRatioInfo{
				GroupRatio: 1,
			},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     100,
		CompletionTokens: 0,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedCreationTokens: 10,
		},
		ClaudeCacheCreation5mTokens: 2,
		ClaudeCacheCreation1hTokens: 3,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// 100 + remaining(5)*1 + 2*2 + 3*3 = 118
	require.Equal(t, 118, summary.Quota)
}

func TestCalculateTextQuotaSummaryStaysWithinOneHourCacheWritePreauthorization(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatClaude,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         "claude-sonnet-4-6",
		PriceData: types.PriceData{
			ModelRatio: 1.5, CompletionRatio: 5,
			CacheCreationRatio: 1.25, CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2,
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}
	usage := &dto.Usage{
		PromptTokens: 1,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedCreationTokens: 99,
		},
		ClaudeCacheCreation1hTokens: 99,
		UsageSemantic:               "anthropic",
	}

	summary := calculateTextQuotaSummary(context, relayInfo, usage)

	// Preauthorization for a 100-token prompt at the 1h rate is 300 quota.
	require.Equal(t, 299, summary.Quota)
	require.LessOrEqual(t, summary.Quota, 300)
}

func TestCalculateTextQuotaSummaryBillsCacheOnlyClaudeUsage(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:             types.RelayFormatClaude,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         "claude-sonnet-4-6",
		PriceData: types.PriceData{
			ModelRatio: 1.5, CompletionRatio: 5, CacheRatio: 0.1,
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}
	usage := &dto.Usage{
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 100},
		UsageSemantic:       "anthropic",
	}

	summary := calculateTextQuotaSummary(context, relayInfo, usage)

	require.True(t, ValidUsage(usage))
	require.Equal(t, 100, summary.TotalTokens)
	require.Equal(t, 15, summary.Quota)
}

func TestZenMuxClaudeWebSearchIsRecordedWithoutSeparateCharge(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Set("claude_web_search_requests", 2)
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6",
		PriceData: types.PriceData{
			ModelRatio: 1, CompletionRatio: 1, PriceProvider: types.PriceProviderZenMux,
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}
	usage := &dto.Usage{PromptTokens: 100, TotalTokens: 100, UsageSemantic: "anthropic"}

	summary := calculateTextQuotaSummary(context, info, usage)

	require.Equal(t, 100, summary.Quota)
	require.Equal(t, 2, summary.ClaudeWebSearchCallCount)
	require.Zero(t, summary.ClaudeWebSearchPrice)
	require.False(t, summary.ClaudeWebSearchBilled)
}

func TestNonZenMuxClaudeWebSearchKeepsExistingSeparateCharge(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Set("claude_web_search_requests", 2)
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6",
		PriceData: types.PriceData{
			ModelRatio: 1, CompletionRatio: 1, PriceProvider: "tabcode",
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}
	usage := &dto.Usage{PromptTokens: 100, TotalTokens: 100, UsageSemantic: "anthropic"}

	summary := calculateTextQuotaSummary(context, info, usage)

	require.Equal(t, 10100, summary.Quota)
	require.Equal(t, 2, summary.ClaudeWebSearchCallCount)
	require.Equal(t, 10.0, summary.ClaudeWebSearchPrice)
	require.True(t, summary.ClaudeWebSearchBilled)
}

func TestStrictSettlementExhaustionMetadataAndLogAreExplicitAndSecretFree(t *testing.T) {
	relayInfo := &relaycommon.RelayInfo{
		RequestId:             "strict-settlement-request",
		UserId:                41,
		TokenId:               42,
		TokenKey:              "secret-token-key-must-not-be-logged",
		FinalPreConsumedQuota: 80,
		ChannelMeta:           &relaycommon.ChannelMeta{ChannelId: 43},
	}
	other := map[string]interface{}{}

	appendStrictSettlementFailureMetadata(other, relayInfo, 50)

	require.Equal(t, "manual_reconciliation_required", other["billing_settlement_status"])
	require.Equal(t, relayInfo.RequestId, other["billing_request_id"])
	require.Equal(t, 50, other["billing_actual_quota"])
	require.Equal(t, 80, other["billing_preconsumed_quota"])
	require.Equal(t, 41, other["billing_user_id"])
	require.Equal(t, 42, other["billing_token_id"])
	require.Equal(t, 43, other["billing_channel_id"])
	require.Equal(t, strictSettlementMaxAttempts, other["billing_settlement_attempts"])
	require.Equal(t, false, other["automatic_settlement_retry"])
	require.Equal(t, false, other["upstream_replay_allowed"])

	message := strictSettlementFailureLogMessage(
		relayInfo,
		50,
		false,
		errors.New("injected settlement failure"),
	)
	require.Contains(t, message, "manual_reconciliation_required=true")
	require.Contains(t, message, "request_id=strict-settlement-request")
	require.Contains(t, message, "actual_quota=50")
	require.Contains(t, message, "preconsumed_quota=80")
	require.Contains(t, message, "user_id=41")
	require.Contains(t, message, "token_id=42")
	require.Contains(t, message, "channel_id=43")
	require.Contains(t, message, "settlement_attempts=3")
	require.Contains(t, message, "automatic_settlement_retry=false")
	require.Contains(t, message, "upstream_replay_allowed=false")
	require.Contains(t, message, "injected settlement failure")
	require.NotContains(t, message, relayInfo.TokenKey)
}

func TestCalculateTextQuotaSummaryUsesAnthropicUsageSemanticFromUpstreamUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "claude-3-7-sonnet",
		PriceData: types.PriceData{
			ModelRatio:           1,
			CompletionRatio:      2,
			CacheRatio:           0.1,
			CacheCreationRatio:   1.25,
			CacheCreation5mRatio: 1.25,
			CacheCreation1hRatio: 2,
			GroupRatioInfo: types.GroupRatioInfo{
				GroupRatio: 1,
			},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     1000,
		CompletionTokens: 200,
		UsageSemantic:    "anthropic",
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         100,
			CachedCreationTokens: 50,
		},
		ClaudeCacheCreation5mTokens: 10,
		ClaudeCacheCreation1hTokens: 20,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	require.True(t, summary.IsClaudeUsageSemantic)
	require.Equal(t, "anthropic", summary.UsageSemantic)
	require.Equal(t, 1488, summary.Quota)
}

func TestCacheWriteTokensTotal(t *testing.T) {
	t.Run("split cache creation", func(t *testing.T) {
		summary := textQuotaSummary{
			CacheCreationTokens:   50,
			CacheCreationTokens5m: 10,
			CacheCreationTokens1h: 20,
		}
		require.Equal(t, 50, cacheWriteTokensTotal(summary))
	})

	t.Run("legacy cache creation", func(t *testing.T) {
		summary := textQuotaSummary{CacheCreationTokens: 50}
		require.Equal(t, 50, cacheWriteTokensTotal(summary))
	})

	t.Run("split cache creation without aggregate remainder", func(t *testing.T) {
		summary := textQuotaSummary{
			CacheCreationTokens5m: 10,
			CacheCreationTokens1h: 20,
		}
		require.Equal(t, 30, cacheWriteTokensTotal(summary))
	})
}

func TestCalculateTextQuotaSummaryHandlesLegacyClaudeDerivedOpenAIUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		RelayFormat:     types.RelayFormatOpenAI,
		OriginModelName: "claude-3-7-sonnet",
		PriceData: types.PriceData{
			ModelRatio:           1,
			CompletionRatio:      5,
			CacheRatio:           0.1,
			CacheCreationRatio:   1.25,
			CacheCreation5mRatio: 1.25,
			CacheCreation1hRatio: 2,
			GroupRatioInfo:       types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     62,
		CompletionTokens: 95,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 3544,
		},
		ClaudeCacheCreation5mTokens: 586,
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// 62 + 3544*0.1 + 586*1.25 + 95*5 = 1624.9 => 1624
	require.Equal(t, 1624, summary.Quota)
}

func TestCalculateTextQuotaSummarySeparatesOpenRouterCacheReadFromPromptBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "openai/gpt-4.1",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeOpenRouter,
		},
		PriceData: types.PriceData{
			ModelRatio:         1,
			CompletionRatio:    1,
			CacheRatio:         0.1,
			CacheCreationRatio: 1.25,
			GroupRatioInfo:     types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     2604,
		CompletionTokens: 383,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 2432,
		},
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// OpenRouter OpenAI-format display keeps prompt_tokens as total input,
	// but billing still separates normal input from cache read tokens.
	// quota = (2604 - 2432) + 2432*0.1 + 383 = 798.2 => 798
	require.Equal(t, 2604, summary.PromptTokens)
	require.Equal(t, 798, summary.Quota)
}

func TestCalculateTextQuotaSummarySeparatesOpenRouterCacheCreationFromPromptBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "openai/gpt-4.1",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeOpenRouter,
		},
		PriceData: types.PriceData{
			ModelRatio:         1,
			CompletionRatio:    1,
			CacheCreationRatio: 1.25,
			GroupRatioInfo:     types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     2604,
		CompletionTokens: 383,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedCreationTokens: 100,
		},
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// prompt_tokens is still logged as total input, but cache creation is billed separately.
	// quota = (2604 - 100) + 100*1.25 + 383 = 3012
	require.Equal(t, 2604, summary.PromptTokens)
	require.Equal(t, 3012, summary.Quota)
}

func TestCalculateTextQuotaSummaryKeepsPrePRClaudeOpenRouterBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)

	relayInfo := &relaycommon.RelayInfo{
		FinalRequestRelayFormat: types.RelayFormatClaude,
		OriginModelName:         "anthropic/claude-3.7-sonnet",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeOpenRouter,
		},
		PriceData: types.PriceData{
			ModelRatio:         1,
			CompletionRatio:    1,
			CacheRatio:         0.1,
			CacheCreationRatio: 1.25,
			GroupRatioInfo:     types.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	usage := &dto.Usage{
		PromptTokens:     2604,
		CompletionTokens: 383,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 2432,
		},
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, usage)

	// Pre-PR PostClaudeConsumeQuota behavior for OpenRouter:
	// prompt = 2604 - 2432 = 172
	// quota = 172 + 2432*0.1 + 383 = 798.2 => 798
	require.True(t, summary.IsClaudeUsageSemantic)
	require.Equal(t, 172, summary.PromptTokens)
	require.Equal(t, 798, summary.Quota)
}

func TestImageOutputAndCachedInputAreBilledAtSeparateRates(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-image-2", StartTime: time.Now(),
		PriceData: types.PriceData{ChannelSpecific: true, PriceProvider: "yuyu", ModelRatio: 4,
			CompletionRatio: 1, ImageRatio: 1, ImageCompletionRatio: 3.75, CacheRatio: 0.25,
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}}
	usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
		PromptTokensDetails:    dto.InputTokenDetails{ImageTokens: 200, CachedTokens: 100},
		CompletionTokenDetails: dto.OutputTokenDetails{ImageTokens: 400, TextTokens: 100}}
	result := calculateTextQuotaSummary(ctx, info, usage)
	// (900*8 + 100*2 + 100*8 + 400*30) / 1M USD, then 1.25 sales markup.
	require.Equal(t, 12625, result.Quota)
	usage = &dto.Usage{PromptTokens: 18, CompletionTokens: 515, TotalTokens: 533,
		CompletionTokenDetails: dto.OutputTokenDetails{ImageTokens: 515}}
	require.Equal(t, 9746, calculateTextQuotaSummary(ctx, info, usage).Quota)
}
