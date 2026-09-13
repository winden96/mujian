package openai

import (
	"errors"

	"github.com/QuantumNous/new-api/dto"
)

// Image usage totals already include all generated images. Keep the output
// image count separate so its rate replaces, rather than adds to, the text rate.
func normalizePricedImageUsage(usage *dto.Usage) error {
	if usage.InputTokens <= 0 || usage.OutputTokens <= 0 || usage.TotalTokens < usage.InputTokens ||
		usage.TotalTokens-usage.InputTokens != usage.OutputTokens ||
		usage.InputTokensDetails == nil {
		return errors.New("图像上游未返回完整的 Token 用量")
	}
	input, output := usage.InputTokensDetails, usage.OutputTokensDetails
	// Native Images usage permits omitting output_tokens_details. In that
	// format output_tokens counts image tokens, rather than chat text tokens.
	if output == nil {
		output = &dto.OutputTokenDetails{ImageTokens: usage.OutputTokens}
	}
	if input.CachedTokens < 0 || input.CachedTokens > usage.InputTokens ||
		input.ImageTokens < 0 || input.ImageTokens > usage.InputTokens ||
		input.TextTokens < 0 || input.TextTokens > usage.InputTokens-input.ImageTokens ||
		output.ImageTokens <= 0 || output.ImageTokens > usage.OutputTokens ||
		output.TextTokens < 0 || output.TextTokens > usage.OutputTokens-output.ImageTokens ||
		input.AudioTokens != 0 || output.AudioTokens != 0 || input.CachedCreationTokens != 0 {
		return errors.New("图像上游返回的 Token 明细无效")
	}
	usage.PromptTokens = usage.InputTokens
	usage.CompletionTokens = usage.OutputTokens
	usage.PromptTokensDetails = *input
	usage.CompletionTokenDetails = *output
	return nil
}
