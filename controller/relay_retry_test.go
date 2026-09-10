package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayhelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestShouldRetryStopsAfterResponseIsCommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	retryable := types.NewErrorWithStatusCode(
		errors.New("upstream failed"),
		types.ErrorCodeChannelResponseTimeExceeded,
		http.StatusInternalServerError,
	)

	require.True(t, shouldRetry(context, retryable, 1))
	_, err := context.Writer.Write([]byte("partial response"))
	require.NoError(t, err)
	require.False(t, shouldRetry(context, retryable, 1))
}

func TestAnthropicResponsesAreRejectedBeforePricing(t *testing.T) {
	for _, relayFormat := range []types.RelayFormat{
		types.RelayFormatOpenAIResponses,
		types.RelayFormatOpenAIResponsesCompaction,
	} {
		err := validateRelayProtocolBeforePricing(relayFormat, constant.ChannelTypeAnthropic)
		require.ErrorContains(t, err, "不支持 OpenAI Responses")
	}
	require.NoError(t, validateRelayProtocolBeforePricing(types.RelayFormatOpenAI, constant.ChannelTypeAnthropic))
	require.NoError(t, validateRelayProtocolBeforePricing(types.RelayFormatOpenAIResponses, constant.ChannelTypeOpenAI))
}

func TestShouldRetryAllowsIncompleteStreamOnlyBeforeResponseIsCommitted(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	relayErr := types.NewErrorWithStatusCode(
		errors.New("stream ended before message_stop"),
		types.ErrorCodeBadResponse,
		http.StatusBadGateway,
	)

	require.True(t, shouldRetry(context, relayErr, 1))
	_, err := context.Writer.Write([]byte("data: partial\n\n"))
	require.NoError(t, err)
	require.False(t, shouldRetry(context, relayErr, 1))
}

func TestResetRelayAttemptResponseStateDropsPreviousChannelAttestation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	context.Writer.Header().Set(common.ReasoningContentSeparatedHeader, "true")
	context.Set("claude_web_search_requests", 3)
	stream := false
	start := time.Now().Add(-time.Minute)
	info := &relaycommon.RelayInfo{
		Request:               &dto.GeneralOpenAIRequest{Stream: &stream},
		IsStream:              true,
		StartTime:             start,
		FirstResponseTime:     time.Now(),
		ReceivedResponseCount: 3,
		StreamStatus:          relaycommon.NewStreamStatus(),
	}

	resetRelayAttemptResponseState(context, info)

	require.Empty(t, context.Writer.Header().Get(common.ReasoningContentSeparatedHeader))
	require.Zero(t, context.GetInt("claude_web_search_requests"))
	require.Equal(t, start, info.StartTime)
	require.False(t, info.IsStream)
	require.False(t, info.HasSendResponse())
	require.Zero(t, info.ReceivedResponseCount)
	require.Nil(t, info.StreamStatus)
	info.SetFirstResponseTime()
	require.True(t, info.HasSendResponse())
}

func TestNormalizeClaudeMaxTokensForPricingMatchesOutboundDefault(t *testing.T) {
	want := model_setting.GetClaudeSettings().GetDefaultMaxTokens("claude-sonnet-4-6")
	require.Positive(t, want)

	native := &dto.ClaudeRequest{Model: "claude-sonnet-4-6"}
	nativeMeta := &types.TokenCountMeta{}
	require.NoError(t, normalizeClaudeMaxTokensForPricing(native, native.Model, nativeMeta))
	require.NotNil(t, native.MaxTokens)
	require.Equal(t, uint(want), *native.MaxTokens)
	require.Equal(t, want, nativeMeta.MaxTokens)

	openAI := &dto.GeneralOpenAIRequest{Model: "claude-sonnet-4-6"}
	openAIMeta := &types.TokenCountMeta{}
	require.NoError(t, normalizeClaudeMaxTokensForPricing(openAI, openAI.Model, openAIMeta))
	require.NotNil(t, openAI.MaxTokens)
	require.Equal(t, uint(want), *openAI.MaxTokens)
	require.Equal(t, want, openAIMeta.MaxTokens)

	responses := &dto.OpenAIResponsesRequest{Model: "claude-sonnet-4-6"}
	responsesMeta := &types.TokenCountMeta{}
	require.NoError(t, normalizeClaudeMaxTokensForPricing(responses, responses.Model, responsesMeta))
	require.NotNil(t, responses.MaxOutputTokens)
	require.Equal(t, uint(want), *responses.MaxOutputTokens)
	require.Equal(t, want, responsesMeta.MaxTokens)
}

