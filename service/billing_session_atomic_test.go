package service

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func strictWalletRelayInfo(userID, tokenID int, tokenKey, requestID string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        tokenKey,
		RequestId:       requestID,
		OriginModelName: "claude-sonnet-4-6",
		ForcePreConsume: true,
		PriceData: types.PriceData{
			ChannelSpecific: true,
		},
		UserSetting: dto.UserSetting{
			BillingPreference: "wallet_only",
		},
	}
}

func strictSubscriptionRelayInfo(userID, tokenID int, tokenKey, requestID string) *relaycommon.RelayInfo {
	info := strictWalletRelayInfo(userID, tokenID, tokenKey, requestID)
	info.UserSetting.BillingPreference = "subscription_only"
	return info
}

func runConcurrentStrictReservations(t *testing.T, infos []*relaycommon.RelayInfo, quota int) []*types.NewAPIError {
	t.Helper()
	start := make(chan struct{})
	errs := make([]*types.NewAPIError, len(infos))
	var wg sync.WaitGroup
	for i, info := range infos {
		wg.Add(1)
		go func(index int, relayInfo *relaycommon.RelayInfo) {
			defer wg.Done()
			<-start
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			_, errs[index] = NewBillingSession(ctx, relayInfo, quota)
		}(i, info)
	}
	close(start)
	wg.Wait()
	return errs
}

func TestStrictBillingReservationAtomicallyGuardsWalletQuota(t *testing.T) {
	truncate(t)
	const (
		userID   = 9101
		tokenID  = 9102
		tokenKey = "strict-wallet-token"
	)
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, tokenKey, 1000)

	errs := runConcurrentStrictReservations(t, []*relaycommon.RelayInfo{
		strictWalletRelayInfo(userID, tokenID, tokenKey, "strict-wallet-1"),
		strictWalletRelayInfo(userID, tokenID, tokenKey, "strict-wallet-2"),
	}, 80)

	successes := 0
	insufficient := 0
	for _, apiErr := range errs {
		if apiErr == nil {
			successes++
			continue
		}
		if apiErr.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
			insufficient++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, insufficient)
	require.Equal(t, 20, getUserQuota(t, userID))
	require.Equal(t, 920, getTokenRemainQuota(t, tokenID))
	require.Equal(t, 80, getTokenUsedQuota(t, tokenID))
}

func TestStrictBillingReservationAtomicallyGuardsTokenQuota(t *testing.T) {
	truncate(t)
	const (
		userID   = 9201
		tokenID  = 9202
		tokenKey = "strict-token-quota"
	)
	seedUser(t, userID, 1000)
	seedToken(t, tokenID, userID, tokenKey, 100)

	errs := runConcurrentStrictReservations(t, []*relaycommon.RelayInfo{
		strictWalletRelayInfo(userID, tokenID, tokenKey, "strict-token-1"),
		strictWalletRelayInfo(userID, tokenID, tokenKey, "strict-token-2"),
	}, 80)

	successes := 0
	tokenFailures := 0
	for _, apiErr := range errs {
		if apiErr == nil {
			successes++
			continue
		}
		if apiErr.GetErrorCode() == types.ErrorCodePreConsumeTokenQuotaFailed {
			tokenFailures++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, tokenFailures)
	require.Equal(t, 920, getUserQuota(t, userID))
	require.Equal(t, 20, getTokenRemainQuota(t, tokenID))
	require.Equal(t, 80, getTokenUsedQuota(t, tokenID))
}

func TestStrictReservationRollsBackTokenWhenWalletIsInsufficientWithBatchEnabled(t *testing.T) {
	truncate(t)
	const (
		userID   = 9251
		tokenID  = 9252
		tokenKey = "strict-token-rollback"
	)
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, tokenKey, 100)

	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() {
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
	})

	// Financial balances bypass the legacy batch queue, so every replica sees
	// the wallet balance at 20 before the strict reservation starts.
	require.NoError(t, model.DecreaseUserQuota(userID, 80, false))
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(ctx, strictWalletRelayInfo(userID, tokenID, tokenKey, "strict-token-rollback"), 80)
	require.Nil(t, session)
	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())

	// The failed wallet condition rolls back the token UPDATE in the same
	// transaction, with no compensation write or pending batch delta.
	require.Equal(t, 100, getTokenRemainQuota(t, tokenID))
	require.Zero(t, getTokenUsedQuota(t, tokenID))
	var records int64
	require.NoError(t, model.DB.Model(&model.SubscriptionPreConsumeRecord{}).
		Where("request_id = ?", "strict-token-rollback").Count(&records).Error)
	require.Zero(t, records)
}

