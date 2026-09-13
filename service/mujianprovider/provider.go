package mujianprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/mujianpricing"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxProviderResponseBytes             = 16 << 20
	claudeCacheCreationOneHourMultiplier = 6 / 3.75
)

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

	capabilities providerCapabilities
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
	Configured      bool                  `json:"configured"`
	Enabled         bool                  `json:"enabled"`
	StrictLifecycle bool                  `json:"strict_lifecycle"`
	TestReady       bool                  `json:"test_ready"`
	EnableReady     bool                  `json:"enable_ready"`
	KeyLast4        string                `json:"key_last4,omitempty"`
	ModelCount      int                   `json:"model_count"`
	SyncedAt        int64                 `json:"synced_at,omitempty"`
	TestedAt        int64                 `json:"tested_at,omitempty"`
	LastError       string                `json:"last_error,omitempty"`
	Routes          []ProviderRouteStatus `json:"routes"`
}

type ProviderRouteStatus struct {
	ChannelID int                   `json:"channel_id"`
	Name      string                `json:"name"`
	ProfileID string                `json:"profile_id"`
	Group     string                `json:"group"`
	Status    int                   `json:"status"`
	Priority  int64                 `json:"priority"`
	Prices    []ProviderPriceStatus `json:"prices"`
}

// ProviderPriceStatus intentionally excludes OriginalPricing and every
// credential-bearing channel field. It is safe to render in the Root panel.
type ProviderPriceStatus struct {
	CatalogID         string   `json:"catalog_id"`
	UpstreamModelID   string   `json:"upstream_model_id"`
	BillingType       string   `json:"billing_type"`
	Currency          string   `json:"currency"`
	InputPrice        float64  `json:"input_price"`
	OutputPrice       float64  `json:"output_price"`
	ImageOutputPrice  float64  `json:"image_output_price,omitempty"`
	FixedPrice        float64  `json:"fixed_price"`
	CacheReadPrice    *float64 `json:"cache_read_price,omitempty"`
	CacheWrite5mPrice *float64 `json:"cache_write_5m_price,omitempty"`
	CacheWrite1hPrice *float64 `json:"cache_write_1h_price,omitempty"`
	Available         bool     `json:"available"`
	SourceURL         string   `json:"source_url"`
	SourceVersion     string   `json:"source_version"`
	SyncedAt          int64    `json:"synced_at"`
	TestedAt          int64    `json:"tested_at"`
	LastError         string   `json:"last_error,omitempty"`
}

type CatalogAvailability struct {
	RetailPricing *mujianpricing.ImagePrice `json:"retail_pricing,omitempty"`
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
	MinImageOutputPrice        float64  `json:"min_image_output_price,omitempty"`
	MaxImageOutputPrice        float64  `json:"max_image_output_price,omitempty"`
	MinFixedPrice              float64  `json:"min_fixed_price"`
	MaxFixedPrice              float64  `json:"max_fixed_price"`
	UnavailableReason          string   `json:"unavailable_reason,omitempty"`
}

var definitions = []Definition{
	{ID: "zex", Name: "ZexAPI", SiteURL: "https://zexapi.com/pricing", BaseURL: "https://zexapi.com", PricingURL: "https://zexapi.com/api/pricing"},
	{ID: "geeknow", Name: "GeekNow", SiteURL: "https://www.geeknow.top/pricing", BaseURL: "https://geeknow.ai", PricingURL: "https://geeknow.ai/api/pricing", StatusURL: "https://www.geeknow.top/api/status"},
	{ID: "yunwu", Name: "云雾 API", SiteURL: "https://yunwu.ai/", BaseURL: "https://yunwu.ai", PricingURL: "https://yunwu.ai/api/pricing"},
	{ID: "yuyu", Name: "羽宇 AI", SiteURL: "https://api.yu-yu.ai/pricing", BaseURL: "https://api.yu-yu.ai", PricingURL: "https://api.yu-yu.ai/api/pricing", capabilities: yuYuCapabilities()},
	{
		ID: "zenmux", Name: "ZenMux", SiteURL: "https://zenmux.ai/", BaseURL: "https://zenmux.ai/api/anthropic",
		PricingURL: "https://zenmux.ai/api/anthropic/v1/models", capabilities: zenMuxCapabilities(),
	},
	{
		ID: "tabcode", Name: "TabCode Kiro", SiteURL: "https://tabcode.cc/", BaseURL: tabCodeKiroBaseURL,
		PricingURL: tabCodeChannelsURL, capabilities: tabCodeCapabilities(),
	},
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
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", ProviderName: "Anthropic", Kind: "chat", Tags: []string{"对话", "推理"}, SupportsReasoning: true, ReasoningProtocol: ReasoningProtocolClaudeAdaptive},
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
	"claude-sonnet-4-6": {"claude-sonnet-4-6"},
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

	InputPrice       float64 `json:"-"`
	OutputPrice      float64 `json:"-"`
	ImageOutputPrice float64 `json:"-"`
	OriginalPricing  string  `json:"-"`
	ValidationError  string  `json:"-"`
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
	{ID: "chat", Priority: map[string]int64{"zenmux": 600, "tabcode": 550, "yunwu": 300, "geeknow": 200, "zex": 100, "yuyu": 50}},
	{ID: "nano", Priority: map[string]int64{"geeknow": 320, "zex": 220, "yunwu": 120, "yuyu": 50}},
	{ID: "gpt-image", Priority: map[string]int64{"zex": 320, "yunwu": 220, "geeknow": 120, "yuyu": 50}},
}

var providerConfigureLocks sync.Map

func Definitions() []Definition { return append([]Definition(nil), definitions...) }

func Catalog() []CatalogEntry {
	items := make([]CatalogEntry, len(catalog))
	copy(items, catalog)
	return items
}

// IsCatalogModel reports whether provider synchronization can create a
// channel-specific price snapshot for this public model ID. Relay pricing can
// use it to keep ordinary, non-catalog model requests on the legacy hot path.
func IsCatalogModel(modelID string) bool {
	for _, entry := range catalog {
		if entry.ID == modelID {
			return true
		}
	}
	return false
}

