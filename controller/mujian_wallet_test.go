package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWechatPayNotifyFailsClosedWhenPaymentIsDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousEnabled := setting.WechatPayEnabled
	t.Cleanup(func() { setting.WechatPayEnabled = previousEnabled })
	setting.WechatPayEnabled = false

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/mujian/wallet/wechatpay/notify", strings.NewReader(`{}`))
	WechatPayNotify(context)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.JSONEq(t, `{"code":"FAIL","message":"支付通知处理失败"}`, recorder.Body.String())
}

func setupEpayCallbackTest(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.Log{}))

	previousPayAddress := operation_setting.PayAddress
	previousEpayID := operation_setting.EpayId
	previousEpayKey := operation_setting.EpayKey
	t.Cleanup(func() {
		operation_setting.PayAddress = previousPayAddress
		operation_setting.EpayId = previousEpayID
		operation_setting.EpayKey = previousEpayKey
	})
	operation_setting.PayAddress = "https://pay.example.com"
	operation_setting.EpayId = "merchant-1"
	operation_setting.EpayKey = "callback-secret"
}

func signedEpayCallbackForm(tradeNo, money string) string {
	params := epay.GenerateParams(map[string]string{
		"pid":          "merchant-1",
		"type":         "alipay",
		"trade_no":     "provider-order-1",
		"out_trade_no": tradeNo,
		"money":        money,
		"trade_status": epay.StatusTradeSuccess,
	}, "callback-secret")
	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	return form.Encode()
}

func runEpayCallback(form string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/user/epay/notify", strings.NewReader(form))
	context.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	EpayNotify(context)
	return recorder
}

func TestEpayNotifyRejectsOversizedBody(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/user/epay/notify",
		strings.NewReader("payload="+strings.Repeat("a", epayNotifyBodyLimit)),
	)
	context.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	EpayNotify(context)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "fail", recorder.Body.String())
}

func TestEpayNotifyCreditsWalletOrderExactlyOnce(t *testing.T) {
	setupEpayCallbackTest(t)
	user := model.User{Username: "callback-user", Quota: 1000}
	require.NoError(t, model.DB.Create(&user).Error)
	topUp := model.TopUp{
		UserId: user.Id, Amount: 100, Money: 10, TradeNo: "MJW-test-order",
		PaymentMethod: "alipay", Source: model.TopUpSourceMujianWallet,
		CreditedQuota: 777, Status: common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(&topUp).Error)
	form := signedEpayCallbackForm(topUp.TradeNo, "10.00")

	first := runEpayCallback(form)
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "success", first.Body.String())
	require.NoError(t, model.DB.First(&user, user.Id).Error)
	require.Equal(t, 1777, user.Quota)

	second := runEpayCallback(form)
	require.Equal(t, http.StatusOK, second.Code)
	require.NoError(t, model.DB.First(&user, user.Id).Error)
	require.Equal(t, 1777, user.Quota)
}

func TestEpayNotifyRejectsMismatchedAmount(t *testing.T) {
	setupEpayCallbackTest(t)
	user := model.User{Username: "callback-mismatch", Quota: 1000}
	require.NoError(t, model.DB.Create(&user).Error)
	topUp := model.TopUp{
		UserId: user.Id, Amount: 100, Money: 10, TradeNo: "MJW-mismatch-order",
		PaymentMethod: "alipay", Source: model.TopUpSourceMujianWallet,
		CreditedQuota: 777, Status: common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(&topUp).Error)

	response := runEpayCallback(signedEpayCallbackForm(topUp.TradeNo, "9.99"))
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, "fail", response.Body.String())
	require.NoError(t, model.DB.First(&user, user.Id).Error)
	require.Equal(t, 1000, user.Quota)
	require.NoError(t, model.DB.First(&topUp, topUp.Id).Error)
	require.Equal(t, common.TopUpStatusPending, topUp.Status)
}

func TestEpayNotifyRejectsInvalidSignatureAndUnknownOrder(t *testing.T) {
	setupEpayCallbackTest(t)
	invalidSignature, err := url.ParseQuery(signedEpayCallbackForm("missing-order", "10.00"))
	require.NoError(t, err)
	invalidSignature.Set("money", "11.00")
	require.Equal(t, http.StatusBadRequest, runEpayCallback(invalidSignature.Encode()).Code)

	unknownOrder := runEpayCallback(signedEpayCallbackForm("missing-order", "10.00"))
	require.Equal(t, http.StatusBadRequest, unknownOrder.Code)
}

func TestEpayNotifyConcurrentCallbacksCreditOnlyOnce(t *testing.T) {
	setupEpayCallbackTest(t)
	user := model.User{Username: "callback-concurrent", Quota: 1000}
	require.NoError(t, model.DB.Create(&user).Error)
	topUp := model.TopUp{
		UserId: user.Id, Amount: 100, Money: 10, TradeNo: "MJW-concurrent-order",
		PaymentMethod: "alipay", Source: model.TopUpSourceMujianWallet,
		CreditedQuota: 777, Status: common.TopUpStatusPending,
	}
	require.NoError(t, model.DB.Create(&topUp).Error)
	form := signedEpayCallbackForm(topUp.TradeNo, "10.00")

	responses := make(chan int, 2)
	var callbacks sync.WaitGroup
	for range 2 {
		callbacks.Add(1)
		go func() {
			defer callbacks.Done()
			responses <- runEpayCallback(form).Code
		}()
	}
	callbacks.Wait()
	close(responses)
	for status := range responses {
		require.Equal(t, http.StatusOK, status)
	}
	require.NoError(t, model.DB.First(&user, user.Id).Error)
	require.Equal(t, 1777, user.Quota)
}
