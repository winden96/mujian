package mujianprovider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

type providerRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn providerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestClaudeProviderCapabilitiesAreNarrowAndProviderSpecific(t *testing.T) {
	zenmux, ok := definition("zenmux")
	require.True(t, ok)
	require.Equal(t, []string{"chat"}, zenmux.capabilities.ProfileIDs)
	require.Equal(t, constant.ChannelTypeAnthropic, zenmux.capabilities.ChannelType)
	require.Equal(t, int64(600), profiles[0].Priority[zenmux.ID])
	require.Equal(t, map[string][]string{
		"claude-fable-5-nc": {"anthropic/claude-fable-5"},
		"claude-opus-5":     {"anthropic/claude-opus-5"},
		"claude-sonnet-5":   {"anthropic/claude-sonnet-5"},
		"claude-haiku-4-5":  {"anthropic/claude-haiku-4.5"},
		"claude-sonnet-4-6": {"anthropic/claude-sonnet-4.6"},
	}, zenmux.capabilities.Aliases)
	require.Empty(t, providerAliases(zenmux.ID, CatalogEntry{ID: "gpt-image-2"}))

	tabcode, ok := definition("tabcode")
	require.True(t, ok)
	require.Equal(t, []string{"chat"}, tabcode.capabilities.ProfileIDs)
	require.Equal(t, constant.ChannelTypeAnthropic, tabcode.capabilities.ChannelType)
	require.Equal(t, int64(550), profiles[0].Priority[tabcode.ID])
	require.Equal(t, []string{"claude-sonnet-4-6"}, providerAliases(tabcode.ID, CatalogEntry{ID: "claude-sonnet-4-6"}))
	require.Empty(t, providerAliases(tabcode.ID, CatalogEntry{ID: "claude-sonnet-5"}))

	encoded, err := common.Marshal(ProviderStatus{Definition: zenmux})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "capabilities")
	require.NotContains(t, string(encoded), "ProbeModel")
}

func TestConfigureClaudeProvidersCreatesOneDisabledAnthropicChannelIdempotently(t *testing.T) {
	tests := []struct {
		providerID     string
		priority       int64
		headerOverride string
	}{
		{providerID: "zenmux", priority: 600, headerOverride: `{"anthropic-version":"2023-06-01"}`},
		{providerID: "tabcode", priority: 550, headerOverride: `{"Authorization":"Bearer {api_key}","anthropic-version":"2023-06-01"}`},
	}
	for _, test := range tests {
		t.Run(test.providerID, func(t *testing.T) {
			setupProviderDB(t)
			require.NoError(t, Configure(test.providerID, "first-api-key"))
			require.NoError(t, model.DB.Model(&model.Channel{}).
				Where("tag = ?", providerTag(test.providerID, "chat")).
				Updates(map[string]any{
					"header_override": `{"Authorization":"stale"}`,
					"test_model":      "wrong-model",
				}).Error)
			require.NoError(t, Configure(test.providerID, "second-api-key"))

			channels, err := providerChannels(test.providerID, true)
			require.NoError(t, err)
			require.Len(t, channels, 1)
			channel := channels[0]
			require.Equal(t, providerTag(test.providerID, "chat"), channel.GetTag())
			require.Equal(t, constant.ChannelTypeAnthropic, channel.Type)
			require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
			require.Equal(t, test.priority, channel.GetPriority())
			require.Equal(t, "second-api-key", channel.Key)
			require.Equal(t, test.headerOverride, pointerValue(channel.HeaderOverride))
			require.Equal(t, "claude-sonnet-4-6", pointerValue(channel.TestModel))
			require.NotContains(t, channel.GetTag(), ":nano")
			require.NotContains(t, channel.GetTag(), ":gpt-image")
		})
	}
}

