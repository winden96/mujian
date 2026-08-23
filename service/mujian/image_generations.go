package mujian

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianobject"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	MaxImageReferenceCount      = 3
	MaxImageReferenceBytes      = 10 << 20
	MaxImageReferenceTotalBytes = 14 << 20
	MaxImageGenerationBodyBytes = 16 << 20
	MaxGeneratedImageBytes      = 24 << 20
	MaxImagePromptRunes         = 8000

	imageGenerationAction          = "mujian-project-image"
	imageGenerationStatusQueued    = "queued"
	imageGenerationStatusRunning   = "running"
	imageGenerationStatusSucceeded = "succeeded"
	imageGenerationStatusFailed    = "failed"
	projectImageRequestTimeout     = model.MujianImageGenerationRequestTimeout
	imageGenerationStaleAfter      = model.MujianImageGenerationLeaseTimeout
)

var allowedReferenceMIMETypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
}

type ImageReferenceInput struct {
	Name string
	Data []byte
}

type CreateImageGenerationInput struct {
	SessionID   string
	Prompt      string
	Engine      string
	ModelID     string
	AspectRatio string
	References  []ImageReferenceInput
}

type ImageReference struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	MIMEType   string `json:"mime_type"`
	SizeBytes  int64  `json:"size_bytes"`
	Position   int    `json:"position"`
	ContentURL string `json:"content_url"`
}

type ImageGenerationContent struct {
	MIMEType string
	Data     []byte
}

type ImageReferenceContent struct {
	Name     string
	MIMEType string
	Data     []byte
}

type ImageGeneration struct {
	ID             string           `json:"id"`
	ProjectID      string           `json:"project_id"`
	SessionID      string           `json:"session_id"`
	Prompt         string           `json:"prompt"`
	Engine         string           `json:"engine"`
	ModelID        string           `json:"model_id"`
	AspectRatio    string           `json:"aspect_ratio"`
	Status         string           `json:"status"`
	TaskID         string           `json:"task_id"`
	ResultURL      string           `json:"result_url,omitempty"`
	Error          string           `json:"error,omitempty"`
	RelayRequestID string           `json:"relay_request_id,omitempty"`
	References     []ImageReference `json:"references"`
	CreatedAt      int64            `json:"created_at"`
	UpdatedAt      int64            `json:"updated_at"`
}

type projectImageDispatchInput struct {
	Context     context.Context
	UserID      int
	Engine      string
	ModelID     string
	Prompt      string
	AspectRatio string
	References  []model.MujianImageReference
}

type projectImageDispatchResult struct {
	ResultURL      string
	RelayRequestID string
}

var (
	dispatchProjectImage            = relayProjectImage
	persistProjectImageGeneration   = persistImageGeneration
	imageObjectStoreFromEnvironment = mujianobject.FromEnvironment
	openImageObjectStore            = mujianobject.OpenFromEnvironment
)

func CreateImageGeneration(userID int, projectID string, input CreateImageGenerationInput) (*ImageGeneration, error) {
	return CreateImageGenerationWithContext(context.Background(), userID, projectID, input)
}

func CreateImageGenerationWithContext(ctx context.Context, userID int, projectID string, input CreateImageGenerationInput) (*ImageGeneration, error) {
	if _, err := getAgentSession(model.DB, userID, projectID, input.SessionID); err != nil {
		return nil, err
	}
	validated, err := validateImageGenerationInput(input)
	if err != nil {
		return nil, err
	}
	generation, references, task := newImageGeneration(userID, projectID, validated)
	store, objectStorageEnabled, err := imageObjectStoreFromEnvironment()
	if err != nil {
		return nil, err
	}
	uploadedKeys := make([]string, 0, len(references))
	if objectStorageEnabled {
		uploadedKeys, err = uploadImageReferences(ctx, store, references)
		if err != nil {
			return nil, err
		}
	}
	if err = persistProjectImageGeneration(generation, references, task); err != nil {
		return nil, errors.Join(err, cleanupImageObjects(store, uploadedKeys))
	}
	startProjectImageTask(userID, projectID, generation.ID, task.TaskID)
	return getImageGeneration(userID, projectID, generation.SessionID, generation.ID)
}

func GetImageGeneration(userID int, projectID, sessionID, generationID string) (*ImageGeneration, error) {
	generation, err := getImageGenerationModel(userID, projectID, sessionID, generationID)
	if err != nil {
		return nil, err
	}
	if generation.Status == imageGenerationStatusQueued || generation.Status == imageGenerationStatusRunning {
		task, exists, taskErr := model.GetByTaskId(userID, generation.TaskID)
		if taskErr != nil {
			return nil, taskErr
		}
		if !exists || task.Platform != constant.TaskPlatformMujianImage || task.Action != imageGenerationAction {
			if err = model.DB.Model(generation).Where("status IN ?", []string{imageGenerationStatusQueued, imageGenerationStatusRunning}).
				Updates(map[string]interface{}{"status": imageGenerationStatusFailed, "error": "生图任务记录不存在"}).Error; err != nil {
				return nil, err
			}
		} else if generation.Status == imageGenerationStatusQueued && task.Status == model.TaskStatusSubmitted {
			startProjectImageTask(userID, projectID, generation.ID, generation.TaskID)
		} else if task.Status == model.TaskStatusFailure {
			if err = model.DB.Model(generation).Where("status IN ?", []string{imageGenerationStatusQueued, imageGenerationStatusRunning}).
				Updates(map[string]interface{}{"status": imageGenerationStatusFailed, "error": task.FailReason}).Error; err != nil {
				return nil, err
			}
		} else if task.Status == model.TaskStatusSuccess && generation.Status != imageGenerationStatusSucceeded {
			if err = model.DB.Model(generation).Where("status IN ?", []string{imageGenerationStatusQueued, imageGenerationStatusRunning}).
				Updates(map[string]interface{}{"status": imageGenerationStatusFailed, "error": "生图任务状态不一致，请重新生成"}).Error; err != nil {
				return nil, err
			}
		} else if task.Status == model.TaskStatusInProgress &&
			task.StartTime > 0 && time.Since(time.Unix(task.StartTime, 0)) > imageGenerationStaleAfter {
			if err = failStaleProjectImageTask(task, generation.ID); err != nil {
				return nil, err
			}
		}
	}
	return getImageGeneration(userID, projectID, sessionID, generationID)
}

