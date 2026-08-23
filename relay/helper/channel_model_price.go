package helper

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"
)

func channelModelPreConsumePrice(modelID string, promptTokens int, meta *types.TokenCountMeta, group types.GroupRatioInfo) (types.PriceData, bool, error) {
	prices, err := model.ListAvailableChannelModelPrices(modelID)
	if err != nil || len(prices) == 0 {
		return types.PriceData{}, false, err
	}
	multiplier := 1.0
	if meta != nil && meta.ImagePriceRatio > 0 {
		multiplier = meta.ImagePriceRatio
	}
	promptPreConsumedTokens := common.Max(promptTokens, common.PreConsumedQuota)
	maxCompletionTokens := 0
	if meta != nil {
		maxCompletionTokens = meta.MaxTokens
	}
	var selected types.PriceData
	for _, price := range prices {
		candidate := priceDataFromSnapshot(price, group, multiplier)
		if candidate.UsePrice {
			candidate.QuotaToPreConsume = int(candidate.ModelPrice * common.QuotaPerUnit * group.GroupRatio)
		} else {
			weightedTokens := float64(promptPreConsumedTokens) + float64(maxCompletionTokens)*candidate.CompletionRatio
			candidate.QuotaToPreConsume = int(weightedTokens * candidate.ModelRatio * group.GroupRatio)
		}
		if candidate.QuotaToPreConsume > selected.QuotaToPreConsume {
			selected = candidate
		}
	}
	return selected, true, nil
}

// ApplyChannelModelPrice replaces the worst-case preauthorization snapshot
// with the price of the selected channel while preserving the preauthorized
// amount. Settlement can therefore refund the difference after a retry.
func ApplyChannelModelPrice(info *relaycommon.RelayInfo, channelID int) error {
	if !info.PriceData.ChannelSpecific {
		return nil
	}
	price, err := model.GetAvailableChannelModelPrice(channelID, info.OriginModelName)
	if err != nil {
		return errors.New("所选渠道缺少已验证的模型价格")
	}
	if info.RelayMode == relayconstant.RelayModeImagesEdits {
		if !price.SupportsReferenceImages() {
			return errors.New("所选渠道未声明当前模型的多图参考协议")
		}
	}
	preConsumed := info.PriceData.QuotaToPreConsume
	actual := priceDataFromSnapshot(*price, info.PriceData.GroupRatioInfo, info.PriceData.UnitPriceMultiplier)
	actual.QuotaToPreConsume = preConsumed
	info.PriceData = actual
	return nil
}

func priceDataFromSnapshot(price model.ChannelModelPrice, group types.GroupRatioInfo, multiplier float64) types.PriceData {
	data := types.PriceData{
		GroupRatioInfo: group, ChannelSpecific: true, UnitPriceMultiplier: multiplier,
		CacheRatio: price.CacheRatio, CacheCreationRatio: price.CacheCreationRatio,
		CacheCreation5mRatio: price.CacheCreationRatio,
		CacheCreation1hRatio: price.CacheCreationRatio * claudeCacheCreation1hMultiplier,
	}
	if price.BillingType == model.ChannelModelBillingFixed {
		data.UsePrice = true
		data.ModelPrice = price.FixedPrice * multiplier
		return data
	}
	data.ModelRatio = price.InputPrice / 2
	data.CompletionRatio = price.OutputPrice / price.InputPrice
	return data
}
