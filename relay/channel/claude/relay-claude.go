package claude

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relay/channel/openrouter"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/reasonmap"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	WebSearchMaxUsesLow    = 1
	WebSearchMaxUsesMedium = 5
	WebSearchMaxUsesHigh   = 10
	maxClaudeResponseBytes = 16 << 20
)

func stopReasonClaude2OpenAI(reason string) string {
	return reasonmap.ClaudeStopReasonToOpenAIFinishReason(reason)
}

func maybeMarkClaudeRefusal(c *gin.Context, stopReason string) {
	if c == nil {
		return
	}
	if strings.EqualFold(stopReason, "refusal") {
		common.SetContextKey(c, constant.ContextKeyAdminRejectReason, "claude_stop_reason=refusal")
	}
}

// ValidateOpenAIRequestForClaude checks dynamic JSON fields that the OpenAI
// request DTO cannot express with static Go types. It is exported so the
// controller can reject malformed requests before reserving quota, while the
// converter repeats the check as a safety boundary for non-controller callers.
func ValidateOpenAIRequestForClaude(textRequest *dto.GeneralOpenAIRequest) error {
	if textRequest == nil {
		return errors.New("request is nil")
	}
	for index := range textRequest.Messages {
		message := &textRequest.Messages[index]
		switch message.Role {
		case "", "system", "user", "assistant", "tool":
		default:
			return fmt.Errorf("invalid message role at index %d", index)
		}
		if err := validateOpenAIMessageContentForClaude(message.Content); err != nil {
			return fmt.Errorf("invalid message content at index %d: %w", index, err)
		}
	}
	for index, tool := range textRequest.Tools {
		params, ok := tool.Function.Parameters.(map[string]any)
		if !ok {
			if tool.Function.Parameters == nil {
				continue
			}
			return fmt.Errorf("invalid tool schema at index %d: function.parameters must be an object", index)
		}
		if rawType := params["type"]; rawType != nil {
			schemaType, ok := rawType.(string)
			if !ok || strings.TrimSpace(schemaType) == "" {
				return fmt.Errorf("invalid tool schema at index %d: function.parameters.type must be a non-empty string", index)
			}
		}
	}

	if textRequest.Stop == nil {
		return nil
	}
	switch stops := textRequest.Stop.(type) {
	case string, []string:
		return nil
	case []interface{}:
		for index, stop := range stops {
			if _, ok := stop.(string); !ok {
				return fmt.Errorf("invalid stop sequence at index %d: value must be a string", index)
			}
		}
		return nil
	default:
		return errors.New("invalid stop: value must be a string or an array of strings")
	}
}

func validateOpenAIMessageContentForClaude(content any) error {
	switch typed := content.(type) {
	case nil, string:
		return nil
	case []dto.MediaContent:
		return nil
	case []any:
		for index, item := range typed {
			switch item.(type) {
			case dto.MediaContent, *dto.MediaContent, map[string]any:
			default:
				return fmt.Errorf("array item %d must be an object", index)
			}
		}
		return nil
	default:
		return errors.New("value must be a string, null, or an array of content objects")
	}
}

func RequestOpenAI2ClaudeMessage(c *gin.Context, textRequest dto.GeneralOpenAIRequest) (*dto.ClaudeRequest, error) {
	if err := ValidateOpenAIRequestForClaude(&textRequest); err != nil {
		return nil, err
	}
	claudeTools := make([]any, 0, len(textRequest.Tools))

	for _, tool := range textRequest.Tools {
		params, ok := tool.Function.Parameters.(map[string]any)
		if !ok {
			continue
		}
		claudeTool := dto.Tool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Strict:      tool.Function.Strict,
		}
		claudeTool.InputSchema = make(map[string]interface{})
		if rawType := params["type"]; rawType != nil {
			schemaType, _ := rawType.(string)
			claudeTool.InputSchema["type"] = schemaType
		}
		claudeTool.InputSchema["properties"] = params["properties"]
		claudeTool.InputSchema["required"] = params["required"]
		for s, a := range params {
			if s == "type" || s == "properties" || s == "required" {
				continue
			}
			claudeTool.InputSchema[s] = a
		}
		claudeTools = append(claudeTools, &claudeTool)
	}

	// Web search tool
	// https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/web-search-tool
	if textRequest.WebSearchOptions != nil {
		webSearchTool := dto.ClaudeWebSearchTool{
			Type: "web_search_20250305",
			Name: "web_search",
		}

		// 处理 user_location
		if textRequest.WebSearchOptions.UserLocation != nil {
			anthropicUserLocation := &dto.ClaudeWebSearchUserLocation{
				Type: "approximate", // 固定为 "approximate"
			}

			// 解析 UserLocation JSON
			var userLocationMap map[string]interface{}
			if err := common.Unmarshal(textRequest.WebSearchOptions.UserLocation, &userLocationMap); err == nil {
				// 检查是否有 approximate 字段
				if approximateData, ok := userLocationMap["approximate"].(map[string]interface{}); ok {
					if timezone, ok := approximateData["timezone"].(string); ok && timezone != "" {
						anthropicUserLocation.Timezone = timezone
					}
					if country, ok := approximateData["country"].(string); ok && country != "" {
						anthropicUserLocation.Country = country
					}
					if region, ok := approximateData["region"].(string); ok && region != "" {
						anthropicUserLocation.Region = region
					}
					if city, ok := approximateData["city"].(string); ok && city != "" {
						anthropicUserLocation.City = city
					}
				}
			}

			webSearchTool.UserLocation = anthropicUserLocation
		}

		// 处理 search_context_size 转换为 max_uses
		if textRequest.WebSearchOptions.SearchContextSize != "" {
			switch textRequest.WebSearchOptions.SearchContextSize {
			case "low":
				webSearchTool.MaxUses = WebSearchMaxUsesLow
			case "medium":
				webSearchTool.MaxUses = WebSearchMaxUsesMedium
			case "high":
				webSearchTool.MaxUses = WebSearchMaxUsesHigh
			}
		}

		claudeTools = append(claudeTools, &webSearchTool)
	}

	claudeRequest := dto.ClaudeRequest{
		Model:         textRequest.Model,
		StopSequences: nil,
		Temperature:   textRequest.Temperature,
		Tools:         claudeTools,
	}
	if maxTokens := textRequest.GetMaxTokens(); maxTokens > 0 {
		claudeRequest.MaxTokens = common.GetPointer(maxTokens)
	}
	if textRequest.TopP != nil {
		claudeRequest.TopP = common.GetPointer(*textRequest.TopP)
	}
	if textRequest.TopK != nil {
		claudeRequest.TopK = common.GetPointer(*textRequest.TopK)
	}
	if textRequest.IsStream(nil) {
		claudeRequest.Stream = common.GetPointer(true)
	}

	// 处理 tool_choice 和 parallel_tool_calls
	if textRequest.ToolChoice != nil || textRequest.ParallelTooCalls != nil {
		claudeToolChoice := mapToolChoice(textRequest.ToolChoice, textRequest.ParallelTooCalls)
		if claudeToolChoice != nil {
			claudeRequest.ToolChoice = claudeToolChoice
		}
	}

	if claudeRequest.MaxTokens == nil || *claudeRequest.MaxTokens == 0 {
		defaultMaxTokens := uint(model_setting.GetClaudeSettings().GetDefaultMaxTokens(textRequest.Model))
		claudeRequest.MaxTokens = &defaultMaxTokens
	}

	if baseModel, effortLevel, ok := reasoning.TrimEffortSuffix(textRequest.Model); ok && effortLevel != "" &&
		(strings.HasPrefix(textRequest.Model, "claude-opus-4-6") || strings.HasPrefix(textRequest.Model, "claude-opus-4-7")) {
		claudeRequest.Model = baseModel
		claudeRequest.Thinking = &dto.Thinking{
			Type: "adaptive",
		}
		claudeRequest.OutputConfig = json.RawMessage(fmt.Sprintf(`{"effort":"%s"}`, effortLevel))
		if strings.HasPrefix(baseModel, "claude-opus-4-7") {
			// Opus 4.7 rejects non-default temperature/top_p/top_k with 400
			// and defaults display to "omitted"; restore the 4.6 visible summary.
			claudeRequest.Thinking.Display = "summarized"
			claudeRequest.Temperature = nil
			claudeRequest.TopP = nil
			claudeRequest.TopK = nil
		} else {
			claudeRequest.TopP = nil
			claudeRequest.Temperature = common.GetPointer[float64](1.0)
		}
	} else if model_setting.GetClaudeSettings().ThinkingAdapterEnabled &&
		strings.HasSuffix(textRequest.Model, "-thinking") {

		trimmedModel := strings.TrimSuffix(textRequest.Model, "-thinking")
		if strings.HasPrefix(trimmedModel, "claude-opus-4-7") {
			// Opus 4.7 rejects thinking.type="enabled"; use adaptive at high effort.
			claudeRequest.Thinking = &dto.Thinking{Type: "adaptive", Display: "summarized"}
			claudeRequest.OutputConfig = json.RawMessage(`{"effort":"high"}`)
			claudeRequest.Temperature = nil
			claudeRequest.TopP = nil
			claudeRequest.TopK = nil
		} else {
			// 因为BudgetTokens 必须大于1024
			if claudeRequest.MaxTokens == nil || *claudeRequest.MaxTokens < 1280 {
				claudeRequest.MaxTokens = common.GetPointer[uint](1280)
			}

			// BudgetTokens 为 max_tokens 的 80%
			claudeRequest.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: common.GetPointer[int](int(float64(*claudeRequest.MaxTokens) * model_setting.GetClaudeSettings().ThinkingAdapterBudgetTokensPercentage)),
			}
			// TODO: 临时处理
			// https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking#important-considerations-when-using-extended-thinking
			claudeRequest.TopP = nil
			claudeRequest.Temperature = common.GetPointer[float64](1.0)
		}
		if !model_setting.ShouldPreserveThinkingSuffix(textRequest.Model) {
			claudeRequest.Model = trimmedModel
		}
	}

	if textRequest.ReasoningEffort != "" {
		switch textRequest.ReasoningEffort {
		case "low":
			claudeRequest.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: common.GetPointer[int](1280),
			}
		case "medium":
			claudeRequest.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: common.GetPointer[int](2048),
			}
		case "high":
			claudeRequest.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: common.GetPointer[int](4096),
			}
		}
	}

	// 指定了 reasoning 参数,覆盖 budgetTokens
	if textRequest.Reasoning != nil {
		var reasoning openrouter.RequestReasoning
		if err := common.Unmarshal(textRequest.Reasoning, &reasoning); err != nil {
			return nil, err
		}

		budgetTokens := reasoning.MaxTokens
		if budgetTokens > 0 {
			claudeRequest.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: &budgetTokens,
			}
		}
	}

	// Explicit Claude-native controls take precedence over legacy OpenAI-style
	// reasoning fields. Adaptive thinking does not accept sampling controls.
	if len(textRequest.THINKING) > 0 {
		var thinking *dto.Thinking
		if err := common.Unmarshal(textRequest.THINKING, &thinking); err != nil {
			return nil, fmt.Errorf("invalid Claude thinking config: %w", err)
		}
		claudeRequest.Thinking = thinking
	}
	if len(textRequest.OutputConfig) > 0 {
		claudeRequest.OutputConfig = append(json.RawMessage(nil), textRequest.OutputConfig...)
	}
	if claudeRequest.Thinking != nil && claudeRequest.Thinking.Type == "adaptive" {
		claudeRequest.Temperature = nil
		claudeRequest.TopP = nil
		claudeRequest.TopK = nil
	}

	if textRequest.Stop != nil {
		// stop may be a string or an array of strings.
		switch stops := textRequest.Stop.(type) {
		case string:
			claudeRequest.StopSequences = []string{stops}
		case []interface{}:
			stopSequences := make([]string, 0, len(stops))
			for index, stop := range stops {
				stopSequence, ok := stop.(string)
				if !ok {
					return nil, fmt.Errorf("invalid stop sequence at index %d: value must be a string", index)
				}
				stopSequences = append(stopSequences, stopSequence)
			}
			claudeRequest.StopSequences = stopSequences
		case []string:
			claudeRequest.StopSequences = append([]string(nil), stops...)
		default:
			return nil, errors.New("invalid stop: value must be a string or an array of strings")
		}
	}
	formatMessages := make([]dto.Message, 0)
	lastMessage := dto.Message{
		Role: "tool",
	}
	for _, message := range textRequest.Messages {
		if message.Role == "" {
			message.Role = "user"
		}
		fmtMessage := dto.Message{
			Role:    message.Role,
			Content: message.Content,
		}
		if message.Role == "tool" {
			fmtMessage.ToolCallId = message.ToolCallId
		}
		if message.Role == "assistant" && message.ToolCalls != nil {
			fmtMessage.ToolCalls = message.ToolCalls
		}
		if lastMessage.Role == message.Role && lastMessage.Role != "tool" {
			if lastMessage.IsStringContent() && message.IsStringContent() {
				fmtMessage.SetStringContent(strings.Trim(fmt.Sprintf("%s %s", lastMessage.StringContent(), message.StringContent()), "\""))
				// delete last message
				formatMessages = formatMessages[:len(formatMessages)-1]
			}
		}
		if fmtMessage.Content == nil || (fmtMessage.IsStringContent() && fmtMessage.StringContent() == "") {
			fmtMessage.SetStringContent("...")
		}
		formatMessages = append(formatMessages, fmtMessage)
		lastMessage = fmtMessage
	}

	claudeMessages := make([]dto.ClaudeMessage, 0)
	isFirstMessage := true
	// 初始化system消息数组，用于累积多个system消息
	var systemMessages []dto.ClaudeMediaMessage

	for _, message := range formatMessages {
		if message.Role == "system" {
			// 根据Claude API规范，system字段使用数组格式更有通用性
			if message.IsStringContent() {
				if text := message.StringContent(); text != "" {
					systemMessages = append(systemMessages, dto.ClaudeMediaMessage{
						Type: "text",
						Text: common.GetPointer[string](text),
					})
				}
			} else {
				// 支持复合内容的system消息（虽然不常见，但需要考虑完整性）
				for _, ctx := range message.ParseContent() {
					if ctx.Type == "text" && ctx.Text != "" {
						systemMessages = append(systemMessages, dto.ClaudeMediaMessage{
							Type: "text",
							Text: common.GetPointer[string](ctx.Text),
						})
					}
					// 未来可以在这里扩展对图片等其他类型的支持
				}
			}
		} else {
			if isFirstMessage {
				isFirstMessage = false
				if message.Role != "user" {
					// fix: first message is assistant, add user message
					claudeMessage := dto.ClaudeMessage{
						Role: "user",
						Content: []dto.ClaudeMediaMessage{
							{
								Type: "text",
								Text: common.GetPointer[string]("..."),
							},
						},
					}
					claudeMessages = append(claudeMessages, claudeMessage)
				}
			}
			claudeMessage := dto.ClaudeMessage{
				Role: message.Role,
			}
			if message.Role == "tool" {
				if len(claudeMessages) > 0 && claudeMessages[len(claudeMessages)-1].Role == "user" {
					lastMessage := claudeMessages[len(claudeMessages)-1]
					var lastContent []dto.ClaudeMediaMessage
					switch content := lastMessage.Content.(type) {
					case string:
						lastContent = []dto.ClaudeMediaMessage{
							{
								Type: "text",
								Text: common.GetPointer[string](content),
							},
						}
					case []dto.ClaudeMediaMessage:
						lastContent = content
					case nil:
					default:
						return nil, errors.New("invalid preceding user content for Claude tool result")
					}
					lastMessage.Content = append(lastContent, dto.ClaudeMediaMessage{
						Type:      "tool_result",
						ToolUseId: message.ToolCallId,
						Content:   message.Content,
					})
					claudeMessages[len(claudeMessages)-1] = lastMessage
					continue
				} else {
					claudeMessage.Role = "user"
					claudeMessage.Content = []dto.ClaudeMediaMessage{
						{
							Type:      "tool_result",
							ToolUseId: message.ToolCallId,
							Content:   message.Content,
						},
					}
				}
			} else if message.IsStringContent() && message.ToolCalls == nil {
				text := message.StringContent()
				if text == "" {
					text = "..."
				}
				claudeMessage.Content = text
			} else {
				claudeMediaMessages := make([]dto.ClaudeMediaMessage, 0)
				for _, mediaMessage := range message.ParseContent() {
					switch mediaMessage.Type {
					case "text":
						if mediaMessage.Text != "" {
							claudeMediaMessages = append(claudeMediaMessages, dto.ClaudeMediaMessage{
								Type: "text",
								Text: common.GetPointer[string](mediaMessage.Text),
							})
						}
					default:
						source := mediaMessage.ToFileSource()
						if source == nil {
							continue
						}
						if mediaMessage.Type == dto.ContentTypeFile {
							file := mediaMessage.GetFile()
							if file == nil {
								continue
							}
							extension := strings.TrimPrefix(filepath.Ext(file.FileName), ".")
							if extension != "" {
								mimeType := service.GetMimeTypeByExtension(extension)
								if mimeType == "application/octet-stream" {
									continue
								}
								source = types.NewFileSourceFromData(file.FileData, mimeType)
							}
						}
						base64Data, mimeType, err := service.GetBase64Data(c, source, "formatting file for Claude")
						if err != nil {
							return nil, fmt.Errorf("get file data failed: %s", err.Error())
						}
						switch {
						case strings.HasPrefix(mimeType, "text/"):
							textData, err := base64.StdEncoding.DecodeString(base64Data)
							if err != nil {
								return nil, fmt.Errorf("decode text file for Claude failed: %s", err.Error())
							}
							text := string(textData)
							claudeMediaMessages = append(claudeMediaMessages, dto.ClaudeMediaMessage{
								Type: "text",
								Text: &text,
							})
						case strings.HasPrefix(mimeType, "application/pdf"), strings.HasPrefix(mimeType, "image/"):
							mediaType := "image"
							if strings.HasPrefix(mimeType, "application/pdf") {
								mediaType = "document"
							}
							claudeMediaMessages = append(claudeMediaMessages, dto.ClaudeMediaMessage{
								Type: mediaType,
								Source: &dto.ClaudeMessageSource{
									Type:      "base64",
									MediaType: mimeType,
									Data:      base64Data,
								},
							})
						}
						continue
					}
				}

				if message.ToolCalls != nil {
					for _, toolCall := range message.ParseToolCalls() {
						inputObj := make(map[string]any)
						if err := common.Unmarshal([]byte(toolCall.Function.Arguments), &inputObj); err != nil {
							common.SysLog("tool call function arguments is not a map[string]any: " + fmt.Sprintf("%v", toolCall.Function.Arguments))
							continue
						}
						claudeMediaMessages = append(claudeMediaMessages, dto.ClaudeMediaMessage{
							Type:  "tool_use",
							Id:    toolCall.ID,
							Name:  toolCall.Function.Name,
							Input: inputObj,
						})
					}
				}
				claudeMessage.Content = claudeMediaMessages
			}
			claudeMessages = append(claudeMessages, claudeMessage)
		}
	}

	// 设置累积的system消息
	if len(systemMessages) > 0 {
		claudeRequest.System = systemMessages
	}

	claudeRequest.Prompt = ""
	claudeRequest.Messages = claudeMessages
	return &claudeRequest, nil
}

