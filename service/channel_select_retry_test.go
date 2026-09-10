package service

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAutoGroupsForRequestFreezesOrderedCandidates(t *testing.T) {
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["route-a","route-b"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"route-a":"A","route-b":"B","route-x":"X"}`))
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Equal(t, []string{"route-a", "route-b"}, AutoGroupsForRequest(ctx, "default"))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["route-x","route-a","route-b"]`))
	require.Equal(t, []string{"route-a", "route-b"}, AutoGroupsForRequest(ctx, "default"))
}

func TestCatalogPricedRetryUsesBestUnattemptedChannel(t *testing.T) {
	previousDB := model.DB
	previousCacheSetting := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelModelPrice{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousCacheSetting
		if previousCacheSetting {
			model.InitChannelCache()
		}
	})

	primaryPriority, backupPriority := int64(600), int64(550)
	weight := uint(100)
	channels := []model.Channel{
		{Id: 11, Key: "primary-a", Name: "primary-a", Group: "default", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &primaryPriority, Weight: &weight},
		{Id: 12, Key: "primary-b", Name: "primary-b", Group: "default", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &primaryPriority, Weight: &weight},
		{Id: 13, Key: "lower", Name: "lower", Group: "default", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &backupPriority, Weight: &weight},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "default", Model: "claude-sonnet-4-6", ChannelId: 11, Enabled: true, Priority: &primaryPriority, Weight: weight},
		{Group: "default", Model: "claude-sonnet-4-6", ChannelId: 12, Enabled: true, Priority: &primaryPriority, Weight: weight},
		{Group: "default", Model: "claude-sonnet-4-6", ChannelId: 13, Enabled: true, Priority: &backupPriority, Weight: weight},
	}).Error)
	model.InitChannelCache()

	selectRetry := func(catalogPriced bool) *model.Channel {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Set("use_channel", []string{"11"})
		common.SetContextKey(ctx, constant.ContextKeyCatalogPriceAuthorized, catalogPriced)
		if catalogPriced {
			common.SetContextKey(ctx, constant.ContextKeyCatalogPriceRoutes, map[string][]int{"default": {11, 12}})
		}
		retry := 1
		channel, _, selectErr := CacheGetRandomSatisfiedChannel(&RetryParam{
			Ctx: ctx, TokenGroup: "default", ModelName: "claude-sonnet-4-6", Retry: &retry,
		})
		require.NoError(t, selectErr)
		return channel
	}

	catalogRetry := selectRetry(true)
	require.NotNil(t, catalogRetry)
	require.Equal(t, 12, catalogRetry.Id)

	legacyRetry := selectRetry(false)
	require.NotNil(t, legacyRetry)
	require.Equal(t, 13, legacyRetry.Id)

	model.CacheUpdateChannelStatus(11, common.ChannelStatusManuallyDisabled)
	catalogAfterDisable := selectRetry(true)
	require.NotNil(t, catalogAfterDisable)
	require.Equal(t, 12, catalogAfterDisable.Id)
}

