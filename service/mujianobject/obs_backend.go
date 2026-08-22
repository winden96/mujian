package mujianobject

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/huaweicloud/huaweicloud-sdk-go-obs/obs"
)

const (
	obsCacheControl          = "public, max-age=31536000, immutable"
	obsSHA256Key             = "sha256"
	obsForbidOverwriteHeader = "x-obs-forbid-overwrite"
)

type obsBackend struct {
	client        *obs.ObsClient
	httpClient    *http.Client
	bucket        string
	prefix        string
	publicBaseURL string
}

func NewOBSBackend(config Config) (Backend, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	client, err := obs.New("", "", config.Endpoint,
		obs.WithRegion(config.Region), obs.WithSignature(obs.SignatureObs), obs.WithSslVerify(true),
		obs.WithConnectTimeout(10), obs.WithSocketTimeout(60), obs.WithMaxRetryCount(2),
		obs.WithSecurityProviders(obs.NewEcsSecurityProvider(1)),
	)
	if err != nil {
		return nil, fmt.Errorf("create OBS client: %w", err)
	}
	return &obsBackend{
		client: client, httpClient: newOBSHTTPClient(), bucket: config.Bucket, prefix: config.Prefix,
		publicBaseURL: config.PublicBaseURL,
	}, nil
}

func newOBSHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Presigned URLs contain temporary credentials. They must go directly to
	// OBS, never through a process-level HTTP proxy that could retain them.
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 60 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (backend *obsBackend) BuildKey(kind ObjectKind, id, contentType string) (string, error) {
	return BuildObjectKey(backend.prefix, kind, id, contentType)
}

func (backend *obsBackend) Put(ctx context.Context, input PutInput) (Object, error) {
	if err := backend.validateRequest(ctx, input.Key); err != nil {
		return Object{}, err
	}
	headers := obsPutHeaders(input)
	signed, err := backend.client.CreateSignedUrl(&obs.CreateSignedUrlInput{
		Method: obs.HttpMethodPut, Bucket: backend.bucket, Key: input.Key,
		Expires: 300, Headers: headers,
	})
	if err != nil {
		return Object{}, fmt.Errorf("OBS CreateSignedUrl for PutObject: %w", err)
	}
	response, err := backend.doSignedRequest(
		ctx, "OBS PutObject", http.MethodPut, signed, input.Body, input.SizeBytes,
	)
	if err != nil {
		if isObjectAlreadyExists(err) {
			return Object{}, ErrObjectAlreadyExists
		}
		return Object{}, err
	}
	defer response.Body.Close()
	publicURL, err := backend.PublicURL(input.Key)
	if err != nil {
		return Object{}, err
	}
	return Object{
		Key: input.Key, PublicURL: publicURL, ContentType: input.ContentType,
		SizeBytes: input.SizeBytes, SHA256: input.SHA256, ETag: normalizeETag(response.Header.Get("ETag")),
	}, nil
}

func obsPutHeaders(input PutInput) map[string]string {
	return map[string]string{
		"Cache-Control":              obsCacheControl,
		"Content-Disposition":        "inline",
		"Content-Length":             strconv.FormatInt(input.SizeBytes, 10),
		"Content-Type":               input.ContentType,
		"x-obs-content-sha256":       input.SHA256,
		"x-obs-meta-" + obsSHA256Key: input.SHA256,
		obsForbidOverwriteHeader:     "true",
	}
}

func isObjectAlreadyExists(err error) bool {
	var obsErr obs.ObsError
	if !errors.As(err, &obsErr) {
		return false
	}
	if obsErr.StatusCode == http.StatusConflict || obsErr.StatusCode == http.StatusPreconditionFailed {
		return true
	}
	switch strings.ToLower(obsErr.Code) {
	case "objectalreadyexists", "preconditionfailed":
		return true
	default:
		return false
	}
}

func isObjectNotFound(err error) bool {
	var obsErr obs.ObsError
	return errors.As(err, &obsErr) && obsErr.StatusCode == http.StatusNotFound
}

// A signed-request transport error may embed the complete presigned URL,
// including temporary credential material. Never propagate that URL into task
// errors, database retry rows, or application logs.
func safeSignedRequestError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	var obsErr obs.ObsError
	if errors.As(err, &obsErr) {
		return fmt.Errorf("%s failed: status=%d code=%q request_id=%q", operation, obsErr.StatusCode, obsErr.Code, obsErr.RequestId)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s transport failed during %s", operation, urlErr.Op)
	}
	return fmt.Errorf("%s failed", operation)
}

func (backend *obsBackend) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	if err := backend.validateRequest(ctx, key); err != nil {
		return nil, Object{}, err
	}
	signed, err := backend.client.CreateSignedUrl(&obs.CreateSignedUrlInput{
		Method: obs.HttpMethodGet, Bucket: backend.bucket, Key: key, Expires: 300,
	})
	if err != nil {
		return nil, Object{}, fmt.Errorf("OBS CreateSignedUrl for GetObject: %w", err)
	}
	response, err := backend.doSignedRequest(ctx, "OBS GetObject", http.MethodGet, signed, nil, 0)
	if err != nil {
		return nil, Object{}, err
	}
	object, err := backend.objectFromHeaders(key, response.Header, response.ContentLength)
	if err != nil {
		response.Body.Close()
		return nil, Object{}, err
	}
	return response.Body, object, nil
}