func RegenerateImageGeneration(userID int, projectID, sessionID, generationID string) (*ImageGeneration, error) {
	return RegenerateImageGenerationWithContext(context.Background(), userID, projectID, sessionID, generationID)
}

func RegenerateImageGenerationWithContext(ctx context.Context, userID int, projectID, sessionID, generationID string) (*ImageGeneration, error) {
	source, err := getImageGenerationModel(userID, projectID, sessionID, generationID)
	if err != nil {
		return nil, err
	}
	var stored []model.MujianImageReference
	if err = model.DB.Where("generation_id = ?", source.ID).Order("position ASC").Find(&stored).Error; err != nil {
		return nil, err
	}
	input := CreateImageGenerationInput{
		SessionID: source.SessionID,
		Prompt:    source.Prompt, Engine: source.Engine, ModelID: source.ModelID, AspectRatio: source.AspectRatio,
		References: make([]ImageReferenceInput, 0, len(stored)),
	}
	for _, reference := range stored {
		data, loadErr := loadImageReferenceData(ctx, reference)
		if loadErr != nil {
			return nil, loadErr
		}
		input.References = append(input.References, ImageReferenceInput{Name: reference.Name, Data: data})
	}
	return CreateImageGenerationWithContext(ctx, userID, projectID, input)
}

func GetImageReferenceContent(userID int, projectID, sessionID, generationID, referenceID string) (*ImageReferenceContent, error) {
	return GetImageReferenceContentWithContext(context.Background(), userID, projectID, sessionID, generationID, referenceID)
}

func GetImageReferenceContentWithContext(ctx context.Context, userID int, projectID, sessionID, generationID, referenceID string) (*ImageReferenceContent, error) {
	if _, err := getImageGenerationModel(userID, projectID, sessionID, generationID); err != nil {
		return nil, err
	}
	var reference model.MujianImageReference
	if err := model.DB.Where("id = ? AND generation_id = ?", referenceID, generationID).First(&reference).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	data, err := loadImageReferenceData(ctx, reference)
	if err != nil {
		return nil, err
	}
	return &ImageReferenceContent{Name: reference.Name, MIMEType: reference.MIMEType, Data: data}, nil
}

func GetImageGenerationContent(userID int, projectID, sessionID, generationID string) (*ImageGenerationContent, error) {
	return GetImageGenerationContentWithContext(context.Background(), userID, projectID, sessionID, generationID)
}

