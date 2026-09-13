package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPricedImageResponsePreservesBytesAndOutputUsage(t *testing.T) {
	body := `{"data":[{"b64_json":"image-content"}],"usage":{"input_tokens":18,"input_tokens_details":{"image_tokens":0,"text_tokens":18},"output_tokens":515,"output_tokens_details":{"image_tokens":515,"text_tokens":0},"total_tokens":533}}`
	writer := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(writer)
	info := &relaycommon.RelayInfo{PriceData: types.PriceData{ImageCompletionRatio: 3.75}}
	response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
	usage, apiErr := OpenaiHandlerWithUsage(ctx, info, response)
	require.Nil(t, apiErr)
	require.Equal(t, body, writer.Body.String())
	require.Equal(t, 18, usage.PromptTokens)
	require.Equal(t, 515, usage.CompletionTokens)
	require.Equal(t, 515, usage.CompletionTokenDetails.ImageTokens)
}

func TestPricedImageResponseAcceptsNativeUsageWithoutOutputDetails(t *testing.T) {
	for _, detail := range []string{"", `,"output_tokens_details":null`} {
		body := `{"data":[{"b64_json":"image-content"}],"usage":{"input_tokens":3100,"input_tokens_details":{"image_tokens":3085,"text_tokens":15},"output_tokens":196,"total_tokens":3296` + detail + `}}`
		writer := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(writer)
		info := &relaycommon.RelayInfo{PriceData: types.PriceData{ImageCompletionRatio: 3.75}}
		response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
		usage, apiErr := OpenaiHandlerWithUsage(ctx, info, response)
		require.Nil(t, apiErr)
		require.Equal(t, body, writer.Body.String())
		require.Equal(t, 3100, usage.PromptTokens)
		require.Equal(t, 3085, usage.PromptTokensDetails.ImageTokens)
		require.Equal(t, 196, usage.CompletionTokens)
		require.Equal(t, 196, usage.CompletionTokenDetails.ImageTokens)
		require.Nil(t, usage.OutputTokensDetails)
	}
}

func TestPricedImageUsageRejectsIncompleteOrInconsistentAccounting(t *testing.T) {
	for name, mutate := range map[string]func(*dto.Usage){
		"missing input detail":      func(u *dto.Usage) { u.InputTokensDetails = nil },
		"image count exceeds total": func(u *dto.Usage) { u.OutputTokensDetails.ImageTokens = 516 },
		"negative cache":            func(u *dto.Usage) { u.InputTokensDetails.CachedTokens = -1 },
		"inconsistent total":        func(u *dto.Usage) { u.TotalTokens = 1 },
		"no usage":                  func(u *dto.Usage) { *u = dto.Usage{} },
	} {
		t.Run(name, func(t *testing.T) {
			u := &dto.Usage{InputTokens: 18, OutputTokens: 515, TotalTokens: 533,
				InputTokensDetails:  &dto.InputTokenDetails{TextTokens: 18},
				OutputTokensDetails: &dto.OutputTokenDetails{ImageTokens: 515}}
			mutate(u)
			require.Error(t, normalizePricedImageUsage(u))
		})
	}
}
