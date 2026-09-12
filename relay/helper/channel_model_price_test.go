package helper

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelPriceDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelModelPrice{}))
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
}

func TestOrdinaryModelPricingDoesNotQueryProviderSnapshotTables(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	setTestLegacyTokenRatios(t, "gpt-4o", 1, 1, 0, 0)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-4o",
		UserGroup:       "default",
		UsingGroup:      "default",
	}

	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.False(t, price.ChannelSpecific)
}

func bindManagedTestPrice(t *testing.T, ctx *gin.Context, channelID int, catalogID, group string) {
	t.Helper()
	price, err := model.GetAvailableChannelModelPrice(channelID, catalogID)
	require.NoError(t, err)
	common.SetContextKey(ctx, constant.ContextKeyManagedChannelPrice, types.ChannelModelPriceSnapshot{
		PriceID: price.ID, ChannelID: channelID, CatalogID: catalogID,
		UpstreamModelID: price.UpstreamModelID, Provider: price.Provider,
		BillingType: price.BillingType, Currency: price.Currency, RoutingGroup: group,
		InputPrice: price.InputPrice, OutputPrice: price.OutputPrice, FixedPrice: price.FixedPrice,
		CacheRatio: price.CacheRatio, CacheCreationRatio: price.CacheCreationRatio,
	})
}

func TestStrictCatalogUsesDatabasePriceAndRequestBoundSettlementSnapshot(t *testing.T) {
	previousCacheSetting := common.MemoryCacheEnabled
	t.Cleanup(func() { common.MemoryCacheEnabled = previousCacheSetting })
	setupChannelPriceDB(t)
	common.MemoryCacheEnabled = true
	priority := int64(600)
	tag := "mujian-provider:zenmux:chat"
	channel := model.Channel{
		Id: 1, Key: "managed", Name: "managed", Group: "default", Models: "claude-sonnet-4-6",
		Status: common.ChannelStatusEnabled, Priority: &priority, Tag: &tag,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderZenMux,
		UpstreamModelID: "anthropic/claude-sonnet-4.6", BillingType: model.ChannelModelBillingToken,
		Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true,
	}).Error)
	addTestAbility(t, channel.Id, "default", "claude-sonnet-4-6", true, &priority)
	model.InitChannelCache()

	// Simulate a price update committed by another instance without refreshing
	// this process's local cache.
	require.NoError(t, model.DB.Model(&model.ChannelModelPrice{}).
		Where("channel_id = ? AND catalog_id = ?", channel.Id, "claude-sonnet-4-6").
		Updates(map[string]any{"input_price": 6.0, "output_price": 12.0}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, channel.Id)
	ctx.Set("mujian_managed_provider", true)
	bindManagedTestPrice(t, ctx, channel.Id, "claude-sonnet-4-6", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default",
	}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})
	require.NoError(t, err)
	require.Equal(t, 11250, price.QuotaToPreConsume)

	require.NoError(t, ApplyChannelModelPrice(ctx, info, channel.Id, true))
	require.Equal(t, 3.0, info.PriceData.ModelRatio)
	require.Equal(t, 2.0, info.PriceData.CompletionRatio)
	require.Equal(t, 11250, info.PriceData.QuotaToPreConsume)
}

func TestModelPriceHelperKeepsSelectedAutoGroupAfterConfigChange(t *testing.T) {
	setupChannelPriceDB(t)
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["route-b"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"route-a":"A","route-b":"B"}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
	})

	priority := int64(600)
	channels := []model.Channel{
		{Id: 1, Key: "selected", Name: "selected", Group: "route-a", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 2, Key: "remaining", Name: "remaining", Group: "route-b", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderZenMux, UpstreamModelID: "anthropic/claude-sonnet-4.6", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 6, OutputPrice: 12, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderTabCode, UpstreamModelID: "claude-sonnet-4-6", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true},
	}).Error)
	addTestAbility(t, 1, "route-a", "claude-sonnet-4-6", true, &priority)
	addTestAbility(t, 2, "route-b", "claude-sonnet-4-6", true, &priority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "route-a")
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)
	ctx.Set("mujian_managed_provider", true)
	bindManagedTestPrice(t, ctx, 1, "claude-sonnet-4-6", "route-a")
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6", TokenGroup: "auto",
		UserGroup: "default", UsingGroup: "route-a",
	}

	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.True(t, price.ChannelSpecific)
	require.Equal(t, 11250, price.QuotaToPreConsume)
}