func GetImageGenerationContentWithContext(ctx context.Context, userID int, projectID, sessionID, generationID string) (*ImageGenerationContent, error) {
	generation, err := findImageGenerationModel(userID, projectID, sessionID, generationID, true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(generation.ResultObjectKey) != "" {
		return loadImageObject(ctx, generation.ResultObjectKey, generation.ResultMIMEType, generation.ResultSizeBytes, generation.ResultSHA256, generation.ResultETag, MaxGeneratedImageBytes)
	}
	if len(generation.ResultData) > 0 {
		return validateStoredImageGenerationContent(generation.ResultMIMEType, generation.ResultData)
	}
	return loadImageGenerationContent(generation.ResultURL)
}

func validateStoredImageGenerationContent(mimeType string, data []byte) (*ImageGenerationContent, error) {
	if len(data) == 0 {
		return nil, ErrNotFound
	}
	if len(data) > MaxGeneratedImageBytes {
		return nil, errors.New("生成图片超过可读取大小限制")
	}
	detectedType := http.DetectContentType(data[:min(len(data), 512)])
	if detectedType != mimeType || !isAllowedGeneratedImageMIME(detectedType) {
		return nil, errors.New("生成结果不是支持的图片格式")
	}
	return &ImageGenerationContent{MIMEType: detectedType, Data: data}, nil
}

func uploadImageReferences(ctx context.Context, store mujianobject.Store, references []model.MujianImageReference) ([]string, error) {
	uploadedKeys := make([]string, 0, len(references))
	for index := range references {
		reference := &references[index]
		objectKey, err := store.BuildKey(mujianobject.ObjectKindReference, reference.ID, reference.MIMEType)
		if err != nil {
			return nil, errors.Join(err, cleanupImageObjects(store, uploadedKeys))
		}
		object, err := store.Upload(ctx, mujianobject.UploadInput{
			ObjectKey:   objectKey,
			ContentType: reference.MIMEType,
			Body:        bytes.NewReader(reference.Data),
			SizeBytes:   int64(len(reference.Data)),
		})
		if err != nil {
			return nil, errors.Join(err, cleanupImageObjects(store, uploadedKeys))
		}
		uploadedKeys = append(uploadedKeys, object.Key)
		reference.ObjectKey = object.Key
		reference.ObjectSizeBytes = object.SizeBytes
		reference.ObjectSHA256 = object.SHA256
		reference.ObjectETag = object.ETag
		reference.Data = []byte{}
	}
	return uploadedKeys, nil
}

func cleanupImageObjects(store mujianobject.Store, objectKeys []string) error {
	if store == nil || len(objectKeys) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var cleanupErrors []error
	for _, objectKey := range objectKeys {
		if err := store.Delete(ctx, objectKey); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("cleanup object %q: %w", objectKey, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func loadImageReferenceData(ctx context.Context, reference model.MujianImageReference) ([]byte, error) {
	if strings.TrimSpace(reference.ObjectKey) != "" {
		content, err := loadImageObject(
			ctx,
			reference.ObjectKey,
			reference.MIMEType,
			reference.ObjectSizeBytes,
			reference.ObjectSHA256,
			reference.ObjectETag,
			MaxImageReferenceBytes,
		)
		if err != nil {
			return nil, err
		}
		return content.Data, nil
	}
	content, err := validateStoredImageGenerationContent(reference.MIMEType, reference.Data)
	if err != nil {
		return nil, err
	}
	if len(content.Data) > MaxImageReferenceBytes {
		return nil, errors.New("单张参考图不能超过 10MB")
	}
	return content.Data, nil
}

func loadImageObject(ctx context.Context, objectKey, expectedMIMEType string, expectedSize int64, expectedSHA256, expectedETag string, maxBytes int) (*ImageGenerationContent, error) {
	store, err := imageObjectStoreForRead()
	if err != nil {
		return nil, err
	}
	reader, object, err := store.Get(ctx, objectKey)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, int64(maxBytes)+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(data) > maxBytes {
		return nil, errors.New("对象存储图片超过可读取大小限制")
	}
	if expectedSize > 0 && int64(len(data)) != expectedSize {
		return nil, errors.New("对象存储图片完整性校验失败")
	}
	if object.SizeBytes > 0 && int64(len(data)) != object.SizeBytes {
		return nil, errors.New("对象存储图片完整性校验失败")
	}
	digest := sha256.Sum256(data)
	digestHex := hex.EncodeToString(digest[:])
	if expectedSHA256 != "" && !strings.EqualFold(digestHex, expectedSHA256) {
		return nil, errors.New("对象存储图片完整性校验失败")
	}
	if object.SHA256 != "" && !strings.EqualFold(digestHex, object.SHA256) {
		return nil, errors.New("对象存储图片完整性校验失败")
	}
	if expectedETag != "" && object.ETag != "" && expectedETag != object.ETag {
		return nil, errors.New("对象存储图片完整性校验失败")
	}
	if object.ContentType != "" && expectedMIMEType != "" && object.ContentType != expectedMIMEType {
		return nil, errors.New("对象存储图片类型不匹配")
	}
	mimeType := expectedMIMEType
	if mimeType == "" {
		mimeType = object.ContentType
	}
	return validateStoredImageGenerationContent(mimeType, data)
}

func imageObjectStoreForRead() (mujianobject.Store, error) {
	store, enabled, err := imageObjectStoreFromEnvironment()
	if err != nil {
		return nil, err
	}
	if enabled {
		return store, nil
	}
	return openImageObjectStore()
}

func loadImageGenerationContent(value string) (*ImageGenerationContent, error) {
	return loadImageGenerationContentWithContext(context.Background(), value)
}

func loadImageGenerationContentWithContext(ctx context.Context, value string) (*ImageGenerationContent, error) {
	if content, err := decodeBase64ImageDataURL(value, MaxGeneratedImageBytes); err == nil {
		return content, nil
	} else if strings.HasPrefix(value, "data:") {
		return nil, err
	}
	return downloadImageGenerationContent(ctx, value)
}

func decodeBase64ImageDataURL(value string, maxBytes int) (*ImageGenerationContent, error) {
	mimeType, encoded, ok := parseBase64ImageDataURL(value)
	if !ok {
		return nil, ErrNotFound
	}
	if maxBytes < 0 || len(encoded) > base64.StdEncoding.EncodedLen(maxBytes) {
		return nil, errors.New("生成图片超过可读取大小限制")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("生成图片数据无效")
	}
	if len(data) > maxBytes {
		return nil, errors.New("生成图片超过可读取大小限制")
	}
	return validateStoredImageGenerationContent(mimeType, data)
}

func listImageGenerations(userID int, projectID, sessionID string) ([]ImageGeneration, error) {
	var rows []model.MujianImageGeneration
	if err := model.DB.Omit("result_data").Where("project_id = ? AND user_id = ? AND session_id = ?", projectID, userID, sessionID).
		Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return imageGenerationDTOs(projectID, rows)
}

func getImageGeneration(userID int, projectID, sessionID, generationID string) (*ImageGeneration, error) {
	row, err := getImageGenerationModel(userID, projectID, sessionID, generationID)
	if err != nil {
		return nil, err
	}
	dtos, err := imageGenerationDTOs(projectID, []model.MujianImageGeneration{*row})
	if err != nil {
		return nil, err
	}
	if len(dtos) != 1 {
		return nil, ErrNotFound
	}
	return &dtos[0], nil
}

func getImageGenerationModel(userID int, projectID, sessionID, generationID string) (*model.MujianImageGeneration, error) {
	return findImageGenerationModel(userID, projectID, sessionID, generationID, false)
}

func findImageGenerationModel(userID int, projectID, sessionID, generationID string, includeResultData bool) (*model.MujianImageGeneration, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session_id 不能为空")
	}
	var generation model.MujianImageGeneration
	query := model.DB
	if !includeResultData {
		query = query.Omit("result_data")
	}
	if err := query.Where(
		"id = ? AND project_id = ? AND user_id = ? AND session_id = ?", generationID, projectID, userID, sessionID,
	).First(&generation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &generation, nil
}

func imageGenerationDTOs(projectID string, rows []model.MujianImageGeneration) ([]ImageGeneration, error) {
	dtos := make([]ImageGeneration, 0, len(rows))
	if len(rows) == 0 {
		return dtos, nil
	}
	ids := make([]string, 0, len(rows))
	sessionByGeneration := make(map[string]string, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		sessionByGeneration[row.ID] = row.SessionID
	}
	var references []model.MujianImageReference
	if err := model.DB.Select("id", "generation_id", "position", "name", "mime_type", "size_bytes", "object_key").
		Where("generation_id IN ?", ids).Order("generation_id ASC, position ASC").Find(&references).Error; err != nil {
		return nil, err
	}
	var objectStore mujianobject.Store
	needsObjectStore := false
	for _, row := range rows {
		if strings.TrimSpace(row.ResultObjectKey) != "" {
			needsObjectStore = true
			break
		}
	}
	if !needsObjectStore {
		for _, reference := range references {
			if strings.TrimSpace(reference.ObjectKey) != "" {
				needsObjectStore = true
				break
			}
		}
	}
	if needsObjectStore {
		var err error
		objectStore, err = imageObjectStoreForRead()
		if err != nil {
			return nil, err
		}
	}
	byGeneration := make(map[string][]ImageReference, len(rows))
	for _, reference := range references {
		contentURL := imageReferenceContentURL(projectID, reference.GenerationID, reference.ID, sessionByGeneration[reference.GenerationID])
		if reference.ObjectKey != "" {
			var err error
			contentURL, err = objectStore.PublicURL(reference.ObjectKey)
			if err != nil {
				return nil, err
			}
		}
		byGeneration[reference.GenerationID] = append(byGeneration[reference.GenerationID], ImageReference{
			ID: reference.ID, Name: reference.Name, MIMEType: reference.MIMEType, SizeBytes: reference.SizeBytes,
			Position:   reference.Position,
			ContentURL: contentURL,
		})
	}
	for _, row := range rows {
		references := byGeneration[row.ID]
		if references == nil {
			references = []ImageReference{}
		}
		resultURL, err := imageGenerationResultURL(projectID, row, objectStore)
		if err != nil {
			return nil, err
		}
		dtos = append(dtos, ImageGeneration{
			ID: row.ID, ProjectID: row.ProjectID, SessionID: row.SessionID, Prompt: row.Prompt, Engine: row.Engine, ModelID: row.ModelID,
			AspectRatio: row.AspectRatio, Status: row.Status, TaskID: row.TaskID, ResultURL: resultURL,
			Error: row.Error, RelayRequestID: row.RelayRequestID, References: references,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		})
	}
	return dtos, nil
}

func imageGenerationResultURL(projectID string, generation model.MujianImageGeneration, objectStore mujianobject.Store) (string, error) {
	if generation.Status != imageGenerationStatusSucceeded || !hasImageGenerationResult(generation) {
		return "", nil
	}
	if generation.ResultObjectKey != "" {
		return objectStore.PublicURL(generation.ResultObjectKey)
	}
	return imageGenerationContentURL(projectID, generation.ID, generation.SessionID), nil
}

func hasImageGenerationResult(generation model.MujianImageGeneration) bool {
	return strings.TrimSpace(generation.ResultObjectKey) != "" || strings.TrimSpace(generation.ResultMIMEType) != "" || strings.TrimSpace(generation.ResultURL) != ""
}

func imageGenerationContentURL(projectID, generationID, sessionID string) string {
	return "/api/mujian/projects/" + projectID + "/image-generations/" + generationID + "/content?session_id=" + url.QueryEscape(sessionID)
}

func imageReferenceContentURL(projectID, generationID, referenceID, sessionID string) string {
	return "/api/mujian/projects/" + projectID + "/image-generations/" + generationID + "/references/" + referenceID + "/content?session_id=" + url.QueryEscape(sessionID)
}

func parseBase64ImageDataURL(value string) (string, string, bool) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || encoded == "" || !strings.HasSuffix(header, ";base64") || !strings.HasPrefix(header, "data:") {
		return "", "", false
	}
	mimeType := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	if _, allowed := allowedReferenceMIMETypes[mimeType]; !allowed {
		return "", "", false
	}
	return mimeType, encoded, true
}

func materializeImageGenerationResult(value string) (*ImageGenerationContent, error) {
	return loadImageGenerationContentWithContext(context.Background(), strings.TrimSpace(value))
}

func materializeImageGenerationResultWithContext(ctx context.Context, value string) (*ImageGenerationContent, error) {
	return loadImageGenerationContentWithContext(ctx, strings.TrimSpace(value))
}

func downloadImageGenerationContent(ctx context.Context, value string) (*ImageGenerationContent, error) {
	client, err := newImageGenerationHTTPClient(value)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, value, nil)
	if err != nil {
		return nil, errors.New("生成图片地址无效")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("下载生成图片失败")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("下载生成图片失败")
	}
	if response.ContentLength > MaxGeneratedImageBytes {
		return nil, errors.New("生成图片超过可读取大小限制")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxGeneratedImageBytes+1))
	if err != nil {
		return nil, errors.New("读取生成图片失败")
	}
	if len(data) > MaxGeneratedImageBytes {
		return nil, errors.New("生成图片超过可读取大小限制")
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr != nil || (mediaType != "application/octet-stream" && !isAllowedGeneratedImageMIME(mediaType)) {
			return nil, errors.New("生成结果不是支持的图片格式")
		}
	}
	detectedType := http.DetectContentType(data[:min(len(data), 512)])
	if !isAllowedGeneratedImageMIME(detectedType) {
		return nil, errors.New("生成结果不是支持的图片格式")
	}
	return &ImageGenerationContent{MIMEType: detectedType, Data: data}, nil
}

func isAllowedGeneratedImageMIME(mimeType string) bool {
	_, allowed := allowedReferenceMIMETypes[mimeType]
	return allowed
}

func validateImageGenerationURLWithSetting(value string, fetchSetting system_setting.FetchSetting) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("生成图片地址无效")
	}
	if err := common.ValidateURLWithFetchSetting(
		value, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp,
		fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList,
		fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain,
	); err != nil {
		return errors.New("生成图片地址不安全")
	}
	return nil
}

