package relay

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func postTextConsumeQuota(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, extraContent []string) *types.NewAPIError {
	settlementErr := service.PostTextConsumeQuota(c, info, usage, extraContent)
	return finishTextBillingResponse(c, info, settlementErr)
}

func finishTextBillingResponse(c *gin.Context, info *relaycommon.RelayInfo, settlementErr error) *types.NewAPIError {
	strict := info.UsesAtomicStrictBilling()
	if settlementErr == nil {
		if strict && !info.IsStream {
			service.FlushStagedResponseBytes(c)
		}
		return nil
	}
	if !strict {
		return nil
	}
	if !info.IsStream {
		service.DiscardStagedResponseBytes(c)
	}
	return types.NewErrorWithStatusCode(
		errors.New("billing settlement failed; reserved quota remains locked (manual_reconciliation_required; automatic_settlement_retry=false)"),
		types.ErrorCodeUpdateDataError,
		http.StatusInternalServerError,
		types.ErrOptionWithSkipRetry(),
	)
}
