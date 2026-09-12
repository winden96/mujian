package mujian

import (
	"github.com/QuantumNous/new-api/pkg/mujianpricing"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/shopspring/decimal"
)

// The product catalog uses the user's own routing group. Provider administration
// continues to read the unmodified cost snapshots directly.
func applyCatalogSalesPrices(items []mujianprovider.CatalogAvailability, group string) {
	groupRatio, special := ratio_setting.GetGroupGroupRatio(group, group)
	if !special {
		groupRatio = ratio_setting.GetGroupRatio(group)
	}
	ratio := decimal.NewFromFloat(mujianpricing.SalesRatio).Mul(decimal.NewFromFloat(groupRatio))
	for i := range items {
		item := &items[i]
		for _, price := range []*float64{&item.MinInputPrice, &item.MaxInputPrice, &item.MinOutputPrice, &item.MaxOutputPrice, &item.MinFixedPrice, &item.MaxFixedPrice} {
			*price = decimal.NewFromFloat(*price).Mul(ratio).InexactFloat64()
		}
	}
}
