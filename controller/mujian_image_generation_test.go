package controller

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	mujianservice "github.com/QuantumNous/new-api/service/mujian"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupMujianImageControllerTest(t *testing.T) (model.User, string, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Log{}, &model.Ability{}, &model.Channel{}, &model.ChannelModelPrice{},
		&model.TopUp{}, &model.MujianProject{}, &model.MujianScene{}, &model.MujianShot{},
		&model.MujianAgentSession{}, &model.MujianAgentMessage{},
		&model.MujianImageGeneration{}, &model.MujianImageReference{}, &model.MujianUserPreference{}, &model.Task{},
	))
	user := model.User{Username: "image-controller", Password: "hashed", DisplayName: "image-controller", AffCode: "image-controller"}
	require.NoError(t, db.Create(&user).Error)
	project, err := mujianservice.CreateProject(user.Id, "空项目", "现代修仙喜剧")
	require.NoError(t, err)
	workspace, err := mujianservice.GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	channel := model.Channel{Name: "controller-gpt-image", Key: "provider-key", Status: 1, Models: "gpt-image-2"}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "gpt-image-2", UpstreamModelID: "gpt-image-2", Provider: "test",
		BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.1, Currency: "USD", Available: true,
		ReferenceProtocol: mujianprovider.ReferenceProtocolOpenAIEditMultipart, MaxReferenceImages: 3,
	}).Error)
	return user, project.ID, workspace.ActiveSessionID
}

func mujianImageTestRouter(userID int) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", userID)
		c.Next()
	})
	router.POST("/api/mujian/projects/:projectId/image-generations", CreateMujianImageGeneration)
	router.GET("/api/mujian/projects/:projectId/image-generations/:generationId", GetMujianImageGeneration)
	router.GET("/api/mujian/projects/:projectId/image-generations/:generationId/content", GetMujianImageGenerationContent)
	router.GET("/api/mujian/projects/:projectId/image-generations/:generationId/references/:referenceId/content", GetMujianImageReferenceContent)
	return router
}