func TestParseZenMuxSnapshotNormalizesExactFlatUSDPrices(t *testing.T) {
	provider, ok := definition("zenmux")
	require.True(t, ok)
	body := zenMuxResponse(t, zenMuxModelDocument(
		"anthropic/claude-sonnet-4.6", 3, 15, 0.3, 3.75, 6,
	))

	snapshot, err := parseZenMuxSnapshot(provider, body)
	require.NoError(t, err)
	require.Equal(t, []string{"anthropic/claude-sonnet-4.6"}, snapshot.Models)
	require.Len(t, snapshot.Pricing.Items, 1)
	require.Equal(t, "sha256:", snapshot.Pricing.Version[:7])
	require.Len(t, snapshot.Pricing.Version, 71)

	item := snapshot.Pricing.Items[0]
	require.Empty(t, item.ValidationError)
	require.Equal(t, 3.0, item.InputPrice)
	require.Equal(t, 15.0, item.OutputPrice)
	require.InDelta(t, 0.1, item.CacheRatio, 1e-9)
	require.InDelta(t, 1.25, item.CreateCacheRatio, 1e-9)
	require.Contains(t, item.OriginalPricing, `"web_search"`)

	price, err := toChannelPrice(provider, "claude-sonnet-4-6", item.ModelName, item, snapshot.Pricing.Version)
	require.NoError(t, err)
	require.Equal(t, model.ChannelModelBillingToken, price.BillingType)
	require.Equal(t, 3.0, price.InputPrice)
	require.Equal(t, 15.0, price.OutputPrice)
	require.Contains(t, price.OriginalPricing, `"anthropic/claude-sonnet-4.6"`)
}

