package common

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRelayInfoGetFinalRequestRelayFormatPrefersExplicitFinal(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:             types.RelayFormatOpenAI,
		RequestConversionChain:  []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
		FinalRequestRelayFormat: types.RelayFormatOpenAIResponses,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatOpenAIResponses), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToConversionChain(t *testing.T) {
	info := &RelayInfo{
		RelayFormat:            types.RelayFormatOpenAI,
		RequestConversionChain: []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatClaude), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatFallsBackToRelayFormat(t *testing.T) {
	info := &RelayInfo{
		RelayFormat: types.RelayFormatGemini,
	}

	require.Equal(t, types.RelayFormat(types.RelayFormatGemini), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoGetFinalRequestRelayFormatNilReceiver(t *testing.T) {
	var info *RelayInfo
	require.Equal(t, types.RelayFormat(""), info.GetFinalRequestRelayFormat())
}

func TestRelayInfoResetAttemptResponseStatePreservesRequestStart(t *testing.T) {
	stream := false
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	start := time.Now().Add(-time.Minute)
	info := &RelayInfo{
		Request:                   &dto.GeneralOpenAIRequest{Stream: &stream},
		IsStream:                  true,
		StartTime:                 start,
		FirstResponseTime:         time.Now(),
		ReceivedResponseCount:     7,
		DelayPingUntilFirstWrite:  true,
		RuntimeHeadersOverride:    map[string]interface{}{"Authorization": "Bearer stale"},
		UseRuntimeHeadersOverride: true,
		ParamOverrideAudit:        []string{"stale"},
		FinalRequestRelayFormat:   types.RelayFormatClaude,
		RequestConversionChain:    []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude},
		StreamStatus:              NewStreamStatus(),
	}
	info.StreamStatus.RecordError("previous attempt")

	info.ResetAttemptResponseState(context)

	require.Equal(t, start, info.StartTime)
	require.False(t, info.IsStream)
	require.Equal(t, start.Add(-time.Second), info.FirstResponseTime)
	require.Zero(t, info.ReceivedResponseCount)
	require.Nil(t, info.StreamStatus)
	require.False(t, info.DelayPingUntilFirstWrite)
	require.Nil(t, info.RuntimeHeadersOverride)
	require.False(t, info.UseRuntimeHeadersOverride)
	require.Nil(t, info.ParamOverrideAudit)
	require.Empty(t, info.FinalRequestRelayFormat)
	require.Equal(t, []types.RelayFormat{types.RelayFormatOpenAI}, info.RequestConversionChain)
	require.Equal(t, types.RelayFormatOpenAI, info.GetFinalRequestRelayFormat())
	require.False(t, info.HasSendResponse())

	info.SetFirstResponseTime()
	require.True(t, info.HasSendResponse())
}
