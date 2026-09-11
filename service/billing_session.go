package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// BillingSession — 统一计费会话
// ---------------------------------------------------------------------------

// BillingSession 封装单次请求的预扣费/结算/退款生命周期。
// 实现 relaycommon.BillingSettler 接口。
type BillingSession struct {
	relayInfo         *relaycommon.RelayInfo
	funding           FundingSource
	preConsumedQuota  int  // 实际预扣额度（信任用户可能为 0）
	tokenConsumed     int  // 令牌额度实际扣减量
	fundingSettled    bool // funding.Settle 已成功，资金来源已提交
	settled           bool // Settle 全部完成（资金 + 令牌）
	settlementStarted bool // strict 结算目标已确定，不得再退还预扣
	refunded          bool // Refund 已调用
	mu                sync.Mutex
}

const (
	strictSettlementMaxAttempts = 3
	strictSettlementBackoff     = 10 * time.Millisecond
)

// Settle 根据实际消耗额度进行结算。严格路径在一个数据库事务内提交
// funding、token 和 ledger；普通路径保留原有的两步提交行为。
func (s *BillingSession) Settle(actualQuota int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settled {
		return nil
	}
	delta := actualQuota - s.preConsumedQuota
	if s.usesAtomicStrictBilling() {
		// Once upstream usage is known, a failed settlement leaves the consumed
		// ledger intact for explicit idempotent reconciliation. Refunding here
		// would erase a real provider liability. No background retry is implied.
		s.settlementStarted = true
		err := settleStrictBillingWithRetry(model.StrictBillingParams{
			RequestID:      s.relayInfo.RequestId,
			UserID:         s.relayInfo.UserId,
			SubscriptionID: s.relayInfo.SubscriptionId,
			TokenID:        s.relayInfo.TokenId,
			TokenKey:       s.relayInfo.TokenKey,
			SkipToken:      s.relayInfo.IsPlayground,
			ActualQuota:    actualQuota,
			AllowOverdraft: s.relayInfo.PriceData.SettlesUpstreamUsage(),
		})
		if err != nil {
			return err
		}
		s.fundingSettled = true
		if s.funding.Source() == BillingSourceSubscription {
			s.relayInfo.SubscriptionPostDelta += int64(delta)
		}
		s.settled = true
		return nil
	}
	if delta == 0 {
		s.settled = true
		return nil
	}
	// 1) 调整资金来源（仅在尚未提交时执行，防止重复调用）
	if !s.fundingSettled {
		if err := s.funding.Settle(delta); err != nil {
			return err
		}
		s.fundingSettled = true
	}
	// 2) 调整令牌额度
	var tokenErr error
	if !s.relayInfo.IsPlayground {
		if delta > 0 {
			tokenErr = model.DecreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, delta)
		} else {
			tokenErr = model.IncreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, -delta)
		}
		if tokenErr != nil {
			// 资金来源已提交，令牌调整失败只能记录日志；标记 settled 防止 Refund 误退资金
			common.SysLog(fmt.Sprintf("error adjusting token quota after funding settled (userId=%d, tokenId=%d, delta=%d): %s",
				s.relayInfo.UserId, s.relayInfo.TokenId, delta, tokenErr.Error()))
		}
	}
	// 3) 更新 relayInfo 上的订阅 PostDelta（用于日志）
	if s.funding.Source() == BillingSourceSubscription {
		s.relayInfo.SubscriptionPostDelta += int64(delta)
	}
	s.settled = true
	return tokenErr
}

func settleStrictBillingWithRetry(params model.StrictBillingParams) error {
	var lastErr error
	for attempt := 1; attempt <= strictSettlementMaxAttempts; attempt++ {
		lastErr = model.SettleStrictBilling(params)
		if lastErr == nil {
			return nil
		}
		if attempt < strictSettlementMaxAttempts {
			// Atomic settlement is idempotent, so a short bounded delay safely
			// absorbs brief database lock/conflict windows without rerunning usage
			// accounting or the consume log.
			time.Sleep(time.Duration(attempt) * strictSettlementBackoff)
		}
	}
	return fmt.Errorf(
		"strict billing settlement failed after %d synchronous attempts; manual_reconciliation_required=true; automatic_settlement_retry=false: %w",
		strictSettlementMaxAttempts,
		lastErr,
	)
}

