package service

import (
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func TestManagedSalesRoundingGroupAndFixedPrice(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fixed        bool
		prompt       int
		ratio, group float64
		n            float64
		want         int
	}{
		{"cost_08_retail_1", false, 1, .8, 1, 1, 1},
		{"round_only_after_markup", false, 1, 1.4, 1, 1, 2},
		{"group_discount", false, 100, .8, .8, 1, 80},
		{"fixed_image_quantity", true, 1, .8, 1, 2, 1000000},
		{"zero_usage_refund", false, 0, .8, 1, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{StartTime: time.Now(), PriceData: types.PriceData{ChannelSpecific: true, PriceProvider: "yunwu", ModelRatio: tc.ratio, ModelPrice: tc.ratio, UsePrice: tc.fixed, CompletionRatio: 1, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: tc.group}, OtherRatios: map[string]float64{"n": tc.n}}}
			result := calculateTextQuotaSummary(ctx, info, &dto.Usage{PromptTokens: tc.prompt, TotalTokens: tc.prompt})
			require.Equal(t, tc.want, result.Quota)
		})
	}
}

func TestManagedSalesIncludesSeparateToolFees(t *testing.T) {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("claude_web_search_requests", 2)
	info := &relaycommon.RelayInfo{StartTime: time.Now(), PriceData: types.PriceData{ChannelSpecific: true, PriceProvider: "tabcode", ModelRatio: 1, CompletionRatio: 1, GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1}}}
	usage := &dto.Usage{PromptTokens: 100, TotalTokens: 100, UsageSemantic: "anthropic"}
	require.Equal(t, 12625, calculateTextQuotaSummary(ctx, info, usage).Quota)
	info.PriceData.PriceProvider = "zenmux"
	require.Equal(t, 125, calculateTextQuotaSummary(ctx, info, usage).Quota)
}