func StreamResponseClaude2OpenAI(claudeResponse *dto.ClaudeResponse) *dto.ChatCompletionsStreamResponse {
	return streamResponseClaude2OpenAI(claudeResponse, 0)
}

func streamResponseClaude2OpenAI(claudeResponse *dto.ClaudeResponse, toolCallIndex int) *dto.ChatCompletionsStreamResponse {
	var response dto.ChatCompletionsStreamResponse
	response.Object = "chat.completion.chunk"
	response.Model = claudeResponse.Model
	response.Choices = make([]dto.ChatCompletionsStreamResponseChoice, 0)
	tools := make([]dto.ToolCallResponse, 0)
	var choice dto.ChatCompletionsStreamResponseChoice
	if claudeResponse.Type == "message_start" {
		if claudeResponse.Message != nil {
			response.Id = claudeResponse.Message.Id
			response.Model = claudeResponse.Message.Model
		}
		//claudeUsage = &claudeResponse.Message.Usage
		choice.Delta.SetContentString("")
		choice.Delta.Role = "assistant"
	} else if claudeResponse.Type == "content_block_start" {
		if claudeResponse.ContentBlock != nil {
			// 如果是文本块，尽可能发送首段文本（若存在）
			if claudeResponse.ContentBlock.Type == "text" && claudeResponse.ContentBlock.Text != nil {
				choice.Delta.SetContentString(*claudeResponse.ContentBlock.Text)
			}
			if claudeResponse.ContentBlock.Type == "tool_use" {
				tools = append(tools, dto.ToolCallResponse{
					Index: common.GetPointer(toolCallIndex),
					ID:    claudeResponse.ContentBlock.Id,
					Type:  "function",
					Function: dto.FunctionResponse{
						Name:      claudeResponse.ContentBlock.Name,
						Arguments: "",
					},
				})
			}
		} else {
			return nil
		}
	} else if claudeResponse.Type == "content_block_delta" {
		if claudeResponse.Delta != nil {
			choice.Delta.Content = claudeResponse.Delta.Text
			switch claudeResponse.Delta.Type {
			case "input_json_delta":
				if claudeResponse.Delta.PartialJson != nil {
					tools = append(tools, dto.ToolCallResponse{
						Type:  "function",
						Index: common.GetPointer(toolCallIndex),
						Function: dto.FunctionResponse{
							Arguments: *claudeResponse.Delta.PartialJson,
						},
					})
				}
			case "signature_delta":
				// Signature bytes are protocol metadata, not user-visible reasoning.
			case "thinking_delta":
				choice.Delta.ReasoningContent = claudeResponse.Delta.Thinking
			}
		}
	} else if claudeResponse.Type == "message_delta" {
		if claudeResponse.Delta != nil && claudeResponse.Delta.StopReason != nil {
			finishReason := stopReasonClaude2OpenAI(*claudeResponse.Delta.StopReason)
			if finishReason != "null" {
				choice.FinishReason = &finishReason
			}
		}
		//claudeUsage = &claudeResponse.Usage
	} else if claudeResponse.Type == "message_stop" {
		return nil
	} else {
		return nil
	}
	if len(tools) > 0 {
		choice.Delta.Content = nil // compatible with other OpenAI derivative applications, like LobeOpenAICompatibleFactory ...
		choice.Delta.ToolCalls = tools
	}
	response.Choices = append(response.Choices, choice)

	return &response
}

