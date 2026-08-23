package mujian

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
)

func configureWalletPaymentForTest(t *testing.T) {
	t.Helper()
	previousPayAddress := operation_setting.PayAddress
	previousEpayID := operation_setting.EpayId
	previousEpayKey := operation_setting.EpayKey
	previousMethods := operation_setting.PayMethods
	previousServerAddress := system_setting.ServerAddress
	previousCallback := operation_setting.CustomCallbackAddress
	t.Cleanup(func() {
		operation_setting.PayAddress = previousPayAddress
		operation_setting.EpayId = previousEpayID
		operation_setting.EpayKey = previousEpayKey
		operation_setting.PayMethods = previousMethods
		system_setting.ServerAddress = previousServerAddress
		operation_setting.CustomCallbackAddress = previousCallback
	})

	operation_setting.PayAddress = "https://pay.example.com"
	operation_setting.EpayId = "merchant-1"
	operation_setting.EpayKey = "test-secret"
	operation_setting.PayMethods = []map[string]string{
		{"name": "支付宝", "type": "alipay"},
		{"name": "微信支付", "type": "wxpay"},
	}
	system_setting.ServerAddress = "https://app.example.com"
	operation_setting.CustomCallbackAddress = "https://callback.example.com"
}

func TestWalletPaymentCapabilityAndPricing(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "wallet-pricing")
	configureWalletPaymentForTest(t)

	wallet, err := GetWallet(user.Id)
	require.NoError(t, err)
	require.True(t, wallet.Payment.Enabled)
	require.Equal(t, CreditsPerCNY, wallet.CreditsPerCNY)
	require.Equal(t, []WalletCreditPackage{
		{Credits: 100, Amount: 10},
		{Credits: 300, Amount: 30},
		{Credits: 500, Amount: 50},
		{Credits: 1000, Amount: 100},
	}, wallet.Packages)
	require.Equal(t, WalletCustomAmount{Min: 10, Max: 10000, Step: 10}, wallet.CustomAmount)
}

func TestWalletPaymentCapabilityRequiresHTTPSOutsideLocalDevelopment(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "wallet-insecure-callback")
	configureWalletPaymentForTest(t)
	system_setting.ServerAddress = "http://app.example.com"

	wallet, err := GetWallet(user.Id)
	require.NoError(t, err)
	require.False(t, wallet.Payment.Enabled)
	require.Equal(t, "服务器地址或支付回调地址未正确配置", wallet.Payment.DisabledReason)
}

func TestCreateWalletPaymentValidatesCreditsBeforeCreatingOrder(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "wallet-validation")
	configureWalletPaymentForTest(t)

	_, err := CreateWalletPayment(context.Background(), user.Id, 15, "alipay")
	require.EqualError(t, err, "充值积分须为 10 的倍数")
	_, err = CreateWalletPayment(context.Background(), user.Id, 10001, "alipay")
	require.EqualError(t, err, "充值积分须在 10 到 10000 之间")

	var count int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestCreateWalletPaymentBuildsAuditableEpayOrder(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "wallet-order")
	configureWalletPaymentForTest(t)

	payment, err := CreateWalletPayment(context.Background(), user.Id, 100, "wxpay")
	require.NoError(t, err)
	require.Equal(t, "POST", payment.Checkout.Method)
	require.Equal(t, "https://pay.example.com/submit.php", payment.Checkout.URL)
	require.Equal(t, payment.Order.TradeNo, payment.Checkout.Fields["out_trade_no"])
	require.Equal(t, "10.00", payment.Checkout.Fields["money"])

	var topUp model.TopUp
	require.NoError(t, model.DB.First(&topUp, payment.Order.ID).Error)
	require.Equal(t, model.TopUpSourceMujianWallet, topUp.Source)
	require.Equal(t, int64(100), topUp.Amount)
	require.Equal(t, walletCreditedQuota(100), topUp.CreditedQuota)
	require.Equal(t, common.TopUpStatusPending, topUp.Status)
}

