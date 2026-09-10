package mujianprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

const (
	providerAuthBearer  = "bearer"
	providerAuthXAPIKey = "x-api-key"

	providerSyncZenMux  = "zenmux"
	providerSyncTabCode = "tabcode"

	tabCodeKiroBaseURL            = "https://api2.tabcode.cc/claude/kiropower"
	tabCodeChannelsURL            = "https://tabcode.cc/api/v1/billing/channels"
	anthropicPricingURL           = "https://platform.claude.com/docs/en/about-claude/pricing"
	anthropicSonnet46PriceDate    = "2026-02-18"
	anthropicSonnet46InputPrice   = 3.0
	anthropicSonnet46OutputPrice  = 15.0
	anthropicSonnet46CacheRead    = 0.3
	anthropicSonnet46CacheWrite5m = 3.75
	anthropicSonnet46CacheWrite1h = 6.0

	nativeMessagesVersion              = "2023-06-01"
	maxExternalTokenPriceUSDPerMillion = 1000.0
	maxTabCodeMultiplier               = 10.0
)

type providerCapabilities struct {
	ProfileIDs           []string
	ChannelType          int
	CatalogIDs           []string
	Aliases              map[string][]string
	AuthStyle            string
	AnthropicVersion     string
	SyncStrategy         string
	ProbeModel           string
	HeaderOverride       string
	ManageHeaderOverride bool
	StrictLifecycle      bool
}

type providerSnapshot struct {
	Models          []string
	Pricing         pricingSnapshot
	ResolvedBaseURL string
}

func zenMuxCapabilities() providerCapabilities {
	return providerCapabilities{
		ProfileIDs:  []string{"chat"},
		ChannelType: constant.ChannelTypeAnthropic,
		CatalogIDs: []string{
			"claude-fable-5-nc",
			"claude-opus-5",
			"claude-sonnet-5",
			"claude-haiku-4-5",
			"claude-sonnet-4-6",
		},
		Aliases: map[string][]string{
			"claude-fable-5-nc": {"anthropic/claude-fable-5"},
			"claude-opus-5":     {"anthropic/claude-opus-5"},
			"claude-sonnet-5":   {"anthropic/claude-sonnet-5"},
			"claude-haiku-4-5":  {"anthropic/claude-haiku-4.5"},
			"claude-sonnet-4-6": {"anthropic/claude-sonnet-4.6"},
		},
		AuthStyle:            providerAuthXAPIKey,
		AnthropicVersion:     nativeMessagesVersion,
		SyncStrategy:         providerSyncZenMux,
		ProbeModel:           "anthropic/claude-sonnet-4.6",
		HeaderOverride:       `{"anthropic-version":"2023-06-01"}`,
		ManageHeaderOverride: true,
		StrictLifecycle:      true,
	}
}

func tabCodeCapabilities() providerCapabilities {
	return providerCapabilities{
		ProfileIDs:  []string{"chat"},
		ChannelType: constant.ChannelTypeAnthropic,
		CatalogIDs:  []string{"claude-sonnet-4-6"},
		Aliases: map[string][]string{
			"claude-sonnet-4-6": {"claude-sonnet-4-6"},
		},
		AuthStyle:            providerAuthBearer,
		AnthropicVersion:     nativeMessagesVersion,
		SyncStrategy:         providerSyncTabCode,
		ProbeModel:           "claude-sonnet-4-6",
		HeaderOverride:       `{"Authorization":"Bearer {api_key}","anthropic-version":"2023-06-01"}`,
		ManageHeaderOverride: true,
		StrictLifecycle:      true,
	}
}

func providerRouteProfiles(provider Definition) []routeProfile {
	if len(provider.capabilities.ProfileIDs) == 0 {
		return profiles
	}
	result := make([]routeProfile, 0, len(provider.capabilities.ProfileIDs))
	for _, profile := range profiles {
		if containsString(provider.capabilities.ProfileIDs, profile.ID) {
			result = append(result, profile)
		}
	}
	return result
}

