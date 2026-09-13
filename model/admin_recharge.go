package model

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrAdminRechargePermission = errors.New("无权为该账号充值")
	ErrAdminRechargeUser       = errors.New("充值用户不存在或已删除")
	ErrAdminRechargeConflict   = errors.New("请求标识已用于另一笔充值，请勿修改重试参数")
)

// CreateAdminRecharge commits the order and balance together. The unique trade
// number serializes retries across processes, including concurrent requests.
func CreateAdminRecharge(ctx context.Context, order *TopUp) (created bool, err error) {
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Acquire the write claim before reading on SQLite, avoiding a read-to-write
		// lock upgrade when multiple connections recharge concurrently.
		insert := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "trade_no"}}, DoNothing: true}).Create(order)
		if insert.Error != nil {
			return insert.Error
		}
		var admin User
		if err := tx.First(&admin, order.AdminId).Error; err != nil {
			return err
		}
		if admin.Status != common.UserStatusEnabled || admin.Role < common.RoleAdminUser || admin.Id == order.UserId {
			return ErrAdminRechargePermission
		}
		var user User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, order.UserId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAdminRechargeUser
			}
			return err
		}
		if admin.Role != common.RoleRootUser && user.Role >= admin.Role {
			return ErrAdminRechargePermission
		}
		if insert.RowsAffected == 0 {
			var existing TopUp
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("trade_no = ?", order.TradeNo).First(&existing).Error; err != nil {
				return err
			}
			if existing.AdminId != order.AdminId || existing.UserId != order.UserId || existing.Amount != order.Amount || existing.Remark != order.Remark || existing.PaymentMethod != TopUpPaymentMethodAdmin || existing.Status != common.TopUpStatusSuccess {
				return ErrAdminRechargeConflict
			}
			*order = existing
			return nil
		}
		order.AdminUsername = admin.Username
		if err := tx.Model(order).Update("admin_username", admin.Username).Error; err != nil {
			return err
		}
		if order.CreditedQuota <= 0 {
			return errors.New("无效的充值额度")
		}
		result := tx.Model(&User{}).Where("id = ? AND quota <= ?", user.Id, int64(math.MaxInt64)-order.CreditedQuota).
			Update("quota", gorm.Expr("quota + ?", order.CreditedQuota))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("账户余额超出支持范围")
		}
		created = true
		return nil
	})
	if err != nil {
		return false, err
	}
	// Retried successful requests also invalidate the cache, repairing a previous
	// post-commit cache failure without applying the credit again.
	if err := InvalidateUserCache(order.UserId); err != nil {
		common.SysLog(fmt.Sprintf("failed to invalidate user cache after admin recharge %s: %v", order.TradeNo, err))
	}
	return created, nil
}