func TestStrictReservationRollsBackFundingAndTokenWhenLedgerCreateFails(t *testing.T) {
	truncate(t)
	const (
		userID    = 9255
		tokenID   = 9256
		tokenKey  = "strict-ledger-rollback"
		requestID = "strict-ledger-create-failure"
	)
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, tokenKey, 100)

	const callbackName = "strict_billing:ledger_create_failure"
	require.NoError(t, model.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "subscription_pre_consume_records" {
			tx.AddError(errors.New("injected strict ledger create failure"))
		}
	}))
	t.Cleanup(func() {
		require.NoError(t, model.DB.Callback().Create().Remove(callbackName))
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(ctx, strictWalletRelayInfo(userID, tokenID, tokenKey, requestID), 80)
	require.Nil(t, session)
	require.NotNil(t, apiErr)
	require.Equal(t, types.ErrorCodeUpdateDataError, apiErr.GetErrorCode())
	require.Equal(t, 100, getUserQuota(t, userID))
	require.Equal(t, 100, getTokenRemainQuota(t, tokenID))
	require.Zero(t, getTokenUsedQuota(t, tokenID))
	var records int64
	require.NoError(t, model.DB.Model(&model.SubscriptionPreConsumeRecord{}).
		Where("request_id = ?", requestID).Count(&records).Error)
	require.Zero(t, records)
}

func TestStrictRefundIsSynchronouslyPersistedWhenBatchUpdatesAreEnabled(t *testing.T) {
	truncate(t)
	const (
		userID   = 9261
		tokenID  = 9262
		tokenKey = "strict-sync-refund"
	)
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, tokenKey, 100)

	previousBatchUpdateEnabled := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() {
		common.BatchUpdateEnabled = previousBatchUpdateEnabled
	})

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(ctx, strictWalletRelayInfo(userID, tokenID, tokenKey, "strict-sync-refund"), 80)
	require.Nil(t, apiErr)
	require.NotNil(t, session)
	require.Equal(t, 20, getUserQuota(t, userID))
	require.Equal(t, 20, getTokenRemainQuota(t, tokenID))

	session.Refund(ctx)

	require.Equal(t, 100, getUserQuota(t, userID))
	require.Equal(t, 100, getTokenRemainQuota(t, tokenID))
	require.Zero(t, getTokenUsedQuota(t, tokenID))
	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", "strict-sync-refund").First(&record).Error)
	require.Equal(t, "refunded", record.Status)
	require.NoError(t, model.RefundStrictBilling(model.StrictBillingParams{
		RequestID: "strict-sync-refund", UserID: userID, TokenID: tokenID, TokenKey: tokenKey,
	}))
	require.Equal(t, 100, getUserQuota(t, userID))
	require.Equal(t, 100, getTokenRemainQuota(t, tokenID))
}

func TestStrictPreConsumeReusesRequestLedgerWithoutDoubleDebit(t *testing.T) {
	truncate(t)
	const (
		userID    = 9265
		tokenID   = 9266
		tokenKey  = "strict-idempotent-preconsume"
		requestID = "strict-idempotent-preconsume-request"
	)
	seedUser(t, userID, 200)
	seedToken(t, tokenID, userID, tokenKey, 200)

	firstContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	firstSession, firstErr := NewBillingSession(
		firstContext,
		strictWalletRelayInfo(userID, tokenID, tokenKey, requestID),
		80,
	)
	require.Nil(t, firstErr)
	require.NotNil(t, firstSession)

	secondContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	secondSession, secondErr := NewBillingSession(
		secondContext,
		strictWalletRelayInfo(userID, tokenID, tokenKey, requestID),
		80,
	)
	require.Nil(t, secondErr)
	require.NotNil(t, secondSession)
	require.Equal(t, 120, getUserQuota(t, userID))
	require.Equal(t, 120, getTokenRemainQuota(t, tokenID))
	require.Equal(t, 80, getTokenUsedQuota(t, tokenID))

	var records int64
	require.NoError(t, model.DB.Model(&model.SubscriptionPreConsumeRecord{}).
		Where("request_id = ?", requestID).Count(&records).Error)
	require.EqualValues(t, 1, records)
}