type imageGenerationDialer struct {
	ssrfEnabled bool
	protection  *common.SSRFProtection
	lookupIP    func(context.Context, string) ([]net.IPAddr, error)
	dialContext func(context.Context, string, string) (net.Conn, error)
}

func (d *imageGenerationDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("生成图片地址无效")
	}
	var addresses []net.IPAddr
	if literal := net.ParseIP(host); literal != nil {
		addresses = append(addresses, net.IPAddr{IP: literal})
	} else {
		addresses, err = d.lookupIP(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("生成图片地址解析失败")
		}
	}
	if d.ssrfEnabled {
		for _, candidate := range addresses {
			if !d.protection.IsIPAccessAllowed(candidate.IP) {
				return nil, errors.New("生成图片地址不安全")
			}
		}
	}
	var dialErr error
	for _, candidate := range addresses {
		ipHost := candidate.IP.String()
		if candidate.Zone != "" {
			ipHost += "%" + candidate.Zone
		}
		connection, connectErr := d.dialContext(ctx, network, net.JoinHostPort(ipHost, port))
		if connectErr == nil {
			return connection, nil
		}
		dialErr = connectErr
	}
	return nil, dialErr
}

func newImageGenerationHTTPClient(initialURL string) (*http.Client, error) {
	fetchSetting := *system_setting.GetFetchSetting()
	fetchSetting.DomainList = append([]string(nil), fetchSetting.DomainList...)
	fetchSetting.IpList = append([]string(nil), fetchSetting.IpList...)
	fetchSetting.AllowedPorts = append([]string(nil), fetchSetting.AllowedPorts...)
	if err := validateImageGenerationURLWithSetting(initialURL, fetchSetting); err != nil {
		return nil, err
	}
	dialer := &imageGenerationDialer{
		ssrfEnabled: fetchSetting.EnableSSRFProtection,
		protection: &common.SSRFProtection{
			AllowPrivateIp: fetchSetting.AllowPrivateIp,
			IpFilterMode:   fetchSetting.IpFilterMode,
			IpList:         append([]string(nil), fetchSetting.IpList...),
		},
		lookupIP: net.DefaultResolver.LookupIPAddr,
		dialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	return &http.Client{
		Transport: transport,
		Timeout:   projectImageRequestTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("生成图片重定向次数过多")
			}
			return validateImageGenerationURLWithSetting(request.URL.String(), fetchSetting)
		},
	}, nil
}

