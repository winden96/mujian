package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"

	"github.com/gin-gonic/gin"
)

const stagedResponseContextKey = "mujian_staged_http_response"

var blockedUpstreamResponseHeaders = []string{
	"Authorization",
	"Proxy-Authorization",
	"x-api-key",
	"api-key",
	"x-goog-api-key",
}

type stagedHTTPResponse struct {
	statusCode int
	header     http.Header
	body       []byte
}

func copySafeUpstreamResponseHeaders(destination, source http.Header, channelKey string) {
	for name, values := range source {
		if strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, common.RequestIdKey) ||
			isBlockedUpstreamResponseHeader(name) || headerValuesContainChannelKey(values, channelKey) {
			continue
		}
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}

func headerValuesContainChannelKey(values []string, channelKey string) bool {
	if channelKey == "" {
		return false
	}
	for _, value := range values {
		if strings.Contains(value, channelKey) {
			return true
		}
	}
	return false
}

func isBlockedUpstreamResponseHeader(name string) bool {
	for _, blocked := range blockedUpstreamResponseHeaders {
		if strings.EqualFold(name, blocked) {
			return true
		}
	}
	return false
}

func CloseResponseBodyGracefully(httpResponse *http.Response) {
	if httpResponse == nil || httpResponse.Body == nil {
		return
	}
	err := httpResponse.Body.Close()
	if err != nil {
		common.SysError("failed to close response body: " + err.Error())
	}
}

func IOCopyBytesGracefully(c *gin.Context, src *http.Response, data []byte) {
	if c.Writer == nil {
		return
	}

	body := io.NopCloser(bytes.NewBuffer(data))

	// We shouldn't set the header before we parse the response body, because the parse part may fail.
	// And then we will have to send an error response, but in this case, the header has already been set.
	// So the httpClient will be confused by the response.
	// For example, Postman will report error, and we cannot check the response at all.
	if src != nil {
		copySafeUpstreamResponseHeaders(c.Writer.Header(), src.Header, common.GetContextKeyString(c, constant.ContextKeyChannelKey))
	}

	// set Content-Length header manually BEFORE calling WriteHeader
	c.Writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))

	// Write header with status code (this sends the headers)
	if src != nil {
		c.Writer.WriteHeader(src.StatusCode)
	} else {
		c.Writer.WriteHeader(http.StatusOK)
	}

	_, err := io.Copy(c.Writer, body)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("failed to copy response body: %s", err.Error()))
	}
	c.Writer.Flush()
}

// StageResponseBytes retains a complete response without committing headers or
// a body to the client. Strict non-stream billing uses this boundary so the
// successful upstream response becomes visible only after settlement commits.
func StageResponseBytes(c *gin.Context, src *http.Response, data []byte) {
	if c == nil {
		return
	}
	statusCode := http.StatusOK
	header := make(http.Header)
	if src != nil {
		if src.StatusCode != 0 {
			statusCode = src.StatusCode
		}
		copySafeUpstreamResponseHeaders(header, src.Header, common.GetContextKeyString(c, constant.ContextKeyChannelKey))
	}
	c.Set(stagedResponseContextKey, &stagedHTTPResponse{
		statusCode: statusCode,
		header:     header,
		body:       append([]byte(nil), data...),
	})
}

// FlushStagedResponseBytes atomically takes and writes the staged response.
// Returning false means the request did not stage a response.
func FlushStagedResponseBytes(c *gin.Context) bool {
	response := takeStagedResponse(c)
	if response == nil {
		return false
	}
	IOCopyBytesGracefully(c, &http.Response{
		StatusCode: response.statusCode,
		Header:     response.header,
	}, response.body)
	return true
}

// DiscardStagedResponseBytes drops a staged success response before an error is
// returned. The caller can then write one clean error response before the first
// byte without concatenating it with the upstream JSON.
func DiscardStagedResponseBytes(c *gin.Context) {
	_ = takeStagedResponse(c)
}

func takeStagedResponse(c *gin.Context) *stagedHTTPResponse {
	if c == nil {
		return nil
	}
	value, exists := c.Get(stagedResponseContextKey)
	if !exists {
		return nil
	}
	c.Set(stagedResponseContextKey, nil)
	response, _ := value.(*stagedHTTPResponse)
	return response
}