func TestStrictSettlementRollsBackAtomicallyAndCanBeRetriedIdempotently(t *testing.T) {
	truncate(t)
	const (
		userID    = 9271
		tokenID   = 9272
		tokenKey  = "strict-atomic-settle"
		requestID = "strict-atomic-settle-request"
	)
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, tokenKey, 100)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := strictWalletRelayInfo(userID, tokenID, tokenKey, requestID)
	session, apiErr := NewBillingSession(ctx, info, 80)
	require.Nil(t, apiErr)
	require.NotNil(t, session)

	const callbackName = "strict_billing:settle_token_failure"
	var reject atomic.Bool
	var rejectedAttempts atomic.Int32
	reject.Store(true)
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if reject.Load() && tx.Statement.Table == "tokens" {
			rejectedAttempts.Add(1)
			tx.AddError(errors.New("injected strict token settlement failure"))
		}
	}))
	t.Cleanup(func() {
		require.NoError(t, model.DB.Callback().Update().Remove(callbackName))
	})

	settlementErr := session.Settle(50)
	require.Error(t, settlementErr)
	require.ErrorContains(t, settlementErr, "failed after 3 synchronous attempts")
	require.ErrorContains(t, settlementErr, "manual_reconciliation_required=true")
	require.ErrorContains(t, settlementErr, "automatic_settlement_retry=false")
	require.EqualValues(t, strictSettlementMaxAttempts, rejectedAttempts.Load())
	require.False(t, session.NeedsRefund())
	require.Equal(t, 20, getUserQuota(t, userID))
	require.Equal(t, 20, getTokenRemainQuota(t, tokenID))
	require.Equal(t, 80, getTokenUsedQuota(t, tokenID))
	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", requestID).First(&record).Error)
	require.Equal(t, "consumed", record.Status)
	require.EqualValues(t, 80, record.PreConsumed)
	var intermediateStates int64
	require.NoError(t, model.DB.Model(&model.SubscriptionPreConsumeRecord{}).
		Where("status LIKE ?", "settling:%").Count(&intermediateStates).Error)
	require.Zero(t, intermediateStates)

	reject.Store(false)
	require.NoError(t, session.Settle(50))
	require.Equal(t, 50, getUserQuota(t, userID))
	require.Equal(t, 50, getTokenRemainQuota(t, tokenID))
	require.Equal(t, 50, getTokenUsedQuota(t, tokenID))
	require.NoError(t, model.DB.Where("request_id = ?", requestID).First(&record).Error)
	require.Equal(t, "settled", record.Status)
	require.EqualValues(t, 50, record.PreConsumed)

	require.NoError(t, model.SettleStrictBilling(model.StrictBillingParams{
		RequestID: requestID, UserID: userID, TokenID: tokenID, TokenKey: tokenKey, ActualQuota: 50,
	}))
	require.Equal(t, 50, getUserQuota(t, userID))
	require.Equal(t, 50, getTokenRemainQuota(t, tokenID))
}

func TestStrictSettlementRetriesTransientFailureWithoutDoubleApplying(t *testing.T) {
	truncate(t)
	const (
		userID    = 9273
		tokenID   = 9274
		tokenKey  = "strict-transient-settle"
		requestID = "strict-transient-settle-request"
	)
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, tokenKey, 100)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	session, apiErr := NewBillingSession(ctx, strictWalletRelayInfo(userID, tokenID, tokenKey, requestID), 80)
	require.Nil(t, apiErr)
	require.NotNil(t, session)

	const callbackName = "strict_billing:transient_settle_token_failure"
	var attempts atomic.Int32
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "tokens" {
			return
		}
		if attempts.Add(1) <= 2 {
			tx.AddError(errors.New("injected transient strict token settlement failure"))
		}
	}))
	t.Cleanup(func() {
		require.NoError(t, model.DB.Callback().Update().Remove(callbackName))
	})

	require.NoError(t, session.Settle(50))
	require.EqualValues(t, strictSettlementMaxAttempts, attempts.Load())
	require.Equal(t, 50, getUserQuota(t, userID))
	require.Equal(t, 50, getTokenRemainQuota(t, tokenID))
	require.Equal(t, 50, getTokenUsedQuota(t, tokenID))

	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", requestID).First(&record).Error)
	require.Equal(t, "settled", record.Status)
	require.EqualValues(t, 50, record.PreConsumed)

	// A repeated caller observes the completed session and does not execute the
	// transaction again or apply either balance delta twice.
	require.NoError(t, session.Settle(50))
	require.EqualValues(t, strictSettlementMaxAttempts, attempts.Load())
	require.Equal(t, 50, getUserQuota(t, userID))
	require.Equal(t, 50, getTokenRemainQuota(t, tokenID))
	require.Equal(t, 50, getTokenUsedQuota(t, tokenID))

	// A process-level reconciliation retry reaches the persisted terminal
	// ledger and is idempotent as well.
	require.NoError(t, settleStrictBillingWithRetry(model.StrictBillingParams{
		RequestID: requestID, UserID: userID, TokenID: tokenID, TokenKey: tokenKey, ActualQuota: 50,
	}))
	require.EqualValues(t, strictSettlementMaxAttempts, attempts.Load())
	require.Equal(t, 50, getUserQuota(t, userID))
	require.Equal(t, 50, getTokenRemainQuota(t, tokenID))
	require.Equal(t, 50, getTokenUsedQuota(t, tokenID))
}

