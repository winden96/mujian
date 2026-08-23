package openai

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestConvertImageEditPreservesOrderedImageArrayAndZeroFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var input bytes.Buffer
	inputWriter := multipart.NewWriter(&input)
	require.NoError(t, inputWriter.WriteField("model", "catalog-model"))
	require.NoError(t, inputWriter.WriteField("prompt", "combine references"))
	require.NoError(t, inputWriter.WriteField("n", "0"))
	require.NoError(t, inputWriter.WriteField("output_compression", "0"))
	for _, image := range []struct {
		name string
		data string
	}{{"first.png", "first-image"}, {"second.png", "second-image"}} {
		part, err := inputWriter.CreateFormFile("image[]", image.name)
		require.NoError(t, err)
		_, err = part.Write([]byte(image.data))
		require.NoError(t, err)
	}
	require.NoError(t, inputWriter.Close())
	request := httptest.NewRequest("POST", "/v1/images/edits", &input)
	request.Header.Set("Content-Type", inputWriter.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesEdits}

	converted, err := (&Adaptor{}).ConvertImageRequest(context, info, dto.ImageRequest{Model: "upstream-model"})

	require.NoError(t, err)
	mediaType, params, err := mime.ParseMediaType(context.Request.Header.Get("Content-Type"))
	require.NoError(t, err)
	require.Equal(t, "multipart/form-data", mediaType)
	reader := multipart.NewReader(converted.(*bytes.Buffer), params["boundary"])
	fields := map[string][]string{}
	imageData := make([]string, 0, 2)
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		require.NoError(t, nextErr)
		data, readErr := io.ReadAll(part)
		require.NoError(t, readErr)
		if part.FileName() != "" {
			require.Equal(t, "image[]", part.FormName())
			imageData = append(imageData, string(data))
			continue
		}
		fields[part.FormName()] = append(fields[part.FormName()], string(data))
	}
	require.Equal(t, []string{"upstream-model"}, fields["model"])
	require.Equal(t, []string{"combine references"}, fields["prompt"])
	require.Equal(t, []string{"0"}, fields["n"])
	require.Equal(t, []string{"0"}, fields["output_compression"])
	require.Equal(t, []string{"first-image", "second-image"}, imageData)
}

func TestConvertImageEditUsesImageBytesBeforeMisleadingFilename(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var input bytes.Buffer
	inputWriter := multipart.NewWriter(&input)
	require.NoError(t, inputWriter.WriteField("model", "catalog-model"))
	require.NoError(t, inputWriter.WriteField("prompt", "preserve mime"))
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="image[]"; filename="misleading.jpg"`)
	header.Set("Content-Type", "image/png")
	part, err := inputWriter.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write(append([]byte("\x89PNG\r\n\x1a\n"), []byte("image")...))
	require.NoError(t, err)
	require.NoError(t, inputWriter.Close())

	request := httptest.NewRequest("POST", "/v1/images/edits", &input)
	request.Header.Set("Content-Type", inputWriter.FormDataContentType())
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = request
	info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesEdits}

	converted, err := (&Adaptor{}).ConvertImageRequest(context, info, dto.ImageRequest{Model: "upstream-model"})
	require.NoError(t, err)
	_, params, err := mime.ParseMediaType(context.Request.Header.Get("Content-Type"))
	require.NoError(t, err)
	reader := multipart.NewReader(converted.(*bytes.Buffer), params["boundary"])
	for {
		convertedPart, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			t.Fatal("converted image part not found")
		}
		require.NoError(t, nextErr)
		if convertedPart.FileName() != "" {
			require.Equal(t, "image/png", convertedPart.Header.Get("Content-Type"))
			return
		}
	}
}