// IsClaudeCatalogModel identifies public catalog IDs whose native upstream
// applies Claude's configurable default max_tokens when the client omits it.
func IsClaudeCatalogModel(modelID string) bool {
	for _, entry := range catalog {
		if entry.ID == modelID {
			return entry.Kind == "chat" && entry.ProviderName == "Anthropic"
		}
	}
	return false
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

func strictProviderForTag(tag string) (Definition, bool) {
	tag = strings.TrimSpace(tag)
	for _, provider := range definitions {
		if !provider.capabilities.StrictLifecycle {
			continue
		}
		for _, profile := range providerRouteProfiles(provider) {
			if tag == providerTag(provider.ID, profile.ID) {
				return provider, true
			}
		}
	}
	return Definition{}, false
}

// StrictProviderIDForTag identifies tags reserved for provider-managed
// channels. Root's generic channel editor must not bypass their lifecycle.
func StrictProviderIDForTag(tag string) (string, bool) {
	provider, ok := strictProviderForTag(tag)
	if !ok {
		return "", false
	}
	return provider.ID, true
}

// StrictProviderTags returns the exact channel tags owned by the provider
// lifecycle. Generic bulk channel operations use this list to leave managed
// routes intact.
func StrictProviderTags() []string {
	tags := make([]string, 0)
	for _, provider := range definitions {
		if !provider.capabilities.StrictLifecycle {
			continue
		}
		for _, profile := range providerRouteProfiles(provider) {
			tags = append(tags, providerTag(provider.ID, profile.ID))
		}
	}
	sort.Strings(tags)
	return tags
}

// ValidateManagedRelayChannel is the final outbound configuration gate for
// provider-managed channels. Ordinary channels are intentionally unaffected.
func ValidateManagedRelayChannel(channel model.Channel) error {
	provider, ok := strictProviderForTag(channel.GetTag())
	if !ok {
		return nil
	}
	return validateManagedChannelConfiguration(provider, channel)
}

type managedRelayChannelState struct {
	model.Channel
	PriceID                 int64   `gorm:"column:managed_price_id"`
	PriceProvider           string  `gorm:"column:managed_price_provider"`
	PriceBillingType        string  `gorm:"column:managed_price_billing_type"`
	PriceCurrency           string  `gorm:"column:managed_price_currency"`
	PriceUpstreamModel      string  `gorm:"column:managed_price_upstream_model"`
	PriceInput              float64 `gorm:"column:managed_price_input"`
	PriceOutput             float64 `gorm:"column:managed_price_output"`
	PriceImageOutput        float64 `gorm:"column:managed_price_image_output"`
	PriceFixed              float64 `gorm:"column:managed_price_fixed"`
	PriceCacheRead          float64 `gorm:"column:managed_price_cache_read"`
	PriceCacheWrite         float64 `gorm:"column:managed_price_cache_write"`
	PriceReferenceProtocol  string  `gorm:"column:managed_price_reference_protocol"`
	PriceMaxReferenceImages int     `gorm:"column:managed_price_max_reference_images"`
}

var errManagedRelayRouteUnavailable = errors.New("供应商托管渠道当前不可用")

// LoadManagedRelayChannelForRequest replaces a potentially stale local-cache
// channel with one database-authoritative request snapshot. Channel state,
// ability, attested price, and credential are read by one SQL statement so a
// different instance's fail-close transition cannot leave a mixed view.
func LoadManagedRelayChannelForRequest(cached model.Channel, catalogID, expectedGroup string) (model.Channel, types.ChannelModelPriceSnapshot, bool, error) {
	return loadManagedRelayChannelForRequest(model.DB, cached, catalogID, expectedGroup)
}

func loadManagedRelayChannelForRequest(db *gorm.DB, cached model.Channel, catalogID, expectedGroup string) (model.Channel, types.ChannelModelPriceSnapshot, bool, error) {
	expectedProvider, managed := strictProviderForTag(cached.GetTag())
	if !managed {
		return cached, types.ChannelModelPriceSnapshot{}, false, nil
	}
	expectedGroup = strings.TrimSpace(expectedGroup)
	if expectedGroup == "" || expectedGroup == "auto" {
		return model.Channel{}, types.ChannelModelPriceSnapshot{}, true,
			fmt.Errorf("%w: 缺少确定的路由分组", errManagedRelayRouteUnavailable)
	}
	abilityGroupColumn := "abilities.`group`"
	channelGroupColumn := "channels.`group`"
	if db.Dialector.Name() == "postgres" {
		abilityGroupColumn = `abilities."group"`
		channelGroupColumn = `channels."group"`
	}
	selectColumns := `channels.*,
prices.id AS managed_price_id,
prices.provider AS managed_price_provider,
prices.billing_type AS managed_price_billing_type,
prices.currency AS managed_price_currency,
prices.upstream_model_id AS managed_price_upstream_model,
prices.input_price AS managed_price_input,
prices.output_price AS managed_price_output,
prices.image_output_price AS managed_price_image_output,
prices.fixed_price AS managed_price_fixed,
prices.cache_ratio AS managed_price_cache_read,
prices.reference_protocol AS managed_price_reference_protocol,
prices.max_reference_images AS managed_price_max_reference_images,
prices.cache_creation_ratio AS managed_price_cache_write`
	var state managedRelayChannelState
	err := db.Table("channels").
		Select(selectColumns).
		Joins("JOIN abilities ON abilities.channel_id = channels.id").
		Joins("JOIN channel_model_prices AS prices ON prices.channel_id = channels.id").
		Where("channels.id = ? AND channels.status = ?", cached.Id, common.ChannelStatusEnabled).
		Where("abilities.model = ? AND abilities.enabled = ?", catalogID, true).
		Where(abilityGroupColumn+" = ? AND "+channelGroupColumn+" = ?", expectedGroup, expectedGroup).
		Where(abilityGroupColumn+" = "+channelGroupColumn).
		Where("abilities.priority = channels.priority").
		Where("prices.catalog_id = ? AND prices.available = ? AND prices.synced_at > 0", catalogID, true).
		Where("prices.tested_at >= prices.synced_at AND channels.test_time >= prices.synced_at").
		Clauses(clause.Locking{Strength: "SHARE"}).
		Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Channel{}, types.ChannelModelPriceSnapshot{}, true, errManagedRelayRouteUnavailable
	}
	if err != nil {
		return model.Channel{}, types.ChannelModelPriceSnapshot{}, true, fmt.Errorf("校验供应商托管渠道失败: %w", err)
	}
	currentProvider, currentManaged := strictProviderForTag(state.Channel.GetTag())
	if !currentManaged || currentProvider.ID != expectedProvider.ID {
		return model.Channel{}, types.ChannelModelPriceSnapshot{}, true,
			fmt.Errorf("%w: 渠道归属已变化", errManagedRelayRouteUnavailable)
	}
	if err = validateManagedChannelConfiguration(currentProvider, state.Channel); err != nil {
		return model.Channel{}, types.ChannelModelPriceSnapshot{}, true,
			fmt.Errorf("%w: %v", errManagedRelayRouteUnavailable, err)
	}
	var mapping map[string]string
	if err = common.Unmarshal([]byte(state.Channel.GetModelMapping()), &mapping); err != nil {
		return model.Channel{}, types.ChannelModelPriceSnapshot{}, true,
			fmt.Errorf("%w: 模型映射无效", errManagedRelayRouteUnavailable)
	}
	if !containsString(state.Channel.GetModels(), catalogID) || mapping[catalogID] != state.PriceUpstreamModel {
		return model.Channel{}, types.ChannelModelPriceSnapshot{}, true,
			fmt.Errorf("%w: 模型价格与映射不一致", errManagedRelayRouteUnavailable)
	}
	expectedBillingType := model.ChannelModelBillingToken
	if currentProvider.ID == "yuyu" && catalogID == "nano-banana-2" {
		expectedBillingType = model.ChannelModelBillingFixed
	}
	if state.PriceProvider != currentProvider.ID || state.PriceBillingType != expectedBillingType ||
		!catalogRoutePriceIsSafe(state) ||
		state.PriceInput > maxExternalTokenPriceUSDPerMillion || state.PriceOutput > maxExternalTokenPriceUSDPerMillion ||
		(currentProvider.ID == "yuyu" && catalogID == "gpt-image-2" && !validPositivePrice(state.PriceImageOutput)) {
		return model.Channel{}, types.ChannelModelPriceSnapshot{}, true,
			fmt.Errorf("%w: 价格校验失败", errManagedRelayRouteUnavailable)
	}
	price := types.ChannelModelPriceSnapshot{
		PriceID: state.PriceID, ChannelID: state.Channel.Id, CatalogID: catalogID,
		UpstreamModelID: state.PriceUpstreamModel, Provider: state.PriceProvider,
		BillingType: state.PriceBillingType, Currency: state.PriceCurrency, RoutingGroup: expectedGroup,
		InputPrice: state.PriceInput, OutputPrice: state.PriceOutput, ImageOutputPrice: state.PriceImageOutput, FixedPrice: state.PriceFixed,
		CacheRatio: state.PriceCacheRead, CacheCreationRatio: state.PriceCacheWrite,
		ReferenceProtocol: state.PriceReferenceProtocol, MaxReferenceImages: state.PriceMaxReferenceImages,
	}
	return state.Channel, price, true, nil
}

// CatalogModelRoutableForGroupTx checks the same database-authoritative
// channel, ability, mapping, lifecycle, and price state used by relay. The
// caller supplies its transaction so a default-model preference cannot be
// committed from a stale readiness snapshot.
func CatalogModelRoutableForGroupTx(db *gorm.DB, catalogID, group string) (bool, error) {
	group = strings.TrimSpace(group)
	if db == nil || group == "" || strings.TrimSpace(catalogID) == "" {
		return false, nil
	}
	abilityGroupColumn := "abilities.`group`"
	if db.Dialector.Name() == "postgres" {
		abilityGroupColumn = `abilities."group"`
	}
	states := make([]managedRelayChannelState, 0)
	err := db.Table("channels").
		Select(`channels.*,
prices.id AS managed_price_id,
prices.provider AS managed_price_provider,
prices.billing_type AS managed_price_billing_type,
prices.currency AS managed_price_currency,
prices.upstream_model_id AS managed_price_upstream_model,
prices.input_price AS managed_price_input,
prices.output_price AS managed_price_output,
prices.image_output_price AS managed_price_image_output,
prices.fixed_price AS managed_price_fixed,
prices.cache_ratio AS managed_price_cache_read,
prices.reference_protocol AS managed_price_reference_protocol,
prices.max_reference_images AS managed_price_max_reference_images,
prices.cache_creation_ratio AS managed_price_cache_write`).
		Joins("JOIN abilities ON abilities.channel_id = channels.id").
		Joins("JOIN channel_model_prices AS prices ON prices.channel_id = channels.id AND prices.catalog_id = abilities.model").
		Where("channels.status = ?", common.ChannelStatusEnabled).
		Where("abilities.model = ? AND abilities.enabled = ?", catalogID, true).
		Where(abilityGroupColumn+" = ?", group).
		Where("prices.available = ?", true).
		Clauses(clause.Locking{Strength: "SHARE"}).
		Order("abilities.priority DESC, channels.id ASC").
		Find(&states).Error
	if err != nil {
		return false, fmt.Errorf("校验模型 %s 在分组 %q 的路由失败: %w", catalogID, group, err)
	}
	for _, state := range states {
		channel := state.Channel
		if _, managed := strictProviderForTag(channel.GetTag()); !managed {
			if catalogRoutePriceIsSafe(state) {
				return true, nil
			}
			continue
		}
		if _, _, _, routeErr := loadManagedRelayChannelForRequest(db, channel, catalogID, group); routeErr == nil {
			return true, nil
		} else if !errors.Is(routeErr, errManagedRelayRouteUnavailable) {
			return false, routeErr
		}
	}
	return false, nil
}

