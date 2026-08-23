package helper

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelPriceDB(t *testing.T) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.ChannelModelPrice{}))
	model.DB = db
}

func TestChannelModelPricePreauthorizesWorstCandidateAndSettlesSelected(t *testing.T) {
	setupChannelPriceDB(t)
	priorityA, priorityB := int64(200), int64(100)
	channels := []model.Channel{
		{Id: 1, Key: "test-a", Name: "A", Status: common.ChannelStatusEnabled, Priority: &priorityA},
		{Id: 2, Key: "test-b", Name: "B", Status: common.ChannelStatusEnabled, Priority: &priorityB},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	prices := []model.ChannelModelPrice{
		{ChannelID: 1, CatalogID: "test-model", Provider: "a", UpstreamModelID: "test-model-a", BillingType: model.ChannelModelBillingToken, InputPrice: 2, OutputPrice: 4, Available: true},
		{ChannelID: 2, CatalogID: "test-model", Provider: "b", UpstreamModelID: "test-model-b", BillingType: model.ChannelModelBillingToken, InputPrice: 1, OutputPrice: 10, Available: true},
	}
	require.NoError(t, model.DB.Create(&prices).Error)

	group := types.GroupRatioInfo{GroupRatio: 1}
	preauth, found, err := channelModelPreConsumePrice("test-model", 1000, &types.TokenCountMeta{MaxTokens: 1000}, group)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, preauth.ChannelSpecific)
	require.Equal(t, 10.0, preauth.CompletionRatio)
	require.Equal(t, 5500, preauth.QuotaToPreConsume)

	info := &relaycommon.RelayInfo{OriginModelName: "test-model", PriceData: preauth}
	require.NoError(t, ApplyChannelModelPrice(info, 1))
	require.Equal(t, 1.0, info.PriceData.ModelRatio)
	require.Equal(t, 2.0, info.PriceData.CompletionRatio)
	require.Equal(t, 5500, info.PriceData.QuotaToPreConsume)
}

func TestApplyChannelModelPriceRejectsUnpricedRetryChannel(t *testing.T) {
	setupChannelPriceDB(t)
	info := &relaycommon.RelayInfo{
		OriginModelName: "test-model",
		PriceData:       types.PriceData{ChannelSpecific: true, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}},
	}
	require.Error(t, ApplyChannelModelPrice(info, 999))
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

	err := ApplyChannelModelPrice(info, 1)

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

	err := ApplyChannelModelPrice(info, 1)

	require.NoError(t, err)
	require.True(t, info.PriceData.UsePrice)
	require.Equal(t, 0.05, info.PriceData.ModelPrice)
	require.Equal(t, 25000, info.PriceData.QuotaToPreConsume)
}
