package model

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func enableBatchUpdatesForTest(t *testing.T) {
	t.Helper()
	previous := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() {
		common.BatchUpdateEnabled = previous
	})
}

func TestFinancialBalancesBypassProcessLocalBatchQueue(t *testing.T) {
	truncateTables(t)
	enableBatchUpdatesForTest(t)

	const (
		userID  = 9801
		tokenID = 9802
	)
	require.NoError(t, DB.Create(&User{
		Id: userID, Username: "write-through-quota-user", Password: "password",
		Quota: 100, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, DB.Create(&Token{
		Id: tokenID, UserId: userID, Key: "writethroughquotafixture", Name: "write-through quota token",
		Status: common.TokenStatusEnabled, RemainQuota: 100,
	}).Error)

	// Even with BatchUpdateEnabled, financial balances are persisted before the
	// legacy call returns. A second replica therefore sees the debit immediately.
	require.NoError(t, DecreaseUserQuota(userID, 80, false))
	require.NoError(t, DecreaseTokenQuota(tokenID, "writethroughquotafixture", 80))

	var user User
	var token Token
	require.NoError(t, DB.Select("quota").First(&user, userID).Error)
	require.NoError(t, DB.Select("remain_quota", "used_quota").First(&token, tokenID).Error)
	require.Equal(t, 20, user.Quota)
	require.Equal(t, 20, token.RemainQuota)
	require.Equal(t, 80, token.UsedQuota)
	require.ErrorIs(t, DecreaseUserQuotaIfEnough(userID, 80), ErrInsufficientUserQuota)
	require.ErrorIs(t, DecreaseTokenQuotaIfEnough(tokenID, "writethroughquotafixture", 80), ErrInsufficientTokenQuota)

	// The periodic flush has no balance delta left to apply and cannot make
	// either persisted balance negative.
	batchUpdate()
	require.NoError(t, DB.Select("quota").First(&user, userID).Error)
	require.NoError(t, DB.Select("remain_quota", "used_quota").First(&token, tokenID).Error)
	require.Equal(t, 20, user.Quota)
	require.Equal(t, 20, token.RemainQuota)
	require.Equal(t, 80, token.UsedQuota)
}

func TestConditionalQuotaReservationsAreSafeAcrossConcurrentSQLiteConnections(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}))

	const (
		userID  = 9811
		tokenID = 9812
	)
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "concurrent-quota-user", Password: "password",
		Quota: 100, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&Token{
		Id: tokenID, UserId: userID, Key: "concurrentquotafixture", Name: "concurrent quota token",
		Status: common.TokenStatusEnabled, RemainQuota: 100,
	}).Error)

	assertSingleConditionalReservation(t, func() error {
		return decreaseUserQuotaIfEnoughTx(db, userID, 80)
	})
	var user User
	require.NoError(t, db.Select("quota").First(&user, userID).Error)
	require.Equal(t, 20, user.Quota)

	assertSingleConditionalReservation(t, func() error {
		return decreaseTokenQuotaIfEnoughTx(db, tokenID, 80)
	})
	var token Token
	require.NoError(t, db.Select("remain_quota", "used_quota").First(&token, tokenID).Error)
	require.Equal(t, 20, token.RemainQuota)
	require.Equal(t, 80, token.UsedQuota)
}

func assertSingleConditionalReservation(t *testing.T, reserve func() error) {
	t.Helper()
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for index := range errs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			errs[index] = reserve()
		}(index)
	}
	close(start)
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes)
}
