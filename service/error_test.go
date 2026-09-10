package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func captureErrorLog(t *testing.T, run func()) string {
	t.Helper()
	var output bytes.Buffer
	common.LogWriterMu.Lock()
	previous := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &output
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = previous
		common.LogWriterMu.Unlock()
	})
	run()
	return output.String()
}

func TestRelayErrorHandlerBoundsAndRedactsRawBody(t *testing.T) {
	secret := "upstream-secret-key"
	response := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader(secret)),
	}

	relayErr := RelayErrorHandler(context.Background(), response, true)

	require.NotNil(t, relayErr)
	require.NotContains(t, relayErr.Error(), secret)
	require.Equal(t, "bad response status code 502", relayErr.Error())

	response = &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxRelayErrorResponseBytes+1))),
	}
	relayErr = RelayErrorHandler(context.Background(), response, false)
	require.ErrorContains(t, relayErr, "response body exceeds")
}

func TestRelayErrorHandlerNeverLogsRawContentType(t *testing.T) {
	const secret = "provider-secret-key"
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ginContext, constant.ContextKeyChannelKey, secret)

	for _, testCase := range []struct {
		name string
		body string
	}{
		{name: "non json", body: "not-json"},
		{name: "oversized", body: strings.Repeat("x", maxRelayErrorResponseBytes+1)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: http.StatusBadGateway,
				Header:     http.Header{"Content-Type": []string{"text/plain; boundary=" + secret}},
				Body:       io.NopCloser(strings.NewReader(testCase.body)),
			}
			logged := captureErrorLog(t, func() {
				require.NotNil(t, RelayErrorHandler(ginContext, response, false))
			})

			require.NotContains(t, logged, secret)
			require.Contains(t, logged, "content_type_class=text")
		})
	}
}

func TestRelayErrorHandlerRedactsChannelKeyFromStructuredError(t *testing.T) {
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ginContext, constant.ContextKeyChannelKey, "provider-secret-key")
	response := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"invalid provider-secret-key","type":"authentication_error"}}`,
		)),
	}

	relayErr := RelayErrorHandler(ginContext, response, true)

	require.NotNil(t, relayErr)
	require.NotContains(t, relayErr.Error(), "provider-secret-key")
	require.Contains(t, relayErr.Error(), "invalid ***")
}

func TestRelayErrorHandlerSanitizesEveryStructuredErrorField(t *testing.T) {
	const secret = "provider-secret-key"
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ginContext, constant.ContextKeyChannelKey, secret)
	response := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"invalid provider-secret-key","type":"authentication_provider-secret-key","param":"x-api-key=provider-secret-key","code":"bad_provider-secret-key","metadata":{"debug":"provider-secret-key"}}}`,
		)),
	}

	relayErr := RelayErrorHandler(ginContext, response, false)

	require.NotNil(t, relayErr)
	structured, ok := relayErr.RelayError.(types.OpenAIError)
	require.True(t, ok)
	require.Contains(t, structured.Message, "invalid ***")
	require.Contains(t, structured.Type, "authentication_***")
	require.Contains(t, structured.Param, "x-api-key=***")
	require.Equal(t, "bad_***", structured.Code)
	require.Empty(t, structured.Metadata)
	require.NotContains(t, relayErr.Error(), secret)
	require.NotContains(t, string(relayErr.GetErrorCode()), secret)
}

func TestSanitizeOpenAIErrorDropsStructuredCode(t *testing.T) {
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ginContext, constant.ContextKeyChannelKey, "provider-secret-key")

	sanitized := SanitizeOpenAIError(ginContext, types.OpenAIError{
		Message: "useful explanation",
		Type:    "upstream_error",
		Code:    map[string]any{"detail": "provider-secret-key"},
	})

	require.Equal(t, "useful explanation", sanitized.Message)
	require.Equal(t, "upstream_error", sanitized.Code)
}

func TestResetStatusCode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		statusCode       int
		statusCodeConfig string
		expectedCode     int
	}{
		{
			name:             "map string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"503"}`,
			expectedCode:     503,
		},
		{
			name:             "map int value",
			statusCode:       429,
			statusCodeConfig: `{"429":503}`,
			expectedCode:     503,
		},
		{
			name:             "skip invalid string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"bad-code"}`,
			expectedCode:     429,
		},
		{
			name:             "skip status code 200",
			statusCode:       200,
			statusCodeConfig: `{"200":503}`,
			expectedCode:     200,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			newAPIError := &types.NewAPIError{
				StatusCode: tc.statusCode,
			}
			ResetStatusCode(newAPIError, tc.statusCodeConfig)
			require.Equal(t, tc.expectedCode, newAPIError.StatusCode)
		})
	}
}