func TestAutoGroupPreauthorizationExcludesInitialChannelFromLaterGroup(t *testing.T) {
	setupChannelPriceDB(t)
	previousRetryTimes := common.RetryTimes
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	common.RetryTimes = 1
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["route-a","route-b"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"route-a":"A","route-b":"B"}`))
	t.Cleanup(func() {
		common.RetryTimes = previousRetryTimes
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
	})

	primaryPriority, backupPriority := int64(600), int64(550)
	channels := []model.Channel{
		{Id: 1, Key: "shared", Name: "shared", Group: "route-a", Status: common.ChannelStatusEnabled, Priority: &primaryPriority},
		{Id: 2, Key: "backup", Name: "backup", Group: "route-b", Status: common.ChannelStatusEnabled, Priority: &backupPriority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "shared", UpstreamModelID: "shared", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: "backup", UpstreamModelID: "backup", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 3, OutputPrice: 6, Available: true},
	}).Error)
	addTestAbility(t, 1, "route-a", "claude-sonnet-4-6", true, &primaryPriority)
	addTestAbility(t, 1, "route-b", "claude-sonnet-4-6", true, &primaryPriority)
	addTestAbility(t, 2, "route-b", "claude-sonnet-4-6", true, &backupPriority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "route-a")
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6", TokenGroup: "auto",
		UsingGroup: "route-a", UserGroup: "default",
	}

	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 5625, price.QuotaToPreConsume)
	bounds, err := priceAuthorizationForRequest(ctx)
	require.NoError(t, err)
	require.True(t, bounds.authorizes(1, "route-a"))
	require.False(t, bounds.authorizes(1, "route-b"))
	require.True(t, bounds.authorizes(2, "route-b"))
}

func addTestAbility(t *testing.T, channelID int, group, modelID string, enabled bool, priority *int64) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: group, Model: modelID, ChannelId: channelID, Enabled: enabled, Priority: priority,
	}).Error)
}

func setTestModelPrice(t *testing.T, modelID string, price float64) {
	t.Helper()
	previous := ratio_setting.ModelPrice2JSONString()
	prices := ratio_setting.GetModelPriceMap()
	prices[modelID] = price
	raw, err := common.Marshal(prices)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(raw)))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previous)) })
}

func setTestLegacyTokenRatios(t *testing.T, modelID string, modelRatio, completionRatio, cacheRatio, cacheCreationRatio float64) {
	t.Helper()
	previousModelPrices := ratio_setting.ModelPrice2JSONString()
	previousModelRatios := ratio_setting.ModelRatio2JSONString()
	previousCompletionRatios := ratio_setting.CompletionRatio2JSONString()
	previousCacheRatios := ratio_setting.CacheRatio2JSONString()
	previousCacheCreationRatios := ratio_setting.CreateCacheRatio2JSONString()
	encode := func(values map[string]float64) string {
		raw, err := common.Marshal(values)
		require.NoError(t, err)
		return string(raw)
	}
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(encode(map[string]float64{modelID: modelRatio})))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(encode(map[string]float64{modelID: completionRatio})))
	require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(encode(map[string]float64{modelID: cacheRatio})))
	require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(encode(map[string]float64{modelID: cacheCreationRatio})))
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousModelPrices))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousModelRatios))
		require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(previousCompletionRatios))
		require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(previousCacheRatios))
		require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(previousCacheCreationRatios))
	})
}

func TestChannelModelPricePreauthorizesWorstCandidateAndSettlesSelected(t *testing.T) {
	setupChannelPriceDB(t)
	priorityA, priorityB := int64(200), int64(100)
	channels := []model.Channel{
		{Id: 1, Key: "test-a", Name: "A", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorityA},
		{Id: 2, Key: "test-b", Name: "B", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorityB},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	prices := []model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "test-model", Provider: "a", UpstreamModelID: "test-model-a", BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "test-model", Provider: "b", UpstreamModelID: "test-model-b", BillingType: model.ChannelModelBillingToken, InputPrice: 1, OutputPrice: 10, Available: true},
	}
	require.NoError(t, model.DB.Create(&prices).Error)
	addTestAbility(t, 1, "default", "test-model", true, &priorityA)
	addTestAbility(t, 2, "default", "test-model", true, &priorityB)

	group := types.GroupRatioInfo{GroupRatio: 1}
	preauth, found, err := channelModelPreConsumePrice("test-model", "default", nil, 1000, &types.TokenCountMeta{MaxTokens: 1000}, group)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, preauth.ChannelSpecific)
	require.Equal(t, 10.0, preauth.CompletionRatio)
	require.Equal(t, 6875, preauth.QuotaToPreConsume)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	bindPriceAuthorizationBounds(ctx, 1000, &types.TokenCountMeta{MaxTokens: 1000}, []priceAuthorizedRoute{
		{ChannelID: 1, RoutingGroup: "default"},
		{ChannelID: 2, RoutingGroup: "default"},
	})
	info := &relaycommon.RelayInfo{OriginModelName: "test-model", UsingGroup: "default", PriceData: preauth}
	require.NoError(t, ApplyChannelModelPrice(ctx, info, 1, false))
	require.Equal(t, 1.0, info.PriceData.ModelRatio)
	require.Equal(t, 2.0, info.PriceData.CompletionRatio)
	require.Equal(t, 6875, info.PriceData.QuotaToPreConsume)
}

func TestApplyChannelModelPriceRejectsConcurrentOrdinaryPriceIncrease(t *testing.T) {
	setupChannelPriceDB(t)
	priority := int64(100)
	channel := model.Channel{
		Id: 1, Key: "ordinary", Name: "ordinary", Group: "default",
		Status: common.ChannelStatusEnabled, Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", Provider: "ordinary",
		UpstreamModelID: "claude-sonnet-4-6", BillingType: model.ChannelModelBillingToken,
		InputPrice: 2, OutputPrice: 4, Available: true,
	}).Error)
	addTestAbility(t, channel.Id, "default", "claude-sonnet-4-6", true, &priority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, channel.Id)
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6", UserGroup: "default", UsingGroup: "default",
	}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})
	require.NoError(t, err)
	require.Equal(t, 3750, price.QuotaToPreConsume)

	require.NoError(t, model.DB.Model(&model.ChannelModelPrice{}).
		Where("channel_id = ? AND catalog_id = ?", channel.Id, "claude-sonnet-4-6").
		Updates(map[string]any{"input_price": 6.0, "output_price": 12.0}).Error)

	err = ApplyChannelModelPrice(ctx, info, channel.Id, false)
	require.ErrorContains(t, err, "超过本次请求已预授权额度")
	require.Equal(t, 3750, info.PriceData.QuotaToPreConsume)
}

func TestApplyChannelModelPriceRejectsNewHigherPricedManagedRetry(t *testing.T) {
	setupChannelPriceDB(t)
	priority := int64(100)
	initial := model.Channel{
		Id: 1, Key: "initial", Name: "initial", Group: "default",
		Status: common.ChannelStatusEnabled, Priority: &priority,
	}
	retry := model.Channel{
		Id: 2, Key: "retry", Name: "retry", Group: "default",
		Status: common.ChannelStatusManuallyDisabled, Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&[]model.Channel{initial, retry}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: initial.Id, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderZenMux,
		UpstreamModelID: "anthropic/claude-sonnet-4.6", BillingType: model.ChannelModelBillingToken,
		Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true,
	}).Error)
	addTestAbility(t, initial.Id, "default", "claude-sonnet-4-6", true, &priority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, initial.Id)
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6", UserGroup: "default", UsingGroup: "default",
	}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})
	require.NoError(t, err)
	require.Equal(t, 3750, price.QuotaToPreConsume)

	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: retry.Id, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderTabCode,
		UpstreamModelID: "claude-sonnet-4-6", BillingType: model.ChannelModelBillingToken,
		Currency: "USD", InputPrice: 10, OutputPrice: 20, Available: true,
	}).Error)
	bindManagedTestPrice(t, ctx, retry.Id, "claude-sonnet-4-6", "default")

	err = ApplyChannelModelPrice(ctx, info, retry.Id, true)
	require.ErrorContains(t, err, "超过本次请求已预授权额度")
	require.Equal(t, 3750, info.PriceData.QuotaToPreConsume)
}

func TestMixedSnapshotAndOrdinaryRoutesKeepLegacyFallback(t *testing.T) {
	setupChannelPriceDB(t)
	setTestModelPrice(t, "claude-sonnet-4-6", 0.02)
	previousRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = previousRetryTimes })
	priorityManaged, priorityOrdinary := int64(600), int64(500)
	managedTag := "mujian-provider:zenmux:chat"
	channels := []model.Channel{
		{Id: 1, Key: "managed", Name: "managed", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorityManaged, Tag: &managedTag},
		{Id: 2, Key: "ordinary", Name: "ordinary", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorityOrdinary},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderZenMux, UpstreamModelID: "anthropic/claude-sonnet-4.6",
		BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true,
	}).Error)
	addTestAbility(t, 1, "default", "claude-sonnet-4-6", true, &priorityManaged)
	addTestAbility(t, 2, "default", "claude-sonnet-4-6", true, &priorityOrdinary)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	ctx.Set("mujian_managed_provider", true)
	bindManagedTestPrice(t, ctx, 1, "claude-sonnet-4-6", "default")
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default"}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.True(t, price.ChannelSpecific)
	require.NotNil(t, info.LegacyPriceData)
	// The ordinary fixed price ($0.02) is more expensive than the managed
	// token snapshot for these bounds and therefore defines preauthorization.
	require.Equal(t, 10000, price.QuotaToPreConsume)

	bindManagedTestPrice(t, ctx, 1, "claude-sonnet-4-6", "default")
	require.NoError(t, ApplyChannelModelPrice(ctx, info, 1, true))
	require.Equal(t, types.PriceProviderZenMux, info.PriceData.PriceProvider)
	require.False(t, info.PriceData.UsePrice)
	require.Equal(t, 10000, info.PriceData.QuotaToPreConsume)

	require.NoError(t, ApplyChannelModelPrice(ctx, info, 2, false))
	require.Empty(t, info.PriceData.PriceProvider)
	require.True(t, info.PriceData.UsePrice)
	require.Equal(t, 0.02, info.PriceData.ModelPrice)
	require.True(t, info.PriceData.ChannelSpecific)
	require.Equal(t, 10000, info.PriceData.QuotaToPreConsume)

	// Rebinding must also work in the opposite retry direction.
	require.NoError(t, ApplyChannelModelPrice(ctx, info, 1, true))
	require.Equal(t, types.PriceProviderZenMux, info.PriceData.PriceProvider)
}

func TestStrictManagedRouteNeverUsesLegacyFallback(t *testing.T) {
	setupChannelPriceDB(t)
	legacy := types.PriceData{UsePrice: true, ModelPrice: 0.02, UnitPriceMultiplier: 1}
	info := &relaycommon.RelayInfo{
		OriginModelName: "missing-model",
		PriceData: types.PriceData{
			ChannelSpecific: true, QuotaToPreConsume: 10000,
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
		},
		LegacyPriceData: &legacy,
	}

	require.EqualError(t, ApplyChannelModelPrice(nil, info, 99, true), "所选渠道缺少已验证的模型价格")
}

func TestSnapshotOnlyRoutesIgnoreLegacyGlobalPrice(t *testing.T) {
	setupChannelPriceDB(t)
	setTestModelPrice(t, "claude-sonnet-4-6", 1)
	priority := int64(600)
	managedTag := "mujian-provider:zenmux:chat"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 1, Key: "managed", Name: "managed", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority, Tag: &managedTag,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderZenMux, UpstreamModelID: "anthropic/claude-sonnet-4.6",
		BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true,
	}).Error)
	addTestAbility(t, 1, "default", "claude-sonnet-4-6", true, &priority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	ctx.Set("mujian_managed_provider", true)
	bindManagedTestPrice(t, ctx, 1, "claude-sonnet-4-6", "default")
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default"}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 3750, price.QuotaToPreConsume)
	require.Nil(t, info.LegacyPriceData)
}

func TestAllowedChannelIDsExcludeOrdinaryLegacyFallback(t *testing.T) {
	setupChannelPriceDB(t)
	setTestModelPrice(t, "claude-sonnet-4-6", 1)
	priority := int64(100)
	channels := []model.Channel{
		{Id: 1, Key: "snapshot", Name: "snapshot", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 2, Key: "ordinary", Name: "ordinary", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "priced", UpstreamModelID: "claude-sonnet-4-6",
		BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true,
	}).Error)
	addTestAbility(t, 1, "default", "claude-sonnet-4-6", true, &priority)
	addTestAbility(t, 2, "default", "claude-sonnet-4-6", true, &priority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyAllowedChannelIds, []int{1})
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default"}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 3750, price.QuotaToPreConsume)
	require.Nil(t, info.LegacyPriceData)
}

func TestSpecificChannelPreauthorizesOnlyThatChannel(t *testing.T) {
	setupChannelPriceDB(t)
	priorityHigh, priorityLow := int64(600), int64(550)
	channels := []model.Channel{
		{Id: 1, Key: "selected", Name: "selected", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorityHigh},
		{Id: 2, Key: "unreachable", Name: "unreachable", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorityLow},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "selected", UpstreamModelID: "selected", BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: "unreachable", UpstreamModelID: "unreachable", BillingType: model.ChannelModelBillingToken, InputPrice: 100, OutputPrice: 200, Available: true},
	}).Error)
	addTestAbility(t, 1, "default", "claude-sonnet-4-6", true, &priorityHigh)
	addTestAbility(t, 2, "default", "claude-sonnet-4-6", true, &priorityLow)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	common.SetContextKey(ctx, constant.ContextKeyTokenSpecificChannelId, "1")
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default"}

	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 3750, price.QuotaToPreConsume)
	bounds, err := priceAuthorizationForRequest(ctx)
	require.NoError(t, err)
	require.True(t, bounds.authorizes(1, "default"))
	require.False(t, bounds.authorizes(2, "default"))
}

func TestSingleRetryPreauthorizationExcludesLowerUnreachablePriority(t *testing.T) {
	setupChannelPriceDB(t)
	previousRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = previousRetryTimes })
	priorities := []int64{600, 550, 100}
	zenTag := "mujian-provider:zenmux:chat"
	tabTag := "mujian-provider:tabcode:chat"
	tabBaseURL := "https://api2.tabcode.cc/claude/kiropower"
	tabHeaders := `{"Authorization":"Bearer {api_key}","anthropic-version":"2023-06-01"}`
	tabMapping := `{"claude-sonnet-4-6":"claude-sonnet-4-6"}`
	testModel := "claude-sonnet-4-6"
	channels := []model.Channel{
		{Id: 1, Key: "primary", Name: "primary", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorities[0], Tag: &zenTag},
		{Id: 2, Type: constant.ChannelTypeAnthropic, Key: "backup", Name: "backup", Group: "default", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priorities[1], Tag: &tabTag, BaseURL: &tabBaseURL, HeaderOverride: &tabHeaders, ModelMapping: &tabMapping, TestModel: &testModel, TestTime: 200},
		{Id: 3, Key: "lower", Name: "lower", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorities[2]},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderZenMux, UpstreamModelID: "anthropic/claude-sonnet-4.6", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderTabCode, UpstreamModelID: "claude-sonnet-4-6", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 3, OutputPrice: 6, Available: true, SyncedAt: 100, TestedAt: 100},
		{ChannelID: 3, CatalogID: "claude-sonnet-4-6", Provider: "lower", UpstreamModelID: "lower", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 100, OutputPrice: 200, Available: true},
	}).Error)
	for index, channel := range channels {
		addTestAbility(t, channel.Id, "default", "claude-sonnet-4-6", true, &priorities[index])
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	ctx.Set("mujian_managed_provider", true)
	bindManagedTestPrice(t, ctx, 1, "claude-sonnet-4-6", "default")
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default"}

	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 5625, price.QuotaToPreConsume)
	bounds, err := priceAuthorizationForRequest(ctx)
	require.NoError(t, err)
	require.True(t, bounds.authorizes(1, "default"))
	require.True(t, bounds.authorizes(2, "default"))
	require.False(t, bounds.authorizes(3, "default"))
}

func TestClaudeWebSearchDetectionCoversNativeAndOpenAIShapes(t *testing.T) {
	tests := []struct {
		name    string
		request dto.Request
	}{
		{
			name: "native single tool object",
			request: &dto.ClaudeRequest{Tools: map[string]any{
				"type": "web_search_20250305",
			}},
		},
		{
			name:    "chat completions web search options",
			request: &dto.GeneralOpenAIRequest{WebSearchOptions: &dto.WebSearchOptions{}},
		},
		{
			name: "responses tool array",
			request: &dto.OpenAIResponsesRequest{Tools: []byte(`[
				{"type":"web_search_preview"}
			]`)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usesWebSearch, err := requestUsesClaudeWebSearch(test.request)
			require.NoError(t, err)
			require.True(t, usesWebSearch)
		})
	}

	usesWebSearch, err := requestUsesClaudeWebSearch(&dto.ClaudeRequest{Tools: []map[string]any{{"type": "custom"}}})
	require.NoError(t, err)
	require.False(t, usesWebSearch)

	_, err = requestUsesClaudeWebSearch(&dto.ClaudeRequest{Tools: "invalid"})
	require.ErrorContains(t, err, "格式无法用于价格预授权")
}

func TestClaudeWebSearchExcludesStrictProvidersFromRetryAuthorization(t *testing.T) {
	routes := []routableChannelPriceView{
		{
			channelID: 1,
			price: &model.ChannelModelPrice{
				Provider: types.PriceProviderZenMux,
			},
		},
		{
			channelID: 2,
			price: &model.ChannelModelPrice{
				Provider: types.PriceProviderTabCode,
			},
		},
		{
			channelID: 3,
			price: &model.ChannelModelPrice{
				Provider: "ordinary",
			},
		},
	}

	filtered := priceAuthorizableRoutes(routes, "claude-sonnet-4-6", "default", nil, true, false)

	require.Len(t, filtered, 1)
	require.Equal(t, 3, filtered[0].channelID)
}

func TestUnboundedClaudeMediaExcludesStrictProvidersFromRetryAuthorization(t *testing.T) {
	routes := []routableChannelPriceView{
		{channelID: 1, price: &model.ChannelModelPrice{Provider: types.PriceProviderZenMux}},
		{channelID: 2, price: &model.ChannelModelPrice{Provider: types.PriceProviderTabCode}},
		{channelID: 3, price: &model.ChannelModelPrice{Provider: "ordinary"}},
	}

	filtered := priceAuthorizableRoutes(routes, "claude-sonnet-4-6", "default", nil, false, true)

	require.Len(t, filtered, 1)
	require.Equal(t, 3, filtered[0].channelID)
}

func TestUnboundedClaudeMediaFailsBeforeTokenCountingOnStrictSelectedRoute(t *testing.T) {
	setupChannelPriceDB(t)
	priority := int64(600)
	channel := model.Channel{
		Id: 91, Key: "strict", Name: "strict", Group: "default", Models: "claude-sonnet-4-6",
		Status: common.ChannelStatusEnabled, Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderZenMux,
		UpstreamModelID: "anthropic/claude-sonnet-4.6", BillingType: model.ChannelModelBillingToken,
		Currency: "USD", InputPrice: 3, OutputPrice: 15, Available: true,
	}).Error)
	addTestAbility(t, channel.Id, "default", "claude-sonnet-4-6", true, &priority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, channel.Id)
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default",
		Request: &dto.GeneralOpenAIRequest{Messages: []dto.Message{{Content: []any{map[string]any{
			"type": "image_url", "image_url": map[string]any{"url": "https://media.example/large.png"},
		}}}}},
	}

	require.ErrorContains(t, ValidateClaudeMediaBeforeTokenCount(ctx, info), "file_id")
	_, err := ModelPriceHelper(ctx, info, 100, &types.TokenCountMeta{MaxTokens: 100})
	require.ErrorContains(t, err, "file_id")
	_, bound := ctx.Get(priceAuthorizationBoundsContextKey)
	require.False(t, bound)
}

func TestManagedWebSearchFailsBeforePriceAuthorization(t *testing.T) {
	setupChannelPriceDB(t)
	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })

	priority := int64(550)
	weight := uint(100)
	tag := "mujian-provider:tabcode:chat"
	baseURL := "https://api2.tabcode.cc/claude/kiropower"
	headers := `{"Authorization":"Bearer {api_key}","anthropic-version":"2023-06-01"}`
	mapping := `{"claude-sonnet-4-6":"claude-sonnet-4-6"}`
	testModel := "claude-sonnet-4-6"
	channel := model.Channel{
		Id: 77, Type: constant.ChannelTypeAnthropic, Key: "tab-secret", Name: "TabCode",
		Group: "default", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled,
		Priority: &priority, Weight: &weight, Tag: &tag, BaseURL: &baseURL,
		HeaderOverride: &headers, ModelMapping: &mapping, TestModel: &testModel, TestTime: 200,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderTabCode,
		UpstreamModelID: "claude-sonnet-4-6", BillingType: model.ChannelModelBillingToken,
		Currency: "USD", InputPrice: 2.25, OutputPrice: 11.25,
		CacheRatio: 0.1, CacheCreationRatio: 1.25,
		Available: true, SyncedAt: 100, TestedAt: 100,
	}).Error)
	addTestAbility(t, channel.Id, "default", "claude-sonnet-4-6", true, &priority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, channel.Id)
	ctx.Set("mujian_managed_provider", true)
	bindManagedTestPrice(t, ctx, channel.Id, "claude-sonnet-4-6", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default",
		Request: &dto.ClaudeRequest{Tools: []map[string]any{{"type": "web_search_20250305"}}},
	}

	_, err := ModelPriceHelper(ctx, info, 100, &types.TokenCountMeta{MaxTokens: 100})

	require.ErrorContains(t, err, "web_search")
	_, bound := ctx.Get(priceAuthorizationBoundsContextKey)
	require.False(t, bound)
}

func TestSingleRetryAutoPreauthorizationStopsAtFirstReachableFutureGroup(t *testing.T) {
	setupChannelPriceDB(t)
	previousRetryTimes := common.RetryTimes
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	common.RetryTimes = 1
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["route-a","route-b","route-c"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"route-a":"A","route-b":"B","route-c":"C"}`))
	t.Cleanup(func() {
		common.RetryTimes = previousRetryTimes
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
	})
	priority := int64(600)
	channels := []model.Channel{
		{Id: 1, Key: "a", Name: "A", Group: "route-a", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 2, Key: "b", Name: "B", Group: "route-b", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 3, Key: "c", Name: "C", Group: "route-c", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "a", UpstreamModelID: "a", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: "b", UpstreamModelID: "b", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 3, OutputPrice: 6, Available: true},
		{ChannelID: 3, CatalogID: "claude-sonnet-4-6", Provider: "c", UpstreamModelID: "c", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 100, OutputPrice: 200, Available: true},
	}).Error)
	for index, group := range []string{"route-a", "route-b", "route-c"} {
		addTestAbility(t, index+1, group, "claude-sonnet-4-6", true, &priority)
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "route-a")
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6", TokenGroup: "auto", UsingGroup: "route-a", UserGroup: "default",
	}

	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 5625, price.QuotaToPreConsume)
	bounds, err := priceAuthorizationForRequest(ctx)
	require.NoError(t, err)
	require.True(t, bounds.authorizes(1, "route-a"))
	require.True(t, bounds.authorizes(2, "route-b"))
	require.False(t, bounds.authorizes(3, "route-c"))
}

