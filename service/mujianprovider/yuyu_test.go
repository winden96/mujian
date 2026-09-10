package mujianprovider

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

// These prices are fixtures, not a statement of YuYu's live prices.
func mockYuYuAPI(t *testing.T) {
	t.Helper()
	previousClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: providerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "https", r.URL.Scheme)
		require.Equal(t, "api.yu-yu.ai", r.URL.Host)
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "Bearer yuyu-test-key", r.Header.Get("Authorization"))
		status := http.StatusOK
		var body string
		switch r.URL.Path {
		case "/v1/models":
			body = `{"data":[{"id":"deepseek-v4-flash"},{"id":"gemini-2.5-flash-image"},{"id":"gpt-image-2"},{"id":"gpt-5.6-sol"}]}`
		default:
			return nil, fmt.Errorf("unexpected YuYu request: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = previousClient })
}

func TestYuYuConfigureSyncAndRoute(t *testing.T) {
	setupProviderDB(t)
	mockYuYuAPI(t)
	require.NoError(t, Configure("yuyu", "yuyu-test-key"))
	require.NoError(t, Configure("yuyu", "yuyu-test-key"))
	require.NoError(t, ImportYuYuPricing(yuYuPricingFixture(t)))
	channels, err := providerChannels("yuyu", true)
	require.NoError(t, err)
	require.Len(t, channels, 3, "repeated configuration must not duplicate routes")
	for _, channel := range channels {
		require.Equal(t, constant.ChannelTypeOpenAI, channel.Type)
		require.Equal(t, "https://api.yu-yu.ai", channel.GetBaseURL())
		require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
		require.Equal(t, int64(50), channel.GetPriority())
	}

	count, err := Sync("yuyu")
	require.NoError(t, err)
	require.Equal(t, 1, count, "unpriced and incompatible models must not be advertised")
	_, _, err = Test("yuyu")
	require.NoError(t, err)
	require.NoError(t, SetEnabled("yuyu", true))
	var prices []model.ChannelModelPrice
	require.NoError(t, model.DB.Where("provider = ? AND available = ?", "yuyu", true).Order("catalog_id").Find(&prices).Error)
	require.Len(t, prices, 1)
	require.Equal(t, "deepseek-v4-flash", prices[0].CatalogID)
	require.Equal(t, 1.0, prices[0].InputPrice)
	require.Equal(t, 2.0, prices[0].OutputPrice)
	require.Equal(t, 0.1, prices[0].CacheRatio)
	for _, price := range prices {
		require.Equal(t, "USD", price.Currency)
		require.Equal(t, "https://api.yu-yu.ai/api/pricing", price.SourceURL)
		require.True(t, strings.HasPrefix(price.SourceVersion, "import-"))
		routable, err := CatalogModelRoutableForGroupTx(model.DB, price.CatalogID, "default")
		require.NoError(t, err)
		require.True(t, routable)
	}
	statuses, err := Statuses()
	require.NoError(t, err)
	encoded, err := common.Marshal(statuses)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"name":"羽宇 AI"`)
	require.NotContains(t, string(encoded), "yuyu-test-key")
	require.NoError(t, SetEnabled("yuyu", false))
	routable, err := CatalogModelRoutableForGroupTx(model.DB, "deepseek-v4-flash", "default")
	require.NoError(t, err)
	require.False(t, routable)
}

func TestYuYuPricingRequiresAuthorizationWithoutFallback(t *testing.T) {
	setupProviderDB(t)
	mockYuYuAPI(t)
	require.NoError(t, Configure("yuyu", "yuyu-test-key"))
	count, err := Sync("yuyu")
	require.ErrorContains(t, err, "请先导入官方价格 JSON")
	require.Zero(t, count)
	channels, err := providerChannels("yuyu", true)
	require.NoError(t, err)
	for _, channel := range channels {
		require.Empty(t, channel.Models)
		require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
	}
}
