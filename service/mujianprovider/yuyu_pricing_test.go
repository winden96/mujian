package mujianprovider

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func yuYuPricingFixture(t *testing.T) YuYuPricingImport {
	t.Helper()
	var input YuYuPricingImport
	require.NoError(t, common.UnmarshalJsonStr(`{"upstream_group":"auto","pricing":{
		"success":true,"pricing_version":"fixture-v1","group_ratio":{"default":1},"auto_groups":["default"],"data":[
		{"model_name":"deepseek-v4-flash","quota_type":0,"model_ratio":0.5,"completion_ratio":2,"cache_ratio":0.1,"enable_groups":["default"]},
		{"model_name":"gemini-2.5-flash-image","quota_type":1,"model_price":0.06,"supported_endpoint_types":["image-generation"],"enable_groups":["default"]},
		{"model_name":"gpt-image-2","quota_type":1,"model_price":0.2,"supported_endpoint_types":["chat"],"enable_groups":["default"]}
	]}}`, &input))
	return input
}

func TestYuYuLinearExpressionUsesActualPricesInsteadOfLegacyRatios(t *testing.T) {
	input := yuYuPricingFixture(t)
	entry := &input.Pricing.Data[0]
	entry.ModelRatio = 37.5
	entry.BillingMode = "tiered_expr"
	entry.BillingExpr = `tier("base", p * 2 + c * 10 + cr * 0.2 + cc * 2.5 + cc1h * 4)`
	input.Pricing.GroupRatio["default"] = 0.3
	snapshot, err := normalizeYuYuPricing(input)
	require.NoError(t, err)
	item := snapshot.Items[0]
	require.Empty(t, item.ValidationError)
	require.InDelta(t, 0.6, item.InputPrice, 1e-12)
	require.InDelta(t, 3.0, item.OutputPrice, 1e-12)
	require.Equal(t, 0.1, item.CacheRatio)
	require.Equal(t, 1.25, item.CreateCacheRatio)
	require.Contains(t, item.OriginalPricing, "billing_expr")
}

func TestYuYuUnsupportedBillingCannotFallBackToOldRatios(t *testing.T) {
	for _, expression := range []string{
		`len <= 272000 ? tier("base", p * 2 + c * 12) : tier("longContext", p * 4 + c * 18)`,
		`tier("base", p * 8 + c * 8 + img_o * 30)`,
		`tier("base", p * 1 + p * 2 + c * 3)`,
		`tier("base", p * 1 + c * 3 + cc * 1.25 + cc1h * 99)`,
		`tier("base", p * 1 + c * 3 + cc * 1.25)`,
		`tier("base", p * 1 - c * 3)`,
		`tier("base", p * 1 + c * 3); evil()`,
	} {
		t.Run(expression, func(t *testing.T) {
			input := yuYuPricingFixture(t)
			input.Pricing.Data[0].BillingMode = "tiered_expr"
			input.Pricing.Data[0].BillingExpr = expression
			snapshot, err := normalizeYuYuPricing(input)
			require.NoError(t, err)
			require.NotEmpty(t, snapshot.Items[0].ValidationError)
			_, _, reason := matchCatalogEntry("yuyu", CatalogEntry{ID: "deepseek-v4-flash", Kind: "chat"}, map[string]struct{}{"deepseek-v4-flash": {}}, map[string]pricingItem{"deepseek-v4-flash": snapshot.Items[0]})
			require.NotEmpty(t, reason)
		})
	}
}

func TestYuYuGroupPricingAndImportValidation(t *testing.T) {
	input := yuYuPricingFixture(t)
	input.Pricing.GroupRatio["discount"] = 0.3
	input.Pricing.AutoGroups = append(input.Pricing.AutoGroups, "discount")
	input.Pricing.Data[0].EnableGroups = append(input.Pricing.Data[0].EnableGroups, "discount")
	snapshot, err := normalizeYuYuPricing(input)
	require.NoError(t, err)
	require.Contains(t, snapshot.Items[0].ValidationError, "不同价格")
	input.UpstreamGroup = "discount"
	snapshot, err = normalizeYuYuPricing(input)
	require.NoError(t, err)
	require.Equal(t, 0.3, snapshot.Items[0].InputPrice)
	require.Contains(t, snapshot.Items[1].ValidationError, "未向令牌")
	input.UpstreamGroup = "missing"
	_, err = normalizeYuYuPricing(input)
	require.Error(t, err)
	input = yuYuPricingFixture(t)
	input.Pricing.Data = append(input.Pricing.Data, input.Pricing.Data[0])
	_, err = normalizeYuYuPricing(input)
	require.ErrorContains(t, err, "重复模型")
}

func TestYuYuImportAndKeyRotationInvalidateExistingRoutes(t *testing.T) {
	setupProviderDB(t)
	mockYuYuAPI(t)
	require.NoError(t, Configure("yuyu", "yuyu-test-key"))
	input := yuYuPricingFixture(t)
	require.NoError(t, ImportYuYuPricing(input))
	_, err := Sync("yuyu")
	require.NoError(t, err)
	_, _, err = Test("yuyu")
	require.NoError(t, err)
	require.NoError(t, SetEnabled("yuyu", true))
	require.NoError(t, ImportYuYuPricing(input))
	routable, err := CatalogModelRoutableForGroupTx(model.DB, "deepseek-v4-flash", "default")
	require.NoError(t, err)
	require.False(t, routable)
	_, err = Sync("yuyu")
	require.NoError(t, err)
	_, _, err = Test("yuyu")
	require.NoError(t, err)
	require.NoError(t, SetEnabled("yuyu", true))
	require.NoError(t, ConfigureFromAPI("yuyu", "rotated-yuyu-key", nil))
	_, err = resolveYuYuPricing("rotated-yuyu-key")
	require.ErrorContains(t, err, "Key 已变化")
	require.Error(t, SetEnabled("yuyu", true))
	channels, err := providerChannels("yuyu", true)
	require.NoError(t, err)
	for _, channel := range channels {
		require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
	}
	var stored model.Option
	require.NoError(t, model.DB.Where("key = ?", yuYuPricingOption).First(&stored).Error)
	require.False(t, strings.Contains(stored.Value, "yuyu-test-key"))
}

func TestYuYuImageOutputRateRespectsGroupAndCacheExpression(t *testing.T) {
	input := yuYuPricingFixture(t)
	entry := &input.Pricing.Data[2]
	entry.QuotaType, entry.ModelPrice = 0, 0
	entry.BillingMode = "tiered_expr"
	entry.BillingExpr = `tier("base", p * 8 + c * 8 + img_o * 30)`
	input.Pricing.GroupRatio["default"] = 0.5
	snapshot, err := normalizeYuYuPricing(input)
	require.NoError(t, err)
	require.Empty(t, snapshot.Items[2].ValidationError)
	require.Equal(t, 4.0, snapshot.Items[2].InputPrice)
	require.Equal(t, 15.0, snapshot.Items[2].ImageOutputPrice)
	require.Equal(t, 1.0, snapshot.Items[2].CacheRatio, "cache remains part of p when cr is absent")
}