func ResponseClaude2OpenAI(claudeResponse *dto.ClaudeResponse) *dto.OpenAITextResponse {
	choices := make([]dto.OpenAITextResponseChoice, 0)
	fullTextResponse := dto.OpenAITextResponse{
		Id:      fmt.Sprintf("chatcmpl-%s", common.GetUUID()),
		Object:  "chat.completion",
		Created: common.GetTimestamp(),
	}
	var responseText strings.Builder
	var responseThinking strings.Builder
	tools := make([]dto.ToolCallResponse, 0)

	fullTextResponse.Id = claudeResponse.Id
	for _, message := range claudeResponse.Content {
		switch message.Type {
		case "tool_use":
			args, _ := common.Marshal(message.Input)
			tools = append(tools, dto.ToolCallResponse{
				ID:   message.Id,
				Type: "function", // compatible with other OpenAI derivative applications
				Function: dto.FunctionResponse{
					Name:      message.Name,
					Arguments: string(args),
				},
			})
		case "thinking":
			// 加密的不管， 只输出明文的推理过程
			if message.Thinking != nil {
				_, _ = responseThinking.WriteString(*message.Thinking)
			}
		case "text":
			_, _ = responseText.WriteString(message.GetText())
		}
	}
	choice := dto.OpenAITextResponseChoice{
		Index: 0,
		Message: dto.Message{
			Role: "assistant",
		},
		FinishReason: stopReasonClaude2OpenAI(claudeResponse.StopReason),
	}
	choice.SetStringContent(responseText.String())
	if len(tools) > 0 {
		choice.Message.SetToolCalls(tools)
	}
	choice.Message.ReasoningContent = responseThinking.String()
	fullTextResponse.Model = claudeResponse.Model
	choices = append(choices, choice)
	fullTextResponse.Choices = choices
	return &fullTextResponse
}

type ClaudeResponseInfo struct {
	ResponseId                  string
	Created                     int64
	Model                       string
	ResponseText                strings.Builder
	Usage                       *dto.Usage
	Done                        bool // enough completion evidence for OpenAI conversion
	SawMessageStart             bool // strict managed streams accept exactly one start event
	SawMessageStop              bool // required for a complete native Anthropic stream
	SawValidInputUsage          bool // strict managed streams must attest input usage before output
	SawTerminalUsage            bool // strict managed streams must attest final output usage
	SawTerminalEvent            bool // no content or second terminal event may follow
	UpstreamBillableOutputText  strings.Builder
	UpstreamBillableOutputBytes int64
	// BillableOutput tracks only semantic output emitted in the caller's
	// selected format and is the basis for interrupted-stream settlement.
	BillableOutputText             strings.Builder
	BillableOutputBytes            int64
	FailedReportedCompletionTokens int
	APIKeyOutputSuffix             string
	toolCallIndexes                map[int]int
	nextToolCallIndex              int
}

type claudeBillableEvidence struct {
	text  string
	bytes int64
}

type claudeBillableEvidenceBuilder struct {
	text  strings.Builder
	bytes int64
}

func (builder *claudeBillableEvidenceBuilder) addString(value string) error {
	if int64(len(value)) > math.MaxInt64-builder.bytes {
		return errors.New("Claude billable output exceeded the supported range")
	}
	builder.bytes += int64(len(value))
	_, _ = builder.text.WriteString(value)
	return nil
}

func (builder *claudeBillableEvidenceBuilder) addJSON(value any) error {
	data, err := common.Marshal(value)
	if err != nil {
		return errors.New("Claude billable output could not be serialized")
	}
	return builder.addString(string(data))
}

func (builder *claudeBillableEvidenceBuilder) build() claudeBillableEvidence {
	return claudeBillableEvidence{text: builder.text.String(), bytes: builder.bytes}
}

func addClaudeMediaMessageEvidence(builder *claudeBillableEvidenceBuilder, block *dto.ClaudeMediaMessage, strictType bool) error {
	if block == nil {
		return nil
	}
	for _, value := range []*string{block.Text, block.Thinking} {
		if value != nil {
			if err := builder.addString(*value); err != nil {
				return err
			}
		}
	}
	switch block.Type {
	case "text", "thinking":
		return nil
	case "tool_use":
		if err := builder.addString(block.Name); err != nil {
			return err
		}
		if block.Input != nil {
			return builder.addJSON(block.Input)
		}
		return nil
	case "redacted_thinking":
		return builder.addString(block.Data)
	default:
		if strictType {
			return errors.New("Claude response included an unsupported billable content block")
		}
		return nil
	}
}

func addClaudeMessageContentEvidence(builder *claudeBillableEvidenceBuilder, content any) error {
	if content == nil {
		return nil
	}
	if text, ok := content.(string); ok {
		return builder.addString(text)
	}
	blocks, err := common.Any2Type[[]dto.ClaudeMediaMessage](content)
	if err != nil {
		return errors.New("Claude message_start content could not be measured")
	}
	for index := range blocks {
		if err := addClaudeMediaMessageEvidence(builder, &blocks[index], true); err != nil {
			return err
		}
	}
	return nil
}

func claudeStreamBillableEvidence(response *dto.ClaudeResponse) (claudeBillableEvidence, error) {
	builder := &claudeBillableEvidenceBuilder{}
	if response == nil {
		return builder.build(), nil
	}
	if response.Type == "message_start" && response.Message != nil {
		if err := addClaudeMessageContentEvidence(builder, response.Message.Content); err != nil {
			return claudeBillableEvidence{}, err
		}
	}
	if delta := response.Delta; delta != nil {
		for _, value := range []*string{delta.Text, delta.Thinking, delta.PartialJson} {
			if value != nil {
				if err := builder.addString(*value); err != nil {
					return claudeBillableEvidence{}, err
				}
			}
		}
		if delta.Type == "redacted_thinking" {
			if err := builder.addString(delta.Data); err != nil {
				return claudeBillableEvidence{}, err
			}
		}
	}
	if block := response.ContentBlock; block != nil {
		if err := addClaudeMediaMessageEvidence(builder, block, true); err != nil {
			return claudeBillableEvidence{}, err
		}
	}
	return builder.build(), nil
}

func claudeNonStreamBillableEvidence(response *dto.ClaudeResponse) (claudeBillableEvidence, error) {
	builder := &claudeBillableEvidenceBuilder{}
	if response == nil {
		return builder.build(), nil
	}
	if err := builder.addString(response.Completion); err != nil {
		return claudeBillableEvidence{}, err
	}
	for index := range response.Content {
		if err := addClaudeMediaMessageEvidence(builder, &response.Content[index], true); err != nil {
			return claudeBillableEvidence{}, err
		}
	}
	return builder.build(), nil
}

func claudeNonStreamCredentialFragments(response *dto.ClaudeResponse, relayFormat types.RelayFormat) ([]string, error) {
	if response == nil {
		return nil, nil
	}
	if relayFormat != types.RelayFormatClaude {
		evidence, err := claudeNonStreamBillableEvidence(response)
		if err != nil || evidence.text == "" {
			return nil, err
		}
		return []string{evidence.text}, nil
	}
	fragments := make([]string, 0, 1+len(response.Content)*6)
	if response.Completion != "" {
		fragments = append(fragments, response.Completion)
	}
	for index := range response.Content {
		block := &response.Content[index]
		for _, value := range []*string{block.Text, block.Thinking, block.PartialJson} {
			if value != nil {
				fragments = append(fragments, *value)
			}
		}
		for _, value := range []string{block.Signature, block.Data, block.Id, block.Name, block.Delta} {
			if value != "" {
				fragments = append(fragments, value)
			}
		}
		if block.Input != nil {
			input, err := common.Marshal(block.Input)
			if err != nil {
				return nil, errors.New("Claude response tool input could not be checked for credential material")
			}
			fragments = append(fragments, string(input))
		}
		if content, ok := block.Content.(string); ok {
			fragments = append(fragments, content)
		}
	}
	return fragments, nil
}

func claudeWrittenStreamBillableEvidence(response *dto.ClaudeResponse, relayFormat types.RelayFormat) (claudeBillableEvidence, error) {
	if relayFormat == types.RelayFormatClaude {
		return claudeStreamBillableEvidence(response)
	}
	builder := &claudeBillableEvidenceBuilder{}
	if response == nil || relayFormat != types.RelayFormatOpenAI {
		return builder.build(), nil
	}
	if response.Type == "content_block_delta" && response.Delta != nil {
		delta := response.Delta
		switch delta.Type {
		case "input_json_delta":
			if delta.PartialJson != nil {
				if err := builder.addString(*delta.PartialJson); err != nil {
					return claudeBillableEvidence{}, err
				}
			} else if delta.Text != nil {
				if err := builder.addString(*delta.Text); err != nil {
					return claudeBillableEvidence{}, err
				}
			}
		case "thinking_delta":
			for _, value := range []*string{delta.Text, delta.Thinking} {
				if value != nil {
					if err := builder.addString(*value); err != nil {
						return claudeBillableEvidence{}, err
					}
				}
			}
		default:
			if delta.Text != nil {
				if err := builder.addString(*delta.Text); err != nil {
					return claudeBillableEvidence{}, err
				}
			}
		}
	}
	if response.Type == "content_block_start" && response.ContentBlock != nil {
		block := response.ContentBlock
		if block.Type == "text" && block.Text != nil {
			if err := builder.addString(*block.Text); err != nil {
				return claudeBillableEvidence{}, err
			}
		}
		if block.Type == "tool_use" {
			if err := builder.addString(block.Name); err != nil {
				return claudeBillableEvidence{}, err
			}
		}
	}
	return builder.build(), nil
}

func checkedClaudeBillableOutputBytes(current, additional int64) (int64, error) {
	if current < 0 || additional < 0 || current > math.MaxInt64-additional {
		return 0, errors.New("Claude billable output exceeded the supported range")
	}
	return current + additional, nil
}

func validateClaudeOutputEvidenceUsage(evidenceBytes int64, outputTokens int) error {
	if outputTokens < 0 {
		return errors.New("Claude response included invalid output usage")
	}
	requiredTokens, err := relaycommon.ClaudeEvidenceTokenFloor(evidenceBytes)
	if err != nil {
		return err
	}
	if requiredTokens > int64(outputTokens) {
		return errors.New("Claude response output usage did not cover its billable content")
	}
	return nil
}

func validateClaudeInputEvidenceUsage(info *relaycommon.RelayInfo, usage *dto.ClaudeUsage) error {
	if !isManagedClaudeRelay(info) || info.ClaudeInputEvidenceBytes <= 0 {
		return nil
	}
	canonical, err := canonicalizeClaudeUsage(usage)
	if err != nil {
		return err
	}
	reportedTokens, err := checkedClaudeUsageSum(
		canonical.InputTokens, canonical.CacheReadTokens, canonical.CacheCreationTokens,
	)
	if err != nil {
		return err
	}
	requiredTokens, err := relaycommon.ClaudeEvidenceTokenFloor(info.ClaudeInputEvidenceBytes)
	if err != nil {
		return err
	}
	if reportedTokens < requiredTokens {
		return errors.New("Claude response input usage did not cover the authorized request evidence")
	}
	return nil
}

func managedClaudeAPIKey(info *relaycommon.RelayInfo) string {
	if !isManagedClaudeRelay(info) || info.ChannelMeta == nil {
		return ""
	}
	return strings.TrimSpace(info.ChannelMeta.ApiKey)
}

func validateManagedClaudeResponseString(info *relaycommon.RelayInfo, data string) error {
	apiKey := managedClaudeAPIKey(info)
	if apiKey == "" {
		return nil
	}
	if strings.Contains(data, apiKey) {
		return errors.New("managed Claude response contained credential material")
	}
	var normalized any
	if err := common.UnmarshalJsonStr(data, &normalized); err != nil {
		// The protocol decoder reports malformed JSON separately.
		return nil
	}
	return validateManagedClaudeResponseValue(info, normalized)
}