func TestMemoryCachedPricingExcludesDatabaseOnlyRoute(t *testing.T) {
	setupChannelPriceDB(t)
	previousCacheSetting := common.MemoryCacheEnabled
	previousRetryTimes := common.RetryTimes
	common.MemoryCacheEnabled = true
	common.RetryTimes = 1
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousCacheSetting
		common.RetryTimes = previousRetryTimes
	})
	priority := int64(600)
	cheap := model.Channel{Id: 1, Key: "cheap", Name: "cheap", Group: "default", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priority}
	require.NoError(t, model.DB.Create(&cheap).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "cheap", UpstreamModelID: "cheap",
		BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true,
	}).Error)
	addTestAbility(t, 1, "default", "claude-sonnet-4-6", true, &priority)
	model.InitChannelCache()

	expensive := model.Channel{Id: 2, Key: "expensive", Name: "expensive", Group: "default", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priority}
	require.NoError(t, model.DB.Create(&expensive).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: "expensive", UpstreamModelID: "expensive",
		BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 100, OutputPrice: 200, Available: true,
	}).Error)
	addTestAbility(t, 2, "default", "claude-sonnet-4-6", true, &priority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default"}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 3750, price.QuotaToPreConsume)
	bounds, err := priceAuthorizationForRequest(ctx)
	require.NoError(t, err)
	require.True(t, bounds.authorizes(1, "default"))
	require.False(t, bounds.authorizes(2, "default"))
}