func catalogRoutePriceIsSafe(state managedRelayChannelState) bool {
	if state.PriceID <= 0 || state.PriceCurrency != "USD" {
		return false
	}
	switch state.PriceBillingType {
	case model.ChannelModelBillingFixed:
		return validPositivePrice(state.PriceFixed)
	case model.ChannelModelBillingToken:
		return validPositivePrice(state.PriceInput) && validPositivePrice(state.PriceOutput) &&
			validNonNegativePrice(state.PriceImageOutput) && state.PriceImageOutput <= maxExternalTokenPriceUSDPerMillion &&
			validPositivePrice(state.PriceInput/2) && validPositivePrice(state.PriceOutput/state.PriceInput) &&
			validNonNegativePrice(state.PriceCacheRead) && validNonNegativePrice(state.PriceCacheWrite) &&
			validNonNegativePrice(state.PriceCacheWrite*claudeCacheCreationOneHourMultiplier)
	default:
		return false
	}
}

// ValidateManagedChannelUpdate keeps strict provider routes out of the generic
// channel editor. Even apparently harmless full-object updates can race a
// lifecycle transaction and restore stale status or abilities.
func ValidateManagedChannelUpdate(origin, patch model.Channel) error {
	provider, managed := strictProviderForTag(origin.GetTag())
	if !managed {
		if patch.Tag != nil {
			if _, reserved := strictProviderForTag(*patch.Tag); reserved {
				return errors.New("供应商托管标签只能由供应商面板创建")
			}
		}
		return nil
	}
	return fmt.Errorf("供应商 %s 的托管渠道请在供应商面板更新", provider.Name)
}

func providerChannels(providerID string, includeKey bool) ([]model.Channel, error) {
	provider, ok := definition(providerID)
	if !ok {
		return nil, errors.New("未知供应商")
	}
	return providerChannelsWithDB(model.DB, provider, includeKey, false)
}

func providerChannelsWithDB(db *gorm.DB, provider Definition, includeKey, lockRows bool) ([]model.Channel, error) {
	tags := make([]string, 0, len(providerRouteProfiles(provider)))
	for _, profile := range providerRouteProfiles(provider) {
		tags = append(tags, providerTag(provider.ID, profile.ID))
	}
	channels := make([]model.Channel, 0, len(profiles))
	query := db.Where("tag IN ?", tags)
	if !includeKey {
		query = query.Omit("key")
	}
	if lockRows {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Order("id ASC").Find(&channels).Error
	return channels, err
}

func providerConfigureMutex(providerID string) *sync.Mutex {
	mutex, _ := providerConfigureLocks.LoadOrStore(providerID, &sync.Mutex{})
	return mutex.(*sync.Mutex)
}

func acquireProviderAdvisoryLock(tx *gorm.DB, providerID string) error {
	if tx.Dialector.Name() == "postgres" {
		return tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "mujian-provider:"+providerID).Error
	}
	// MySQL and SQLite have no transaction-scoped advisory lock shared by this
	// code path. A durable Option row gives both databases a portable ownership
	// primitive without a schema migration. Updating it acquires a row/write lock
	// before an empty provider channel set can be observed and inserted twice.
	lockKey := "_mujian_provider_lock:" + providerID
	lockRow := model.Option{Key: lockKey, Value: ""}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&lockRow).Error; err != nil {
		return err
	}
	return tx.Model(&model.Option{}).Where("key = ?", lockKey).UpdateColumn("value", "").Error
}

var errProviderConfigurationChanged = errors.New("供应商配置已变更，请重试当前操作")

func providerLifecycleGeneration(db *gorm.DB, providerID string) (string, error) {
	var option model.Option
	err := db.Where("key = ?", "_mujian_provider_generation:"+providerID).Take(&option).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return option.Value, err
}

func requireProviderLifecycleGeneration(db *gorm.DB, providerID, expected string) error {
	current, err := providerLifecycleGeneration(db, providerID)
	if err != nil {
		return err
	}
	if current != expected {
		return errProviderConfigurationChanged
	}
	return nil
}

func advanceProviderLifecycleGeneration(tx *gorm.DB, providerID string) error {
	option := model.Option{Key: "_mujian_provider_generation:" + providerID, Value: uuid.NewString()}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&option).Error
}

// lockedProviderChannels re-reads the provider state immediately before a
// strict lifecycle write. The API key is compared without ever including it
// in an error, preventing a slow sync/test started with an old key from
// attesting or invalidating a newly configured key.
func lockedProviderChannels(tx *gorm.DB, provider Definition, expectedKey string) ([]model.Channel, error) {
	if err := acquireProviderAdvisoryLock(tx, provider.ID); err != nil {
		return nil, err
	}
	channels, err := providerChannelsWithDB(tx, provider, true, true)
	if err != nil {
		return nil, err
	}
	if err = validateManagedProviderChannels(provider, channels); err != nil {
		return nil, err
	}
	for index := range channels {
		if strings.TrimSpace(channels[index].Key) != expectedKey {
			return nil, errProviderConfigurationChanged
		}
		if err = validateManagedChannelConfiguration(provider, channels[index]); err != nil {
			return nil, fmt.Errorf("%w: %v", errProviderConfigurationChanged, err)
		}
	}
	return channels, nil
}