func TestNormalizeClaudeMaxTokensForPricingPreservesExplicitLimit(t *testing.T) {
	explicit := uint(7)
	request := &dto.ClaudeRequest{Model: "claude-sonnet-4-6", MaxTokens: &explicit}
	meta := &types.TokenCountMeta{MaxTokens: int(explicit)}

	require.NoError(t, normalizeClaudeMaxTokensForPricing(request, request.Model, meta))
	require.Equal(t, explicit, *request.MaxTokens)
	require.Equal(t, int(explicit), meta.MaxTokens)
}

func TestNormalizeClaudeMaxTokensForPricingMatchesThinkingMinimum(t *testing.T) {
	explicit := uint(7)
	request := &dto.ClaudeRequest{Model: "claude-sonnet-4-5-thinking", MaxTokens: &explicit}
	meta := &types.TokenCountMeta{MaxTokens: int(explicit)}

	require.NoError(t, normalizeClaudeMaxTokensForPricing(request, request.Model, meta))
	require.Equal(t, uint(1280), *request.MaxTokens)
	require.Equal(t, 1280, meta.MaxTokens)
}

func TestNormalizeClaudeMaxTokensForPricingUpdatesOpenAICompletionLimit(t *testing.T) {
	explicit := uint(7)
	request := &dto.GeneralOpenAIRequest{
		Model:               "claude-sonnet-4-5-thinking",
		MaxCompletionTokens: &explicit,
	}
	meta := &types.TokenCountMeta{MaxTokens: int(explicit)}

	require.NoError(t, normalizeClaudeMaxTokensForPricing(request, request.Model, meta))
	require.NotNil(t, request.MaxCompletionTokens)
	require.Equal(t, uint(1280), *request.MaxCompletionTokens)
	require.Nil(t, request.MaxTokens)
	require.Equal(t, 1280, meta.MaxTokens)
}

