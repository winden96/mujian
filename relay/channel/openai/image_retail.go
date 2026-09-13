package openai

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
)

// Count deliverable images before writing a response or settling the reservation.
// Preserve upstream extensions and usage, including usage omitted by the provider.
func prepareRetailImageResponse(body []byte, info *relaycommon.RelayInfo, usage *dto.Usage) ([]byte, error) {
	var response map[string]json.RawMessage
	if err := common.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	var images []json.RawMessage
	if err := common.Unmarshal(response["data"], &images); err != nil {
		return nil, errors.New("图像上游未返回图片列表")
	}
	valid := make([]json.RawMessage, 0, len(images))
	for _, raw := range images {
		var item struct {
			URL    string `json:"url"`
			Base64 string `json:"b64_json"`
		}
		if err := common.Unmarshal(raw, &item); err != nil {
			continue
		}
		deliverable := false
		if item.Base64 != "" {
			_, _, _, err := service.DecodeBase64ImageData(item.Base64)
			deliverable = err == nil
		} else if item.URL != "" {
			u, err := url.Parse(strings.TrimSpace(item.URL))
			deliverable = err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http")
		}
		if deliverable {
			valid = append(valid, raw)
		}
	}
	if len(valid) == 0 {
		return nil, errors.New("图像上游未返回有效图片，本次不收费")
	}
	if len(valid) > info.ImageRetail.RequestedCount {
		return nil, errors.New("图像上游返回数量超过请求数量")
	}
	if len(valid) != len(images) {
		data, err := common.Marshal(valid)
		if err != nil {
			return nil, err
		}
		response["data"] = data
		body, err = common.Marshal(response)
		if err != nil {
			return nil, err
		}
	}
	info.ImageRetail.ReturnedCount = len(valid)
	info.ImageRetailUsage = response["usage"]
	// Usage is audit information, not a prerequisite for per-image billing.
	if usage.InputTokens > 0 {
		usage.PromptTokens = usage.InputTokens
	}
	if usage.OutputTokens > 0 {
		usage.CompletionTokens = usage.OutputTokens
	}
	if usage.InputTokensDetails != nil {
		usage.PromptTokensDetails = *usage.InputTokensDetails
	}
	if usage.OutputTokensDetails != nil {
		usage.CompletionTokenDetails = *usage.OutputTokensDetails
	}
	return body, nil
}
