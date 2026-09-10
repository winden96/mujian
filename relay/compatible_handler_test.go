package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCompatibleUpstreamErrorRedactsSelectedChannelKey(t *testing.T) {
	const channelKey = "compatible-provider-secret"
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	common.SetContextKey(context, constant.ContextKeyChannelKey, channelKey)
	response := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"type":"authentication_error","message":"invalid compatible-provider-secret"}}`,
		)),
	}

	relayErr := compatibleUpstreamError(context, response, "")

	require.NotNil(t, relayErr)
	require.Equal(t, http.StatusUnauthorized, relayErr.StatusCode)
	require.Contains(t, relayErr.Error(), "invalid ***")
	require.NotContains(t, relayErr.Error(), channelKey)
	require.NotContains(t, relayErr.ToOpenAIError().Message, channelKey)
}
