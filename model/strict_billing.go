package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	strictBillingStatusConsumed = "consumed"
	strictBillingStatusSettled  = "settled"
	strictBillingStatusRefunded = "refunded"
)

// StrictBillingPreConsumeParams describes one strict reservation. The token,
// funding balance, and idempotency ledger are committed in one transaction.
type StrictBillingPreConsumeParams struct {
	RequestID       string
	UserID          int
	UseSubscription bool
	TokenID         int
	TokenKey        string
	SkipToken       bool
	Quota           int
}

// StrictBillingPreConsumeResult contains the selected funding source snapshot.
// SubscriptionID is zero for wallet funding.
type StrictBillingPreConsumeResult struct {
	SubscriptionID  int
	PreConsumed     int64
	AmountTotal     int64
	AmountUsedAfter int64
}

// StrictBillingParams identifies one strict reservation during settlement or
// refund. SubscriptionID=0 denotes wallet funding.
type StrictBillingParams struct {
	RequestID      string
	UserID         int
	SubscriptionID int
	TokenID        int
	TokenKey       string
	SkipToken      bool
	ActualQuota    int
}

// PreConsumeStrictBilling atomically reserves token and funding quota and
// creates the request ledger. A failure in any step rolls back every step, so
// a process exit cannot leave a token-only or funding-only reservation.
func PreConsumeStrictBilling(params StrictBillingPreConsumeParams) (*StrictBillingPreConsumeResult, error) {
	if err := validateStrictBillingPreConsumeParams(params); err != nil {
		return nil, err
	}

	result := &StrictBillingPreConsumeResult{}
	created := false
	now := GetDBTimestamp()
	err := DB.Transaction(func(tx *gorm.DB) error {
		// Locking the user serializes request-id checks for the same account on
		// databases that support row locks. The conditional UPDATE statements
		// remain the balance safety boundary, including on SQLite.
		if err := lockStrictBillingUser(tx, params.UserID); err != nil {
			return err
		}

		var existing SubscriptionPreConsumeRecord
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("request_id = ?", params.RequestID).Limit(1).Find(&existing)
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected > 0 {
			return loadExistingStrictReservation(tx, params, &existing, result)
		}

		if !params.SkipToken {
			if err := decreaseTokenQuotaIfEnoughTx(tx, params.TokenID, params.Quota); err != nil {
				return err
			}
		}

		if params.UseSubscription {
			subscription, err := preConsumeNewUserSubscriptionTx(
				tx, params.RequestID, params.UserID, int64(params.Quota), now,
			)
			if err != nil {
				return err
			}
			result.SubscriptionID = subscription.UserSubscriptionId
			result.PreConsumed = subscription.PreConsumed
			result.AmountTotal = subscription.AmountTotal
			result.AmountUsedAfter = subscription.AmountUsedAfter
		} else {
			if err := decreaseUserQuotaIfEnoughTx(tx, params.UserID, params.Quota); err != nil {
				return err
			}
			record := &SubscriptionPreConsumeRecord{
				RequestId:          params.RequestID,
				UserId:             params.UserID,
				UserSubscriptionId: 0,
				PreConsumed:        int64(params.Quota),
				Status:             strictBillingStatusConsumed,
			}
			if err := tx.Create(record).Error; err != nil {
				return err
			}
			result.PreConsumed = int64(params.Quota)
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if created {
		invalidateStrictPreConsumeCaches(params)
	}
	return result, nil
}

// SettleStrictBilling atomically applies the funding and token deltas and
// moves the ledger directly from consumed to settled. On failure the whole
// transaction rolls back and the consumed reservation remains available for
// an explicit idempotent reconciliation attempt.
func SettleStrictBilling(params StrictBillingParams) error {
	if err := validateStrictBillingParams(params); err != nil {
		return err
	}
	changed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockStrictBillingUser(tx, params.UserID); err != nil {
			return err
		}
		record, err := lockStrictBillingRecord(tx, params.RequestID)
		if err != nil {
			return err
		}
		if err := validateStrictBillingRecord(record, params.UserID, params.SubscriptionID); err != nil {
			return err
		}
		if record.Status == strictBillingStatusSettled {
			if record.PreConsumed != int64(params.ActualQuota) {
				return errors.New("strict billing request was settled with a different quota")
			}
			return nil
		}
		if record.Status != strictBillingStatusConsumed {
			return fmt.Errorf("strict billing settlement state is invalid: %s", record.Status)
		}

		preConsumed, err := strictBillingQuotaFromRecord(record)
		if err != nil {
			return err
		}
		delta := params.ActualQuota - preConsumed
		if delta != 0 {
			if err := adjustStrictFundingTx(tx, params, delta); err != nil {
				return err
			}
			if !params.SkipToken {
				if err := adjustStrictTokenTx(tx, params.TokenID, delta); err != nil {
					return err
				}
			}
		}

		update := tx.Model(&SubscriptionPreConsumeRecord{}).
			Where("id = ? AND status = ?", record.Id, strictBillingStatusConsumed).
			Updates(map[string]any{
				"pre_consumed": int64(params.ActualQuota),
				"status":       strictBillingStatusSettled,
				"updated_at":   common.GetTimestamp(),
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errors.New("strict billing settlement state changed unexpectedly")
		}
		changed = true
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		invalidateStrictBillingCaches(params)
	}
	return nil
}

// RefundStrictBilling atomically refunds both balances and closes the ledger.
func RefundStrictBilling(params StrictBillingParams) error {
	if err := validateStrictBillingParams(params); err != nil {
		return err
	}
	changed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockStrictBillingUser(tx, params.UserID); err != nil {
			return err
		}
		record, err := lockStrictBillingRecord(tx, params.RequestID)
		if err != nil {
			return err
		}
		if err := validateStrictBillingRecord(record, params.UserID, params.SubscriptionID); err != nil {
			return err
		}
		if record.Status == strictBillingStatusRefunded {
			return nil
		}
		if record.Status != strictBillingStatusConsumed {
			return fmt.Errorf("strict billing refund state is invalid: %s", record.Status)
		}

		preConsumed, err := strictBillingQuotaFromRecord(record)
		if err != nil {
			return err
		}
		if params.SubscriptionID == 0 {
			if err := increaseUserQuotaTx(tx, params.UserID, preConsumed); err != nil {
				return err
			}
		} else if err := postConsumeUserSubscriptionDeltaTx(tx, params.SubscriptionID, -int64(preConsumed)); err != nil {
			return err
		}
		if !params.SkipToken {
			if err := increaseTokenQuotaTx(tx, params.TokenID, preConsumed); err != nil {
				return err
			}
		}

		update := tx.Model(&SubscriptionPreConsumeRecord{}).
			Where("id = ? AND status = ?", record.Id, strictBillingStatusConsumed).
			Updates(map[string]any{
				"status":     strictBillingStatusRefunded,
				"updated_at": common.GetTimestamp(),
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errors.New("strict billing refund state changed unexpectedly")
		}
		changed = true
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		invalidateStrictBillingCaches(params)
	}
	return nil
}

func loadExistingStrictReservation(
	tx *gorm.DB,
	params StrictBillingPreConsumeParams,
	record *SubscriptionPreConsumeRecord,
	result *StrictBillingPreConsumeResult,
) error {
	if record.UserId != params.UserID {
		return errors.New("strict billing request belongs to another user")
	}
	if params.UseSubscription != (record.UserSubscriptionId > 0) {
		return errors.New("strict billing funding source does not match reservation")
	}
	if record.Status != strictBillingStatusConsumed || record.PreConsumed != int64(params.Quota) {
		return fmt.Errorf("strict billing pre-consume request is already %s", record.Status)
	}
	result.SubscriptionID = record.UserSubscriptionId
	result.PreConsumed = record.PreConsumed
	if record.UserSubscriptionId == 0 {
		return nil
	}
	var subscription UserSubscription
	if err := tx.Where("id = ?", record.UserSubscriptionId).First(&subscription).Error; err != nil {
		return err
	}
	result.AmountTotal = subscription.AmountTotal
	result.AmountUsedAfter = subscription.AmountUsed
	return nil
}

func validateStrictBillingPreConsumeParams(params StrictBillingPreConsumeParams) error {
	if strings.TrimSpace(params.RequestID) == "" {
		return errors.New("requestId is empty")
	}
	if params.UserID <= 0 {
		return errors.New("invalid userId")
	}
	if params.Quota <= 0 {
		return errors.New("quota must be > 0")
	}
	if !params.SkipToken && params.TokenID <= 0 {
		return errors.New("invalid tokenId")
	}
	return nil
}

func validateStrictBillingParams(params StrictBillingParams) error {
	if strings.TrimSpace(params.RequestID) == "" {
		return errors.New("requestId is empty")
	}
	if params.UserID <= 0 {
		return errors.New("invalid userId")
	}
	if params.SubscriptionID < 0 {
		return errors.New("invalid subscriptionId")
	}
	if params.ActualQuota < 0 {
		return errors.New("actual quota cannot be negative")
	}
	if !params.SkipToken && params.TokenID <= 0 {
		return errors.New("invalid tokenId")
	}
	return nil
}

func validateStrictBillingRecord(record *SubscriptionPreConsumeRecord, userID, subscriptionID int) error {
	if record == nil {
		return errors.New("strict billing record is nil")
	}
	if record.UserId != userID {
		return errors.New("strict billing request belongs to another user")
	}
	if record.UserSubscriptionId != subscriptionID {
		return errors.New("strict billing funding source does not match reservation")
	}
	return nil
}

func lockStrictBillingUser(tx *gorm.DB, userID int) error {
	var user User
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").Where("id = ?", userID).First(&user).Error
}

func lockStrictBillingRecord(tx *gorm.DB, requestID string) (*SubscriptionPreConsumeRecord, error) {
	var record SubscriptionPreConsumeRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("request_id = ?", requestID).First(&record).Error
	return &record, err
}

func strictBillingQuotaFromRecord(record *SubscriptionPreConsumeRecord) (int, error) {
	if record.PreConsumed <= 0 || int64(int(record.PreConsumed)) != record.PreConsumed {
		return 0, errors.New("strict billing record has invalid pre-consumed quota")
	}
	return int(record.PreConsumed), nil
}

func adjustStrictFundingTx(tx *gorm.DB, params StrictBillingParams, delta int) error {
	if params.SubscriptionID != 0 {
		return postConsumeUserSubscriptionDeltaTx(tx, params.SubscriptionID, int64(delta))
	}
	if delta > 0 {
		return decreaseUserQuotaIfEnoughTx(tx, params.UserID, delta)
	}
	return increaseUserQuotaTx(tx, params.UserID, -delta)
}

func adjustStrictTokenTx(tx *gorm.DB, tokenID, delta int) error {
	if delta > 0 {
		return decreaseTokenQuotaIfEnoughTx(tx, tokenID, delta)
	}
	return increaseTokenQuotaTx(tx, tokenID, -delta)
}

func increaseUserQuotaTx(tx *gorm.DB, userID, quota int) error {
	update := tx.Model(&User{}).
		Where("id = ?", userID).
		Update("quota", gorm.Expr("quota + ?", quota))
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return errors.New("strict billing user quota row was not updated")
	}
	return nil
}

func increaseTokenQuotaTx(tx *gorm.DB, tokenID, quota int) error {
	update := tx.Model(&Token{}).
		Where("id = ?", tokenID).
		Updates(map[string]any{
			"remain_quota":  gorm.Expr("remain_quota + ?", quota),
			"used_quota":    gorm.Expr("used_quota - ?", quota),
			"accessed_time": common.GetTimestamp(),
		})
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected != 1 {
		return errors.New("strict billing token quota row was not updated")
	}
	return nil
}

func invalidateStrictPreConsumeCaches(params StrictBillingPreConsumeParams) {
	if !params.UseSubscription {
		invalidateUserQuotaCache(params.UserID, "strict billing pre-consume")
	}
	if !params.SkipToken && common.RedisEnabled {
		if err := cacheDeleteToken(params.TokenKey); err != nil {
			common.SysLog("failed to invalidate token quota cache after strict billing pre-consume: " + err.Error())
		}
	}
}

func invalidateStrictBillingCaches(params StrictBillingParams) {
	if params.SubscriptionID == 0 {
		invalidateUserQuotaCache(params.UserID, "strict billing")
	}
	if !params.SkipToken && common.RedisEnabled {
		if err := cacheDeleteToken(params.TokenKey); err != nil {
			common.SysLog("failed to invalidate token quota cache after strict billing: " + err.Error())
		}
	}
}

func invalidateUserQuotaCache(userID int, operation string) {
	if err := invalidateUserCache(userID); err != nil {
		common.SysLog("failed to invalidate user quota cache after " + operation + ": " + err.Error())
	}
}
