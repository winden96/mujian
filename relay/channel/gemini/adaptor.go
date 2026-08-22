package gemini

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

type Adaptor struct {
}

const (
	maxNativeReferenceImages     = 3
	maxNativeReferenceImageBytes = 10 << 20
	maxNativeReferenceTotalBytes = 14 << 20
)

var nativeReferenceMimeTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	if len(request.Contents) > 0 {
		for i, content := range request.Contents {
			if i == 0 {
				if request.Contents[0].Role == "" {
					request.Contents[0].Role = "user"
				}
			}
			for _, part := range content.Parts {
				if part.FileData != nil {
					if part.FileData.MimeType == "" && strings.Contains(part.FileData.FileUri, "www.youtube.com") {
						part.FileData.MimeType = "video/webm"
					}
				}
			}
		}
	}
	return request, nil
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, req *dto.ClaudeRequest) (any, error) {
	adaptor := openai.Adaptor{}
	oaiReq, err := adaptor.ConvertClaudeRequest(c, info, req)
	if err != nil {
		return nil, err
	}
	return a.ConvertOpenAIRequest(c, info, oaiReq.(*dto.GeneralOpenAIRequest))
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	//TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	if isNativeGeminiImageModel(info.UpstreamModelName) {
		parts := []dto.GeminiPart{{Text: request.Prompt}}
		if info.RelayMode == constant.RelayModeImagesEdits {
			var err error
			parts, err = nativeGeminiReferenceParts(c, request.Prompt)
			if err != nil {
				return nil, err
			}
		}
		return map[string]any{
			"contents": []dto.GeminiChatContent{{Role: "user", Parts: parts}},
			"generationConfig": map[string]any{
				"responseModalities": []string{"IMAGE"},
				"imageConfig":        nativeGeminiImageConfig(info.UpstreamModelName, request),
			},
		}, nil
	}
	if !strings.HasPrefix(info.UpstreamModelName, "imagen") {
		return nil, errors.New("not supported model for image generation, only imagen models are supported")
	}

	// convert size to aspect ratio but allow user to specify aspect ratio
	aspectRatio := "1:1" // default aspect ratio
	size := strings.TrimSpace(request.Size)
	if size != "" {
		if strings.Contains(size, ":") {
			aspectRatio = size
		} else {
			switch size {
			case "256x256", "512x512", "1024x1024":
				aspectRatio = "1:1"
			case "1536x1024":
				aspectRatio = "3:2"
			case "1024x1536":
				aspectRatio = "2:3"
			case "1024x1792":
				aspectRatio = "9:16"
			case "1792x1024":
				aspectRatio = "16:9"
			}
		}
	}

	// build gemini imagen request
	geminiRequest := dto.GeminiImageRequest{
		Instances: []dto.GeminiImageInstance{
			{
				Prompt: request.Prompt,
			},
		},
		Parameters: dto.GeminiImageParameters{
			SampleCount:      int(lo.FromPtrOr(request.N, uint(1))),
			AspectRatio:      aspectRatio,
			PersonGeneration: "allow_adult", // default allow adult
		},
	}

	// Set imageSize when quality parameter is specified
	// Map quality parameter to imageSize (only supported by Standard and Ultra models)
	// quality values: auto, high, medium, low (for gpt-image-1), hd, standard (for dall-e-3)
	// imageSize values: 1K (default), 2K
	// https://ai.google.dev/gemini-api/docs/imagen
	// https://platform.openai.com/docs/api-reference/images/create
	if request.Quality != "" {
		imageSize := "1K" // default
		switch request.Quality {
		case "hd", "high":
			imageSize = "2K"
		case "2K":
			imageSize = "2K"
		case "standard", "medium", "low", "auto", "1K":
			imageSize = "1K"
		default:
			// unknown quality value, default to 1K
			imageSize = "1K"
		}
		geminiRequest.Parameters.ImageSize = imageSize
	}

	return geminiRequest, nil
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {

}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {

	if model_setting.GetGeminiSettings().ThinkingAdapterEnabled &&
		!model_setting.ShouldPreserveThinkingSuffix(info.OriginModelName) {
		// 新增逻辑：处理 -thinking-<budget> 格式
		if strings.Contains(info.UpstreamModelName, "-thinking-") {
			parts := strings.Split(info.UpstreamModelName, "-thinking-")
			info.UpstreamModelName = parts[0]
		} else if strings.HasSuffix(info.UpstreamModelName, "-thinking") { // 旧的适配
			info.UpstreamModelName = strings.TrimSuffix(info.UpstreamModelName, "-thinking")
		} else if strings.HasSuffix(info.UpstreamModelName, "-nothinking") {
			info.UpstreamModelName = strings.TrimSuffix(info.UpstreamModelName, "-nothinking")
		} else if baseModel, level, ok := reasoning.TrimEffortSuffix(info.UpstreamModelName); ok && level != "" {
			info.UpstreamModelName = baseModel
		}
	}

	version := model_setting.GetGeminiVersionSetting(info.UpstreamModelName)

	if strings.HasPrefix(info.UpstreamModelName, "imagen") {
		return fmt.Sprintf("%s/%s/models/%s:predict", info.ChannelBaseUrl, version, info.UpstreamModelName), nil
	}

	if strings.HasPrefix(info.UpstreamModelName, "text-embedding") ||
		strings.HasPrefix(info.UpstreamModelName, "embedding") ||
		strings.HasPrefix(info.UpstreamModelName, "gemini-embedding") {
		action := "embedContent"
		if info.IsGeminiBatchEmbedding {
			action = "batchEmbedContents"
		}
		return fmt.Sprintf("%s/%s/models/%s:%s", info.ChannelBaseUrl, version, info.UpstreamModelName, action), nil
	}

	action := "generateContent"
	if info.IsStream {
		action = "streamGenerateContent?alt=sse"
		if info.RelayMode == constant.RelayModeGemini {
			info.DisablePing = true
		}
	}
	return fmt.Sprintf("%s/%s/models/%s:%s", info.ChannelBaseUrl, version, info.UpstreamModelName, action), nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, req)
	// Gemini adaptor output is JSON even when the incoming OpenAI image-edit
	// request is multipart/form-data.
	req.Set("Content-Type", "application/json")
	req.Set("x-goog-api-key", info.ApiKey)
	if !strings.Contains(info.ChannelBaseUrl, "googleapis.com") {
		req.Set("Authorization", "Bearer "+info.ApiKey)
	}
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}

	geminiRequest, err := CovertOpenAI2Gemini(c, *request, info)
	if err != nil {
		return nil, err
	}

	return geminiRequest, nil
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return nil, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	if request.Input == nil {
		return nil, errors.New("input is required")
	}

	inputs := request.ParseInput()
	if len(inputs) == 0 {
		return nil, errors.New("input is empty")
	}
	// We always build a batch-style payload with `requests`, so ensure we call the
	// batch endpoint upstream to avoid payload/endpoint mismatches.
	info.IsGeminiBatchEmbedding = true
	// process all inputs
	geminiRequests := make([]map[string]interface{}, 0, len(inputs))
	for _, input := range inputs {
		geminiRequest := map[string]interface{}{
			"model": fmt.Sprintf("models/%s", info.UpstreamModelName),
			"content": dto.GeminiChatContent{
				Parts: []dto.GeminiPart{
					{
						Text: input,
					},
				},
			},
		}

		// set specific parameters for different models
		// https://ai.google.dev/api/embeddings?hl=zh-cn#method:-models.embedcontent
		switch info.UpstreamModelName {
		case "text-embedding-004", "gemini-embedding-exp-03-07", "gemini-embedding-001":
			// Only newer models introduced after 2024 support OutputDimensionality
			dimensions := lo.FromPtrOr(request.Dimensions, 0)
			if dimensions > 0 {
				geminiRequest["outputDimensionality"] = dimensions
			}
		}
		geminiRequests = append(geminiRequests, geminiRequest)
	}

	return map[string]interface{}{
		"requests": geminiRequests,
	}, nil
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	// TODO implement me
	return nil, errors.New("not implemented")
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	return channel.DoApiRequest(a, c, info, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	if info.RelayMode == constant.RelayModeGemini {
		if strings.Contains(info.RequestURLPath, ":embedContent") ||
			strings.Contains(info.RequestURLPath, ":batchEmbedContents") {
			return NativeGeminiEmbeddingHandler(c, resp, info)
		}
		if info.IsStream {
			return GeminiTextGenerationStreamHandler(c, info, resp)
		} else {
			return GeminiTextGenerationHandler(c, info, resp)
		}
	}

	if strings.HasPrefix(info.UpstreamModelName, "imagen") {
		return GeminiImageHandler(c, info, resp)
	}
	if (info.RelayMode == constant.RelayModeImagesGenerations || info.RelayMode == constant.RelayModeImagesEdits) &&
		isNativeGeminiImageModel(info.UpstreamModelName) {
		return GeminiNativeImageHandler(c, info, resp)
	}

	// check if the model is an embedding model
	if strings.HasPrefix(info.UpstreamModelName, "text-embedding") ||
		strings.HasPrefix(info.UpstreamModelName, "embedding") ||
		strings.HasPrefix(info.UpstreamModelName, "gemini-embedding") {
		return GeminiEmbeddingHandler(c, info, resp)
	}

	if info.IsStream {
		return GeminiChatStreamHandler(c, info, resp)
	} else {
		return GeminiChatHandler(c, info, resp)
	}

}

