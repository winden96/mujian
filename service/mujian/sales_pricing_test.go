package mujian

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCatalogSalesPricesPreserveCostsAndRespectGroupDiscount(t *testing.T) {
	setupTestDB(t)
	enableModelForGroup(t, DefaultChatModel, "default", "")
	original := ratio_setting.GroupGroupRatio2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(original)) })
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{"default":{"default":0.8}}`))
	for i := 0; i < 2; i++ {
		_, items, err := CatalogForUser(0)
		require.NoError(t, err)
		item := availabilityForModel(t, items, DefaultChatModel)
		require.Equal(t, 1.0, item.MinInputPrice)
		require.Equal(t, 2.0, item.MaxOutputPrice)
	}
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{}`))
	_, items, err := CatalogForUser(0)
	require.NoError(t, err)
	require.Equal(t, 1.25, availabilityForModel(t, items, DefaultChatModel).MinInputPrice)
	raw, err := mujianprovider.CatalogAvailabilityListForGroup("default")
	require.NoError(t, err)
	require.Equal(t, 1.0, availabilityForModel(t, raw, DefaultChatModel).MinInputPrice)
	var price model.ChannelModelPrice
	require.NoError(t, model.DB.First(&price).Error)
	require.Equal(t, 1.0, price.InputPrice)
}

func TestImageRetailPriceIsIndependentOfProviderSyncAndGroup(t *testing.T) {
	for _, upstream := range []float64{8, 80} {
		items := []mujianprovider.CatalogAvailability{{CatalogEntry: mujianprovider.CatalogEntry{ID: "gpt-image-2"}, Available: true, MinInputPrice: upstream, MaxImageOutputPrice: 30}}
		applyCatalogSalesPrices(items, "default")
		require.NotNil(t, items[0].RetailPricing)
		require.Equal(t, 0.2, items[0].RetailPricing.Amount)
		require.Equal(t, "CNY", items[0].RetailPricing.Currency)
		require.Equal(t, upstream, items[0].MinInputPrice)
	}
}
