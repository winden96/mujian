package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	openapispec "github.com/QuantumNous/new-api/docs/openapi"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type relayContractRoute struct {
	method string
	path   string
}

func TestRepresentativeDocumentedRelayRoutesRequireBearerAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetRelayRouter(engine)
	SetVideoRouter(engine)

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/v1/models"},
		{method: http.MethodPost, path: "/v1/chat/completions", body: `{}`},
		{method: http.MethodPost, path: "/v1/messages", body: `{}`},
		{method: http.MethodPost, path: "/v1/images/generations", body: `{}`},
		{method: http.MethodPost, path: "/v1beta/models/gemini-2.0-flash:generateContent", body: `{}`},
		{method: http.MethodPost, path: "/v1/videos", body: `{}`},
		{method: http.MethodPost, path: "/kling/v1/videos/text2video", body: `{}`},
		{
			method: http.MethodPost,
			path:   "/jimeng/?Action=CVSync2AsyncSubmitTask&Version=2022-08-31",
			body:   `{"req_key":"jimeng_vgfm_t2v_l20","prompt":"test"}`,
		},
		{method: http.MethodPost, path: "/mj/submit/imagine", body: `{}`},
		{method: http.MethodPost, path: "/suno/submit/music", body: `{}`},
	}

	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			engine.ServeHTTP(response, request)

			require.Equal(t, http.StatusUnauthorized, response.Code)
		})
	}
}

func TestMidjourneyImageRoutesRequireAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("relay-contract-test"))))
	SetRelayRouter(engine)

	for _, path := range []string{
		"/mj/image/task-1",
		"/mj-fast/mj/image/task-1",
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))

			require.Equal(t, http.StatusUnauthorized, response.Code)
		})
	}
}

// These routes are intentionally not part of the public, executable contract.
// Keeping the reasons next to the contract test prevents accidental exposure.
var relayContractExceptions = map[relayContractRoute]string{
	{method: "POST", path: "/pg/chat/completions"}:          "internal administrator playground",
	{method: "POST", path: "/v1/engines/:model/embeddings"}: "known request-contract mismatch; documented in the page appendix",
	{method: "POST", path: "/v1/images/variations"}:         "RelayNotImplemented",
	{method: "GET", path: "/v1/files"}:                      "RelayNotImplemented",
	{method: "POST", path: "/v1/files"}:                     "RelayNotImplemented",
	{method: "DELETE", path: "/v1/files/:id"}:               "RelayNotImplemented",
	{method: "GET", path: "/v1/files/:id"}:                  "RelayNotImplemented",
	{method: "GET", path: "/v1/files/:id/content"}:          "RelayNotImplemented",
	{method: "POST", path: "/v1/fine-tunes"}:                "RelayNotImplemented",
	{method: "GET", path: "/v1/fine-tunes"}:                 "RelayNotImplemented",
	{method: "GET", path: "/v1/fine-tunes/:id"}:             "RelayNotImplemented",
	{method: "POST", path: "/v1/fine-tunes/:id/cancel"}:     "RelayNotImplemented",
	{method: "GET", path: "/v1/fine-tunes/:id/events"}:      "RelayNotImplemented",
	{method: "DELETE", path: "/v1/models/:model"}:           "RelayNotImplemented",
	{method: "POST", path: "/v1/models/*path"}:              "expanded into the four supported Gemini actions in OpenAPI",
	{method: "POST", path: "/v1beta/models/*path"}:          "expanded into the four supported Gemini actions in OpenAPI",
	{method: "POST", path: "/mj/submit/edits"}:              "active compatibility route with no verified public request contract",
	{method: "POST", path: "/:mode/mj/submit/edits"}:        "active compatibility route with no verified public request contract",
}

func TestRelayRoutesMatchPublicOpenAPIContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetRelayRouter(engine)
	SetVideoRouter(engine)

	actualRoutes := make(map[relayContractRoute]struct{})
	for _, route := range engine.Routes() {
		actualRoutes[relayContractRoute{method: route.Method, path: route.Path}] = struct{}{}
	}

	var document map[string]any
	require.NoError(t, common.Unmarshal(openapispec.RelaySpec(), &document))
	paths := document["paths"].(map[string]any)
	documentedRoutes := make(map[relayContractRoute]struct{})
	for path, rawPathItem := range paths {
		for method := range rawPathItem.(map[string]any) {
			upperMethod := method
			switch method {
			case "get":
				upperMethod = "GET"
			case "post":
				upperMethod = "POST"
			default:
				continue
			}
			documentedRoutes[relayContractRoute{method: upperMethod, path: openAPIPathToGin(path)}] = struct{}{}
		}
	}

	for route := range actualRoutes {
		if _, isException := relayContractExceptions[route]; isException {
			continue
		}
		require.Contains(t, documentedRoutes, route, "active relay route must be documented or explicitly excepted: %s %s", route.method, route.path)
	}

	for route := range documentedRoutes {
		if isExpandedGeminiRoute(route) {
			continue
		}
		require.Contains(t, actualRoutes, route, "documented route must be mounted by Gin: %s %s", route.method, route.path)
	}

	for route, reason := range relayContractExceptions {
		require.NotEmpty(t, reason)
		require.Contains(t, actualRoutes, route, "stale explicit route exception: %s %s", route.method, route.path)
		require.NotContains(t, documentedRoutes, route, "explicit route exception must stay out of the public contract: %s %s", route.method, route.path)
	}
}

func openAPIPathToGin(path string) string {
	replacements := map[string]string{
		"{model}":    ":model",
		"{task_id}":  ":task_id",
		"{video_id}": ":video_id",
		"{id}":       ":id",
		"{mode}":     ":mode",
		"{action}":   ":action",
	}
	for openAPIParameter, ginParameter := range replacements {
		path = strings.ReplaceAll(path, openAPIParameter, ginParameter)
	}
	return path
}

func isExpandedGeminiRoute(route relayContractRoute) bool {
	if route.method != "POST" {
		return false
	}
	for _, prefix := range []string{"/v1/models/:model:", "/v1beta/models/:model:"} {
		if len(route.path) > len(prefix) && route.path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