func isNativeGeminiImageModel(modelName string) bool {
	return strings.HasPrefix(modelName, "gemini-") && strings.Contains(modelName, "image")
}

func nativeGeminiImageConfig(modelName string, request dto.ImageRequest) map[string]string {
	imageSize := "1K"
	if request.Quality == "high" || request.Quality == "hd" || request.Quality == "2K" {
		imageSize = "2K"
	}
	config := map[string]string{"aspectRatio": imageAspectRatio(request.Size)}
	if !strings.Contains(modelName, "pro-image") {
		config["imageSize"] = imageSize
	}
	return config
}

func nativeGeminiReferenceParts(c *gin.Context, prompt string) ([]dto.GeminiPart, error) {
	if c == nil || c.Request == nil {
		return nil, errors.New("image edit request is missing")
	}
	form := c.Request.MultipartForm
	if form == nil {
		if _, err := c.MultipartForm(); err != nil {
			return nil, fmt.Errorf("failed to parse image edit form: %w", err)
		}
		form = c.Request.MultipartForm
	}
	files := orderedReferenceFiles(form)
	if len(files) == 0 {
		return nil, errors.New("at least one reference image is required")
	}
	if len(files) > maxNativeReferenceImages {
		return nil, fmt.Errorf("at most %d reference images are supported", maxNativeReferenceImages)
	}
	parts := make([]dto.GeminiPart, 0, len(files)+1)
	totalBytes := 0
	for index, header := range files {
		file, err := header.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open reference image %d: %w", index, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxNativeReferenceImageBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, fmt.Errorf("failed to read reference image %d: %w", index, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("failed to close reference image %d: %w", index, closeErr)
		}
		if len(data) > maxNativeReferenceImageBytes {
			return nil, fmt.Errorf("reference image %d exceeds 10 MiB", index)
		}
		totalBytes += len(data)
		if totalBytes > maxNativeReferenceTotalBytes {
			return nil, errors.New("reference images exceed 14 MiB in total")
		}
		declaredType := normalizeReferenceMimeType(header.Header.Get("Content-Type"))
		detectedType := normalizeReferenceMimeType(http.DetectContentType(data))
		if !nativeReferenceMimeTypes[detectedType] {
			return nil, fmt.Errorf("reference image %d has unsupported mime type %q", index, detectedType)
		}
		if declaredType != "" && declaredType != "application/octet-stream" && declaredType != detectedType {
			return nil, fmt.Errorf("reference image %d content does not match declared mime type %q", index, declaredType)
		}
		parts = append(parts, dto.GeminiPart{InlineData: &dto.GeminiInlineData{
			MimeType: detectedType,
			Data:     base64.StdEncoding.EncodeToString(data),
		}})
	}
	parts = append(parts, dto.GeminiPart{Text: prompt})
	return parts, nil
}

func normalizeReferenceMimeType(value string) string {
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	if mimeType == "image/jpg" {
		return "image/jpeg"
	}
	return mimeType
}

func orderedReferenceFiles(form *multipart.Form) []*multipart.FileHeader {
	if form == nil {
		return nil
	}
	if files := form.File["image[]"]; len(files) > 0 {
		return files
	}
	if files := form.File["image"]; len(files) > 0 {
		return files
	}
	keys := make([]string, 0)
	for key, files := range form.File {
		if strings.HasPrefix(key, "image[") && key != "image[]" && len(files) > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	files := make([]*multipart.FileHeader, 0, len(keys))
	for _, key := range keys {
		files = append(files, form.File[key]...)
	}
	return files
}

func imageAspectRatio(size string) string {
	if strings.Contains(size, ":") {
		return size
	}
	switch size {
	case "1536x1024", "1792x1024":
		return "16:9"
	case "1024x1536", "1024x1792":
		return "9:16"
	default:
		return "1:1"
	}
}

func (a *Adaptor) GetModelList() []string {
	return ModelList
}

func (a *Adaptor) GetChannelName() string {
	return ChannelName
}
