package claude

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSetupRequestHeaderUsesXAPIKeyWithoutAuthorizationOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	context.Request.Header.Set("Content-Type", "application/json")

	header := http.Header{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "zenmux-test-key"},
	}

	err := (&Adaptor{}).SetupRequestHeader(context, &header, info)

	require.NoError(t, err)
	require.Equal(t, []string{"zenmux-test-key"}, header.Values("x-api-key"))
	require.Empty(t, header.Values("Authorization"))
	require.Equal(t, "application/json", header.Get("Content-Type"))
	require.Equal(t, "2023-06-01", header.Get("anthropic-version"))
}

func TestDoRequestAuthorizationOverrideExcludesXAPIKey(t *testing.T) {
	service.InitHttpClient()

	for _, test := range []struct {
		name string
		info *relaycommon.RelayInfo
	}{
		{
			name: "static mixed-case override",
			info: &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ApiKey: "tabcode-test-key",
					HeadersOverride: map[string]interface{}{
						"aUtHoRiZaTiOn":     "Bearer {api_key}",
						"anthropic-version": "2023-06-01",
					},
				},
			},
		},
		{
			name: "authorization suppresses explicit x-api-key override",
			info: &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ApiKey: "tabcode-test-key",
					HeadersOverride: map[string]interface{}{
						"Authorization":     "Bearer {api_key}",
						"x-api-key":         "must-not-leak",
						"anthropic-version": "2023-06-01",
					},
				},
			},
		},
		{
			name: "runtime override",
			info: &relaycommon.RelayInfo{
				ChannelMeta: &relaycommon.ChannelMeta{
					ApiKey: "tabcode-test-key",
					HeadersOverride: map[string]interface{}{
						"x-api-key": "unused-static-value",
					},
				},
				UseRuntimeHeadersOverride: true,
				RuntimeHeadersOverride: map[string]interface{}{
					"AUTHORIZATION":     "Bearer {api_key}",
					"anthropic-version": "2023-06-01",
				},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			capturedHeaders := make(chan http.Header, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				capturedHeaders <- request.Header.Clone()
				writer.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			test.info.ChannelBaseUrl = server.URL
			gin.SetMode(gin.TestMode)
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
			context.Request.Header.Set("Content-Type", "application/json")
			context.Request.Header.Set("anthropic-version", "2099-01-01")

			response, err := (&Adaptor{}).DoRequest(context, test.info, strings.NewReader(`{}`))

			require.NoError(t, err)
			httpResponse, ok := response.(*http.Response)
			require.True(t, ok)
			_, _ = io.Copy(io.Discard, httpResponse.Body)
			require.NoError(t, httpResponse.Body.Close())

			headers := <-capturedHeaders
			require.Equal(t, []string{"Bearer tabcode-test-key"}, headers.Values("Authorization"))
			require.Empty(t, headers.Values("x-api-key"))
			require.Equal(t, "application/json", headers.Get("Content-Type"))
			require.Equal(t, "2023-06-01", headers.Get("anthropic-version"))
		})
	}
}

