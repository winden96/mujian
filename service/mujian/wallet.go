package mujian

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	baseservice "github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/shopspring/decimal"
)

const (
	CreditsPerCNY       = 10
	WalletMinCredits    = 10
	WalletMaxCredits    = 10000
	WalletCreditStep    = 10
	walletPaymentSource = model.TopUpSourceMujianWallet
)

var WalletCreditPackages = []int{100, 300, 500, 1000}

type WalletPaymentMethod struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type WalletCreditPackage struct {
	Credits int     `json:"credits"`
	Amount  float64 `json:"amount"`
}

type WalletPaymentCapability struct {
	Enabled        bool                  `json:"enabled"`
	Methods        []WalletPaymentMethod `json:"methods"`
	DisabledReason string                `json:"disabled_reason,omitempty"`
}

type WalletCustomAmount struct {
	Min  int `json:"min"`
	Max  int `json:"max"`
	Step int `json:"step"`
}

type Wallet struct {
	Quota         int                     `json:"quota"`
	Credits       float64                 `json:"credits"`
	QuotaPerUSD   int                     `json:"quota_per_usd"`
	CreditsPerUSD int                     `json:"credits_per_usd"`
	CreditsPerCNY int                     `json:"credits_per_cny"`
	Payment       WalletPaymentCapability `json:"payment"`
	Packages      []WalletCreditPackage   `json:"packages"`
	CustomAmount  WalletCustomAmount      `json:"custom_amount"`
}

type WalletOrder struct {
	ID            int     `json:"id"`
	TradeNo       string  `json:"trade_no"`
	Credits       int64   `json:"credits"`
	Money         float64 `json:"money"`
	PaymentMethod string  `json:"payment_method"`
	Status        string  `json:"status"`
	CreateTime    int64   `json:"create_time"`
	CompleteTime  int64   `json:"complete_time"`
}

