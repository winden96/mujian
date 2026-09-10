package service

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPClientBlocksAuthenticatedAnthropicRedirect(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", target.URL+"/captured")
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)

	InitHttpClient()
	request, err := http.NewRequest(http.MethodPost, source.URL+"/v1/messages", nil)
	require.NoError(t, err)
	request = request.WithContext(WithStrictManagedRedirectPolicy(request.Context()))
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("x-api-key", "must-not-leak")

	response, err := GetHttpClient().Do(request)

	require.ErrorContains(t, err, "redirects are disabled")
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	require.Zero(t, redirectedRequests.Load())
}

func TestCheckRedirectAllowsOrdinaryAuthenticatedSameOriginRequest(t *testing.T) {
	// Only the policy is called: no request is sent to this public literal address.
	original, err := http.NewRequest(http.MethodPost, "https://8.8.8.8/v1/messages", nil)
	require.NoError(t, err)
	original.Header.Set("anthropic-version", "2023-06-01")
	original.Header.Set("x-api-key", "must-not-leak")
	redirected, err := http.NewRequest(http.MethodPost, "https://8.8.8.8/v1/messages-v2", nil)
	require.NoError(t, err)

	require.NoError(t, checkRedirect(redirected, []*http.Request{original}))
}

func TestCheckRedirectBlocksStrictManagedSameOriginRequest(t *testing.T) {
	original, err := http.NewRequest(http.MethodPost, "https://provider.example/v1/messages", nil)
	require.NoError(t, err)
	original = original.WithContext(WithStrictManagedRedirectPolicy(original.Context()))
	redirected, err := http.NewRequest(http.MethodPost, "https://provider.example/v1/messages-v2", nil)
	require.NoError(t, err)

	err = checkRedirect(redirected, []*http.Request{original})
	require.ErrorContains(t, err, "redirects are disabled")
}

func TestCheckRedirectBlocksCrossOriginCustomCredential(t *testing.T) {
	original, err := http.NewRequest(http.MethodGet, "https://provider.example/v1/models", nil)
	require.NoError(t, err)
	original.Header.Set("x-api-key", "must-not-leak")
	redirected, err := http.NewRequest(http.MethodGet, "https://attacker.example/capture", nil)
	require.NoError(t, err)

	err = checkRedirect(redirected, []*http.Request{original})

	require.ErrorContains(t, err, "cross-origin redirect blocked")
}
