package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	corecommon "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/types"
)

const (
	claudeInboundConversionExpansion = int64(4)
	claudeRequestFramingAllowance    = int64(256)
	// Anthropic adds a hidden system prompt when tools are enabled. Reserve a
	// conservative fixed allowance in addition to the serialized tool schema.
	claudeToolSystemPromptAllowance = int64(1024)
	ClaudeEvidenceBytesPerToken     = int64(128)
)

func ClaudeEvidenceTokenFloor(evidenceBytes int64) (int64, error) {
	if evidenceBytes < 0 {
		return 0, errors.New("Claude evidence byte count is invalid")
	}
	tokens := evidenceBytes / ClaudeEvidenceBytesPerToken
	if evidenceBytes%ClaudeEvidenceBytesPerToken != 0 {
		tokens++
	}
	return tokens, nil
}

// ClaudeInboundPromptTokenUpperBound reserves enough input liability for a
// later OpenAI-to-Claude conversion. The final serialized Claude body is
// checked separately before any upstream request, so the expansion factor is
// an admission limit rather than an assumption that can under-authorize cost.
func ClaudeInboundPromptTokenUpperBound(bodyBytes int64, hasTools bool) (int, error) {
	if bodyBytes < 0 || bodyBytes > (math.MaxInt64/claudeInboundConversionExpansion) {
		return 0, errors.New("Claude 请求体大小无法建立价格边界")
	}
	bound := bodyBytes * claudeInboundConversionExpansion
	if hasTools {
		if bound > math.MaxInt64-claudeToolSystemPromptAllowance {
			return 0, errors.New("Claude 工具请求无法建立价格边界")
		}
		bound += claudeToolSystemPromptAllowance
	}
	if bound > int64(maxInt()) {
		return 0, errors.New("Claude 请求体价格边界超过平台整数范围")
	}
	return int(bound), nil
}