func TestDatabasePricingUsesAbilityPrioritySnapshot(t *testing.T) {
	setupChannelPriceDB(t)
	previousCacheSetting := common.MemoryCacheEnabled
	previousRetryTimes := common.RetryTimes
	common.MemoryCacheEnabled = false
	common.RetryTimes = 1
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousCacheSetting
		common.RetryTimes = previousRetryTimes
	})
	channelPriorities := []int64{600, 100, 550}
	abilityPriorities := []int64{600, 550, 100}
	channels := []model.Channel{
		{Id: 1, Key: "initial", Name: "initial", Group: "default", Status: common.ChannelStatusEnabled, Priority: &channelPriorities[0]},
		{Id: 2, Key: "reachable", Name: "reachable", Group: "default", Status: common.ChannelStatusEnabled, Priority: &channelPriorities[1]},
		{Id: 3, Key: "unreachable", Name: "unreachable", Group: "default", Status: common.ChannelStatusEnabled, Priority: &channelPriorities[2]},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "initial", UpstreamModelID: "initial", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: "reachable", UpstreamModelID: "reachable", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 3, OutputPrice: 6, Available: true},
		{ChannelID: 3, CatalogID: "claude-sonnet-4-6", Provider: "unreachable", UpstreamModelID: "unreachable", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 100, OutputPrice: 200, Available: true},
	}).Error)
	for index := range channels {
		addTestAbility(t, index+1, "default", "claude-sonnet-4-6", true, &abilityPriorities[index])
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default"}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 5625, price.QuotaToPreConsume)
	bounds, err := priceAuthorizationForRequest(ctx)
	require.NoError(t, err)
	require.True(t, bounds.authorizes(2, "default"))
	require.False(t, bounds.authorizes(3, "default"))
}