func Statuses() ([]ProviderStatus, error) {
	statuses := make([]ProviderStatus, 0, len(definitions))
	for _, item := range definitions {
		channels, err := providerChannels(item.ID, true)
		if err != nil {
			return nil, err
		}
		status := ProviderStatus{
			Definition:      item,
			StrictLifecycle: item.capabilities.StrictLifecycle,
			Routes:          make([]ProviderRouteStatus, 0, len(channels)),
		}
		providerNames := map[string]struct{}{}
		for _, channel := range channels {
			route := ProviderRouteStatus{
				ChannelID: channel.Id,
				Name:      channel.Name,
				ProfileID: providerProfileID(channel.GetTag()),
				Group:     channel.Group,
				Status:    channel.Status,
				Priority:  channel.GetPriority(),
				Prices:    make([]ProviderPriceStatus, 0),
			}
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
			if err = model.DB.Omit("original_pricing").Where("channel_id = ?", channel.Id).Find(&prices).Error; err != nil {
				return nil, err
			}
			for _, price := range prices {
				priceStatus := ProviderPriceStatus{
					CatalogID:        price.CatalogID,
					UpstreamModelID:  price.UpstreamModelID,
					BillingType:      price.BillingType,
					Currency:         price.Currency,
					InputPrice:       price.InputPrice,
					OutputPrice:      price.OutputPrice,
					ImageOutputPrice: price.ImageOutputPrice,
					FixedPrice:       price.FixedPrice,
					Available:        price.Available,
					SourceURL:        price.SourceURL,
					SourceVersion:    price.SourceVersion,
					SyncedAt:         price.SyncedAt,
					TestedAt:         price.TestedAt,
					LastError:        price.LastError,
				}
				if price.BillingType == model.ChannelModelBillingToken && price.CacheRatio > 0 {
					priceStatus.CacheReadPrice = common.GetPointer(price.InputPrice * price.CacheRatio)
				}
				if price.BillingType == model.ChannelModelBillingToken && price.CacheCreationRatio > 0 {
					cacheWrite5mPrice := price.InputPrice * price.CacheCreationRatio
					priceStatus.CacheWrite5mPrice = common.GetPointer(cacheWrite5mPrice)
					if price.Provider == "zenmux" || price.Provider == "tabcode" {
						priceStatus.CacheWrite1hPrice = common.GetPointer(cacheWrite5mPrice * (anthropicSonnet46CacheWrite1h / anthropicSonnet46CacheWrite5m))
					}
				}
				route.Prices = append(route.Prices, priceStatus)
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
			sort.Slice(route.Prices, func(left, right int) bool {
				return route.Prices[left].CatalogID < route.Prices[right].CatalogID
			})
			status.Routes = append(status.Routes, route)
		}
		if status.Configured {
			status.TestReady = true
			status.EnableReady = true
			if item.capabilities.StrictLifecycle {
				status.TestReady = validateProviderTestReady(model.DB, item, channels) == nil
				status.EnableReady = validateProviderActivationReady(model.DB, item, channels) == nil
			}
		}
		status.ModelCount = len(providerNames)
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func providerProfileID(tag string) string {
	parts := strings.Split(tag, ":")
	if len(parts) != 3 {
		return ""
	}
	return parts[2]
}

func Configure(providerID, key string) error {
	return configureProvider(providerID, key, common.GetPointer("default"))
}

func normalizeProviderGroup(value string) (string, error) {
	group := strings.TrimSpace(value)
	if group == "" {
		return "", errors.New("路由分组不能为空")
	}
	if strings.Contains(group, ",") {
		return "", errors.New("供应商托管渠道只能属于一个路由分组")
	}
	if len(group) > 64 {
		return "", errors.New("路由分组名称过长")
	}
	if !ratio_setting.ContainsGroupRatio(group) {
		return "", fmt.Errorf("路由分组 %q 尚未创建", group)
	}
	return group, nil
}

// ConfigureInGroup creates or repairs the provider-owned route and optionally
// places it into one validated routing group in the same lifecycle transaction.
func ConfigureInGroup(providerID, key, requestedGroup string) error {
	return configureProvider(providerID, key, &requestedGroup)
}

// ConfigureFromAPI distinguishes an omitted group on key rotation from a
// missing or explicitly blank group during first-time setup.
func ConfigureFromAPI(providerID, key string, requestedGroup *string) error {
	return configureProvider(providerID, key, requestedGroup)
}

func configureProvider(providerID, key string, requestedGroup *string) error {
	provider, ok := definition(providerID)
	if !ok {
		return errors.New("未知供应商")
	}
	normalizedGroup := ""
	if requestedGroup != nil {
		var groupErr error
		normalizedGroup, groupErr = normalizeProviderGroup(*requestedGroup)
		if groupErr != nil {
			return groupErr
		}
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
	mutex := providerConfigureMutex(providerID)
	mutex.Lock()
	defer mutex.Unlock()
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if lockErr := acquireProviderAdvisoryLock(tx, providerID); lockErr != nil {
			return lockErr
		}
		existing, queryErr := providerChannelsWithDB(tx, provider, true, true)
		if queryErr != nil {
			return queryErr
		}
		existingByTag := make(map[string]*model.Channel, len(existing))
		for index := range existing {
			channel := &existing[index]
			if existingByTag[channel.GetTag()] != nil {
				return fmt.Errorf("供应商 %s 存在重复渠道，请先清理", provider.Name)
			}
			existingByTag[channel.GetTag()] = channel
		}
		if key == "" && len(existing) == 0 {
			return errors.New("首次配置必须填写 API Key")
		}
		if len(existing) == 0 && requestedGroup == nil {
			return errors.New("首次配置必须明确填写路由分组")
		}
		effectiveGroup := normalizedGroup
		if requestedGroup == nil && len(existing) > 0 {
			for index := range existing {
				group := strings.TrimSpace(existing[index].Group)
				if group == "" {
					return fmt.Errorf("供应商 %s 的现有渠道缺少路由分组，请在 Root 页面显式修复", provider.Name)
				}
				if effectiveGroup == "" {
					effectiveGroup = group
				} else if effectiveGroup != group {
					return fmt.Errorf("供应商 %s 的现有渠道路由分组不一致，请在 Root 页面显式修复", provider.Name)
				}
			}
		}
		requiresResync := false
		if key != "" {
			for index := range existing {
				if key != strings.TrimSpace(existing[index].Key) {
					requiresResync = true
					break
				}
			}
		}
		if provider.capabilities.StrictLifecycle {
			for index := range existing {
				if validateManagedChannelConfiguration(provider, existing[index]) != nil {
					requiresResync = true
					break
				}
			}
		}
		for _, profile := range providerRouteProfiles(provider) {
			tag := providerTag(providerID, profile.ID)
			if channel := existingByTag[tag]; channel != nil {
				updates := map[string]any{
					"base_url": provider.BaseURL,
					"priority": profile.Priority[providerID],
					"type":     providerChannelType(provider, profile.ID),
				}
				if provider.capabilities.StrictLifecycle {
					updates["open_ai_organization"] = nil
					updates["other"] = ""
					updates["status_code_mapping"] = nil
					updates["auto_ban"] = 1
					updates["setting"] = nil
					updates["param_override"] = nil
					updates["settings"] = ""
					updates["channel_info"] = model.ChannelInfo{}
				}
				if provider.capabilities.ManageHeaderOverride {
					updates["header_override"] = provider.capabilities.HeaderOverride
				}
				if id := providerTestCatalogID(provider); id != "" {
					updates["test_model"] = id
				}
				if key != "" {
					updates["key"] = key
				}
				if updateErr := tx.Model(channel).Updates(updates).Error; updateErr != nil {
					return updateErr
				}
				continue
			}
			priority := profile.Priority[providerID]
			weight := uint(100)
			autoBan := 1
			baseURL := provider.BaseURL
			modelMapping := "{}"
			var headerOverride *string
			if provider.capabilities.ManageHeaderOverride {
				headerOverride = common.GetPointer(provider.capabilities.HeaderOverride)
			}
			var testModel *string
			if id := providerTestCatalogID(provider); id != "" {
				testModel = common.GetPointer(id)
			}
			channel := model.Channel{
				Type: providerChannelType(provider, profile.ID), Key: key, Status: common.ChannelStatusManuallyDisabled,
				Name: fmt.Sprintf("幕间 · %s · %s", provider.Name, profileName(profile.ID)), Weight: &weight,
				CreatedTime: common.GetTimestamp(), BaseURL: &baseURL, Models: "", Group: effectiveGroup,
				ModelMapping: &modelMapping, Priority: &priority, AutoBan: &autoBan, Tag: &tag,
				HeaderOverride: headerOverride, TestModel: testModel,
			}
			if createErr := tx.Create(&channel).Error; createErr != nil {
				return createErr
			}
			if createErr := channel.AddAbilities(tx); createErr != nil {
				return createErr
			}
		}
		if requiresResync && provider.capabilities.StrictLifecycle {
			if err := failClosedProviderTx(tx, provider, existing, "供应商配置已更新，请重新同步价格并完成鉴权测试", true); err != nil {
				return err
			}
			return advanceProviderLifecycleGeneration(tx, provider.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	model.InitChannelCache()
	return nil
}

// SetGroup moves every route owned by one provider as a single transaction.
// It deliberately shares the provider advisory lock with sync, test, configure,
// and enable so channel rows and abilities cannot diverge under concurrency.
func SetGroup(providerID, value string) error {
	provider, ok := definition(providerID)
	if !ok {
		return errors.New("未知供应商")
	}
	group, err := normalizeProviderGroup(value)
	if err != nil {
		return err
	}
	mutex := providerConfigureMutex(providerID)
	mutex.Lock()
	defer mutex.Unlock()
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if lockErr := acquireProviderAdvisoryLock(tx, providerID); lockErr != nil {
			return lockErr
		}
		channels, queryErr := providerChannelsWithDB(tx, provider, true, true)
		if queryErr != nil {
			return queryErr
		}
		if len(channels) == 0 {
			return errors.New("供应商尚未配置")
		}
		for index := range channels {
			channel := &channels[index]
			if channel.Group == group {
				continue
			}
			if updateErr := tx.Model(channel).Update("group", group).Error; updateErr != nil {
				return updateErr
			}
			channel.Group = group
			if updateErr := channel.UpdateAbilities(tx); updateErr != nil {
				return updateErr
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	model.InitChannelCache()
	return nil
}

func SetEnabled(providerID string, enabled bool) error {
	provider, ok := definition(providerID)
	if !ok {
		return errors.New("未知供应商")
	}
	mutex := providerConfigureMutex(providerID)
	mutex.Lock()
	defer mutex.Unlock()
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
	if !provider.capabilities.StrictLifecycle {
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
	if err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := acquireProviderAdvisoryLock(tx, providerID); err != nil {
			return err
		}
		lockedChannels, err := providerChannelsWithDB(tx, provider, true, true)
		if err != nil {
			return err
		}
		if len(lockedChannels) == 0 {
			return errors.New("供应商尚未配置")
		}
		if enabled {
			if err := validateProviderActivationReady(tx, provider, lockedChannels); err != nil {
				return err
			}
		}
		eligibleCount := 0
		for index := range lockedChannels {
			channel := lockedChannels[index]
			if enabled && strings.TrimSpace(channel.Models) == "" {
				continue
			}
			if enabled {
				eligibleCount++
			}
			update := tx.Model(&model.Channel{}).Where("id = ?", channel.Id)
			if enabled {
				update = update.Where("test_time = ?", channel.TestTime)
			}
			result := update.Update("status", status)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("渠道 %s 状态已变化，请刷新后重试", channel.Name)
			}
			channel.Status = status
			if err := channel.UpdateAbilities(tx); err != nil {
				return err
			}
		}
		if enabled && eligibleCount == 0 {
			return errors.New("供应商尚未同步可用模型")
		}
		return nil
	}); err != nil {
		return err
	}
	model.InitChannelCache()
	return nil
}

func Test(providerID string) (int, int, error) {
	mutex := providerConfigureMutex(providerID)
	mutex.Lock()
	defer mutex.Unlock()
	provider, channels, key, err := configuredProvider(providerID)
	if err != nil {
		return 0, 0, err
	}
	generation := ""
	if provider.capabilities.StrictLifecycle {
		if generation, err = prepareProviderTest(provider, key); err != nil {
			return 0, 0, err
		}
	}
	started := time.Now()
	models, err := testProviderConnection(context.Background(), provider, key)
	elapsed := int(time.Since(started).Milliseconds())
	if !provider.capabilities.StrictLifecycle {
		if persistErr := recordLegacyProviderTest(provider, channels, models, elapsed, err); persistErr != nil {
			return 0, elapsed, fmt.Errorf("写入供应商测试结果失败: %w", persistErr)
		}
		model.InitChannelCache()
		if err != nil {
			return 0, elapsed, err
		}
		return len(models), elapsed, nil
	}
	if err != nil {
		persistErr := recordProviderTestFailure(provider, key, generation, elapsed, err)
		if persistErr != nil {
			return 0, elapsed, fmt.Errorf("%v；写入 fail-closed 状态失败: %w", err, persistErr)
		}
		return 0, elapsed, err
	}
	if err = recordProviderTestSuccess(provider, key, generation, models, elapsed); err != nil {
		if errors.Is(err, errProviderConfigurationChanged) {
			return 0, elapsed, err
		}
		closeErr := recordProviderTestFailure(provider, key, generation, elapsed, err)
		if closeErr != nil {
			return 0, elapsed, fmt.Errorf("%v；写入 fail-closed 状态失败: %w", err, closeErr)
		}
		return 0, elapsed, err
	}
	model.InitChannelCache()
	return len(models), elapsed, nil
}

func recordLegacyProviderTest(provider Definition, channels []model.Channel, models []string, elapsed int, probeErr error) error {
	now := common.GetTimestamp()
	return model.DB.Transaction(func(tx *gorm.DB) error {
		for index := range channels {
			channel := &channels[index]
			if err := tx.Model(channel).Updates(map[string]any{
				"base_url": provider.BaseURL, "test_time": now, "response_time": elapsed,
			}).Error; err != nil {
				return err
			}
			priceUpdates := map[string]any{"tested_at": now}
			if probeErr != nil {
				priceUpdates["last_error"] = probeErr.Error()
				priceUpdates["available"] = false
			}
			if err := tx.Model(&model.ChannelModelPrice{}).Where("channel_id = ?", channel.Id).Updates(priceUpdates).Error; err != nil {
				return err
			}
			if probeErr == nil {
				if err := tx.Model(&model.ChannelModelPrice{}).
					Where("channel_id = ? AND billing_type != ?", channel.Id, "unavailable").
					Update("available", false).Error; err != nil {
					return err
				}
				if len(models) > 0 {
					if err := tx.Model(&model.ChannelModelPrice{}).
						Where("channel_id = ? AND billing_type != ? AND upstream_model_id IN ?", channel.Id, "unavailable", models).
						Updates(map[string]any{"available": true, "last_error": ""}).Error; err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func Sync(providerID string) (int, error) {
	mutex := providerConfigureMutex(providerID)
	mutex.Lock()
	defer mutex.Unlock()
	provider, channels, key, err := configuredProvider(providerID)
	if err != nil {
		return 0, err
	}
	generation := ""
	if provider.capabilities.StrictLifecycle {
		generation, err = prepareProviderSync(provider, key)
		if err != nil {
			return 0, err
		}
		model.InitChannelCache()
	}
	handleFailure := func(cause error) (int, error) {
		if provider.capabilities.StrictLifecycle {
			return syncFailure(provider, key, generation, cause)
		}
		return 0, cause
	}
	snapshot, err := resolveProviderSnapshot(context.Background(), provider, key)
	if err != nil {
		return handleFailure(err)
	}
	models := snapshot.Models
	pricing := snapshot.Pricing
	if snapshot.ResolvedBaseURL != "" {
		provider.BaseURL = snapshot.ResolvedBaseURL
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
	for _, entry := range providerCatalog(provider) {
		if pricing.Partial && !hasPricedCandidateForProvider(providerID, entry, pricingByID) {
			continue
		}
		upstreamID, item, reason := matchCatalogEntry(providerID, entry, modelSet, pricingByID)
		profile := profileID(entry)
		touchedProfiles[profile] = true
		if reason != "" {
			matchedByProfile[profile] = append(matchedByProfile[profile], unavailablePrice(provider, entry, upstreamID, item, pricing.Version, reason))
			continue
		}
		price, marshalErr := toChannelPrice(provider, entry.ID, upstreamID, item, pricing.Version)
		if marshalErr != nil {
			matchedByProfile[profile] = append(matchedByProfile[profile], unavailablePrice(provider, entry, upstreamID, item, pricing.Version, marshalErr.Error()))
			continue
		}
		matchedByProfile[profile] = append(matchedByProfile[profile], price)
		mappingByProfile[profile][entry.ID] = upstreamID
	}
	if provider.capabilities.StrictLifecycle {
		total, persistErr := persistStrictProviderSync(provider, key, generation, pricing, matchedByProfile, mappingByProfile, touchedProfiles)
		if persistErr != nil {
			if errors.Is(persistErr, errProviderConfigurationChanged) {
				return 0, persistErr
			}
			return handleFailure(persistErr)
		}
		model.InitChannelCache()
		return total, nil
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
	for _, profile := range providerRouteProfiles(provider) {
		if pricing.Partial && !touchedProfiles[profile.ID] {
			continue
		}
		channel := channelsByProfile[profile.ID]
		if channel == nil {
			return handleFailure(fmt.Errorf("供应商路由 %s 缺失", profile.ID))
		}
		prices := matchedByProfile[profile.ID]
		mapping := mappingByProfile[profile.ID]
		ids := make([]string, 0, len(mapping))
		for id := range mapping {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		mappingJSON, _ := common.Marshal(mapping)
		channel.BaseURL = common.GetPointer(provider.BaseURL)
		channel.Models = strings.Join(ids, ",")
		channel.ModelMapping = common.GetPointer(string(mappingJSON))
		if len(ids) > 0 {
			testModel := providerTestCatalogID(provider)
			if testModel == "" {
				testModel = ids[0]
			}
			channel.TestModel = common.GetPointer(testModel)
		}
		if len(ids) == 0 {
			channel.Status = common.ChannelStatusManuallyDisabled
		}
		channel.TestTime = common.GetTimestamp()
		if err = channel.Update(); err != nil {
			return handleFailure(err)
		}
		if err = model.UpsertChannelModelPrices(prices, channel.Id); err != nil {
			return handleFailure(err)
		}
		total += len(ids)
	}
	model.InitChannelCache()
	return total, nil
}

func persistStrictProviderSync(
	provider Definition,
	expectedKey string,
	expectedGeneration string,
	pricing pricingSnapshot,
	pricesByProfile map[string][]model.ChannelModelPrice,
	mappingByProfile map[string]map[string]string,
	touchedProfiles map[string]bool,
) (int, error) {
	total := 0
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		channels, err := lockedProviderChannels(tx, provider, expectedKey)
		if err != nil {
			return err
		}
		if err = requireProviderLifecycleGeneration(tx, provider.ID, expectedGeneration); err != nil {
			return err
		}
		if err = markProviderPendingTx(tx, channels); err != nil {
			return err
		}
		channelsByProfile := make(map[string]*model.Channel, len(channels))
		for index := range channels {
			channel := &channels[index]
			channelsByProfile[providerProfileID(channel.GetTag())] = channel
		}
		for _, profile := range providerRouteProfiles(provider) {
			if pricing.Partial && !touchedProfiles[profile.ID] {
				continue
			}
			channel := channelsByProfile[profile.ID]
			if channel == nil {
				return fmt.Errorf("供应商路由 %s 缺失", profile.ID)
			}
			mapping := mappingByProfile[profile.ID]
			ids := make([]string, 0, len(mapping))
			for id := range mapping {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			mappingJSON, err := common.Marshal(mapping)
			if err != nil {
				return err
			}
			channel.BaseURL = common.GetPointer(provider.BaseURL)
			channel.Models = strings.Join(ids, ",")
			channel.ModelMapping = common.GetPointer(string(mappingJSON))
			channel.Status = common.ChannelStatusManuallyDisabled
			channel.TestTime = 0
			channel.ResponseTime = 0
			if len(ids) > 0 {
				testModel := providerTestCatalogID(provider)
				if testModel == "" {
					testModel = ids[0]
				}
				channel.TestModel = common.GetPointer(testModel)
			}
			updates := map[string]any{
				"base_url": provider.BaseURL, "models": channel.Models, "model_mapping": channel.GetModelMapping(),
				"status": channel.Status, "test_time": 0, "response_time": 0,
			}
			if channel.TestModel != nil {
				updates["test_model"] = *channel.TestModel
			}
			if err = tx.Model(channel).Updates(updates).Error; err != nil {
				return err
			}
			if err = channel.UpdateAbilities(tx); err != nil {
				return err
			}
			prices := append([]model.ChannelModelPrice(nil), pricesByProfile[profile.ID]...)
			for index := range prices {
				prices[index].Available = false
			}
			if err = model.UpsertUntestedChannelModelPricesTx(tx, prices, channel.Id); err != nil {
				return err
			}
			total += len(ids)
		}
		return advanceProviderLifecycleGeneration(tx, provider.ID)
	})
	return total, err
}

func validateChannelActivation(db *gorm.DB, channel model.Channel) error {
	var latestSync int64
	if err := db.Model(&model.ChannelModelPrice{}).
		Where("channel_id = ?", channel.Id).
		Select("COALESCE(MAX(synced_at), 0)").
		Scan(&latestSync).Error; err != nil {
		return err
	}
	if latestSync <= 0 {
		return fmt.Errorf("渠道 %s 尚未同步价格", channel.Name)
	}
	if channel.TestTime <= 0 || channel.TestTime < latestSync {
		return fmt.Errorf("渠道 %s 需在最新价格同步后完成鉴权测试", channel.Name)
	}
	var mapping map[string]string
	if err := common.Unmarshal([]byte(channel.GetModelMapping()), &mapping); err != nil {
		return fmt.Errorf("渠道 %s 的模型映射无效", channel.Name)
	}
	validated := 0
	seen := make(map[string]struct{})
	for _, catalogID := range channel.GetModels() {
		catalogID = strings.TrimSpace(catalogID)
		if catalogID == "" {
			continue
		}
		if _, duplicate := seen[catalogID]; duplicate {
			return fmt.Errorf("渠道 %s 存在重复模型", channel.Name)
		}
		seen[catalogID] = struct{}{}
		upstreamID := strings.TrimSpace(mapping[catalogID])
		if upstreamID == "" {
			return fmt.Errorf("渠道 %s 缺少模型 %s 的映射", channel.Name, catalogID)
		}
		var price model.ChannelModelPrice
		err := db.Where(
			"channel_id = ? AND catalog_id = ? AND upstream_model_id = ? AND available = ? AND billing_type != ? AND synced_at > 0 AND tested_at >= synced_at",
			channel.Id, catalogID, upstreamID, true, "unavailable",
		).First(&price).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("渠道 %s 的模型 %s 没有经过鉴权测试的可用价格", channel.Name, catalogID)
		}
		if err != nil {
			return err
		}
		validated++
	}
	if validated == 0 {
		return fmt.Errorf("渠道 %s 没有经过鉴权测试的可用价格", channel.Name)
	}
	return nil
}

func validateProviderActivationReady(db *gorm.DB, provider Definition, channels []model.Channel) error {
	if err := validateManagedProviderChannels(provider, channels); err != nil {
		return err
	}
	eligibleCount := 0
	for _, channel := range channels {
		if strings.TrimSpace(channel.Models) == "" {
			continue
		}
		eligibleCount++
		if err := validateManagedChannelConfiguration(provider, channel); err != nil {
			return err
		}
		if err := validateChannelActivation(db, channel); err != nil {
			return err
		}
	}
	if eligibleCount == 0 {
		return fmt.Errorf("供应商 %s 没有可启用路由", provider.Name)
	}
	return nil
}

// validateProviderTestReady prevents a paid Messages probe when the current
// synchronized snapshot cannot possibly be attested. In particular, key
// rotation and sync failure deliberately preserve evidence timestamps while
// invalidating billing rows, so SyncedAt alone is not a readiness signal.
func validateProviderTestReady(db *gorm.DB, provider Definition, channels []model.Channel) error {
	if err := validateManagedProviderChannels(provider, channels); err != nil {
		return err
	}
	testCatalogID := providerTestCatalogID(provider)
	for _, channel := range channels {
		if err := validateManagedChannelConfiguration(provider, channel); err != nil {
			return err
		}
		if testCatalogID == "" && provider.ID == "yuyu" && len(channel.GetModels()) > 0 {
			testCatalogID = channel.GetModels()[0]
		}
		if testCatalogID == "" || !containsString(channel.GetModels(), testCatalogID) {
			continue
		}
		var mapping map[string]string
		if err := common.Unmarshal([]byte(channel.GetModelMapping()), &mapping); err != nil {
			return fmt.Errorf("%s 的模型映射无效，请先重新同步", provider.Name)
		}
		upstreamID := strings.TrimSpace(mapping[testCatalogID])
		if upstreamID == "" {
			return fmt.Errorf("%s 尚未同步测试模型，请先重新同步", provider.Name)
		}
		var count int64
		if err := db.Model(&model.ChannelModelPrice{}).
			Where("channel_id = ? AND catalog_id = ? AND upstream_model_id = ? AND billing_type != ? AND synced_at > 0",
				channel.Id, testCatalogID, upstreamID, "unavailable").
			Count(&count).Error; err != nil {
			return err
		}
		if count == 1 {
			return nil
		}
	}
	return fmt.Errorf("%s 当前同步快照不可测试，请先重新同步", provider.Name)
}

func validateManagedChannelConfiguration(provider Definition, channel model.Channel) error {
	if strings.TrimSpace(channel.Key) == "" {
		return fmt.Errorf("渠道 %s 缺少 API Key", channel.Name)
	}
	if strings.TrimSpace(channel.GetBaseURL()) != provider.BaseURL {
		return fmt.Errorf("渠道 %s 的服务地址已被修改，请重新保存供应商配置", channel.Name)
	}
	if provider.ID == "tabcode" {
		if err := validateTabCodeKiroURL(channel.GetBaseURL()); err != nil {
			return err
		}
	}

	selectedProfileID := ""
	for _, profile := range providerRouteProfiles(provider) {
		if channel.GetTag() == providerTag(provider.ID, profile.ID) {
			selectedProfileID = profile.ID
			if channel.GetPriority() != profile.Priority[provider.ID] {
				return fmt.Errorf("渠道 %s 的优先级已被修改，请重新保存供应商配置", channel.Name)
			}
			break
		}
	}
	if selectedProfileID == "" {
		return fmt.Errorf("渠道 %s 的供应商标签无效", channel.Name)
	}
	if channel.Type != providerChannelType(provider, selectedProfileID) {
		return fmt.Errorf("渠道 %s 的协议类型已被修改，请重新保存供应商配置", channel.Name)
	}
	if !equalHeaderOverride(channel.HeaderOverride, provider.capabilities.HeaderOverride) {
		return fmt.Errorf("渠道 %s 的鉴权头配置已被修改，请重新保存供应商配置", channel.Name)
	}
	if optionalString(channel.OpenAIOrganization) != "" || strings.TrimSpace(channel.Other) != "" ||
		optionalString(channel.StatusCodeMapping) != "" || optionalString(channel.Setting) != "" ||
		optionalString(channel.ParamOverride) != "" || strings.TrimSpace(channel.OtherSettings) != "" {
		return fmt.Errorf("渠道 %s 包含未经允许的请求改写配置，请重新保存供应商配置", channel.Name)
	}
	if channel.AutoBan != nil && *channel.AutoBan != 1 {
		return fmt.Errorf("渠道 %s 的自动禁用配置已被修改，请重新保存供应商配置", channel.Name)
	}
	if channel.ChannelInfo.IsMultiKey || channel.ChannelInfo.MultiKeySize != 0 ||
		len(channel.ChannelInfo.MultiKeyStatusList) > 0 || len(channel.ChannelInfo.MultiKeyDisabledReason) > 0 ||
		len(channel.ChannelInfo.MultiKeyDisabledTime) > 0 || channel.ChannelInfo.MultiKeyPollingIndex != 0 ||
		channel.ChannelInfo.MultiKeyMode != "" {
		return fmt.Errorf("渠道 %s 不允许使用通用多 Key 配置，请在供应商面板更新 Key", channel.Name)
	}
	if expected := providerTestCatalogID(provider); expected != "" &&
		(channel.TestModel == nil || strings.TrimSpace(*channel.TestModel) != expected) {
		return fmt.Errorf("渠道 %s 的测试模型已被修改，请重新保存供应商配置", channel.Name)
	}

	allowedCatalog := make(map[string]CatalogEntry, len(provider.capabilities.CatalogIDs))
	for _, entry := range providerCatalog(provider) {
		if selectedProfileID == profileID(entry) {
			allowedCatalog[entry.ID] = entry
		}
	}
	var mapping map[string]string
	if err := common.Unmarshal([]byte(channel.GetModelMapping()), &mapping); err != nil {
		return fmt.Errorf("渠道 %s 的模型映射无效", channel.Name)
	}
	modelCount := 0
	for _, catalogID := range strings.Split(channel.Models, ",") {
		catalogID = strings.TrimSpace(catalogID)
		if catalogID == "" {
			continue
		}
		modelCount++
		entry, ok := allowedCatalog[catalogID]
		if !ok || !containsString(providerAliases(provider.ID, entry), mapping[catalogID]) {
			return fmt.Errorf("渠道 %s 的模型映射已被修改，请重新同步", channel.Name)
		}
	}
	if len(mapping) != modelCount {
		return fmt.Errorf("渠道 %s 的模型映射已被修改，请重新同步", channel.Name)
	}
	return nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func validateManagedProviderChannels(provider Definition, channels []model.Channel) error {
	expectedTags := make(map[string]struct{}, len(providerRouteProfiles(provider)))
	for _, profile := range providerRouteProfiles(provider) {
		expectedTags[providerTag(provider.ID, profile.ID)] = struct{}{}
	}
	if len(channels) != len(expectedTags) {
		return fmt.Errorf("供应商 %s 的托管渠道数量异常，请重新保存配置", provider.Name)
	}
	seen := make(map[string]struct{}, len(channels))
	key := ""
	for _, channel := range channels {
		tag := channel.GetTag()
		if _, ok := expectedTags[tag]; !ok {
			return fmt.Errorf("供应商 %s 的托管渠道标签异常，请重新保存配置", provider.Name)
		}
		if _, duplicate := seen[tag]; duplicate {
			return fmt.Errorf("供应商 %s 存在重复渠道，请先清理", provider.Name)
		}
		seen[tag] = struct{}{}
		channelKey := strings.TrimSpace(channel.Key)
		if channelKey == "" {
			return fmt.Errorf("供应商 %s 的托管渠道缺少 API Key", provider.Name)
		}
		if key == "" {
			key = channelKey
		} else if channelKey != key {
			return fmt.Errorf("供应商 %s 的托管渠道 API Key 不一致", provider.Name)
		}
	}
	return nil
}

func equalHeaderOverride(actual *string, expected string) bool {
	decode := func(raw string) (map[string]string, bool) {
		result := make(map[string]string)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return result, true
		}
		var values map[string]any
		if err := common.Unmarshal([]byte(raw), &values); err != nil {
			return nil, false
		}
		for name, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			normalizedName := strings.ToLower(strings.TrimSpace(name))
			if normalizedName == "" {
				return nil, false
			}
			if _, duplicate := result[normalizedName]; duplicate {
				return nil, false
			}
			result[normalizedName] = text
		}
		return result, true
	}
	actualRaw := ""
	if actual != nil {
		actualRaw = *actual
	}
	actualValues, actualOK := decode(actualRaw)
	expectedValues, expectedOK := decode(expected)
	if !actualOK || !expectedOK || len(actualValues) != len(expectedValues) {
		return false
	}
	for name, value := range expectedValues {
		if actualValues[name] != value {
			return false
		}
	}
	return true
}

func markProviderPendingTx(tx *gorm.DB, channels []model.Channel) error {
	for index := range channels {
		channel := &channels[index]
		if err := tx.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
			"status": common.ChannelStatusManuallyDisabled, "test_time": 0, "response_time": 0,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channel.Id).Delete(&model.Ability{}).Error; err != nil {
			return err
		}
		channel.Status = common.ChannelStatusManuallyDisabled
		channel.TestTime = 0
		channel.ResponseTime = 0
	}
	return nil
}

func prepareProviderSync(provider Definition, expectedKey string) (string, error) {
	generation := ""
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		channels, err := lockedProviderChannels(tx, provider, expectedKey)
		if err != nil {
			return err
		}
		if err = failClosedProviderTx(tx, provider, channels, "价格同步中，请稍候", true); err != nil {
			return err
		}
		if err = advanceProviderLifecycleGeneration(tx, provider.ID); err != nil {
			return err
		}
		generation, err = providerLifecycleGeneration(tx, provider.ID)
		return err
	})
	return generation, err
}

func prepareProviderTest(provider Definition, expectedKey string) (string, error) {
	generation := ""
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		channels, err := lockedProviderChannels(tx, provider, expectedKey)
		if err != nil {
			return err
		}
		if err = validateProviderTestReady(tx, provider, channels); err != nil {
			return err
		}
		generation, err = providerLifecycleGeneration(tx, provider.ID)
		return err
	})
	return generation, err
}

func recordProviderTestSuccess(provider Definition, expectedKey, expectedGeneration string, models []string, elapsed int) error {
	now := common.GetTimestamp()
	matchedPrices := int64(0)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		channels, err := lockedProviderChannels(tx, provider, expectedKey)
		if err != nil {
			return err
		}
		if err = requireProviderLifecycleGeneration(tx, provider.ID, expectedGeneration); err != nil {
			return err
		}
		for index := range channels {
			channel := &channels[index]
			if err := tx.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
				"base_url": provider.BaseURL, "test_time": now, "response_time": elapsed,
			}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.ChannelModelPrice{}).
				Where("channel_id = ? AND billing_type != ?", channel.Id, "unavailable").
				Updates(map[string]any{
					"available": false, "tested_at": 0, "last_error": "鉴权测试未返回该已同步模型",
				}).Error; err != nil {
				return err
			}
			if len(models) == 0 {
				continue
			}
			result := tx.Model(&model.ChannelModelPrice{}).
				Where("channel_id = ? AND billing_type != ? AND synced_at > 0 AND upstream_model_id IN ?",
					channel.Id, "unavailable", models).
				Updates(map[string]any{"available": true, "tested_at": now, "last_error": ""})
			if result.Error != nil {
				return result.Error
			}
			matchedPrices += result.RowsAffected
		}
		if matchedPrices == 0 {
			return errors.New("鉴权测试成功，但没有匹配已同步的可用价格，请先重新同步")
		}
		return advanceProviderLifecycleGeneration(tx, provider.ID)
	})
}

func recordProviderTestFailure(provider Definition, expectedKey, expectedGeneration string, elapsed int, cause error) error {
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		channels, err := lockedProviderChannels(tx, provider, expectedKey)
		if err != nil {
			return err
		}
		if err = requireProviderLifecycleGeneration(tx, provider.ID, expectedGeneration); err != nil {
			return err
		}
		if err = failClosedProviderTx(tx, provider, channels, cause.Error(), false); err != nil {
			return err
		}
		for index := range channels {
			if err = tx.Model(&model.Channel{}).Where("id = ?", channels[index].Id).Updates(map[string]any{
				"base_url": provider.BaseURL, "response_time": elapsed,
			}).Error; err != nil {
				return err
			}
		}
		return advanceProviderLifecycleGeneration(tx, provider.ID)
	})
	if err == nil {
		model.InitChannelCache()
	}
	return err
}

func syncFailure(provider Definition, expectedKey, expectedGeneration string, cause error) (int, error) {
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		channels, err := lockedProviderChannels(tx, provider, expectedKey)
		if err != nil {
			return err
		}
		if err = requireProviderLifecycleGeneration(tx, provider.ID, expectedGeneration); err != nil {
			return err
		}
		if err = failClosedProviderTx(tx, provider, channels, cause.Error(), true); err != nil {
			return err
		}
		return advanceProviderLifecycleGeneration(tx, provider.ID)
	})
	if err != nil {
		return 0, fmt.Errorf("%v；写入 fail-closed 状态失败: %w", cause, err)
	}
	model.InitChannelCache()
	return 0, cause
}