func validateManagedClaudeResponseBytes(info *relaycommon.RelayInfo, data []byte) error {
	apiKey := managedClaudeAPIKey(info)
	if apiKey == "" {
		return nil
	}
	if strings.Contains(string(data), apiKey) {
		return errors.New("managed Claude response contained credential material")
	}
	var normalized any
	if err := common.Unmarshal(data, &normalized); err != nil {
		return nil
	}
	return validateManagedClaudeResponseValue(info, normalized)
}

func validateManagedClaudeResponseValue(info *relaycommon.RelayInfo, value any) error {
	apiKey := managedClaudeAPIKey(info)
	if apiKey == "" {
		return nil
	}
	data, err := common.Marshal(value)
	if err != nil {
		return errors.New("managed Claude response could not be checked for credential material")
	}
	var normalized any
	if err = common.Unmarshal(data, &normalized); err != nil {
		return errors.New("managed Claude response could not be checked for credential material")
	}
	if claudeResponseValueContainsAPIKey(normalized, apiKey) {
		return errors.New("managed Claude response contained credential material")
	}
	return nil
}

func claudeResponseValueContainsAPIKey(value any, apiKey string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, apiKey)
	case []any:
		for _, item := range typed {
			if claudeResponseValueContainsAPIKey(item, apiKey) {
				return true
			}
		}
	case map[string]any:
		for key, item := range typed {
			if strings.Contains(key, apiKey) || claudeResponseValueContainsAPIKey(item, apiKey) {
				return true
			}
		}
	}
	return false
}

func claudeWrittenStreamFragments(response *dto.ClaudeResponse, relayFormat types.RelayFormat) ([]string, error) {
	if response == nil {
		return nil, nil
	}
	if relayFormat == types.RelayFormatOpenAI {
		evidence, err := claudeWrittenStreamBillableEvidence(response, relayFormat)
		if err != nil || evidence.text == "" {
			return nil, err
		}
		return []string{evidence.text}, nil
	}
	fragments := make([]string, 0, 4)
	if relayFormat == types.RelayFormatClaude && response.Type == "message_start" && response.Message != nil {
		builder := &claudeBillableEvidenceBuilder{}
		if err := addClaudeMessageContentEvidence(builder, response.Message.Content); err != nil {
			return nil, err
		}
		if evidence := builder.build(); evidence.text != "" {
			fragments = append(fragments, evidence.text)
		}
	}
	if delta := response.Delta; delta != nil {
		if delta.Text != nil {
			fragments = append(fragments, *delta.Text)
		}
		if delta.Thinking != nil {
			fragments = append(fragments, *delta.Thinking)
		}
		if delta.PartialJson != nil {
			fragments = append(fragments, *delta.PartialJson)
		}
		if delta.Type == "redacted_thinking" {
			fragments = append(fragments, delta.Data)
		}
		if delta.Signature != "" {
			fragments = append(fragments, delta.Signature)
		}
	}
	if block := response.ContentBlock; block != nil {
		if block.Text != nil {
			fragments = append(fragments, *block.Text)
		}
		if block.Thinking != nil {
			fragments = append(fragments, *block.Thinking)
		}
		if block.Type == "tool_use" {
			fragments = append(fragments, block.Name)
			if block.Input != nil {
				input, err := common.Marshal(block.Input)
				if err != nil {
					return nil, errors.New("Claude stream tool input could not be checked for credential material")
				}
				fragments = append(fragments, string(input))
			}
		}
		if block.Type == "redacted_thinking" {
			fragments = append(fragments, block.Data)
		}
	}
	return fragments, nil
}

func advanceClaudeAPIKeySuffix(current, apiKey string, fragments []string) (string, bool) {
	if apiKey == "" {
		return "", false
	}
	suffix := current
	for _, fragment := range fragments {
		combined := suffix + fragment
		if strings.Contains(combined, apiKey) {
			return suffix, true
		}
		maxSuffixBytes := min(len(combined), len(apiKey)-1)
		suffix = ""
		for suffixBytes := maxSuffixBytes; suffixBytes > 0; suffixBytes-- {
			candidate := combined[len(combined)-suffixBytes:]
			if strings.HasPrefix(apiKey, candidate) {
				suffix = candidate
				break
			}
		}
	}
	return suffix, false
}

func (info *ClaudeResponseInfo) openAIToolCallIndex(response *dto.ClaudeResponse) int {
	if info == nil || response == nil || response.Index == nil {
		return 0
	}
	contentBlockIndex := *response.Index
	if toolIndex, ok := info.toolCallIndexes[contentBlockIndex]; ok {
		return toolIndex
	}
	isToolStart := response.Type == "content_block_start" && response.ContentBlock != nil && response.ContentBlock.Type == "tool_use"
	isToolDelta := response.Type == "content_block_delta" && response.Delta != nil && response.Delta.Type == "input_json_delta"
	if !isToolStart && !isToolDelta {
		return 0
	}
	if info.toolCallIndexes == nil {
		info.toolCallIndexes = make(map[int]int)
	}
	toolIndex := info.nextToolCallIndex
	info.toolCallIndexes[contentBlockIndex] = toolIndex
	info.nextToolCallIndex++
	return toolIndex
}

func claudeStreamMayAddToolCallIndex(response *dto.ClaudeResponse) bool {
	if response == nil || response.Index == nil {
		return false
	}
	return (response.Type == "content_block_start" && response.ContentBlock != nil && response.ContentBlock.Type == "tool_use") ||
		(response.Type == "content_block_delta" && response.Delta != nil && response.Delta.Type == "input_json_delta")
}

func hasClaudeStopReason(claudeResponse *dto.ClaudeResponse) bool {
	return claudeResponse != nil && claudeResponse.Delta != nil &&
		claudeResponse.Delta.StopReason != nil && validClaudeStopReason(*claudeResponse.Delta.StopReason)
}

func validClaudeStopReason(reason string) bool {
	reason = strings.TrimSpace(reason)
	return reason != "" && !strings.EqualFold(reason, "null")
}

func claudeUsageHasNegativeValue(usage *dto.ClaudeUsage) bool {
	if usage == nil {
		return false
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 ||
		usage.CacheReadInputTokens < 0 || usage.CacheCreationInputTokens < 0 ||
		usage.ClaudeCacheCreation5mTokens < 0 || usage.ClaudeCacheCreation1hTokens < 0 {
		return true
	}
	if usage.CacheCreation != nil &&
		(usage.CacheCreation.Ephemeral5mInputTokens < 0 || usage.CacheCreation.Ephemeral1hInputTokens < 0) {
		return true
	}
	return usage.ServerToolUse != nil && usage.ServerToolUse.WebSearchRequests < 0
}

func hasValidClaudeUsage(usage *dto.ClaudeUsage) bool {
	if usage == nil || claudeUsageHasNegativeValue(usage) {
		return false
	}
	return usage.InputTokens > 0 || usage.OutputTokens > 0 ||
		usage.CacheReadInputTokens > 0 || usage.CacheCreationInputTokens > 0 ||
		usage.ClaudeCacheCreation5mTokens > 0 || usage.ClaudeCacheCreation1hTokens > 0 ||
		usage.GetCacheCreation5mTokens() > 0 || usage.GetCacheCreation1hTokens() > 0
}

func hasValidClaudeInputUsage(usage *dto.ClaudeUsage) bool {
	if !hasValidClaudeUsage(usage) {
		return false
	}
	return usage.InputTokens > 0 || usage.CacheReadInputTokens > 0 ||
		usage.CacheCreationInputTokens > 0 || usage.ClaudeCacheCreation5mTokens > 0 ||
		usage.ClaudeCacheCreation1hTokens > 0 || usage.GetCacheCreation5mTokens() > 0 ||
		usage.GetCacheCreation1hTokens() > 0
}

func hasValidClaudeTerminalUsage(usage *dto.ClaudeUsage) bool {
	return usage != nil && !claudeUsageHasNegativeValue(usage) && usage.OutputTokens > 0
}

type canonicalClaudeUsage struct {
	InputTokens         int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	OutputTokens        int64
}

func checkedClaudeUsageSum(values ...int64) (int64, error) {
	const maxInt64 = int64(^uint64(0) >> 1)
	var total int64
	for _, value := range values {
		if value < 0 || total > maxInt64-value {
			return 0, fmt.Errorf("Claude response usage overflowed the supported range")
		}
		total += value
	}
	return total, nil
}

func canonicalizeClaudeUsage(usage *dto.ClaudeUsage) (canonicalClaudeUsage, error) {
	if usage == nil {
		return canonicalClaudeUsage{}, nil
	}
	if claudeUsageHasNegativeValue(usage) {
		return canonicalClaudeUsage{}, fmt.Errorf("Claude response included invalid usage")
	}
	nested5m, nested1h := 0, 0
	if usage.CacheCreation != nil {
		nested5m = usage.CacheCreation.Ephemeral5mInputTokens
		nested1h = usage.CacheCreation.Ephemeral1hInputTokens
	}
	nestedSplit := nested5m > 0 || nested1h > 0
	legacySplit := usage.ClaudeCacheCreation5mTokens > 0 || usage.ClaudeCacheCreation1hTokens > 0
	if nestedSplit && legacySplit {
		return canonicalClaudeUsage{}, fmt.Errorf("Claude response included conflicting cache creation usage")
	}
	cache5m, cache1h := usage.ClaudeCacheCreation5mTokens, usage.ClaudeCacheCreation1hTokens
	if nestedSplit {
		cache5m, cache1h = nested5m, nested1h
	}
	splitTotal, err := checkedClaudeUsageSum(int64(cache5m), int64(cache1h))
	if err != nil {
		return canonicalClaudeUsage{}, err
	}
	cacheCreationTotal := int64(usage.CacheCreationInputTokens)
	if cacheCreationTotal > 0 && splitTotal > 0 && cacheCreationTotal != splitTotal {
		return canonicalClaudeUsage{}, fmt.Errorf("Claude response included inconsistent cache creation usage")
	}
	if splitTotal > cacheCreationTotal {
		cacheCreationTotal = splitTotal
	}
	return canonicalClaudeUsage{
		InputTokens:         int64(usage.InputTokens),
		CacheReadTokens:     int64(usage.CacheReadInputTokens),
		CacheCreationTokens: cacheCreationTotal,
		OutputTokens:        int64(usage.OutputTokens),
	}, nil
}

func validateClaudeUsageAuthorization(info *relaycommon.RelayInfo, usage *dto.ClaudeUsage) error {
	if info == nil || !info.RequiresClaudeUsageAuthorization() {
		return nil
	}
	if info.AuthorizedPromptTokens <= 0 || info.AuthorizedCompletionTokens <= 0 {
		return fmt.Errorf("Claude response is missing price authorization bounds")
	}
	canonical, err := canonicalizeClaudeUsage(usage)
	if err != nil {
		return err
	}
	inputTokens, err := checkedClaudeUsageSum(
		canonical.InputTokens, canonical.CacheReadTokens, canonical.CacheCreationTokens,
	)
	if err != nil {
		return err
	}
	if inputTokens > int64(info.AuthorizedPromptTokens) {
		return fmt.Errorf("Claude input usage exceeded the price authorization bound")
	}
	if canonical.OutputTokens > int64(info.AuthorizedCompletionTokens) {
		return fmt.Errorf("Claude output usage exceeded the price authorization bound")
	}
	if usage != nil && usage.ServerToolUse != nil && usage.ServerToolUse.WebSearchRequests > 0 &&
		(info.PriceData.PriceProvider == types.PriceProviderZenMux || info.PriceData.PriceProvider == types.PriceProviderTabCode) {
		return fmt.Errorf("managed Claude provider returned unsupported web_search usage")
	}
	return nil
}

func validateClaudeResponseToolPricing(info *relaycommon.RelayInfo, response *dto.ClaudeResponse) error {
	if info == nil || response == nil ||
		(info.PriceData.PriceProvider != types.PriceProviderZenMux && info.PriceData.PriceProvider != types.PriceProviderTabCode) {
		return nil
	}
	if claudeContentBlockUsesWebSearch(response.ContentBlock) || claudeContentBlockUsesWebSearch(response.Delta) {
		return fmt.Errorf("managed Claude provider returned unpriced web_search content")
	}
	for index := range response.Content {
		if claudeContentBlockUsesWebSearch(&response.Content[index]) {
			return fmt.Errorf("managed Claude provider returned unpriced web_search content")
		}
	}
	if response.Message != nil {
		for _, block := range response.Message.ParseMediaContent() {
			if claudeContentBlockUsesWebSearch(&block) {
				return fmt.Errorf("managed Claude provider returned unpriced web_search content")
			}
		}
	}
	return nil
}

func claudeContentBlockUsesWebSearch(block *dto.ClaudeMediaMessage) bool {
	if block == nil {
		return false
	}
	blockType := strings.ToLower(strings.TrimSpace(block.Type))
	if strings.HasPrefix(blockType, "web_search_tool_result") {
		return true
	}
	return blockType == "server_tool_use" &&
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(block.Name)), "web_search")
}

