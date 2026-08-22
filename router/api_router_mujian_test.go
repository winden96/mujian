package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMujianRouterDoesNotMountLegacyStructuredWriteRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)
	routes := make(map[string]struct{})
	for _, route := range engine.Routes() {
		routes[route.Method+" "+route.Path] = struct{}{}
	}

	for _, active := range []string{
		"GET /api/mujian/projects/:projectId/workspace",
		"POST /api/mujian/projects/:projectId/agent/messages/stream",
		"POST /api/mujian/projects/:projectId/image-generations",
		"GET /api/mujian/projects/:projectId/image-generations/:generationId",
	} {
		require.Contains(t, routes, active)
	}
	for _, removed := range []string{
		"PATCH /api/mujian/projects/:projectId/workspace",
		"POST /api/mujian/projects/:projectId/agent/messages/:messageId/apply",
		"POST /api/mujian/projects/:projectId/agent/messages/:messageId/undo",
		"POST /api/mujian/projects/:projectId/shots/:shotId/generate",
		"GET /api/mujian/projects/:projectId/image-tasks/:taskId",
		"POST /api/mujian/projects/:projectId/image-generations/:generationId/apply",
	} {
		require.NotContains(t, routes, removed)
	}

	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodPatch, "/api/mujian/projects/project/workspace"},
		{http.MethodPost, "/api/mujian/projects/project/agent/messages/message/apply"},
		{http.MethodPost, "/api/mujian/projects/project/agent/messages/message/undo"},
		{http.MethodPost, "/api/mujian/projects/project/shots/shot/generate"},
		{http.MethodGet, "/api/mujian/projects/project/image-tasks/task"},
		{http.MethodPost, "/api/mujian/projects/project/image-generations/generation/apply"},
	} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(request.method, request.path, nil))
		require.Equal(t, http.StatusNotFound, response.Code, request.method+" "+request.path)
	}
}
