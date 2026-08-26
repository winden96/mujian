package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	"github.com/stretchr/testify/require"
)

func TestIsGeminiEmbeddingRequest(t *testing.T) {
	require.True(t, isGeminiEmbeddingRequest(&dto.GeminiEmbeddingRequest{}))
	require.True(t, isGeminiEmbeddingRequest(&dto.GeminiBatchEmbeddingRequest{}))
	require.False(t, isGeminiEmbeddingRequest(&dto.GeminiChatRequest{}))
}