func TestStrictSubscriptionSettlementUpdatesFundingAndTokenInOneLedgerTransition(t *testing.T) {
	truncate(t)
	const (
		userID    = 9275
		tokenID   = 9276
		planID    = 9277
		subID     = 9278
		tokenKey  = "strict-subscription-settle"
		requestID = "strict-subscription-settle-request"
	)
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, tokenKey, 100)
	seedSubscriptionPlan(t, planID, 100)
	seedSubscriptionForPlan(t, subID, userID, planID, 100, 0)

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := strictSubscriptionRelayInfo(userID, tokenID, tokenKey, requestID)
	session, apiErr := NewBillingSession(ctx, info, 80)
	require.Nil(t, apiErr)
	require.NotNil(t, session)
	require.EqualValues(t, 80, getSubscriptionUsed(t, subID))
	require.Equal(t, 20, getTokenRemainQuota(t, tokenID))

	require.NoError(t, session.Settle(50))
	require.EqualValues(t, 50, getSubscriptionUsed(t, subID))
	require.Equal(t, 50, getTokenRemainQuota(t, tokenID))
	require.Equal(t, 50, getTokenUsedQuota(t, tokenID))
	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", requestID).First(&record).Error)
	require.Equal(t, "settled", record.Status)
	require.EqualValues(t, 50, record.PreConsumed)

	require.NoError(t, model.SettleStrictBilling(model.StrictBillingParams{
		RequestID: requestID, UserID: userID, SubscriptionID: subID,
		TokenID: tokenID, TokenKey: tokenKey, ActualQuota: 50,
	}))
	require.EqualValues(t, 50, getSubscriptionUsed(t, subID))
	require.Equal(t, 50, getTokenRemainQuota(t, tokenID))
}

func TestMixedStrictAndLegacyReservationsShareConditionalDatabaseBoundary(t *testing.T) {
	for _, redisEnabled := range []bool{false, true} {
		redisEnabled := redisEnabled
		t.Run(fmt.Sprintf("redis_%t", redisEnabled), func(t *testing.T) {
			truncate(t)
			previousRedisEnabled := common.RedisEnabled
			previousRedisClient := common.RDB
			common.RedisEnabled = redisEnabled
			if redisEnabled {
				common.RDB = redis.NewClient(&redis.Options{
					Addr: "127.0.0.1:1", DialTimeout: time.Millisecond,
					ReadTimeout: time.Millisecond, WriteTimeout: time.Millisecond, MaxRetries: -1,
				})
			}
			t.Cleanup(func() {
				if redisEnabled {
					_ = common.RDB.Close()
				}
				common.RDB = previousRedisClient
				common.RedisEnabled = previousRedisEnabled
			})

			baseID := 9280
			if redisEnabled {
				baseID = 9290
			}
			userID := baseID + 1
			tokenID := baseID + 2
			tokenKey := fmt.Sprintf("mixed-user-reservation-%d", baseID)
			seedUser(t, userID, 100)
			seedToken(t, tokenID, userID, tokenKey, 1000)

			start := make(chan struct{})
			var wg sync.WaitGroup
			var strictUserErr *types.NewAPIError
			var legacyUserErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				_, strictUserErr = NewBillingSession(ctx, strictWalletRelayInfo(
					userID, tokenID, tokenKey, fmt.Sprintf("mixed-strict-user-%d", baseID),
				), 80)
			}()
			go func() {
				defer wg.Done()
				<-start
				legacyUserErr = (&WalletFunding{userId: userID}).PreConsume(80)
			}()
			close(start)
			wg.Wait()
			userSuccesses := 0
			if strictUserErr == nil {
				userSuccesses++
			}
			if legacyUserErr == nil {
				userSuccesses++
			}
			require.Equal(t, 1, userSuccesses)
			require.Equal(t, 20, getUserQuota(t, userID))

			tokenUserID := baseID + 3
			tokenOnlyID := baseID + 4
			tokenOnlyKey := fmt.Sprintf("mixed-token-reservation-%d", baseID)
			seedUser(t, tokenUserID, 1000)
			seedToken(t, tokenOnlyID, tokenUserID, tokenOnlyKey, 100)
			start = make(chan struct{})
			wg = sync.WaitGroup{}
			var strictTokenErr *types.NewAPIError
			var legacyTokenErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				_, strictTokenErr = NewBillingSession(ctx, strictWalletRelayInfo(
					tokenUserID, tokenOnlyID, tokenOnlyKey, fmt.Sprintf("mixed-strict-token-%d", baseID),
				), 80)
			}()
			go func() {
				defer wg.Done()
				<-start
				legacyTokenErr = PreConsumeTokenQuota(&relaycommon.RelayInfo{
					TokenId: tokenOnlyID, TokenKey: tokenOnlyKey,
				}, 80)
			}()
			close(start)
			wg.Wait()
			tokenSuccesses := 0
			if strictTokenErr == nil {
				tokenSuccesses++
			}
			if legacyTokenErr == nil {
				tokenSuccesses++
			}
			require.Equal(t, 1, tokenSuccesses)
			require.Equal(t, 20, getTokenRemainQuota(t, tokenOnlyID))
			require.Equal(t, 80, getTokenUsedQuota(t, tokenOnlyID))
		})
	}
}

