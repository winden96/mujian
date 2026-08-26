package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNormalizeMidjourneyOriginAction(t *testing.T) {
	tests := []struct {
		name         string
		relayMode    int
		request      dto.MidjourneyRequest
		taskID       string
		action       string
		index        int
		errorMessage string
	}{
		{
			name:      "change",
			relayMode: relayconstant.RelayModeMidjourneyChange,
			request:   dto.MidjourneyRequest{TaskId: "task-1", Action: constant.MjActionUpscale, Index: 1},
			taskID:    "task-1",
			action:    constant.MjActionUpscale,
			index:     1,
		},
		{
			name:      "simple change",
			relayMode: relayconstant.RelayModeMidjourneySimpleChange,
			request:   dto.MidjourneyRequest{Content: "task-2 V4"},
			taskID:    "task-2",
			action:    constant.MjActionVariation,
			index:     4,
		},
		{
			name:      "modal",
			relayMode: relayconstant.RelayModeMidjourneyModal,
			request:   dto.MidjourneyRequest{TaskId: "task-3"},
			taskID:    "task-3",
			action:    constant.MjActionModal,
		},
		{
			name:      "video",
			relayMode: relayconstant.RelayModeMidjourneyVideo,
			request:   dto.MidjourneyRequest{TaskId: "task-4"},
			taskID:    "task-4",
			action:    constant.MjActionVideo,
		},
		{
			name:         "action missing task id",
			relayMode:    relayconstant.RelayModeMidjourneyChange,
			request:      dto.MidjourneyRequest{Action: constant.MjActionUpscale, Index: 1},
			errorMessage: "task_id_is_required",
		},
		{
			name:         "missing task id",
			relayMode:    relayconstant.RelayModeMidjourneyVideo,
			errorMessage: "task_id_is_required",
		},
		{
			name:         "malformed simple change",
			relayMode:    relayconstant.RelayModeMidjourneySimpleChange,
			request:      dto.MidjourneyRequest{Content: "task u"},
			errorMessage: "content_parse_failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := test.request
			taskID, err := normalizeMidjourneyOriginAction(test.relayMode, &request)
			if test.errorMessage != "" {
				require.NotNil(t, err)
				require.Equal(t, test.errorMessage, err.Description)
				return
			}

			require.Nil(t, err)
			require.Equal(t, test.taskID, taskID)
			require.Equal(t, test.taskID, request.TaskId)
			require.Equal(t, test.action, request.Action)
			require.Equal(t, test.index, request.Index)
		})
	}
}

func TestRelayMidjourneyImageEnforcesTaskOwnership(t *testing.T) {
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })

	db, err := gorm.Open(
		sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Midjourney{}))
	model.DB = db
	require.NoError(t, db.Create(&model.Midjourney{UserId: 1, MjId: "task-owner"}).Error)

	for _, test := range []struct {
		name   string
		userID int
		role   int
		found  bool
	}{
		{name: "owner", userID: 1, found: true},
		{name: "different user", userID: 2, found: false},
		{name: "administrator", userID: 2, role: common.RoleAdminUser, found: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.found, getMidjourneyImageTask(test.userID, test.role, "task-owner") != nil)
		})
	}

	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/mj/image/task-owner", nil)
	context.Params = gin.Params{{Key: "id", Value: "task-owner"}}
	context.Set("id", 2)
	RelayMidjourneyImage(context)
	require.Equal(t, http.StatusNotFound, response.Code)
	require.JSONEq(t, `{"error":"midjourney_task_not_found"}`, response.Body.String())
}

func TestSetupMidjourneyOriginChannelReplacesRequestAndBillingContext(t *testing.T) {
	baseURL := "https://origin.example"
	channel := &model.Channel{
		Id:      42,
		Type:    constant.ChannelTypeMidjourney,
		Key:     "origin-secret",
		Name:    "origin",
		Status:  common.ChannelStatusEnabled,
		BaseURL: &baseURL,
	}

	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/mj/submit/change", nil)
	common.SetContextKey(context, constant.ContextKeyChannelId, 99)
	common.SetContextKey(context, constant.ContextKeyChannelType, constant.ChannelTypeMidjourneyPlus)
	common.SetContextKey(context, constant.ContextKeyChannelKey, "distributed-secret")
	common.SetContextKey(context, constant.ContextKeyChannelBaseUrl, "https://distributed.example")
	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "mj_upscale",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId:      99,
			ChannelType:    constant.ChannelTypeMidjourneyPlus,
			ChannelBaseUrl: "https://distributed.example",
			ApiKey:         "distributed-secret",
		},
	}

	require.NoError(t, setupMidjourneyOriginChannel(context, channel, relayInfo))
	require.Equal(t, channel.Id, common.GetContextKeyInt(context, constant.ContextKeyChannelId))
	require.Equal(t, channel.Type, common.GetContextKeyInt(context, constant.ContextKeyChannelType))
	require.Equal(t, channel.Key, common.GetContextKeyString(context, constant.ContextKeyChannelKey))
	require.Equal(t, baseURL, common.GetContextKeyString(context, constant.ContextKeyChannelBaseUrl))
	require.Equal(t, channel.Id, relayInfo.ChannelId)
	require.Equal(t, channel.Type, relayInfo.ChannelType)
	require.Equal(t, channel.Key, relayInfo.ApiKey)
	require.Equal(t, baseURL, relayInfo.ChannelBaseUrl)
}

func TestRelayMidjourneyTaskImageSeedUsesOriginChannelURLAndKey(t *testing.T) {
	type receivedRequest struct {
		path          string
		secret        string
		authorization string
	}
	received := make(chan receivedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		received <- receivedRequest{
			path:          request.URL.Path,
			secret:        request.Header.Get("mj-api-secret"),
			authorization: request.Header.Get("Authorization"),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"description":"ok","result":"seed"}`))
	}))
	t.Cleanup(upstream.Close)

	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })
	db, err := gorm.Open(
		sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Midjourney{}))
	model.DB = db
	baseURL := upstream.URL
	require.NoError(t, db.Create(&model.Channel{
		Id:      42,
		Type:    constant.ChannelTypeMidjourney,
		Key:     "origin-secret",
		Name:    "origin",
		Status:  common.ChannelStatusEnabled,
		BaseURL: &baseURL,
	}).Error)
	require.NoError(t, db.Create(&model.Midjourney{
		UserId:    7,
		MjId:      "origin-task",
		ChannelId: 42,
	}).Error)

	service.InitHttpClient()
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/mj/task/origin-task/image-seed", nil)
	context.Request.Header.Set("Authorization", "Bearer user-token")
	context.Params = gin.Params{{Key: "id", Value: "origin-task"}}
	context.Set("id", 7)
	common.SetContextKey(context, constant.ContextKeyChannelId, 99)
	common.SetContextKey(context, constant.ContextKeyChannelKey, "distributed-secret")
	common.SetContextKey(context, constant.ContextKeyChannelBaseUrl, "https://distributed.example")

	require.Nil(t, RelayMidjourneyTaskImageSeed(context))
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, 42, common.GetContextKeyInt(context, constant.ContextKeyChannelId))
	require.Equal(t, "origin-secret", common.GetContextKeyString(context, constant.ContextKeyChannelKey))
	require.Equal(t, upstream.URL, common.GetContextKeyString(context, constant.ContextKeyChannelBaseUrl))

	upstreamRequest := <-received
	require.Equal(t, "/mj/task/origin-task/image-seed", upstreamRequest.path)
	require.Equal(t, "origin-secret", upstreamRequest.secret)
	require.Empty(t, upstreamRequest.authorization)
}