func validateImageGenerationInput(input CreateImageGenerationInput) (CreateImageGenerationInput, error) {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Engine = strings.ToLower(strings.TrimSpace(input.Engine))
	input.ModelID = strings.TrimSpace(input.ModelID)
	input.AspectRatio = strings.TrimSpace(input.AspectRatio)
	if input.SessionID == "" {
		return input, errors.New("session_id 不能为空")
	}
	if input.Prompt == "" {
		return input, errors.New("图片提示词不能为空")
	}
	if len([]rune(input.Prompt)) > MaxImagePromptRunes {
		return input, fmt.Errorf("图片提示词不能超过 %d 个字符", MaxImagePromptRunes)
	}
	if input.Engine != "nano" && input.Engine != "gpt" {
		return input, errors.New("生图引擎只支持 Nano 或 GPT")
	}
	if input.AspectRatio == "" {
		input.AspectRatio = "1:1"
	}
	if input.AspectRatio != "1:1" && input.AspectRatio != "9:16" && input.AspectRatio != "16:9" {
		return input, errors.New("不支持的图片比例")
	}
	if input.ModelID == "" {
		input.ModelID = defaultImageModelForEngine(input.Engine)
	}
	if input.ModelID == "" || !modelAvailable("image", input.ModelID) {
		return input, errors.New("图像模型未在可用渠道中开放")
	}
	if imageEngine(input.ModelID) != input.Engine {
		return input, errors.New("所选模型与生图引擎不匹配")
	}
	if err := validateImageReferences(input.References); err != nil {
		return input, err
	}
	if len(input.References) > 0 {
		available, err := mujianprovider.ReferenceImageModelAvailable(input.ModelID)
		if err != nil {
			return input, err
		}
		if !available {
			return input, errors.New("所选模型暂无支持多图参考的可用渠道")
		}
	}
	return input, nil
}

func validateImageReferences(references []ImageReferenceInput) error {
	if len(references) > MaxImageReferenceCount {
		return fmt.Errorf("参考图最多 %d 张", MaxImageReferenceCount)
	}
	total := 0
	for index := range references {
		reference := &references[index]
		if len(reference.Data) == 0 {
			return fmt.Errorf("第 %d 张参考图内容为空", index+1)
		}
		if len(reference.Data) > MaxImageReferenceBytes {
			return errors.New("单张参考图不能超过 10MB")
		}
		total += len(reference.Data)
		if total > MaxImageReferenceTotalBytes {
			return errors.New("参考图总大小不能超过 14MB")
		}
		mimeType := http.DetectContentType(reference.Data[:min(len(reference.Data), 512)])
		if _, ok := allowedReferenceMIMETypes[mimeType]; !ok {
			return errors.New("参考图仅支持 PNG、JPEG 或 WebP")
		}
		reference.Name = safeReferenceName(reference.Name, mimeType, index)
	}
	return nil
}

func defaultImageModelForEngine(engine string) string {
	for _, candidate := range CatalogModels()["image"] {
		if imageEngine(candidate) == engine {
			return candidate
		}
	}
	return ""
}

func imageEngine(modelID string) string {
	if modelID == "gpt-image-2" {
		return "gpt"
	}
	if strings.HasPrefix(modelID, "nano-banana") {
		return "nano"
	}
	return ""
}

