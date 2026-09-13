package mujian

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAdminRecharge(t *testing.T) (model.User, model.User) {
	t.Helper()
	previousDB, previousLogDB, previousRedis := model.DB, model.LOG_DB, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "recharge.db")+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	model.DB, model.LOG_DB, common.RedisEnabled = db, db, false
	t.Cleanup(func() {
		model.DB, model.LOG_DB, common.RedisEnabled = previousDB, previousLogDB, previousRedis
		_ = sqlDB.Close()
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.Log{}))
	admin := model.User{Username: "recharge-admin", AffCode: "recharge-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled}
	user := model.User{Username: "recharge-recipient", AffCode: "recharge-recipient", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Quota: 500000}
	require.NoError(t, db.Create(&admin).Error)
	require.NoError(t, db.Create(&user).Error)
	return admin, user
}

func TestAdminRechargeBoundariesAndAudit(t *testing.T) {
	admin, user := setupAdminRecharge(t)
	for _, credits := range []int{1, 1000000} {
		result, err := RechargeUserCredits(context.Background(), admin.Id, user.Id, AdminRechargeRequest{Credits: credits, Remark: "客户积分补充", RequestID: uuid.NewString()})
		require.NoError(t, err)
		user.Quota += int(walletCreditedQuota(credits))
		require.Equal(t, user.Quota, result.Quota)
		require.Equal(t, CreditsFromQuota(user.Quota), result.Credits)
		require.Equal(t, int64(credits), result.Order.Credits)
		require.Zero(t, result.Order.Money)
		var order model.TopUp
		require.NoError(t, model.DB.First(&order, result.Order.ID).Error)
		require.Equal(t, admin.Username, order.AdminUsername)
		require.Equal(t, "客户积分补充", order.Remark)
		require.Equal(t, walletCreditedQuota(credits), order.CreditedQuota)
	}
	orders, total, err := ListWalletOrders(user.Id, &common.PageInfo{})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	for _, order := range orders {
		require.Equal(t, "admin", order.PaymentMethod)
		require.Equal(t, common.TopUpStatusSuccess, order.Status)
	}
	var count int64
	require.NoError(t, model.DB.Model(&model.Log{}).Where("user_id = ? AND type = ?", user.Id, model.LogTypeManage).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestAdminRechargeValidationAndPermissions(t *testing.T) {
	admin, user := setupAdminRecharge(t)
	request := AdminRechargeRequest{Credits: 1, RequestID: uuid.NewString()}
	for _, credits := range []int{-1, 0, 1000001} {
		invalid := request
		invalid.Credits = credits
		_, err := RechargeUserCredits(context.Background(), admin.Id, user.Id, invalid)
		require.ErrorIs(t, err, ErrAdminRechargeInput)
	}
	for _, invalid := range []AdminRechargeRequest{
		{Credits: 1, RequestID: "bad"}, {Credits: 1, RequestID: uuid.Nil.String()},
		{Credits: 1, RequestID: uuid.NewString(), Remark: strings.Repeat("字", 201)},
	} {
		_, err := RechargeUserCredits(context.Background(), admin.Id, user.Id, invalid)
		require.ErrorIs(t, err, ErrAdminRechargeInput)
	}
	for _, pair := range [][2]int{{admin.Id, admin.Id}, {user.Id, admin.Id}} {
		_, err := RechargeUserCredits(context.Background(), pair[0], pair[1], request)
		require.ErrorIs(t, err, model.ErrAdminRechargePermission)
	}
	_, err := RechargeUserCredits(context.Background(), admin.Id, 999999, request)
	require.ErrorIs(t, err, model.ErrAdminRechargeUser)
	require.NoError(t, model.DB.Model(&user).Update("role", common.RoleAdminUser).Error)
	_, err = RechargeUserCredits(context.Background(), admin.Id, user.Id, request)
	require.ErrorIs(t, err, model.ErrAdminRechargePermission)
	require.NoError(t, model.DB.Model(&admin).Update("role", common.RoleRootUser).Error)
	_, err = RechargeUserCredits(context.Background(), admin.Id, user.Id, request)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&user).Update("status", common.UserStatusDisabled).Error)
	request.RequestID = uuid.NewString()
	_, err = RechargeUserCredits(context.Background(), admin.Id, user.Id, request)
	require.NoError(t, err)
	require.NoError(t, model.DB.First(&user, user.Id).Error)
	require.Equal(t, common.UserStatusDisabled, user.Status)
	require.NoError(t, model.DB.Delete(&user).Error)
	request.RequestID = uuid.NewString()
	_, err = RechargeUserCredits(context.Background(), admin.Id, user.Id, request)
	require.ErrorIs(t, err, model.ErrAdminRechargeUser)
	var count int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestAdminRechargeIdempotencyAndConcurrentConsumption(t *testing.T) {
	admin, user := setupAdminRecharge(t)
	request := AdminRechargeRequest{Credits: 73, RequestID: uuid.NewString()}
	var wg sync.WaitGroup
	errorsCh := make(chan error, 24)
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, err := RechargeUserCredits(context.Background(), admin.Id, user.Id, request)
			errorsCh <- err
		}()
		go func() {
			defer wg.Done()
			_, err := RechargeUserCredits(context.Background(), admin.Id, user.Id, AdminRechargeRequest{Credits: 73, RequestID: uuid.NewString()})
			errorsCh <- err
		}()
		go func() { defer wg.Done(); errorsCh <- model.DecreaseUserQuotaIfEnough(user.Id, 1000) }()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	quota, err := model.GetUserQuotaDirect(user.Id)
	require.NoError(t, err)
	require.Equal(t, user.Quota+9*500000-8000, quota)
	var count int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
	require.EqualValues(t, 9, count)
	for _, change := range []AdminRechargeRequest{
		{Credits: 74, RequestID: request.RequestID}, {Credits: 73, RequestID: request.RequestID, Remark: "变更"},
	} {
		_, err = RechargeUserCredits(context.Background(), admin.Id, user.Id, change)
		require.ErrorIs(t, err, model.ErrAdminRechargeConflict)
	}
	other := model.User{Username: "other-recipient", AffCode: "other-recipient", Role: common.RoleCommonUser}
	require.NoError(t, model.DB.Create(&other).Error)
	_, err = RechargeUserCredits(context.Background(), admin.Id, other.Id, request)
	require.ErrorIs(t, err, model.ErrAdminRechargeConflict)
}

