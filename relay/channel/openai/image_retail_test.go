package openai

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/mujianpricing"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetailImageResponseCountsActualImagesAndPreservesAudit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		count      int
		fail       bool
	}{
		{"base64", `{"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="}]}`, 1, false},
		{"url without usage", `{"data":[{"url":"https://example.com/a.png"}],"created":123}`, 1, false},
		{"partial", `{"data":[{"url":"https://example.com/a.png"},{}],"usage":{"input_tokens":9,"output_tokens":22}}`, 1, false},
		{"two", `{"data":[{"url":"https://example.com/a.png"},{"url":"https://example.com/b.png"}]}`, 2, false},
		{"empty", `{"data":[]}`, 0, true},
		{"invalid image", `{"data":[{"b64_json":"bm90IGFuIGltYWdl"}]}`, 0, true},
		{"invalid url", `{"data":[{"url":"javascript:alert(1)"}]}`, 0, true},
		{"missing data", `{"error":{"message":"failed"}}`, 0, true},
		{"too many", `{"data":[{"url":"https://example.com/1"},{"url":"https://example.com/2"},{"url":"https://example.com/3"}]}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := mujianpricing.NewImageQuote("gpt-image-2", 2, 7.3, 500000)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			info := &relaycommon.RelayInfo{ImageRetail: q, PriceData: types.PriceData{ImageCompletionRatio: 3.75}}
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body))}
			usage, apiErr := OpenaiHandlerWithUsage(c, info, resp)
			if tc.fail {
				require.NotNil(t, apiErr)
				require.Empty(t, w.Body.String())
				require.Zero(t, q.ReturnedCount)
				return
			}
			require.Nil(t, apiErr)
			require.Equal(t, tc.count, q.ReturnedCount)
			var result struct {
				Data []any `json:"data"`
			}
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &result))
			require.Len(t, result.Data, tc.count)
			if tc.name == "partial" {
				require.Equal(t, 9, usage.PromptTokens)
				require.Equal(t, 22, usage.CompletionTokens)
				require.JSONEq(t, `{"input_tokens":9,"output_tokens":22}`, string(info.ImageRetailUsage))
			}
		})
	}
}
