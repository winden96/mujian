package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestJimengRequestConvertRoutesFetchAction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/jimeng/", JimengRequestConvert(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"method":     c.Request.Method,
			"path":       c.Request.URL.Path,
			"relay_mode": c.GetInt("relay_mode"),
			"task_id":    c.GetString("task_id"),
		})
	})

	request := httptest.NewRequest(
		http.MethodPost,
		"/jimeng/?Action=CVSync2AsyncGetResult&Version=2022-08-31",
		strings.NewReader(`{"task_id":"task-123"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Method    string `json:"method"`
		Path      string `json:"path"`
		RelayMode int    `json:"relay_mode"`
		TaskID    string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, http.MethodGet, payload.Method)
	require.Equal(t, "/v1/video/generations/task-123", payload.Path)
	require.Equal(t, relayconstant.RelayModeVideoFetchByID, payload.RelayMode)
	require.Equal(t, "task-123", payload.TaskID)
}

func TestJimengRequestConvertRoutesSubmitAction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/jimeng/", JimengRequestConvert(), func(c *gin.Context) {
		var body map[string]interface{}
		require.NoError(t, c.ShouldBindJSON(&body))
		c.JSON(http.StatusOK, gin.H{
			"method": c.Request.Method,
			"path":   c.Request.URL.Path,
			"action": c.GetString("action"),
			"body":   body,
		})
	})

	request := httptest.NewRequest(
		http.MethodPost,
		"/jimeng/?Action="+jimengSubmitAction+"&Version=2022-08-31",
		strings.NewReader(`{"req_key":"jimeng_vgfm_t2v_l20","prompt":"test prompt"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Method string                 `json:"method"`
		Path   string                 `json:"path"`
		Action string                 `json:"action"`
		Body   map[string]interface{} `json:"body"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	require.Equal(t, http.MethodPost, payload.Method)
	require.Equal(t, "/v1/video/generations", payload.Path)
	require.Equal(t, constant.TaskActionTextGenerate, payload.Action)
	require.Equal(t, "jimeng_vgfm_t2v_l20", payload.Body["model"])
	require.Equal(t, "test prompt", payload.Body["prompt"])
	require.Equal(t, "jimeng_vgfm_t2v_l20", payload.Body["metadata"].(map[string]interface{})["req_key"])
}

func TestJimengRequestConvertRejectsUnknownAction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	called := false
	engine := gin.New()
	engine.POST("/jimeng/", JimengRequestConvert(), func(c *gin.Context) {
		called = true
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(
		http.MethodPost,
		"/jimeng/?Action=CVSync2AsyncSubmitTas&Version=2022-08-31",
		strings.NewReader(`{"req_key":"jimeng_vgfm_t2v_l20","prompt":"must not submit"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.False(t, called)
	require.Contains(t, response.Body.String(), "Unsupported Action query parameter")
}
