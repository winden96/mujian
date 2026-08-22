package mujian

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	wechatpay "github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

const (
	WechatPayMethod          = model.TopUpPaymentMethodWechatPay
	wechatPayOrderTTL        = 15 * time.Minute
	wechatPayTransactionType = "NATIVE"
)

type wechatPayNativeOrder struct {
	CodeURL   string
	ExpiresAt time.Time
}

var createWechatPayNativeOrder = requestWechatPayNativeOrder

func wechatPayConfigurationStatus() (bool, string) {
	if !setting.WechatPayEnabled {
		return false, ""
	}
	required := []string{
		setting.WechatPayAppID,
		setting.WechatPayMchID,
		setting.WechatPayMchCertificateSerialNumber,
		setting.WechatPayAPIv3Key,
		setting.WechatPayMerchantPrivateKeyPath,
		setting.WechatPayPublicKeyID,
		setting.WechatPayPublicKeyPath,
	}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return false, "微信支付商户参数尚未配置完整"
		}
	}
	if len(setting.WechatPayAPIv3Key) != 32 {
		return false, "微信支付 API v3 密钥必须为 32 个字符"
	}
	if !validWechatPayServerAddress(system_setting.ServerAddress) {
		return false, "微信支付回调需要正确配置公网 HTTPS 服务器地址"
	}
	if _, err := utils.LoadPrivateKeyWithPath(setting.WechatPayMerchantPrivateKeyPath); err != nil {
		return false, "微信支付商户私钥文件无法读取或格式错误"
	}
	if _, err := utils.LoadPublicKeyWithPath(setting.WechatPayPublicKeyPath); err != nil {
		return false, "微信支付公钥文件无法读取或格式错误"
	}
	return true, ""
}

func validWechatPayServerAddress(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return false
	}
	if address := net.ParseIP(host); address != nil && (address.IsLoopback() || address.IsPrivate()) {
		return false
	}
	return true
}

func newWechatPayClient(ctx context.Context) (*wechatpay.Client, error) {
	privateKey, err := utils.LoadPrivateKeyWithPath(setting.WechatPayMerchantPrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load merchant private key: %w", err)
	}
	publicKey, err := utils.LoadPublicKeyWithPath(setting.WechatPayPublicKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load wechat pay public key: %w", err)
	}
	client, err := wechatpay.NewClient(ctx, option.WithWechatPayPublicKeyAuthCipher(
		setting.WechatPayMchID,
		setting.WechatPayMchCertificateSerialNumber,
		privateKey,
		setting.WechatPayPublicKeyID,
		publicKey,
	))
	if err != nil {
		return nil, fmt.Errorf("initialize wechat pay client: %w", err)
	}
	return client, nil
}

func requestWechatPayNativeOrder(ctx context.Context, tradeNo string, totalCents int64) (*wechatPayNativeOrder, error) {
	client, err := newWechatPayClient(ctx)
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().Add(wechatPayOrderTTL)
	notifyURL := strings.TrimRight(system_setting.ServerAddress, "/") + "/api/mujian/wallet/wechatpay/notify"
	service := native.NativeApiService{Client: client}
	response, _, err := service.Prepay(ctx, native.PrepayRequest{
		Appid:       wechatpay.String(setting.WechatPayAppID),
		Mchid:       wechatpay.String(setting.WechatPayMchID),
		Description: wechatpay.String("幕间AI积分充值"),
		OutTradeNo:  wechatpay.String(tradeNo),
		TimeExpire:  wechatpay.Time(expiresAt),
		NotifyUrl:   wechatpay.String(notifyURL),
		Amount: &native.Amount{
			Total:    wechatpay.Int64(totalCents),
			Currency: wechatpay.String("CNY"),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("wechat pay native prepay: %w", err)
	}
	if response == nil || response.CodeUrl == nil || strings.TrimSpace(*response.CodeUrl) == "" {
		return nil, errors.New("wechat pay returned an empty code_url")
	}
	return &wechatPayNativeOrder{CodeURL: *response.CodeUrl, ExpiresAt: expiresAt}, nil
}

func parseWechatPayNotification(ctx context.Context, request *http.Request) (*payments.Transaction, error) {
	publicKey, err := utils.LoadPublicKeyWithPath(setting.WechatPayPublicKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load wechat pay public key: %w", err)
	}
	handler, err := notify.NewRSANotifyHandler(
		setting.WechatPayAPIv3Key,
		verifiers.NewSHA256WithRSAPubkeyVerifier(setting.WechatPayPublicKeyID, *publicKey),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize wechat pay notification handler: %w", err)
	}
	transaction := new(payments.Transaction)
	if _, err = handler.ParseNotifyRequest(ctx, request, transaction); err != nil {
		return nil, fmt.Errorf("verify wechat pay notification: %w", err)
	}
	return transaction, nil
}

func ProcessWechatPayNotification(ctx context.Context, request *http.Request, callerIP string) error {
	if enabled, reason := wechatPayConfigurationStatus(); !enabled {
		if reason == "" {
			reason = "微信支付未启用"
		}
		return errors.New(reason)
	}
	transaction, err := parseWechatPayNotification(ctx, request)
	if err != nil {
		return err
	}
	if transaction.TradeState == nil || *transaction.TradeState != "SUCCESS" {
		return errors.New("微信支付订单尚未成功")
	}
	if transaction.TradeType == nil || *transaction.TradeType != wechatPayTransactionType {
		return errors.New("微信支付交易类型不匹配")
	}
	if transaction.Mchid == nil || *transaction.Mchid != setting.WechatPayMchID ||
		transaction.Appid == nil || *transaction.Appid != setting.WechatPayAppID {
		return errors.New("微信支付商户信息不匹配")
	}
	if transaction.OutTradeNo == nil || transaction.TransactionId == nil ||
		transaction.Amount == nil || transaction.Amount.Total == nil || transaction.Amount.Currency == nil {
		return errors.New("微信支付回调缺少订单信息")
	}
	if *transaction.Amount.Currency != "CNY" {
		return errors.New("微信支付货币不匹配")
	}
	return model.CompleteWechatPayTopUp(
		*transaction.OutTradeNo,
		*transaction.TransactionId,
		*transaction.Amount.Total,
		callerIP,
	)
}