func TestMemoryCachedPricingExcludesZeroWeightPeer(t *testing.T) {
	setupChannelPriceDB(t)
	previousCacheSetting := common.MemoryCacheEnabled
	previousRetryTimes := common.RetryTimes
	common.MemoryCacheEnabled = true
	common.RetryTimes = 1
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousCacheSetting
		common.RetryTimes = previousRetryTimes
	})
	priorities := []int64{600, 550, 550}
	weights := []uint{100, 100, 0}
	channels := []model.Channel{
		{Id: 1, Key: "initial", Name: "initial", Group: "default", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priorities[0], Weight: &weights[0]},
		{Id: 2, Key: "weighted", Name: "weighted", Group: "default", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priorities[1], Weight: &weights[1]},
		{Id: 3, Key: "zero", Name: "zero", Group: "default", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priorities[2], Weight: &weights[2]},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "initial", UpstreamModelID: "initial", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: "weighted", UpstreamModelID: "weighted", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 3, OutputPrice: 6, Available: true},
		{ChannelID: 3, CatalogID: "claude-sonnet-4-6", Provider: "zero", UpstreamModelID: "zero", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 100, OutputPrice: 200, Available: true},
	}).Error)
	for index := range channels {
		require.NoError(t, model.DB.Create(&model.Ability{
			Group: "default", Model: "claude-sonnet-4-6", ChannelId: index + 1,
			Enabled: true, Priority: &priorities[index], Weight: uint(weights[index]),
		}).Error)
	}
	model.InitChannelCache()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default"}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 5625, price.QuotaToPreConsume)
	bounds, err := priceAuthorizationForRequest(ctx)
	require.NoError(t, err)
	require.True(t, bounds.authorizes(2, "default"))
	require.False(t, bounds.authorizes(3, "default"))
}

