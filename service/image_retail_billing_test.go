package service

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/mujianpricing"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRetailImageBillingUsesActualCountAndAtomicRefund(t *testing.T) {
	for _, modelID := range []string{"gpt-image-2", "nano-banana-2"} {
		t.Run(modelID, func(t *testing.T) {
			for _, count := range []int{0, 1, 2} {
				t.Run(fmt.Sprint(count), func(t *testing.T) {
					truncate(t)
					const uid, tid, initial = 9521, 9522, 100000
					seedUser(t, uid, initial)
					seedToken(t, tid, uid, "image-retail-test", initial)
					info := strictWalletRelayInfo(uid, tid, "image-retail-test", "image-retail-request")
					info.OriginModelName = modelID
					info.ChannelMeta = &relaycommon.ChannelMeta{}
					info.StartTime = time.Now()
					info.PriceData = types.PriceData{ChannelSpecific: true, PriceProvider: "yuyu", ModelRatio: 4, ImageCompletionRatio: 3.75, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 9}}
					if modelID == "nano-banana-2" {
						info.PriceData.UsePrice = true
						info.PriceData.ModelPrice = 0.0275
					}
					quote, err := mujianpricing.NewImageQuote(modelID, 2, 7.3, 500000)
					require.NoError(t, err)
					info.ImageRetail = quote
					reserved := quote.Quota(2)
					info.PriceData.QuotaToPreConsume = reserved
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
					require.Nil(t, PreConsumeBilling(c, reserved, info))
					require.Equal(t, initial-reserved, getUserQuota(t, uid))
					quote.ReturnedCount = count
					if count == 0 {
						info.Billing.Refund(c)
						info.Billing.Refund(c)
					} else {
						require.NoError(t, PostTextConsumeQuota(c, info, &dto.Usage{}, nil))
						require.NoError(t, info.Billing.Settle(quote.Quota(count)))
						info.Billing.Refund(c)
						var log model.Log
						require.NoError(t, model.LOG_DB.Where("user_id = ?", uid).First(&log).Error)
						require.Equal(t, quote.Quota(count), log.Quota)
						var metadata map[string]any
						require.NoError(t, common.UnmarshalJsonStr(log.Other, &metadata))
						require.Equal(t, float64(1), metadata["sales_ratio"])
						require.Equal(t, float64(4), metadata["upstream_pricing"].(map[string]any)["model_ratio"])
						require.Equal(t, float64(1), metadata["group_ratio"])
						require.Equal(t, float64(count), metadata["retail_pricing"].(map[string]any)["returned_count"])
					}
					require.Equal(t, initial-quote.Quota(count), getUserQuota(t, uid))
					require.Equal(t, initial-quote.Quota(count), getTokenRemainQuota(t, tid))
				})
			}
		})
	}
}

func TestRetailImageInsufficientBalanceDoesNotReserve(t *testing.T) {
	truncate(t)
	seedUser(t, 9531, 13698)
	seedToken(t, 9532, 9531, "image-retail-low", 100000)
	info := strictWalletRelayInfo(9531, 9532, "image-retail-low", "image-retail-low-request")
	info.ImageRetail, _ = mujianpricing.NewImageQuote("gpt-image-2", 1, 7.3, 500000)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.NotNil(t, PreConsumeBilling(c, 13699, info))
	require.Equal(t, 13698, getUserQuota(t, 9531))
	require.Equal(t, 100000, getTokenRemainQuota(t, 9532))
	require.False(t, ChargeViolationFeeIfNeeded(c, info, nil))
}
