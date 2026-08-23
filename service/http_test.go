package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIOCopyBytesGracefullyPreservesGatewayRequestID(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Header(common.RequestIdKey, "gateway-request")

	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":      []string{"application/json"},
			common.RequestIdKey: []string{"upstream-request"},
		},
	}
	IOCopyBytesGracefully(context, upstream, []byte(`{"ok":true}`))

	require.Equal(t, "gateway-request", recorder.Header().Get(common.RequestIdKey))
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
}
