package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyRechargeUser struct {
	User      `gorm:"embedded"`
	Quota     int `gorm:"type:int;default:0"`
	UsedQuota int `gorm:"type:int;default:0;column:used_quota"`
}

func (legacyRechargeUser) TableName() string { return "users" }

type legacyRechargeTopUp struct {
	TopUp         `gorm:"embedded"`
	AdminId       int    `gorm:"-"`
	AdminUsername string `gorm:"-"`
	Remark        string `gorm:"-"`
}

func (legacyRechargeTopUp) TableName() string { return "top_ups" }

func TestAdminRechargeMigrationPreservesExistingData(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&legacyRechargeUser{}, &legacyRechargeTopUp{}))
	require.NoError(t, db.Create(&legacyRechargeUser{User: User{Id: 1, Username: "legacy", AffCode: "legacy"}, Quota: 123, UsedQuota: 456}).Error)
	require.NoError(t, db.Create(&legacyRechargeTopUp{TopUp: TopUp{Id: 1, UserId: 1, TradeNo: "legacy-paid", Amount: 100, Money: 10, Status: "success"}}).Error)
	for i := 0; i < 2; i++ {
		require.NoError(t, db.AutoMigrate(&User{}, &TopUp{}))
	}
	var user User
	require.NoError(t, db.First(&user, 1).Error)
	require.Equal(t, 123, user.Quota)
	require.Equal(t, 456, user.UsedQuota)
	var order TopUp
	require.NoError(t, db.First(&order, 1).Error)
	require.Equal(t, "legacy-paid", order.TradeNo)
	require.EqualValues(t, 10, order.Money)
	require.Zero(t, order.AdminId)
	require.Empty(t, order.Remark)
	require.NoError(t, db.Model(&user).Updates(map[string]interface{}{"quota": int64(6849315068), "used_quota": int64(6849315068)}).Error)
	require.NoError(t, db.First(&user, 1).Error)
	require.EqualValues(t, 6849315068, user.Quota)
	require.EqualValues(t, 6849315068, user.UsedQuota)
}