func validateClaudeResponseModelAuthorization(info *relaycommon.RelayInfo, modelName string) error {
	if info == nil || !info.RequiresClaudeUsageAuthorization() {
		return nil
	}
	if strings.TrimSpace(modelName) == "" || modelName != info.UpstreamModelName {
		return fmt.Errorf("Claude response model did not match the authorized upstream model")
	}
	return nil
}

func validateAccumulatedClaudeUsageAuthorization(info *relaycommon.RelayInfo, usage *dto.Usage) error {
	if info == nil || !info.RequiresClaudeUsageAuthorization() || usage == nil {
		return nil
	}
	cacheCreationTotal := int64(usage.PromptTokensDetails.CachedCreationTokens)
	splitTotal, err := checkedClaudeUsageSum(
		int64(usage.ClaudeCacheCreation5mTokens), int64(usage.ClaudeCacheCreation1hTokens),
	)
	if err != nil {
		return err
	}
	if cacheCreationTotal > 0 && splitTotal > 0 && cacheCreationTotal != splitTotal {
		return fmt.Errorf("Claude response included inconsistent accumulated cache creation usage")
	}
	if splitTotal > cacheCreationTotal {
		cacheCreationTotal = splitTotal
	}
	inputTokens, err := checkedClaudeUsageSum(
		int64(usage.PromptTokens), int64(usage.PromptTokensDetails.CachedTokens), cacheCreationTotal,
	)
	if err != nil {
		return err
	}
	if inputTokens > int64(info.AuthorizedPromptTokens) {
		return fmt.Errorf("Claude input usage exceeded the price authorization bound")
	}
	if int64(usage.CompletionTokens) > int64(info.AuthorizedCompletionTokens) {
		return fmt.Errorf("Claude output usage exceeded the price authorization bound")
	}
	return nil
}

func claudeUsageRegresses(current *dto.Usage, next *dto.ClaudeUsage) bool {
	if current == nil || next == nil {
		return false
	}
	return positiveCounterRegresses(current.PromptTokens, next.InputTokens) ||
		positiveCounterRegresses(current.CompletionTokens, next.OutputTokens) ||
		positiveCounterRegresses(current.PromptTokensDetails.CachedTokens, next.CacheReadInputTokens) ||
		positiveCounterRegresses(current.PromptTokensDetails.CachedCreationTokens, next.CacheCreationInputTokens) ||
		positiveCounterRegresses(current.ClaudeCacheCreation5mTokens, next.GetCacheCreation5mTokens()) ||
		positiveCounterRegresses(current.ClaudeCacheCreation1hTokens, next.GetCacheCreation1hTokens())
}

func positiveCounterRegresses(current, next int) bool {
	return current > 0 && next > 0 && next < current
}

func isManagedClaudeRelay(info *relaycommon.RelayInfo) bool {
	return info != nil && info.ChannelMeta != nil && info.ChannelMeta.ManagedProvider
}

func isManagedClaudeStreamEventType(eventType string) bool {
	switch eventType {
	case "message_start", "content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop", "ping", "error":
		return true
	default:
		return false
	}
}

func cacheCreationTokensForOpenAIUsage(usage *dto.Usage) int {
	if usage == nil {
		return 0
	}
	splitCacheCreationTokens := usage.ClaudeCacheCreation5mTokens + usage.ClaudeCacheCreation1hTokens
	if splitCacheCreationTokens == 0 {
		return usage.PromptTokensDetails.CachedCreationTokens
	}
	if usage.PromptTokensDetails.CachedCreationTokens > splitCacheCreationTokens {
		return usage.PromptTokensDetails.CachedCreationTokens
	}
	return splitCacheCreationTokens
}

func buildOpenAIStyleUsageFromClaudeUsage(usage *dto.Usage) dto.Usage {
	if usage == nil {
		return dto.Usage{}
	}
	clone := *usage
	clone.ClaudeCacheCreation5mTokens, clone.ClaudeCacheCreation1hTokens = service.NormalizeCacheCreationSplit(
		usage.PromptTokensDetails.CachedCreationTokens,
		usage.ClaudeCacheCreation5mTokens,
		usage.ClaudeCacheCreation1hTokens,
	)
	cacheCreationTokens := cacheCreationTokensForOpenAIUsage(usage)
	totalInputTokens := usage.PromptTokens + usage.PromptTokensDetails.CachedTokens + cacheCreationTokens
	clone.PromptTokens = totalInputTokens
	clone.InputTokens = totalInputTokens
	clone.TotalTokens = totalInputTokens + usage.CompletionTokens
	clone.UsageSemantic = "openai"
	clone.UsageSource = "anthropic"
	return clone
}

func buildMessageDeltaPatchUsage(claudeResponse *dto.ClaudeResponse, claudeInfo *ClaudeResponseInfo) *dto.ClaudeUsage {
	usage := &dto.ClaudeUsage{}
	if claudeResponse != nil && claudeResponse.Usage != nil {
		*usage = *claudeResponse.Usage
	}

	if claudeInfo == nil || claudeInfo.Usage == nil {
		return usage
	}

	if usage.InputTokens == 0 && claudeInfo.Usage.PromptTokens > 0 {
		usage.InputTokens = claudeInfo.Usage.PromptTokens
	}
	if usage.CacheReadInputTokens == 0 && claudeInfo.Usage.PromptTokensDetails.CachedTokens > 0 {
		usage.CacheReadInputTokens = claudeInfo.Usage.PromptTokensDetails.CachedTokens
	}
	if usage.CacheCreationInputTokens == 0 && claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens > 0 {
		usage.CacheCreationInputTokens = claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens
	}
	cacheCreation5m := 0
	cacheCreation1h := 0
	if usage.CacheCreation != nil {
		cacheCreation5m = usage.CacheCreation.Ephemeral5mInputTokens
		cacheCreation1h = usage.CacheCreation.Ephemeral1hInputTokens
	} else {
		cacheCreation5m = claudeInfo.Usage.ClaudeCacheCreation5mTokens
		cacheCreation1h = claudeInfo.Usage.ClaudeCacheCreation1hTokens
	}
	cacheCreation5m, cacheCreation1h = service.NormalizeCacheCreationSplit(
		usage.CacheCreationInputTokens,
		cacheCreation5m,
		cacheCreation1h,
	)
	if usage.CacheCreation == nil && (cacheCreation5m > 0 || cacheCreation1h > 0) {
		usage.CacheCreation = &dto.ClaudeCacheCreationUsage{}
	}
	if usage.CacheCreation != nil {
		usage.CacheCreation.Ephemeral5mInputTokens = cacheCreation5m
		usage.CacheCreation.Ephemeral1hInputTokens = cacheCreation1h
	}
	return usage
}

func shouldSkipClaudeMessageDeltaUsagePatch(info *relaycommon.RelayInfo) bool {
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled {
		return true
	}
	if info == nil || info.ChannelMeta == nil {
		return false
	}
	return info.ChannelSetting.PassThroughBodyEnabled
}

func patchClaudeMessageDeltaUsageData(data string, usage *dto.ClaudeUsage) string {
	if data == "" || usage == nil {
		return data
	}

	data = setMessageDeltaUsageInt(data, "usage.input_tokens", usage.InputTokens)
	data = setMessageDeltaUsageInt(data, "usage.cache_read_input_tokens", usage.CacheReadInputTokens)
	data = setMessageDeltaUsageInt(data, "usage.cache_creation_input_tokens", usage.CacheCreationInputTokens)

	if usage.CacheCreation != nil {
		data = setMessageDeltaUsageInt(data, "usage.cache_creation.ephemeral_5m_input_tokens", usage.CacheCreation.Ephemeral5mInputTokens)
		data = setMessageDeltaUsageInt(data, "usage.cache_creation.ephemeral_1h_input_tokens", usage.CacheCreation.Ephemeral1hInputTokens)
	}

	return data
}

func setMessageDeltaUsageInt(data string, path string, localValue int) string {
	if localValue <= 0 {
		return data
	}

	upstreamValue := gjson.Get(data, path)
	if upstreamValue.Exists() && upstreamValue.Int() > 0 {
		return data
	}

	patchedData, err := sjson.Set(data, path, localValue)
	if err != nil {
		return data
	}
	return patchedData
}