func failClosedProviderTx(tx *gorm.DB, provider Definition, channels []model.Channel, reason string, invalidateSnapshot bool) error {
	for index := range channels {
		channel := &channels[index]
		if err := tx.Model(&model.Channel{}).Where("id = ?", channel.Id).Updates(map[string]any{
			"status": common.ChannelStatusManuallyDisabled, "test_time": 0,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channel.Id).Delete(&model.Ability{}).Error; err != nil {
			return err
		}
		priceUpdates := map[string]any{
			"available": false, "tested_at": 0, "last_error": reason,
		}
		if invalidateSnapshot {
			priceUpdates["billing_type"] = "unavailable"
		}
		result := tx.Model(&model.ChannelModelPrice{}).
			Where("channel_id = ?", channel.Id).
			Updates(priceUpdates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			prices := providerFailurePrices(provider, *channel, reason)
			if len(prices) > 0 {
				if err := tx.Create(&prices).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func providerFailurePrices(provider Definition, channel model.Channel, reason string) []model.ChannelModelPrice {
	parts := strings.Split(channel.GetTag(), ":")
	if len(parts) != 3 {
		return nil
	}
	prices := make([]model.ChannelModelPrice, 0)
	for _, entry := range providerCatalog(provider) {
		if profileID(entry) != parts[2] {
			continue
		}
		upstreamID := ""
		if candidates := providerAliases(provider.ID, entry); len(candidates) > 0 {
			upstreamID = candidates[0]
		}
		price := unavailablePrice(provider, entry, upstreamID, pricingItem{}, "", reason)
		price.ChannelID = channel.Id
		prices = append(prices, price)
	}
	return prices
}

func CatalogAvailabilityList() ([]CatalogAvailability, error) {
	prices, err := model.ListAvailableChannelModelPricesAll()
	if err != nil {
		return nil, err
	}
	prices, err = filterValidatedManagedPrices(model.DB, prices, "")
	if err != nil {
		return nil, err
	}
	snapshots, err := model.ListChannelModelPriceSnapshotsAll()
	if err != nil {
		return nil, err
	}
	return catalogAvailabilityList(prices, snapshots), nil
}

// CatalogAvailabilityListForGroup limits catalog state to channels that
// explicitly belong to group. The global variant remains available to Root
// provider management.
func CatalogAvailabilityListForGroup(group string) ([]CatalogAvailability, error) {
	prices, err := model.ListRoutableChannelModelPricesForGroupAll(group, nil)
	if err != nil {
		return nil, err
	}
	prices, err = filterValidatedManagedPrices(model.DB, prices, group)
	if err != nil {
		return nil, err
	}
	snapshots, err := model.ListChannelModelPriceSnapshotsForGroupAll(group)
	if err != nil {
		return nil, err
	}
	return catalogAvailabilityList(prices, snapshots), nil
}

// AvailableCatalogIDsForGroup is the narrow authorization view used on relay
// hot paths. Strict provider rows pass the same database-authoritative checks
// as relay before their catalog IDs are exposed to a user.
func AvailableCatalogIDsForGroup(group string) ([]string, error) {
	prices, err := model.ListRoutableChannelModelPricesForGroupAll(group, nil)
	if err != nil {
		return nil, err
	}
	prices, err = filterValidatedManagedPrices(model.DB, prices, group)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(prices))
	ids := make([]string, 0, len(prices))
	for _, price := range prices {
		if _, exists := seen[price.CatalogID]; exists {
			continue
		}
		seen[price.CatalogID] = struct{}{}
		ids = append(ids, price.CatalogID)
	}
	sort.Strings(ids)
	return ids, nil
}

func filterValidatedManagedPrices(db *gorm.DB, prices []model.ChannelModelPrice, group string) ([]model.ChannelModelPrice, error) {
	if len(prices) == 0 {
		return []model.ChannelModelPrice{}, nil
	}
	channelIDs := make([]int, 0, len(prices))
	seenChannelIDs := make(map[int]struct{}, len(prices))
	for _, price := range prices {
		if _, exists := seenChannelIDs[price.ChannelID]; exists {
			continue
		}
		seenChannelIDs[price.ChannelID] = struct{}{}
		channelIDs = append(channelIDs, price.ChannelID)
	}
	var channels []model.Channel
	if err := db.Select("id", "tag", "group").Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("校验供应商托管模型目录失败: %w", err)
	}
	channelsByID := make(map[int]model.Channel, len(channels))
	for _, channel := range channels {
		channelsByID[channel.Id] = channel
	}

	validated := make([]model.ChannelModelPrice, 0, len(prices))
	for _, price := range prices {
		channel, exists := channelsByID[price.ChannelID]
		if !exists {
			continue
		}
		if _, managed := strictProviderForTag(channel.GetTag()); !managed {
			validated = append(validated, price)
			continue
		}
		routingGroup := strings.TrimSpace(group)
		if routingGroup == "" {
			routingGroup = strings.TrimSpace(channel.Group)
		}
		_, snapshot, managed, err := loadManagedRelayChannelForRequest(db, channel, price.CatalogID, routingGroup)
		if err != nil {
			if errors.Is(err, errManagedRelayRouteUnavailable) {
				continue
			}
			return nil, err
		}
		if managed && snapshot.PriceID == price.ID {
			validated = append(validated, price)
		}
	}
	return validated, nil
}

func catalogAvailabilityList(prices, snapshots []model.ChannelModelPrice) []CatalogAvailability {
	pricesByCatalog := groupPricesByCatalog(prices)
	snapshotsByCatalog := groupPricesByCatalog(snapshots)
	items := make([]CatalogAvailability, 0, len(catalog))
	for _, entry := range catalog {
		entryPrices := pricesByCatalog[entry.ID]
		item := CatalogAvailability{CatalogEntry: entry, ChannelCount: len(entryPrices), Available: len(entryPrices) > 0}
		if len(entryPrices) == 0 {
			item.UnavailableReason = unavailableReason(snapshotsByCatalog[entry.ID])
		}
		providers := map[string]struct{}{}
		referenceProtocols := map[string]struct{}{}
		for index, price := range entryPrices {
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
				item.MinImageOutputPrice, item.MaxImageOutputPrice = price.ImageOutputPrice, price.ImageOutputPrice
				item.MinFixedPrice, item.MaxFixedPrice = price.FixedPrice, price.FixedPrice
				continue
			}
			item.MinInputPrice, item.MaxInputPrice = minMax(item.MinInputPrice, item.MaxInputPrice, price.InputPrice)
			item.MinOutputPrice, item.MaxOutputPrice = minMax(item.MinOutputPrice, item.MaxOutputPrice, price.OutputPrice)
			item.MinImageOutputPrice, item.MaxImageOutputPrice = minMax(item.MinImageOutputPrice, item.MaxImageOutputPrice, price.ImageOutputPrice)
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
	return items
}

func groupPricesByCatalog(prices []model.ChannelModelPrice) map[string][]model.ChannelModelPrice {
	grouped := make(map[string][]model.ChannelModelPrice)
	for _, price := range prices {
		grouped[price.CatalogID] = append(grouped[price.CatalogID], price)
	}
	return grouped
}

// ReferenceImageModelAvailable reports whether at least one enabled, priced
// channel explicitly supports the reference protocol required by modelID.
func ReferenceImageModelAvailable(modelID string) (bool, error) {
	return referenceImageModelAvailable(modelID, model.ListAvailableChannelModelPrices)
}

func ReferenceImageModelAvailableForGroup(modelID, group string) (bool, error) {
	return referenceImageModelAvailable(modelID, func(catalogID string) ([]model.ChannelModelPrice, error) {
		return model.ListRoutableChannelModelPricesForGroup(catalogID, group, nil)
	})
}

func referenceImageModelAvailable(
	modelID string,
	listAvailable func(string) ([]model.ChannelModelPrice, error),
) (bool, error) {
	expected, ok := requiredReferenceProtocol(modelID)
	if !ok {
		return false, nil
	}
	prices, err := listAvailable(modelID)
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
	if provider.capabilities.StrictLifecycle {
		if err = validateManagedProviderChannels(provider, channels); err != nil {
			return Definition{}, nil, "", err
		}
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
	if provider.ID == "yuyu" {
		return resolveYuYuPricing(key)
	}
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

func hasPricedCandidateForProvider(providerID string, entry CatalogEntry, pricing map[string]pricingItem) bool {
	for _, candidate := range providerAliases(providerID, entry) {
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
	for _, candidate := range providerAliases(providerID, entry) {
		if _, exists := models[candidate]; !exists {
			continue
		}
		modelFound = true
		item, priced := pricing[candidate]
		if !priced {
			continue
		}
		priceFound = true
		if item.ValidationError != "" {
			return candidate, item, item.ValidationError
		}
		if item.Available != nil && !*item.Available {
			continue
		}
		if providerID == "yuyu" && entry.ID == "nano-banana-2" && item.QuotaType != 1 {
			return candidate, item, "羽宇图像路由需要明确的按次价格"
		}
		if providerID == "yuyu" && entry.ID == "gpt-image-2" && (item.QuotaType != 0 || !validPositivePrice(item.ImageOutputPrice)) {
			return candidate, item, "GPT Image 2 缺少图片输出 Token 单价"
		}
		// The image workspace uses /v1/images/generations. Do not advertise
		// chat-only models; native Gemini routes use the existing image adapter.
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
	if (providerID == "zex" || providerID == "yuyu") && catalogID != "gpt-image-2" {
		return strings.HasPrefix(upstreamID, "gemini-") && strings.Contains(upstreamID, "image") &&
			containsString(endpoints, "gemini")
	}
	if providerID == "yuyu" && catalogID == "gpt-image-2" && upstreamID == "gpt-image-2" {
		return containsString(endpoints, "openai")
	}
	if containsString(endpoints, "image-generation") {
		return true
	}
	return false
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
	original := []byte(item.OriginalPricing)
	if len(original) == 0 {
		var err error
		original, err = common.Marshal(item)
		if err != nil {
			return model.ChannelModelPrice{}, err
		}
	}
	price := model.ChannelModelPrice{
		CatalogID: catalogID, UpstreamModelID: upstreamID, Provider: provider.ID, Currency: "USD",
		SourceURL: provider.PricingURL, SourceVersion: version, Available: true, OriginalPricing: string(original),
		CacheRatio: item.CacheRatio, CacheCreationRatio: item.CreateCacheRatio, ImageOutputPrice: item.ImageOutputPrice,
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
	if item.InputPrice != 0 || item.OutputPrice != 0 {
		price.BillingType = model.ChannelModelBillingToken
		price.InputPrice = item.InputPrice
		price.OutputPrice = item.OutputPrice
		if !validPositivePrice(price.InputPrice) || !validPositivePrice(price.OutputPrice) {
			return model.ChannelModelPrice{}, fmt.Errorf("模型 %s 缺少 Token 价格", upstreamID)
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
		// These Nano routes are configured as native Gemini channels. Their
		// pricing metadata must also explicitly advertise the Gemini endpoint.
		if (providerID == "zex" || providerID == "yuyu") && strings.HasPrefix(upstreamID, "gemini-") &&
			strings.Contains(upstreamID, "image") && containsEndpoint(endpoints, "gemini") {
			return expected, MaxReferenceImages
		}
	case ReferenceProtocolOpenAIEditMultipart:
		if hasExplicitImageEditEndpoint(endpoints) ||
			(providerID == "yuyu" && catalogID == "gpt-image-2" && upstreamID == "gpt-image-2" && containsString(endpoints, "openai")) {
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

func unavailablePrice(provider Definition, entry CatalogEntry, upstreamID string, item pricingItem, version, reason string) model.ChannelModelPrice {
	original := item.OriginalPricing
	if original == "" && item.ModelName != "" {
		if encoded, err := common.Marshal(item); err == nil {
			original = string(encoded)
		}
	}
	return model.ChannelModelPrice{
		CatalogID: entry.ID, UpstreamModelID: upstreamID, Provider: provider.ID,
		BillingType: "unavailable", Currency: "USD", SourceURL: provider.PricingURL,
		SourceVersion: version, Available: false, LastError: entry.Name + "：" + reason,
		OriginalPricing: original,
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
	if len(snapshots) > 0 {
		// Detailed provider, network, and persistence errors are Root-only status
		// data. The public catalog exposes a stable availability category instead.
		return "模型已同步，但价格或鉴权尚未通过"
	}
	return "尚未配置并同步可用渠道"
}