func TestAdminRechargeRollsBackOrderOnBalanceFailure(t *testing.T) {
	admin, user := setupAdminRecharge(t)
	require.NoError(t, model.DB.Callback().Update().Before("gorm:update").Register("test:fail-recharge", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("injected balance write failure"))
		}
	}))
	request := AdminRechargeRequest{Credits: 100, RequestID: uuid.NewString()}
	_, err := RechargeUserCredits(context.Background(), admin.Id, user.Id, request)
	require.ErrorContains(t, err, "injected balance write failure")
	require.NoError(t, model.DB.Callback().Update().Remove("test:fail-recharge"))
	quota, err := model.GetUserQuotaDirect(user.Id)
	require.NoError(t, err)
	require.Equal(t, user.Quota, quota)
	var count int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
	require.Zero(t, count)
	_, err = RechargeUserCredits(context.Background(), admin.Id, user.Id, request)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&user).Update("quota", int64(math.MaxInt64)).Error)
	_, err = RechargeUserCredits(context.Background(), admin.Id, user.Id, AdminRechargeRequest{Credits: 1, RequestID: uuid.NewString()})
	require.ErrorContains(t, err, "账户余额超出支持范围")
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestAdminRechargeInvalidatesRedisBalance(t *testing.T) {
	admin, user := setupAdminRecharge(t)
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server is required for the real cache test")
	}
	socketDir, err := os.MkdirTemp("/tmp", "recharge-redis-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "redis.sock")
	cmd := exec.Command(binary, "--port", "0", "--unixsocket", socket, "--save", "", "--appendonly", "no")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: socket})
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, func() bool { return client.Ping(context.Background()).Err() == nil }, 5*time.Second, 20*time.Millisecond)
	previous := common.RDB
	common.RDB, common.RedisEnabled = client, true
	t.Cleanup(func() { common.RDB = previous; common.RedisEnabled = false })
	key := fmt.Sprintf("user:%d", user.Id)
	require.NoError(t, client.HSet(context.Background(), key, "Quota", user.Quota).Err())
	request := AdminRechargeRequest{Credits: 73, RequestID: uuid.NewString()}
	_, err = RechargeUserCredits(context.Background(), admin.Id, user.Id, request)
	require.NoError(t, err)
	cachedQuota, cacheErr := model.GetUserQuota(user.Id, false)
	require.NoError(t, cacheErr)
	require.Equal(t, 1000000, cachedQuota)
	require.Eventually(t, func() bool { return client.HGet(context.Background(), key, "Quota").Val() == "1000000" }, time.Second, 10*time.Millisecond)
	// Replayed success also clears stale cache without changing the balance.
	require.NoError(t, client.HSet(context.Background(), key, "Quota", 1).Err())
	_, err = RechargeUserCredits(context.Background(), admin.Id, user.Id, request)
	require.NoError(t, err)
	cachedQuota, cacheErr = model.GetUserQuota(user.Id, false)
	require.NoError(t, cacheErr)
	require.Equal(t, 1000000, cachedQuota)
	require.Eventually(t, func() bool { return client.HGet(context.Background(), key, "Quota").Val() == "1000000" }, time.Second, 10*time.Millisecond)
	quota, err := model.GetUserQuotaDirect(user.Id)
	require.NoError(t, err)
	require.Equal(t, 1000000, quota)
}
