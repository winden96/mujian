package helper

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/mujianpricing"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func ModelPriceHelper(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) (types.PriceData, error) {
	// Resolve and attest routable upstream prices even when the retail price is fixed.
	data, err := upstreamModelPriceHelper(c, info, promptTokens, meta)
	if err != nil {
		return data, err
	}
	request, imageRequest := info.Request.(*dto.ImageRequest)
	if !imageRequest {
		return data, nil
	}
	count := uint(1)
	if request.N != nil {
		count = *request.N
	}
	if info.ImageRetail == nil {
		info.ImageRetail, err = mujianpricing.NewImageQuote(info.OriginModelName, count, operation_setting.USDExchangeRate, common.QuotaPerUnit)
		if err != nil {
			return types.PriceData{}, err
		}
	}
	if info.ImageRetail != nil {
		data.QuotaToPreConsume = info.ImageRetail.Quota(info.ImageRetail.RequestedCount)
		data.FreeModel = false
		info.PriceData = data
	}
	return data, nil
}
