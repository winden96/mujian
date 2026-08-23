package mujian

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

type wechatPayTestCredentials struct {
	platformPrivateKey *rsa.PrivateKey
}

func configureWechatPayForTest(t *testing.T) wechatPayTestCredentials {
	t.Helper()
	merchantKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	platformKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	merchantKeyPath := filepath.Join(t.TempDir(), "merchant_private_key.pem")
	publicKeyPath := filepath.Join(t.TempDir(), "wechatpay_public_key.pem")
	writePrivateKey(t, merchantKeyPath, merchantKey)
	writePublicKey(t, publicKeyPath, &platformKey.PublicKey)

	previous := struct {
		enabled                                                            bool
		appID, mchID, serial, apiKey, privatePath, publicKeyID, publicPath string
		serverAddress, payAddress, epayID, epayKey                         string
		payMethods                                                         []map[string]string
	}{
		setting.WechatPayEnabled, setting.WechatPayAppID,
		setting.WechatPayMchID, setting.WechatPayMchCertificateSerialNumber,
		setting.WechatPayAPIv3Key, setting.WechatPayMerchantPrivateKeyPath,
		setting.WechatPayPublicKeyID, setting.WechatPayPublicKeyPath,
		system_setting.ServerAddress, operation_setting.PayAddress,
		operation_setting.EpayId, operation_setting.EpayKey, operation_setting.PayMethods,
	}
	t.Cleanup(func() {
		setting.WechatPayEnabled = previous.enabled
		setting.WechatPayAppID = previous.appID
		setting.WechatPayMchID = previous.mchID
		setting.WechatPayMchCertificateSerialNumber = previous.serial
		setting.WechatPayAPIv3Key = previous.apiKey
		setting.WechatPayMerchantPrivateKeyPath = previous.privatePath
		setting.WechatPayPublicKeyID = previous.publicKeyID
		setting.WechatPayPublicKeyPath = previous.publicPath
		system_setting.ServerAddress = previous.serverAddress
		operation_setting.PayAddress = previous.payAddress
		operation_setting.EpayId = previous.epayID
		operation_setting.EpayKey = previous.epayKey
		operation_setting.PayMethods = previous.payMethods
	})

	setting.WechatPayEnabled = true
	setting.WechatPayAppID = "wx-test-app"
	setting.WechatPayMchID = "1900000109"
	setting.WechatPayMchCertificateSerialNumber = "MERCHANT_CERT_SERIAL"
	setting.WechatPayAPIv3Key = "0123456789abcdef0123456789abcdef" // gitleaks:allow -- deterministic test fixture
	setting.WechatPayMerchantPrivateKeyPath = merchantKeyPath
	setting.WechatPayPublicKeyID = "PUB_KEY_ID_TEST"
	setting.WechatPayPublicKeyPath = publicKeyPath
	system_setting.ServerAddress = "https://app.example.com"
	operation_setting.PayAddress = ""
	operation_setting.EpayId = ""
	operation_setting.EpayKey = ""
	operation_setting.PayMethods = nil
	return wechatPayTestCredentials{platformPrivateKey: platformKey}
}

func writePrivateKey(t *testing.T, path string, key *rsa.PrivateKey) {
	t.Helper()
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0o600))
}

func writePublicKey(t *testing.T, path string, key *rsa.PublicKey) {
	t.Helper()
	encoded, err := x509.MarshalPKIXPublicKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded}), 0o600))
}

func TestWechatPayCapabilityAndNativeOrder(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "wechatpay-native")
	configureWechatPayForTest(t)

	originalCreate := createWechatPayNativeOrder
	t.Cleanup(func() { createWechatPayNativeOrder = originalCreate })
	var receivedTradeNo string
	var receivedCents int64
	createWechatPayNativeOrder = func(ctx context.Context, tradeNo string, totalCents int64) (*wechatPayNativeOrder, error) {
		require.NoError(t, ctx.Err())
		receivedTradeNo = tradeNo
		receivedCents = totalCents
		return &wechatPayNativeOrder{CodeURL: "weixin://wxpay/bizpayurl?pr=test", ExpiresAt: time.Now().Add(time.Minute)}, nil
	}

	wallet, err := GetWallet(user.Id)
	require.NoError(t, err)
	require.Equal(t, []WalletPaymentMethod{{Type: WechatPayMethod, Name: "微信支付"}}, wallet.Payment.Methods)

	payment, err := CreateWalletPayment(context.Background(), user.Id, 100, WechatPayMethod)
	require.NoError(t, err)
	require.Equal(t, payment.Order.TradeNo, receivedTradeNo)
	require.Equal(t, int64(1000), receivedCents)
	require.Equal(t, "QR_CODE", payment.Checkout.Method)
	require.Equal(t, "weixin://wxpay/bizpayurl?pr=test", payment.Checkout.CodeURL)
}

func TestWechatPayGatewayFailureMarksOrderFailed(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "wechatpay-failure")
	configureWechatPayForTest(t)

	originalCreate := createWechatPayNativeOrder
	t.Cleanup(func() { createWechatPayNativeOrder = originalCreate })
	createWechatPayNativeOrder = func(context.Context, string, int64) (*wechatPayNativeOrder, error) {
		return nil, errors.New("gateway unavailable")
	}

	_, err := CreateWalletPayment(context.Background(), user.Id, 100, WechatPayMethod)
	require.EqualError(t, err, "微信支付下单失败")
	var topUp model.TopUp
	require.NoError(t, model.DB.Where("user_id = ?", user.Id).First(&topUp).Error)
	require.Equal(t, common.TopUpStatusFailed, topUp.Status)
	require.NoError(t, model.CompleteWechatPayTopUp(topUp.TradeNo, "420000000000000099", 1000, "127.0.0.1"))
	require.NoError(t, model.DB.First(&topUp, topUp.Id).Error)
	require.Equal(t, common.TopUpStatusSuccess, topUp.Status)
}