type WalletCheckout struct {
	Method    string            `json:"method"`
	URL       string            `json:"url,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
	CodeURL   string            `json:"code_url,omitempty"`
	ExpiresAt int64             `json:"expires_at,omitempty"`
}

type WalletPayment struct {
	Order    WalletOrder    `json:"order"`
	Checkout WalletCheckout `json:"checkout"`
}

func GetWallet(userID int) (*Wallet, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return nil, err
	}
	quota, err := model.GetUserQuota(userID, true)
	if err != nil {
		return nil, err
	}
	return &Wallet{
		Quota:         quota,
		Credits:       CreditsFromQuota(quota),
		QuotaPerUSD:   QuotaPerUSD,
		CreditsPerUSD: CreditsPerUSD,
		CreditsPerCNY: CreditsPerCNY,
		Payment:       walletPaymentCapability(),
		Packages:      walletPackages(),
		CustomAmount: WalletCustomAmount{
			Min: WalletMinCredits, Max: WalletMaxCredits, Step: WalletCreditStep,
		},
	}, nil
}

func walletPaymentCapability() WalletPaymentCapability {
	methods := make([]WalletPaymentMethod, 0, 3)
	wechatEnabled, wechatDisabledReason := wechatPayConfigurationStatus()
	if wechatEnabled {
		methods = append(methods, WalletPaymentMethod{Type: WechatPayMethod, Name: "微信支付"})
	}

	epayMethods, epayDisabledReason := configuredEpayPaymentMethods()
	for _, method := range epayMethods {
		if wechatEnabled && method.Type == "wxpay" {
			continue
		}
		methods = append(methods, method)
	}
	if len(methods) > 0 {
		return WalletPaymentCapability{Enabled: true, Methods: methods}
	}
	if wechatDisabledReason != "" {
		return WalletPaymentCapability{DisabledReason: wechatDisabledReason}
	}
	return WalletPaymentCapability{DisabledReason: epayDisabledReason}
}

func configuredEpayPaymentMethods() ([]WalletPaymentMethod, string) {
	methods := configuredWalletPaymentMethods()
	if baseservice.GetEpayClient() == nil {
		return nil, "管理员尚未完成支付渠道配置"
	}
	if !validAbsoluteURL(operation_setting.PayAddress) {
		return nil, "支付网关地址未正确配置"
	}
	if len(methods) == 0 {
		return nil, "管理员尚未启用易支付渠道"
	}
	if !validAbsoluteURL(system_setting.ServerAddress) || !validAbsoluteURL(baseservice.GetCallbackAddress()) {
		return nil, "服务器地址或支付回调地址未正确配置"
	}
	return methods, ""
}

func configuredWalletPaymentMethods() []WalletPaymentMethod {
	methods := make([]WalletPaymentMethod, 0, 2)
	for _, method := range operation_setting.PayMethods {
		typeName := method["type"]
		if typeName != "alipay" && typeName != "wxpay" {
			continue
		}
		name := method["name"]
		if name == "" {
			name = "支付宝"
			if typeName == "wxpay" {
				name = "微信支付"
			}
		}
		methods = append(methods, WalletPaymentMethod{Type: typeName, Name: name})
	}
	return methods
}

func walletPackages() []WalletCreditPackage {
	packages := make([]WalletCreditPackage, 0, len(WalletCreditPackages))
	for _, credits := range WalletCreditPackages {
		packages = append(packages, WalletCreditPackage{Credits: credits, Amount: walletPaymentAmount(credits)})
	}
	return packages
}

func walletPaymentAmount(credits int) float64 {
	amount, _ := decimal.NewFromInt(int64(credits)).Div(decimal.NewFromInt(CreditsPerCNY)).Float64()
	return amount
}

func walletCreditedQuota(credits int) int64 {
	return decimal.NewFromInt(int64(credits)).Mul(decimal.NewFromInt(QuotaPerUSD)).Div(decimal.NewFromInt(CreditsPerUSD)).Round(0).IntPart()
}

func validateWalletCredits(credits int) error {
	if credits < WalletMinCredits || credits > WalletMaxCredits {
		return fmt.Errorf("充值积分须在 %d 到 %d 之间", WalletMinCredits, WalletMaxCredits)
	}
	if credits%WalletCreditStep != 0 {
		return fmt.Errorf("充值积分须为 %d 的倍数", WalletCreditStep)
	}
	return nil
}

func CreateWalletPayment(ctx context.Context, userID, credits int, paymentMethod string) (*WalletPayment, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return nil, err
	}
	if err := validateWalletCredits(credits); err != nil {
		return nil, err
	}
	capability := walletPaymentCapability()
	if !capability.Enabled {
		return nil, errors.New(capability.DisabledReason)
	}
	if !walletPaymentMethodEnabled(capability.Methods, paymentMethod) {
		return nil, errors.New("支付方式不存在或未启用")
	}

	tradeNo := fmt.Sprintf("MJW%dNO%s%d", userID, common.GetRandomString(6), time.Now().Unix())
	amount := walletPaymentAmount(credits)
	topUp := &model.TopUp{
		UserId: userID, Amount: int64(credits), Money: amount, TradeNo: tradeNo,
		PaymentMethod: paymentMethod, Source: walletPaymentSource,
		CreditedQuota: walletCreditedQuota(credits), CreateTime: time.Now().Unix(), Status: common.TopUpStatusPending,
	}
	if err := topUp.Insert(); err != nil {
		return nil, err
	}
	if paymentMethod == WechatPayMethod {
		return createWechatWalletPayment(ctx, topUp)
	}
	return createEpayWalletPayment(topUp)
}

func createWechatWalletPayment(ctx context.Context, topUp *model.TopUp) (*WalletPayment, error) {
	totalCents := decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromInt(100)).Round(0).IntPart()
	order, err := createWechatPayNativeOrder(ctx, topUp.TradeNo, totalCents)
	if err != nil {
		return nil, failWalletOrder(topUp, "微信支付下单失败", err)
	}
	return &WalletPayment{
		Order: walletOrderFromTopUp(topUp),
		Checkout: WalletCheckout{
			Method: "QR_CODE", CodeURL: order.CodeURL, ExpiresAt: order.ExpiresAt.Unix(),
		},
	}, nil
}

func createEpayWalletPayment(topUp *model.TopUp) (*WalletPayment, error) {
	client := baseservice.GetEpayClient()
	if client == nil {
		return nil, failWalletOrder(topUp, "支付网关未就绪", errors.New("epay client is not configured"))
	}
	returnURL, _ := url.Parse(strings.TrimRight(system_setting.ServerAddress, "/") + "/console/mujian/wallet?payment=return")
	notifyURL, _ := url.Parse(strings.TrimRight(baseservice.GetCallbackAddress(), "/") + "/api/user/epay/notify")
	checkoutURL, fields, err := client.Purchase(&epay.PurchaseArgs{
		Type: topUp.PaymentMethod, ServiceTradeNo: topUp.TradeNo,
		Name: "幕间AI积分充值", Money: strconv.FormatFloat(topUp.Money, 'f', 2, 64),
		Device: epay.PC, NotifyUrl: notifyURL, ReturnUrl: returnURL,
	})
	if err != nil {
		return nil, failWalletOrder(topUp, "拉起支付失败", err)
	}

	return &WalletPayment{
		Order:    walletOrderFromTopUp(topUp),
		Checkout: WalletCheckout{Method: "POST", URL: checkoutURL, Fields: fields},
	}, nil
}

func failWalletOrder(topUp *model.TopUp, message string, cause error) error {
	topUp.Status = common.TopUpStatusFailed
	if updateErr := topUp.Update(); updateErr != nil {
		common.SysError(fmt.Sprintf("%s; update order status: %v; cause: %v", message, updateErr, cause))
		return errors.New(message + "，订单状态更新失败")
	}
	common.SysError(message + ": " + cause.Error())
	return errors.New(message)
}

func ListWalletOrders(userID int, pageInfo *common.PageInfo) ([]WalletOrder, int64, error) {
	topUps, total, err := model.GetUserMujianWalletTopUps(userID, pageInfo)
	if err != nil {
		return nil, 0, err
	}
	orders := make([]WalletOrder, 0, len(topUps))
	for _, topUp := range topUps {
		orders = append(orders, walletOrderFromTopUp(topUp))
	}
	return orders, total, nil
}

func walletOrderFromTopUp(topUp *model.TopUp) WalletOrder {
	status := topUp.Status
	if status == common.TopUpStatusPending && topUp.PaymentMethod == WechatPayMethod &&
		time.Now().Unix() >= topUp.CreateTime+int64(wechatPayOrderTTL/time.Second) {
		status = "expired"
	}
	return WalletOrder{
		ID: topUp.Id, TradeNo: topUp.TradeNo, Credits: topUp.Amount, Money: topUp.Money,
		PaymentMethod: topUp.PaymentMethod, Status: status,
		CreateTime: topUp.CreateTime, CompleteTime: topUp.CompleteTime,
	}
}

func walletPaymentMethodEnabled(methods []WalletPaymentMethod, candidate string) bool {
	for _, method := range methods {
		if method.Type == candidate {
			return true
		}
	}
	return false
}

func validAbsoluteURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	host := parsed.Hostname()
	return parsed.Scheme == "http" && (host == "localhost" || net.ParseIP(host).IsLoopback())
}
