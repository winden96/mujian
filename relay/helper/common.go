package helper

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const downstreamWriteFailureContextKey = "relay_downstream_write_failure"

func markDownstreamWriteFailure(c *gin.Context) {
	if c != nil {
		c.Set(downstreamWriteFailureContextKey, true)
	}
}

// HasDownstreamWriteFailure distinguishes a broken client/proxy connection
// from an upstream channel failure. Controllers must not retry or penalize a
// healthy upstream after the response destination becomes unwritable.
func HasDownstreamWriteFailure(c *gin.Context) bool {
	return c != nil && c.GetBool(downstreamWriteFailureContextKey)
}

func FlushWriter(c *gin.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("flush panic recovered: %v", r)
		}
	}()

	if c == nil || c.Writer == nil {
		return nil
	}

	if c.Request != nil && c.Request.Context().Err() != nil {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return errors.New("streaming error: flusher not found")
	}

	flusher.Flush()
	return nil
}

func SetEventStreamHeaders(c *gin.Context) {
	// 检查是否已经设置过头部
	if c.GetBool("event_stream_headers_set") {
		return
	}

	// 设置标志，表示头部已经设置过
	c.Set("event_stream_headers_set", true)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("Transfer-Encoding", "chunked")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
}

// ResetEventStreamHeaders removes response-only state left by an upstream that
// failed before committing bytes. This lets a non-stream fallback publish its
// own framing without inheriting chunked SSE headers from the prior attempt.
func ResetEventStreamHeaders(c *gin.Context) {
	if c == nil || c.Writer == nil || c.Writer.Written() {
		return
	}
	c.Set("event_stream_headers_set", false)
	for _, name := range []string{"Content-Type", "Cache-Control", "Connection", "Transfer-Encoding", "X-Accel-Buffering"} {
		c.Writer.Header().Del(name)
	}
}

func ClaudeData(c *gin.Context, resp dto.ClaudeResponse) error {
	jsonData, err := common.Marshal(resp)
	if err != nil {
		return fmt.Errorf("error marshalling stream response: %w", err)
	}
	return writeEventStreamPayload(c, fmt.Sprintf("event: %s\ndata: %s\n\n", resp.Type, jsonData))
}

func ClaudeChunkData(c *gin.Context, resp dto.ClaudeResponse, data string) error {
	return writeEventStreamPayload(c, fmt.Sprintf("event: %s\ndata: %s\n\n", resp.Type, data))
}

func ResponseChunkData(c *gin.Context, resp dto.ResponsesStreamResponse, data string) {
	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("event: %s\n", resp.Type)})
	c.Render(-1, common.CustomEvent{Data: fmt.Sprintf("data: %s", data)})
	_ = FlushWriter(c)
}

func StringData(c *gin.Context, str string) error {
	return writeEventStreamPayload(c, "data: "+str+"\n\n")
}

func writeEventStreamPayload(c *gin.Context, payload string) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			markDownstreamWriteFailure(c)
			err = fmt.Errorf("write event stream panic recovered: %v", recovered)
		}
	}()

	if c == nil || c.Writer == nil {
		return errors.New("context or writer is nil")
	}

	if c.Request != nil && c.Request.Context().Err() != nil {
		return fmt.Errorf("request context done: %w", c.Request.Context().Err())
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	if c.Writer.Header().Get("Cache-Control") == "" {
		c.Writer.Header().Set("Cache-Control", "no-cache")
	}
	if _, err := c.Writer.Write([]byte(payload)); err != nil {
		markDownstreamWriteFailure(c)
		return fmt.Errorf("write event stream data failed: %w", err)
	}
	if err := FlushWriter(c); err != nil {
		markDownstreamWriteFailure(c)
		return err
	}
	return nil
}

func PingData(c *gin.Context) error {
	return writeEventStreamPayload(c, ": PING\n\n")
}

func ObjectData(c *gin.Context, object interface{}) error {
	if object == nil {
		return errors.New("object is nil")
	}
	jsonData, err := common.Marshal(object)
	if err != nil {
		return fmt.Errorf("error marshalling object: %w", err)
	}
	return StringData(c, string(jsonData))
}

func Done(c *gin.Context) error {
	return StringData(c, "[DONE]")
}

func WssString(c *gin.Context, ws *websocket.Conn, str string) error {
	if ws == nil {
		logger.LogError(c, "websocket connection is nil")
		return errors.New("websocket connection is nil")
	}
	//common.LogInfo(c, fmt.Sprintf("sending message: %s", str))
	return ws.WriteMessage(1, []byte(str))
}

func WssObject(c *gin.Context, ws *websocket.Conn, object interface{}) error {
	jsonData, err := common.Marshal(object)
	if err != nil {
		return fmt.Errorf("error marshalling object: %w", err)
	}
	if ws == nil {
		logger.LogError(c, "websocket connection is nil")
		return errors.New("websocket connection is nil")
	}
	//common.LogInfo(c, fmt.Sprintf("sending message: %s", jsonData))
	return ws.WriteMessage(1, jsonData)
}

func WssError(c *gin.Context, ws *websocket.Conn, openaiError types.OpenAIError) {
	if ws == nil {
		return
	}
	errorObj := &dto.RealtimeEvent{
		Type:    "error",
		EventId: GetLocalRealtimeID(c),
		Error:   &openaiError,
	}
	_ = WssObject(c, ws, errorObj)
}

func GetResponseID(c *gin.Context) string {
	logID := c.GetString(common.RequestIdKey)
	return fmt.Sprintf("chatcmpl-%s", logID)
}

func GetLocalRealtimeID(c *gin.Context) string {
	logID := c.GetString(common.RequestIdKey)
	return fmt.Sprintf("evt_%s", logID)
}

func GenerateStartEmptyResponse(id string, createAt int64, model string, systemFingerprint *string) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:                id,
		Object:            "chat.completion.chunk",
		Created:           createAt,
		Model:             model,
		SystemFingerprint: systemFingerprint,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				Delta: dto.ChatCompletionsStreamResponseChoiceDelta{
					Role:    "assistant",
					Content: common.GetPointer(""),
				},
			},
		},
	}
}

func GenerateStopResponse(id string, createAt int64, model string, finishReason string) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:                id,
		Object:            "chat.completion.chunk",
		Created:           createAt,
		Model:             model,
		SystemFingerprint: nil,
		Choices: []dto.ChatCompletionsStreamResponseChoice{
			{
				FinishReason: &finishReason,
			},
		},
	}
}

func GenerateFinalUsageResponse(id string, createAt int64, model string, usage dto.Usage) *dto.ChatCompletionsStreamResponse {
	return &dto.ChatCompletionsStreamResponse{
		Id:                id,
		Object:            "chat.completion.chunk",
		Created:           createAt,
		Model:             model,
		SystemFingerprint: nil,
		Choices:           make([]dto.ChatCompletionsStreamResponseChoice, 0),
		Usage:             &usage,
	}
}
