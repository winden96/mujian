package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldRetryStopsAfterResponseIsCommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	retryable := types.NewErrorWithStatusCode(
		errors.New("upstream failed"),
		types.ErrorCodeChannelResponseTimeExceeded,
		http.StatusInternalServerError,
	)

	require.True(t, shouldRetry(context, retryable, 1))
	_, err := context.Writer.Write([]byte("partial response"))
	require.NoError(t, err)
	require.False(t, shouldRetry(context, retryable, 1))
}

func TestResetRelayAttemptResponseStateDropsPreviousChannelAttestation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Writer.Header().Set(common.ReasoningContentSeparatedHeader, "true")

	resetRelayAttemptResponseState(context)

	require.Empty(t, context.Writer.Header().Get(common.ReasoningContentSeparatedHeader))
}