func TestCatalogAutoRetryCannotEscapeAuthorizedGroupFrontier(t *testing.T) {
	previousDB := model.DB
	previousCacheSetting := common.MemoryCacheEnabled
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelModelPrice{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["route-a","route-b","route-c"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"route-a":"A","route-b":"B","route-c":"C"}`))
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousCacheSetting
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
		if previousCacheSetting {
			model.InitChannelCache()
		}
	})

	priority := int64(600)
	weight := uint(100)
	channels := []model.Channel{
		{Id: 51, Key: "initial", Name: "initial", Group: "route-a", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight},
		{Id: 52, Key: "frontier", Name: "frontier", Group: "route-b", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight},
		{Id: 53, Key: "later", Name: "later", Group: "route-c", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "route-a", Model: "claude-sonnet-4-6", ChannelId: 51, Enabled: true, Priority: &priority, Weight: weight},
		{Group: "route-b", Model: "claude-sonnet-4-6", ChannelId: 52, Enabled: true, Priority: &priority, Weight: weight},
		{Group: "route-c", Model: "claude-sonnet-4-6", ChannelId: 53, Enabled: true, Priority: &priority, Weight: weight},
	}).Error)
	model.InitChannelCache()

	newContext := func() *gin.Context {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
		common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, true)
		common.SetContextKey(ctx, constant.ContextKeyAutoGroup, "route-a")
		common.SetContextKey(ctx, constant.ContextKeyAutoGroupIndex, 0)
		common.SetContextKey(ctx, constant.ContextKeyAutoGroupRetryIndex, 1)
		common.SetContextKey(ctx, constant.ContextKeyCatalogPriceAuthorized, true)
		common.SetContextKey(ctx, constant.ContextKeyCatalogPriceRoutes, map[string][]int{
			"route-a": {51},
			"route-b": {52},
		})
		ctx.Set("use_channel", []string{"51"})
		return ctx
	}
	retry := 1
	selected, group, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: newContext(), TokenGroup: "auto", ModelName: "claude-sonnet-4-6", Retry: &retry,
	})
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, 52, selected.Id)
	require.Equal(t, "route-b", group)

	model.CacheUpdateChannelStatus(52, common.ChannelStatusManuallyDisabled)
	retry = 1
	selected, _, err = CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: newContext(), TokenGroup: "auto", ModelName: "claude-sonnet-4-6", Retry: &retry,
	})
	require.NoError(t, err)
	require.Nil(t, selected, "route-c was not part of the request-frozen price frontier")
}

func TestAutoSelectionScansInitiallyButRetryRequiresCrossGroupPermission(t *testing.T) {
	previousDB := model.DB
	previousCacheSetting := common.MemoryCacheEnabled
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelModelPrice{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["route-a","route-b","route-c"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"route-a":"A","route-b":"B","route-c":"C"}`))
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousCacheSetting
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
		if previousCacheSetting {
			model.InitChannelCache()
		}
	})

	priority := int64(600)
	weight := uint(100)
	channels := []model.Channel{
		{Id: 21, Key: "b", Name: "B", Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight, Group: "route-b", Models: "test-model"},
		{Id: 22, Key: "c", Name: "C", Status: common.ChannelStatusEnabled, Priority: &priority, Weight: &weight, Group: "route-c", Models: "test-model"},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "route-b", Model: "test-model", ChannelId: 21, Enabled: true, Priority: &priority, Weight: weight},
		{Group: "route-c", Model: "test-model", ChannelId: 22, Enabled: true, Priority: &priority, Weight: weight},
	}).Error)
	model.InitChannelCache()

	newContext := func(crossGroupRetry bool) *gin.Context {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
		common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, crossGroupRetry)
		return ctx
	}
	selectTwice := func(crossGroupRetry, managed bool) *model.Channel {
		ctx := newContext(crossGroupRetry)
		ctx.Set("mujian_managed_provider", managed)
		retry := 0
		param := &RetryParam{Ctx: ctx, TokenGroup: "auto", ModelName: "test-model", Retry: &retry}
		first, firstGroup, firstErr := CacheGetRandomSatisfiedChannel(param)
		require.NoError(t, firstErr)
		require.NotNil(t, first)
		require.Equal(t, 21, first.Id)
		require.Equal(t, "route-b", firstGroup)

		ctx.Set("use_channel", []string{"21"})
		retry = 1
		second, _, secondErr := CacheGetRandomSatisfiedChannel(param)
		require.NoError(t, secondErr)
		return second
	}

	legacyRetry := selectTwice(false, false)
	require.NotNil(t, legacyRetry)
	require.Equal(t, 21, legacyRetry.Id)

	withoutPermission := selectTwice(false, true)
	require.Nil(t, withoutPermission)

	withPermission := selectTwice(true, true)
	require.NotNil(t, withPermission)
	require.Equal(t, 22, withPermission.Id)
}

func TestAutoSelectionContinuesAfterLocallyRejectedManagedRoute(t *testing.T) {
	previousDB := model.DB
	previousCacheSetting := common.MemoryCacheEnabled
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelModelPrice{}))
	model.DB = db
	common.MemoryCacheEnabled = true
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["route-a","route-b"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"route-a":"A","route-b":"B"}`))
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled = previousCacheSetting
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
		if previousCacheSetting {
			model.InitChannelCache()
		}
	})

	primaryPriority, backupPriority := int64(600), int64(550)
	weight := uint(100)
	managedTag := "mujian-provider:zenmux:chat"
	channels := []model.Channel{
		{Id: 31, Key: "stale", Name: "stale managed", Status: common.ChannelStatusEnabled, Priority: &primaryPriority, Weight: &weight, Group: "route-a", Models: "claude-sonnet-4-6", Tag: &managedTag},
		{Id: 32, Key: "healthy", Name: "healthy ordinary", Status: common.ChannelStatusEnabled, Priority: &backupPriority, Weight: &weight, Group: "route-b", Models: "claude-sonnet-4-6"},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "route-a", Model: "claude-sonnet-4-6", ChannelId: 31, Enabled: true, Priority: &primaryPriority, Weight: weight},
		{Group: "route-b", Model: "claude-sonnet-4-6", ChannelId: 32, Enabled: true, Priority: &backupPriority, Weight: weight},
	}).Error)
	model.InitChannelCache()
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", 31).Update("status", common.ChannelStatusManuallyDisabled).Error)
	require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", 31).Update("enabled", false).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(ctx, constant.ContextKeyTokenCrossGroupRetry, false)
	retry := 0
	channel, group, err := CacheGetRandomSatisfiedChannel(&RetryParam{
		Ctx: ctx, TokenGroup: "auto", ModelName: "claude-sonnet-4-6", Retry: &retry,
	})

	require.NoError(t, err)
	require.NotNil(t, channel)
	require.Equal(t, 32, channel.Id)
	require.Equal(t, "route-b", group)
	require.Empty(t, ctx.GetStringSlice("use_channel"))
	require.Equal(t, []string{"31"}, ctx.GetStringSlice(locallyExcludedChannelIDsContextKey))
}
