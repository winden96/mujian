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