func TestOrdinaryCatalogPreauthorizationMatchesBestUnattemptedRetry(t *testing.T) {
	setupChannelPriceDB(t)
	previousRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = previousRetryTimes })
	priorities := []int64{600, 600, 550}
	channels := []model.Channel{
		{Id: 1, Key: "primary-a", Name: "primary-a", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorities[0]},
		{Id: 2, Key: "primary-b", Name: "primary-b", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorities[1]},
		{Id: 3, Key: "lower", Name: "lower", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priorities[2]},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "ordinary-a", UpstreamModelID: "ordinary-a", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: "ordinary-b", UpstreamModelID: "ordinary-b", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 3, OutputPrice: 6, Available: true},
		{ChannelID: 3, CatalogID: "claude-sonnet-4-6", Provider: "lower", UpstreamModelID: "lower", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 100, OutputPrice: 200, Available: true},
	}).Error)
	for index, channel := range channels {
		addTestAbility(t, channel.Id, "default", "claude-sonnet-4-6", true, &priorities[index])
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default"}

	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 5625, price.QuotaToPreConsume)
	require.True(t, common.GetContextKeyBool(ctx, constant.ContextKeyCatalogPriceAuthorized))
	bounds, err := priceAuthorizationForRequest(ctx)
	require.NoError(t, err)
	require.True(t, bounds.authorizes(1, "default"))
	require.True(t, bounds.authorizes(2, "default"))
	require.False(t, bounds.authorizes(3, "default"))
}

func TestAffinityFailurePreauthorizesOnlyInitialCatalogChannel(t *testing.T) {
	setupChannelPriceDB(t)
	previousRetryTimes := common.RetryTimes
	common.RetryTimes = 1
	t.Cleanup(func() { common.RetryTimes = previousRetryTimes })
	primaryPriority, backupPriority := int64(600), int64(550)
	channels := []model.Channel{
		{Id: 1, Key: "initial", Name: "initial", Group: "default", Status: common.ChannelStatusEnabled, Priority: &primaryPriority},
		{Id: 2, Key: "backup", Name: "backup", Group: "default", Status: common.ChannelStatusEnabled, Priority: &backupPriority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "initial", UpstreamModelID: "initial", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: "backup", UpstreamModelID: "backup", BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 100, OutputPrice: 200, Available: true},
	}).Error)
	addTestAbility(t, 1, "default", "claude-sonnet-4-6", true, &primaryPriority)
	addTestAbility(t, 2, "default", "claude-sonnet-4-6", true, &backupPriority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	ctx.Set("channel_affinity_skip_retry_on_failure", true)
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default"}

	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 3750, price.QuotaToPreConsume)
	bounds, err := priceAuthorizationForRequest(ctx)
	require.NoError(t, err)
	require.True(t, bounds.authorizes(1, "default"))
	require.False(t, bounds.authorizes(2, "default"))
}

func TestStrictRouteEnabledAfterLegacyAuthorizationIsRejected(t *testing.T) {
	setupChannelPriceDB(t)
	setTestModelPrice(t, "claude-sonnet-4-6", 0.02)
	priority := int64(600)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 1, Key: "legacy", Name: "legacy", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority,
	}).Error)
	addTestAbility(t, 1, "default", "claude-sonnet-4-6", true, &priority)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default", UserGroup: "default"}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})
	require.NoError(t, err)
	require.False(t, price.ChannelSpecific)

	tag := "mujian-provider:tabcode:chat"
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 2, Key: "new-strict", Name: "new-strict", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority, Tag: &tag,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: types.PriceProviderTabCode, UpstreamModelID: "claude-sonnet-4-6",
		BillingType: model.ChannelModelBillingToken, Currency: "USD", InputPrice: 2.25, OutputPrice: 11.25, Available: true,
	}).Error)
	addTestAbility(t, 2, "default", "claude-sonnet-4-6", true, &priority)
	bindManagedTestPrice(t, ctx, 2, "claude-sonnet-4-6", "default")

	err = ApplyChannelModelPrice(ctx, info, 2, true)
	require.ErrorContains(t, err, "不在本次请求的价格预授权范围")
}

func TestSnapshotPricingHonorsZeroRatioFreePreconsumePolicy(t *testing.T) {
	setupChannelPriceDB(t)
	previous := operation_setting.GetQuotaSetting().EnableFreeModelPreConsume
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	t.Cleanup(func() { operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = previous })
	priority := int64(100)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 1, Key: "snapshot", Name: "snapshot", Group: "free", Status: common.ChannelStatusEnabled, Priority: &priority,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 1, CatalogID: "free-snapshot", Provider: "priced", UpstreamModelID: "free-snapshot",
		BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true,
	}).Error)
	addTestAbility(t, 1, "free", "free-snapshot", true, &priority)

	price, found, err := channelModelPreConsumePrice(
		"free-snapshot", "free", nil, 1000, &types.TokenCountMeta{MaxTokens: 1000}, types.GroupRatioInfo{GroupRatio: 0},
	)

	require.NoError(t, err)
	require.True(t, found)
	require.True(t, price.FreeModel)
	require.Zero(t, price.QuotaToPreConsume)
}