// Refund 退还所有预扣费，幂等安全。严格预扣路径同步持久化，
// 普通路径保留原有的异步退款行为。
func (s *BillingSession) Refund(c *gin.Context) {
	s.mu.Lock()
	if s.settled || s.refunded || !s.needsRefundLocked() {
		s.mu.Unlock()
		return
	}
	strict := s.usesAtomicStrictBilling()
	if !strict {
		s.refunded = true
	}
	tokenId := s.relayInfo.TokenId
	tokenKey := s.relayInfo.TokenKey
	isPlayground := s.relayInfo.IsPlayground
	tokenConsumed := s.tokenConsumed
	funding := s.funding
	requestID := s.relayInfo.RequestId
	userID := s.relayInfo.UserId
	subscriptionID := s.relayInfo.SubscriptionId
	s.mu.Unlock()

	logger.LogInfo(c, fmt.Sprintf("用户 %d 请求失败, 返还预扣费（token_quota=%s, funding=%s）",
		userID,
		logger.FormatQuota(tokenConsumed),
		funding.Source(),
	))

	if strict {
		err := model.RefundStrictBilling(model.StrictBillingParams{
			RequestID:      requestID,
			UserID:         userID,
			SubscriptionID: subscriptionID,
			TokenID:        tokenId,
			TokenKey:       tokenKey,
			SkipToken:      isPlayground,
		})
		if err != nil {
			common.SysLog("error refunding strict billing reservation: " + err.Error())
			return
		}
		s.mu.Lock()
		s.refunded = true
		s.mu.Unlock()
		return
	}

	refund := func() {
		// 1) 退还资金来源
		if err := funding.Refund(); err != nil {
			common.SysLog("error refunding billing source: " + err.Error())
		}
		// 2) 退还令牌额度
		if tokenConsumed > 0 && !isPlayground {
			var err error
			err = model.IncreaseTokenQuota(tokenId, tokenKey, tokenConsumed)
			if err != nil {
				common.SysLog("error refunding token quota: " + err.Error())
			}
		}
	}
	gopool.Go(refund)
}

// NeedsRefund 返回是否存在需要退还的预扣状态。
func (s *BillingSession) NeedsRefund() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.needsRefundLocked()
}

func (s *BillingSession) needsRefundLocked() bool {
	if s.settled || s.refunded || s.fundingSettled || s.settlementStarted {
		// fundingSettled 时资金来源已提交结算，不能再退预扣费
		return false
	}
	if s.tokenConsumed > 0 {
		return true
	}
	// 订阅可能在 tokenConsumed=0 时仍预扣了额度
	if sub, ok := s.funding.(*SubscriptionFunding); ok && sub.preConsumed > 0 {
		return true
	}
	return false
}

// GetPreConsumedQuota 返回实际预扣的额度。
func (s *BillingSession) GetPreConsumedQuota() int {
	return s.preConsumedQuota
}

// ---------------------------------------------------------------------------
// PreConsume — 统一预扣费入口（含信任额度旁路）
// ---------------------------------------------------------------------------