func providerCatalog(provider Definition) []CatalogEntry {
	if len(provider.capabilities.CatalogIDs) == 0 {
		return catalog
	}
	result := make([]CatalogEntry, 0, len(provider.capabilities.CatalogIDs))
	for _, entry := range catalog {
		if containsString(provider.capabilities.CatalogIDs, entry.ID) {
			result = append(result, entry)
		}
	}
	return result
}

func providerAliases(providerID string, entry CatalogEntry) []string {
	provider, ok := definition(providerID)
	if ok && provider.capabilities.Aliases != nil {
		return provider.capabilities.Aliases[entry.ID]
	}
	return aliases[entry.ID]
}

func providerChannelType(provider Definition, profileID string) int {
	if provider.capabilities.ChannelType != 0 {
		return provider.capabilities.ChannelType
	}
	if provider.ID == "zex" && profileID == "nano" {
		return constant.ChannelTypeGemini
	}
	return constant.ChannelTypeOpenAI
}

func providerTestCatalogID(provider Definition) string {
	for _, entry := range providerCatalog(provider) {
		for _, upstreamID := range providerAliases(provider.ID, entry) {
			if upstreamID == provider.capabilities.ProbeModel {
				return entry.ID
			}
		}
	}
	return ""
}

func resolveProviderSnapshot(parent context.Context, provider Definition, key string) (providerSnapshot, error) {
	switch provider.capabilities.SyncStrategy {
	case providerSyncZenMux:
		return fetchZenMuxSnapshot(parent, provider, key)
	case providerSyncTabCode:
		return fetchTabCodeSnapshot(parent, provider)
	default:
		models, err := fetchModels(parent, provider, key)
		if err != nil {
			return providerSnapshot{}, fmt.Errorf("读取模型列表失败: %w", err)
		}
		pricing, err := resolvePricing(parent, provider, key)
		if err != nil {
			return providerSnapshot{}, fmt.Errorf("读取价格失败: %w", err)
		}
		return providerSnapshot{Models: models, Pricing: pricing}, nil
	}
}

type zenMuxModelsResponse struct {
	Data    []json.RawMessage `json:"data"`
	HasMore bool              `json:"has_more"`
}

type zenMuxModel struct {
	ID       string                       `json:"id"`
	Pricings map[string][]json.RawMessage `json:"pricings"`
}

func fetchZenMuxSnapshot(parent context.Context, provider Definition, key string) (providerSnapshot, error) {
	body, err := doProviderRequest(parent, provider, http.MethodGet, provider.PricingURL, key, nil, true)
	if err != nil {
		return providerSnapshot{}, fmt.Errorf("读取 ZenMux 模型与价格失败: %w", err)
	}
	return parseZenMuxSnapshot(provider, body)
}

func parseZenMuxSnapshot(provider Definition, body []byte) (providerSnapshot, error) {
	var response zenMuxModelsResponse
	if err := common.Unmarshal(body, &response); err != nil {
		return providerSnapshot{}, errors.New("ZenMux 返回的不是有效 JSON")
	}
	if len(response.Data) == 0 {
		return providerSnapshot{}, errors.New("ZenMux 模型列表为空")
	}
	if response.HasMore {
		return providerSnapshot{}, errors.New("ZenMux 模型列表存在未处理的分页")
	}

	wanted := make(map[string]struct{})
	for _, entry := range providerCatalog(provider) {
		for _, upstreamID := range providerAliases(provider.ID, entry) {
			wanted[upstreamID] = struct{}{}
		}
	}
	models := make([]string, 0, len(wanted))
	items := make([]pricingItem, 0, len(wanted))
	itemIndex := make(map[string]int, len(wanted))
	for _, raw := range response.Data {
		var identity struct {
			ID string `json:"id"`
		}
		if err := common.Unmarshal(raw, &identity); err != nil {
			continue
		}
		if _, ok := wanted[identity.ID]; !ok {
			continue
		}
		if index, duplicate := itemIndex[identity.ID]; duplicate {
			items[index].ValidationError = "ZenMux 模型列表包含重复模型 ID"
			items[index].OriginalPricing = appendRawPricingEvidence(items[index].OriginalPricing, raw)
			continue
		}
		itemIndex[identity.ID] = len(items)
		models = append(models, identity.ID)
		item := pricingItem{ModelName: identity.ID, OriginalPricing: string(raw)}
		var upstream zenMuxModel
		if err := common.Unmarshal(raw, &upstream); err != nil {
			item.ValidationError = "ZenMux 模型价格格式无效"
			items = append(items, item)
			continue
		}
		if err := normalizeZenMuxPricing(upstream, &item); err != nil {
			item.ValidationError = err.Error()
		}
		items = append(items, item)
	}

	return providerSnapshot{
		Models: models,
		Pricing: pricingSnapshot{
			Items:     items,
			SourceURL: provider.PricingURL,
			Version:   evidenceHash(body),
		},
	}, nil
}