func TestChannelModelPricePreauthorizationCoversOneHourCacheWrite(t *testing.T) {
	setupChannelPriceDB(t)
	previousPreConsumedQuota := common.PreConsumedQuota
	common.PreConsumedQuota = 0
	t.Cleanup(func() { common.PreConsumedQuota = previousPreConsumedQuota })
	priority := int64(100)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 1, Key: "cache-key", Name: "cache-priced", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "tabcode", UpstreamModelID: "claude-sonnet-4-6",
		BillingType: model.ChannelModelBillingToken, InputPrice: 3, OutputPrice: 15,
		CacheRatio: 0.1, CacheCreationRatio: 1.25, Available: true,
	}).Error)
	addTestAbility(t, 1, "default", "claude-sonnet-4-6", true, &priority)

	preauth, found, err := channelModelPreConsumePrice(
		"claude-sonnet-4-6", "default", nil, 100, &types.TokenCountMeta{}, types.GroupRatioInfo{GroupRatio: 1},
	)

	require.NoError(t, err)
	require.True(t, found)
	// The 1h write rate is 2x input: 100 * 2 * ($3 / $2 baseline).
	require.Equal(t, 375, preauth.QuotaToPreConsume)
	require.Equal(t, 2.0, preauth.CacheCreation1hRatio)
}

func TestApplyChannelModelPriceRejectsUnpricedRetryChannel(t *testing.T) {
	setupChannelPriceDB(t)
	info := &relaycommon.RelayInfo{
		OriginModelName: "test-model",
		PriceData:       types.PriceData{ChannelSpecific: true, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}},
	}
	require.Error(t, ApplyChannelModelPrice(nil, info, 999, true))
}

func TestApplyChannelModelPriceRequiresMatchingReferenceCapabilityForEdits(t *testing.T) {
	setupChannelPriceDB(t)
	priority := int64(100)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 1, Key: "test", Name: "GPT image", Status: common.ChannelStatusEnabled, Priority: &priority,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 1, CatalogID: "gpt-image-2", Provider: "zex", UpstreamModelID: "gpt-image-2",
		BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.05, Available: true,
		ReferenceProtocol: model.ChannelModelReferenceGeminiInline, MaxReferenceImages: 3,
	}).Error)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2", RelayMode: relayconstant.RelayModeImagesEdits,
		PriceData: types.PriceData{ChannelSpecific: true, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}},
	}

	err := ApplyChannelModelPrice(nil, info, 1, false)

	require.EqualError(t, err, "所选渠道未声明当前模型的多图参考协议")
}

func TestApplyChannelModelPriceAcceptsDeclaredReferenceCapabilityForEdits(t *testing.T) {
	setupChannelPriceDB(t)
	priority := int64(100)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 1, Key: "test", Name: "GPT image", Status: common.ChannelStatusEnabled, Priority: &priority,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 1, CatalogID: "gpt-image-2", Provider: "yunwu", UpstreamModelID: "gpt-image-2",
		BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.05, Available: true,
		ReferenceProtocol: model.ChannelModelReferenceOpenAIEditMultipart, MaxReferenceImages: 3,
	}).Error)
	info := &relaycommon.RelayInfo{
		OriginModelName: "gpt-image-2", RelayMode: relayconstant.RelayModeImagesEdits,
		PriceData: types.PriceData{
			ChannelSpecific: true, QuotaToPreConsume: 25000, UnitPriceMultiplier: 1,
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
		},
	}

	err := ApplyChannelModelPrice(nil, info, 1, false)

	require.NoError(t, err)
	require.True(t, info.PriceData.UsePrice)
	require.Equal(t, 0.05, info.PriceData.ModelPrice)
	require.Equal(t, 25000, info.PriceData.QuotaToPreConsume)
}

func TestChannelModelPriceRejectsOverflowInsteadOfUnderAuthorizing(t *testing.T) {
	setupChannelPriceDB(t)
	priority := int64(100)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 1, Key: "test", Name: "unsafe", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 1, CatalogID: "test-model", Provider: "unsafe", UpstreamModelID: "test-model",
		BillingType: model.ChannelModelBillingToken, InputPrice: 1e100, OutputPrice: 1e100, Available: true,
	}).Error)
	addTestAbility(t, 1, "default", "test-model", true, &priority)

	_, found, err := channelModelPreConsumePrice(
		"test-model", "default", nil, 1000, &types.TokenCountMeta{MaxTokens: 1000}, types.GroupRatioInfo{GroupRatio: 1},
	)

	require.False(t, found)
	require.ErrorContains(t, err, "可安全预授权范围")
}

func TestChannelModelPricePreauthorizationIgnoresOtherGroups(t *testing.T) {
	setupChannelPriceDB(t)
	priority := int64(100)
	channels := []model.Channel{
		{Id: 1, Key: "default-key", Name: "default", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 2, Key: "canary-key", Name: "canary", Group: "mujian-canary", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "test-model", Provider: "default", UpstreamModelID: "test-model", BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "test-model", Provider: "canary", UpstreamModelID: "test-model", BillingType: model.ChannelModelBillingToken, InputPrice: 200, OutputPrice: 400, Available: true},
	}).Error)
	addTestAbility(t, 1, "default", "test-model", true, &priority)
	addTestAbility(t, 2, "mujian-canary", "test-model", true, &priority)

	preauth, found, err := channelModelPreConsumePrice(
		"test-model", "default", nil, 1000, &types.TokenCountMeta{MaxTokens: 1000}, types.GroupRatioInfo{GroupRatio: 1},
	)

	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1.0, preauth.ModelRatio)
	require.Equal(t, 3750, preauth.QuotaToPreConsume)
}