func TestNormalizeZenMuxPricingRejectsAmbiguousOrUnsupportedPrices(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string][]json.RawMessage)
		want   string
	}{
		{
			name: "conditional",
			mutate: func(prices map[string][]json.RawMessage) {
				prices["prompt"] = []json.RawMessage{json.RawMessage(`{"value":3,"unit":"perMTokens","currency":"USD","condition":{"context":"long"}}`)}
			},
			want: "条件或未知",
		},
		{
			name: "tiered",
			mutate: func(prices map[string][]json.RawMessage) {
				prices["prompt"] = append(prices["prompt"], prices["prompt"][0])
			},
			want: "唯一的无条件",
		},
		{
			name: "missing",
			mutate: func(prices map[string][]json.RawMessage) {
				delete(prices, "completion")
			},
			want: "completion",
		},
		{
			name: "currency",
			mutate: func(prices map[string][]json.RawMessage) {
				prices["prompt"] = []json.RawMessage{json.RawMessage(`{"value":3,"unit":"perMTokens","currency":"EUR"}`)}
			},
			want: "USD/perMTokens",
		},
		{
			name: "unit",
			mutate: func(prices map[string][]json.RawMessage) {
				prices["prompt"] = []json.RawMessage{json.RawMessage(`{"value":3,"unit":"perToken","currency":"USD"}`)}
			},
			want: "USD/perMTokens",
		},
		{
			name: "unrepresentable one hour cache",
			mutate: func(prices map[string][]json.RawMessage) {
				prices["input_cache_write_1_h"] = []json.RawMessage{json.RawMessage(`{"value":7,"unit":"perMTokens","currency":"USD"}`)}
			},
			want: "无法用当前计费模型表示",
		},
		{
			name: "extreme price",
			mutate: func(prices map[string][]json.RawMessage) {
				prices["completion"] = []json.RawMessage{json.RawMessage(fmt.Sprintf(
					`{"value":%v,"unit":"perMTokens","currency":"USD"}`,
					maxExternalTokenPriceUSDPerMillion+1,
				))}
			},
			want: "超过安全上限",
		},
		{
			name: "unsupported billable item",
			mutate: func(prices map[string][]json.RawMessage) {
				prices["internal_reasoning"] = []json.RawMessage{json.RawMessage(`{"value":1,"unit":"perMTokens","currency":"USD"}`)}
			},
			want: "未支持的计费项 internal_reasoning",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := validZenMuxModel("anthropic/claude-sonnet-4.6", 3, 15, 0.3, 3.75, 6)
			test.mutate(upstream.Pricings)
			var item pricingItem
			err := normalizeZenMuxPricing(upstream, &item)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestParseZenMuxSnapshotMarksDuplicateSelectedModelUnavailable(t *testing.T) {
	provider, _ := definition("zenmux")
	document := zenMuxModelDocument("anthropic/claude-sonnet-4.6", 3, 15, 0.3, 3.75, 6)
	documentJSON, err := common.Marshal(document)
	require.NoError(t, err)

	snapshot, err := parseZenMuxSnapshot(provider, zenMuxResponse(t, document, document))
	require.NoError(t, err)
	require.Len(t, snapshot.Models, 1)
	require.Len(t, snapshot.Pricing.Items, 1)
	require.Contains(t, snapshot.Pricing.Items[0].ValidationError, "重复模型 ID")
	var evidence []json.RawMessage
	require.NoError(t, common.Unmarshal([]byte(snapshot.Pricing.Items[0].OriginalPricing), &evidence))
	require.Len(t, evidence, 2)
	require.JSONEq(t, string(documentJSON), string(evidence[0]))
	require.JSONEq(t, string(documentJSON), string(evidence[1]))
}

func TestParseZenMuxSnapshotRetainsMalformedTargetPricingEvidence(t *testing.T) {
	provider, _ := definition("zenmux")
	raw := json.RawMessage(`{"id":"anthropic/claude-sonnet-4.6","pricings":{"prompt":"invalid"}}`)

	snapshot, err := parseZenMuxSnapshot(provider, zenMuxResponse(t, raw))
	require.NoError(t, err)
	require.Equal(t, []string{"anthropic/claude-sonnet-4.6"}, snapshot.Models)
	require.Len(t, snapshot.Pricing.Items, 1)
	require.Contains(t, snapshot.Pricing.Items[0].ValidationError, "价格格式无效")
	require.JSONEq(t, string(raw), snapshot.Pricing.Items[0].OriginalPricing)
}

func TestParseZenMuxSnapshotRejectsIncompletePagination(t *testing.T) {
	provider, _ := definition("zenmux")
	body := []byte(`{"data":[{"id":"anthropic/claude-sonnet-4.6"}],"has_more":true}`)

	_, err := parseZenMuxSnapshot(provider, body)
	require.ErrorContains(t, err, "未处理的分页")
}

func TestZenMuxSyncPersistsExplicitUnavailableRowsForMissingSelectedModels(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, Configure("zenmux", "zenmux-test-key"))

	requestKey := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestKey <- r.Header.Get("x-api-key")
		require.Equal(t, nativeMessagesVersion, r.Header.Get("anthropic-version"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(zenMuxResponse(t, zenMuxModelDocument(
			"anthropic/claude-sonnet-4.6", 3, 15, 0.3, 3.75, 6,
		)))
	}))
	t.Cleanup(server.Close)
	overrideProviderDefinition(t, "zenmux", func(provider *Definition) {
		provider.PricingURL = server.URL
	})

	count, err := Sync("zenmux")
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, "zenmux-test-key", <-requestKey)

	channels, err := providerChannels("zenmux", true)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	channel := channels[0]
	require.Equal(t, "claude-sonnet-4-6", channel.Models)
	require.JSONEq(t, `{"claude-sonnet-4-6":"anthropic/claude-sonnet-4.6"}`, channel.GetModelMapping())

	var prices []model.ChannelModelPrice
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Order("catalog_id").Find(&prices).Error)
	require.Len(t, prices, 5)
	pendingAuthentication := 0
	unavailable := 0
	for _, price := range prices {
		require.False(t, price.Available)
		require.Zero(t, price.TestedAt)
		if price.BillingType != "unavailable" {
			pendingAuthentication++
			require.Equal(t, "claude-sonnet-4-6", price.CatalogID)
			require.Equal(t, "anthropic/claude-sonnet-4.6", price.UpstreamModelID)
			require.NotEmpty(t, price.OriginalPricing)
			continue
		}
		unavailable++
		require.Equal(t, "unavailable", price.BillingType)
		require.Contains(t, price.LastError, "实时模型列表未找到候选 ID")
	}
	require.Equal(t, 1, pendingAuthentication)
	require.Equal(t, 4, unavailable)
}

