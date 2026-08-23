package mujianprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
)

const maxProviderResponseBytes = 16 << 20

const (
	ReferenceProtocolGeminiInline        = model.ChannelModelReferenceGeminiInline
	ReferenceProtocolOpenAIEditMultipart = model.ChannelModelReferenceOpenAIEditMultipart
	MaxReferenceImages                   = 3
)

type Definition struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SiteURL    string `json:"site_url"`
	BaseURL    string `json:"base_url"`
	PricingURL string `json:"pricing_url"`
	StatusURL  string `json:"-"`
}

type CatalogEntry struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	ProviderName      string   `json:"provider_name"`
	Kind              string   `json:"kind"`
	Tags              []string `json:"tags"`
	SupportsReasoning bool     `json:"supports_reasoning"`
	ReasoningProtocol string   `json:"reasoning_protocol,omitempty"`
}

const (
	ReasoningProtocolOpenAIEffort   = "openai_effort"
	ReasoningProtocolClaudeAdaptive = "claude_adaptive"
)

type ProviderStatus struct {
	Definition
	Configured bool   `json:"configured"`
	Enabled    bool   `json:"enabled"`
	KeyLast4   string `json:"key_last4,omitempty"`
	ModelCount int    `json:"model_count"`
	SyncedAt   int64  `json:"synced_at,omitempty"`
	TestedAt   int64  `json:"tested_at,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

type CatalogAvailability struct {
	CatalogEntry
	Available                  bool     `json:"available"`
	ChannelCount               int      `json:"channel_count"`
	Providers                  []string `json:"providers"`
	ReferenceAvailable         bool     `json:"reference_available"`
	ReferenceChannelCount      int      `json:"reference_channel_count"`
	ReferenceProtocols         []string `json:"reference_protocols,omitempty"`
	ReferenceUnavailableReason string   `json:"reference_unavailable_reason,omitempty"`
	BillingType                string   `json:"billing_type,omitempty"`
	MinInputPrice              float64  `json:"min_input_price"`
	MaxInputPrice              float64  `json:"max_input_price"`
	MinOutputPrice             float64  `json:"min_output_price"`
	MaxOutputPrice             float64  `json:"max_output_price"`
	MinFixedPrice              float64  `json:"min_fixed_price"`
	MaxFixedPrice              float64  `json:"max_fixed_price"`
	UnavailableReason          string   `json:"unavailable_reason,omitempty"`
}

var definitions = []Definition{
	{ID: "zex", Name: "ZexAPI", SiteURL: "https://zexapi.com/pricing", BaseURL: "https://zexapi.com", PricingURL: "https://zexapi.com/api/pricing"},
	{ID: "geeknow", Name: "GeekNow", SiteURL: "https://www.geeknow.top/pricing", BaseURL: "https://geeknow.ai", PricingURL: "https://geeknow.ai/api/pricing", StatusURL: "https://www.geeknow.top/api/status"},
	{ID: "yunwu", Name: "云雾 API", SiteURL: "https://yunwu.ai/", BaseURL: "https://yunwu.ai", PricingURL: "https://yunwu.ai/api/pricing"},
}

var catalog = []CatalogEntry{
	{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", ProviderName: "DeepSeek", Kind: "chat", Tags: []string{"对话", "推理"}, SupportsReasoning: true, ReasoningProtocol: ReasoningProtocolOpenAIEffort},
	{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol", ProviderName: "OpenAI", Kind: "chat", Tags: []string{"对话", "推理"}, SupportsReasoning: true, ReasoningProtocol: ReasoningProtocolOpenAIEffort},
	{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", ProviderName: "OpenAI", Kind: "chat", Tags: []string{"对话", "工具"}},
	{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna", ProviderName: "OpenAI", Kind: "chat", Tags: []string{"对话", "快速"}},
	{ID: "claude-fable-5-nc", Name: "Claude Fable 5", ProviderName: "Anthropic", Kind: "chat", Tags: []string{"对话", "长文本"}},
	{ID: "claude-opus-5", Name: "Claude Opus 5", ProviderName: "Anthropic", Kind: "chat", Tags: []string{"对话", "推理"}, SupportsReasoning: true, ReasoningProtocol: ReasoningProtocolClaudeAdaptive},
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", ProviderName: "Anthropic", Kind: "chat", Tags: []string{"对话", "创作"}},
	{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5", ProviderName: "Anthropic", Kind: "chat", Tags: []string{"对话", "快速"}},
	{ID: "grok-4.5", Name: "Grok 4.5", ProviderName: "xAI", Kind: "chat", Tags: []string{"对话", "推理"}, SupportsReasoning: true, ReasoningProtocol: ReasoningProtocolOpenAIEffort},
	{ID: "nano-banana", Name: "Nano Banana", ProviderName: "Google", Kind: "image", Tags: []string{"图像", "快速"}},
	{ID: "nano-banana-pro", Name: "Nano Banana Pro", ProviderName: "Google", Kind: "image", Tags: []string{"图像", "高质量"}},
	{ID: "nano-banana-2", Name: "Nano Banana 2", ProviderName: "Google", Kind: "image", Tags: []string{"图像", "多模态"}},
	{ID: "gpt-image-2", Name: "GPT Image 2", ProviderName: "OpenAI", Kind: "image", Tags: []string{"图像", "文字渲染"}},
}

var aliases = map[string][]string{
	"deepseek-v4-flash": {"deepseek-v4-flash"},
	"gpt-5.6-sol":       {"gpt-5.6-sol"},
	"gpt-5.6-terra":     {"gpt-5.6-terra"},
	"gpt-5.6-luna":      {"gpt-5.6-luna"},
	"claude-fable-5-nc": {"claude-fable-5-nc", "claude-fable-5"},
	"claude-opus-5":     {"claude-opus-5"},
	"claude-sonnet-5":   {"claude-sonnet-5"},
	"claude-haiku-4-5":  {"claude-haiku-4-5", "claude-haiku-4-5-20251001"},
	"grok-4.5":          {"grok-4.5"},
	"nano-banana":       {"nano-banana", "gemini-2.5-flash-image", "gemini-2.5-flash-image-preview"},
	"nano-banana-pro":   {"nano-banana-pro", "nano_banana_pro-1K", "gemini-3-pro-image-preview"},
	"nano-banana-2":     {"nano-banana-2", "nano_banana_2", "gemini-3.1-flash-image-preview"},
	"gpt-image-2":       {"gpt-image-2", "image2"},
}

type pricingItem struct {
	ModelName          string   `json:"model_name"`
	QuotaType          int      `json:"quota_type"`
	ModelRatio         float64  `json:"model_ratio"`
	ModelPrice         float64  `json:"model_price"`
	CompletionRatio    float64  `json:"completion_ratio"`
	CacheRatio         float64  `json:"cache_ratio"`
	CreateCacheRatio   float64  `json:"create_cache_ratio"`
	Available          *bool    `json:"available"`
	SupportedEndpoints []string `json:"supported_endpoint_types"`
}

type pricingResponse struct {
	Success        bool          `json:"success"`
	Data           []pricingItem `json:"data"`
	PricingVersion string        `json:"pricing_version"`
}

type pricingSnapshot struct {
	Items     []pricingItem
	SourceURL string
	Version   string
	Partial   bool
}

var verifiedPricingSnapshots = map[string]pricingSnapshot{
	"geeknow": {
		Items: []pricingItem{{
			ModelName: "gemini-2.5-flash-image", QuotaType: 1, ModelPrice: 0.06,
			SupportedEndpoints: []string{"image-generation"},
		}},
		SourceURL: "https://api.geeknow.ai/",
		Version:   "verified-2026-08-13",
		Partial:   true,
	},
}

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

type statusResponse struct {
	Data struct {
		ServerAddress string `json:"server_address"`
	} `json:"data"`
}

type routeProfile struct {
	ID       string
	Priority map[string]int64
}

var profiles = []routeProfile{
	{ID: "chat", Priority: map[string]int64{"yunwu": 300, "geeknow": 200, "zex": 100}},
	{ID: "nano", Priority: map[string]int64{"geeknow": 320, "zex": 220, "yunwu": 120}},
	{ID: "gpt-image", Priority: map[string]int64{"zex": 320, "yunwu": 220, "geeknow": 120}},
}

func Definitions() []Definition { return append([]Definition(nil), definitions...) }

func Catalog() []CatalogEntry {
	items := make([]CatalogEntry, len(catalog))
	copy(items, catalog)
	return items
}

func SupportsReasoning(modelID string) bool {
	return ModelReasoningProtocol(modelID) != ""
}

func ModelReasoningProtocol(modelID string) string {
	for _, entry := range catalog {
		if entry.ID == modelID {
			return entry.ReasoningProtocol
		}
	}
	return ""
}

func definition(providerID string) (Definition, bool) {
	for _, item := range definitions {
		if item.ID == providerID {
			return item, true
		}
	}
	return Definition{}, false
}

func profileID(entry CatalogEntry) string {
	if entry.Kind == "chat" {
		return "chat"
	}
	if entry.ID == "gpt-image-2" {
		return "gpt-image"
	}
	return "nano"
}

func providerTag(providerID, profileID string) string {
	return "mujian-provider:" + providerID + ":" + profileID
}

func providerChannels(providerID string, includeKey bool) ([]model.Channel, error) {
	channels := make([]model.Channel, 0, len(profiles))
	query := model.DB.Where("tag LIKE ?", "mujian-provider:"+providerID+":%")
	if !includeKey {
		query = query.Omit("key")
	}
	err := query.Order("id ASC").Find(&channels).Error
	return channels, err
}

func Statuses() ([]ProviderStatus, error) {
	statuses := make([]ProviderStatus, 0, len(definitions))
	for _, item := range definitions {
		channels, err := providerChannels(item.ID, true)
		if err != nil {
			return nil, err
		}
		status := ProviderStatus{Definition: item}
		providerNames := map[string]struct{}{}
		for _, channel := range channels {
			if channel.Key != "" {
				status.Configured = true
				status.KeyLast4 = lastFour(channel.Key)
			}
			if channel.Status == common.ChannelStatusEnabled {
				status.Enabled = true
			}
			if channel.TestTime > status.TestedAt {
				status.TestedAt = channel.TestTime
			}
			var prices []model.ChannelModelPrice
			if err = model.DB.Where("channel_id = ?", channel.Id).Find(&prices).Error; err != nil {
				return nil, err
			}
			for _, price := range prices {
				if price.Available {
					providerNames[price.CatalogID] = struct{}{}
				}
				if price.SyncedAt > status.SyncedAt {
					status.SyncedAt = price.SyncedAt
				}
				if price.LastError != "" {
					status.LastError = price.LastError
				}
			}
		}
		status.ModelCount = len(providerNames)
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func Configure(providerID, key string) error {
	provider, ok := definition(providerID)
	if !ok {
		return errors.New("未知供应商")
	}
	var err error
	provider, err = resolveProvider(context.Background(), provider)
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key != "" && len(key) < 8 {
		return errors.New("API Key 格式无效")
	}
	existing, err := providerChannels(providerID, true)
	if err != nil {
		return err
	}
	existingByTag := make(map[string]*model.Channel, len(existing))
	for index := range existing {
		channel := &existing[index]
		existingByTag[channel.GetTag()] = channel
	}
	if key == "" && len(existing) == 0 {
		return errors.New("首次配置必须填写 API Key")
	}
	for _, profile := range profiles {
		tag := providerTag(providerID, profile.ID)
		if channel := existingByTag[tag]; channel != nil {
			updates := map[string]any{
				"base_url": provider.BaseURL,
				"priority": profile.Priority[providerID],
				"type":     channelType(providerID, profile.ID),
			}
			if key != "" {
				updates["key"] = key
			}
			if err = model.DB.Model(channel).Updates(updates).Error; err != nil {
				return err
			}
			continue
		}
		priority := profile.Priority[providerID]
		weight := uint(100)
		autoBan := 1
		baseURL := provider.BaseURL
		modelMapping := "{}"
		channel := model.Channel{
			Type: channelType(providerID, profile.ID), Key: key, Status: common.ChannelStatusManuallyDisabled,
			Name: fmt.Sprintf("幕间 · %s · %s", provider.Name, profileName(profile.ID)), Weight: &weight,
			CreatedTime: common.GetTimestamp(), BaseURL: &baseURL, Models: "", Group: "default",
			ModelMapping: &modelMapping, Priority: &priority, AutoBan: &autoBan, Tag: &tag,
		}
		if err = channel.Insert(); err != nil {
			return err
		}
	}
	model.InitChannelCache()
	return nil
}

func SetEnabled(providerID string, enabled bool) error {
	channels, err := providerChannels(providerID, true)
	if err != nil {
		return err
	}
	if len(channels) == 0 {
		return errors.New("供应商尚未配置")
	}
	status := common.ChannelStatusManuallyDisabled
	if enabled {
		status = common.ChannelStatusEnabled
	}
	for index := range channels {
		channel := &channels[index]
		if enabled && strings.TrimSpace(channel.Models) == "" {
			continue
		}
		if err = model.DB.Model(channel).Update("status", status).Error; err != nil {
			return err
		}
		channel.Status = status
		if err = channel.UpdateAbilities(nil); err != nil {
			return err
		}
	}
	model.InitChannelCache()
	return nil
}

func Test(providerID string) (int, int, error) {
	provider, channels, key, err := configuredProvider(providerID)
	if err != nil {
		return 0, 0, err
	}
	started := time.Now()
	models, err := fetchModels(context.Background(), provider, key)
	elapsed := int(time.Since(started).Milliseconds())
	now := common.GetTimestamp()
	for index := range channels {
		channel := &channels[index]
		_ = model.DB.Model(channel).Updates(map[string]any{
			"base_url": provider.BaseURL, "test_time": now, "response_time": elapsed,
		}).Error
		priceUpdates := map[string]any{"tested_at": now}
		if err != nil {
			priceUpdates["last_error"] = err.Error()
			priceUpdates["available"] = false
		}
		_ = model.DB.Model(&model.ChannelModelPrice{}).Where("channel_id = ?", channel.Id).Updates(priceUpdates).Error
		if err == nil {
			_ = model.DB.Model(&model.ChannelModelPrice{}).
				Where("channel_id = ? AND billing_type != ?", channel.Id, "unavailable").
				Update("available", false).Error
			if len(models) > 0 {
				_ = model.DB.Model(&model.ChannelModelPrice{}).
					Where("channel_id = ? AND billing_type != ? AND upstream_model_id IN ?", channel.Id, "unavailable", models).
					Updates(map[string]any{"available": true, "last_error": ""}).Error
			}
		}
	}
	if err != nil {
		return 0, elapsed, err
	}
	return len(models), elapsed, nil
}

func Sync(providerID string) (int, error) {
	provider, channels, key, err := configuredProvider(providerID)
	if err != nil {
		return 0, err
	}
	models, err := fetchModels(context.Background(), provider, key)
	if err != nil {
		return 0, fmt.Errorf("读取模型列表失败: %w", err)
	}
	pricing, err := resolvePricing(context.Background(), provider, key)
	if err != nil {
		return 0, fmt.Errorf("读取价格失败: %w", err)
	}
	provider.PricingURL = pricing.SourceURL
	modelSet := make(map[string]struct{}, len(models))
	for _, id := range models {
		modelSet[id] = struct{}{}
	}
	pricingByID := make(map[string]pricingItem, len(pricing.Items))
	for _, item := range pricing.Items {
		pricingByID[item.ModelName] = item
	}

	matchedByProfile := map[string][]model.ChannelModelPrice{"chat": {}, "nano": {}, "gpt-image": {}}
	mappingByProfile := map[string]map[string]string{"chat": {}, "nano": {}, "gpt-image": {}}
	touchedProfiles := make(map[string]bool)
	for _, entry := range catalog {
		if pricing.Partial && !hasPricedCandidate(entry, pricingByID) {
			continue
		}
		upstreamID, item, reason := matchCatalogEntry(providerID, entry, modelSet, pricingByID)
		profile := profileID(entry)
		touchedProfiles[profile] = true
		if reason != "" {
			matchedByProfile[profile] = append(matchedByProfile[profile], unavailablePrice(provider, entry, upstreamID, pricing.Version, reason))
			continue
		}
		price, marshalErr := toChannelPrice(provider, entry.ID, upstreamID, item, pricing.Version)
		if marshalErr != nil {
			matchedByProfile[profile] = append(matchedByProfile[profile], unavailablePrice(provider, entry, upstreamID, pricing.Version, marshalErr.Error()))
			continue
		}
		matchedByProfile[profile] = append(matchedByProfile[profile], price)
		mappingByProfile[profile][entry.ID] = upstreamID
	}

	channelsByProfile := make(map[string]*model.Channel, len(channels))
	for index := range channels {
		channel := &channels[index]
		parts := strings.Split(channel.GetTag(), ":")
		if len(parts) == 3 {
			channelsByProfile[parts[2]] = channel
		}
	}
	total := 0
	for _, profile := range profiles {
		if pricing.Partial && !touchedProfiles[profile.ID] {
			continue
		}
		channel := channelsByProfile[profile.ID]
		if channel == nil {
			return 0, fmt.Errorf("供应商路由 %s 缺失", profile.ID)
		}
		prices := matchedByProfile[profile.ID]
		mapping := mappingByProfile[profile.ID]
		ids := make([]string, 0, len(mapping))
		for id := range mapping {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		mappingJSON, _ := common.Marshal(mapping)
		status := channel.Status
		if len(ids) == 0 {
			status = common.ChannelStatusManuallyDisabled
		}
		channel.BaseURL = common.GetPointer(provider.BaseURL)
		channel.Models = strings.Join(ids, ",")
		channel.ModelMapping = common.GetPointer(string(mappingJSON))
		channel.Status = status
		channel.TestTime = common.GetTimestamp()
		if len(ids) > 0 {
			channel.TestModel = common.GetPointer(ids[0])
		}
		if err = channel.Update(); err != nil {
			return 0, err
		}
		if err = model.UpsertChannelModelPrices(prices, channel.Id); err != nil {
			return 0, err
		}
		total += len(ids)
	}
	model.InitChannelCache()
	return total, nil
}

func CatalogAvailabilityList() ([]CatalogAvailability, error) {
	items := make([]CatalogAvailability, 0, len(catalog))
	for _, entry := range catalog {
		prices, err := model.ListAvailableChannelModelPrices(entry.ID)
		if err != nil {
			return nil, err
		}
		item := CatalogAvailability{CatalogEntry: entry, ChannelCount: len(prices), Available: len(prices) > 0}
		if len(prices) == 0 {
			snapshots, snapshotErr := model.ListChannelModelPriceSnapshots(entry.ID)
			if snapshotErr != nil {
				return nil, snapshotErr
			}
			item.UnavailableReason = unavailableReason(snapshots)
		}
		providers := map[string]struct{}{}
		referenceProtocols := map[string]struct{}{}
		for index, price := range prices {
			providers[price.Provider] = struct{}{}
			if protocol, _ := resolvedReferenceCapability(price); protocol != "" {
				item.ReferenceAvailable = true
				item.ReferenceChannelCount++
				referenceProtocols[protocol] = struct{}{}
			}
			item.BillingType = price.BillingType
			if index == 0 {
				item.MinInputPrice, item.MaxInputPrice = price.InputPrice, price.InputPrice
				item.MinOutputPrice, item.MaxOutputPrice = price.OutputPrice, price.OutputPrice
				item.MinFixedPrice, item.MaxFixedPrice = price.FixedPrice, price.FixedPrice
				continue
			}
			item.MinInputPrice, item.MaxInputPrice = minMax(item.MinInputPrice, item.MaxInputPrice, price.InputPrice)
			item.MinOutputPrice, item.MaxOutputPrice = minMax(item.MinOutputPrice, item.MaxOutputPrice, price.OutputPrice)
			item.MinFixedPrice, item.MaxFixedPrice = minMax(item.MinFixedPrice, item.MaxFixedPrice, price.FixedPrice)
		}
		for provider := range providers {
			item.Providers = append(item.Providers, provider)
		}
		for protocol := range referenceProtocols {
			item.ReferenceProtocols = append(item.ReferenceProtocols, protocol)
		}
		sort.Strings(item.Providers)
		sort.Strings(item.ReferenceProtocols)
		if entry.Kind == "image" && !item.ReferenceAvailable {
			if item.Available {
				item.ReferenceUnavailableReason = "已配置渠道均未声明多图参考协议"
			} else {
				item.ReferenceUnavailableReason = item.UnavailableReason
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// ReferenceImageModelAvailable reports whether at least one enabled, priced
// channel explicitly supports the reference protocol required by modelID.
func ReferenceImageModelAvailable(modelID string) (bool, error) {
	expected, ok := requiredReferenceProtocol(modelID)
	if !ok {
		return false, nil
	}
	prices, err := model.ListAvailableChannelModelPrices(modelID)
	if err != nil {
		return false, err
	}
	for _, price := range prices {
		protocol, maxImages := resolvedReferenceCapability(price)
		if protocol == expected {
			// Compatibility migration for snapshots synced before capability
			// columns were introduced. Only explicit immutable pricing metadata
			// can populate the fields, so an unknown channel remains ineligible.
			if price.ReferenceProtocol == "" {
				if err = model.DB.Model(&model.ChannelModelPrice{}).Where("id = ? AND (reference_protocol = ? OR reference_protocol IS NULL)", price.ID, "").
					Updates(map[string]any{"reference_protocol": protocol, "max_reference_images": maxImages}).Error; err != nil {
					return false, err
				}
			}
			return true, nil
		}
	}
	return false, nil
}

func configuredProvider(providerID string) (Definition, []model.Channel, string, error) {
	provider, ok := definition(providerID)
	if !ok {
		return Definition{}, nil, "", errors.New("未知供应商")
	}
	var err error
	provider, err = resolveProvider(context.Background(), provider)
	if err != nil {
		return Definition{}, nil, "", err
	}
	channels, err := providerChannels(providerID, true)
	if err != nil {
		return Definition{}, nil, "", err
	}
	if len(channels) == 0 || strings.TrimSpace(channels[0].Key) == "" {
		return Definition{}, nil, "", errors.New("请先配置供应商 API Key")
	}
	return provider, channels, strings.TrimSpace(channels[0].Key), nil
}

func resolveProvider(parent context.Context, provider Definition) (Definition, error) {
	if provider.StatusURL == "" {
		return provider, nil
	}
	var response statusResponse
	if err := getJSON(parent, provider.StatusURL, "", &response); err != nil {
		return Definition{}, fmt.Errorf("读取 %s 服务地址失败: %w", provider.Name, err)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(response.Data.ServerAddress), "/")
	if !strings.HasPrefix(baseURL, "https://") {
		return Definition{}, fmt.Errorf("%s 状态接口未返回安全服务地址", provider.Name)
	}
	provider.BaseURL = baseURL
	provider.PricingURL = baseURL + "/api/pricing"
	return provider, nil
}

func fetchModels(parent context.Context, provider Definition, key string) ([]string, error) {
	var response modelsResponse
	if err := getJSON(parent, provider.BaseURL+"/v1/models", key, &response); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(response.Data))
	for _, item := range response.Data {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	return ids, nil
}

func fetchPricing(parent context.Context, provider Definition, key string) ([]pricingItem, string, error) {
	var response pricingResponse
	if err := getJSON(parent, provider.PricingURL, key, &response); err != nil {
		return nil, "", err
	}
	if len(response.Data) == 0 {
		return nil, "", errors.New("价格接口未返回模型")
	}
	return response.Data, response.PricingVersion, nil
}

func resolvePricing(parent context.Context, provider Definition, key string) (pricingSnapshot, error) {
	items, version, err := fetchPricing(parent, provider, key)
	if err == nil {
		return pricingSnapshot{Items: items, SourceURL: provider.PricingURL, Version: version}, nil
	}
	// GeekNow's relay key can generate images but cannot authenticate its
	// dashboard-only pricing endpoint. Keep the fallback narrow and auditable:
	// only the model and price verified from GeekNow's official marketplace.
	if snapshot, ok := verifiedPricingSnapshots[provider.ID]; ok {
		return snapshot, nil
	}
	return pricingSnapshot{}, err
}

func hasPricedCandidate(entry CatalogEntry, pricing map[string]pricingItem) bool {
	for _, candidate := range aliases[entry.ID] {
		if _, ok := pricing[candidate]; ok {
			return true
		}
	}
	return false
}

func getJSON(parent context.Context, url, key string, target any) error {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	if err = common.Unmarshal(body, target); err != nil {
		return errors.New("上游返回的不是有效 JSON")
	}
	return nil
}

func matchCatalogEntry(providerID string, entry CatalogEntry, models map[string]struct{}, pricing map[string]pricingItem) (string, pricingItem, string) {
	modelFound := false
	priceFound := false
	endpointMismatch := false
	for _, candidate := range aliases[entry.ID] {
		if _, exists := models[candidate]; !exists {
			continue
		}
		modelFound = true
		item, priced := pricing[candidate]
		if !priced {
			continue
		}
		priceFound = true
		if item.Available != nil && !*item.Available {
			continue
		}
		// The image workspace uses /v1/images/generations. Do not advertise
		// models whose pricing metadata only declares chat/gemini endpoints;
		// those require a separate task protocol adapter.
		if entry.Kind == "image" && !imageEndpointSupported(providerID, entry.ID, candidate, item.SupportedEndpoints) {
			endpointMismatch = true
			continue
		}
		return candidate, item, ""
	}
	if !modelFound {
		return "", pricingItem{}, "实时模型列表未找到候选 ID"
	}
	if !priceFound {
		return "", pricingItem{}, "价格接口没有该上游模型的明确价格"
	}
	if endpointMismatch {
		return "", pricingItem{}, "上游接口不兼容工作台图像生成协议"
	}
	return "", pricingItem{}, "价格接口标记模型当前不可用"
}

func imageEndpointSupported(providerID, catalogID, upstreamID string, endpoints []string) bool {
	if providerID == "zex" && catalogID != "gpt-image-2" {
		return strings.HasPrefix(upstreamID, "gemini-") && strings.Contains(upstreamID, "image") &&
			containsString(endpoints, "gemini")
	}
	if containsString(endpoints, "image-generation") {
		return true
	}
	return false
}

func channelType(providerID, profileID string) int {
	if providerID == "zex" && profileID == "nano" {
		return constant.ChannelTypeGemini
	}
	return constant.ChannelTypeOpenAI
}

func containsString(items []string, expected string) bool {
	for _, item := range items {
		if item == expected {
			return true
		}
	}
	return false
}

func toChannelPrice(provider Definition, catalogID, upstreamID string, item pricingItem, version string) (model.ChannelModelPrice, error) {
	original, err := common.Marshal(item)
	if err != nil {
		return model.ChannelModelPrice{}, err
	}
	price := model.ChannelModelPrice{
		CatalogID: catalogID, UpstreamModelID: upstreamID, Provider: provider.ID, Currency: "USD",
		SourceURL: provider.PricingURL, SourceVersion: version, Available: true, OriginalPricing: string(original),
		CacheRatio: item.CacheRatio, CacheCreationRatio: item.CreateCacheRatio,
	}
	price.ReferenceProtocol, price.MaxReferenceImages = declaredReferenceCapability(
		provider.ID, catalogID, upstreamID, item.SupportedEndpoints,
	)
	if item.QuotaType == 1 || item.ModelPrice > 0 {
		price.BillingType = model.ChannelModelBillingFixed
		price.FixedPrice = item.ModelPrice
		if price.FixedPrice <= 0 {
			return model.ChannelModelPrice{}, fmt.Errorf("模型 %s 缺少按次价格", upstreamID)
		}
		return price, nil
	}
	price.BillingType = model.ChannelModelBillingToken
	price.InputPrice = item.ModelRatio * 2
	price.OutputPrice = price.InputPrice * item.CompletionRatio
	if price.InputPrice <= 0 || price.OutputPrice <= 0 {
		return model.ChannelModelPrice{}, fmt.Errorf("模型 %s 缺少 Token 价格", upstreamID)
	}
	return price, nil
}

func requiredReferenceProtocol(catalogID string) (string, bool) {
	switch catalogID {
	case "nano-banana", "nano-banana-pro", "nano-banana-2":
		return ReferenceProtocolGeminiInline, true
	case "gpt-image-2":
		return ReferenceProtocolOpenAIEditMultipart, true
	default:
		return "", false
	}
}

func declaredReferenceCapability(providerID, catalogID, upstreamID string, endpoints []string) (string, int) {
	expected, ok := requiredReferenceProtocol(catalogID)
	if !ok {
		return "", 0
	}
	switch expected {
	case ReferenceProtocolGeminiInline:
		// Zex's Nano route is configured as a native Gemini channel. Its
		// pricing metadata must also explicitly advertise the Gemini endpoint.
		if providerID == "zex" && strings.HasPrefix(upstreamID, "gemini-") &&
			strings.Contains(upstreamID, "image") && containsEndpoint(endpoints, "gemini") {
			return expected, MaxReferenceImages
		}
	case ReferenceProtocolOpenAIEditMultipart:
		if hasExplicitImageEditEndpoint(endpoints) {
			return expected, MaxReferenceImages
		}
	}
	return "", 0
}

func resolvedReferenceCapability(price model.ChannelModelPrice) (string, int) {
	expected, ok := requiredReferenceProtocol(price.CatalogID)
	if !ok {
		return "", 0
	}
	if price.ReferenceProtocol != "" {
		if price.ReferenceProtocol == expected && price.MaxReferenceImages > 0 {
			return price.ReferenceProtocol, price.MaxReferenceImages
		}
		return "", 0
	}
	// Existing installations may have snapshots created before the capability
	// columns existed. Derive only from the immutable original pricing payload;
	// generic OpenAI/image-generation labels are intentionally insufficient.
	var item pricingItem
	if price.OriginalPricing == "" || common.Unmarshal([]byte(price.OriginalPricing), &item) != nil {
		return "", 0
	}
	return declaredReferenceCapability(price.Provider, price.CatalogID, price.UpstreamModelID, item.SupportedEndpoints)
}

func containsEndpoint(endpoints []string, expected string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	for _, endpoint := range endpoints {
		if strings.ToLower(strings.TrimSpace(endpoint)) == expected {
			return true
		}
	}
	return false
}

func hasExplicitImageEditEndpoint(endpoints []string) bool {
	for _, endpoint := range endpoints {
		switch strings.ToLower(strings.TrimSpace(endpoint)) {
		case "openai编辑图片", "image-edit", "image-edits", "images-edit", "images-edits",
			"openai-image-edit", "openai-image-edits", "openai_image_edit", "/v1/images/edits":
			return true
		}
	}
	return false
}

func unavailablePrice(provider Definition, entry CatalogEntry, upstreamID, version, reason string) model.ChannelModelPrice {
	return model.ChannelModelPrice{
		CatalogID: entry.ID, UpstreamModelID: upstreamID, Provider: provider.ID,
		BillingType: "unavailable", Currency: "USD", SourceURL: provider.PricingURL,
		SourceVersion: version, Available: false, LastError: entry.Name + "：" + reason,
	}
}

func lastFour(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4 {
		return ""
	}
	return value[len(value)-4:]
}

func profileName(id string) string {
	switch id {
	case "chat":
		return "对话"
	case "nano":
		return "Nano 图像"
	default:
		return "GPT Image"
	}
}

func minMax(currentMin, currentMax, value float64) (float64, float64) {
	if value < currentMin {
		currentMin = value
	}
	if value > currentMax {
		currentMax = value
	}
	return currentMin, currentMax
}

func unavailableReason(snapshots []model.ChannelModelPrice) string {
	for _, snapshot := range snapshots {
		if snapshot.Available {
			return "模型已同步，但可用渠道尚未启用"
		}
	}
	for _, snapshot := range snapshots {
		if snapshot.LastError != "" {
			return snapshot.LastError
		}
	}
	return "尚未配置并同步可用渠道"
}
