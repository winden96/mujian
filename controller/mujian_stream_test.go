package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mujianservice "github.com/QuantumNous/new-api/service/mujian"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteMujianSSEPreservesEventOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/mujian/projects/project-1/agent/messages/stream", nil)
	context.Header("Content-Type", "text/event-stream; charset=utf-8")

	require.NoError(t, writeMujianSSE(context, mujianservice.AgentStreamEvent{
		Type: "started", Data: gin.H{"mode": "consult", "skill": "短剧编剧"},
	}))
	require.NoError(t, writeMujianSSE(context, mujianservice.AgentStreamEvent{
		Type: "delta", Data: gin.H{"text": "你好"},
	}))

	body := recorder.Body.String()
	require.Equal(t, "text/event-stream; charset=utf-8", recorder.Header().Get("Content-Type"))
	require.Less(t, strings.Index(body, "event: started"), strings.Index(body, "event: delta"))
	require.Contains(t, body, `data: {"mode":"consult","skill":"短剧编剧"}`)
	require.Contains(t, body, `data: {"text":"你好"}`)
}