func TestParseTabCodeSnapshotValidatesKiroAndCalculatesPinnedPrice(t *testing.T) {
	provider, ok := definition("tabcode")
	require.True(t, ok)
	body := []byte(`[
		{"provider":"claude","name":"office","path":"https://api2.tabcode.cc/claude/office","multiplier":1.875},
		{"provider":"claude","name":"kiro逆向","displayName":"Kiro逆向","path":"https://api2.tabcode.cc/claude/kiropower","multiplier":0.75}
	]`)

	snapshot, err := parseTabCodeSnapshot(provider, body)
	require.NoError(t, err)
	require.Equal(t, tabCodeKiroBaseURL, snapshot.ResolvedBaseURL)
	require.Equal(t, []string{"claude-sonnet-4-6"}, snapshot.Models)
	require.Len(t, snapshot.Pricing.Items, 1)
	require.Len(t, snapshot.Pricing.Version, 71)

	item := snapshot.Pricing.Items[0]
	require.Equal(t, 2.25, item.InputPrice)
	require.Equal(t, 11.25, item.OutputPrice)
	require.InDelta(t, 0.225, item.InputPrice*item.CacheRatio, 1e-9)
	require.InDelta(t, 2.8125, item.InputPrice*item.CreateCacheRatio, 1e-9)
	require.InDelta(t, 4.5, item.InputPrice*item.CreateCacheRatio*1.6, 1e-9)
	require.Contains(t, item.OriginalPricing, tabCodeKiroBaseURL)
	require.Contains(t, item.OriginalPricing, anthropicPricingURL)
	require.Contains(t, item.OriginalPricing, anthropicSonnet46PriceDate)

	price, err := toChannelPrice(provider, "claude-sonnet-4-6", "claude-sonnet-4-6", item, snapshot.Pricing.Version)
	require.NoError(t, err)
	require.Equal(t, 2.25, price.InputPrice)
	require.Equal(t, 11.25, price.OutputPrice)
	require.InDelta(t, 0.1, price.CacheRatio, 1e-9)
	require.InDelta(t, 1.25, price.CacheCreationRatio, 1e-9)
}

func TestTabCodeSyncPersistsOnlySonnet46WithBearerOverride(t *testing.T) {
	setupProviderDB(t)
	require.NoError(t, Configure("tabcode", "tabcode-test-key"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`[{"provider":"claude","path":"https://api2.tabcode.cc/claude/kiropower","multiplier":0.75}]`))
	}))
	t.Cleanup(server.Close)
	overrideProviderDefinition(t, "tabcode", func(provider *Definition) {
		provider.PricingURL = server.URL
	})

	count, err := Sync("tabcode")
	require.NoError(t, err)
	require.Equal(t, 1, count)

	channels, err := providerChannels("tabcode", true)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	channel := channels[0]
	require.Equal(t, tabCodeKiroBaseURL, channel.GetBaseURL())
	require.Equal(t, "claude-sonnet-4-6", channel.Models)
	require.JSONEq(t, `{"claude-sonnet-4-6":"claude-sonnet-4-6"}`, channel.GetModelMapping())
	require.Equal(t, `{"Authorization":"Bearer {api_key}","anthropic-version":"2023-06-01"}`, pointerValue(channel.HeaderOverride))

	var prices []model.ChannelModelPrice
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&prices).Error)
	require.Len(t, prices, 1)
	require.False(t, prices[0].Available)
	require.Zero(t, prices[0].TestedAt)
	require.Equal(t, 2.25, prices[0].InputPrice)
	require.Equal(t, 11.25, prices[0].OutputPrice)
	require.Contains(t, prices[0].OriginalPricing, anthropicPricingURL)
}