func appendRawPricingEvidence(existing string, raw json.RawMessage) string {
	evidence := make([]json.RawMessage, 0, 2)
	trimmed := strings.TrimSpace(existing)
	if strings.HasPrefix(trimmed, "[") {
		if err := common.Unmarshal([]byte(trimmed), &evidence); err != nil {
			return existing
		}
	} else if trimmed != "" {
		evidence = append(evidence, json.RawMessage(append([]byte(nil), trimmed...)))
	}
	evidence = append(evidence, json.RawMessage(append([]byte(nil), raw...)))
	encoded, err := common.Marshal(evidence)
	if err != nil {
		return existing
	}
	return string(encoded)
}

func normalizeZenMuxPricing(upstream zenMuxModel, item *pricingItem) error {
	allowed := map[string]struct{}{
		"prompt": {}, "completion": {}, "input_cache_read": {},
		"input_cache_write_5_min": {}, "input_cache_write_1_h": {},
		// web_search is deliberately retained only as raw evidence. Its optional
		// per-call price is not imported into the model-price snapshot.
		"web_search": {},
	}
	for name, entries := range upstream.Pricings {
		if _, ok := allowed[name]; !ok && len(entries) > 0 {
			return fmt.Errorf("ZenMux 包含未支持的计费项 %s", name)
		}
	}
	prompt, err := exactZenMuxPrice(upstream.Pricings, "prompt")
	if err != nil {
		return err
	}
	completion, err := exactZenMuxPrice(upstream.Pricings, "completion")
	if err != nil {
		return err
	}
	cacheRead, err := exactZenMuxPrice(upstream.Pricings, "input_cache_read")
	if err != nil {
		return err
	}
	cacheWrite5m, err := exactZenMuxPrice(upstream.Pricings, "input_cache_write_5_min")
	if err != nil {
		return err
	}
	cacheWrite1h, err := exactZenMuxPrice(upstream.Pricings, "input_cache_write_1_h")
	if err != nil {
		return err
	}
	if !almostEqual(cacheWrite1h/cacheWrite5m, 1.6) {
		return errors.New("ZenMux 1 小时缓存写入价无法用当前计费模型表示")
	}
	cacheRatio := cacheRead / prompt
	cacheCreationRatio := cacheWrite5m / prompt
	if !validPositivePrice(cacheRatio) || !validPositivePrice(cacheCreationRatio) {
		return errors.New("ZenMux 缓存价格比率无效")
	}
	item.InputPrice = prompt
	item.OutputPrice = completion
	item.CacheRatio = cacheRatio
	item.CreateCacheRatio = cacheCreationRatio
	return nil
}

func exactZenMuxPrice(pricings map[string][]json.RawMessage, name string) (float64, error) {
	entries := pricings[name]
	if len(entries) != 1 {
		return 0, fmt.Errorf("ZenMux %s 价格必须是唯一的无条件价格", name)
	}
	var fields map[string]json.RawMessage
	if err := common.Unmarshal(entries[0], &fields); err != nil {
		return 0, fmt.Errorf("ZenMux %s 价格格式无效", name)
	}
	if len(fields) != 3 || fields["value"] == nil || fields["unit"] == nil || fields["currency"] == nil {
		return 0, fmt.Errorf("ZenMux %s 存在条件或未知定价字段", name)
	}
	var price struct {
		Value    float64 `json:"value"`
		Unit     string  `json:"unit"`
		Currency string  `json:"currency"`
	}
	if err := common.Unmarshal(entries[0], &price); err != nil {
		return 0, fmt.Errorf("ZenMux %s 价格格式无效", name)
	}
	if price.Unit != "perMTokens" || price.Currency != "USD" {
		return 0, fmt.Errorf("ZenMux %s 价格不是 USD/perMTokens", name)
	}
	if !validPositivePrice(price.Value) {
		return 0, fmt.Errorf("ZenMux %s 价格无效", name)
	}
	if price.Value > maxExternalTokenPriceUSDPerMillion {
		return 0, fmt.Errorf("ZenMux %s 价格超过安全上限", name)
	}
	return price.Value, nil
}

