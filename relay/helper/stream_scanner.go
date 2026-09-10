package helper

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
)

const (
	InitialScannerBufferSize    = 64 << 10 // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 64 << 20 // 64MB (64*1024*1024) default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	DefaultStreamingTimeout     = 300 * time.Second
)

var ErrStreamResponseTooLarge = errors.New("stream response exceeded byte limit")

func getScannerBufferSize() int {
	if constant.StreamScannerMaxBufferMB > 0 {
		return constant.StreamScannerMaxBufferMB << 20
	}
	return DefaultMaxScannerBufferSize
}

// scanRawLines keeps each line delimiter in the token so a cumulative stream
// limit accounts for the exact upstream bytes, including blank and metadata
// lines that never reach a protocol data handler.
func scanRawLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		return index + 1, data[:index+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func scanRawLinesWithinLimit(limit uint64) bufio.SplitFunc {
	var scannedBytes uint64
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		advance, token, err = scanRawLines(data, atEOF)
		if err != nil || limit == 0 {
			return advance, token, err
		}

		if scannedBytes > limit {
			return 0, nil, fmt.Errorf("%w: limit=%d bytes", ErrStreamResponseTooLarge, limit)
		}
		remaining := limit - scannedBytes
		candidateBytes := uint64(len(data))
		if token != nil {
			candidateBytes = uint64(len(token))
		}
		if candidateBytes > remaining {
			return 0, nil, fmt.Errorf("%w: limit=%d bytes", ErrStreamResponseTooLarge, limit)
		}
		if token != nil {
			scannedBytes += candidateBytes
		}
		return advance, token, nil
	}
}

func trimRawLineEnding(line []byte) string {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	return string(line)
}

func streamPingAllowed(c *gin.Context, info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	if !info.DelayPingUntilFirstWrite {
		return true
	}
	return c != nil && c.Writer != nil && c.Writer.Written()
}

// ParseSSEDataLine applies the exact framing accepted by the stream parser.
// Leading whitespace is significant: a line must start with "data:" in
// column zero. The first-response watchdog uses the same predicate so ignored
// input cannot accidentally disable failover.
func ParseSSEDataLine(line string) (string, bool) {
	line = strings.TrimSuffix(line, "\r")
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	return data, data != ""
}

// ParseSSEEventType returns one exact, unambiguous top-level JSON type. The
// first-response watchdog and protocol handlers share this parser so duplicate
// or case-variant keys cannot be classified differently by each layer.
func ParseSSEEventType(data string) (string, error) {
	eventType, found, err := common.DecodeUniqueTopLevelStringField(common.StringToByteSlice(data), "type")
	if err != nil {
		return "", fmt.Errorf("decode SSE event type: %w", err)
	}
	if !found {
		return "", errors.New("SSE event is missing type")
	}
	if strings.ContainsAny(eventType, "\r\n") {
		return "", errors.New("SSE event type contains a newline")
	}
	return eventType, nil
}

func IsSSEHeartbeatData(data string) bool {
	eventType, err := ParseSSEEventType(data)
	return err == nil && eventType == "ping"
}

