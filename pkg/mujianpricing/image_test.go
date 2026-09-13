package mujianpricing

import (
	"github.com/stretchr/testify/require"
	"math"
	"testing"
)

func TestImageQuoteRoundsTotalOnce(t *testing.T) {
	q, err := NewImageQuote("gpt-image-2", 2, 7.3, 500000)
	require.NoError(t, err)
	require.Equal(t, 13699, q.Quota(1))
	require.Equal(t, 27397, q.Quota(2))
	require.Zero(t, q.Quota(0))
	require.Equal(t, ImagePrice{Currency: "CNY", Unit: "image", Amount: 0.2}, q.Price)
	require.Nil(t, FixedImagePrice("other-image-model"))
}

func TestImageQuoteRejectsUnsafeReservation(t *testing.T) {
	for _, rate := range []float64{0, -1, math.NaN(), math.Inf(1), math.SmallestNonzeroFloat64} {
		_, err := NewImageQuote("gpt-image-2", 1, rate, 500000)
		require.Error(t, err)
	}
	_, err := NewImageQuote("gpt-image-2", 0, 7.3, 500000)
	require.Error(t, err)
	_, err = NewImageQuote("gpt-image-2", ^uint(0), 7.3, 500000)
	require.Error(t, err)
}

func TestNanoQuoteChargesPerSuccessfulRequest(t *testing.T) {
	q, err := NewImageQuote("nano-banana-2", 2, 7.3, 500000)
	require.NoError(t, err)
	require.Equal(t, ImagePrice{Currency: "CNY", Unit: "request", Amount: 0.2}, q.Price)
	require.Equal(t, 13699, q.Quota(1))
	require.Equal(t, 13699, q.Quota(2))
	require.Zero(t, q.Quota(0))
}