func TestTabCodeKiroURLAndMultiplierRejectUnsafeSource(t *testing.T) {
	invalidURLs := []string{
		"http://api2.tabcode.cc/claude/kiropower",
		"https://evil.example/claude/kiropower",
		"https://api2.tabcode.cc/claude/kiropower/",
		"https://api2.tabcode.cc/claude/kiropower?target=evil",
		"https://api2.tabcode.cc/claude/kiropower?",
		"https://user@api2.tabcode.cc/claude/kiropower",
	}
	for _, raw := range invalidURLs {
		require.Error(t, validateTabCodeKiroURL(raw), raw)
	}
	require.NoError(t, validateTabCodeKiroURL(tabCodeKiroBaseURL))

	provider, _ := definition("tabcode")
	for _, multiplier := range []float64{0, -1} {
		body := []byte(fmt.Sprintf(`[{"provider":"claude","path":"%s","multiplier":%v}]`, tabCodeKiroBaseURL, multiplier))
		_, err := parseTabCodeSnapshot(provider, body)
		require.ErrorContains(t, err, "倍率无效")
	}
	overflow := []byte(fmt.Sprintf(`[{"provider":"claude","path":"%s","multiplier":1e308}]`, tabCodeKiroBaseURL))
	_, err := parseTabCodeSnapshot(provider, overflow)
	require.ErrorContains(t, err, "超过安全上限")

	extreme := []byte(fmt.Sprintf(`[{"provider":"claude","path":"%s","multiplier":%v}]`,
		tabCodeKiroBaseURL, maxTabCodeMultiplier+0.01))
	_, err = parseTabCodeSnapshot(provider, extreme)
	require.ErrorContains(t, err, "超过安全上限")

	duplicate := []byte(fmt.Sprintf(`[
		{"provider":"claude","path":"%s","multiplier":0.75},
		{"provider":"claude","path":"%s","multiplier":0.75}
	]`, tabCodeKiroBaseURL, tabCodeKiroBaseURL))
	_, err = parseTabCodeSnapshot(provider, duplicate)
	require.ErrorContains(t, err, "多个 Kiro")
}

func TestNativeMessagesProbeUsesProviderAuthAndValidatesResponse(t *testing.T) {
	tests := []struct {
		providerID     string
		wantAPIKey     string
		wantAuth       string
		wantProbeModel string
	}{
		{providerID: "zenmux", wantAPIKey: "probe-secret", wantProbeModel: "anthropic/claude-sonnet-4.6"},
		{providerID: "tabcode", wantAuth: "Bearer probe-secret", wantProbeModel: "claude-sonnet-4-6"},
	}
	for _, test := range tests {
		t.Run(test.providerID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/messages", r.URL.Path)
				require.Equal(t, test.wantAPIKey, r.Header.Get("x-api-key"))
				require.Equal(t, test.wantAuth, r.Header.Get("Authorization"))
				require.Equal(t, nativeMessagesVersion, r.Header.Get("anthropic-version"))
				var requestBody struct {
					Model     string `json:"model"`
					MaxTokens int    `json:"max_tokens"`
				}
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, common.Unmarshal(body, &requestBody))
				require.Equal(t, test.wantProbeModel, requestBody.Model)
				require.Equal(t, 1, requestBody.MaxTokens)
				_, _ = fmt.Fprintf(w, `{"model":%q,"content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":4,"output_tokens":1}}`, test.wantProbeModel)
			}))
			t.Cleanup(server.Close)
			provider, _ := definition(test.providerID)
			provider.BaseURL = server.URL
			require.NoError(t, probeNativeMessages(t.Context(), provider, "probe-secret"))
		})
	}
}

func TestTabCodeConnectionTestUsesOnlyAuthenticatedNativeMessages(t *testing.T) {
	provider, _ := definition("tabcode")
	previousClient := http.DefaultClient
	requestCount := 0
	http.DefaultClient = &http.Client{Transport: providerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestCount++
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "https://api2.tabcode.cc/claude/kiropower/v1/messages", request.URL.String())
		require.Equal(t, "Bearer probe-secret", request.Header.Get("Authorization"))
		require.Empty(t, request.Header.Get("x-api-key"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":4,"output_tokens":1}}`,
			)),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { http.DefaultClient = previousClient })

	models, err := testProviderConnection(t.Context(), provider, "probe-secret")

	require.NoError(t, err)
	require.Equal(t, 1, requestCount, "the connection test must not use an anonymous catalog request")
	require.Equal(t, []string{"claude-sonnet-4-6"}, models)
}