func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult)) {

	if resp == nil || resp.Body == nil {
		return
	}
	body := resp.Body
	closeBody := sync.OnceFunc(func() { _ = body.Close() })
	defer closeBody()

	if info == nil || dataHandler == nil {
		return
	}

	if info.StreamStatus == nil {
		info.StreamStatus = relaycommon.NewStreamStatus()
	}

	streamingTimeout := time.Duration(constant.StreamingTimeout) * time.Second
	if streamingTimeout <= 0 {
		streamingTimeout = DefaultStreamingTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		stopChan   = make(chan struct{}, 1)
		scanner    = bufio.NewScanner(resp.Body)
		ticker     = time.NewTicker(streamingTimeout)
		pingTicker *time.Ticker
		wg         sync.WaitGroup // 用于等待所有 goroutine 退出
		received   atomic.Int64
	)
	signalStop := func() {
		select {
		case stopChan <- struct{}{}:
		default:
		}
	}

	generalSettings := operation_setting.GetGeneralSetting()
	pingEnabled := generalSettings.PingIntervalEnabled && !info.DisablePing
	pingInterval := time.Duration(generalSettings.PingIntervalSeconds) * time.Second
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}

	if pingEnabled {
		pingTicker = time.NewTicker(pingInterval)
	}

	if common.DebugEnabled {
		// print timeout and ping interval for debugging
		println("relay timeout seconds:", common.RelayTimeout)
		println("relay max idle conns:", common.RelayMaxIdleConns)
		println("relay max idle conns per host:", common.RelayMaxIdleConnsPerHost)
		println("streaming timeout seconds:", int64(streamingTimeout.Seconds()))
		println("ping interval seconds:", int64(pingInterval.Seconds()))
	}

	streamByteLimit := uint64(0)
	if info.MaxStreamResponseBytes > 0 {
		streamByteLimit = uint64(info.MaxStreamResponseBytes)
	}
	scannerBufferSize := getScannerBufferSize()
	if streamByteLimit > 0 && streamByteLimit < uint64(scannerBufferSize) {
		// Leave room to observe one byte beyond the exact boundary so an
		// unterminated oversized line reports the request limit, not ErrTooLong.
		scannerBufferSize = int(streamByteLimit) + 1
	}
	scanner.Buffer(make([]byte, InitialScannerBufferSize), scannerBufferSize)
	scanner.Split(scanRawLinesWithinLimit(streamByteLimit))
	SetEventStreamHeaders(c)

	dataChan := make(chan string, 10)
	var pingChan <-chan time.Time
	if pingTicker != nil {
		pingChan = pingTicker.C
	}

	// Ping and streamed data share this single writer. A ResponseWriter call
	// cannot outlive StreamScannerHandler because this worker is joined below.
	wg.Add(1)
	gopool.Go(func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("stream writer goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("writer panic: %v", r))
			}
			signalStop()
		}()
		sr := newStreamResult(info.StreamStatus)
		for {
			select {
			case data, ok := <-dataChan:
				if !ok {
					return
				}
				sr.reset()
				dataHandler(data, sr)
				if sr.IsStopped() {
					return
				}
			case <-pingChan:
				if !streamPingAllowed(c, info) {
					continue
				}
				if err := PingData(c); err != nil {
					logger.LogError(c, "ping data error: "+err.Error())
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPingFail, err)
					return
				}
				if common.DebugEnabled {
					println("ping data sent")
				}
			}
		}
	})

	// Scanner goroutine with improved error handling
	wg.Add(1)
	common.RelayCtxGo(ctx, func() {
		defer wg.Done()
		defer func() {
			close(dataChan)
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("scanner goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("scanner panic: %v", r))
			}
			signalStop()
			if common.DebugEnabled {
				println("scanner goroutine exited")
			}
		}()

		for scanner.Scan() {
			// 检查是否需要停止
			select {
			case <-ctx.Done():
				return
			case <-c.Request.Context().Done():
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
				return
			default:
			}

			ticker.Reset(streamingTimeout)
			line := trimRawLineEnding(scanner.Bytes())
			if common.DebugEnabled {
				println("stream event bytes:", len(line))
			}

			data, ok := ParseSSEDataLine(line)
			if !ok {
				continue
			}
			if !strings.HasPrefix(data, "[DONE]") {
				if !IsSSEHeartbeatData(data) {
					info.SetFirstResponseTime()
					received.Add(1)
				}

				select {
				case dataChan <- data:
				case <-ctx.Done():
					return
				case <-c.Request.Context().Done():
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
					return
				}
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				if common.DebugEnabled {
					println("received [DONE], stopping scanner")
				}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			if err != io.EOF {
				logger.LogError(c, "scanner error: "+err.Error())
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
			}
		}
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	})

	// 主循环等待完成或超时
	select {
	case <-ticker.C:
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
	case <-stopChan:
		// EndReason already set by the goroutine that triggered stopChan
	case <-c.Request.Context().Done():
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
	}

	cancel()
	ticker.Stop()
	if pingTicker != nil {
		pingTicker.Stop()
	}
	// Closing the upstream body unblocks Scanner.Scan after a timeout, client
	// disconnect, or handler stop so all workers can finish before we return.
	closeBody()
	wg.Wait()
	info.ReceivedResponseCount = int(received.Load())

	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
		logger.LogInfo(c, fmt.Sprintf("stream ended: %s", info.StreamStatus.Summary()))
	} else {
		logger.LogError(c, fmt.Sprintf("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
	}
}
