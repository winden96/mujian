package constant

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPath2RelayModeMidjourneySimpleChange(t *testing.T) {
	for _, path := range []string{
		"/mj/submit/simple-change",
		"/mj-fast/mj/submit/simple-change",
	} {
		require.Equal(t, RelayModeMidjourneySimpleChange, Path2RelayModeMidjourney(path))
	}

	require.Equal(t, RelayModeMidjourneyChange, Path2RelayModeMidjourney("/mj/submit/change"))
}