func FormatClaudeResponseInfo(claudeResponse *dto.ClaudeResponse, oaiResponse *dto.ChatCompletionsStreamResponse, claudeInfo *ClaudeResponseInfo) bool {
	if claudeInfo == nil {
		return false
	}
	if claudeInfo.Usage == nil {
		claudeInfo.Usage = &dto.Usage{}
	}
	if claudeResponse.Type == "message_start" {
		claudeInfo.SawMessageStart = true
		if claudeResponse.Message != nil {
			claudeInfo.ResponseId = claudeResponse.Message.Id
			claudeInfo.Model = claudeResponse.Message.Model
		}

		// message_start, 获取usage
		if claudeResponse.Message != nil && claudeResponse.Message.Usage != nil {
			claudeInfo.SawValidInputUsage = hasValidClaudeInputUsage(claudeResponse.Message.Usage)
			claudeInfo.Usage.PromptTokens = max(claudeInfo.Usage.PromptTokens, claudeResponse.Message.Usage.InputTokens)
			claudeInfo.Usage.UsageSemantic = "anthropic"
			claudeInfo.Usage.PromptTokensDetails.CachedTokens = max(claudeInfo.Usage.PromptTokensDetails.CachedTokens, claudeResponse.Message.Usage.CacheReadInputTokens)
			claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens = max(claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens, claudeResponse.Message.Usage.CacheCreationInputTokens)
			claudeInfo.Usage.ClaudeCacheCreation5mTokens = max(claudeInfo.Usage.ClaudeCacheCreation5mTokens, claudeResponse.Message.Usage.GetCacheCreation5mTokens())
			claudeInfo.Usage.ClaudeCacheCreation1hTokens = max(claudeInfo.Usage.ClaudeCacheCreation1hTokens, claudeResponse.Message.Usage.GetCacheCreation1hTokens())
			claudeInfo.Usage.CompletionTokens = max(claudeInfo.Usage.CompletionTokens, claudeResponse.Message.Usage.OutputTokens)
		}
	} else if claudeResponse.Type == "content_block_delta" {
		if claudeResponse.Delta != nil {
			if claudeResponse.Delta.Text != nil {
				claudeInfo.ResponseText.WriteString(*claudeResponse.Delta.Text)
			}
			if claudeResponse.Delta.Thinking != nil {
				claudeInfo.ResponseText.WriteString(*claudeResponse.Delta.Thinking)
			}
			if claudeResponse.Delta.PartialJson != nil {
				claudeInfo.ResponseText.WriteString(*claudeResponse.Delta.PartialJson)
			}
		}
	} else if claudeResponse.Type == "message_delta" {
		// 最终的usage获取
		if claudeResponse.Usage != nil {
			if hasClaudeStopReason(claudeResponse) && hasValidClaudeTerminalUsage(claudeResponse.Usage) {
				claudeInfo.SawTerminalUsage = true
				claudeInfo.SawTerminalEvent = true
			}
			claudeInfo.Usage.UsageSemantic = "anthropic"
			if claudeResponse.Usage.InputTokens > 0 {
				// 不叠加，只取最新的
				claudeInfo.Usage.PromptTokens = max(claudeInfo.Usage.PromptTokens, claudeResponse.Usage.InputTokens)
			}
			if claudeResponse.Usage.CacheReadInputTokens > 0 {
				claudeInfo.Usage.PromptTokensDetails.CachedTokens = max(claudeInfo.Usage.PromptTokensDetails.CachedTokens, claudeResponse.Usage.CacheReadInputTokens)
			}
			if claudeResponse.Usage.CacheCreationInputTokens > 0 {
				claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens = max(claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens, claudeResponse.Usage.CacheCreationInputTokens)
			}
			if cacheCreation5m := claudeResponse.Usage.GetCacheCreation5mTokens(); cacheCreation5m > 0 {
				claudeInfo.Usage.ClaudeCacheCreation5mTokens = max(claudeInfo.Usage.ClaudeCacheCreation5mTokens, cacheCreation5m)
			}
			if cacheCreation1h := claudeResponse.Usage.GetCacheCreation1hTokens(); cacheCreation1h > 0 {
				claudeInfo.Usage.ClaudeCacheCreation1hTokens = max(claudeInfo.Usage.ClaudeCacheCreation1hTokens, cacheCreation1h)
			}
			if claudeResponse.Usage.OutputTokens > 0 {
				claudeInfo.Usage.CompletionTokens = max(claudeInfo.Usage.CompletionTokens, claudeResponse.Usage.OutputTokens)
			}
			claudeInfo.Usage.TotalTokens = claudeInfo.Usage.PromptTokens + claudeInfo.Usage.CompletionTokens
		}

		// A message_delta is completion evidence only when Anthropic supplies a
		// non-empty stop_reason. Some compatible upstreams emit usage-only or
		// malformed message_delta events before disconnecting.
		if hasClaudeStopReason(claudeResponse) {
			claudeInfo.Done = true
		}
	} else if claudeResponse.Type == "content_block_start" {
		if claudeResponse.ContentBlock != nil && claudeResponse.ContentBlock.Text != nil {
			claudeInfo.ResponseText.WriteString(*claudeResponse.ContentBlock.Text)
		}
	} else if claudeResponse.Type == "message_stop" {
		// message_stop is Anthropic's canonical semantic completion event. It
		// has no OpenAI chunk representation, so update state and skip output.
		claudeInfo.Done = true
		claudeInfo.SawMessageStop = true
		return false
	} else {
		return false
	}
	if oaiResponse != nil {
		oaiResponse.Id = claudeInfo.ResponseId
		oaiResponse.Created = claudeInfo.Created
		oaiResponse.Model = claudeInfo.Model
	}
	return true
}

