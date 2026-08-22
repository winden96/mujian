package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMujianUserDeleteErrorReturnsConflictForActiveImageLease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)

	mujianUserDeleteError(context, model.ErrMujianImageGenerationActive)

	require.Equal(t, http.StatusConflict, response.Code)
	require.JSONEq(t, `{"success":false,"message":"生图任务正在进行中，请稍后再删除"}`, response.Body.String())
}