func claudeFinalPromptTokenUpperBound(bodyBytes int, hasTools bool) (int, error) {
	allowance := claudeRequestFramingAllowance
	if hasTools {
		allowance += claudeToolSystemPromptAllowance
	}
	if bodyBytes < 0 || int64(bodyBytes) > math.MaxInt64-allowance {
		return 0, errors.New("Claude 最终请求体大小无法建立价格边界")
	}
	bound := int64(bodyBytes) + allowance
	if bound > int64(maxInt()) {
		return 0, errors.New("Claude 最终请求体价格边界超过平台整数范围")
	}
	return int(bound), nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

type claudeRequestLimits struct {
	Model             string          `json:"model"`
	Prompt            string          `json:"prompt"`
	MaxTokens         *uint64         `json:"max_tokens"`
	MaxTokensToSample *uint64         `json:"max_tokens_to_sample"`
	System            json.RawMessage `json:"system"`
	Messages          []struct {
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools        json.RawMessage `json:"tools"`
	InferenceGeo string          `json:"inference_geo"`
	Speed        json.RawMessage `json:"speed"`
	ServiceTier  string          `json:"service_tier"`
}

const unboundedClaudeMediaError = "托管 Claude 渠道暂不支持远程 URL、file_id 或内联二进制媒体；请改用文本内容或内联文本 document"

// ClaudeRequestUsesUnboundedMedia reports media that strict Claude pricing
// cannot attest safely. This includes remote references and inline binary
// image/document data: remote bytes are unknown before fetch, while upstream
// input_tokens cannot prove the billable size of binary payloads.
func ClaudeRequestUsesUnboundedMedia(request dto.Request) (bool, error) {
	switch typed := request.(type) {
	case *dto.ClaudeRequest:
		if typed == nil {
			return false, nil
		}
		usesUnbounded, err := claudeContentUsesUnboundedMediaValue(typed.System)
		if err != nil || usesUnbounded {
			return usesUnbounded, err
		}
		for _, message := range typed.Messages {
			usesUnbounded, err = claudeContentUsesUnboundedMediaValue(message.Content)
			if err != nil || usesUnbounded {
				return usesUnbounded, err
			}
		}
	case *dto.GeneralOpenAIRequest:
		if typed == nil {
			return false, nil
		}
		for _, message := range typed.Messages {
			usesUnbounded, err := openAIContentUsesUnboundedMediaValue(message.Content)
			if err != nil || usesUnbounded {
				return usesUnbounded, err
			}
		}
	}
	return false, nil
}

func UnboundedClaudeMediaError() error {
	return errors.New(unboundedClaudeMediaError)
}

// ValidateClaudeRequestPriceAuthorization verifies the final serialized body,
// after channel mapping and overrides, against the bound used for balance
// authorization. This prevents a late channel/affinity override or global
// pass-through setting from increasing the upstream completion ceiling.
func ValidateClaudeRequestPriceAuthorization(data []byte, info *RelayInfo) error {
	if info == nil || !info.RequiresClaudeUsageAuthorization() {
		return nil
	}
	if info.AuthorizedPromptTokens <= 0 || info.AuthorizedCompletionTokens <= 0 {
		return errors.New("Claude 请求缺少有效的价格预授权边界")
	}
	info.ClaudeInputEvidenceBytes = 0
	var limits claudeRequestLimits
	if err := corecommon.Unmarshal(data, &limits); err != nil {
		return fmt.Errorf("Claude 最终请求无法校验价格预授权: %w", err)
	}
	hasTools, err := claudeToolsPresent(limits.Tools)
	if err != nil {
		return err
	}
	promptBound, err := claudeFinalPromptTokenUpperBound(len(data), hasTools)
	if err != nil {
		return err
	}
	if promptBound > info.AuthorizedPromptTokens {
		return errors.New("Claude 最终请求体超过已预授权的输入边界")
	}
	if strings.TrimSpace(limits.Model) == "" || limits.Model != info.UpstreamModelName {
		return errors.New("Claude 最终请求的 model 与已授权渠道映射不一致")
	}
	maxTokens := uint64(0)
	if limits.MaxTokens != nil {
		maxTokens = *limits.MaxTokens
	}
	if limits.MaxTokensToSample != nil && *limits.MaxTokensToSample > maxTokens {
		maxTokens = *limits.MaxTokensToSample
	}
	if maxTokens == 0 {
		return errors.New("Claude 最终请求缺少 max_tokens 价格边界")
	}
	if maxTokens > uint64(info.AuthorizedCompletionTokens) {
		return errors.New("Claude 最终请求的 max_tokens 超过已预授权边界")
	}
	if geo := strings.ToLower(strings.TrimSpace(limits.InferenceGeo)); geo != "" && geo != "global" {
		return errors.New("托管 Claude 渠道暂不支持非 global inference_geo 的可验证计价")
	}
	if rawJSONHasValue(limits.Speed) {
		return errors.New("托管 Claude 渠道暂不支持 speed 的可验证计价")
	}
	if strings.TrimSpace(limits.ServiceTier) != "" {
		return errors.New("托管 Claude 渠道暂不支持 service_tier 的可验证计价")
	}
	usesWebSearch, err := claudeToolsUseWebSearch(limits.Tools)
	if err != nil {
		return err
	}
	strictPriceRules := (info.ChannelMeta != nil && info.ChannelMeta.ManagedProvider) ||
		isStrictClaudePriceProvider(info.PriceData.PriceProvider)
	if usesWebSearch && strictPriceRules {
		return errors.New("托管 Claude 渠道暂不支持未接入按次价格的 web_search")
	}
	usesUnboundedMedia, err := claudeSerializedRequestUsesUnboundedMedia(limits)
	if err != nil {
		return err
	}
	if usesUnboundedMedia && strictPriceRules {
		return UnboundedClaudeMediaError()
	}
	if info.ChannelMeta != nil && info.ChannelMeta.ManagedProvider {
		evidenceBytes, err := claudeInputEvidenceBytes(limits)
		if err != nil {
			return err
		}
		info.ClaudeInputEvidenceBytes = evidenceBytes
	}
	return nil
}

type claudeEvidenceByteCounter struct {
	total int64
}

func (counter *claudeEvidenceByteCounter) addString(value string) error {
	if int64(len(value)) > math.MaxInt64-counter.total {
		return errors.New("Claude input evidence exceeds the supported range")
	}
	counter.total += int64(len(value))
	return nil
}

func (counter *claudeEvidenceByteCounter) addJSON(value any) error {
	data, err := corecommon.Marshal(value)
	if err != nil {
		return errors.New("Claude input evidence cannot be serialized")
	}
	return counter.addString(string(data))
}

func claudeInputEvidenceBytes(request claudeRequestLimits) (int64, error) {
	counter := &claudeEvidenceByteCounter{}
	if err := counter.addString(request.Prompt); err != nil {
		return 0, err
	}
	if err := addClaudeInputContentJSON(counter, request.System, false); err != nil {
		return 0, err
	}
	for _, message := range request.Messages {
		if err := addClaudeInputContentJSON(counter, message.Content, false); err != nil {
			return 0, err
		}
	}
	if rawJSONHasValue(request.Tools) {
		if err := counter.addString(string(request.Tools)); err != nil {
			return 0, err
		}
	}
	return counter.total, nil
}

func addClaudeInputContentJSON(counter *claudeEvidenceByteCounter, data []byte, toolResult bool) error {
	if len(data) == 0 || strings.TrimSpace(string(data)) == "null" {
		return nil
	}
	var value any
	if err := corecommon.Unmarshal(data, &value); err != nil {
		return errors.New("Claude input content cannot be measured")
	}
	return addClaudeInputContentValue(counter, value, toolResult)
}

func addClaudeInputContentValue(counter *claudeEvidenceByteCounter, value any, toolResult bool) error {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return counter.addString(typed)
	case []any:
		for _, item := range typed {
			if err := addClaudeInputContentValue(counter, item, toolResult); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		blockType := strings.ToLower(strings.TrimSpace(stringValue(typed["type"])))
		switch blockType {
		case "text":
			return counter.addString(stringValue(typed["text"]))
		case "thinking":
			return counter.addString(stringValue(typed["thinking"]))
		case "redacted_thinking":
			return counter.addString(stringValue(typed["data"]))
		case "tool_use":
			if err := counter.addString(stringValue(typed["name"])); err != nil {
				return err
			}
			if input, exists := typed["input"]; exists {
				return counter.addJSON(input)
			}
			return nil
		case "tool_result":
			return addClaudeInputContentValue(counter, typed["content"], true)
		case "image":
			return nil
		case "document":
			return addClaudeDocumentInputEvidence(counter, typed["source"])
		default:
			if toolResult {
				return counter.addJSON(typed)
			}
			return nil
		}
	default:
		if toolResult {
			return counter.addJSON(typed)
		}
		return nil
	}
}

func addClaudeDocumentInputEvidence(counter *claudeEvidenceByteCounter, value any) error {
	source, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(source["type"]))) {
	case "text":
		return counter.addString(stringValue(source["data"]))
	case "content":
		return addClaudeInputContentValue(counter, source["content"], false)
	default:
		// Base64 image/document payloads are authorized by request-body size and
		// intentionally excluded from semantic input-token attestation.
		return nil
	}
}