func safeReferenceName(name, mimeType string, index int) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = stripControlCharacters(name)
	if name == "" || name == "." {
		extensions, _ := mime.ExtensionsByType(mimeType)
		extension := ".img"
		if len(extensions) > 0 {
			extension = extensions[0]
		}
		name = "reference-" + strconv.Itoa(index+1) + extension
	}
	return truncateRunes(name, 255)
}

func newImageGeneration(userID int, projectID string, input CreateImageGenerationInput) (*model.MujianImageGeneration, []model.MujianImageReference, *model.Task) {
	taskID := model.GenerateTaskID()
	generation := &model.MujianImageGeneration{
		ID: uuid.NewString(), ProjectID: projectID, UserID: userID, SessionID: input.SessionID,
		Prompt: input.Prompt, Engine: input.Engine, ModelID: input.ModelID, AspectRatio: input.AspectRatio,
		Status: imageGenerationStatusQueued, TaskID: taskID,
	}
	references := make([]model.MujianImageReference, 0, len(input.References))
	for index, inputReference := range input.References {
		mimeType := http.DetectContentType(inputReference.Data[:min(len(inputReference.Data), 512)])
		references = append(references, model.MujianImageReference{
			ID: uuid.NewString(), GenerationID: generation.ID, Position: index, Name: inputReference.Name,
			MIMEType: mimeType, SizeBytes: int64(len(inputReference.Data)), Data: inputReference.Data,
		})
	}
	task := &model.Task{
		TaskID: taskID, Platform: constant.TaskPlatformMujianImage, UserId: userID, Action: imageGenerationAction,
		Status: model.TaskStatusSubmitted, Progress: "0%", SubmitTime: time.Now().Unix(),
		Properties: model.Properties{Input: input.Prompt, OriginModelName: input.ModelID},
	}
	task.SetData(map[string]string{"generation_id": generation.ID})
	return generation, references, task
}

func persistImageGeneration(generation *model.MujianImageGeneration, references []model.MujianImageReference, task *model.Task) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := claimProjectWrite(tx, generation.UserID, generation.ProjectID); err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		claim := tx.Model(&model.MujianAgentSession{}).
			Where("id = ? AND project_id = ? AND user_id = ?", generation.SessionID, generation.ProjectID, generation.UserID).
			UpdateColumn("updated_at", gorm.Expr("CASE WHEN updated_at >= ? THEN updated_at + 1 ELSE ? END", now, now))
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return ErrNotFound
		}
		var session model.MujianAgentSession
		if err := tx.Where("id = ? AND project_id = ? AND user_id = ?", generation.SessionID, generation.ProjectID, generation.UserID).
			First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if session.Title == defaultAgentSessionTitle {
			var messageCount, imageCount int64
			if err := tx.Model(&model.MujianAgentMessage{}).
				Where("session_id = ? AND project_id = ? AND user_id = ?", generation.SessionID, generation.ProjectID, generation.UserID).
				Count(&messageCount).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.MujianImageGeneration{}).
				Where("session_id = ? AND project_id = ? AND user_id = ?", generation.SessionID, generation.ProjectID, generation.UserID).
				Count(&imageCount).Error; err != nil {
				return err
			}
			if messageCount == 0 && imageCount == 0 {
				if err := tx.Model(&session).UpdateColumn("title", agentSessionTitle(generation.Prompt)).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Create(generation).Error; err != nil {
			return err
		}
		if len(references) > 0 {
			if err := tx.Create(&references).Error; err != nil {
				return err
			}
			for _, reference := range references {
				if reference.ObjectKey == "" {
					continue
				}
				if err := model.ActivateMujianObject(tx, reference.ObjectKey, reference.ObjectETag); err != nil {
					return err
				}
			}
		}
		return tx.Create(task).Error
	})
}

func startProjectImageTask(userID int, projectID, generationID, taskID string) {
	gopool.Go(func() {
		runProjectImageTask(userID, projectID, generationID, taskID)
	})
}

func beginProjectImageTask(userID int, projectID, generationID, taskID string) (*model.Task, *model.MujianImageGeneration, []model.MujianImageReference, bool, error) {
	var task model.Task
	var generation model.MujianImageGeneration
	var references []model.MujianImageReference
	started := false
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := claimProjectWrite(tx, userID, projectID); err != nil {
			return err
		}
		err := tx.Where(
			"user_id = ? AND task_id = ? AND platform = ? AND action = ?",
			userID, taskID, constant.TaskPlatformMujianImage, imageGenerationAction,
		).First(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && task.Status != model.TaskStatusSubmitted) {
			return nil
		}
		if err != nil {
			return err
		}
		err = tx.Where(
			"id = ? AND project_id = ? AND user_id = ? AND task_id = ? AND status = ?",
			generationID, projectID, userID, taskID, imageGenerationStatusQueued,
		).First(&generation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Model(&model.Task{}).
				Where("id = ? AND status = ?", task.ID, model.TaskStatusSubmitted).
				Updates(map[string]interface{}{
					"status": model.TaskStatusFailure, "progress": "100%", "finish_time": time.Now().Unix(),
					"fail_reason": "生图记录不存在或已删除",
				}).Error
		}
		if err != nil {
			return err
		}
		if err := tx.Where("generation_id = ?", generationID).Order("position ASC").Find(&references).Error; err != nil {
			return err
		}
		startTime := time.Now().Unix()
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", task.ID, model.TaskStatusSubmitted).
			Updates(map[string]interface{}{
				"status": model.TaskStatusInProgress, "progress": "10%", "start_time": startTime,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return nil
		}
		generationResult := tx.Model(&model.MujianImageGeneration{}).
			Where("id = ? AND task_id = ? AND status = ?", generationID, taskID, imageGenerationStatusQueued).
			Updates(map[string]interface{}{"status": imageGenerationStatusRunning, "error": ""})
		if generationResult.Error != nil {
			return generationResult.Error
		}
		if generationResult.RowsAffected != 1 {
			return ErrConflict
		}
		task.Status = model.TaskStatusInProgress
		task.Progress = "10%"
		task.StartTime = startTime
		generation.Status = imageGenerationStatusRunning
		started = true
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		if failErr := failOrphanedSubmittedImageTask(userID, taskID); failErr != nil {
			return nil, nil, nil, false, failErr
		}
		return nil, nil, nil, false, nil
	}
	return &task, &generation, references, started, err
}

