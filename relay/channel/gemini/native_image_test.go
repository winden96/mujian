package gemini

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/mujianpricing"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConvertNativeGeminiImageRequest(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-flash-image-preview"}}
	converted, err := adaptor.ConvertImageRequest(nil, info, dto.ImageRequest{
		Prompt: "雨夜便利店", Size: "1024x1536", Quality: "high",
	})

	require.NoError(t, err)
	payload := converted.(map[string]any)
	config := payload["generationConfig"].(map[string]any)
	imageConfig := config["imageConfig"].(map[string]string)
	require.Equal(t, "9:16", imageConfig["aspectRatio"])
	require.Equal(t, "2K", imageConfig["imageSize"])
}

func TestConvertNativeGeminiProOmitsUnsupportedImageSize(t *testing.T) {
	adaptor := &Adaptor{}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3-pro-image-preview"}}
	converted, err := adaptor.ConvertImageRequest(nil, info, dto.ImageRequest{Prompt: "角色立绘", Quality: "high"})

	require.NoError(t, err)
	payload := converted.(map[string]any)
	config := payload["generationConfig"].(map[string]any)
	imageConfig := config["imageConfig"].(map[string]string)
	_, exists := imageConfig["imageSize"]
	require.False(t, exists)
}

func TestConvertNativeGeminiImageEditIncludesOrderedInlineReferences(t *testing.T) {
	first := append([]byte("\x89PNG\r\n\x1a\n"), []byte("first")...)
	second := append([]byte("\x89PNG\r\n\x1a\n"), []byte("second")...)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for index, data := range [][]byte{first, second} {
		part, err := writer.CreateFormFile("image[]", "reference.png")
		require.NoError(t, err)
		_, err = part.Write(data)
		require.NoError(t, err, index)
	}
	require.NoError(t, writer.Close())
	request := httptest.NewRequest("POST", "/v1/images/edits", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeImagesEdits,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-flash-image-preview"},
	}

	converted, err := (&Adaptor{}).ConvertImageRequest(context, info, dto.ImageRequest{
		Prompt: "融合两张参考图", Size: "9:16",
	})

	require.NoError(t, err)
	payload := converted.(map[string]any)
	contents := payload["contents"].([]dto.GeminiChatContent)
	require.Len(t, contents, 1)
	require.Len(t, contents[0].Parts, 3)
	require.Equal(t, base64.StdEncoding.EncodeToString(first), contents[0].Parts[0].InlineData.Data)
	require.Equal(t, base64.StdEncoding.EncodeToString(second), contents[0].Parts[1].InlineData.Data)
	require.Equal(t, "融合两张参考图", contents[0].Parts[2].Text)
	config := payload["generationConfig"].(map[string]any)
	require.Equal(t, []string{"IMAGE"}, config["responseModalities"])
}

func TestConvertNativeGeminiImageEditRejectsMissingReferences(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("prompt", "no image"))
	require.NoError(t, writer.Close())
	request := httptest.NewRequest("POST", "/v1/images/edits", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeImagesEdits,
		ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: "gemini-3.1-flash-image-preview"},
	}

	_, err := (&Adaptor{}).ConvertImageRequest(context, info, dto.ImageRequest{Prompt: "no image"})

	require.EqualError(t, err, "at least one reference image is required")
}

func TestGeminiImageEditOverridesMultipartContentTypeWithJSON(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/images/edits", nil)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=input")
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	info := &relaycommon.RelayInfo{
		RelayMode:   relayconstant.RelayModeImagesEdits,
		ChannelMeta: &relaycommon.ChannelMeta{ApiKey: "secret", UpstreamModelName: "gemini-3.1-flash-image-preview"},
	}
	header := http.Header{}

	err := (&Adaptor{}).SetupRequestHeader(context, &header, info)

	require.NoError(t, err)
	require.Equal(t, "application/json", header.Get("Content-Type"))
}

func TestGeminiNativeImageHandlerConvertsInlineImageResponse(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	response := &http.Response{Body: io.NopCloser(strings.NewReader(`{
		"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"cG5n"}}]}}],
		"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}
	}`))}

	usage, apiErr := GeminiNativeImageHandler(context, &relaycommon.RelayInfo{}, response)

	require.Nil(t, apiErr)
	require.Equal(t, 7, usage.PromptTokens)
	require.Equal(t, 3, usage.CompletionTokens)
	require.Equal(t, 10, usage.TotalTokens)
	var result dto.ImageResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
	require.Len(t, result.Data, 1)
	require.Equal(t, "cG5n", result.Data[0].B64Json)
}

func TestNanoRetailNativeResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		fail       bool
	}{
		{"inline", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="}}]}}],"usageMetadata":{"promptTokenCount":7,"totalTokenCount":7,"auditField":42}}`, false},
		{"url", `{"data":[{"url":"https://example.com/image.png"}]}`, false},
		{"invalid inline", `{"candidates":[{"content":{"parts":[{"inlineData":{"data":"bm90IGFuIGltYWdl"}}]}}]}`, true},
		{"invalid url", `{"data":[{"url":"javascript:alert(1)"}]}`, true},
		{"empty", `{"candidates":[]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := mujianpricing.NewImageQuote("nano-banana-2", 1, 7.3, 500000)
			require.NoError(t, err)
			info := &relaycommon.RelayInfo{ImageRetail: q}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			usage, apiErr := GeminiNativeImageHandler(c, info, &http.Response{Body: io.NopCloser(strings.NewReader(tc.body))})
			if tc.fail {
				require.NotNil(t, apiErr)
				require.Zero(t, q.ReturnedCount)
				require.Empty(t, w.Body.String())
				return
			}
			require.Nil(t, apiErr)
			require.Equal(t, 1, q.ReturnedCount)
			require.Equal(t, 13699, q.Quota(q.ReturnedCount))
			if tc.name == "inline" {
				require.Equal(t, 7, usage.PromptTokens)
				require.JSONEq(t, `{"promptTokenCount":7,"totalTokenCount":7,"auditField":42}`, string(info.ImageRetailUsage))
			}
		})
	}
}
