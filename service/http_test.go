package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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

func TestIOCopyBytesGracefullyFiltersCredentialResponseHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	common.SetContextKey(context, constant.ContextKeyChannelKey, "upstream-secret")
	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":        {"application/json"},
			"X-Safe-Multi":        {"one", "two"},
			"X-Debug":             {"request used upstream-secret successfully"},
			"Warning":             {"safe", "Bearer upstream-secret"},
			"aUtHoRiZaTiOn":       {"Bearer upstream-secret"},
			"Proxy-Authorization": {"proxy-secret"},
			"X-Api-Key":           {"upstream-secret"},
			"Api-Key":             {"upstream-secret"},
			"X-Goog-Api-Key":      {"upstream-secret"},
		},
	}

	IOCopyBytesGracefully(context, upstream, []byte(`{"ok":true}`))

	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	require.Equal(t, []string{"one", "two"}, recorder.Header().Values("X-Safe-Multi"))
	require.Empty(t, recorder.Header().Values("X-Debug"))
	require.Empty(t, recorder.Header().Values("Warning"))
	assertNoCredentialResponseHeaders(t, recorder.Header())
}

func TestStagedResponseWritesOnlyWhenFlushed(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	upstream := &http.Response{
		StatusCode: http.StatusCreated,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	StageResponseBytes(context, upstream, []byte(`{"ok":true}`))

	require.False(t, context.Writer.Written())
	require.Empty(t, recorder.Body.String())
	require.True(t, FlushStagedResponseBytes(context))
	require.Equal(t, http.StatusCreated, recorder.Code)
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	require.Equal(t, `{"ok":true}`, recorder.Body.String())
	require.False(t, FlushStagedResponseBytes(context), "a staged response must be committed at most once")
}

func TestStagedResponseFiltersCredentialHeadersBeforeFlush(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	common.SetContextKey(context, constant.ContextKeyChannelKey, "upstream-secret")
	upstream := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":  {"application/json"},
			"Location":      {"https://example.test/debug/upstream-secret"},
			"Authorization": {"Bearer upstream-secret"},
			"x-API-key":     {"upstream-secret", "second-secret"},
		},
	}

	StageResponseBytes(context, upstream, []byte(`{"ok":true}`))
	require.True(t, FlushStagedResponseBytes(context))

	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
	require.Empty(t, recorder.Header().Values("Location"))
	assertNoCredentialResponseHeaders(t, recorder.Header())
}

func TestDiscardStagedResponseLeavesWriterUntouched(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	StageResponseBytes(context, nil, []byte(`{"ok":true}`))
	DiscardStagedResponseBytes(context)

	require.False(t, context.Writer.Written())
	require.Empty(t, recorder.Body.String())
	require.False(t, FlushStagedResponseBytes(context))
}

func assertNoCredentialResponseHeaders(t *testing.T, header http.Header) {
	t.Helper()
	for name := range header {
		require.False(t, isBlockedUpstreamResponseHeader(name), name)
	}
}