func claudeSerializedRequestUsesUnboundedMedia(request claudeRequestLimits) (bool, error) {
	usesUnbounded, err := claudeContentUsesUnboundedMediaJSON(request.System)
	if err != nil || usesUnbounded {
		return usesUnbounded, err
	}
	for _, message := range request.Messages {
		usesUnbounded, err = claudeContentUsesUnboundedMediaJSON(message.Content)
		if err != nil || usesUnbounded {
			return usesUnbounded, err
		}
	}
	return false, nil
}

func claudeContentUsesUnboundedMediaJSON(data []byte) (bool, error) {
	if len(data) == 0 || strings.TrimSpace(string(data)) == "null" {
		return false, nil
	}
	var value any
	if err := corecommon.Unmarshal(data, &value); err != nil {
		return false, errors.New("Claude 媒体内容无法校验价格预授权")
	}
	return claudeContentUsesUnboundedMedia(value), nil
}

func claudeContentUsesUnboundedMediaValue(value any) (bool, error) {
	if value == nil {
		return false, nil
	}
	data, err := corecommon.Marshal(value)
	if err != nil {
		return false, errors.New("Claude 媒体内容无法校验价格预授权")
	}
	return claudeContentUsesUnboundedMediaJSON(data)
}

func claudeContentUsesUnboundedMedia(value any) bool {
	switch typed := value.(type) {
	case string, nil:
		return false
	case []any:
		for _, item := range typed {
			if claudeContentUsesUnboundedMedia(item) {
				return true
			}
		}
	case map[string]any:
		blockType := strings.ToLower(strings.TrimSpace(stringValue(typed["type"])))
		switch blockType {
		case "image", "document":
			return claudeSourceUsesUnboundedMedia(blockType, typed["source"])
		case "tool_result":
			return claudeContentUsesUnboundedMedia(typed["content"])
		}
	}
	return false
}

