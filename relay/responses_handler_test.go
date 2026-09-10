package relay

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResponsesHelperRejectsAnthropicWithPassThroughOnOrOff(t *testing.T) {
	for _, passThrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "converted", true: "pass-through"}[passThrough], func(t *testing.T) {
			settings := model_setting.GetGlobalSettings()
			previous := settings.PassThroughRequestEnabled
			settings.PassThroughRequestEnabled = passThrough
			t.Cleanup(func() { settings.PassThroughRequestEnabled = previous })

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			common.SetContextKey(ctx, constant.ContextKeyChannelType, constant.ChannelTypeAnthropic)
			info := &relaycommon.RelayInfo{
				RelayMode:       0,
				RelayFormat:     types.RelayFormatOpenAIResponses,
				OriginModelName: "claude-sonnet-4-6",
				Request:         &dto.OpenAIResponsesRequest{Model: "claude-sonnet-4-6"},
			}

			relayErr := ResponsesHelper(ctx, info)

			require.NotNil(t, relayErr)
			require.Equal(t, http.StatusBadRequest, relayErr.StatusCode)
			require.ErrorContains(t, relayErr, "do not support OpenAI Responses")
			require.False(t, recorder.Result().Header.Get("Content-Type") == "text/event-stream")
		})
	}
}
