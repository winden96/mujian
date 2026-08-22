package mujianprovider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupProviderDB(t *testing.T) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.ChannelModelPrice{}))
	model.DB = db
}

func TestCatalogReasoningProtocols(t *testing.T) {
	tests := []struct {
		modelID  string
		protocol string
	}{
		{modelID: "deepseek-v4-flash", protocol: ReasoningProtocolOpenAIEffort},
		{modelID: "gpt-5.6-sol", protocol: ReasoningProtocolOpenAIEffort},
		{modelID: "claude-opus-5", protocol: ReasoningProtocolClaudeAdaptive},
		{modelID: "grok-4.5", protocol: ReasoningProtocolOpenAIEffort},
		{modelID: "gpt-5.6-terra"},
		{modelID: "missing-model"},
	}
	for _, test := range tests {
		t.Run(test.modelID, func(t *testing.T) {
			require.Equal(t, test.protocol, ModelReasoningProtocol(test.modelID))
			require.Equal(t, test.protocol != "", SupportsReasoning(test.modelID))
		})
	}

	for _, entry := range Catalog() {
		require.Equal(t, entry.ReasoningProtocol != "", entry.SupportsReasoning, entry.ID)
	}
}

func TestResolveProviderUsesStatusServerAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"server_address":"https://api.example.com/"}}`))
	}))
	t.Cleanup(server.Close)

	provider, err := resolveProvider(t.Context(), Definition{Name: "GeekNow", StatusURL: server.URL})

	require.NoError(t, err)
	require.Equal(t, "https://api.example.com", provider.BaseURL)
	require.Equal(t, "https://api.example.com/api/pricing", provider.PricingURL)
}

func TestMatchCatalogEntryRequiresImageGenerationEndpoint(t *testing.T) {
	entry := CatalogEntry{ID: "gpt-image-2", Kind: "image"}
	models := map[string]struct{}{"gpt-image-2": {}, "image2": {}}
	pricing := map[string]pricingItem{
		"gpt-image-2": {ModelName: "gpt-image-2", QuotaType: 1, ModelPrice: 0.05, SupportedEndpoints: []string{"openai"}},
		"image2":      {ModelName: "image2", QuotaType: 1, ModelPrice: 0.05, SupportedEndpoints: []string{"openai", "image-generation"}},
	}

	upstreamID, _, reason := matchCatalogEntry("zex", entry, models, pricing)
	require.Empty(t, reason)
	require.Equal(t, "image2", upstreamID)
}

func TestMatchCatalogEntryResolvesClaudeAlias(t *testing.T) {
	entry := CatalogEntry{ID: "claude-fable-5-nc", Kind: "chat"}
	models := map[string]struct{}{"claude-fable-5": {}}
	pricing := map[string]pricingItem{
		"claude-fable-5": {ModelName: "claude-fable-5", ModelRatio: 5, CompletionRatio: 5},
	}

	upstreamID, _, reason := matchCatalogEntry("yunwu", entry, models, pricing)
	require.Empty(t, reason)
	require.Equal(t, "claude-fable-5", upstreamID)
}

func TestMatchCatalogEntryAllowsZexNativeGeminiImages(t *testing.T) {
	entry := CatalogEntry{ID: "nano-banana-2", Kind: "image"}
	models := map[string]struct{}{"gemini-3.1-flash-image-preview": {}}
	pricing := map[string]pricingItem{
		"gemini-3.1-flash-image-preview": {
			ModelName: "gemini-3.1-flash-image-preview", QuotaType: 1, ModelPrice: 0.12,
			SupportedEndpoints: []string{"gemini", "openai"},
		},
	}

	upstreamID, _, reason := matchCatalogEntry("zex", entry, models, pricing)
	require.Empty(t, reason)
	require.Equal(t, "gemini-3.1-flash-image-preview", upstreamID)
}

func TestMatchCatalogEntryRejectsZexNanoThatCannotUseGeminiChannel(t *testing.T) {
	entry := CatalogEntry{ID: "nano-banana", Kind: "image"}
	models := map[string]struct{}{"nano-banana": {}}
	pricing := map[string]pricingItem{
		"nano-banana": {
			ModelName: "nano-banana", QuotaType: 1, ModelPrice: 0.06,
			SupportedEndpoints: []string{"image-generation"},
		},
	}

	_, _, reason := matchCatalogEntry("zex", entry, models, pricing)
	require.Equal(t, "上游接口不兼容工作台图像生成协议", reason)
}

func TestMatchCatalogEntryExplainsMissingPrice(t *testing.T) {
	entry := CatalogEntry{ID: "grok-4.5", Kind: "chat"}
	models := map[string]struct{}{"grok-4.5": {}}

	_, _, reason := matchCatalogEntry("yunwu", entry, models, map[string]pricingItem{})

	require.Equal(t, "价格接口没有该上游模型的明确价格", reason)
}

func TestResolvePricingUsesVerifiedGeekNowNanoBananaSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	snapshot, err := resolvePricing(t.Context(), Definition{
		ID: "geeknow", PricingURL: server.URL,
	}, "relay-key")

	require.NoError(t, err)
	require.True(t, snapshot.Partial)
	require.Equal(t, "https://api.geeknow.ai/", snapshot.SourceURL)
	require.Equal(t, "verified-2026-08-13", snapshot.Version)
	require.Len(t, snapshot.Items, 1)
	require.Equal(t, "gemini-2.5-flash-image", snapshot.Items[0].ModelName)
	require.Equal(t, 0.06, snapshot.Items[0].ModelPrice)
	require.Contains(t, snapshot.Items[0].SupportedEndpoints, "image-generation")
}

func TestResolvePricingPrefersLiveGeekNowPricingWhenAvailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"pricing_version":"live-version","data":[{"model_name":"gemini-2.5-flash-image","quota_type":1,"model_price":0.07,"supported_endpoint_types":["image-generation"]}]}`))
	}))
	t.Cleanup(server.Close)

	snapshot, err := resolvePricing(t.Context(), Definition{
		ID: "geeknow", PricingURL: server.URL,
	}, "relay-key")

	require.NoError(t, err)
	require.False(t, snapshot.Partial)
	require.Equal(t, server.URL, snapshot.SourceURL)
	require.Equal(t, "live-version", snapshot.Version)
	require.Equal(t, 0.07, snapshot.Items[0].ModelPrice)
}