func TestStrictBillingReservationAtomicallyGuardsSubscriptionQuota(t *testing.T) {
	truncate(t)
	const (
		userID   = 9301
		tokenID  = 9302
		planID   = 9303
		subID    = 9304
		tokenKey = "strict-subscription-token"
	)
	seedUser(t, userID, 1000)
	seedToken(t, tokenID, userID, tokenKey, 1000)
	seedSubscriptionPlan(t, planID, 100)
	seedSubscriptionForPlan(t, subID, userID, planID, 100, 0)

	errs := runConcurrentStrictReservations(t, []*relaycommon.RelayInfo{
		strictSubscriptionRelayInfo(userID, tokenID, tokenKey, "strict-subscription-1"),
		strictSubscriptionRelayInfo(userID, tokenID, tokenKey, "strict-subscription-2"),
	}, 80)

	successes := 0
	insufficient := 0
	for _, apiErr := range errs {
		if apiErr == nil {
			successes++
			continue
		}
		if apiErr.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
			insufficient++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, insufficient)
	require.EqualValues(t, 80, getSubscriptionUsed(t, subID))
	require.Equal(t, 1000, getUserQuota(t, userID))
	require.Equal(t, 920, getTokenRemainQuota(t, tokenID))
	require.Equal(t, 80, getTokenUsedQuota(t, tokenID))

	var records int64
	require.NoError(t, model.DB.Model(&model.SubscriptionPreConsumeRecord{}).Count(&records).Error)
	require.EqualValues(t, 1, records)
}

func TestSubscriptionPreConsumeRefundIsAtomicAndIdempotent(t *testing.T) {
	truncate(t)
	const (
		userID = 9401
		planID = 9402
		subID  = 9403
	)
	seedUser(t, userID, 1000)
	seedSubscriptionPlan(t, planID, 100)
	seedSubscriptionForPlan(t, subID, userID, planID, 100, 0)
	_, err := model.PreConsumeUserSubscription("strict-refund", userID, "claude-sonnet-4-6", 0, 80)
	require.NoError(t, err)
	require.EqualValues(t, 80, getSubscriptionUsed(t, subID))

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for index := range errs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			errs[index] = model.RefundSubscriptionPreConsume("strict-refund")
		}(index)
	}
	close(start)
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	require.Zero(t, getSubscriptionUsed(t, subID))

	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", "strict-refund").First(&record).Error)
	require.Equal(t, "refunded", record.Status)
}

func TestPreConsumeCleanupRetainsUnfinishedReservations(t *testing.T) {
	truncate(t)
	records := []model.SubscriptionPreConsumeRecord{
		{RequestId: "cleanup-consumed", UserId: 9501, PreConsumed: 80, Status: "consumed"},
		{RequestId: "cleanup-settled", UserId: 9501, PreConsumed: 50, Status: "settled"},
		{RequestId: "cleanup-refunded", UserId: 9501, PreConsumed: 80, Status: "refunded"},
	}
	require.NoError(t, model.DB.Create(&records).Error)
	require.NoError(t, model.DB.Model(&model.SubscriptionPreConsumeRecord{}).
		Where("request_id IN ?", []string{"cleanup-consumed", "cleanup-settled", "cleanup-refunded"}).
		UpdateColumn("updated_at", common.GetTimestamp()-100).Error)

	deleted, err := model.CleanupSubscriptionPreConsumeRecords(1)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted)

	var remaining []model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Order("request_id").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	require.Equal(t, "cleanup-consumed", remaining[0].RequestId)
	require.Equal(t, "consumed", remaining[0].Status)
}
