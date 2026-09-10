package model

import (
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	ChannelModelBillingToken                 = "token"
	ChannelModelBillingFixed                 = "fixed"
	ChannelModelReferenceGeminiInline        = "gemini-inline"
	ChannelModelReferenceOpenAIEditMultipart = "openai-edit-multipart"
)

// Catalog reads need pricing metadata but not potentially large raw evidence
// for chat models. Legacy image rows retain their small evidence payload so
// reference-protocol backfill remains compatible with older installations.
const channelModelPriceCatalogProjection = `prices.id, prices.channel_id, prices.catalog_id, prices.upstream_model_id,
prices.provider, prices.billing_type, prices.input_price, prices.output_price, prices.fixed_price,
prices.cache_ratio, prices.cache_creation_ratio, prices.reference_protocol, prices.max_reference_images,
prices.currency, prices.source_url, prices.source_version, prices.available, prices.last_error,
CASE WHEN prices.catalog_id IN ('nano-banana', 'nano-banana-pro', 'nano-banana-2', 'gpt-image-2')
THEN prices.original_pricing ELSE '' END AS original_pricing,
prices.synced_at, prices.tested_at, prices.created_at, prices.updated_at`

// ChannelModelPrice is a provider-specific pricing snapshot for a curated
// product model. It deliberately keeps the public catalog id separate from
// the upstream id selected for a channel.
type ChannelModelPrice struct {
	ID                 int64   `json:"id" gorm:"primaryKey;autoIncrement"`
	ChannelID          int     `json:"channel_id" gorm:"uniqueIndex:idx_channel_catalog;index;not null"`
	CatalogID          string  `json:"catalog_id" gorm:"type:varchar(120);uniqueIndex:idx_channel_catalog;index;not null"`
	UpstreamModelID    string  `json:"upstream_model_id" gorm:"type:varchar(160);not null"`
	Provider           string  `json:"provider" gorm:"type:varchar(32);index;not null"`
	BillingType        string  `json:"billing_type" gorm:"type:varchar(16);not null"`
	InputPrice         float64 `json:"input_price"`
	OutputPrice        float64 `json:"output_price"`
	FixedPrice         float64 `json:"fixed_price"`
	CacheRatio         float64 `json:"cache_ratio"`
	CacheCreationRatio float64 `json:"cache_creation_ratio"`
	ReferenceProtocol  string  `json:"reference_protocol,omitempty" gorm:"type:varchar(32)"`
	MaxReferenceImages int     `json:"max_reference_images,omitempty"`
	Currency           string  `json:"currency" gorm:"type:varchar(8);not null;default:'USD'"`
	SourceURL          string  `json:"source_url" gorm:"type:text"`
	SourceVersion      string  `json:"source_version" gorm:"type:varchar(128)"`
	Available          bool    `json:"available" gorm:"index;not null;default:false"`
	LastError          string  `json:"last_error" gorm:"type:text"`
	OriginalPricing    string  `json:"-" gorm:"type:text"`
	SyncedAt           int64   `json:"synced_at" gorm:"index"`
	TestedAt           int64   `json:"tested_at"`
	CreatedAt          int64   `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt          int64   `json:"updated_at" gorm:"autoUpdateTime"`
}

// RoutableChannelPriceState describes whether a channel that the ability
// router may select has a verified price for the requested catalog model.
// ChannelTag lets the relay distinguish strict provider-managed routes from
// ordinary routes without exposing channel credentials.
type RoutableChannelPriceState struct {
	ChannelID  int    `gorm:"column:channel_id"`
	ChannelTag string `gorm:"column:channel_tag"`
	PriceID    int64  `gorm:"column:price_id"`
}

// RoutableChannelPriceView is one database snapshot of an enabled ability and
// its optional verified price. A single LEFT JOIN avoids combining route state
// and pricing from different provider lifecycle generations.
type RoutableChannelPriceView struct {
	ChannelID          int     `gorm:"column:route_channel_id"`
	ChannelTag         string  `gorm:"column:channel_tag"`
	RoutePriority      int64   `gorm:"column:route_priority"`
	RouteWeight        uint    `gorm:"column:route_weight"`
	PriceID            int64   `gorm:"column:price_id"`
	UpstreamModelID    string  `gorm:"column:upstream_model_id"`
	Provider           string  `gorm:"column:provider"`
	BillingType        string  `gorm:"column:billing_type"`
	InputPrice         float64 `gorm:"column:input_price"`
	OutputPrice        float64 `gorm:"column:output_price"`
	FixedPrice         float64 `gorm:"column:fixed_price"`
	CacheRatio         float64 `gorm:"column:cache_ratio"`
	CacheCreationRatio float64 `gorm:"column:cache_creation_ratio"`
	ReferenceProtocol  string  `gorm:"column:reference_protocol"`
	MaxReferenceImages int     `gorm:"column:max_reference_images"`
	Currency           string  `gorm:"column:currency"`
}

func (view RoutableChannelPriceView) ModelPrice(catalogID string) *ChannelModelPrice {
	if view.PriceID == 0 {
		return nil
	}
	return &ChannelModelPrice{
		ID: view.PriceID, ChannelID: view.ChannelID, CatalogID: catalogID,
		UpstreamModelID: view.UpstreamModelID, Provider: view.Provider, BillingType: view.BillingType,
		InputPrice: view.InputPrice, OutputPrice: view.OutputPrice, FixedPrice: view.FixedPrice,
		CacheRatio: view.CacheRatio, CacheCreationRatio: view.CacheCreationRatio,
		ReferenceProtocol: view.ReferenceProtocol, MaxReferenceImages: view.MaxReferenceImages,
		Currency: view.Currency, Available: true,
	}
}

func (price ChannelModelPrice) SupportsReferenceImages() bool {
	if price.MaxReferenceImages <= 0 {
		return false
	}
	switch price.CatalogID {
	case "nano-banana", "nano-banana-pro", "nano-banana-2":
		return price.ReferenceProtocol == ChannelModelReferenceGeminiInline
	case "gpt-image-2":
		return price.ReferenceProtocol == ChannelModelReferenceOpenAIEditMultipart
	default:
		return false
	}
}

func ListAvailableChannelModelPrices(catalogID string) ([]ChannelModelPrice, error) {
	prices := make([]ChannelModelPrice, 0)
	err := DB.Table("channel_model_prices AS prices").
		Select(channelModelPriceCatalogProjection).
		Joins("JOIN channels ON channels.id = prices.channel_id").
		Where("prices.catalog_id = ? AND prices.available = ? AND channels.status = ?", catalogID, true, 1).
		Order("channels.priority DESC, channels.id ASC").
		Scan(&prices).Error
	return prices, err
}

// ListAvailableChannelModelPricesAll returns the routable pricing snapshot for
// the complete curated catalog in one query. Catalog consumers should prefer
// this over issuing one query per model.
func ListAvailableChannelModelPricesAll() ([]ChannelModelPrice, error) {
	prices := make([]ChannelModelPrice, 0)
	err := DB.Table("channel_model_prices AS prices").
		Select(channelModelPriceCatalogProjection).
		Joins("JOIN channels ON channels.id = prices.channel_id").
		Where("prices.available = ? AND channels.status = ?", true, 1).
		Order("prices.catalog_id ASC, channels.priority DESC, channels.id ASC").
		Scan(&prices).Error
	return prices, err
}

// ListAvailableChannelModelPricesForGroup returns enabled prices whose channel
// explicitly belongs to group. Channel groups are comma-separated, so the
// membership check is performed on parsed values instead of a substring match.
func ListAvailableChannelModelPricesForGroup(catalogID, group string) ([]ChannelModelPrice, error) {
	return listChannelModelPricesForGroup(catalogID, group, true, "channels.priority DESC, channels.id ASC")
}

// ListRoutableChannelModelPricesForGroup returns pricing snapshots only for
// channels that the ability router can use for this model and group. A nil
// allowedChannelIDs slice means unrestricted; a non-nil empty slice allows no
// channels, matching the distributor contract.
func ListRoutableChannelModelPricesForGroup(catalogID, group string, allowedChannelIDs []int) ([]ChannelModelPrice, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return []ChannelModelPrice{}, nil
	}
	abilityGroupColumn := "abilities.`group`"
	if DB.Dialector.Name() == "postgres" {
		abilityGroupColumn = `abilities."group"`
	}
	query := DB.Table("channel_model_prices AS prices").
		Select(channelModelPriceCatalogProjection).
		Joins("JOIN channels ON channels.id = prices.channel_id").
		Joins("JOIN abilities ON abilities.channel_id = prices.channel_id AND abilities.model = prices.catalog_id").
		Where("prices.catalog_id = ? AND prices.available = ?", catalogID, true).
		Where("channels.status = ?", 1).
		Where(abilityGroupColumn+" = ? AND abilities.enabled = ?", group, true)
	if allowedChannelIDs != nil {
		if len(allowedChannelIDs) == 0 {
			return []ChannelModelPrice{}, nil
		}
		query = query.Where("prices.channel_id IN ?", allowedChannelIDs)
	}
	prices := make([]ChannelModelPrice, 0)
	err := query.Order("abilities.priority DESC, channels.id ASC").Scan(&prices).Error
	return prices, err
}

// ListRoutableChannelPriceStatesForGroup returns every enabled route for a
// model/group and whether that exact channel has an available price snapshot.
// It keeps ordinary legacy-priced routes working when verified provider
// snapshots coexist in the same routing group.
func ListRoutableChannelPriceStatesForGroup(modelID, group string, allowedChannelIDs []int) ([]RoutableChannelPriceState, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return []RoutableChannelPriceState{}, nil
	}
	abilityGroupColumn := "abilities.`group`"
	if DB.Dialector.Name() == "postgres" {
		abilityGroupColumn = `abilities."group"`
	}
	query := DB.Table("abilities").
		Select("channels.id AS channel_id, COALESCE(channels.tag, '') AS channel_tag, COALESCE(prices.id, 0) AS price_id").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Joins("LEFT JOIN channel_model_prices AS prices ON prices.channel_id = channels.id AND prices.catalog_id = ? AND prices.available = ?", modelID, true).
		Where("abilities.model = ? AND abilities.enabled = ?", modelID, true).
		Where("channels.status = ?", 1).
		Where(abilityGroupColumn+" = ?", group)
	if allowedChannelIDs != nil {
		if len(allowedChannelIDs) == 0 {
			return []RoutableChannelPriceState{}, nil
		}
		query = query.Where("channels.id IN ?", allowedChannelIDs)
	}
	states := make([]RoutableChannelPriceState, 0)
	err := query.Order("abilities.priority DESC, channels.id ASC").Scan(&states).Error
	return states, err
}

func ListRoutableChannelPriceViewsForGroup(modelID, group string, allowedChannelIDs []int) ([]RoutableChannelPriceView, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return []RoutableChannelPriceView{}, nil
	}
	abilityGroupColumn := "abilities.`group`"
	if DB.Dialector.Name() == "postgres" {
		abilityGroupColumn = `abilities."group"`
	}
	query := DB.Table("abilities").
		Select(`channels.id AS route_channel_id, COALESCE(channels.tag, '') AS channel_tag,
COALESCE(abilities.priority, 0) AS route_priority,
COALESCE(abilities.weight, 0) AS route_weight,
COALESCE(prices.id, 0) AS price_id, COALESCE(prices.upstream_model_id, '') AS upstream_model_id,
COALESCE(prices.provider, '') AS provider, COALESCE(prices.billing_type, '') AS billing_type,
COALESCE(prices.input_price, 0) AS input_price, COALESCE(prices.output_price, 0) AS output_price,
COALESCE(prices.fixed_price, 0) AS fixed_price, COALESCE(prices.cache_ratio, 0) AS cache_ratio,
COALESCE(prices.cache_creation_ratio, 0) AS cache_creation_ratio,
COALESCE(prices.reference_protocol, '') AS reference_protocol,
COALESCE(prices.max_reference_images, 0) AS max_reference_images,
COALESCE(prices.currency, '') AS currency`).
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Joins("LEFT JOIN channel_model_prices AS prices ON prices.channel_id = channels.id AND prices.catalog_id = ? AND prices.available = ?", modelID, true).
		Where("abilities.model = ? AND abilities.enabled = ?", modelID, true).
		Where("channels.status = ?", 1).
		Where(abilityGroupColumn+" = ?", group)
	if allowedChannelIDs != nil {
		if len(allowedChannelIDs) == 0 {
			return []RoutableChannelPriceView{}, nil
		}
		query = query.Where("channels.id IN ?", allowedChannelIDs)
	}
	views := make([]RoutableChannelPriceView, 0)
	err := query.Order("abilities.priority DESC, channels.id ASC").Scan(&views).Error
	return views, err
}

// ListRoutableChannelModelPricesForGroupAll returns every routable catalog
// price for one group without per-model query fan-out.
func ListRoutableChannelModelPricesForGroupAll(group string, allowedChannelIDs []int) ([]ChannelModelPrice, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return []ChannelModelPrice{}, nil
	}
	abilityGroupColumn := "abilities.`group`"
	if DB.Dialector.Name() == "postgres" {
		abilityGroupColumn = `abilities."group"`
	}
	query := DB.Table("channel_model_prices AS prices").
		Select(channelModelPriceCatalogProjection).
		Joins("JOIN channels ON channels.id = prices.channel_id").
		Joins("JOIN abilities ON abilities.channel_id = prices.channel_id AND abilities.model = prices.catalog_id").
		Where("prices.available = ? AND channels.status = ?", true, 1).
		Where(abilityGroupColumn+" = ? AND abilities.enabled = ?", group, true)
	if allowedChannelIDs != nil {
		if len(allowedChannelIDs) == 0 {
			return []ChannelModelPrice{}, nil
		}
		query = query.Where("prices.channel_id IN ?", allowedChannelIDs)
	}
	prices := make([]ChannelModelPrice, 0)
	err := query.Order("prices.catalog_id ASC, abilities.priority DESC, channels.id ASC").Scan(&prices).Error
	return prices, err
}

// ListRoutableChannelModelPriceCatalogIDsForGroup returns only the catalog IDs
// needed by token authorization. Keeping this projection narrow avoids loading
// and materializing every price snapshot on the relay hot path.
func ListRoutableChannelModelPriceCatalogIDsForGroup(group string, allowedChannelIDs []int) ([]string, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return []string{}, nil
	}
	abilityGroupColumn := "abilities.`group`"
	if DB.Dialector.Name() == "postgres" {
		abilityGroupColumn = `abilities."group"`
	}
	query := DB.Table("channel_model_prices AS prices").
		Distinct("prices.catalog_id").
		Joins("JOIN channels ON channels.id = prices.channel_id").
		Joins("JOIN abilities ON abilities.channel_id = prices.channel_id AND abilities.model = prices.catalog_id").
		Where("prices.available = ? AND channels.status = ?", true, 1).
		Where(abilityGroupColumn+" = ? AND abilities.enabled = ?", group, true)
	if allowedChannelIDs != nil {
		if len(allowedChannelIDs) == 0 {
			return []string{}, nil
		}
		query = query.Where("prices.channel_id IN ?", allowedChannelIDs)
	}
	ids := make([]string, 0)
	err := query.Order("prices.catalog_id ASC").Pluck("prices.catalog_id", &ids).Error
	return ids, err
}

func GetAvailableChannelModelPrice(channelID int, catalogID string) (*ChannelModelPrice, error) {
	var price ChannelModelPrice
	err := DB.Table("channel_model_prices AS prices").
		Select(channelModelPriceCatalogProjection).
		Where("prices.channel_id = ? AND prices.catalog_id = ? AND prices.available = ?", channelID, catalogID, true).
		Take(&price).Error
	return &price, err
}

func ListChannelModelPriceSnapshots(catalogID string) ([]ChannelModelPrice, error) {
	prices := make([]ChannelModelPrice, 0)
	err := DB.Table("channel_model_prices AS prices").Select(channelModelPriceCatalogProjection).
		Where("prices.catalog_id = ?", catalogID).Order("prices.synced_at DESC, prices.id DESC").Find(&prices).Error
	return prices, err
}

func ListChannelModelPriceSnapshotsAll() ([]ChannelModelPrice, error) {
	prices := make([]ChannelModelPrice, 0)
	err := DB.Table("channel_model_prices AS prices").Select(channelModelPriceCatalogProjection).
		Order("prices.catalog_id ASC, prices.synced_at DESC, prices.id DESC").Find(&prices).Error
	return prices, err
}

func ListChannelModelPriceSnapshotsForGroup(catalogID, group string) ([]ChannelModelPrice, error) {
	return listChannelModelPricesForGroup(catalogID, group, false, "prices.synced_at DESC, prices.id DESC")
}

func ListChannelModelPriceSnapshotsForGroupAll(group string) ([]ChannelModelPrice, error) {
	return listAllChannelModelPricesForGroup(group)
}

type channelModelPriceGroupRow struct {
	ChannelModelPrice
	ChannelGroup string `gorm:"column:channel_group"`
}

type channelGroupIdentity struct {
	ChannelID    int    `gorm:"column:channel_id"`
	ChannelGroup string `gorm:"column:channel_group"`
}

func listChannelModelPricesForGroup(catalogID, group string, activeOnly bool, order string) ([]ChannelModelPrice, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return []ChannelModelPrice{}, nil
	}
	groupColumn := "channels.`group`"
	if DB.Dialector.Name() == "postgres" {
		groupColumn = `channels."group"`
	}
	query := DB.Table("channel_model_prices AS prices").
		Select(channelModelPriceCatalogProjection+", "+groupColumn+" AS channel_group").
		Joins("JOIN channels ON channels.id = prices.channel_id").
		Where("prices.catalog_id = ?", catalogID)
	if activeOnly {
		query = query.Where("prices.available = ?", true)
		query = query.Where("channels.status = ?", 1)
	}
	var rows []channelModelPriceGroupRow
	if err := query.Order(order).Scan(&rows).Error; err != nil {
		return nil, err
	}
	prices := make([]ChannelModelPrice, 0, len(rows))
	for _, row := range rows {
		for _, channelGroup := range strings.Split(row.ChannelGroup, ",") {
			if strings.TrimSpace(channelGroup) == group {
				prices = append(prices, row.ChannelModelPrice)
				break
			}
		}
	}
	return prices, nil
}

func listAllChannelModelPricesForGroup(group string) ([]ChannelModelPrice, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return []ChannelModelPrice{}, nil
	}
	groupColumn := "channels.`group`"
	if DB.Dialector.Name() == "postgres" {
		groupColumn = `channels."group"`
	}
	channelRows := make([]channelGroupIdentity, 0)
	if err := DB.Table("channels").
		Select("channels.id AS channel_id, " + groupColumn + " AS channel_group").
		Scan(&channelRows).Error; err != nil {
		return nil, err
	}
	channelIDs := make([]int, 0, len(channelRows))
	for _, row := range channelRows {
		for _, channelGroup := range strings.Split(row.ChannelGroup, ",") {
			if strings.TrimSpace(channelGroup) == group {
				channelIDs = append(channelIDs, row.ChannelID)
				break
			}
		}
	}
	prices := make([]ChannelModelPrice, 0)
	const batchSize = 500
	for start := 0; start < len(channelIDs); start += batchSize {
		end := min(start+batchSize, len(channelIDs))
		batch := make([]ChannelModelPrice, 0)
		if err := DB.Table("channel_model_prices AS prices").
			Select(channelModelPriceCatalogProjection).
			Where("prices.channel_id IN ?", channelIDs[start:end]).
			Scan(&batch).Error; err != nil {
			return nil, err
		}
		prices = append(prices, batch...)
	}
	sort.Slice(prices, func(i, j int) bool {
		if prices[i].CatalogID != prices[j].CatalogID {
			return prices[i].CatalogID < prices[j].CatalogID
		}
		if prices[i].SyncedAt != prices[j].SyncedAt {
			return prices[i].SyncedAt > prices[j].SyncedAt
		}
		return prices[i].ID > prices[j].ID
	})
	return prices, nil
}

func UpsertChannelModelPrices(txPrices []ChannelModelPrice, channelID int) error {
	return upsertChannelModelPrices(txPrices, channelID, true)
}

// UpsertUntestedChannelModelPrices persists a newly synchronized snapshot
// without treating the synchronization request as an authenticated model
// probe. Managed providers must pass their separate Messages test before any
// row becomes routable.
func UpsertUntestedChannelModelPrices(txPrices []ChannelModelPrice, channelID int) error {
	return upsertChannelModelPrices(txPrices, channelID, false)
}

func upsertChannelModelPrices(txPrices []ChannelModelPrice, channelID int, attest bool) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		return upsertChannelModelPricesTx(tx, txPrices, channelID, attest)
	})
}

// UpsertUntestedChannelModelPricesTx lets provider synchronization replace the
// channel configuration, abilities, and price evidence in one transaction.
func UpsertUntestedChannelModelPricesTx(tx *gorm.DB, prices []ChannelModelPrice, channelID int) error {
	return upsertChannelModelPricesTx(tx, prices, channelID, false)
}

func upsertChannelModelPricesTx(tx *gorm.DB, prices []ChannelModelPrice, channelID int, attest bool) error {
	if err := tx.Where("channel_id = ?", channelID).Delete(&ChannelModelPrice{}).Error; err != nil {
		return err
	}
	if len(prices) == 0 {
		return nil
	}
	now := time.Now().Unix()
	for index := range prices {
		prices[index].ChannelID = channelID
		prices[index].SyncedAt = now
		if attest {
			prices[index].TestedAt = now
		} else {
			prices[index].TestedAt = 0
		}
	}
	return tx.Create(&prices).Error
}
