package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestGetChannelWithAllowedChannelIDsFiltersBeforePrioritySelection(t *testing.T) {
	previousGroupColumn := commonGroupCol
	commonGroupCol = "`group`"
	t.Cleanup(func() { commonGroupCol = previousGroupColumn })

	require.NoError(t, DB.AutoMigrate(&Ability{}))
	require.NoError(t, DB.Exec("DELETE FROM abilities").Error)
	require.NoError(t, DB.Exec("DELETE FROM channels").Error)
	t.Cleanup(func() {
		_ = DB.Exec("DELETE FROM abilities").Error
		_ = DB.Exec("DELETE FROM channels").Error
	})

	highPriority := int64(320)
	lowPriority := int64(220)
	zeroWeight := uint(0)
	channels := []Channel{
		{Id: 7101, Name: "ineligible-high-priority", Key: "key-1", Status: common.ChannelStatusEnabled, Priority: &highPriority, Weight: &zeroWeight, Group: "default", Models: "gpt-image-2"},
		{Id: 7102, Name: "eligible-low-priority", Key: "key-2", Status: common.ChannelStatusEnabled, Priority: &lowPriority, Weight: &zeroWeight, Group: "default", Models: "gpt-image-2"},
	}
	require.NoError(t, DB.Create(&channels).Error)
	require.NoError(t, DB.Create(&[]Ability{
		{Group: "default", Model: "gpt-image-2", ChannelId: channels[0].Id, Enabled: true, Priority: &highPriority},
		{Group: "default", Model: "gpt-image-2", ChannelId: channels[1].Id, Enabled: true, Priority: &lowPriority},
	}).Error)

	previousCacheSetting := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousCacheSetting })

	selected, err := GetRandomSatisfiedChannelWithAllowedChannelIDs(
		"default", "gpt-image-2", 0, []int{channels[1].Id},
	)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, channels[1].Id, selected.Id)

	common.MemoryCacheEnabled = true
	InitChannelCache()
	selected, err = GetRandomSatisfiedChannelWithAllowedChannelIDs(
		"default", "gpt-image-2", 0, []int{channels[1].Id},
	)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, channels[1].Id, selected.Id)

	selected, err = GetRandomSatisfiedChannelWithAllowedChannelIDs(
		"default", "gpt-image-2", 0, []int{},
	)
	require.NoError(t, err)
	require.Nil(t, selected)
}

func TestChannelRetryUsesNextPriorityOnceAndNeverReplaysLastTier(t *testing.T) {
	previousGroupColumn := commonGroupCol
	previousCacheSetting := common.MemoryCacheEnabled
	commonGroupCol = "`group`"
	t.Cleanup(func() {
		commonGroupCol = previousGroupColumn
		common.MemoryCacheEnabled = previousCacheSetting
		if previousCacheSetting {
			InitChannelCache()
		}
	})

	require.NoError(t, DB.AutoMigrate(&Ability{}))
	require.NoError(t, DB.Exec("DELETE FROM abilities").Error)
	require.NoError(t, DB.Exec("DELETE FROM channels").Error)
	t.Cleanup(func() {
		_ = DB.Exec("DELETE FROM abilities").Error
		_ = DB.Exec("DELETE FROM channels").Error
	})

	highPriority := int64(600)
	lowPriority := int64(550)
	weight := uint(100)
	channels := []Channel{
		{Id: 7201, Name: "primary", Key: "key-1", Status: common.ChannelStatusEnabled, Priority: &highPriority, Weight: &weight, Group: "default", Models: "claude-sonnet-4-6"},
		{Id: 7202, Name: "backup", Key: "key-2", Status: common.ChannelStatusEnabled, Priority: &lowPriority, Weight: &weight, Group: "default", Models: "claude-sonnet-4-6"},
	}
	require.NoError(t, DB.Create(&channels).Error)
	require.NoError(t, DB.Create(&[]Ability{
		{Group: "default", Model: "claude-sonnet-4-6", ChannelId: channels[0].Id, Enabled: true, Priority: &highPriority, Weight: weight},
		{Group: "default", Model: "claude-sonnet-4-6", ChannelId: channels[1].Id, Enabled: true, Priority: &lowPriority, Weight: weight},
	}).Error)

	for _, memoryCache := range []bool{false, true} {
		common.MemoryCacheEnabled = memoryCache
		if memoryCache {
			InitChannelCache()
		}

		primary, err := GetRandomSatisfiedChannelWithFilters("default", "claude-sonnet-4-6", 0, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, primary)
		require.Equal(t, channels[0].Id, primary.Id)

		backup, err := GetRandomSatisfiedChannelWithFilters("default", "claude-sonnet-4-6", 1, nil, []int{primary.Id})
		require.NoError(t, err)
		require.NotNil(t, backup)
		require.Equal(t, channels[1].Id, backup.Id)

		exhausted, err := GetRandomSatisfiedChannelWithFilters("default", "claude-sonnet-4-6", 2, nil, []int{primary.Id, backup.Id})
		require.NoError(t, err)
		require.Nil(t, exhausted)

		singleChannelRetry, err := GetRandomSatisfiedChannelWithFilters(
			"default", "claude-sonnet-4-6", 1, []int{primary.Id}, []int{primary.Id},
		)
		require.NoError(t, err)
		require.Nil(t, singleChannelRetry)
	}
}

func TestChannelRetryUsesUnattemptedPeerAtSamePriority(t *testing.T) {
	previousGroupColumn := commonGroupCol
	previousCacheSetting := common.MemoryCacheEnabled
	commonGroupCol = "`group`"
	t.Cleanup(func() {
		commonGroupCol = previousGroupColumn
		common.MemoryCacheEnabled = previousCacheSetting
		if previousCacheSetting {
			InitChannelCache()
		}
	})

	require.NoError(t, DB.AutoMigrate(&Ability{}))
	require.NoError(t, DB.Exec("DELETE FROM abilities").Error)
	require.NoError(t, DB.Exec("DELETE FROM channels").Error)
	t.Cleanup(func() {
		_ = DB.Exec("DELETE FROM abilities").Error
		_ = DB.Exec("DELETE FROM channels").Error
	})
	priority := int64(600)
	weight := uint(100)
	channels := []Channel{
		{Id: 7301, Name: "peer-a", Key: "key-a", Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight, Group: "default", Models: "peer-model"},
		{Id: 7302, Name: "peer-b", Key: "key-b", Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight, Group: "default", Models: "peer-model"},
	}
	require.NoError(t, DB.Create(&channels).Error)
	require.NoError(t, DB.Create(&[]Ability{
		{Group: "default", Model: "peer-model", ChannelId: channels[0].Id, Enabled: true, Priority: &priority, Weight: weight},
		{Group: "default", Model: "peer-model", ChannelId: channels[1].Id, Enabled: true, Priority: &priority, Weight: weight},
	}).Error)

	for _, memoryCache := range []bool{false, true} {
		common.MemoryCacheEnabled = memoryCache
		if memoryCache {
			InitChannelCache()
		}
		first, err := GetRandomSatisfiedChannelWithFilters("default", "peer-model", 0, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, first)
		second, err := GetRandomSatisfiedChannelWithFilters("default", "peer-model", 1, nil, []int{first.Id})
		require.NoError(t, err)
		require.NotNil(t, second)
		require.NotEqual(t, first.Id, second.Id)
	}
}