func TestWechatPayRequiresPublicHTTPSCallback(t *testing.T) {
	configureWechatPayForTest(t)
	for _, address := range []string{"http://pay.example.com", "https://localhost:3100", "https://127.0.0.1"} {
		system_setting.ServerAddress = address
		enabled, reason := wechatPayConfigurationStatus()
		require.False(t, enabled, address)
		require.Contains(t, reason, "公网 HTTPS")
	}
}

func TestWechatPaySignedNotificationCreditsExactlyOnce(t *testing.T) {
	setupTestDB(t)
	credentials := configureWechatPayForTest(t)
	user := createTestUser(t, "wechatpay-callback")
	topUp := model.TopUp{
		UserId: user.Id, Amount: 100, Money: 10, TradeNo: "MJW-WECHAT-CALLBACK",
		PaymentMethod: WechatPayMethod, Source: model.TopUpSourceMujianWallet,
		CreditedQuota: 777, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix(),
	}
	require.NoError(t, model.DB.Create(&topUp).Error)
	request := signedWechatPayNotification(t, credentials.platformPrivateKey, topUp.TradeNo, "420000000000000001", 1000)

	require.NoError(t, ProcessWechatPayNotification(context.Background(), request, "127.0.0.1"))
	afterFirst, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, user.Quota+777, afterFirst)

	request = signedWechatPayNotification(t, credentials.platformPrivateKey, topUp.TradeNo, "420000000000000001", 1000)
	require.NoError(t, ProcessWechatPayNotification(context.Background(), request, "127.0.0.1"))
	afterDuplicate, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, afterFirst, afterDuplicate)
	require.NoError(t, model.DB.First(&topUp, topUp.Id).Error)
	require.Equal(t, "420000000000000001", topUp.ProviderTradeNo)
}

func TestWechatPayNotificationRejectsInvalidSignatureAndAmount(t *testing.T) {
	setupTestDB(t)
	credentials := configureWechatPayForTest(t)
	user := createTestUser(t, "wechatpay-invalid-callback")
	topUp := model.TopUp{
		UserId: user.Id, Amount: 100, Money: 10, TradeNo: "MJW-WECHAT-INVALID",
		PaymentMethod: WechatPayMethod, Source: model.TopUpSourceMujianWallet,
		CreditedQuota: 777, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix(),
	}
	require.NoError(t, model.DB.Create(&topUp).Error)

	invalidSignature := signedWechatPayNotification(t, credentials.platformPrivateKey, topUp.TradeNo, "420000000000000002", 1000)
	invalidSignature.Header.Set("Wechatpay-Signature", "invalid")
	require.ErrorContains(t, ProcessWechatPayNotification(context.Background(), invalidSignature, "127.0.0.1"), "verify")

	wrongAmount := signedWechatPayNotification(t, credentials.platformPrivateKey, topUp.TradeNo, "420000000000000002", 999)
	require.EqualError(t, ProcessWechatPayNotification(context.Background(), wrongAmount, "127.0.0.1"), "微信支付金额与订单不一致")
	require.NoError(t, model.DB.First(&topUp, topUp.Id).Error)
	require.Equal(t, common.TopUpStatusPending, topUp.Status)
}

func signedWechatPayNotification(t *testing.T, platformKey *rsa.PrivateKey, tradeNo, transactionID string, total int64) *http.Request {
	t.Helper()
	transaction, err := json.Marshal(map[string]any{
		"appid": setting.WechatPayAppID, "mchid": setting.WechatPayMchID,
		"out_trade_no": tradeNo, "transaction_id": transactionID,
		"trade_state": "SUCCESS", "trade_type": wechatPayTransactionType,
		"amount": map[string]any{"total": total, "currency": "CNY"},
	})
	require.NoError(t, err)
	block, err := aes.NewCipher([]byte(setting.WechatPayAPIv3Key))
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	nonce := "0123456789ab"
	associatedData := "transaction"
	ciphertext := aead.Seal(nil, []byte(nonce), transaction, []byte(associatedData))
	body, err := json.Marshal(map[string]any{
		"id": "notify-id", "create_time": "2026-08-11T10:00:00+08:00",
		"event_type": "TRANSACTION.SUCCESS", "resource_type": "encrypt-resource",
		"resource": map[string]any{
			"algorithm": "AEAD_AES_256_GCM", "ciphertext": base64.StdEncoding.EncodeToString(ciphertext),
			"nonce": nonce, "associated_data": associatedData,
		},
		"summary": "payment success",
	})
	require.NoError(t, err)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	headerNonce := "notification-nonce"
	signature, err := utils.SignSHA256WithRSA(timestamp+"\n"+headerNonce+"\n"+string(body)+"\n", platformKey)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, "/api/mujian/wallet/wechatpay/notify", bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Wechatpay-Timestamp", timestamp)
	request.Header.Set("Wechatpay-Nonce", headerNonce)
	request.Header.Set("Wechatpay-Serial", setting.WechatPayPublicKeyID)
	request.Header.Set("Wechatpay-Signature", signature)
	request.Header.Set("Content-Type", "application/json")
	return request
}
