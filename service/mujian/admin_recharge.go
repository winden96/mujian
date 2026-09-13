package mujian

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
)

type AdminRechargeRequest struct {
	Credits   int    `json:"credits"`
	Remark    string `json:"remark"`
	RequestID string `json:"request_id"`
}

type AdminRechargeResult struct {
	Order   WalletOrder `json:"order"`
	Credits float64     `json:"credits"`
	Quota   int         `json:"quota"`
}

var ErrAdminRechargeInput = errors.New("充值参数无效：积分须为 1～1,000,000 的整数，备注最多 200 字，请求标识须为 UUID")

func RechargeUserCredits(ctx context.Context, adminID, userID int, request AdminRechargeRequest) (*AdminRechargeResult, error) {
	requestID, err := uuid.Parse(request.RequestID)
	if err != nil || requestID == uuid.Nil || userID <= 0 || request.Credits < 1 || request.Credits > 1000000 || utf8.RuneCountInString(request.Remark) > 200 {
		return nil, ErrAdminRechargeInput
	}
	now := common.GetTimestamp()
	order := &model.TopUp{
		UserId: userID, AdminId: adminID, Remark: strings.TrimSpace(request.Remark),
		Amount: int64(request.Credits), CreditedQuota: walletCreditedQuota(request.Credits),
		TradeNo: fmt.Sprintf("admin_%d_%s", adminID, requestID.String()),
		Source:  model.TopUpSourceMujianWallet, PaymentMethod: model.TopUpPaymentMethodAdmin,
		Status: common.TopUpStatusSuccess, CreateTime: now, CompleteTime: now,
	}
	created, err := model.CreateAdminRecharge(ctx, order)
	if err != nil {
		return nil, err
	}
	if created {
		model.RecordLogWithAdminInfo(userID, model.LogTypeManage,
			fmt.Sprintf("管理员充值 %d 积分，订单 %s，备注：%s", request.Credits, order.TradeNo, order.Remark),
			map[string]interface{}{"admin_id": adminID, "admin_username": order.AdminUsername})
	}
	quota, err := model.GetUserQuotaDirect(userID)
	if err != nil {
		return nil, err
	}
	return &AdminRechargeResult{Order: walletOrderFromTopUp(order), Quota: quota, Credits: CreditsFromQuota(quota)}, nil
}