func TestClaudeProviderActivationRequiresSyncThenAuthenticatedTest(t *testing.T) {
	setupProviderDB(t)
	failProbe := false
	probeRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write(zenMuxResponse(t, zenMuxModelDocument(
				"anthropic/claude-sonnet-4.6", 3, 15, 0.3, 3.75, 6,
			)))
		case http.MethodPost:
			probeRequests++
			if failProbe {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("secret response body"))
				return
			}
			_, _ = w.Write([]byte(`{"model":"anthropic/claude-sonnet-4.6","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":4,"output_tokens":1}}`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	overrideProviderDefinition(t, "zenmux", func(provider *Definition) {
		provider.BaseURL = server.URL
		provider.PricingURL = server.URL + "/v1/models"
	})

	require.NoError(t, Configure("zenmux", "initial-api-key"))
	count, err := Sync("zenmux")
	require.NoError(t, err)
	require.Equal(t, 1, count)

	channel := loadOnlyProviderChannel(t, "zenmux")
	require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
	require.Zero(t, channel.TestTime)
	price := loadProviderPrice(t, channel.Id, "claude-sonnet-4-6")
	require.Greater(t, price.SyncedAt, int64(0))
	require.Zero(t, price.TestedAt)
	require.NoError(t, model.DB.Model(&model.ChannelModelPrice{}).
		Where("channel_id = ? AND catalog_id = ?", channel.Id, "claude-opus-5").
		Updates(map[string]any{
			"upstream_model_id": "anthropic/not-returned", "billing_type": model.ChannelModelBillingToken,
			"input_price": 1, "output_price": 1, "available": true,
		}).Error)
	require.ErrorContains(t, SetEnabled("zenmux", true), "鉴权测试")

	modelCount, _, err := Test("zenmux")
	require.NoError(t, err)
	require.Equal(t, 5, modelCount)
	channel = loadOnlyProviderChannel(t, "zenmux")
	price = loadProviderPrice(t, channel.Id, "claude-sonnet-4-6")
	require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
	require.GreaterOrEqual(t, channel.TestTime, price.SyncedAt)
	require.GreaterOrEqual(t, price.TestedAt, price.SyncedAt)
	require.True(t, price.Available)
	unmatchedPrice := loadProviderPrice(t, channel.Id, "claude-opus-5")
	require.False(t, unmatchedPrice.Available)
	require.Zero(t, unmatchedPrice.TestedAt)
	require.Contains(t, unmatchedPrice.LastError, "未返回")

	require.NoError(t, model.DB.Model(&model.ChannelModelPrice{}).
		Where("id = ?", price.ID).Update("available", false).Error)
	require.ErrorContains(t, SetEnabled("zenmux", true), "没有经过鉴权测试的可用价格")
	_, _, err = Test("zenmux")
	require.NoError(t, err)
	price = loadProviderPrice(t, channel.Id, "claude-sonnet-4-6")

	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).
		Update("test_time", price.SyncedAt-1).Error)
	require.ErrorContains(t, SetEnabled("zenmux", true), "最新价格同步后")
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).
		Update("test_time", price.TestedAt).Error)
	require.NoError(t, SetEnabled("zenmux", true))
	channel = loadOnlyProviderChannel(t, "zenmux")
	require.Equal(t, common.ChannelStatusEnabled, channel.Status)
	var enabledAbilities int64
	require.NoError(t, model.DB.Model(&model.Ability{}).
		Where("channel_id = ? AND enabled = ?", channel.Id, true).Count(&enabledAbilities).Error)
	require.Greater(t, enabledAbilities, int64(0))
	require.NoError(t, Configure("zenmux", "initial-api-key"))
	channel = loadOnlyProviderChannel(t, "zenmux")
	require.Equal(t, common.ChannelStatusEnabled, channel.Status)
	require.Greater(t, channel.TestTime, int64(0))

	failProbe = true
	_, _, err = Test("zenmux")
	require.ErrorContains(t, err, "HTTP 401")
	channel = loadOnlyProviderChannel(t, "zenmux")
	price = loadProviderPrice(t, channel.Id, "claude-sonnet-4-6")
	require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
	require.Zero(t, channel.TestTime)
	require.Equal(t, model.ChannelModelBillingToken, price.BillingType)
	require.False(t, price.Available)
	require.Zero(t, price.TestedAt)
	require.Contains(t, price.LastError, "HTTP 401")
	requireProviderAbilitiesEmpty(t, channel.Id)

	failProbe = false
	_, _, err = Test("zenmux")
	require.NoError(t, err)
	channel = loadOnlyProviderChannel(t, "zenmux")
	price = loadProviderPrice(t, channel.Id, "claude-sonnet-4-6")
	require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
	require.True(t, price.Available)
	require.NoError(t, SetEnabled("zenmux", true))

	require.NoError(t, Configure("zenmux", "rotated-api-key"))
	channel = loadOnlyProviderChannel(t, "zenmux")
	price = loadProviderPrice(t, channel.Id, "claude-sonnet-4-6")
	require.Equal(t, "rotated-api-key", channel.Key)
	require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
	require.Zero(t, channel.TestTime)
	require.Equal(t, "unavailable", price.BillingType)
	require.False(t, price.Available)
	require.Zero(t, price.TestedAt)
	requireProviderAbilitiesEmpty(t, channel.Id)
	requestsBeforePreflight := probeRequests
	require.ErrorContains(t, SetEnabled("zenmux", true), "鉴权测试")
	_, _, err = Test("zenmux")
	require.ErrorContains(t, err, "请先重新同步")
	require.Equal(t, requestsBeforePreflight, probeRequests, "an invalidated snapshot must fail before the paid probe")
	price = loadProviderPrice(t, channel.Id, "claude-sonnet-4-6")
	require.Equal(t, "unavailable", price.BillingType)
	require.False(t, price.Available)
}