func HandleStreamResponseData(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo, data string) *types.NewAPIError {
	if err := validateManagedClaudeResponseString(info, data); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if isManagedClaudeRelay(info) {
		eventType, err := helper.ParseSSEEventType(data)
		if err != nil {
			return types.NewError(fmt.Errorf("Claude stream included invalid event type: %w", err), types.ErrorCodeBadResponseBody)
		}
		if !isManagedClaudeStreamEventType(eventType) {
			return types.NewError(fmt.Errorf("Claude stream included an unsupported event type"), types.ErrorCodeBadResponseBody)
		}
	}

	var claudeResponse dto.ClaudeResponse
	err := common.UnmarshalJsonStr(data, &claudeResponse)
	if err != nil {
		common.SysLog("error unmarshalling stream response: " + err.Error())
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if claudeError := claudeResponse.GetClaudeError(); claudeError != nil {
		return types.WithClaudeError(service.SanitizeClaudeError(c, *claudeError), http.StatusInternalServerError)
	}
	if err = validateClaudeResponseToolPricing(info, &claudeResponse); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if claudeResponse.Type == "message_start" {
		if claudeResponse.Message == nil {
			return types.NewError(fmt.Errorf("Claude message_start did not include a message"), types.ErrorCodeBadResponseBody)
		}
		if err = validateClaudeResponseModelAuthorization(info, claudeResponse.Message.Model); err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
	}
	if err = validateClaudeUsageAuthorization(info, claudeResponse.Usage); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if claudeResponse.Message != nil {
		if err = validateClaudeUsageAuthorization(info, claudeResponse.Message.Usage); err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
	}
	if claudeResponse.Type == "message_start" {
		if err = validateClaudeInputEvidenceUsage(info, claudeResponse.Message.Usage); err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
	}
	if isManagedClaudeRelay(info) {
		if claudeUsageHasNegativeValue(claudeResponse.Usage) ||
			(claudeResponse.Message != nil && claudeUsageHasNegativeValue(claudeResponse.Message.Usage)) {
			return types.NewError(fmt.Errorf("Claude response included invalid usage"), types.ErrorCodeBadResponseBody)
		}
		if claudeResponse.Type == "ping" {
			// A managed route stays retryable until message_start attests input usage.
			// Once the stream has started, keep the downstream connection alive in
			// the protocol the caller selected.
			if claudeInfo == nil || !claudeInfo.SawMessageStart || !claudeInfo.SawValidInputUsage {
				return nil
			}
			switch info.RelayFormat {
			case types.RelayFormatClaude:
				err = helper.ClaudeChunkData(c, claudeResponse, data)
			case types.RelayFormatOpenAI:
				err = helper.PingData(c)
			}
			if err != nil {
				return types.NewError(err, types.ErrorCodeBadResponseBody)
			}
			return nil
		}
		if claudeInfo != nil && claudeInfo.SawTerminalEvent {
			if claudeResponse.Type != "message_stop" || claudeInfo.SawMessageStop {
				return types.NewError(fmt.Errorf("Claude stream emitted data after its terminal event"), types.ErrorCodeBadResponseBody)
			}
		}
		if claudeResponse.Type == "message_start" && claudeInfo != nil && claudeInfo.SawMessageStart {
			return types.NewError(fmt.Errorf("Claude stream emitted duplicate message_start"), types.ErrorCodeBadResponseBody)
		}
		if claudeResponse.Type != "message_start" &&
			(claudeInfo == nil || !claudeInfo.SawMessageStart || !claudeInfo.SawValidInputUsage) {
			return types.NewError(fmt.Errorf("Claude stream emitted data before valid message_start usage"), types.ErrorCodeBadResponseBody)
		}
		if claudeResponse.Type == "message_start" && (claudeResponse.Message == nil ||
			!hasValidClaudeInputUsage(claudeResponse.Message.Usage)) {
			return types.NewError(fmt.Errorf("Claude message_start did not include valid usage"), types.ErrorCodeBadResponseBody)
		}
		if claudeResponse.Type == "message_delta" && hasClaudeStopReason(&claudeResponse) &&
			!hasValidClaudeTerminalUsage(claudeResponse.Usage) {
			return types.NewError(fmt.Errorf("Claude message_delta did not include valid terminal usage"), types.ErrorCodeBadResponseBody)
		}
		if claudeResponse.Type == "message_delta" && claudeUsageRegresses(claudeInfo.Usage, claudeResponse.Usage) {
			return types.NewError(fmt.Errorf("Claude usage counters regressed"), types.ErrorCodeBadResponseBody)
		}
		if claudeResponse.Type == "message_stop" && (claudeInfo == nil || !claudeInfo.SawTerminalUsage) {
			return types.NewError(fmt.Errorf("Claude stream ended without valid terminal usage"), types.ErrorCodeBadResponseBody)
		}
	}
	var billableEvidence claudeBillableEvidence
	projectedUpstreamBillableBytes := int64(0)
	if isManagedClaudeRelay(info) {
		billableEvidence, err = claudeStreamBillableEvidence(&claudeResponse)
		if err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		projectedUpstreamBillableBytes, err = checkedClaudeBillableOutputBytes(
			claudeInfo.UpstreamBillableOutputBytes, billableEvidence.bytes,
		)
		if err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		if claudeResponse.Type == "message_delta" && hasClaudeStopReason(&claudeResponse) &&
			hasValidClaudeTerminalUsage(claudeResponse.Usage) {
			reportedCompletionTokens := claudeResponse.Usage.OutputTokens
			if claudeInfo.Usage != nil {
				reportedCompletionTokens = max(reportedCompletionTokens, claudeInfo.Usage.CompletionTokens)
			}
			if err = validateClaudeOutputEvidenceUsage(projectedUpstreamBillableBytes, reportedCompletionTokens); err != nil {
				claudeInfo.FailedReportedCompletionTokens = max(
					claudeInfo.FailedReportedCompletionTokens, reportedCompletionTokens,
				)
				return types.NewError(err, types.ErrorCodeBadResponseBody)
			}
		}
	}
	stateBeforeEvent := snapshotClaudeResponseInfo(
		claudeInfo,
		info.RelayFormat == types.RelayFormatOpenAI && claudeStreamMayAddToolCallIndex(&claudeResponse),
	)
	var openAIResponse *dto.ChatCompletionsStreamResponse
	shouldWrite := info.RelayFormat == types.RelayFormatClaude
	if info.RelayFormat == types.RelayFormatClaude {
		FormatClaudeResponseInfo(&claudeResponse, nil, claudeInfo)
	} else if info.RelayFormat == types.RelayFormatOpenAI {
		openAIResponse = streamResponseClaude2OpenAI(&claudeResponse, claudeInfo.openAIToolCallIndex(&claudeResponse))
		shouldWrite = FormatClaudeResponseInfo(&claudeResponse, openAIResponse, claudeInfo)
	}
	if isManagedClaudeRelay(info) {
		claudeInfo.UpstreamBillableOutputBytes = projectedUpstreamBillableBytes
		_, _ = claudeInfo.UpstreamBillableOutputText.WriteString(billableEvidence.text)
	}
	if err = validateAccumulatedClaudeUsageAuthorization(info, claudeInfo.Usage); err != nil {
		restoreClaudeResponseInfo(claudeInfo, stateBeforeEvent)
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}

	nextAPIKeyOutputSuffix := claudeInfo.APIKeyOutputSuffix
	var writtenBillableEvidence claudeBillableEvidence
	if shouldWrite {
		if isManagedClaudeRelay(info) {
			writtenBillableEvidence, err = claudeWrittenStreamBillableEvidence(&claudeResponse, info.RelayFormat)
			if err != nil {
				restoreClaudeResponseInfo(claudeInfo, stateBeforeEvent)
				return types.NewError(err, types.ErrorCodeBadResponseBody)
			}
			if _, err = checkedClaudeBillableOutputBytes(
				claudeInfo.BillableOutputBytes, writtenBillableEvidence.bytes,
			); err != nil {
				restoreClaudeResponseInfo(claudeInfo, stateBeforeEvent)
				return types.NewError(err, types.ErrorCodeBadResponseBody)
			}
		}
		switch info.RelayFormat {
		case types.RelayFormatClaude:
			if claudeResponse.Type == "message_delta" && !shouldSkipClaudeMessageDeltaUsagePatch(info) {
				// Compatible upstreams may omit the input/cache counters from the
				// terminal delta. Patch only from the now-authorized cumulative state.
				data = patchClaudeMessageDeltaUsageData(data, buildMessageDeltaPatchUsage(&claudeResponse, claudeInfo))
			}
			err = validateManagedClaudeResponseString(info, data)
		case types.RelayFormatOpenAI:
			err = validateManagedClaudeResponseValue(info, openAIResponse)
		}
		if err != nil {
			restoreClaudeResponseInfo(claudeInfo, stateBeforeEvent)
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		fragments, fragmentErr := claudeWrittenStreamFragments(&claudeResponse, info.RelayFormat)
		if fragmentErr != nil {
			restoreClaudeResponseInfo(claudeInfo, stateBeforeEvent)
			return types.NewError(fragmentErr, types.ErrorCodeBadResponseBody)
		}
		var leaksAPIKey bool
		nextAPIKeyOutputSuffix, leaksAPIKey = advanceClaudeAPIKeySuffix(
			claudeInfo.APIKeyOutputSuffix, managedClaudeAPIKey(info), fragments,
		)
		if leaksAPIKey {
			restoreClaudeResponseInfo(claudeInfo, stateBeforeEvent)
			return types.NewError(
				errors.New("managed Claude response contained credential material"),
				types.ErrorCodeBadResponseBody,
			)
		}
	}

	// Side effects are committed only after the whole event, including its
	// cumulative usage and outgoing bytes, has passed authorization.
	if claudeResponse.StopReason != "" {
		maybeMarkClaudeRefusal(c, claudeResponse.StopReason)
	}
	if claudeResponse.Delta != nil && claudeResponse.Delta.StopReason != nil {
		maybeMarkClaudeRefusal(c, *claudeResponse.Delta.StopReason)
	}
	recordClaudeWebSearchRequests(c, claudeResponse.Usage)
	if claudeResponse.Message != nil {
		recordClaudeWebSearchRequests(c, claudeResponse.Message.Usage)
	}
	if !shouldWrite {
		return nil
	}
	if info.RelayFormat == types.RelayFormatClaude {
		if err = helper.ClaudeChunkData(c, claudeResponse, data); err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
	} else if info.RelayFormat == types.RelayFormatOpenAI {
		if err = helper.ObjectData(c, openAIResponse); err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
	}
	if writtenBillableEvidence.bytes > 0 {
		claudeInfo.BillableOutputBytes += writtenBillableEvidence.bytes
		_, _ = claudeInfo.BillableOutputText.WriteString(writtenBillableEvidence.text)
	}
	claudeInfo.APIKeyOutputSuffix = nextAPIKeyOutputSuffix
	return nil
}

func cloneClaudeUsage(usage *dto.Usage) *dto.Usage {
	if usage == nil {
		return &dto.Usage{}
	}
	clone := *usage
	if usage.InputTokensDetails != nil {
		details := *usage.InputTokensDetails
		clone.InputTokensDetails = &details
	}
	return &clone
}

type claudeResponseInfoSnapshot struct {
	responseID                     string
	model                          string
	responseText                   string
	usage                          *dto.Usage
	done                           bool
	sawMessageStart                bool
	sawMessageStop                 bool
	sawValidInputUsage             bool
	sawTerminalUsage               bool
	sawTerminalEvent               bool
	upstreamBillableOutputText     string
	upstreamBillableOutputBytes    int64
	billableOutputText             string
	billableOutputBytes            int64
	failedReportedCompletionTokens int
	apiKeyOutputSuffix             string
	hasToolCallIndexesSnapshot     bool
	toolCallIndexes                map[int]int
	nextToolCallIndex              int
}

func snapshotClaudeResponseInfo(info *ClaudeResponseInfo, snapshotToolCallIndexes bool) claudeResponseInfoSnapshot {
	if info == nil {
		return claudeResponseInfoSnapshot{}
	}
	var toolCallIndexes map[int]int
	if snapshotToolCallIndexes && info.toolCallIndexes != nil {
		toolCallIndexes = make(map[int]int, len(info.toolCallIndexes))
		for contentBlockIndex, toolCallIndex := range info.toolCallIndexes {
			toolCallIndexes[contentBlockIndex] = toolCallIndex
		}
	}
	return claudeResponseInfoSnapshot{
		responseID: info.ResponseId, model: info.Model,
		responseText: info.ResponseText.String(), usage: cloneClaudeUsage(info.Usage),
		done: info.Done, sawMessageStart: info.SawMessageStart, sawMessageStop: info.SawMessageStop,
		sawValidInputUsage: info.SawValidInputUsage, sawTerminalUsage: info.SawTerminalUsage,
		sawTerminalEvent:            info.SawTerminalEvent,
		upstreamBillableOutputText:  info.UpstreamBillableOutputText.String(),
		upstreamBillableOutputBytes: info.UpstreamBillableOutputBytes,
		billableOutputText:          info.BillableOutputText.String(), billableOutputBytes: info.BillableOutputBytes,
		failedReportedCompletionTokens: info.FailedReportedCompletionTokens,
		apiKeyOutputSuffix:             info.APIKeyOutputSuffix,
		hasToolCallIndexesSnapshot:     snapshotToolCallIndexes,
		toolCallIndexes:                toolCallIndexes,
		nextToolCallIndex:              info.nextToolCallIndex,
	}
}

func restoreClaudeResponseInfo(info *ClaudeResponseInfo, snapshot claudeResponseInfoSnapshot) {
	if info == nil {
		return
	}
	info.ResponseId = snapshot.responseID
	info.Model = snapshot.model
	info.ResponseText.Reset()
	_, _ = info.ResponseText.WriteString(snapshot.responseText)
	info.Usage = snapshot.usage
	info.Done = snapshot.done
	info.SawMessageStart = snapshot.sawMessageStart
	info.SawMessageStop = snapshot.sawMessageStop
	info.SawValidInputUsage = snapshot.sawValidInputUsage
	info.SawTerminalUsage = snapshot.sawTerminalUsage
	info.SawTerminalEvent = snapshot.sawTerminalEvent
	info.UpstreamBillableOutputText.Reset()
	_, _ = info.UpstreamBillableOutputText.WriteString(snapshot.upstreamBillableOutputText)
	info.UpstreamBillableOutputBytes = snapshot.upstreamBillableOutputBytes
	info.BillableOutputText.Reset()
	_, _ = info.BillableOutputText.WriteString(snapshot.billableOutputText)
	info.BillableOutputBytes = snapshot.billableOutputBytes
	info.FailedReportedCompletionTokens = snapshot.failedReportedCompletionTokens
	info.APIKeyOutputSuffix = snapshot.apiKeyOutputSuffix
	if snapshot.hasToolCallIndexesSnapshot {
		info.toolCallIndexes = snapshot.toolCallIndexes
		info.nextToolCallIndex = snapshot.nextToolCallIndex
	}
}

func finalizeClaudeStreamUsage(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo) {
	if claudeInfo == nil {
		return
	}
	if claudeInfo.Usage == nil {
		claudeInfo.Usage = &dto.Usage{}
	}
	if claudeInfo.Usage.CompletionTokens == 0 || !claudeInfo.Done {
		if common.DebugEnabled {
			common.SysLog("claude response usage is not complete, maybe upstream error")
		}
		// 只补缺失字段，不整份覆盖——保留 message_start 已拿到的 cache 字段。
		responseText := claudeInfo.ResponseText.String()
		minimumCompletionTokens := 0
		if isManagedClaudeRelay(info) {
			responseText = claudeInfo.BillableOutputText.String()
			if minimum, err := relaycommon.ClaudeEvidenceTokenFloor(claudeInfo.BillableOutputBytes); err == nil {
				platformMaxInt := int64(^uint(0) >> 1)
				if minimum > platformMaxInt {
					minimumCompletionTokens = int(platformMaxInt)
				} else {
					minimumCompletionTokens = int(minimum)
				}
			}
		}
		fallback := service.ResponseText2Usage(c, responseText, info.UpstreamModelName, info.GetEstimatePromptTokens())
		claudeInfo.Usage.CompletionTokens = max(
			claudeInfo.Usage.CompletionTokens,
			claudeInfo.FailedReportedCompletionTokens,
			fallback.CompletionTokens,
			minimumCompletionTokens,
		)
		if !hasRecordedClaudeInputUsage(claudeInfo.Usage) {
			claudeInfo.Usage.PromptTokens = fallback.PromptTokens
		}
	}
	// An interrupted stream has no trustworthy terminal usage. Keep the local
	// estimate inside the request's already-authorized liability instead of
	// allowing partial-settlement fallback to perform an unbounded post-charge.
	if info != nil && info.RequiresClaudeUsageAuthorization() &&
		(!claudeInfo.Done || !claudeInfo.SawTerminalUsage) {
		if claudeInfo.Usage.CompletionTokens > info.AuthorizedCompletionTokens {
			claudeInfo.Usage.CompletionTokens = info.AuthorizedCompletionTokens
		}
		cacheCreationTokens := cacheCreationTokensForOpenAIUsage(claudeInfo.Usage)
		cachedInputTokens := claudeInfo.Usage.PromptTokensDetails.CachedTokens + cacheCreationTokens
		uncachedLimit := info.AuthorizedPromptTokens - cachedInputTokens
		if uncachedLimit < 0 {
			uncachedLimit = 0
		}
		if claudeInfo.Usage.PromptTokens > uncachedLimit {
			claudeInfo.Usage.PromptTokens = uncachedLimit
		}
	}
	claudeInfo.Usage.TotalTokens = claudeInfo.Usage.PromptTokens + claudeInfo.Usage.CompletionTokens
	claudeInfo.Usage.UsageSemantic = "anthropic"
}

func hasRecordedClaudeInputUsage(usage *dto.Usage) bool {
	return usage != nil && (usage.PromptTokens > 0 ||
		usage.PromptTokensDetails.CachedTokens > 0 ||
		usage.PromptTokensDetails.CachedCreationTokens > 0 ||
		usage.ClaudeCacheCreation5mTokens > 0 || usage.ClaudeCacheCreation1hTokens > 0)
}

func HandleStreamFinalResponse(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("send final Claude stream response panic recovered: %v", recovered)
		}
	}()

	finalizeClaudeStreamUsage(c, info, claudeInfo)

	if info.RelayFormat == types.RelayFormatClaude {
		//
	} else if info.RelayFormat == types.RelayFormatOpenAI {
		if info.ShouldIncludeUsage {
			openAIUsage := buildOpenAIStyleUsageFromClaudeUsage(claudeInfo.Usage)
			response := helper.GenerateFinalUsageResponse(claudeInfo.ResponseId, claudeInfo.Created, info.UpstreamModelName, openAIUsage)
			err := helper.ObjectData(c, response)
			if err != nil {
				return err
			}
		}
		return helper.Done(c)
	}
	return nil
}

func ClaudeStreamHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	// Managed Claude routes remain retryable until real output is written, so a
	// keepalive must not become their first downstream byte.
	if info.ChannelMeta != nil && info.ChannelMeta.ManagedProvider {
		info.DelayPingUntilFirstWrite = true
		if info.MaxStreamResponseBytes <= 0 || info.MaxStreamResponseBytes > maxClaudeResponseBytes {
			info.MaxStreamResponseBytes = maxClaudeResponseBytes
		}
	}
	if info.RelayFormat == types.RelayFormatOpenAI {
		// The OpenAI converter keeps native thinking_delta out of content.
		c.Header(common.ReasoningContentSeparatedHeader, "true")
	}
	claudeInfo := &ClaudeResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}
	var err *types.NewAPIError
	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		err = HandleStreamResponseData(c, info, claudeInfo, data)
		if err != nil {
			sr.Stop(err)
			return
		}
		if info.RelayFormat == types.RelayFormatOpenAI && claudeInfo.Done &&
			(!isManagedClaudeRelay(info) || claudeInfo.SawTerminalUsage) {
			sr.Done()
			return
		}
		if claudeInfo.SawMessageStop {
			sr.Done()
		}
	})
	if err != nil {
		return failedClaudeStreamUsage(c, info, claudeInfo), err
	}
	if completionErr := validateClaudeStreamCompletion(info, claudeInfo); completionErr != nil {
		return failedClaudeStreamUsage(c, info, claudeInfo), completionErr
	}

	if finalErr := HandleStreamFinalResponse(c, info, claudeInfo); finalErr != nil {
		return failedClaudeStreamUsage(c, info, claudeInfo), types.NewError(finalErr, types.ErrorCodeBadResponseBody)
	}
	return claudeInfo.Usage, nil
}

func failedClaudeStreamUsage(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo) *dto.Usage {
	if c == nil || c.Writer == nil || !c.Writer.Written() {
		return nil
	}
	finalizeClaudeStreamUsage(c, info, claudeInfo)
	return claudeInfo.Usage
}

func validateClaudeStreamCompletion(info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo) *types.NewAPIError {
	if info != nil && info.StreamStatus != nil {
		switch info.StreamStatus.EndReason {
		case relaycommon.StreamEndReasonScannerErr:
			if errors.Is(info.StreamStatus.EndError, helper.ErrStreamResponseTooLarge) {
				return types.NewErrorWithStatusCode(
					fmt.Errorf("Claude response exceeded %d MiB limit", maxClaudeResponseBytes>>20),
					types.ErrorCodeBadResponse,
					http.StatusBadGateway,
				)
			}
		case relaycommon.StreamEndReasonPanic, relaycommon.StreamEndReasonPingFail:
			cause := info.StreamStatus.EndError
			if cause == nil {
				cause = fmt.Errorf("Claude stream ended abnormally (%s)", info.StreamStatus.EndReason)
			}
			return types.NewErrorWithStatusCode(
				cause,
				types.ErrorCodeBadResponse,
				http.StatusBadGateway,
			)
		}
	}

	complete := claudeInfo != nil && claudeInfo.Done
	if info != nil && info.RelayFormat == types.RelayFormatClaude {
		complete = claudeInfo != nil && claudeInfo.SawMessageStop
	}
	if complete && isManagedClaudeRelay(info) &&
		(claudeInfo == nil || !claudeInfo.SawValidInputUsage || !claudeInfo.SawTerminalUsage) {
		return types.NewErrorWithStatusCode(
			fmt.Errorf("Claude stream did not include valid usage"),
			types.ErrorCodeBadResponse,
			http.StatusBadGateway,
		)
	}
	if complete {
		return nil
	}
	reason := relaycommon.StreamEndReasonNone
	if info != nil && info.StreamStatus != nil {
		reason = info.StreamStatus.EndReason
		if reason == relaycommon.StreamEndReasonClientGone {
			cause := info.StreamStatus.EndError
			if cause == nil {
				cause = context.Canceled
			}
			return types.NewErrorWithStatusCode(
				cause,
				types.ErrorCodeBadResponse,
				499,
				types.ErrOptionWithSkipRetry(),
				types.ErrOptionWithNoRecordErrorLog(),
			)
		}
	}
	return types.NewErrorWithStatusCode(
		fmt.Errorf("Claude 流在完成事件前中断（%s）", reason),
		types.ErrorCodeBadResponse,
		http.StatusBadGateway,
	)
}

func recordClaudeWebSearchRequests(c *gin.Context, usage *dto.ClaudeUsage) {
	if c == nil || usage == nil || usage.ServerToolUse == nil {
		return
	}
	requests := usage.ServerToolUse.WebSearchRequests
	if requests > c.GetInt("claude_web_search_requests") {
		c.Set("claude_web_search_requests", requests)
	}
}

func HandleClaudeResponseData(c *gin.Context, info *relaycommon.RelayInfo, claudeInfo *ClaudeResponseInfo, httpResp *http.Response, data []byte) *types.NewAPIError {
	if err := validateManagedClaudeResponseBytes(info, data); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	var claudeResponse dto.ClaudeResponse
	err := common.Unmarshal(data, &claudeResponse)
	if err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if claudeError := claudeResponse.GetClaudeError(); claudeError != nil {
		return types.WithClaudeError(service.SanitizeClaudeError(c, *claudeError), http.StatusInternalServerError)
	}
	if err = validateClaudeResponseToolPricing(info, &claudeResponse); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if err = validateClaudeResponseModelAuthorization(info, claudeResponse.Model); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if err = validateClaudeUsageAuthorization(info, claudeResponse.Usage); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if err = validateClaudeInputEvidenceUsage(info, claudeResponse.Usage); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if isManagedClaudeRelay(info) &&
		(!hasValidClaudeInputUsage(claudeResponse.Usage) ||
			!hasValidClaudeTerminalUsage(claudeResponse.Usage) ||
			!validClaudeStopReason(claudeResponse.StopReason)) {
		return types.NewError(fmt.Errorf("Claude response did not include valid usage"), types.ErrorCodeBadResponseBody)
	}
	var billableEvidence claudeBillableEvidence
	if isManagedClaudeRelay(info) {
		billableEvidence, err = claudeNonStreamBillableEvidence(&claudeResponse)
		if err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		credentialFragments, fragmentErr := claudeNonStreamCredentialFragments(&claudeResponse, info.RelayFormat)
		if fragmentErr != nil {
			return types.NewError(fragmentErr, types.ErrorCodeBadResponseBody)
		}
		if _, leaksAPIKey := advanceClaudeAPIKeySuffix(
			"", managedClaudeAPIKey(info), credentialFragments,
		); leaksAPIKey {
			return types.NewError(
				errors.New("managed Claude response contained credential material"),
				types.ErrorCodeBadResponseBody,
			)
		}
		if err = validateClaudeOutputEvidenceUsage(billableEvidence.bytes, claudeResponse.Usage.OutputTokens); err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
	}
	if claudeInfo.Usage == nil {
		claudeInfo.Usage = &dto.Usage{}
	}
	if claudeResponse.Usage != nil {
		claudeInfo.Usage.PromptTokens = claudeResponse.Usage.InputTokens
		claudeInfo.Usage.CompletionTokens = claudeResponse.Usage.OutputTokens
		claudeInfo.Usage.TotalTokens = claudeResponse.Usage.InputTokens + claudeResponse.Usage.OutputTokens
		claudeInfo.Usage.UsageSemantic = "anthropic"
		claudeInfo.Usage.PromptTokensDetails.CachedTokens = claudeResponse.Usage.CacheReadInputTokens
		claudeInfo.Usage.PromptTokensDetails.CachedCreationTokens = claudeResponse.Usage.CacheCreationInputTokens
		claudeInfo.Usage.ClaudeCacheCreation5mTokens = claudeResponse.Usage.GetCacheCreation5mTokens()
		claudeInfo.Usage.ClaudeCacheCreation1hTokens = claudeResponse.Usage.GetCacheCreation1hTokens()
	}
	var responseData []byte
	switch info.RelayFormat {
	case types.RelayFormatOpenAI:
		openaiResponse := ResponseClaude2OpenAI(&claudeResponse)
		openaiResponse.Usage = buildOpenAIStyleUsageFromClaudeUsage(claudeInfo.Usage)
		responseData, err = common.Marshal(openaiResponse)
		if err != nil {
			return types.NewError(err, types.ErrorCodeBadResponseBody)
		}
	case types.RelayFormatClaude:
		responseData = data
	}
	if err = validateManagedClaudeResponseBytes(info, responseData); err != nil {
		return types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	if isManagedClaudeRelay(info) {
		claudeInfo.BillableOutputBytes = billableEvidence.bytes
		_, _ = claudeInfo.BillableOutputText.WriteString(billableEvidence.text)
	}

	maybeMarkClaudeRefusal(c, claudeResponse.StopReason)
	recordClaudeWebSearchRequests(c, claudeResponse.Usage)

	if info.UsesAtomicStrictBilling() && !info.IsStream {
		service.StageResponseBytes(c, httpResp, responseData)
	} else {
		service.IOCopyBytesGracefully(c, httpResp, responseData)
	}
	return nil
}

func ClaudeHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	claudeInfo := &ClaudeResponseInfo{
		ResponseId:   helper.GetResponseID(c),
		Created:      common.GetTimestamp(),
		Model:        info.UpstreamModelName,
		ResponseText: strings.Builder{},
		Usage:        &dto.Usage{},
	}
	responseBody, err := readClaudeResponseBody(resp.Body)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
	}
	handleErr := HandleClaudeResponseData(c, info, claudeInfo, resp, responseBody)
	if handleErr != nil {
		return nil, handleErr
	}
	return claudeInfo.Usage, nil
}

func readClaudeResponseBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxClaudeResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxClaudeResponseBytes {
		return nil, fmt.Errorf("Claude 响应超过 %d MiB 限制", maxClaudeResponseBytes>>20)
	}
	return body, nil
}

func mapToolChoice(toolChoice any, parallelToolCalls *bool) *dto.ClaudeToolChoice {
	var claudeToolChoice *dto.ClaudeToolChoice

	// 处理 tool_choice 字符串值
	if toolChoiceStr, ok := toolChoice.(string); ok {
		switch toolChoiceStr {
		case "auto":
			claudeToolChoice = &dto.ClaudeToolChoice{
				Type: "auto",
			}
		case "required":
			claudeToolChoice = &dto.ClaudeToolChoice{
				Type: "any",
			}
		case "none":
			claudeToolChoice = &dto.ClaudeToolChoice{
				Type: "none",
			}
		}
	} else if toolChoiceMap, ok := toolChoice.(map[string]interface{}); ok {
		// 处理 tool_choice 对象值
		if function, ok := toolChoiceMap["function"].(map[string]interface{}); ok {
			if toolName, ok := function["name"].(string); ok {
				claudeToolChoice = &dto.ClaudeToolChoice{
					Type: "tool",
					Name: toolName,
				}
			}
		}
	}

	// 处理 parallel_tool_calls
	if parallelToolCalls != nil {
		if claudeToolChoice == nil {
			// 如果没有 tool_choice，但有 parallel_tool_calls，创建默认的 auto 类型
			claudeToolChoice = &dto.ClaudeToolChoice{
				Type: "auto",
			}
		}

		// Anthropic schema: tool_choice.type=none does not accept extra fields.
		// When tools are disabled, parallel_tool_calls is irrelevant, so we drop it.
		if claudeToolChoice.Type != "none" {
			// 如果 parallel_tool_calls 为 true，则 disable_parallel_tool_use 为 false
			claudeToolChoice.DisableParallelToolUse = !*parallelToolCalls
		}
	}

	return claudeToolChoice
}