func failOrphanedSubmittedImageTask(userID int, taskID string) error {
	return model.DB.Model(&model.Task{}).
		Where(
			"user_id = ? AND task_id = ? AND platform = ? AND action = ? AND status = ?",
			userID, taskID, constant.TaskPlatformMujianImage, imageGenerationAction, model.TaskStatusSubmitted,
		).
		Updates(map[string]interface{}{
			"status": model.TaskStatusFailure, "progress": "100%", "finish_time": time.Now().Unix(),
			"fail_reason": "生图记录不存在或已删除",
		}).Error
}

func runProjectImageTask(userID int, projectID, generationID, taskID string) {
	task, generation, references, started, err := beginProjectImageTask(userID, projectID, generationID, taskID)
	if err != nil || !started {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), model.MujianImageGenerationExecutionTimeout)
	defer cancel()
	referenceCtx, cancelReferences := context.WithTimeout(ctx, projectImageRequestTimeout)
	for index := range references {
		data, loadErr := loadImageReferenceData(referenceCtx, references[index])
		if loadErr != nil {
			cancelReferences()
			recordProjectImageFailure(task, generationID, loadErr.Error(), "")
			return
		}
		references[index].Data = data
	}
	cancelReferences()
	dispatched, err := dispatchProjectImage(projectImageDispatchInput{
		Context: ctx, UserID: userID, Engine: generation.Engine, ModelID: generation.ModelID, Prompt: generation.Prompt,
		AspectRatio: generation.AspectRatio, References: references,
	})
	if err != nil {
		recordProjectImageFailure(task, generationID, err.Error(), dispatched.RelayRequestID)
		return
	}
	if dispatched.ResultURL == "" {
		recordProjectImageFailure(task, generationID, "图像模型未返回结果", dispatched.RelayRequestID)
		return
	}
	if err = finishProjectImageSuccess(ctx, task, generationID, dispatched); err != nil {
		recordProjectImageFailure(task, generationID, err.Error(), dispatched.RelayRequestID)
	}
}