func TestClaudeProviderSyncFailureInvalidatesStaleSnapshot(t *testing.T) {
	setupProviderDB(t)
	failSync := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if failSync {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("sensitive upstream failure"))
				return
			}
			_, _ = w.Write(zenMuxResponse(t, zenMuxModelDocument(
				"anthropic/claude-sonnet-4.6", 3, 15, 0.3, 3.75, 6,
			)))
			return
		}
		_, _ = w.Write([]byte(`{"model":"anthropic/claude-sonnet-4.6","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":4,"output_tokens":1}}`))
	}))
	t.Cleanup(server.Close)
	overrideProviderDefinition(t, "zenmux", func(provider *Definition) {
		provider.BaseURL = server.URL
		provider.PricingURL = server.URL + "/v1/models"
	})

	require.NoError(t, Configure("zenmux", "zenmux-test-key"))
	_, err := Sync("zenmux")
	require.NoError(t, err)
	_, _, err = Test("zenmux")
	require.NoError(t, err)
	require.NoError(t, SetEnabled("zenmux", true))

	failSync = true
	_, err = Sync("zenmux")
	require.ErrorContains(t, err, "HTTP 502")
	require.NotContains(t, err.Error(), "sensitive upstream failure")
	channel := loadOnlyProviderChannel(t, "zenmux")
	require.Equal(t, common.ChannelStatusManuallyDisabled, channel.Status)
	require.Zero(t, channel.TestTime)
	requireProviderAbilitiesEmpty(t, channel.Id)
	var prices []model.ChannelModelPrice
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Find(&prices).Error)
	require.Len(t, prices, 5)
	for _, price := range prices {
		require.Equal(t, "unavailable", price.BillingType)
		require.False(t, price.Available)
		require.Zero(t, price.TestedAt)
		require.Contains(t, price.LastError, "HTTP 502")
	}
	require.Error(t, SetEnabled("zenmux", true))
}

