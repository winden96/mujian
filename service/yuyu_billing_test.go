package service

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Replay both real invoices through the product's quota, wallet, token and log
// path, with native Claude and normalized OpenAI usage representations.
func TestYuYuTextBillingMatchesUpstreamInvoices(t *testing.T) {
	for _, semantic := range []string{"anthropic", "openai"} {
		for _, tc := range []struct {
			name                            string
			ratio                           float64
			cached, created, reserved, want int
		}{
			{"sonnet", 1, 3080, 5690, 2408, 7912},
			{"opus", 2.5, 0, 2440, 5980, 8853},
		} {
			for _, provider := range []string{types.PriceProviderYuYu, types.PriceProviderTabCode} {
				t.Run(semantic+"/"+tc.name+"/"+provider, func(t *testing.T) {
					truncate(t)
					const uid, tid, initial = 9401, 9402, 6000
					seedUser(t, uid, initial)
					seedToken(t, tid, uid, "yuyu-invoice-token", initial)
					info := strictWalletRelayInfo(uid, tid, "yuyu-invoice-token", "yuyu-invoice")
					info.ChannelMeta = &relaycommon.ChannelMeta{}
					info.StartTime = time.Now()
					info.OriginModelName = "claude-" + tc.name + "-5"
					info.PriceData = types.PriceData{ChannelSpecific: true, PriceProvider: provider, QuotaToPreConsume: tc.reserved, ModelRatio: tc.ratio, CompletionRatio: 5, CacheRatio: 0.1, CacheCreationRatio: 1.25, CacheCreation5mRatio: 1.25, CacheCreation1hRatio: 2.5, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}
					ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
					ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
					require.Nil(t, PreConsumeBilling(ctx, tc.reserved, info))
					prompt := 471
					if semantic == "openai" {
						prompt += tc.cached + tc.created
					}
					usage := &dto.Usage{UsageSemantic: semantic, UsageSource: "anthropic", PromptTokens: prompt, CompletionTokens: 4, TotalTokens: prompt + 4, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: tc.cached, CachedCreationTokens: tc.created}, ClaudeCacheCreation5mTokens: tc.created}
					require.NoError(t, PostTextConsumeQuota(ctx, info, usage, nil))
					want := tc.want
					if provider != types.PriceProviderYuYu {
						want = tc.reserved
					}
					require.Equal(t, initial-want, getUserQuota(t, uid))
					require.Equal(t, initial-want, getTokenRemainQuota(t, tid))
					require.Equal(t, want, getTokenUsedQuota(t, tid))
					var log model.Log
					require.NoError(t, model.LOG_DB.Where("user_id = ?", uid).First(&log).Error)
					require.Equal(t, want, log.Quota)
					var record model.SubscriptionPreConsumeRecord
					require.NoError(t, model.DB.Where("request_id = ?", info.RequestId).First(&record).Error)
					require.Equal(t, "settled", record.Status)
					require.EqualValues(t, want, record.PreConsumed)
					require.NoError(t, info.Billing.Settle(want))
					info.Billing.Refund(ctx)
					require.Equal(t, initial-want, getUserQuota(t, uid))
					require.Equal(t, initial-want, getTokenRemainQuota(t, tid))
					if provider == types.PriceProviderYuYu {
						next := strictWalletRelayInfo(uid, tid, "yuyu-invoice-token", "yuyu-next")
						next.PriceData.PriceProvider = provider
						_, apiErr := NewBillingSession(ctx, next, 1)
						require.NotNil(t, apiErr, "debt must block new upstream requests")
					}
				})
			}
		}
	}
}

func TestYuYuActualSettlementAtomicityAndSubscriptionOverage(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		t.Run(fmt.Sprint(subscription), func(t *testing.T) {
			truncate(t)
			const uid, tid, subid, planid = 9411, 9412, 9413, 9414
			seedUser(t, uid, 100)
			seedToken(t, tid, uid, "yuyu-overage", 100)
			info := strictWalletRelayInfo(uid, tid, "yuyu-overage", "yuyu-overage-request")
			info.PriceData.PriceProvider = types.PriceProviderYuYu
			if subscription {
				seedSubscriptionPlan(t, planid, 100)
				seedSubscriptionForPlan(t, subid, uid, planid, 100, 0)
				info.UserSetting.BillingPreference = "subscription_only"
			}
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			session, apiErr := NewBillingSession(ctx, info, 80)
			require.Nil(t, apiErr)
			reject := true
			const callback = "yuyu:settle-token-failure"
			require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
				if reject && tx.Statement.Table == "tokens" {
					tx.AddError(errors.New("injected token failure"))
				}
			}))
			t.Cleanup(func() { require.NoError(t, model.DB.Callback().Update().Remove(callback)) })
			require.Error(t, session.Settle(150))
			require.Equal(t, 20, getTokenRemainQuota(t, tid))
			if subscription {
				require.EqualValues(t, 80, getSubscriptionUsed(t, subid))
			} else {
				require.Equal(t, 20, getUserQuota(t, uid))
			}
			reject = false
			require.NoError(t, session.Settle(150))
			require.NoError(t, session.Settle(150))
			session.Refund(ctx)
			require.Equal(t, -50, getTokenRemainQuota(t, tid))
			require.Equal(t, 150, getTokenUsedQuota(t, tid))
			if subscription {
				require.EqualValues(t, 150, getSubscriptionUsed(t, subid))
				require.Equal(t, 100, getUserQuota(t, uid))
			} else {
				require.Equal(t, -50, getUserQuota(t, uid))
			}
		})
	}
}

func TestYuYuFailedRequestRefundsReservation(t *testing.T) {
	truncate(t)
	seedUser(t, 9421, 100)
	seedToken(t, 9422, 9421, "yuyu-refund", 100)
	info := strictWalletRelayInfo(9421, 9422, "yuyu-refund", "yuyu-refund-request")
	info.PriceData.PriceProvider = types.PriceProviderYuYu
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(ctx, info, 80)
	require.Nil(t, apiErr)
	session.Refund(ctx)
	session.Refund(ctx)
	require.Equal(t, 100, getUserQuota(t, 9421))
	require.Equal(t, 100, getTokenRemainQuota(t, 9422))
	require.Zero(t, getTokenUsedQuota(t, 9422))
}

func TestYuYuActualPolicyRequiresAttestedChannelPrice(t *testing.T) {
	require.False(t, types.PriceData{PriceProvider: types.PriceProviderYuYu}.SettlesUpstreamUsage())
	require.False(t, types.PriceData{ChannelSpecific: true, PriceProvider: types.PriceProviderTabCode}.SettlesUpstreamUsage())
	require.True(t, types.PriceData{ChannelSpecific: true, PriceProvider: types.PriceProviderYuYu}.SettlesUpstreamUsage())
}