func TestPartialPricingOnlyTouchesCatalogEntriesWithVerifiedCandidates(t *testing.T) {
	pricing := map[string]pricingItem{
		"gemini-2.5-flash-image": {ModelName: "gemini-2.5-flash-image"},
	}

	require.True(t, hasPricedCandidate(CatalogEntry{ID: "nano-banana"}, pricing))
	require.False(t, hasPricedCandidate(CatalogEntry{ID: "nano-banana-pro"}, pricing))
	require.False(t, hasPricedCandidate(CatalogEntry{ID: "deepseek-v4-flash"}, pricing))
}

func TestToChannelPriceNormalizesNewAPIRatios(t *testing.T) {
	provider := definitions[0]
	price, err := toChannelPrice(provider, "deepseek-v4-flash", "deepseek-v4-flash", pricingItem{
		ModelRatio: 0.5, CompletionRatio: 2, CacheRatio: 0.1,
	}, "version-1")

	require.NoError(t, err)
	require.Equal(t, model.ChannelModelBillingToken, price.BillingType)
	require.Equal(t, 1.0, price.InputPrice)
	require.Equal(t, 2.0, price.OutputPrice)
	require.Equal(t, "version-1", price.SourceVersion)
}

func TestToChannelPriceNormalizesFixedPrice(t *testing.T) {
	provider := definitions[0]
	price, err := toChannelPrice(provider, "gpt-image-2", "image2", pricingItem{
		QuotaType: 1, ModelPrice: 0.05,
	}, "version-2")

	require.NoError(t, err)
	require.Equal(t, model.ChannelModelBillingFixed, price.BillingType)
	require.Equal(t, 0.05, price.FixedPrice)
}