func TestNativeMessagesProbeRedactsFailuresAndRejectsInvalidUsage(t *testing.T) {
	provider, _ := definition("zenmux")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("leaked-secret-body"))
	}))
	provider.BaseURL = server.URL
	err := probeNativeMessages(t.Context(), provider, "leaked-secret-key")
	server.Close()
	require.ErrorContains(t, err, "HTTP 401")
	require.NotContains(t, err.Error(), "leaked-secret")

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"anthropic/claude-sonnet-4.6","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":-1,"output_tokens":1}}`))
	}))
	t.Cleanup(server.Close)
	provider.BaseURL = server.URL
	err = probeNativeMessages(t.Context(), provider, "another-secret-key")
	require.ErrorContains(t, err, "无效 usage")

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"BROKEN"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(server.Close)
	provider.BaseURL = server.URL
	err = probeNativeMessages(t.Context(), provider, "another-secret-key")
	require.ErrorContains(t, err, "响应模型与测试模型不一致")

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":""}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(server.Close)
	provider.BaseURL = server.URL
	err = probeNativeMessages(t.Context(), provider, "another-secret-key")
	require.ErrorContains(t, err, "未包含文本内容")
}

func TestProviderResponseLimitRejectsOversizedBody(t *testing.T) {
	_, err := readLimitedBody(strings.NewReader(strings.Repeat("x", maxProviderResponseBytes+1)))
	require.ErrorContains(t, err, "超过大小限制")
}

func validZenMuxModel(id string, prompt, completion, cacheRead, cacheWrite5m, cacheWrite1h float64) zenMuxModel {
	price := func(value float64) []json.RawMessage {
		return []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"value":%v,"unit":"perMTokens","currency":"USD"}`, value))}
	}
	return zenMuxModel{
		ID: id,
		Pricings: map[string][]json.RawMessage{
			"prompt":                  price(prompt),
			"completion":              price(completion),
			"input_cache_read":        price(cacheRead),
			"input_cache_write_5_min": price(cacheWrite5m),
			"input_cache_write_1_h":   price(cacheWrite1h),
			"web_search":              {json.RawMessage(`{"value":0.01,"unit":"perCount","currency":"USD"}`)},
		},
	}
}

func zenMuxModelDocument(id string, prompt, completion, cacheRead, cacheWrite5m, cacheWrite1h float64) map[string]any {
	model := validZenMuxModel(id, prompt, completion, cacheRead, cacheWrite5m, cacheWrite1h)
	return map[string]any{"id": model.ID, "pricings": model.Pricings}
}

func zenMuxResponse(t *testing.T, documents ...any) []byte {
	t.Helper()
	body, err := common.Marshal(map[string]any{"data": documents})
	require.NoError(t, err)
	return body
}

func overrideProviderDefinition(t *testing.T, providerID string, update func(*Definition)) {
	t.Helper()
	for index := range definitions {
		if definitions[index].ID != providerID {
			continue
		}
		original := definitions[index]
		update(&definitions[index])
		t.Cleanup(func() { definitions[index] = original })
		return
	}
	t.Fatalf("provider definition %q not found", providerID)
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func loadOnlyProviderChannel(t *testing.T, providerID string) model.Channel {
	t.Helper()
	channels, err := providerChannels(providerID, true)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	return channels[0]
}

func loadProviderPrice(t *testing.T, channelID int, catalogID string) model.ChannelModelPrice {
	t.Helper()
	var price model.ChannelModelPrice
	require.NoError(t, model.DB.Where("channel_id = ? AND catalog_id = ?", channelID, catalogID).First(&price).Error)
	return price
}

func requireProviderAbilitiesEmpty(t *testing.T, channelID int) {
	t.Helper()
	var abilities int64
	require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", channelID).Count(&abilities).Error)
	require.Zero(t, abilities)
}
