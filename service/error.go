package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

const maxRelayErrorResponseBytes = 1 << 20

func MidjourneyErrorWrapper(code int, desc string) *dto.MidjourneyResponse {
	return &dto.MidjourneyResponse{
		Code:        code,
		Description: desc,
	}
}

func MidjourneyErrorWithStatusCodeWrapper(code int, desc string, statusCode int) *dto.MidjourneyResponseWithStatusCode {
	return &dto.MidjourneyResponseWithStatusCode{
		StatusCode: statusCode,
		Response:   *MidjourneyErrorWrapper(code, desc),
	}
}

//// OpenAIErrorWrapper wraps an error into an OpenAIErrorWithStatusCode
//func OpenAIErrorWrapper(err error, code string, statusCode int) *dto.OpenAIErrorWithStatusCode {
//	text := err.Error()
//	lowerText := strings.ToLower(text)
//	if !strings.HasPrefix(lowerText, "get file base64 from url") && !strings.HasPrefix(lowerText, "mime type is not supported") {
//		if strings.Contains(lowerText, "post") || strings.Contains(lowerText, "dial") || strings.Contains(lowerText, "http") {
//			common.SysLog(fmt.Sprintf("error: %s", text))
//			text = "请求上游地址失败"
//		}
//	}
//	openAIError := dto.OpenAIError{
//		Message: text,
//		Type:    "new_api_error",
//		Code:    code,
//	}
//	return &dto.OpenAIErrorWithStatusCode{
//		Error:      openAIError,
//		StatusCode: statusCode,
//	}
//}
//
//func OpenAIErrorWrapperLocal(err error, code string, statusCode int) *dto.OpenAIErrorWithStatusCode {
//	openaiErr := OpenAIErrorWrapper(err, code, statusCode)
//	openaiErr.LocalError = true
//	return openaiErr
//}

func ClaudeErrorWrapper(err error, code string, statusCode int) *dto.ClaudeErrorWithStatusCode {
	text := err.Error()
	lowerText := strings.ToLower(text)
	if !strings.HasPrefix(lowerText, "get file base64 from url") {
		if strings.Contains(lowerText, "post") || strings.Contains(lowerText, "dial") || strings.Contains(lowerText, "http") {
			common.SysLog(fmt.Sprintf("error: %s", text))
			text = "请求上游地址失败"
		}
	}
	claudeError := types.ClaudeError{
		Message: text,
		Type:    "new_api_error",
	}
	return &dto.ClaudeErrorWithStatusCode{
		Error:      claudeError,
		StatusCode: statusCode,
	}
}

func ClaudeErrorWrapperLocal(err error, code string, statusCode int) *dto.ClaudeErrorWithStatusCode {
	claudeErr := ClaudeErrorWrapper(err, code, statusCode)
	claudeErr.LocalError = true
	return claudeErr
}

func RelayErrorHandler(ctx context.Context, resp *http.Response, showBodyWhenFail bool) (newApiErr *types.NewAPIError) {
	defer CloseResponseBodyGracefully(resp)

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRelayErrorResponseBytes+1))
	if err != nil {
		newApiErr = types.NewErrorWithStatusCode(
			fmt.Errorf("read upstream error response: %w", err),
			types.ErrorCodeReadResponseBodyFailed,
			resp.StatusCode,
		)
		return
	}
	if len(responseBody) > maxRelayErrorResponseBytes {
		newApiErr = types.NewErrorWithStatusCode(
			fmt.Errorf("bad response status code %d, response body exceeds %d bytes", resp.StatusCode, maxRelayErrorResponseBytes),
			types.ErrorCodeBadResponseBody,
			resp.StatusCode,
		)
		logger.LogError(ctx, fmt.Sprintf("upstream error response rejected: status=%d content_type_class=%s body_bytes_over=%d", resp.StatusCode, classifyUpstreamContentType(resp.Header.Get("Content-Type")), maxRelayErrorResponseBytes))
		return
	}
	var errResponse dto.GeneralErrorResponse
	buildSafeErr := func(message string) error {
		if message == "" {
			return fmt.Errorf("bad response status code %d", resp.StatusCode)
		}
		return fmt.Errorf("bad response status code %d, message: %s", resp.StatusCode, message)
	}

	if err := common.Unmarshal(responseBody, &errResponse); err != nil {
		logger.LogError(ctx, fmt.Sprintf("upstream returned non-JSON error: status=%d content_type_class=%s body_bytes=%d", resp.StatusCode, classifyUpstreamContentType(resp.Header.Get("Content-Type")), len(responseBody)))
		newApiErr = types.NewErrorWithStatusCode(buildSafeErr(""), types.ErrorCodeBadResponseStatusCode, resp.StatusCode)
		return
	}

	if common.GetJsonType(errResponse.Error) == "object" {
		// General format error (OpenAI, Anthropic, Gemini, etc.)
		oaiError := errResponse.TryToOpenAIError()
		if oaiError != nil {
			newApiErr = types.WithOpenAIError(SanitizeOpenAIError(ctx, *oaiError), resp.StatusCode)
			if showBodyWhenFail {
				newApiErr.Err = buildSafeErr(newApiErr.Error())
			}
			return
		}
	}
	newApiErr = types.NewOpenAIError(errors.New(SanitizeUpstreamText(ctx, errResponse.ToMessage())), types.ErrorCodeBadResponseStatusCode, resp.StatusCode)
	if showBodyWhenFail {
		newApiErr.Err = buildSafeErr(newApiErr.Error())
	}
	return
}

