package mujianprovider

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func catalogAvailabilityByID(t *testing.T, items []CatalogAvailability, modelID string) CatalogAvailability {
	t.Helper()
	for _, item := range items {
		if item.ID == modelID {
			return item
		}
	}
	require.FailNow(t, "catalog item missing", modelID)
	return CatalogAvailability{}
}

func TestCatalogAvailabilityListForGroupDoesNotExposeOtherGroups(t *testing.T) {
	setupProviderDB(t)
	priority := int64(600)
	channels := []model.Channel{
		{Name: "canary", Key: "key-1", Group: "mujian-canary", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Name: "similar", Key: "key-2", Group: "mujian-canary-extra", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	for index := range channels {
		require.NoError(t, channels[index].AddAbilities(nil))
	}
	prices := []model.ChannelModelPrice{
		{
			ChannelID: channels[0].Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "claude-sonnet-4-6",
			Provider: "tabcode", BillingType: model.ChannelModelBillingToken, InputPrice: 2.25, OutputPrice: 11.25, Available: true,
		},
		{
			ChannelID: channels[1].Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "wrong-group",
			Provider: "wrong", BillingType: model.ChannelModelBillingToken, InputPrice: 1, OutputPrice: 1, Available: true,
		},
	}
	require.NoError(t, model.DB.Create(&prices).Error)

	global, err := CatalogAvailabilityList()
	require.NoError(t, err)
	require.Equal(t, 2, catalogAvailabilityByID(t, global, "claude-sonnet-4-6").ChannelCount)

	canary, err := CatalogAvailabilityListForGroup("mujian-canary")
	require.NoError(t, err)
	canarySonnet := catalogAvailabilityByID(t, canary, "claude-sonnet-4-6")
	require.True(t, canarySonnet.Available)
	require.Equal(t, 1, canarySonnet.ChannelCount)
	require.Equal(t, []string{"tabcode"}, canarySonnet.Providers)

	defaultGroup, err := CatalogAvailabilityListForGroup("default")
	require.NoError(t, err)
	defaultSonnet := catalogAvailabilityByID(t, defaultGroup, "claude-sonnet-4-6")
	require.False(t, defaultSonnet.Available)
	require.Zero(t, defaultSonnet.ChannelCount)
}

func TestAvailableCatalogIDsRemainDatabaseAuthoritativeWithStaleRouteCache(t *testing.T) {
	previousCacheSetting := common.MemoryCacheEnabled
	t.Cleanup(func() {
		common.MemoryCacheEnabled = previousCacheSetting
		if previousCacheSetting {
			model.InitChannelCache()
		}
	})
	setupProviderDB(t)
	common.MemoryCacheEnabled = true
	priority := int64(600)
	channel := model.Channel{
		Name: "cached", Key: "key", Group: "mujian-canary", Models: "claude-sonnet-4-6",
		Status: common.ChannelStatusEnabled, Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "claude-sonnet-4-6",
		Provider: "tabcode", BillingType: model.ChannelModelBillingToken, Available: true,
	}).Error)
	model.InitChannelCache()
	require.NoError(t, model.DB.Model(&model.ChannelModelPrice{}).
		Where("channel_id = ?", channel.Id).Update("available", false).Error)

	ids, err := AvailableCatalogIDsForGroup("mujian-canary")
	require.NoError(t, err)
	require.Empty(t, ids)

	require.NoError(t, model.DB.Model(&model.ChannelModelPrice{}).
		Where("channel_id = ?", channel.Id).Update("available", true).Error)
	require.NoError(t, model.DB.Model(&model.Ability{}).
		Where("channel_id = ?", channel.Id).Update("enabled", false).Error)
	ids, err = AvailableCatalogIDsForGroup("mujian-canary")
	require.NoError(t, err)
	require.Empty(t, ids)
	require.Empty(t, mustAvailableCatalogIDs(t, "default"))
}

func mustAvailableCatalogIDs(t *testing.T, group string) []string {
	t.Helper()
	ids, err := AvailableCatalogIDsForGroup(group)
	require.NoError(t, err)
	return ids
}

func TestReferenceImageModelAvailableForGroupUsesExactMembership(t *testing.T) {
	setupProviderDB(t)
	priority := int64(100)
	channel := model.Channel{
		Name: "canary-image", Key: "key", Group: "mujian-canary", Models: "gpt-image-2", Status: common.ChannelStatusEnabled, Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(nil))
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "gpt-image-2", UpstreamModelID: "gpt-image-2", Provider: "provider",
		BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.1, Available: true,
		ReferenceProtocol: ReferenceProtocolOpenAIEditMultipart, MaxReferenceImages: MaxReferenceImages,
	}).Error)

	available, err := ReferenceImageModelAvailableForGroup("gpt-image-2", "mujian-canary")
	require.NoError(t, err)
	require.True(t, available)
	available, err = ReferenceImageModelAvailableForGroup("gpt-image-2", "mujian-canary-extra")
	require.NoError(t, err)
	require.False(t, available)
}

func TestPublicUnavailableReasonDoesNotExposeProviderErrors(t *testing.T) {
	reason := unavailableReason([]model.ChannelModelPrice{{
		Available: false,
		LastError: "dial tcp 10.0.0.8:5432: secret internal failure",
	}})

	require.Equal(t, "模型已同步，但价格或鉴权尚未通过", reason)
	require.NotContains(t, reason, "10.0.0.8")
	require.NotContains(t, reason, "secret")
}