func TestReferenceCapabilityRequiresExplicitEditEndpointForGPT(t *testing.T) {
	provider := Definition{ID: "yunwu", PricingURL: "https://example.com/pricing"}
	price, err := toChannelPrice(provider, "gpt-image-2", "gpt-image-2", pricingItem{
		QuotaType: 1, ModelPrice: 0.05,
		SupportedEndpoints: []string{"openai", "image-generation", "openai编辑图片"},
	}, "version")

	require.NoError(t, err)
	require.Equal(t, ReferenceProtocolOpenAIEditMultipart, price.ReferenceProtocol)
	require.Equal(t, MaxReferenceImages, price.MaxReferenceImages)

	price, err = toChannelPrice(Definition{ID: "zex"}, "gpt-image-2", "gpt-image-2", pricingItem{
		QuotaType: 1, ModelPrice: 0.05, SupportedEndpoints: []string{"openai", "image-generation"},
	}, "version")
	require.NoError(t, err)
	require.Empty(t, price.ReferenceProtocol)
	require.Zero(t, price.MaxReferenceImages)
}

func TestReferenceCapabilityAllowsOnlyNativeGeminiNano(t *testing.T) {
	price, err := toChannelPrice(Definition{ID: "zex"}, "nano-banana-2", "gemini-3.1-flash-image-preview", pricingItem{
		QuotaType: 1, ModelPrice: 0.12, SupportedEndpoints: []string{"gemini", "openai"},
	}, "version")

	require.NoError(t, err)
	require.Equal(t, ReferenceProtocolGeminiInline, price.ReferenceProtocol)
	require.Equal(t, MaxReferenceImages, price.MaxReferenceImages)

	price, err = toChannelPrice(Definition{ID: "geeknow"}, "nano-banana", "gemini-2.5-flash-image", pricingItem{
		QuotaType: 1, ModelPrice: 0.06, SupportedEndpoints: []string{"image-generation"},
	}, "version")
	require.NoError(t, err)
	require.Empty(t, price.ReferenceProtocol)
}

func TestReferenceImageModelAvailableBackfillsOnlyExplicitLegacyCapability(t *testing.T) {
	setupProviderDB(t)
	priority := int64(100)
	channels := []model.Channel{
		{Id: 1, Key: "yunwu-key", Name: "Yunwu", Status: common.ChannelStatusEnabled, Priority: &priority},
		{Id: 2, Key: "zex-key", Name: "Zex", Status: common.ChannelStatusEnabled, Priority: &priority},
	}
	require.NoError(t, model.DB.Create(&channels).Error)
	prices := []model.ChannelModelPrice{
		{
			ChannelID: 1, CatalogID: "gpt-image-2", Provider: "yunwu", UpstreamModelID: "gpt-image-2",
			BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.05, Available: true,
			OriginalPricing: `{"supported_endpoint_types":["openai","openai编辑图片"]}`,
		},
		{
			ChannelID: 2, CatalogID: "nano-banana", Provider: "geeknow", UpstreamModelID: "gemini-2.5-flash-image",
			BillingType: model.ChannelModelBillingFixed, FixedPrice: 0.06, Available: true,
			OriginalPricing: `{"supported_endpoint_types":["image-generation"]}`,
		},
	}
	require.NoError(t, model.DB.Create(&prices).Error)

	available, err := ReferenceImageModelAvailable("gpt-image-2")
	require.NoError(t, err)
	require.True(t, available)
	var stored model.ChannelModelPrice
	require.NoError(t, model.DB.First(&stored, prices[0].ID).Error)
	require.Equal(t, ReferenceProtocolOpenAIEditMultipart, stored.ReferenceProtocol)
	require.Equal(t, MaxReferenceImages, stored.MaxReferenceImages)

	available, err = ReferenceImageModelAvailable("nano-banana")
	require.NoError(t, err)
	require.False(t, available)
}
