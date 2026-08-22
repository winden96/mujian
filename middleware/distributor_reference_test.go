package middleware

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReferenceImageAllowedChannelIDsUsesOnlyDeclaredCapabilities(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.ChannelModelPrice{}))

	highPriority := int64(320)
	lowPriority := int64(220)
	require.NoError(t, db.Create(&[]model.Channel{
		{Id: 8101, Name: "ineligible-high-priority", Key: "key-1", Status: common.ChannelStatusEnabled, Priority: &highPriority},
		{Id: 8102, Name: "eligible-low-priority", Key: "key-2", Status: common.ChannelStatusEnabled, Priority: &lowPriority},
	}).Error)
	require.NoError(t, db.Create(&[]model.ChannelModelPrice{
		{
			ChannelID: 8101, CatalogID: "gpt-image-2", Provider: "zex", UpstreamModelID: "gpt-image-2",
			BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.05, Available: true,
		},
		{
			ChannelID: 8102, CatalogID: "gpt-image-2", Provider: "yunwu", UpstreamModelID: "gpt-image-2",
			BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.05, Available: true,
			ReferenceProtocol: model.ChannelModelReferenceOpenAIEditMultipart, MaxReferenceImages: 3,
		},
	}).Error)

	allowed, err := referenceImageAllowedChannelIDs("/v1/images/edits", "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, []int{8102}, allowed)

	allowed, err = referenceImageAllowedChannelIDs("/v1/images/generations", "gpt-image-2")
	require.NoError(t, err)
	require.Nil(t, allowed)

	require.NoError(t, db.Delete(&model.ChannelModelPrice{}, "channel_id = ?", 8102).Error)
	allowed, err = referenceImageAllowedChannelIDs("/v1/images/edits", "gpt-image-2")
	require.EqualError(t, err, "当前模型没有已声明多图参考协议的可用渠道")
	require.Empty(t, allowed)
}