func TestGetChannelAppliesPriceToInitialAndFallbackAttempts(t *testing.T) {
	previousDB := model.DB
	previousCacheSetting := common.MemoryCacheEnabled
	previousRetryTimes := common.RetryTimes
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelModelPrice{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	common.RetryTimes = 1
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousCacheSetting
		common.RetryTimes = previousRetryTimes
		if previousCacheSetting {
			model.InitChannelCache()
		}
	})

	highPriority := int64(600)
	lowPriority := int64(550)
	weight := uint(100)
	channels := []model.Channel{
		{Id: 1, Name: "ZenMux", Key: "zen-key", Status: common.ChannelStatusEnabled, Priority: &highPriority, Weight: &weight, Group: "default", Models: "claude-sonnet-4-6"},
		{Id: 2, Name: "TabCode", Key: "tab-key", Status: common.ChannelStatusEnabled, Priority: &lowPriority, Weight: &weight, Group: "default", Models: "claude-sonnet-4-6"},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "default", Model: "claude-sonnet-4-6", ChannelId: 1, Enabled: true, Priority: &highPriority, Weight: weight},
		{Group: "default", Model: "claude-sonnet-4-6", ChannelId: 2, Enabled: true, Priority: &lowPriority, Weight: weight},
	}).Error)
	require.NoError(t, db.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "zenmux", UpstreamModelID: "anthropic/claude-sonnet-4.6", BillingType: model.ChannelModelBillingToken, InputPrice: 3, OutputPrice: 15, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: "tabcode", UpstreamModelID: "claude-sonnet-4-6", BillingType: model.ChannelModelBillingToken, InputPrice: 2.25, OutputPrice: 11.25, Available: true},
	}).Error)
	model.InitChannelCache()

	newContext := func() *gin.Context {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		context.Set("channel_id", 1)
		context.Set("channel_name", "ZenMux")
		context.Set("channel_type", 14)
		context.Set("auto_ban", true)
		return context
	}
	newInfo := func(context *gin.Context) *relaycommon.RelayInfo {
		info := &relaycommon.RelayInfo{
			OriginModelName: "claude-sonnet-4-6",
			TokenGroup:      "default",
			UsingGroup:      "default",
			UserGroup:       "default",
		}
		price, err := relayhelper.ModelPriceHelper(context, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})
		require.NoError(t, err)
		require.Equal(t, 9000, price.QuotaToPreConsume)
		return info
	}

	context := newContext()
	info := newInfo(context)
	retry := 0
	channel, relayErr := getChannel(context, info, &service.RetryParam{
		Ctx: context, TokenGroup: "default", ModelName: "claude-sonnet-4-6", Retry: &retry,
	})
	require.Nil(t, relayErr)
	require.Equal(t, 1, channel.Id)
	require.Equal(t, 1.5, info.PriceData.ModelRatio)
	require.Equal(t, 5.0, info.PriceData.CompletionRatio)
	require.Equal(t, 9000, info.PriceData.QuotaToPreConsume)

	context = newContext()
	info = newInfo(context)
	require.NoError(t, db.Model(&model.ChannelModelPrice{}).Where("channel_id = ?", 1).Update("available", false).Error)
	retry = 0
	channel, relayErr = getChannel(context, info, &service.RetryParam{
		Ctx: context, TokenGroup: "default", ModelName: "claude-sonnet-4-6", Retry: &retry,
	})
	require.Nil(t, relayErr)
	require.Equal(t, 2, channel.Id)
	require.Equal(t, 1, retry)
	require.Equal(t, []string{"1"}, context.GetStringSlice("use_channel"))
	require.Equal(t, 1.125, info.PriceData.ModelRatio)
	require.Equal(t, 5.0, info.PriceData.CompletionRatio)
	require.Equal(t, 9000, info.PriceData.QuotaToPreConsume)

	context = newContext()
	common.SetContextKey(context, constant.ContextKeyTokenSpecificChannelId, "1")
	require.NoError(t, db.Model(&model.ChannelModelPrice{}).Where("channel_id = ?", 1).Update("available", true).Error)
	info = newInfo(context)
	require.NoError(t, db.Model(&model.ChannelModelPrice{}).Where("channel_id = ?", 1).Update("available", false).Error)
	retry = 0
	channel, relayErr = getChannel(context, info, &service.RetryParam{
		Ctx: context, TokenGroup: "default", ModelName: "claude-sonnet-4-6", Retry: &retry,
	})
	require.Nil(t, channel)
	require.NotNil(t, relayErr)
	require.Equal(t, 0, retry)
	require.Empty(t, context.GetStringSlice("use_channel"))

	context = newContext()
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"metadata":{"user_id":"strict-price-test-`+t.Name()+`"}}`),
	)
	context.Request.Header.Set("Content-Type", "application/json")
	_, affinityFound := service.GetPreferredChannelByAffinity(context, "claude-sonnet-4-6", "default")
	require.False(t, affinityFound)
	require.True(t, service.ShouldSkipRetryAfterChannelAffinityFailure(context))
	require.NoError(t, db.Model(&model.ChannelModelPrice{}).Where("channel_id = ?", 1).Update("available", true).Error)
	info = newInfo(context)
	require.NoError(t, db.Model(&model.ChannelModelPrice{}).Where("channel_id = ?", 1).Update("available", false).Error)
	retry = 0
	channel, relayErr = getChannel(context, info, &service.RetryParam{
		Ctx: context, TokenGroup: "default", ModelName: "claude-sonnet-4-6", Retry: &retry,
	})
	require.Nil(t, channel)
	require.NotNil(t, relayErr)
	require.Equal(t, 0, retry)
	require.Empty(t, context.GetStringSlice("use_channel"))
}

func TestAutoRetryUsesGlobalBudgetAndFinalGroupRatio(t *testing.T) {
	previousDB := model.DB
	previousCacheSetting := common.MemoryCacheEnabled
	previousRetryTimes := common.RetryTimes
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	previousGroupRatios := ratio_setting.GroupRatio2JSONString()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelModelPrice{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	common.RetryTimes = 1
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["route-a","route-b"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"route-a":"A","route-b":"B"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"route-a":1,"route-b":1.75}`))
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousCacheSetting
		common.RetryTimes = previousRetryTimes
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroupRatios))
		if previousCacheSetting {
			model.InitChannelCache()
		}
	})

	zenPriority := int64(600)
	tabPriority := int64(550)
	weight := uint(100)
	zenBaseURL := "https://zenmux.ai/api/anthropic"
	tabBaseURL := "https://api2.tabcode.cc/claude/kiropower"
	zenTag := "mujian-provider:zenmux:chat"
	tabTag := "mujian-provider:tabcode:chat"
	zenHeaders := `{"anthropic-version":"2023-06-01"}`
	tabHeaders := `{"Authorization":"Bearer {api_key}","anthropic-version":"2023-06-01"}`
	zenMapping := `{"claude-sonnet-4-6":"anthropic/claude-sonnet-4.6"}`
	tabMapping := `{"claude-sonnet-4-6":"claude-sonnet-4-6"}`
	testModel := "claude-sonnet-4-6"
	channels := []model.Channel{
		{Id: 11, Type: constant.ChannelTypeAnthropic, Name: "route-a", Key: "key-a", Status: common.ChannelStatusEnabled, Priority: &zenPriority, Weight: &weight, Group: "route-a", Models: "claude-sonnet-4-6", BaseURL: &zenBaseURL, Tag: &zenTag, HeaderOverride: &zenHeaders, ModelMapping: &zenMapping, TestModel: &testModel, TestTime: 200},
		{Id: 12, Type: constant.ChannelTypeAnthropic, Name: "route-b", Key: "key-b", Status: common.ChannelStatusEnabled, Priority: &tabPriority, Weight: &weight, Group: "route-b", Models: "claude-sonnet-4-6", BaseURL: &tabBaseURL, Tag: &tabTag, HeaderOverride: &tabHeaders, ModelMapping: &tabMapping, TestModel: &testModel, TestTime: 200},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "route-a", Model: "claude-sonnet-4-6", ChannelId: 11, Enabled: true, Priority: &zenPriority, Weight: weight},
		{Group: "route-b", Model: "claude-sonnet-4-6", ChannelId: 12, Enabled: true, Priority: &tabPriority, Weight: weight},
	}).Error)
	require.NoError(t, db.Create(&[]model.ChannelModelPrice{
		{ChannelID: 11, CatalogID: "claude-sonnet-4-6", Provider: "zenmux", UpstreamModelID: "anthropic/claude-sonnet-4.6", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 3, OutputPrice: 15, CacheRatio: 0.1, CacheCreationRatio: 1.25, Available: true, SyncedAt: 100, TestedAt: 100},
		{ChannelID: 12, CatalogID: "claude-sonnet-4-6", Provider: "tabcode", UpstreamModelID: "claude-sonnet-4-6", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2.25, OutputPrice: 11.25, CacheRatio: 0.1, CacheCreationRatio: 1.25, Available: true, SyncedAt: 100, TestedAt: 100},
	}).Error)
	model.InitChannelCache()

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	common.SetContextKey(context, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(context, constant.ContextKeyUsingGroup, "auto")
	common.SetContextKey(context, constant.ContextKeyAutoGroup, "route-a")
	common.SetContextKey(context, constant.ContextKeyTokenCrossGroupRetry, true)
	selected := channels[0]
	require.Nil(t, middleware.SetupContextForSelectedChannel(context, &selected, "claude-sonnet-4-6"))

	retry := 0
	retryParam := &service.RetryParam{
		Ctx: context, TokenGroup: "auto", ModelName: "claude-sonnet-4-6", Retry: &retry,
	}
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6",
		TokenGroup:      "auto",
		UsingGroup:      "route-a",
		UserGroup:       "default",
	}
	price, err := relayhelper.ModelPriceHelper(context, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})
	require.NoError(t, err)
	require.True(t, price.ChannelSpecific)

	upstreamAttempts := 0
	first, relayErr := getChannel(context, info, retryParam)
	require.Nil(t, relayErr)
	require.Equal(t, 11, first.Id)
	require.Equal(t, "route-a", info.UsingGroup)
	require.Equal(t, 1.0, info.PriceData.GroupRatioInfo.GroupRatio)
	addUsedChannel(context, first.Id)
	upstreamAttempts++
	retryParam.IncreaseRetry()
	info.ChannelMeta = &relaycommon.ChannelMeta{}

	second, relayErr := getChannel(context, info, retryParam)
	require.Nil(t, relayErr)
	require.Equal(t, 12, second.Id)
	require.Equal(t, "route-b", info.UsingGroup)
	require.Equal(t, 1.75, info.PriceData.GroupRatioInfo.GroupRatio)
	addUsedChannel(context, second.Id)
	upstreamAttempts++
	retryParam.IncreaseRetry()

	require.Equal(t, 2, retryParam.GetRetry())
	require.Greater(t, retryParam.GetRetry(), common.RetryTimes)
	require.LessOrEqual(t, upstreamAttempts, common.RetryTimes+1)
	require.Len(t, context.GetStringSlice("use_channel"), common.RetryTimes+1)
}

