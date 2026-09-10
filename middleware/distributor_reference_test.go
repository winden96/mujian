package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDistributeAllowsTaskFetchWithoutSelectingChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/mj/task/fetch", Distribute(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/mj/task/fetch", nil))

	require.Equal(t, http.StatusNoContent, response.Code)
}

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

func TestManagedSpecificChannelResolvesConcreteAutoGroup(t *testing.T) {
	previousDB := model.DB
	previousAutoGroups := setting.AutoGroups2JsonString()
	previousUsableGroups := setting.UserUsableGroups2JSONString()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.ChannelModelPrice{}))
	model.DB = db
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["mujian-canary"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"mujian-canary":"Canary"}`))
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAutoGroups))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsableGroups))
	})

	priority := int64(600)
	baseURL := "https://zenmux.ai/api/anthropic"
	tag := "mujian-provider:zenmux:chat"
	header := `{"anthropic-version":"2023-06-01"}`
	mapping := `{"claude-sonnet-4-6":"anthropic/claude-sonnet-4.6"}`
	testModel := "claude-sonnet-4-6"
	channel := model.Channel{
		Type: constant.ChannelTypeAnthropic, Name: "ZenMux", Key: "zenmux-test-key",
		Status: common.ChannelStatusEnabled, Group: "mujian-canary", Models: "claude-sonnet-4-6",
		BaseURL: &baseURL, Tag: &tag, HeaderOverride: &header, ModelMapping: &mapping,
		TestModel: &testModel, Priority: &priority, TestTime: 200,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group: "mujian-canary", Model: "claude-sonnet-4-6", ChannelId: channel.Id,
		Enabled: true, Priority: &priority,
	}).Error)
	require.NoError(t, db.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "anthropic/claude-sonnet-4.6",
		Provider: "zenmux", BillingType: model.ChannelModelBillingToken, Currency: "USD",
		InputPrice: 3, OutputPrice: 15, CacheRatio: 0.1, CacheCreationRatio: 1.25,
		Available: true, SyncedAt: 100, TestedAt: 100,
	}).Error)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "auto")
	common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")

	resolved, err := resolveDirectAutoGroup(ctx, &channel, "claude-sonnet-4-6")

	require.NoError(t, err)
	require.Equal(t, channel.Id, resolved.Id)
	require.Equal(t, "mujian-canary", service.CurrentRoutingGroup(ctx))
	require.True(t, service.IsManagedChannelDBValidated(ctx, channel.Id, "claude-sonnet-4-6", "mujian-canary"))
}

func TestManagedChannelRejectsAffinityParamTemplate(t *testing.T) {
	priority := int64(600)
	baseURL := "https://zenmux.ai/api/anthropic"
	tag := "mujian-provider:zenmux:chat"
	header := `{"anthropic-version":"2023-06-01"}`
	mapping := `{"claude-sonnet-4-6":"anthropic/claude-sonnet-4.6"}`
	testModel := "claude-sonnet-4-6"
	channel := model.Channel{
		Id: 8103, Type: constant.ChannelTypeAnthropic, Name: "ZenMux", Key: "zenmux-test-key",
		Status: common.ChannelStatusEnabled, Group: "default", Models: "claude-sonnet-4-6",
		BaseURL: &baseURL, Tag: &tag, HeaderOverride: &header, ModelMapping: &mapping,
		TestModel: &testModel, Priority: &priority, TestTime: 200,
	}

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v1/messages",
		strings.NewReader(`{"model":"claude-sonnet-4-6","metadata":{"user_id":"affinity-session"}}`),
	)
	common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "default")
	_, _ = service.GetPreferredChannelByAffinity(ctx, "claude-sonnet-4-6", "default")
	service.MarkManagedChannelDBValidated(ctx, "claude-sonnet-4-6", types.ChannelModelPriceSnapshot{
		ChannelID: channel.Id, CatalogID: "claude-sonnet-4-6", RoutingGroup: "default",
	})

	apiErr := SetupContextForSelectedChannel(ctx, &channel, "claude-sonnet-4-6")

	require.NotNil(t, apiErr)
	require.Contains(t, apiErr.Error(), "不允许应用请求参数覆写模板")
}

func TestOrdinarySpecificChannelResolvesConcreteAutoGroupFromDB(t *testing.T) {
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

	priority := int64(600)
	channel := model.Channel{
		Id: 91, Type: constant.ChannelTypeAnthropic, Name: "ordinary", Key: "ordinary-key",
		Status: common.ChannelStatusEnabled, Group: "route-b", Models: "claude-sonnet-4-6", Priority: &priority,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group: "route-b", Model: "claude-sonnet-4-6", ChannelId: channel.Id,
		Enabled: true, Priority: &priority,
	}).Error)
	model.InitChannelCache()

	newContext := func() *gin.Context {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(ctx, constant.ContextKeyUsingGroup, "auto")
		common.SetContextKey(ctx, constant.ContextKeyUserGroup, "default")
		return ctx
	}
	ctx := newContext()
	resolved, err := resolveDirectAutoGroup(ctx, &channel, "claude-sonnet-4-6")
	require.NoError(t, err)
	require.Equal(t, channel.Id, resolved.Id)
	require.Equal(t, "route-b", service.CurrentRoutingGroup(ctx))

	require.NoError(t, db.Model(&model.Ability{}).
		Where("channel_id = ?", channel.Id).Update("enabled", false).Error)
	staleCacheContext := newContext()
	resolved, err = resolveDirectAutoGroup(staleCacheContext, &channel, "claude-sonnet-4-6")
	require.Nil(t, resolved)
	require.ErrorContains(t, err, "不属于当前用户可用的自动分组")
}
