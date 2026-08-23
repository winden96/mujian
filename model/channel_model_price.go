package model

import (
	"time"

	"gorm.io/gorm"
)

const (
	ChannelModelBillingToken                 = "token"
	ChannelModelBillingFixed                 = "fixed"
	ChannelModelReferenceGeminiInline        = "gemini-inline"
	ChannelModelReferenceOpenAIEditMultipart = "openai-edit-multipart"
)

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
		Select("prices.*").
		Joins("JOIN channels ON channels.id = prices.channel_id").
		Where("prices.catalog_id = ? AND prices.available = ? AND channels.status = ?", catalogID, true, 1).
		Order("channels.priority DESC, channels.id ASC").
		Scan(&prices).Error
	return prices, err
}

func GetAvailableChannelModelPrice(channelID int, catalogID string) (*ChannelModelPrice, error) {
	var price ChannelModelPrice
	err := DB.Where("channel_id = ? AND catalog_id = ? AND available = ?", channelID, catalogID, true).First(&price).Error
	return &price, err
}

func ListChannelModelPriceSnapshots(catalogID string) ([]ChannelModelPrice, error) {
	prices := make([]ChannelModelPrice, 0)
	err := DB.Where("catalog_id = ?", catalogID).Order("synced_at DESC, id DESC").Find(&prices).Error
	return prices, err
}

func UpsertChannelModelPrices(txPrices []ChannelModelPrice, channelID int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("channel_id = ?", channelID).Delete(&ChannelModelPrice{}).Error; err != nil {
			return err
		}
		if len(txPrices) == 0 {
			return nil
		}
		now := time.Now().Unix()
		for index := range txPrices {
			txPrices[index].ChannelID = channelID
			txPrices[index].SyncedAt = now
			txPrices[index].TestedAt = now
		}
		return tx.Create(&txPrices).Error
	})
}