func finishProjectImageSuccess(ctx context.Context, task *model.Task, generationID string, dispatched projectImageDispatchResult) error {
	materializeCtx, cancelMaterialize := context.WithTimeout(ctx, projectImageRequestTimeout)
	content, err := materializeImageGenerationResultWithContext(materializeCtx, dispatched.ResultURL)
	cancelMaterialize()
	if err != nil {
		return err
	}
	store, objectStorageEnabled, err := imageObjectStoreFromEnvironment()
	if err != nil {
		return err
	}
	updates := map[string]interface{}{
		"status": imageGenerationStatusSucceeded, "result_mime_type": content.MIMEType,
		"result_url": "", "error": "", "relay_request_id": dispatched.RelayRequestID,
	}
	var uploadedObject *mujianobject.Object
	if objectStorageEnabled {
		objectKey, buildErr := store.BuildKey(mujianobject.ObjectKindGeneration, generationID, content.MIMEType)
		if buildErr != nil {
			return buildErr
		}
		uploadCtx, cancelUpload := context.WithTimeout(ctx, projectImageRequestTimeout)
		object, uploadErr := store.Upload(uploadCtx, mujianobject.UploadInput{
			ObjectKey:   objectKey,
			ContentType: content.MIMEType,
			Body:        bytes.NewReader(content.Data),
			SizeBytes:   int64(len(content.Data)),
		})
		cancelUpload()
		if uploadErr != nil {
			return uploadErr
		}
		uploadedObject = &object
		updates["result_data"] = nil
		updates["result_object_key"] = object.Key
		updates["result_size_bytes"] = object.SizeBytes
		updates["result_sha256"] = object.SHA256
		updates["result_etag"] = object.ETag
	} else {
		updates["result_data"] = content.Data
		updates["result_object_key"] = ""
		updates["result_size_bytes"] = 0
		updates["result_sha256"] = ""
		updates["result_etag"] = ""
	}
	task.SetData(map[string]string{
		"generation_id": generationID, "relay_request_id": dispatched.RelayRequestID,
	})
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		taskResult := tx.Model(&model.Task{}).Where("id = ? AND status = ?", task.ID, model.TaskStatusInProgress).
			Updates(map[string]interface{}{
				"status": model.TaskStatusSuccess, "progress": "100%", "finish_time": time.Now().Unix(),
				"private_data": task.PrivateData, "data": task.Data,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return ErrConflict
		}
		generationResult := tx.Model(&model.MujianImageGeneration{}).
			Where("id = ? AND task_id = ? AND status = ?", generationID, task.TaskID, imageGenerationStatusRunning).
			Updates(updates)
		if generationResult.Error != nil {
			return generationResult.Error
		}
		if generationResult.RowsAffected != 1 {
			return ErrConflict
		}
		if uploadedObject != nil {
			if err := model.ActivateMujianObject(tx, uploadedObject.Key, uploadedObject.ETag); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && uploadedObject != nil {
		err = errors.Join(err, cleanupImageObjects(store, []string{uploadedObject.Key}))
	}
	return err
}

func recordProjectImageFailure(task *model.Task, generationID, reason, requestID string) {
	if err := finishProjectImageFailure(task, generationID, reason, requestID); err != nil {
		common.SysError(fmt.Sprintf("finish image task failure (task=%d generation=%s): %v", task.ID, generationID, err))
	}
}

func finishProjectImageFailure(task *model.Task, generationID, reason, requestID string) error {
	task.SetData(map[string]string{"generation_id": generationID, "relay_request_id": requestID})
	return model.DB.Transaction(func(tx *gorm.DB) error {
		taskResult := tx.Model(&model.Task{}).Where("id = ? AND status = ?", task.ID, model.TaskStatusInProgress).
			Updates(map[string]interface{}{
				"status": model.TaskStatusFailure, "progress": "100%", "finish_time": time.Now().Unix(), "fail_reason": reason,
				"data": task.Data,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return ErrConflict
		}
		generationResult := tx.Model(&model.MujianImageGeneration{}).
			Where("id = ? AND task_id = ? AND status IN ?", generationID, task.TaskID, []string{imageGenerationStatusQueued, imageGenerationStatusRunning}).
			Updates(map[string]interface{}{"status": imageGenerationStatusFailed, "error": reason, "relay_request_id": requestID})
		if generationResult.Error != nil {
			return generationResult.Error
		}
		if generationResult.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
}

func failStaleProjectImageTask(task *model.Task, generationID string) error {
	const reason = "生图任务在服务重启后中断，请重新生成"
	return model.DB.Transaction(func(tx *gorm.DB) error {
		taskResult := tx.Model(&model.Task{}).Where("id = ? AND status = ?", task.ID, model.TaskStatusInProgress).
			Updates(map[string]interface{}{
				"status": model.TaskStatusFailure, "progress": "100%", "finish_time": time.Now().Unix(), "fail_reason": reason,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return nil
		}
		return tx.Model(&model.MujianImageGeneration{}).
			Where("id = ? AND task_id = ? AND status IN ?", generationID, task.TaskID, []string{imageGenerationStatusQueued, imageGenerationStatusRunning}).
			Updates(map[string]interface{}{"status": imageGenerationStatusFailed, "error": reason}).Error
	})
}

func relayProjectImage(input projectImageDispatchInput) (projectImageDispatchResult, error) {
	if input.Context == nil {
		return projectImageDispatchResult{}, errors.New("image dispatch context is required")
	}
	var body []byte
	var requestID string
	var err error
	if len(input.References) == 0 {
		payload := map[string]interface{}{
			"model": input.ModelID, "prompt": input.Prompt, "n": 1, "size": imageSize(input.AspectRatio),
		}
		jsonBody, marshalErr := common.Marshal(payload)
		if marshalErr != nil {
			return projectImageDispatchResult{}, marshalErr
		}
		body, requestID, err = relayRawRequest(input.Context, input.UserID, "/v1/images/generations", "application/json", bytes.NewReader(jsonBody))
	} else {
		body, requestID, err = relayImageEditRequest(input)
	}
	if err != nil {
		return projectImageDispatchResult{RelayRequestID: requestID}, err
	}
	resultURL, err := imageResultURL(body)
	if err != nil {
		return projectImageDispatchResult{}, err
	}
	return projectImageDispatchResult{ResultURL: resultURL, RelayRequestID: requestID}, nil
}

func relayImageEditRequest(input projectImageDispatchInput) ([]byte, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"model": input.ModelID, "prompt": input.Prompt, "n": "1", "size": imageSize(input.AspectRatio),
	}
	for _, name := range []string{"model", "prompt", "n", "size"} {
		if err := writer.WriteField(name, fields[name]); err != nil {
			return nil, "", err
		}
	}
	for _, reference := range input.References {
		disposition := mime.FormatMediaType("form-data", map[string]string{"name": "image[]", "filename": reference.Name})
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", disposition)
		header.Set("Content-Type", reference.MIMEType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", err
		}
		if _, err = part.Write(reference.Data); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return relayRawRequest(input.Context, input.UserID, "/v1/images/edits", writer.FormDataContentType(), &body)
}

func relayRawRequest(ctx context.Context, userID int, requestPath, contentType string, body io.Reader) ([]byte, string, error) {
	key, err := internalToken(userID)
	if err != nil {
		return nil, "", ErrRelayUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, relayURL(requestPath), body)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer sk-"+key)
	req.Header.Set("Content-Type", contentType)
	client := &http.Client{Timeout: projectImageRequestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrRelayUnavailable, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", err
	}
	requestID := resp.Header.Get(common.RequestIdKey)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := relayResponseError(responseBody)
		if message != "" {
			return nil, requestID, fmt.Errorf("%w: 上游返回 %d: %s", ErrRelayUnavailable, resp.StatusCode, message)
		}
		return nil, requestID, fmt.Errorf("%w: 上游返回 %d", ErrRelayUnavailable, resp.StatusCode)
	}
	return responseBody, requestID, nil
}

func relayResponseError(responseBody []byte) string {
	var response struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if common.Unmarshal(responseBody, &response) != nil {
		return ""
	}
	message := strings.TrimSpace(response.Error.Message)
	if message == "" {
		message = strings.TrimSpace(response.Message)
	}
	return truncateRunes(stripControlCharacters(message), 500)
}

func stripControlCharacters(value string) string {
	return strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
}

func truncateRunes(value string, maxLength int) string {
	runes := []rune(value)
	if len(runes) <= maxLength {
		return value
	}
	return string(runes[:maxLength])
}

func imageResultURL(responseBody []byte) (string, error) {
	var response struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := common.Unmarshal(responseBody, &response); err != nil || len(response.Data) == 0 {
		return "", errors.New("图像模型返回结构无效")
	}
	image := response.Data[0]
	if image.URL != "" {
		return image.URL, nil
	}
	if image.B64JSON != "" {
		return "data:image/png;base64," + image.B64JSON, nil
	}
	return "", errors.New("图像模型未返回结果")
}
