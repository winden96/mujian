package model

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupChannelModelPriceTestDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &ChannelModelPrice{}, &Ability{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })
}

func TestStatusTransitionDoesNotOverwriteConcurrentRoutingConfiguration(t *testing.T) {
	setupChannelModelPriceTestDB(t)
	priority := int64(100)
	mapping := `{"claude-sonnet-4-6":"old-upstream"}`
	header := `{"x-api-key":"old"}`
	channel := Channel{
		Name: "managed", Key: "provider-key", Group: "default", Models: "claude-sonnet-4-6",
		Status: common.ChannelStatusEnabled, Priority: &priority, ModelMapping: &mapping, HeaderOverride: &header,
	}
	require.NoError(t, DB.Create(&channel).Error)
	stale := channel

	newPriority := int64(600)
	newMapping := `{"claude-sonnet-4-6":"anthropic/claude-sonnet-4.6"}`
	newHeader := `{"anthropic-version":"2023-06-01"}`
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
		"group": "mujian-canary", "models": "claude-sonnet-4-6", "model_mapping": newMapping,
		"header_override": newHeader, "priority": newPriority, "test_time": int64(1234),
	}).Error)

	stale.Status = common.ChannelStatusAutoDisabled
	stale.SetOtherInfo(map[string]interface{}{"status_reason": "upstream failed"})
	updated, err := persistChannelStatusTransition(&stale, common.ChannelStatusEnabled)
	require.NoError(t, err)
	require.True(t, updated)

	var stored Channel
	require.NoError(t, DB.First(&stored, channel.Id).Error)
	require.Equal(t, common.ChannelStatusAutoDisabled, stored.Status)
	require.Equal(t, "mujian-canary", stored.Group)
	require.Equal(t, newMapping, stored.GetModelMapping())
	require.Equal(t, newHeader, pointerValueOrEmpty(stored.HeaderOverride))
	require.Equal(t, newPriority, stored.GetPriority())
	require.Equal(t, int64(1234), stored.TestTime)
}

func TestChannelCacheRefreshFailureKeepsPreviousGeneration(t *testing.T) {
	setupChannelModelPriceTestDB(t)
	previousCacheSetting := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { common.MemoryCacheEnabled = previousCacheSetting })

	priority := int64(100)
	channel := Channel{
		Name: "cached", Key: "cached-key", Group: "default", Models: "gpt-5.6-terra",
		Status: common.ChannelStatusEnabled, Priority: &priority,
	}
	require.NoError(t, DB.Create(&channel).Error)
	require.NoError(t, DB.Create(&Ability{
		Group: "default", Model: "gpt-5.6-terra", ChannelId: channel.Id,
		Enabled: true, Priority: &priority,
	}).Error)
	InitChannelCache()
	require.NoError(t, DB.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error)

	callbackName := "test:fail-channel-cache-ability-read"
	require.NoError(t, DB.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			tx.AddError(errors.New("forced ability read failure"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, DB.Callback().Query().Remove(callbackName)) })
	InitChannelCache()

	selected, err := GetRandomSatisfiedChannel("default", "gpt-5.6-terra", 0)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, channel.Id, selected.Id)
}

func pointerValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestListAvailableChannelModelPricesForGroupUsesExactMembership(t *testing.T) {
	setupChannelModelPriceTestDB(t)
	priorities := []int64{600, 700, 800, 500}
	channels := []Channel{
		{Name: "shared", Key: "key-1", Group: " default, mujian-canary ", Status: common.ChannelStatusEnabled, Priority: &priorities[0]},
		{Name: "similar-name", Key: "key-2", Group: "mujian-canary-extra", Status: common.ChannelStatusEnabled, Priority: &priorities[1]},
		{Name: "disabled", Key: "key-3", Group: "mujian-canary", Status: common.ChannelStatusManuallyDisabled, Priority: &priorities[2]},
		{Name: "canary", Key: "key-4", Group: "mujian-canary", Status: common.ChannelStatusEnabled, Priority: &priorities[3]},
	}
	require.NoError(t, DB.Create(&channels).Error)
	for index, channel := range channels {
		require.NoError(t, DB.Create(&ChannelModelPrice{
			ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "upstream",
			Provider: "provider", BillingType: ChannelModelBillingToken, Available: true, SyncedAt: int64(index + 1),
		}).Error)
	}

	canary, err := ListAvailableChannelModelPricesForGroup("claude-sonnet-4-6", "mujian-canary")
	require.NoError(t, err)
	require.Equal(t, []int{channels[0].Id, channels[3].Id}, []int{canary[0].ChannelID, canary[1].ChannelID})

	defaultGroup, err := ListAvailableChannelModelPricesForGroup("claude-sonnet-4-6", "default")
	require.NoError(t, err)
	require.Len(t, defaultGroup, 1)
	require.Equal(t, channels[0].Id, defaultGroup[0].ChannelID)

	snapshots, err := ListChannelModelPriceSnapshotsForGroup("claude-sonnet-4-6", "mujian-canary")
	require.NoError(t, err)
	require.Equal(t, []int{channels[3].Id, channels[2].Id, channels[0].Id}, []int{snapshots[0].ChannelID, snapshots[1].ChannelID, snapshots[2].ChannelID})
}