// preConsume 执行预扣费。严格路径将 token、funding、ledger 放入同一
// 数据库事务；普通路径保留原有的顺序预扣及失败回滚。
func (s *BillingSession) preConsume(c *gin.Context, quota int) *types.NewAPIError {
	effectiveQuota := quota
	if s.usesAtomicStrictBilling() {
		if effectiveQuota > 0 {
			logger.LogInfo(c, fmt.Sprintf("用户 %d 需要严格预扣费 %s (funding=%s)", s.relayInfo.UserId, logger.FormatQuota(effectiveQuota), s.funding.Source()))
		}
		return s.preConsumeStrict(effectiveQuota)
	}

	// ---- 信任额度旁路 ----
	if s.shouldTrust(c) {
		effectiveQuota = 0
		logger.LogInfo(c, fmt.Sprintf("用户 %d 额度充足, 信任且不需要预扣费 (funding=%s)", s.relayInfo.UserId, s.funding.Source()))
	} else if effectiveQuota > 0 {
		logger.LogInfo(c, fmt.Sprintf("用户 %d 需要预扣费 %s (funding=%s)", s.relayInfo.UserId, logger.FormatQuota(effectiveQuota), s.funding.Source()))
	}

	// ---- 1) 预扣令牌额度 ----
	if effectiveQuota > 0 {
		if err := PreConsumeTokenQuota(s.relayInfo, effectiveQuota); err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		s.tokenConsumed = effectiveQuota
	}

	// ---- 2) 预扣资金来源 ----
	if err := s.funding.PreConsume(effectiveQuota); err != nil {
		// 预扣费失败，回滚令牌额度
		if s.tokenConsumed > 0 && !s.relayInfo.IsPlayground {
			rollbackErr := model.IncreaseTokenQuota(s.relayInfo.TokenId, s.relayInfo.TokenKey, s.tokenConsumed)
			if rollbackErr != nil {
				common.SysLog(fmt.Sprintf("error rolling back token quota (userId=%d, tokenId=%d, amount=%d, fundingErr=%s): %s",
					s.relayInfo.UserId, s.relayInfo.TokenId, s.tokenConsumed, err.Error(), rollbackErr.Error()))
			}
			s.tokenConsumed = 0
		}
		if errors.Is(err, model.ErrInsufficientUserQuota) {
			return types.NewErrorWithStatusCode(
				fmt.Errorf("预扣费额度失败: %w", err),
				types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
				types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		// TODO: model 层应定义哨兵错误（如 ErrNoActiveSubscription），用 errors.Is 替代字符串匹配
		errMsg := err.Error()
		if strings.Contains(errMsg, "no active subscription") || strings.Contains(errMsg, "subscription quota insufficient") {
			return types.NewErrorWithStatusCode(fmt.Errorf("订阅额度不足或未配置订阅: %s", errMsg), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		}
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}

	s.preConsumedQuota = effectiveQuota

	// ---- 同步 RelayInfo 兼容字段 ----
	s.syncRelayInfo()

	return nil
}

func (s *BillingSession) preConsumeStrict(quota int) *types.NewAPIError {
	useSubscription := s.funding.Source() == BillingSourceSubscription
	if !useSubscription && s.funding.Source() != BillingSourceWallet {
		return types.NewError(errors.New("unsupported strict billing source"), types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	result, err := model.PreConsumeStrictBilling(model.StrictBillingPreConsumeParams{
		RequestID:       s.relayInfo.RequestId,
		UserID:          s.relayInfo.UserId,
		UseSubscription: useSubscription,
		TokenID:         s.relayInfo.TokenId,
		TokenKey:        s.relayInfo.TokenKey,
		SkipToken:       s.relayInfo.IsPlayground,
		Quota:           quota,
	})
	if err != nil {
		return strictPreConsumeAPIError(err, quota)
	}

	s.preConsumedQuota = quota
	s.tokenConsumed = quota
	switch funding := s.funding.(type) {
	case *WalletFunding:
		funding.consumed = quota
	case *SubscriptionFunding:
		funding.subscriptionId = result.SubscriptionID
		funding.preConsumed = result.PreConsumed
		funding.AmountTotal = result.AmountTotal
		funding.AmountUsedAfter = result.AmountUsedAfter
		if planInfo, planErr := model.GetSubscriptionPlanInfoByUserSubscriptionId(result.SubscriptionID); planErr == nil && planInfo != nil {
			funding.PlanId = planInfo.PlanId
			funding.PlanTitle = planInfo.PlanTitle
		}
	}
	s.syncRelayInfo()
	return nil
}

func (s *BillingSession) usesAtomicStrictBilling() bool {
	return s.relayInfo.UsesAtomicStrictBilling()
}

func strictPreConsumeAPIError(err error, quota int) *types.NewAPIError {
	if errors.Is(err, model.ErrInsufficientTokenQuota) {
		wrapped := fmt.Errorf("token quota is not enough, need quota: %s: %w", logger.FormatQuota(quota), err)
		return types.NewErrorWithStatusCode(wrapped, types.ErrorCodePreConsumeTokenQuotaFailed, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	if errors.Is(err, model.ErrInsufficientUserQuota) {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("预扣费额度失败: %w", err),
			types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
			types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "no active subscription") || strings.Contains(errMsg, "subscription quota insufficient") {
		return types.NewErrorWithStatusCode(fmt.Errorf("订阅额度不足或未配置订阅: %s", errMsg), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
}

// shouldTrust 统一信任额度检查，适用于钱包和订阅。
func (s *BillingSession) shouldTrust(c *gin.Context) bool {
	// 异步任务（ForcePreConsume=true）必须预扣全额，不允许信任旁路
	if s.relayInfo.ForcePreConsume {
		return false
	}

	trustQuota := common.GetTrustQuota()
	if trustQuota <= 0 {
		return false
	}

	// 检查令牌是否充足
	tokenTrusted := s.relayInfo.TokenUnlimited
	if !tokenTrusted {
		tokenQuota := c.GetInt("token_quota")
		tokenTrusted = tokenQuota > trustQuota
	}
	if !tokenTrusted {
		return false
	}

	switch s.funding.Source() {
	case BillingSourceWallet:
		return s.relayInfo.UserQuota > trustQuota
	case BillingSourceSubscription:
		// 订阅不能启用信任旁路。原因：
		// 1. PreConsumeUserSubscription 要求 amount>0 来创建预扣记录并锁定订阅
		// 2. SubscriptionFunding.PreConsume 忽略参数，始终用 s.amount 预扣
		// 3. 若信任旁路将 effectiveQuota 设为 0，会导致 preConsumedQuota 与实际订阅预扣不一致
		return false
	default:
		return false
	}
}

// syncRelayInfo 将 BillingSession 的状态同步到 RelayInfo 的兼容字段上。
func (s *BillingSession) syncRelayInfo() {
	info := s.relayInfo
	info.FinalPreConsumedQuota = s.preConsumedQuota
	info.BillingSource = s.funding.Source()

	if sub, ok := s.funding.(*SubscriptionFunding); ok {
		info.SubscriptionId = sub.subscriptionId
		info.SubscriptionPreConsumed = sub.preConsumed
		info.SubscriptionPostDelta = 0
		info.SubscriptionAmountTotal = sub.AmountTotal
		info.SubscriptionAmountUsedAfterPreConsume = sub.AmountUsedAfter
		info.SubscriptionPlanId = sub.PlanId
		info.SubscriptionPlanTitle = sub.PlanTitle
	} else {
		info.SubscriptionId = 0
		info.SubscriptionPreConsumed = 0
	}
}

// ---------------------------------------------------------------------------
// NewBillingSession 工厂 — 根据计费偏好创建会话并处理回退
// ---------------------------------------------------------------------------

// NewBillingSession 根据用户计费偏好创建 BillingSession，处理 subscription_first / wallet_first 的回退。
func NewBillingSession(c *gin.Context, relayInfo *relaycommon.RelayInfo, preConsumedQuota int) (*BillingSession, *types.NewAPIError) {
	if relayInfo == nil {
		return nil, types.NewError(fmt.Errorf("relayInfo is nil"), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	pref := common.NormalizeBillingPreference(relayInfo.UserSetting.BillingPreference)

	// 钱包路径需要先检查用户额度
	tryWallet := func() (*BillingSession, *types.NewAPIError) {
		var userQuota int
		var err error
		if relayInfo.ForcePreConsume {
			userQuota, err = model.GetUserQuotaDirect(relayInfo.UserId)
		} else {
			userQuota, err = model.GetUserQuota(relayInfo.UserId, false)
		}
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if !relayInfo.ForcePreConsume {
			if userQuota <= 0 {
				return nil, types.NewErrorWithStatusCode(
					fmt.Errorf("用户额度不足, 剩余额度: %s", logger.FormatQuota(userQuota)),
					types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
					types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
			}
			if userQuota-preConsumedQuota < 0 {
				return nil, types.NewErrorWithStatusCode(
					fmt.Errorf("预扣费额度失败, 用户剩余额度: %s, 需要预扣费额度: %s", logger.FormatQuota(userQuota), logger.FormatQuota(preConsumedQuota)),
					types.ErrorCodeInsufficientUserQuota, http.StatusForbidden,
					types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
			}
		}
		relayInfo.UserQuota = userQuota

		session := &BillingSession{
			relayInfo: relayInfo,
			funding: &WalletFunding{
				userId: relayInfo.UserId,
			},
		}
		if apiErr := session.preConsume(c, preConsumedQuota); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	trySubscription := func() (*BillingSession, *types.NewAPIError) {
		subConsume := int64(preConsumedQuota)
		if subConsume <= 0 {
			subConsume = 1
		}
		session := &BillingSession{
			relayInfo: relayInfo,
			funding: &SubscriptionFunding{
				requestId: relayInfo.RequestId,
				userId:    relayInfo.UserId,
				modelName: relayInfo.OriginModelName,
				amount:    subConsume,
			},
		}
		// 必须传 subConsume 而非 preConsumedQuota，保证 SubscriptionFunding.amount、
		// preConsume 参数和 FinalPreConsumedQuota 三者一致，避免订阅多扣费。
		if apiErr := session.preConsume(c, int(subConsume)); apiErr != nil {
			return nil, apiErr
		}
		return session, nil
	}

	switch pref {
	case "subscription_only":
		return trySubscription()
	case "wallet_only":
		return tryWallet()
	case "wallet_first":
		session, err := tryWallet()
		if err != nil {
			if err.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				return trySubscription()
			}
			return nil, err
		}
		return session, nil
	case "subscription_first":
		fallthrough
	default:
		hasSub, subCheckErr := model.HasActiveUserSubscription(relayInfo.UserId)
		if subCheckErr != nil {
			return nil, types.NewError(subCheckErr, types.ErrorCodeQueryDataError, types.ErrOptionWithSkipRetry())
		}
		if !hasSub {
			return tryWallet()
		}
		session, apiErr := trySubscription()
		if apiErr != nil {
			if apiErr.GetErrorCode() == types.ErrorCodeInsufficientUserQuota {
				return tryWallet()
			}
			return nil, apiErr
		}
		return session, nil
	}
}