func TestWriteRelayStreamErrorUsesSSEAndDoesNotAppendDone(t *testing.T) {
	for _, test := range []struct {
		name        string
		relayFormat types.RelayFormat
		wantEvent   string
	}{
		{name: "OpenAI", relayFormat: types.RelayFormatOpenAI, wantEvent: "data: {\"error\":"},
		{name: "Claude", relayFormat: types.RelayFormatClaude, wantEvent: "event: error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			relayErr := types.NewErrorWithStatusCode(
				errors.New("upstream stream interrupted"), types.ErrorCodeBadResponseBody, http.StatusBadGateway,
			)

			writeRelayStreamError(context, test.relayFormat, relayErr)

			require.Contains(t, recorder.Body.String(), test.wantEvent)
			require.NotContains(t, recorder.Body.String(), "[DONE]")
		})
	}
}

func TestManagedClaudeStreamErrorIsNotAppliedToOtherProtocols(t *testing.T) {
	managed := &relaycommon.RelayInfo{
		IsStream:                true,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{
			ManagedProvider: true,
		},
	}
	require.True(t, shouldWriteManagedClaudeStreamError(managed, types.RelayFormatClaude))
	require.True(t, shouldWriteManagedClaudeStreamError(managed, types.RelayFormatOpenAI))
	require.False(t, shouldWriteManagedClaudeStreamError(managed, types.RelayFormatOpenAIResponses))
	require.False(t, shouldWriteManagedClaudeStreamError(managed, types.RelayFormatGemini))
	managed.FinalRequestRelayFormat = types.RelayFormatOpenAI
	require.False(t, shouldWriteManagedClaudeStreamError(managed, types.RelayFormatOpenAI))

	ordinary := &relaycommon.RelayInfo{
		IsStream:                true,
		FinalRequestRelayFormat: types.RelayFormatClaude,
		ChannelMeta:             &relaycommon.ChannelMeta{},
	}
	require.False(t, shouldWriteManagedClaudeStreamError(ordinary, types.RelayFormatOpenAI))
}

func TestCanceledRelayRequestNeverRetriesOrRecordsAChannelFailure(t *testing.T) {
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(requestContext)
	relayErr := types.NewErrorWithStatusCode(
		context.Canceled, types.ErrorCodeBadResponse, http.StatusBadGateway,
	)

	require.True(t, relayRequestCanceled(ginContext))
	require.False(t, shouldRetry(ginContext, relayErr, 1))
}

type downstreamErrorWriter struct {
	gin.ResponseWriter
}

func (writer *downstreamErrorWriter) Write([]byte) (int, error) {
	return 0, errors.New("downstream unavailable")
}

func TestDownstreamWriteFailureNeverRetries(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	context.Writer = &downstreamErrorWriter{ResponseWriter: context.Writer}

	require.Error(t, relayhelper.StringData(context, "partial"))
	require.True(t, relayhelper.HasDownstreamWriteFailure(context))
	require.True(t, relayRequestTerminated(context))
	relayErr := types.NewErrorWithStatusCode(
		errors.New("write failed"), types.ErrorCodeBadResponseBody, http.StatusBadGateway,
	)
	require.False(t, shouldRetry(context, relayErr, 1))
}