type tabCodeChannel struct {
	Provider   string  `json:"provider"`
	Path       string  `json:"path"`
	Multiplier float64 `json:"multiplier"`
}

func fetchTabCodeSnapshot(parent context.Context, provider Definition) (providerSnapshot, error) {
	body, err := doProviderRequest(parent, provider, http.MethodGet, provider.PricingURL, "", nil, false)
	if err != nil {
		return providerSnapshot{}, fmt.Errorf("读取 TabCode 渠道价格失败: %w", err)
	}
	return parseTabCodeSnapshot(provider, body)
}

func parseTabCodeSnapshot(provider Definition, body []byte) (providerSnapshot, error) {
	var rawChannels []json.RawMessage
	if err := common.Unmarshal(body, &rawChannels); err != nil {
		return providerSnapshot{}, errors.New("TabCode 返回的不是有效 JSON")
	}
	var matched *tabCodeChannel
	var matchedRaw json.RawMessage
	for _, raw := range rawChannels {
		var channel tabCodeChannel
		if common.Unmarshal(raw, &channel) != nil || channel.Provider != "claude" {
			continue
		}
		if validateTabCodeKiroURL(channel.Path) != nil {
			continue
		}
		if matched != nil {
			return providerSnapshot{}, errors.New("TabCode 返回多个 Kiro 渠道")
		}
		matched = &channel
		matchedRaw = raw
	}
	if matched == nil {
		return providerSnapshot{}, errors.New("TabCode 未返回预期的 Claude Kiro 渠道")
	}
	if matched.Multiplier <= 0 || math.IsNaN(matched.Multiplier) || math.IsInf(matched.Multiplier, 0) {
		return providerSnapshot{}, errors.New("TabCode Kiro 倍率无效")
	}
	if matched.Multiplier > maxTabCodeMultiplier {
		return providerSnapshot{}, errors.New("TabCode Kiro 倍率超过安全上限")
	}
	inputPrice := anthropicSonnet46InputPrice * matched.Multiplier
	outputPrice := anthropicSonnet46OutputPrice * matched.Multiplier
	if !validPositivePrice(inputPrice) || !validPositivePrice(outputPrice) {
		return providerSnapshot{}, errors.New("TabCode Kiro 倍率计算后价格无效")
	}
	if inputPrice > maxExternalTokenPriceUSDPerMillion || outputPrice > maxExternalTokenPriceUSDPerMillion {
		return providerSnapshot{}, errors.New("TabCode Kiro 倍率计算后价格超过安全上限")
	}

	evidence := struct {
		TabCodeChannel    json.RawMessage `json:"tabcode_channel"`
		AnthropicOfficial any             `json:"anthropic_official"`
	}{
		TabCodeChannel: matchedRaw,
		AnthropicOfficial: map[string]any{
			"source_url":     anthropicPricingURL,
			"version":        anthropicSonnet46PriceDate,
			"model":          "claude-sonnet-4-6",
			"input":          anthropicSonnet46InputPrice,
			"output":         anthropicSonnet46OutputPrice,
			"cache_read":     anthropicSonnet46CacheRead,
			"cache_write_5m": anthropicSonnet46CacheWrite5m,
			"cache_write_1h": anthropicSonnet46CacheWrite1h,
			"currency":       "USD",
			"unit":           "perMTokens",
		},
	}
	evidenceJSON, err := common.Marshal(evidence)
	if err != nil {
		return providerSnapshot{}, err
	}
	versionInput := append(append([]byte(nil), body...), evidenceJSON...)
	item := pricingItem{
		ModelName:        "claude-sonnet-4-6",
		InputPrice:       inputPrice,
		OutputPrice:      outputPrice,
		CacheRatio:       anthropicSonnet46CacheRead / anthropicSonnet46InputPrice,
		CreateCacheRatio: anthropicSonnet46CacheWrite5m / anthropicSonnet46InputPrice,
		OriginalPricing:  string(evidenceJSON),
	}
	return providerSnapshot{
		Models:          []string{"claude-sonnet-4-6"},
		ResolvedBaseURL: strings.TrimRight(matched.Path, "/"),
		Pricing: pricingSnapshot{
			Items:     []pricingItem{item},
			SourceURL: provider.PricingURL,
			Version:   evidenceHash(versionInput),
		},
	}, nil
}

func validateTabCodeKiroURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("TabCode Kiro 服务地址无效")
	}
	if parsed.Scheme != "https" || parsed.Host != "api2.tabcode.cc" || parsed.EscapedPath() != "/claude/kiropower" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("TabCode Kiro 服务地址不在允许范围")
	}
	return nil
}

func testProviderConnection(parent context.Context, provider Definition, key string) ([]string, error) {
	if provider.capabilities.ProbeModel == "" {
		models, err := fetchModels(parent, provider, key)
		return models, err
	}
	if provider.capabilities.SyncStrategy == providerSyncTabCode {
		if err := validateTabCodeKiroURL(provider.BaseURL); err != nil {
			return nil, err
		}
	}
	if err := probeNativeMessages(parent, provider, key); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(provider.capabilities.CatalogIDs))
	for _, entry := range providerCatalog(provider) {
		models = append(models, providerAliases(provider.ID, entry)...)
	}
	return models, nil
}

func probeNativeMessages(parent context.Context, provider Definition, key string) error {
	payload, err := common.Marshal(map[string]any{
		"model":      provider.capabilities.ProbeModel,
		"max_tokens": 1,
		"messages": []map[string]string{{
			"role": "user", "content": "Reply with OK",
		}},
	})
	if err != nil {
		return err
	}
	body, err := doProviderRequest(parent, provider, http.MethodPost, strings.TrimRight(provider.BaseURL, "/")+"/v1/messages", key, payload, true)
	if err != nil {
		return err
	}
	var response struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage *struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err = common.Unmarshal(body, &response); err != nil {
		return errors.New("上游 Messages 返回的不是有效 JSON")
	}
	text := ""
	for _, content := range response.Content {
		if content.Type == "text" {
			text += content.Text
		}
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("上游 Messages 响应未包含文本内容")
	}
	if response.Model != provider.capabilities.ProbeModel {
		return errors.New("上游 Messages 响应模型与测试模型不一致")
	}
	if response.Usage == nil || response.Usage.InputTokens == nil || response.Usage.OutputTokens == nil {
		return errors.New("上游 Messages 响应缺少 usage")
	}
	if *response.Usage.InputTokens < 0 || *response.Usage.OutputTokens < 0 {
		return errors.New("上游 Messages 响应包含无效 usage")
	}
	return nil
}

func doProviderRequest(parent context.Context, provider Definition, method, endpoint, key string, payload []byte, protocolHeaders bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		if provider.capabilities.AuthStyle == providerAuthXAPIKey {
			req.Header.Set("x-api-key", key)
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	if protocolHeaders && provider.capabilities.AnthropicVersion != "" {
		req.Header.Set("anthropic-version", provider.capabilities.AnthropicVersion)
	}
	client := *http.DefaultClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := readLimitedBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	return responseBody, nil
}

func readLimitedBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxProviderResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxProviderResponseBytes {
		return nil, errors.New("上游响应超过大小限制")
	}
	return body, nil
}

func evidenceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func almostEqual(left, right float64) bool {
	return math.Abs(left-right) < 1e-9
}

func validPositivePrice(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validNonNegativePrice(value float64) bool {
	return value >= 0 && value <= maxExternalTokenPriceUSDPerMillion &&
		!math.IsNaN(value) && !math.IsInf(value, 0)
}