func TestModelPriceHelperUsesOnlyRoutableAllowedCandidates(t *testing.T) {
	setupChannelPriceDB(t)
	priority := int64(100)
	channels := []model.Channel{
		{Id: 1, Key: "allowed-cheap", Name: "allowed cheap", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 2, Key: "allowed-worst", Name: "allowed worst", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 3, Key: "disabled-ability", Name: "disabled ability", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 4, Key: "disabled-channel", Name: "disabled channel", Group: "default", Status: common.ChannelStatusManuallyDisabled, Priority: &priority},
		{Id: 5, Key: "other-group", Name: "other group", Group: "mujian-canary", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 6, Key: "not-allowed", Name: "not allowed", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	prices := make([]model.ChannelModelPrice, 0, len(channels))
	for _, channel := range channels {
		inputPrice, outputPrice := 200.0, 400.0
		switch channel.Id {
		case 1:
			inputPrice, outputPrice = 2, 4
		case 2:
			inputPrice, outputPrice = 4, 8
		}
		prices = append(prices, model.ChannelModelPrice{
			ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", Provider: "provider", UpstreamModelID: "claude-sonnet-4-6",
			BillingType: model.ChannelModelBillingToken, InputPrice: inputPrice, OutputPrice: outputPrice, Available: true,
		})
	}
	require.NoError(t, model.DB.Create(&prices).Error)
	addTestAbility(t, 1, "default", "claude-sonnet-4-6", true, &priority)
	addTestAbility(t, 2, "default", "claude-sonnet-4-6", true, &priority)
	addTestAbility(t, 3, "default", "claude-sonnet-4-6", false, &priority)
	addTestAbility(t, 4, "default", "claude-sonnet-4-6", true, &priority)
	addTestAbility(t, 5, "mujian-canary", "claude-sonnet-4-6", true, &priority)
	addTestAbility(t, 6, "default", "claude-sonnet-4-6", true, &priority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyAllowedChannelIds, []int{1, 2, 3, 4, 5})
	info := &relaycommon.RelayInfo{OriginModelName: "claude-sonnet-4-6", UsingGroup: "default"}
	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.True(t, price.ChannelSpecific)
	require.Equal(t, 2.0, price.ModelRatio)
	require.Equal(t, 2.0, price.CompletionRatio)
	require.Equal(t, 7500, price.QuotaToPreConsume)
}

func TestChannelModelPriceEmptyAllowedIDsAllowsNoCandidate(t *testing.T) {
	setupChannelPriceDB(t)
	priority := int64(100)
	require.NoError(t, model.DB.Create(&model.Channel{
		Id: 1, Key: "key", Name: "candidate", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority,
	}).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: 1, CatalogID: "test-model", Provider: "provider", UpstreamModelID: "test-model",
		BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true,
	}).Error)
	addTestAbility(t, 1, "default", "test-model", true, &priority)

	price, found, err := channelModelPreConsumePrice(
		"test-model", "default", []int{}, 1000, &types.TokenCountMeta{MaxTokens: 1000}, types.GroupRatioInfo{GroupRatio: 1},
	)

	require.NoError(t, err)
	require.False(t, found)
	require.Equal(t, types.PriceData{}, price)
}

func TestModelPriceHelperAutoCrossGroupPreauthorizesReachableWorstCase(t *testing.T) {
	setupChannelPriceDB(t)
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	previousGroupRatios := ratio_setting.GroupRatio2JSONString()
	previousSpecialRatios := ratio_setting.GroupGroupRatio2JSONString()
	previousRetryTimes := common.RetryTimes
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["route-a","route-b"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"route-a":"A","route-b":"B"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"route-a":1,"route-b":2}`))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{"customer":{"route-b":3}}`))
	common.RetryTimes = 1
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroupRatios))
		require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(previousSpecialRatios))
		common.RetryTimes = previousRetryTimes
	})

	priority := int64(100)
	channels := []model.Channel{
		{Id: 1, Key: "a", Name: "A", Group: "route-a", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 2, Key: "b", Name: "B", Group: "route-b", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	require.NoError(t, model.DB.Create(&[]model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "claude-sonnet-4-6", Provider: "a", UpstreamModelID: "claude-sonnet-4-6", BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "claude-sonnet-4-6", Provider: "b", UpstreamModelID: "claude-sonnet-4-6", BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true},
	}).Error)
	addTestAbility(t, 1, "route-a", "claude-sonnet-4-6", true, &priority)
	addTestAbility(t, 2, "route-b", "claude-sonnet-4-6", true, &priority)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "route-a")
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	info := &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6", TokenGroup: "auto", UsingGroup: "route-a", UserGroup: "customer",
	}

	price, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})

	require.NoError(t, err)
	require.Equal(t, 11250, price.QuotaToPreConsume)
	require.Equal(t, 3.0, price.GroupRatioInfo.GroupRatio)
	require.True(t, price.GroupRatioInfo.HasSpecialRatio)

	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, false)
	info = &relaycommon.RelayInfo{
		OriginModelName: "claude-sonnet-4-6", TokenGroup: "auto", UsingGroup: "route-a", UserGroup: "customer",
	}
	price, err = ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{MaxTokens: 1000})
	require.NoError(t, err)
	require.Equal(t, 3750, price.QuotaToPreConsume)
	require.Equal(t, 1.0, price.GroupRatioInfo.GroupRatio)
}

func TestCheckedPreConsumeQuotaRoundsPositiveFractionsUp(t *testing.T) {
	for value, expected := range map[float64]int{0.25: 1, 1.25: 2, 2: 2} {
		quota, err := checkedPreConsumeQuota(value)
		require.NoError(t, err)
		require.Equal(t, expected, quota)
	}
}

func TestManagedSalesPreauthorizationRoundsAfterMarkup(t *testing.T) {
	for _, provider := range []string{"yuyu", "yunwu", "geeknow", "zex", "zenmux", "tabcode"} {
		t.Run(provider, func(t *testing.T) {
			source := model.ChannelModelPrice{Provider: provider, BillingType: model.ChannelModelBillingToken, InputPrice: 1.6, OutputPrice: 1.6}
			data, err := preConsumePriceFromSnapshot(source, types.GroupRatioInfo{GroupRatio: 1}, 1, 1, 0)
			require.NoError(t, err)
			require.Equal(t, 1, data.QuotaToPreConsume)
			require.Equal(t, .8, data.ModelRatio, "snapshot remains upstream cost")
			source.BillingType = model.ChannelModelBillingFixed
			source.FixedPrice = .0000016
			data, err = preConsumePriceFromSnapshot(source, types.GroupRatioInfo{GroupRatio: 1}, 1, 0, 0)
			require.NoError(t, err)
			require.Equal(t, 1, data.QuotaToPreConsume, "do not ceil the cost before multiplying")
		})
	}
}
