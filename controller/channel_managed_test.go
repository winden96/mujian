package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/stretchr/testify/require"
)

func TestBuildFetchModelsHeadersSuppressesAnthropicKeyForBearerOverride(t *testing.T) {
	channel := &model.Channel{
		Type: constant.ChannelTypeAnthropic,
		HeaderOverride: common.GetPointer(
			`{"Authorization":"Bearer {api_key}","anthropic-version":"2023-06-01"}`,
		),
	}

	headers, err := buildFetchModelsHeaders(channel, "tabcode-secret")
	require.NoError(t, err)
	require.Equal(t, "Bearer tabcode-secret", headers.Get("Authorization"))
	require.Equal(t, "2023-06-01", headers.Get("anthropic-version"))
	require.Empty(t, headers.Get("x-api-key"))
}

func TestStrictProviderTagsAreManagedChannels(t *testing.T) {
	for _, tag := range mujianprovider.StrictProviderTags() {
		t.Run(tag, func(t *testing.T) {
			require.True(t, isManagedProviderChannel(&model.Channel{Tag: common.GetPointer(tag)}))
		})
	}
	require.False(t, isManagedProviderChannel(&model.Channel{Tag: common.GetPointer("ordinary-route")}))
}