// classifyUpstreamContentType preserves enough information for diagnostics
// without logging an attacker-controlled header value that may contain a key.
func classifyUpstreamContentType(value string) string {
	if strings.TrimSpace(value) == "" {
		return "missing"
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "invalid"
	}
	mediaType = strings.ToLower(mediaType)
	switch {
	case mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"):
		return "json"
	case mediaType == "text/event-stream":
		return "event_stream"
	case strings.HasPrefix(mediaType, "text/"):
		return "text"
	case strings.HasPrefix(mediaType, "image/"), strings.HasPrefix(mediaType, "audio/"), strings.HasPrefix(mediaType, "video/"), mediaType == "application/octet-stream":
		return "binary"
	default:
		return "other"
	}
}

// SanitizeUpstreamText removes the exact key selected for the current channel
// before applying the generic URL, host, and IP masking rules.
func SanitizeUpstreamText(ctx context.Context, text string) string {
	ginContext, ok := ctx.(*gin.Context)
	if ok {
		channelKey := common.GetContextKeyString(ginContext, constant.ContextKeyChannelKey)
		if channelKey != "" {
			text = strings.ReplaceAll(text, channelKey, "***")
		}
	}
	return common.MaskSensitiveInfo(text)
}

// SanitizeClaudeError sanitizes every client-visible field before the error is
// converted into a NewAPIError or written to a response.
func SanitizeClaudeError(ctx context.Context, upstreamError types.ClaudeError) types.ClaudeError {
	upstreamError.Type = SanitizeUpstreamText(ctx, upstreamError.Type)
	upstreamError.Message = SanitizeUpstreamText(ctx, upstreamError.Message)
	return upstreamError
}

// SanitizeOpenAIError sanitizes all scalar fields. Metadata and structured
// codes are intentionally discarded because they can contain arbitrary nested
// upstream data and are not required to diagnose a relay failure.
func SanitizeOpenAIError(ctx context.Context, upstreamError types.OpenAIError) types.OpenAIError {
	upstreamError.Message = SanitizeUpstreamText(ctx, upstreamError.Message)
	upstreamError.Type = SanitizeUpstreamText(ctx, upstreamError.Type)
	upstreamError.Param = SanitizeUpstreamText(ctx, upstreamError.Param)
	if code, ok := upstreamError.Code.(string); ok {
		upstreamError.Code = SanitizeUpstreamText(ctx, code)
	} else if !isSafeScalarErrorCode(upstreamError.Code) {
		upstreamError.Code = "upstream_error"
	}
	upstreamError.Metadata = nil
	return upstreamError
}

func isSafeScalarErrorCode(code any) bool {
	switch code.(type) {
	case nil, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number:
		return true
	default:
		return false
	}
}

func ResetStatusCode(newApiErr *types.NewAPIError, statusCodeMappingStr string) {
	if newApiErr == nil {
		return
	}
	if statusCodeMappingStr == "" || statusCodeMappingStr == "{}" {
		return
	}
	statusCodeMapping := make(map[string]any)
	err := common.Unmarshal([]byte(statusCodeMappingStr), &statusCodeMapping)
	if err != nil {
		return
	}
	if newApiErr.StatusCode == http.StatusOK {
		return
	}
	codeStr := strconv.Itoa(newApiErr.StatusCode)
	if value, ok := statusCodeMapping[codeStr]; ok {
		intCode, ok := parseStatusCodeMappingValue(value)
		if !ok {
			return
		}
		newApiErr.StatusCode = intCode
	}
}

func parseStatusCodeMappingValue(value any) (int, bool) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return 0, false
		}
		statusCode, err := strconv.Atoi(v)
		if err != nil {
			return 0, false
		}
		return statusCode, true
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case json.Number:
		statusCode, err := strconv.Atoi(v.String())
		if err != nil {
			return 0, false
		}
		return statusCode, true
	default:
		return 0, false
	}
}

func TaskErrorWrapperLocal(err error, code string, statusCode int) *dto.TaskError {
	openaiErr := TaskErrorWrapper(err, code, statusCode)
	openaiErr.LocalError = true
	return openaiErr
}

func TaskErrorWrapper(err error, code string, statusCode int) *dto.TaskError {
	text := err.Error()
	lowerText := strings.ToLower(text)
	if strings.Contains(lowerText, "post") || strings.Contains(lowerText, "dial") || strings.Contains(lowerText, "http") {
		common.SysLog(fmt.Sprintf("error: %s", text))
		//text = "请求上游地址失败"
		text = common.MaskSensitiveInfo(text)
	}
	//避免暴露内部错误
	taskError := &dto.TaskError{
		Code:       code,
		Message:    text,
		StatusCode: statusCode,
		Error:      err,
	}

	return taskError
}

// TaskErrorFromAPIError 将 PreConsumeBilling 返回的 NewAPIError 转换为 TaskError。
func TaskErrorFromAPIError(apiErr *types.NewAPIError) *dto.TaskError {
	if apiErr == nil {
		return nil
	}
	return &dto.TaskError{
		Code:       string(apiErr.GetErrorCode()),
		Message:    apiErr.Err.Error(),
		StatusCode: apiErr.StatusCode,
		Error:      apiErr.Err,
	}
}
