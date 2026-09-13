package helper

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestImageRetailFreezesRateAcrossUpstreamPriceChangesAndRetry(t *testing.T) {
	setupChannelPriceDB(t)
	oldRate := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = oldRate })
	operation_setting.USDExchangeRate = 7.3
	priority := int64(200)
	for _, id := range []int{1, 2} {
		channel := model.Channel{Id: id, Key: "test", Name: "image", Group: "default", Models: "gpt-image-2", Status: common.ChannelStatusEnabled, Priority: &priority}
		require.NoError(t, model.DB.Create(&channel).Error)
		require.NoError(t, model.DB.Create(&model.ChannelModelPrice{ChannelID: id, CatalogID: "gpt-image-2", UpstreamModelID: "gpt-image-2", Provider: "yuyu", BillingType: model.ChannelModelBillingToken, InputPrice: 8, OutputPrice: 8, ImageOutputPrice: 30, Available: true, Currency: "USD"}).Error)
		addTestAbility(t, id, "default", "gpt-image-2", true, &priority)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyChannelId, 1)
	n := uint(2)
	req := &dto.ImageRequest{Model: "gpt-image-2", N: &n, Size: "1536x1024", Quality: "high"}
	info := &relaycommon.RelayInfo{OriginModelName: "gpt-image-2", UsingGroup: "default", UserGroup: "default", Request: req}
	data, err := ModelPriceHelper(c, info, 1000, req.GetTokenCountMeta())
	require.NoError(t, err)
	require.Equal(t, 27397, data.QuotaToPreConsume)
	require.False(t, data.FreeModel)
	require.NotNil(t, info.ImageRetail)
	operation_setting.USDExchangeRate = 10
	require.NoError(t, model.DB.Model(&model.ChannelModelPrice{}).Where("channel_id = ?", 2).Updates(map[string]any{"input_price": 800, "image_output_price": 3000}).Error)
	require.NoError(t, ApplyChannelModelPrice(c, info, 2, false))
	require.Equal(t, 7.3, info.ImageRetail.ExchangeRate)
	require.Equal(t, 27397, info.PriceData.QuotaToPreConsume)
	require.Equal(t, 13699, info.ImageRetail.Quota(1))
	require.Equal(t, 400.0, info.PriceData.ModelRatio)
	// A pricing re-entry must retain the request quote too.
	data, err = ModelPriceHelper(c, info, 1000, &types.TokenCountMeta{MaxTokens: 2000})
	require.NoError(t, err)
	require.Equal(t, 27397, data.QuotaToPreConsume)
}

func TestNanoRetailOverridesFixedChannelPrice(t *testing.T) {
	setupChannelPriceDB(t)
	oldRate := operation_setting.USDExchangeRate
	t.Cleanup(func() { operation_setting.USDExchangeRate = oldRate })
	operation_setting.USDExchangeRate = 7.3
	priority := int64(200)
	channel := model.Channel{Id: 1, Key: "test", Name: "nano", Group: "default", Models: "nano-banana-2", Status: common.ChannelStatusEnabled, Priority: &priority}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{ChannelID: 1, CatalogID: "nano-banana-2", UpstreamModelID: "gemini-3.1-flash-image-preview", Provider: "yuyu", BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.022, Available: true, Currency: "USD"}).Error)
	addTestAbility(t, 1, "default", "nano-banana-2", true, &priority)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(c, constant.ContextKeyChannelId, 1)
	n := uint(2)
	req := &dto.ImageRequest{Model: "nano-banana-2", N: &n}
	info := &relaycommon.RelayInfo{OriginModelName: req.Model, UsingGroup: "default", UserGroup: "default", Request: req}
	data, err := ModelPriceHelper(c, info, 1000, req.GetTokenCountMeta())
	require.NoError(t, err)
	require.True(t, data.UsePrice)
	require.Equal(t, 13699, data.QuotaToPreConsume)
	require.Equal(t, "request", info.ImageRetail.Price.Unit)
	require.Equal(t, 0.2, info.ImageRetail.Price.Amount)
}