func claudeSourceUsesUnboundedMedia(blockType string, value any) bool {
	source, ok := value.(map[string]any)
	if !ok {
		return true
	}
	for _, key := range []string{"url", "file_id", "id"} {
		if hasJSONValue(source[key]) {
			return true
		}
	}
	sourceType := strings.ToLower(strings.TrimSpace(stringValue(source["type"])))
	switch blockType {
	case "image":
		return true
	case "document":
		switch sourceType {
		case "base64":
			return true
		case "text":
			// A text document is inline by schema. Its contents may legitimately
			// mention a URL without causing the relay to fetch that URL.
			return !hasNonEmptyString(source["data"])
		case "content":
			return !isInlineClaudeDocumentContent(source["content"])
		default:
			return true
		}
	}
	return true
}

func isInlineClaudeDocumentContent(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		if len(typed) == 0 {
			return false
		}
		for _, item := range typed {
			if !isInlineClaudeDocumentContent(item) {
				return false
			}
		}
		return true
	case map[string]any:
		return strings.EqualFold(strings.TrimSpace(stringValue(typed["type"])), "text") &&
			hasNonEmptyString(typed["text"])
	default:
		return false
	}
}

func openAIContentUsesUnboundedMediaValue(value any) (bool, error) {
	if value == nil {
		return false, nil
	}
	data, err := corecommon.Marshal(value)
	if err != nil {
		return false, errors.New("OpenAI 兼容媒体内容无法校验价格预授权")
	}
	var normalized any
	if err = corecommon.Unmarshal(data, &normalized); err != nil {
		return false, errors.New("OpenAI 兼容媒体内容无法校验价格预授权")
	}
	return openAIContentUsesUnboundedMedia(normalized), nil
}

func openAIContentUsesUnboundedMedia(value any) bool {
	switch typed := value.(type) {
	case string, nil:
		return false
	case []any:
		for _, item := range typed {
			if openAIContentUsesUnboundedMedia(item) {
				return true
			}
		}
	case map[string]any:
		switch strings.ToLower(strings.TrimSpace(stringValue(typed["type"]))) {
		case dto.ContentTypeImageURL:
			return mediaReferenceIsUnbounded(typed["image_url"])
		case dto.ContentTypeVideoUrl:
			return mediaReferenceIsUnbounded(typed["video_url"])
		case dto.ContentTypeFile:
			file, ok := typed["file"].(map[string]any)
			if !ok || hasJSONValue(file["file_id"]) || !hasNonEmptyString(file["file_data"]) {
				return true
			}
			// The converter may turn inline file data into a Claude base64
			// document. Reject before reservation because its upstream token usage
			// cannot attest the binary payload size.
			return true
		case dto.ContentTypeInputAudio:
			audio, ok := typed["input_audio"].(map[string]any)
			if !ok || !hasNonEmptyString(audio["data"]) {
				return true
			}
			return true
		}
	}
	return false
}

func mediaReferenceIsUnbounded(value any) bool {
	if object, ok := value.(map[string]any); ok {
		value = object["url"]
	}
	reference, ok := value.(string)
	if !ok || strings.TrimSpace(reference) == "" {
		return true
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(reference)), "data:") {
		return true
	}
	return stringLooksLikeExternalReference(reference)
}

func stringLooksLikeExternalReference(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "data:") {
		return false
	}
	return strings.Contains(lower, "://") || strings.HasPrefix(lower, "file:")
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func hasNonEmptyString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func hasJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	default:
		return true
	}
}

func rawJSONHasValue(data json.RawMessage) bool {
	value := strings.TrimSpace(string(data))
	return value != "" && value != "null"
}

func claudeToolsPresent(data []byte) (bool, error) {
	if len(data) == 0 || strings.TrimSpace(string(data)) == "null" {
		return false, nil
	}
	var tools []json.RawMessage
	if err := corecommon.Unmarshal(data, &tools); err != nil {
		return false, errors.New("Claude 最终请求的 tools 无法校验价格预授权")
	}
	return len(tools) > 0, nil
}

func isStrictClaudePriceProvider(provider string) bool {
	return provider == types.PriceProviderZenMux || provider == types.PriceProviderTabCode
}

func claudeToolsUseWebSearch(data []byte) (bool, error) {
	if len(data) == 0 || string(data) == "null" {
		return false, nil
	}
	var tools []struct {
		Type string `json:"type"`
	}
	if err := corecommon.Unmarshal(data, &tools); err != nil {
		return false, errors.New("Claude 最终请求的 tools 无法校验价格预授权")
	}
	for _, tool := range tools {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(tool.Type)), "web_search") {
			return true, nil
		}
	}
	return false, nil
}
