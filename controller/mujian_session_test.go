package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	mujianservice "github.com/QuantumNous/new-api/service/mujian"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func mujianSessionTestRouter(userID int) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", userID)
		c.Next()
	})
	router.GET("/api/mujian/projects/:projectId/workspace", GetMujianWorkspace)
	router.POST("/api/mujian/projects/:projectId/agent/sessions", CreateMujianAgentSession)
	router.DELETE("/api/mujian/projects/:projectId/agent/sessions/:sessionId/messages", ClearMujianAgentSession)
	return router
}

func decodeMujianWorkspaceResponse(t *testing.T, recorder *httptest.ResponseRecorder) mujianservice.Workspace {
	t.Helper()
	var response struct {
		Success bool                    `json:"success"`
		Data    mujianservice.Workspace `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	return response.Data
}

func TestMujianAgentSessionAPIWorkspaceContract(t *testing.T) {
	user, projectID, _ := setupMujianImageControllerTest(t)
	router := mujianSessionTestRouter(user.Id)

	initialResponse := httptest.NewRecorder()
	router.ServeHTTP(initialResponse, httptest.NewRequest(http.MethodGet, "/api/mujian/projects/"+projectID+"/workspace", nil))
	require.Equal(t, http.StatusOK, initialResponse.Code, initialResponse.Body.String())
	initial := decodeMujianWorkspaceResponse(t, initialResponse)
	require.Len(t, initial.AgentSessions, 1)
	require.NotEmpty(t, initial.ActiveSessionID)

	createResponse := httptest.NewRecorder()
	router.ServeHTTP(createResponse, httptest.NewRequest(http.MethodPost, "/api/mujian/projects/"+projectID+"/agent/sessions", nil))
	require.Equal(t, http.StatusCreated, createResponse.Code, createResponse.Body.String())
	created := decodeMujianWorkspaceResponse(t, createResponse)
	require.Len(t, created.AgentSessions, 2)
	require.NotEqual(t, initial.ActiveSessionID, created.ActiveSessionID)
	require.Empty(t, created.Messages)

	selectedResponse := httptest.NewRecorder()
	selectedURL := "/api/mujian/projects/" + projectID + "/workspace?session_id=" + initial.ActiveSessionID
	router.ServeHTTP(selectedResponse, httptest.NewRequest(http.MethodGet, selectedURL, nil))
	require.Equal(t, http.StatusOK, selectedResponse.Code, selectedResponse.Body.String())
	selected := decodeMujianWorkspaceResponse(t, selectedResponse)
	require.Equal(t, initial.ActiveSessionID, selected.ActiveSessionID)

	clearResponse := httptest.NewRecorder()
	clearURL := "/api/mujian/projects/" + projectID + "/agent/sessions/" + created.ActiveSessionID + "/messages"
	router.ServeHTTP(clearResponse, httptest.NewRequest(http.MethodDelete, clearURL, nil))
	require.Equal(t, http.StatusOK, clearResponse.Code, clearResponse.Body.String())
	cleared := decodeMujianWorkspaceResponse(t, clearResponse)
	require.Equal(t, created.ActiveSessionID, cleared.ActiveSessionID)
	require.Empty(t, cleared.Messages)

	missingResponse := httptest.NewRecorder()
	missingURL := "/api/mujian/projects/" + projectID + "/workspace?session_id=missing-session"
	router.ServeHTTP(missingResponse, httptest.NewRequest(http.MethodGet, missingURL, nil))
	require.Equal(t, http.StatusNotFound, missingResponse.Code, missingResponse.Body.String())
}