func TestMujianImageGenerationMultipartAPIFlow(t *testing.T) {
	user, projectID, sessionID := setupMujianImageControllerTest(t)
	require.False(t, model.DB.Migrator().HasColumn(&model.MujianImageGeneration{}, "shot_id"))
	type capturedUpstreamRequest struct {
		names []string
		err   error
	}
	upstreamCalls := make(chan capturedUpstreamRequest, 1)
	generatedImage := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x7f}
	encodedGeneratedImage := base64.StdEncoding.EncodeToString(generatedImage)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured := capturedUpstreamRequest{}
		if err := request.ParseMultipartForm(1 << 20); err != nil {
			captured.err = err
		} else {
			for _, header := range request.MultipartForm.File["image[]"] {
				captured.names = append(captured.names, header.Filename)
			}
		}
		upstreamCalls <- captured
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set(common.RequestIdKey, "controller-relay-request")
		_, _ = io.WriteString(writer, `{"data":[{"b64_json":"`+encodedGeneratedImage+`"}]}`)
	}))
	defer upstream.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", upstream.URL)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("prompt", "保持两张参考图的人物一致"))
	require.NoError(t, writer.WriteField("session_id", sessionID))
	require.NoError(t, writer.WriteField("engine", "gpt"))
	require.NoError(t, writer.WriteField("model_id", "gpt-image-2"))
	require.NoError(t, writer.WriteField("aspect_ratio", "9:16"))
	for index, name := range []string{"first.png", "second.png"} {
		part, err := writer.CreateFormFile("reference_images", name)
		require.NoError(t, err)
		_, err = part.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', byte(index)})
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())

	router := mujianImageTestRouter(user.Id)
	createRequest := httptest.NewRequest(http.MethodPost, "/api/mujian/projects/"+projectID+"/image-generations", &body)
	createRequest.Header.Set("Content-Type", writer.FormDataContentType())
	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, createRequest)
	require.Equal(t, http.StatusAccepted, createResponse.Code, createResponse.Body.String())
	var created struct {
		Success bool                          `json:"success"`
		Data    mujianservice.ImageGeneration `json:"data"`
	}
	require.NoError(t, common.Unmarshal(createResponse.Body.Bytes(), &created))
	require.True(t, created.Success)
	require.NotContains(t, createResponse.Body.String(), `"shot_id"`)
	require.Equal(t, sessionID, created.Data.SessionID)
	require.Len(t, created.Data.References, 2)

	var succeeded mujianservice.ImageGeneration
	require.Eventually(t, func() bool {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/mujian/projects/"+projectID+"/image-generations/"+created.Data.ID+"?session_id="+sessionID, nil)
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			return false
		}
		var payload struct {
			Data mujianservice.ImageGeneration `json:"data"`
		}
		if common.Unmarshal(response.Body.Bytes(), &payload) != nil {
			return false
		}
		succeeded = payload.Data
		return succeeded.Status == "succeeded"
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, "/api/mujian/projects/"+projectID+"/image-generations/"+succeeded.ID+"/content?session_id="+sessionID, succeeded.ResultURL)
	require.Equal(t, "controller-relay-request", succeeded.RelayRequestID)
	upstreamRequest := <-upstreamCalls
	require.NoError(t, upstreamRequest.err)
	require.Equal(t, []string{"first.png", "second.png"}, upstreamRequest.names)
	generatedResponse := httptest.NewRecorder()
	generatedRequest := httptest.NewRequest(http.MethodGet, succeeded.ResultURL, nil)
	router.ServeHTTP(generatedResponse, generatedRequest)
	require.Equal(t, http.StatusOK, generatedResponse.Code)
	require.Equal(t, "image/png", generatedResponse.Header().Get("Content-Type"))
	require.Equal(t, generatedImage, generatedResponse.Body.Bytes())

	referenceResponse := httptest.NewRecorder()
	referenceRequest := httptest.NewRequest(http.MethodGet, succeeded.References[0].ContentURL, nil)
	router.ServeHTTP(referenceResponse, referenceRequest)
	require.Equal(t, http.StatusOK, referenceResponse.Code)
	require.Equal(t, "image/png", referenceResponse.Header().Get("Content-Type"))
	require.Equal(t, byte(0), referenceResponse.Body.Bytes()[8])

	missingSessionResponse := httptest.NewRecorder()
	router.ServeHTTP(missingSessionResponse, httptest.NewRequest(
		http.MethodGet, "/api/mujian/projects/"+projectID+"/image-generations/"+succeeded.ID, nil,
	))
	require.Equal(t, http.StatusBadRequest, missingSessionResponse.Code)

	wrongSessionResponse := httptest.NewRecorder()
	router.ServeHTTP(wrongSessionResponse, httptest.NewRequest(
		http.MethodGet, "/api/mujian/projects/"+projectID+"/image-generations/"+succeeded.ID+"?session_id=wrong-session", nil,
	))
	require.Equal(t, http.StatusNotFound, wrongSessionResponse.Code)
	wrongContentSessionResponse := httptest.NewRecorder()
	router.ServeHTTP(wrongContentSessionResponse, httptest.NewRequest(
		http.MethodGet, "/api/mujian/projects/"+projectID+"/image-generations/"+succeeded.ID+"/content?session_id=wrong-session", nil,
	))
	require.Equal(t, http.StatusNotFound, wrongContentSessionResponse.Code)

	legacyApplyResponse := httptest.NewRecorder()
	router.ServeHTTP(legacyApplyResponse, httptest.NewRequest(
		http.MethodPost, "/api/mujian/projects/"+projectID+"/image-generations/"+succeeded.ID+"/apply?session_id="+sessionID, nil,
	))
	require.Equal(t, http.StatusNotFound, legacyApplyResponse.Code)
}
