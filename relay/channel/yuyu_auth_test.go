package channel

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestYuYuManagedAuthReplacesInheritedCredentials(t *testing.T) {
	req := httptest.NewRequest("POST", "https://api.yu-yu.ai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer wrong-provider")
	req.Header.Set("x-api-key", "wrong-provider")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "unrequested-beta")
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{ManagedProvider: true, ManagedProviderID: types.PriceProviderYuYu, ApiKey: "yuyu-test-key"},
		PriceData:   types.PriceData{PriceProvider: types.PriceProviderYuYu},
	}
	require.NoError(t, enforceManagedProviderAuthHeaders(req, info))
	require.Equal(t, "Bearer yuyu-test-key", req.Header.Get("Authorization"))
	require.Empty(t, req.Header.Get("x-api-key"))
	require.Empty(t, req.Header.Get("anthropic-version"))
	require.Empty(t, req.Header.Get("anthropic-beta"))
	info.PriceData.PriceProvider = types.PriceProviderTabCode
	require.ErrorContains(t, enforceManagedProviderAuthHeaders(req, info), "does not match")
}
