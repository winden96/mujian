package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	maxGeneratedImageBytes = 24 << 20
	maxReferenceImageBytes = 10 << 20
)

var errLegacySource404 = errors.New("legacy source returned 404")

type imageContent struct {
	contentType string
	data        []byte
}

type legacyHTTPSource struct {
	client     *http.Client
	protection *common.SSRFProtection
}

func newLegacyHTTPSource(timeout time.Duration) *legacyHTTPSource {
	protection := &common.SSRFProtection{
		AllowPrivateIp:         false,
		DomainFilterMode:       false,
		IpFilterMode:           false,
		AllowedPorts:           []int{httpPort, httpsPort},
		ApplyIPFilterForDomain: true,
	}
	dialer := &safeSourceDialer{
		protection: protection,
		lookupIP:   net.DefaultResolver.LookupIPAddr,
		dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	source := &legacyHTTPSource{protection: protection}
	source.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many legacy image redirects")
			}
			return source.validateURL(request.URL.String())
		},
	}
	return source
}

const (
	httpPort  = 80
	httpsPort = 443
)

func (e *migrationEngine) materialize(ctx context.Context, record imageRecord) (imageContent, string, error) {
	limit := int64(maxGeneratedImageBytes)
	if record.Kind == recordKindReference {
		limit = maxReferenceImageBytes
	}
	if len(record.LegacyData) > 0 {
		content, err := validateImageContent(record.LegacyData, record.LegacyMIME, limit)
		return content, "database_blob", err
	}
	legacyURL := strings.TrimSpace(record.LegacyURL)
	if strings.HasPrefix(legacyURL, "data:") {
		content, err := decodeImageDataURL(legacyURL, limit)
		return content, "data_url", err
	}
	if legacyURL == "" {
		return imageContent{}, "", errors.New("image record has no recoverable source")
	}
	content, err := e.httpSource.download(ctx, legacyURL, limit)
	return content, "http_url", err
}

func validateImageContent(data []byte, declaredType string, maxBytes int64) (imageContent, error) {
	if len(data) == 0 {
		return imageContent{}, errors.New("image content is empty")
	}
	if int64(len(data)) > maxBytes {
		return imageContent{}, fmt.Errorf("image exceeds %d byte limit", maxBytes)
	}
	detectedType := http.DetectContentType(data[:min(len(data), 512)])
	if !allowedImageContentType(detectedType) {
		return imageContent{}, fmt.Errorf("unsupported image content type %q", detectedType)
	}
	if declaredType = strings.TrimSpace(declaredType); declaredType != "" && declaredType != detectedType {
		return imageContent{}, fmt.Errorf("declared image type %q does not match detected type %q", declaredType, detectedType)
	}
	return imageContent{contentType: detectedType, data: data}, nil
}

func decodeImageDataURL(value string, maxBytes int64) (imageContent, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return imageContent{}, errors.New("invalid base64 image data URL")
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"))
	if err != nil || !allowedImageContentType(mediaType) {
		return imageContent{}, errors.New("unsupported image data URL content type")
	}
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	data, err := io.ReadAll(io.LimitReader(decoder, maxBytes+1))
	if err != nil {
		return imageContent{}, errors.New("invalid base64 image data")
	}
	return validateImageContent(data, mediaType, maxBytes)
}

func (s *legacyHTTPSource) download(ctx context.Context, value string, maxBytes int64) (imageContent, error) {
	if err := s.validateURL(value); err != nil {
		return imageContent{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
	if err != nil {
		return imageContent{}, errors.New("invalid legacy image URL")
	}
	response, err := s.client.Do(request)
	if err != nil {
		// net/http errors include the complete request URL. Legacy URLs can
		// contain signed query parameters, so never persist that error verbatim
		// in the migration report.
		return imageContent{}, errors.New("download legacy image request failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return imageContent{}, errLegacySource404
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return imageContent{}, fmt.Errorf("legacy image returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return imageContent{}, fmt.Errorf("legacy image exceeds %d byte limit", maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return imageContent{}, fmt.Errorf("read legacy image: %w", err)
	}
	declaredType := ""
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		parsedType, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr != nil {
			return imageContent{}, errors.New("legacy image has an invalid Content-Type")
		}
		if parsedType != "application/octet-stream" {
			declaredType = parsedType
		}
	}
	return validateImageContent(data, declaredType, maxBytes)
}

func (s *legacyHTTPSource) validateURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("invalid legacy image URL")
	}
	if parsed.User != nil {
		return errors.New("legacy image URL must not contain credentials")
	}
	if err := s.protection.ValidateURL(value); err != nil {
		return fmt.Errorf("unsafe legacy image URL: %w", err)
	}
	return nil
}

func allowedImageContentType(contentType string) bool {
	switch contentType {
	case "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

type safeSourceDialer struct {
	protection *common.SSRFProtection
	lookupIP   func(context.Context, string) ([]net.IPAddr, error)
	dial       func(context.Context, string, string) (net.Conn, error)
}

func (d *safeSourceDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid legacy image address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || (port != httpPort && port != httpsPort) {
		return nil, errors.New("legacy image port is not allowed")
	}

	addresses := []net.IPAddr{}
	if literal := net.ParseIP(host); literal != nil {
		addresses = append(addresses, net.IPAddr{IP: literal})
	} else {
		addresses, err = d.lookupIP(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("legacy image hostname resolution failed")
		}
	}
	for _, candidate := range addresses {
		if !d.protection.IsIPAccessAllowed(candidate.IP) {
			return nil, errors.New("legacy image resolved to a private or blocked address")
		}
	}

	var lastErr error
	for _, candidate := range addresses {
		ipHost := candidate.IP.String()
		if candidate.Zone != "" {
			ipHost += "%" + candidate.Zone
		}
		connection, dialErr := d.dial(ctx, network, net.JoinHostPort(ipHost, portText))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}
