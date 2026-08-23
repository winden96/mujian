package mujianobject

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/huaweicloud/huaweicloud-sdk-go-obs/obs"
	"github.com/stretchr/testify/require"
)

type obsRoundTripFunc func(*http.Request) (*http.Response, error)

func (function obsRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestOBSPutHeadersEnforceImmutableObjectMetadata(t *testing.T) {
	headers := obsPutHeaders(PutInput{
		ContentType: "image/png", SizeBytes: 123, SHA256: "checksum",
	})
	require.Equal(t, "true", headers["x-obs-forbid-overwrite"])
	require.Equal(t, "inline", headers["Content-Disposition"])
	require.Equal(t, obsCacheControl, headers["Cache-Control"])
	require.Equal(t, "image/png", headers["Content-Type"])
	require.Equal(t, "123", headers["Content-Length"])
	require.Equal(t, "checksum", headers["x-obs-content-sha256"])
	require.Equal(t, "checksum", headers["x-obs-meta-sha256"])
}

func TestOBSAlreadyExistsClassification(t *testing.T) {
	conflict := obs.ObsError{BaseModel: obs.BaseModel{StatusCode: http.StatusConflict}}
	require.True(t, isObjectAlreadyExists(fmt.Errorf("wrapped: %w", conflict)))
	require.True(t, isObjectAlreadyExists(obs.ObsError{Code: "ObjectAlreadyExists"}))
	require.False(t, isObjectAlreadyExists(obs.ObsError{BaseModel: obs.BaseModel{StatusCode: http.StatusForbidden}}))
}

func TestSignedRequestErrorsNeverExposePresignedURL(t *testing.T) {
	raw := &url.Error{
		Op:  "Put",
		URL: "https://bucket.example/object?AccessKeyId=temporary&Signature=secret",
		Err: errors.New("dial failed"),
	}
	safe := safeSignedRequestError("OBS PutObject", raw)
	require.ErrorContains(t, safe, "transport failed during Put")
	require.NotContains(t, safe.Error(), "AccessKeyId")
	require.NotContains(t, safe.Error(), "Signature")
	require.NotContains(t, safe.Error(), "temporary")
	require.NotContains(t, safe.Error(), "secret")
}

func TestSignedRequestCancellationReachesHTTPTransport(t *testing.T) {
	requestStarted := make(chan struct{})
	backend := &obsBackend{httpClient: &http.Client{Transport: obsRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := backend.doSignedRequest(ctx, "OBS GetObject", http.MethodGet, &obs.CreateSignedUrlOutput{
			SignedUrl: "https://bucket.example/object?AccessKeyId=temporary&Signature=secret",
		}, nil, 0)
		result <- err
	}()

	<-requestStarted
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
		require.NotContains(t, err.Error(), "AccessKeyId")
		require.NotContains(t, err.Error(), "Signature")
	case <-time.After(time.Second):
		t.Fatal("signed OBS request did not stop after context cancellation")
	}
}

func TestSignedRequestAppliesSignedHeadersAndContentLength(t *testing.T) {
	backend := &obsBackend{httpClient: &http.Client{Transport: obsRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		require.Equal(t, "signed.example", request.Host)
		require.Equal(t, "value", request.Header.Get("X-Obs-Test"))
		require.Equal(t, int64(7), request.ContentLength)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Etag": []string{"\"etag\""}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}}
	response, err := backend.doSignedRequest(context.Background(), "OBS PutObject", http.MethodPut, &obs.CreateSignedUrlOutput{
		SignedUrl: "https://signed.example/object",
		ActualSignedRequestHeaders: http.Header{
			"Host":           []string{"signed.example"},
			"Content-Length": []string{"7"},
			"X-Obs-Test":     []string{"value"},
		},
	}, strings.NewReader("payload"), 7)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}

func TestSignedRequestPreservesSafeProviderClassifications(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		expected   error
	}{
		{
			name:       "missing object",
			statusCode: http.StatusNotFound,
			body:       `<Error><Code>NoSuchKey</Code><RequestId>request-id</RequestId></Error>`,
			expected:   ErrObjectNotFound,
		},
		{
			name:       "immutable conflict",
			statusCode: http.StatusConflict,
			body:       `<Error><Code>ObjectAlreadyExists</Code><RequestId>request-id</RequestId></Error>`,
			expected:   ErrObjectAlreadyExists,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &obsBackend{httpClient: &http.Client{Transport: obsRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: test.statusCode,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(test.body)),
				}, nil
			})}}
			_, err := backend.doSignedRequest(context.Background(), "OBS request", http.MethodPut, &obs.CreateSignedUrlOutput{
				SignedUrl: "https://bucket.example/object?AccessKeyId=temporary&Signature=secret",
			}, nil, 0)
			require.ErrorIs(t, err, test.expected)
			require.NotContains(t, err.Error(), "AccessKeyId")
			require.NotContains(t, err.Error(), "Signature")
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestOBSDeleteTreatsMissingObjectAsSuccess(t *testing.T) {
	client, err := obs.New("a", "b", "https://obs.cn-north-4.myhuaweicloud.com",
		obs.WithRegion("cn-north-4"), obs.WithSignature(obs.SignatureObs),
	)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	backend := &obsBackend{
		client: client,
		httpClient: &http.Client{Transport: obsRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`<Error><Code>NoSuchKey</Code></Error>`)),
			}, nil
		})},
		bucket:        "mujianai",
		prefix:        "mujian/prod/public",
		publicBaseURL: "https://static.mujianai.com",
	}

	err = backend.Delete(context.Background(), "mujian/prod/public/references/00000000-0000-0000-0000-000000000001.png")
	require.NoError(t, err)
}
