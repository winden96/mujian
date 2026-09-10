package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type mujianCatalogTestResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		Chat  []string `json:"chat"`
		Image []string `json:"image"`
		Items []struct {
			ID        string `json:"id"`
			Available bool   `json:"available"`
		} `json:"items"`
	} `json:"data"`
}

func TestUpdateMujianPreferencesRejectsMissingOrInvalidModelFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []string{
		`{}`,
		`null`,
		`{"unknown":"value"}`,
		`{"default_chat_model":""}`,
		`{"default_chat_model":"   "}`,
		`{"default_chat_model":null}`,
		`{"default_chat_model":123}`,
		`{"default_image_model":null}`,
	}
	for _, body := range tests {
		body := body
		t.Run(body, func(t *testing.T) {
			router := gin.New()
			router.PUT("/api/mujian/preferences", UpdateMujianPreferences)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/api/mujian/preferences", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")

			router.ServeHTTP(response, request)

			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			var payload mujianCatalogTestResponse
			require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
			require.False(t, payload.Success)
			require.NotEmpty(t, payload.Message)
		})
	}
}

func TestListMujianModelsReturnsServiceUnavailableWhenCatalogQueryFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, sqlDB.Close())
	})

	router := gin.New()
	router.GET("/api/mujian/models", ListMujianModels)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/mujian/models", nil))

	require.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
	var payload mujianCatalogTestResponse
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	require.False(t, payload.Success)
	require.Equal(t, "模型目录暂时不可用，请稍后重试", payload.Message)
}

func TestListMujianModelsFiltersCanaryCatalogByCurrentUserGroup(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "")
	gin.SetMode(gin.TestMode)
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.ChannelModelPrice{}, &model.Ability{}))
	model.DB = db
	users := []model.User{
		{Username: "catalog-default", Password: "hashed-password", DisplayName: "default", AffCode: "catalog-default", Group: "default"},
		{Username: "catalog-canary", Password: "hashed-password", DisplayName: "canary", AffCode: "catalog-canary", Group: "mujian-canary"},
	}
	require.NoError(t, db.Create(&users).Error)
	priority := int64(100)
	channels := []model.Channel{
		{Name: "default-opus", Key: "key-1", Group: "default", Models: "claude-opus-5", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Name: "canary-sonnet", Key: "key-2", Group: "mujian-canary", Models: "claude-sonnet-4-6", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Create(&[]model.Ability{
		{Group: "default", Model: "claude-opus-5", ChannelId: channels[0].Id, Enabled: true, Priority: &priority},
		{Group: "mujian-canary", Model: "claude-sonnet-4-6", ChannelId: channels[1].Id, Enabled: true, Priority: &priority},
	}).Error)
	require.NoError(t, db.Create(&[]model.ChannelModelPrice{
		{ChannelID: channels[0].Id, CatalogID: "claude-opus-5", UpstreamModelID: "claude-opus-5", Provider: "default", BillingType: model.ChannelModelBillingToken, Available: true},
		{ChannelID: channels[1].Id, CatalogID: "claude-sonnet-4-6", UpstreamModelID: "claude-sonnet-4-6", Provider: "canary", BillingType: model.ChannelModelBillingToken, Available: true},
	}).Error)

	requestCatalog := func(userID int) mujianCatalogTestResponse {
		t.Helper()
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("id", userID)
			c.Next()
		})
		router.GET("/api/mujian/models", ListMujianModels)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/mujian/models", nil))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var payload mujianCatalogTestResponse
		require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
		return payload
	}
	availability := func(payload mujianCatalogTestResponse, modelID string) bool {
		t.Helper()
		for _, item := range payload.Data.Items {
			if item.ID == modelID {
				return item.Available
			}
		}
		require.FailNow(t, "catalog model missing", modelID)
		return false
	}

	defaultPayload := requestCatalog(0)
	require.True(t, defaultPayload.Success)
	require.Equal(t, []string{"claude-opus-5"}, defaultPayload.Data.Chat)
	require.True(t, availability(defaultPayload, "claude-opus-5"))
	require.False(t, availability(defaultPayload, "claude-sonnet-4-6"))

	canaryPayload := requestCatalog(users[1].Id)
	require.True(t, canaryPayload.Success)
	require.Equal(t, []string{"claude-sonnet-4-6"}, canaryPayload.Data.Chat)
	require.True(t, availability(canaryPayload, "claude-sonnet-4-6"))
	require.False(t, availability(canaryPayload, "claude-opus-5"))
}