func TestManagedProviderHeadersIgnoreGlobalAndClientClaudeOverrides(t *testing.T) {
	service.InitHttpClient()
	settings := model_setting.GetClaudeSettings()
	previousHeaders := settings.HeadersSettings
	settings.HeadersSettings = map[string]map[string][]string{
		"claude-sonnet-4-6": {
			"Authorization":     {"Bearer global-secret"},
			"x-api-key":         {"global-secret"},
			"anthropic-beta":    {"global-beta"},
			"anthropic-version": {"2099-01-01"},
		},
	}
	t.Cleanup(func() { settings.HeadersSettings = previousHeaders })

	for _, test := range []struct {
		name              string
		providerID        string
		headerOverride    map[string]interface{}
		wantXAPIKey       string
		wantAuthorization string
	}{
		{
			name:       "ZenMux",
			providerID: types.PriceProviderZenMux,
			headerOverride: map[string]interface{}{
				"anthropic-version": "2023-06-01",
			},
			wantXAPIKey: "managed-key",
		},
		{
			name:       "TabCode",
			providerID: types.PriceProviderTabCode,
			headerOverride: map[string]interface{}{
				"Authorization":     "Bearer {api_key}",
				"anthropic-version": "2023-06-01",
			},
			wantAuthorization: "Bearer managed-key",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			capturedHeaders := make(chan http.Header, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				capturedHeaders <- request.Header.Clone()
				writer.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			info := &relaycommon.RelayInfo{
				OriginModelName: "claude-sonnet-4-6",
				PriceData: types.PriceData{
					ChannelSpecific: true,
					PriceProvider:   test.providerID,
				},
				ChannelMeta: &relaycommon.ChannelMeta{
					ChannelBaseUrl:    server.URL,
					ApiKey:            "managed-key",
					ManagedProvider:   true,
					ManagedProviderID: test.providerID,
					HeadersOverride:   test.headerOverride,
				},
			}
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", strings.NewReader(`{}`))
			context.Request.Header.Set("Content-Type", "application/json")
			context.Request.Header.Set("Authorization", "Bearer client-secret")
			context.Request.Header.Set("x-api-key", "client-secret")
			context.Request.Header.Set("anthropic-beta", "client-beta")
			context.Request.Header.Set("anthropic-version", "2099-01-01")

			response, err := (&Adaptor{}).DoRequest(context, info, strings.NewReader(`{}`))
			require.NoError(t, err)
			httpResponse := response.(*http.Response)
			_, _ = io.Copy(io.Discard, httpResponse.Body)
			require.NoError(t, httpResponse.Body.Close())

			headers := <-capturedHeaders
			require.Equal(t, test.wantXAPIKey, headers.Get("x-api-key"))
			require.Equal(t, test.wantAuthorization, headers.Get("Authorization"))
			require.Empty(t, headers.Get("anthropic-beta"))
			require.Equal(t, "2023-06-01", headers.Get("anthropic-version"))
		})
	}
}

func TestSetupRequestHeaderIgnoresEmptyEffectiveAuthorizationOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	header := http.Header{}
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ApiKey: "anthropic-test-key",
			HeadersOverride: map[string]interface{}{
				"Authorization": "Bearer stale-static-key",
			},
		},
		UseRuntimeHeadersOverride: true,
		RuntimeHeadersOverride: map[string]interface{}{
			"Authorization": "   ",
		},
	}

	err := (&Adaptor{}).SetupRequestHeader(context, &header, info)

	require.NoError(t, err)
	require.Equal(t, []string{"anthropic-test-key"}, header.Values("x-api-key"))
}

func TestDoRequestKeepsXAPIKeyWhenDynamicAuthorizationIsMissing(t *testing.T) {
	service.InitHttpClient()
	capturedHeaders := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capturedHeaders <- request.Header.Clone()
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl: server.URL,
			ApiKey:         "anthropic-test-key",
			HeadersOverride: map[string]interface{}{
				"Authorization": "{client_header:Authorization}",
			},
		},
	}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	context.Request.Header.Set("Content-Type", "application/json")

	response, err := (&Adaptor{}).DoRequest(context, info, strings.NewReader(`{}`))
	require.NoError(t, err)
	httpResponse := response.(*http.Response)
	_, _ = io.Copy(io.Discard, httpResponse.Body)
	require.NoError(t, httpResponse.Body.Close())

	headers := <-capturedHeaders
	require.Empty(t, headers.Values("Authorization"))
	require.Equal(t, []string{"anthropic-test-key"}, headers.Values("x-api-key"))
}

func TestDoRequestTimesOutBeforeFirstResponseBodyByte(t *testing.T) {
	service.InitHttpClient()
	previousTimeout := claudeFirstResponseTimeout
	claudeFirstResponseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { claudeFirstResponseTimeout = previousTimeout })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)

	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	context.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "test-key", ManagedProvider: true, ManagedProviderID: types.PriceProviderZenMux},
		IsStream:    true,
	}

	response, err := (&Adaptor{}).DoRequest(context, info, strings.NewReader(`{}`))
	require.NoError(t, err)
	httpResponse := response.(*http.Response)
	started := time.Now()
	_, readErr := io.ReadAll(httpResponse.Body)
	_ = httpResponse.Body.Close()

	require.Error(t, readErr)
	require.Less(t, time.Since(started), time.Second)
}