func (backend *obsBackend) Head(ctx context.Context, key string) (Object, error) {
	if err := backend.validateRequest(ctx, key); err != nil {
		return Object{}, err
	}
	signed, err := backend.client.CreateSignedUrl(&obs.CreateSignedUrlInput{
		Method: obs.HttpMethodHead, Bucket: backend.bucket, Key: key, Expires: 300,
	})
	if err != nil {
		return Object{}, fmt.Errorf("OBS CreateSignedUrl for GetObjectMetadata: %w", err)
	}
	response, err := backend.doSignedRequest(ctx, "OBS GetObjectMetadata", http.MethodHead, signed, nil, 0)
	if err != nil {
		return Object{}, err
	}
	defer response.Body.Close()
	return backend.objectFromHeaders(key, response.Header, response.ContentLength)
}

func (backend *obsBackend) Delete(ctx context.Context, key string) error {
	if err := backend.validateRequest(ctx, key); err != nil {
		return err
	}
	signed, err := backend.client.CreateSignedUrl(&obs.CreateSignedUrlInput{
		Method: obs.HttpMethodDelete, Bucket: backend.bucket, Key: key, Expires: 300,
	})
	if err != nil {
		return fmt.Errorf("OBS CreateSignedUrl for DeleteObject: %w", err)
	}
	response, err := backend.doSignedRequest(ctx, "OBS DeleteObject", http.MethodDelete, signed, nil, 0)
	if err != nil {
		// Delete is idempotent at the application boundary. An object may have
		// been removed by an earlier attempt whose response was lost.
		if errors.Is(err, ErrObjectNotFound) {
			return nil
		}
		return err
	}
	return response.Body.Close()
}

func (backend *obsBackend) doSignedRequest(
	ctx context.Context,
	operation, method string,
	signed *obs.CreateSignedUrlOutput,
	body io.Reader,
	contentLength int64,
) (*http.Response, error) {
	if signed == nil || strings.TrimSpace(signed.SignedUrl) == "" {
		return nil, fmt.Errorf("%s returned an empty signed URL", operation)
	}
	request, err := http.NewRequestWithContext(ctx, method, signed.SignedUrl, body)
	if err != nil {
		return nil, fmt.Errorf("%s request creation failed", operation)
	}
	request.Header = signed.ActualSignedRequestHeaders.Clone()
	if host := request.Header.Get("Host"); host != "" {
		request.Host = host
		request.Header.Del("Host")
	}
	if contentLength > 0 {
		request.ContentLength = contentLength
	}
	request.Header.Del("Content-Length")
	response, err := backend.httpClient.Do(request)
	if err != nil {
		return nil, safeSignedRequestError(operation, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		providerErr := obs.ParseResponseToObsError(response, true)
		safeErr := safeSignedRequestError(operation, providerErr)
		if isObjectNotFound(providerErr) {
			return nil, errors.Join(ErrObjectNotFound, safeErr)
		}
		if isObjectAlreadyExists(providerErr) {
			return nil, errors.Join(ErrObjectAlreadyExists, safeErr)
		}
		return nil, safeErr
	}
	return response, nil
}

func (backend *obsBackend) PublicURL(key string) (string, error) {
	if err := backend.validateKey(key); err != nil {
		return "", err
	}
	segments := strings.Split(key, "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	return backend.publicBaseURL + "/" + strings.Join(segments, "/"), nil
}

func (backend *obsBackend) objectFromHeaders(key string, headers http.Header, contentLength int64) (Object, error) {
	publicURL, err := backend.PublicURL(key)
	if err != nil {
		return Object{}, err
	}
	return Object{
		Key: key, PublicURL: publicURL, ContentType: headers.Get("Content-Type"),
		SizeBytes: contentLength, SHA256: strings.TrimSpace(headers.Get("x-obs-meta-" + obsSHA256Key)),
		ETag: normalizeETag(headers.Get("ETag")),
	}, nil
}

func normalizeETag(etag string) string {
	return strings.Trim(strings.TrimSpace(etag), "\"")
}

func (backend *obsBackend) validateRequest(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return backend.validateKey(key)
}

func (backend *obsBackend) validateKey(key string) error {
	if key != strings.TrimSpace(key) {
		return errors.New("object key must not contain surrounding whitespace")
	}
	if len(key) > maxObjectKeyBytes {
		return fmt.Errorf("object key exceeds %d bytes", maxObjectKeyBytes)
	}
	if err := validateObjectPath(key); err != nil {
		return fmt.Errorf("invalid object key: %w", err)
	}
	if !strings.HasPrefix(key, backend.prefix+"/") {
		return errors.New("object key is outside the configured OBS prefix")
	}
	return nil
}

func (backend *obsBackend) close() {
	backend.httpClient.CloseIdleConnections()
	backend.client.Close()
}
