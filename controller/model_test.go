package controller

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
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