func TestCatalogPriceReadsDoNotLoadRawChatEvidence(t *testing.T) {
	setupChannelModelPriceTestDB(t)
	priority := int64(100)
	channels := []Channel{
		{Name: "chat", Key: "chat-key", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Name: "image", Key: "image-key", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, DB.Create(&channels).Error)
	prices := []ChannelModelPrice{
		{ChannelID: channels[0].Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "anthropic/claude-sonnet-4.6", Provider: "zenmux", BillingType: ChannelModelBillingToken, Available: true, OriginalPricing: `{"large":"chat-evidence"}`},
		{ChannelID: channels[1].Id, CatalogID: "nano-banana", UpstreamModelID: "gemini-image", Provider: "legacy", BillingType: ChannelModelBillingFixed, Available: true, OriginalPricing: `{"supported_endpoint_types":["gemini"]}`},
	}
	require.NoError(t, DB.Create(&prices).Error)
	for index, channel := range channels {
		modelID := prices[index].CatalogID
		require.NoError(t, DB.Create(&Ability{Group: "default", Model: modelID, ChannelId: channel.Id, Enabled: true, Priority: &priority}).Error)
	}

	chat, err := ListRoutableChannelModelPricesForGroup("claude-sonnet-4-6", "default", nil)
	require.NoError(t, err)
	require.Len(t, chat, 1)
	require.Empty(t, chat[0].OriginalPricing)

	image, err := ListRoutableChannelModelPricesForGroup("nano-banana", "default", nil)
	require.NoError(t, err)
	require.Len(t, image, 1)
	require.NotEmpty(t, image[0].OriginalPricing)
}

func TestRoutableCatalogIDProjectionIsDistinctAndGroupScoped(t *testing.T) {
	setupChannelModelPriceTestDB(t)
	priority := int64(600)
	channels := []Channel{
		{Name: "canary-a", Key: "key-a", Group: "mujian-canary", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Name: "canary-b", Key: "key-b", Group: "mujian-canary", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Name: "default", Key: "key-c", Group: "default", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, DB.Create(&channels).Error)
	prices := []ChannelModelPrice{
		{ChannelID: channels[0].Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "a", Provider: "a", BillingType: ChannelModelBillingToken, Available: true},
		{ChannelID: channels[1].Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "b", Provider: "b", BillingType: ChannelModelBillingToken, Available: true},
		{ChannelID: channels[2].Id, CatalogID: "claude-opus-5", UpstreamModelID: "c", Provider: "c", BillingType: ChannelModelBillingToken, Available: true},
	}
	require.NoError(t, DB.Create(&prices).Error)
	for index, channel := range channels {
		require.NoError(t, DB.Create(&Ability{
			Group: channel.Group, Model: prices[index].CatalogID, ChannelId: channel.Id, Enabled: true, Priority: &priority,
		}).Error)
	}

	ids, err := ListRoutableChannelModelPriceCatalogIDsForGroup("mujian-canary", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-sonnet-4-6"}, ids)
	ids, err = ListRoutableChannelModelPriceCatalogIDsForGroup("mujian-canary", []int{channels[1].Id})
	require.NoError(t, err)
	require.Equal(t, []string{"claude-sonnet-4-6"}, ids)
	ids, err = ListRoutableChannelModelPriceCatalogIDsForGroup("mujian-canary", []int{})
	require.NoError(t, err)
	require.Empty(t, ids)
}

func TestGroupSnapshotReadChunksLargeChannelSets(t *testing.T) {
	setupChannelModelPriceTestDB(t)
	const channelCount = 1005
	channels := make([]Channel, channelCount)
	for index := range channels {
		channels[index] = Channel{
			Name: "large-group", Key: "key", Group: "default",
			Status: common.ChannelStatusManuallyDisabled,
		}
	}
	for start := 0; start < len(channels); start += 100 {
		end := min(start+100, len(channels))
		batch := channels[start:end]
		require.NoError(t, DB.Create(&batch).Error)
	}
	prices := make([]ChannelModelPrice, channelCount)
	for index := range prices {
		prices[index] = ChannelModelPrice{
			ChannelID: channels[index].Id, CatalogID: "claude-sonnet-4-6",
			UpstreamModelID: "claude-sonnet-4-6", Provider: "test",
			BillingType: ChannelModelBillingToken, SyncedAt: int64(index + 1),
		}
	}
	for start := 0; start < len(prices); start += 100 {
		end := min(start+100, len(prices))
		batch := prices[start:end]
		require.NoError(t, DB.Create(&batch).Error)
	}

	snapshots, err := ListChannelModelPriceSnapshotsForGroupAll("default")

	require.NoError(t, err)
	require.Len(t, snapshots, channelCount)
	require.Equal(t, int64(channelCount), snapshots[0].SyncedAt)
	require.Equal(t, int64(1), snapshots[len(snapshots)-1].SyncedAt)
}

func TestFixAbilityRebuildsFromCurrentGroupAndStatus(t *testing.T) {
	setupChannelModelPriceTestDB(t)
	priority := int64(600)
	channel := Channel{
		Name: "canary", Key: "provider-key", Group: "mujian-canary", Models: "claude-sonnet-4-6",
		Status: common.ChannelStatusManuallyDisabled, Priority: &priority,
	}
	require.NoError(t, DB.Create(&channel).Error)
	require.NoError(t, DB.Create(&Ability{
		Group: "default", Model: "claude-sonnet-4-6", ChannelId: channel.Id,
		Enabled: true, Priority: &priority,
	}).Error)

	successes, failures, err := FixAbility()
	require.NoError(t, err)
	require.Equal(t, 1, successes)
	require.Zero(t, failures)

	var abilities []Ability
	require.NoError(t, DB.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	require.Equal(t, "mujian-canary", abilities[0].Group)
	require.False(t, abilities[0].Enabled)
}

func TestUpsertChannelModelPricesPreservesLegacyAttestation(t *testing.T) {
	setupChannelModelPriceTestDB(t)
	prices := []ChannelModelPrice{
		{CatalogID: "explicit", UpstreamModelID: "explicit", Provider: "provider", BillingType: ChannelModelBillingToken, TestedAt: 123},
		{CatalogID: "untested", UpstreamModelID: "untested", Provider: "provider", BillingType: ChannelModelBillingToken},
	}

	require.NoError(t, UpsertChannelModelPrices(prices, 42))
	var stored []ChannelModelPrice
	require.NoError(t, DB.Where("channel_id = ?", 42).Order("catalog_id").Find(&stored).Error)
	require.Len(t, stored, 2)
	require.Greater(t, stored[0].SyncedAt, int64(0))
	require.Equal(t, stored[0].SyncedAt, stored[0].TestedAt)
	require.Greater(t, stored[1].SyncedAt, int64(0))
	require.Equal(t, stored[1].SyncedAt, stored[1].TestedAt)
}

func TestUpsertUntestedChannelModelPricesRequiresSeparateProbe(t *testing.T) {
	setupChannelModelPriceTestDB(t)
	prices := []ChannelModelPrice{{
		CatalogID: "managed", UpstreamModelID: "managed", Provider: "provider",
		BillingType: ChannelModelBillingToken, TestedAt: 123,
	}}

	require.NoError(t, UpsertUntestedChannelModelPrices(prices, 42))
	var stored ChannelModelPrice
	require.NoError(t, DB.Where("channel_id = ?", 42).First(&stored).Error)
	require.Greater(t, stored.SyncedAt, int64(0))
	require.Zero(t, stored.TestedAt)
}

func TestDeleteDisabledChannelExceptTagsPreservesManagedRoutes(t *testing.T) {
	setupChannelModelPriceTestDB(t)
	managedTag := "mujian-provider:tabcode:chat"
	ordinaryTag := "ordinary"
	channels := []Channel{
		{Name: "managed", Key: "key", Status: common.ChannelStatusManuallyDisabled, Tag: &managedTag},
		{Name: "ordinary", Key: "key", Status: common.ChannelStatusManuallyDisabled, Tag: &ordinaryTag},
		{Name: "untagged", Key: "key", Status: common.ChannelStatusAutoDisabled},
		{Name: "enabled", Key: "key", Status: common.ChannelStatusEnabled, Tag: &ordinaryTag},
	}
	require.NoError(t, DB.Create(&channels).Error)

	deleted, err := DeleteDisabledChannelExceptTags([]string{managedTag})
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)

	var remaining []Channel
	require.NoError(t, DB.Order("id").Find(&remaining).Error)
	require.Equal(t, []string{"managed", "enabled"}, []string{remaining[0].Name, remaining[1].Name})
}
