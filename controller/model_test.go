package controller

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestListModelsIncludesCustomChannelPricedTokenModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.ChannelModelPrice{}, &model.Ability{}))

	channel := model.Channel{Name: "priced-channel", Key: "provider-key", Status: 1}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "mujian-custom-priced-model", UpstreamModelID: "upstream-model",
		Provider: "test", BillingType: model.ChannelModelBillingToken, InputPrice: 0.1, OutputPrice: 0.2,
		Currency: "USD", Available: true,
	}).Error)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	common.SetContextKey(context, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(context, constant.ContextKeyTokenModelLimit, map[string]bool{"mujian-custom-priced-model": true})

	ListModels(context, constant.ChannelTypeOpenAI)

	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response.Data, 1)
	require.Equal(t, "mujian-custom-priced-model", response.Data[0].ID)
}

func TestListModelsUnrestrictedTokenChannelPricing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB, previousPath := model.DB, common.SQLitePath
	previousSQLite, previousMySQL, previousPostgres := common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL
	previousRedis, previousMaster := common.RedisEnabled, common.IsMasterNode
	previousSelfUse := operation_setting.SelfUseModeEnabled
	previousRatios := ratio_setting.ModelRatio2JSONString()
	previousAuto, previousUsable := setting.AutoGroups2JsonString(), setting.UserUsableGroups2JSONString()
	t.Cleanup(func() {
		model.DB, common.SQLitePath = previousDB, previousPath
		common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = previousSQLite, previousMySQL, previousPostgres
		common.RedisEnabled, common.IsMasterNode = previousRedis, previousMaster
		operation_setting.SelfUseModeEnabled = previousSelfUse
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousRatios))
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(previousAuto))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(previousUsable))
	})
	t.Setenv("SQL_DSN", "")
	t.Setenv("LOG_SQL_DSN", "")
	common.SQLitePath = "file:" + t.Name() + "?mode=memory&cache=shared"
	common.UsingSQLite, common.UsingMySQL, common.UsingPostgreSQL = false, false, false
	common.RedisEnabled, common.IsMasterNode = false, false
	operation_setting.SelfUseModeEnabled = false
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o":2.5}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","vip"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"default","vip":"vip"}`))
	require.NoError(t, model.InitDB())
	db := model.DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.ChannelModelPrice{}, &model.Ability{}))
	user := model.User{Username: "model-list-user", Group: "default"}
	require.NoError(t, db.Create(&user).Error)

	for _, route := range []struct {
		name      string
		group     string
		status    int
		enabled   bool
		available bool
	}{
		{"custom-default", "default", 1, true, true},
		{"custom-vip", "vip", 1, true, true},
		{"custom-disabled-channel", "default", 2, true, true},
		{"custom-disabled-route", "default", 1, false, true},
		{"custom-unavailable-price", "default", 1, true, false},
		{"custom-private", "private", 1, true, true},
	} {
		channel := model.Channel{Name: route.name, Group: route.group, Key: "test-provider-key", Status: route.status}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, db.Create(&model.Ability{Group: route.group, Model: route.name, ChannelId: channel.Id, Enabled: route.enabled}).Error)
		require.NoError(t, db.Create(&model.ChannelModelPrice{
			ChannelID: channel.Id, CatalogID: route.name, UpstreamModelID: route.name, Provider: "test",
			BillingType: model.ChannelModelBillingToken, InputPrice: 0.1, OutputPrice: 0.2, Currency: "USD", Available: route.available,
		}).Error)
	}
	// A routable model with no pricing must stay hidden; a globally priced
	// model must retain its existing visibility without a channel price row.
	channel := model.Channel{Name: "global-pricing", Group: "default", Key: "test-provider-key", Status: 1}
	require.NoError(t, db.Create(&channel).Error)
	for _, name := range []string{"custom-unpriced", "gpt-4o"} {
		require.NoError(t, db.Create(&model.Ability{Group: "default", Model: name, ChannelId: channel.Id, Enabled: true}).Error)
	}
	// Pricing in another group cannot make an unpriced default route visible.
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "custom-vip", ChannelId: channel.Id, Enabled: true}).Error)

	for _, test := range []struct {
		name  string
		group string
		want  []string
	}{
		{"user group", "", []string{"custom-default", "gpt-4o"}},
		{"explicit token group", "vip", []string{"custom-vip"}},
		{"automatic groups", "auto", []string{"custom-default", "custom-vip", "gpt-4o"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			context.Set("id", user.Id)
			common.SetContextKey(context, constant.ContextKeyTokenGroup, test.group)
			ListModels(context, constant.ChannelTypeOpenAI)
			require.Equal(t, 200, recorder.Code)
			var response struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			var ids []string
			for _, item := range response.Data {
				ids = append(ids, item.ID)
			}
			require.ElementsMatch(t, test.want, ids)
		})
	}
	t.Run("pricing query failure", func(t *testing.T) {
		require.NoError(t, db.Migrator().DropTable(&model.ChannelModelPrice{}))
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Set("id", user.Id)
		ListModels(context, constant.ChannelTypeOpenAI)
		require.Equal(t, 500, recorder.Code)
		require.Contains(t, recorder.Body.String(), "failed to load model pricing")
	})
}
