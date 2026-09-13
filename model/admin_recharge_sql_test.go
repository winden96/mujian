package model

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// DSNs must point to disposable, empty test databases. These opt-in tests drop
// only the users/top_ups tables they create, and never read application env DSNs.
func TestAdminRechargeSQLBackends(t *testing.T) {
	for _, backend := range []string{"MYSQL", "POSTGRES"} {
		t.Run(backend, func(t *testing.T) {
			dsn := os.Getenv("MUJIAN_RECHARGE_TEST_" + backend)
			if dsn == "" {
				t.Skip("requires a dedicated disposable SQL test database")
			}
			var driver gorm.Dialector
			if backend == "MYSQL" {
				driver = mysql.Open(dsn)
			} else {
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { _ = sqlDB.Close() })
			require.False(t, db.Migrator().HasTable(&User{}), "test requires an empty database")
			require.False(t, db.Migrator().HasTable(&TopUp{}), "test requires an empty database")
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&TopUp{}, &User{})) })
			require.NoError(t, db.AutoMigrate(&legacyRechargeUser{}, &legacyRechargeTopUp{}))
			require.NoError(t, db.Create(&legacyRechargeUser{User: User{Id: 1, Username: "sql-recipient", AffCode: "recipient", Role: common.RoleCommonUser}, Quota: 123, UsedQuota: 456}).Error)
			require.NoError(t, db.Create(&legacyRechargeTopUp{TopUp: TopUp{UserId: 1, TradeNo: "legacy-order", Amount: 1, Money: 1, Status: "success"}}).Error)
			for i := 0; i < 2; i++ {
				require.NoError(t, db.AutoMigrate(&User{}, &TopUp{}))
			}
			var user User
			require.NoError(t, db.First(&user, 1).Error)
			require.Equal(t, 123, user.Quota)
			require.Equal(t, 456, user.UsedQuota)
			require.NoError(t, db.Create(&User{Id: 2, Username: "sql-admin", AffCode: "admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled}).Error)
			previousDB, previousRedis := DB, common.RedisEnabled
			DB, common.RedisEnabled = db, false
			t.Cleanup(func() { DB, common.RedisEnabled = previousDB, previousRedis })
			tradeNo := "sql-recharge-" + uuid.NewString()
			errorsCh := make(chan error, 12)
			var wg sync.WaitGroup
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := CreateAdminRecharge(context.Background(), &TopUp{UserId: 1, AdminId: 2, TradeNo: tradeNo, Source: TopUpSourceMujianWallet, PaymentMethod: TopUpPaymentMethodAdmin, Amount: 1000000, CreditedQuota: 6849315068, Status: common.TopUpStatusSuccess})
					errorsCh <- err
				}()
			}
			wg.Wait()
			close(errorsCh)
			for err := range errorsCh {
				require.NoError(t, err)
			}
			require.NoError(t, db.First(&user, 1).Error)
			require.EqualValues(t, 6849315191, user.Quota)
			var count int64
			require.NoError(t, db.Model(&TopUp{}).Count(&count).Error)
			require.EqualValues(t, 2, count)
			require.NoError(t, db.Model(&user).Update("used_quota", int64(6849315068)).Error)
		})
	}
}