func TestCompleteEpayWalletOrderIsAtomicAndIdempotent(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "wallet-callback")
	configureWalletPaymentForTest(t)
	payment, err := CreateWalletPayment(context.Background(), user.Id, 100, "alipay")
	require.NoError(t, err)

	before, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.EqualError(t, model.CompleteEpayTopUp(payment.Order.TradeNo, "alipay", "9.99", "127.0.0.1"), "支付金额与订单不一致")
	afterMismatch, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, before, afterMismatch)

	require.NoError(t, model.CompleteEpayTopUp(payment.Order.TradeNo, "alipay", "10.00", "127.0.0.1"))
	afterSuccess, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, before+int(walletCreditedQuota(100)), afterSuccess)

	require.NoError(t, model.CompleteEpayTopUp(payment.Order.TradeNo, "alipay", "10.00", "127.0.0.1"))
	afterDuplicate, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, afterSuccess, afterDuplicate)
}

func TestCompleteEpayWalletOrderRollsBackWhenUserUpdateFails(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "wallet-callback-rollback")
	configureWalletPaymentForTest(t)
	payment, err := CreateWalletPayment(context.Background(), user.Id, 100, "alipay")
	require.NoError(t, err)
	require.NoError(t, model.DB.Delete(&model.User{}, user.Id).Error)

	require.EqualError(
		t,
		model.CompleteEpayTopUp(payment.Order.TradeNo, "alipay", "10.00", "127.0.0.1"),
		"充值用户不存在",
	)
	var topUp model.TopUp
	require.NoError(t, model.DB.First(&topUp, payment.Order.ID).Error)
	require.Equal(t, common.TopUpStatusPending, topUp.Status)
}

func TestWalletPaymentDisabledWithoutMerchantCredentials(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "wallet-disabled")
	previousPayAddress := operation_setting.PayAddress
	previousEpayID := operation_setting.EpayId
	previousEpayKey := operation_setting.EpayKey
	t.Cleanup(func() {
		operation_setting.PayAddress = previousPayAddress
		operation_setting.EpayId = previousEpayID
		operation_setting.EpayKey = previousEpayKey
	})
	operation_setting.PayAddress = ""
	operation_setting.EpayId = ""
	operation_setting.EpayKey = ""

	wallet, err := GetWallet(user.Id)
	require.NoError(t, err)
	require.False(t, wallet.Payment.Enabled)
	require.NotEmpty(t, wallet.Payment.DisabledReason)
	_, err = CreateWalletPayment(context.Background(), user.Id, 100, "alipay")
	require.EqualError(t, err, wallet.Payment.DisabledReason)
}

func TestListWalletOrdersIsScopedToCurrentUser(t *testing.T) {
	setupTestDB(t)
	firstUser := createTestUser(t, "wallet-orders-first")
	secondUser := createTestUser(t, "wallet-orders-second")
	configureWalletPaymentForTest(t)
	firstPayment, err := CreateWalletPayment(context.Background(), firstUser.Id, 100, "alipay")
	require.NoError(t, err)
	_, err = CreateWalletPayment(context.Background(), secondUser.Id, 300, "wxpay")
	require.NoError(t, err)

	orders, total, err := ListWalletOrders(firstUser.Id, &common.PageInfo{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, orders, 1)
	require.Equal(t, firstPayment.Order.TradeNo, orders[0].TradeNo)
}

func TestManualCompleteWalletOrderUsesCreditedQuota(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "wallet-manual-complete")
	configureWalletPaymentForTest(t)
	payment, err := CreateWalletPayment(context.Background(), user.Id, 300, "wxpay")
	require.NoError(t, err)
	before, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)

	require.NoError(t, model.ManualCompleteTopUp(payment.Order.TradeNo, "127.0.0.1"))
	after, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, before+int(walletCreditedQuota(300)), after)

	require.NoError(t, model.ManualCompleteTopUp(payment.Order.TradeNo, "127.0.0.1"))
	afterDuplicate, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, after, afterDuplicate)
}