func TestFirstResponseBodyTimeoutReturnsRetryableStreamErrorBeforeWrite(t *testing.T) {
	service.InitHttpClient()
	previousTimeout := claudeFirstResponseTimeout
	claudeFirstResponseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { claudeFirstResponseTimeout = previousTimeout })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	context.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "test-key", ManagedProvider: true, ManagedProviderID: types.PriceProviderZenMux},
		IsStream:    true,
	}
	adaptor := &Adaptor{}

	response, err := adaptor.DoRequest(context, info, strings.NewReader(`{}`))
	require.NoError(t, err)
	usage, relayErr := adaptor.DoResponse(context, response.(*http.Response), info)

	require.Nil(t, usage)
	require.NotNil(t, relayErr)
	require.Equal(t, types.ErrorCodeBadResponse, relayErr.GetErrorCode())
	require.Equal(t, http.StatusBadGateway, relayErr.StatusCode)
	require.False(t, context.Writer.Written())
}

func TestFirstResponseTimeoutIgnoresNonDataSSELines(t *testing.T) {
	service.InitHttpClient()
	previousTimeout := claudeFirstResponseTimeout
	claudeFirstResponseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { claudeFirstResponseTimeout = previousTimeout })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = io.WriteString(writer, ": upstream keepalive\n\n")
				_, _ = io.WriteString(writer, " data: {\"type\":\"message_start\"}\n\n")
				_, _ = io.WriteString(writer, "data: {\"type\":\"ping\"}\n\n")
				writer.(http.Flusher).Flush()
			case <-request.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	context.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		ChannelMeta:            &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "test-key", ManagedProvider: true, ManagedProviderID: types.PriceProviderZenMux},
		IsStream:               true,
		AuthorizedPromptTokens: 100, AuthorizedCompletionTokens: 100,
	}
	adaptor := &Adaptor{}

	response, err := adaptor.DoRequest(context, info, strings.NewReader(`{}`))
	require.NoError(t, err)
	usage, relayErr := adaptor.DoResponse(context, response.(*http.Response), info)

	require.Nil(t, usage)
	require.NotNil(t, relayErr)
	require.Equal(t, types.ErrorCodeBadResponse, relayErr.GetErrorCode(), relayErr.Error())
	require.False(t, context.Writer.Written())
}

func TestOrdinaryClaudeStreamKeepsExistingResponseTimeoutBehavior(t *testing.T) {
	service.InitHttpClient()
	previousTimeout := claudeFirstResponseTimeout
	claudeFirstResponseTimeout = 30 * time.Millisecond
	t.Cleanup(func() { claudeFirstResponseTimeout = previousTimeout })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		time.Sleep(2 * claudeFirstResponseTimeout)
		_, _ = io.WriteString(writer, "data: {}\n\n")
	}))
	t.Cleanup(server.Close)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	context.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "test-key"},
		IsStream:    true,
	}

	response, err := (&Adaptor{}).DoRequest(context, info, strings.NewReader(`{}`))
	require.NoError(t, err)
	body, readErr := io.ReadAll(response.(*http.Response).Body)
	require.NoError(t, response.(*http.Response).Body.Close())
	require.NoError(t, readErr)
	require.Equal(t, "data: {}\n\n", string(body))
}

func TestDoRequestDoesNotApplyFirstResponseTimeoutToNonStreamingBody(t *testing.T) {
	service.InitHttpClient()
	previousTimeout := claudeFirstResponseTimeout
	claudeFirstResponseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { claudeFirstResponseTimeout = previousTimeout })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		time.Sleep(2 * claudeFirstResponseTimeout)
		_, _ = io.WriteString(writer, `{"type":"message","content":[]}`)
	}))
	t.Cleanup(server.Close)

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	context.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: server.URL, ApiKey: "test-key"},
		IsStream:    false,
	}

	response, err := (&Adaptor{}).DoRequest(context, info, strings.NewReader(`{}`))
	require.NoError(t, err)
	httpResponse := response.(*http.Response)
	body, readErr := io.ReadAll(httpResponse.Body)
	require.NoError(t, httpResponse.Body.Close())

	require.NoError(t, readErr)
	require.JSONEq(t, `{"type":"message","content":[]}`, string(body))
}
