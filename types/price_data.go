package types

import (
	"fmt"

	"github.com/QuantumNous/new-api/pkg/mujianpricing"
)

const (
	PriceProviderZenMux  = "zenmux"
	PriceProviderTabCode = "tabcode"
	PriceProviderYuYu    = "yuyu"
)

type GroupRatioInfo struct {
	GroupRatio        float64
	GroupSpecialRatio float64
	HasSpecialRatio   bool
}

// ChannelModelPriceSnapshot is the request-scoped price attested together
// with a managed channel's current configuration and routing ability. Keeping
// it in the request context prevents a later local-cache read from mixing two
// provider lifecycle generations.
type ChannelModelPriceSnapshot struct {
	PriceID            int64
	ChannelID          int
	CatalogID          string
	UpstreamModelID    string
	Provider           string
	BillingType        string
	Currency           string
	RoutingGroup       string
	InputPrice         float64
	OutputPrice        float64
	FixedPrice         float64
	CacheRatio         float64
	CacheCreationRatio float64
}

type PriceData struct {
	FreeModel            bool
	ModelPrice           float64
	ModelRatio           float64
	CompletionRatio      float64
	CacheRatio           float64
	CacheCreationRatio   float64
	CacheCreation5mRatio float64
	CacheCreation1hRatio float64
	ImageRatio           float64
	AudioRatio           float64
	AudioCompletionRatio float64
	OtherRatios          map[string]float64
	UsePrice             bool
	Quota                int // 按次计费的最终额度（MJ / Task）
	QuotaToPreConsume    int // 按量计费的预消耗额度
	GroupRatioInfo       GroupRatioInfo
	ChannelSpecific      bool    // 幕间受管渠道按实际成功渠道结算
	UnitPriceMultiplier  float64 // 图像尺寸/质量等按次价格倍率
	PriceProvider        string  `json:"-"` // 最终选中的渠道价格快照来源
}

// SettlesUpstreamUsage requires a managed provider price snapshot. Legacy
// catalog fallback routes have no provider and retain their authorization cap.
func (p PriceData) SettlesUpstreamUsage() bool {
	return p.ChannelSpecific && p.PriceProvider != ""
}

// SalesRatio is separate from upstream cost, image quantity and group discounts.
func (p PriceData) SalesRatio() float64 {
	if p.SettlesUpstreamUsage() {
		return mujianpricing.SalesRatio
	}
	return 1
}

func (p *PriceData) AddOtherRatio(key string, ratio float64) {
	if p.OtherRatios == nil {
		p.OtherRatios = make(map[string]float64)
	}
	if ratio <= 0 {
		return
	}
	p.OtherRatios[key] = ratio
}

func (p *PriceData) ToSetting() string {
	return fmt.Sprintf("ModelPrice: %f, ModelRatio: %f, CompletionRatio: %f, CacheRatio: %f, GroupRatio: %f, UsePrice: %t, CacheCreationRatio: %f, CacheCreation5mRatio: %f, CacheCreation1hRatio: %f, QuotaToPreConsume: %d, ImageRatio: %f, AudioRatio: %f, AudioCompletionRatio: %f", p.ModelPrice, p.ModelRatio, p.CompletionRatio, p.CacheRatio, p.GroupRatioInfo.GroupRatio, p.UsePrice, p.CacheCreationRatio, p.CacheCreation5mRatio, p.CacheCreation1hRatio, p.QuotaToPreConsume, p.ImageRatio, p.AudioRatio, p.AudioCompletionRatio)
}
