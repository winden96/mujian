package mujianpricing

import (
	"errors"
	"math"

	"github.com/shopspring/decimal"
)

const gptImagePriceFen = 20

// ImagePrice is the final customer price, independent of upstream cost snapshots.
type ImagePrice struct {
	Currency string  `json:"currency"`
	Unit     string  `json:"unit"`
	Amount   float64 `json:"amount"`
}

func FixedImagePrice(model string) *ImagePrice {
	if model != "gpt-image-2" {
		return nil
	}
	return &ImagePrice{Currency: "CNY", Unit: "image", Amount: float64(gptImagePriceFen) / 100}
}

// ImageQuote freezes the currency conversion for the entire request, including retries.
type ImageQuote struct {
	Price          ImagePrice `json:"price"`
	ExchangeRate   float64    `json:"usd_exchange_rate"`
	QuotaPerUnit   float64    `json:"quota_per_unit"`
	RequestedCount int        `json:"requested_count"`
	ReturnedCount  int        `json:"returned_count"`
}

func NewImageQuote(model string, count uint, rate, quotaPerUnit float64) (*ImageQuote, error) {
	price := FixedImagePrice(model)
	if price == nil {
		return nil, nil
	}
	if count == 0 || uint64(count) > uint64(^uint(0)>>1) || !positiveFinite(rate) || !positiveFinite(quotaPerUnit) {
		return nil, errors.New("图片数量或人民币计费换算配置无效")
	}
	q := &ImageQuote{Price: *price, ExchangeRate: rate, QuotaPerUnit: quotaPerUnit, RequestedCount: int(count)}
	amount := q.quotaDecimal(q.RequestedCount).Round(0)
	if amount.LessThan(decimal.NewFromInt(1)) || amount.GreaterThan(decimal.NewFromInt(int64(^uint(0)>>1))) {
		return nil, errors.New("图片预扣额度超出有效范围")
	}
	return q, nil
}

func positiveFinite(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

func (q *ImageQuote) quotaDecimal(count int) decimal.Decimal {
	// Keep the retail price in fen and round only after multiplying the image count.
	return decimal.NewFromInt(gptImagePriceFen).Div(decimal.NewFromInt(100)).Mul(decimal.NewFromInt(int64(count))).
		Div(decimal.NewFromFloat(q.ExchangeRate)).Mul(decimal.NewFromFloat(q.QuotaPerUnit))
}

func (q *ImageQuote) Quota(count int) int { return int(q.quotaDecimal(count).Round(0).IntPart()) }
