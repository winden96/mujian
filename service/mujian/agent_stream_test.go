package mujian

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/stretchr/testify/require"
)

func streamData(t *testing.T, value interface{}) string {
	t.Helper()
	data, err := common.Marshal(value)
	require.NoError(t, err)
	return "data: " + string(data) + "\n\n"
}

func discardAgentDelta(string) error { return nil }

func discardAgentEvent(AgentStreamEvent) error { return nil }

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func enableChatModel(t *testing.T, modelID string) {
	t.Helper()
	channel := model.Channel{Name: "agent-provider-" + modelID, Key: "provider-key", Status: 1}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: modelID, UpstreamModelID: modelID,
		Provider: "test", BillingType: model.ChannelModelBillingToken,
		InputPrice: 0.1, OutputPrice: 0.2, Currency: "USD", Available: true,
	}).Error)
}

func TestValidateEnabledSkillUsesDirectSkillValue(t *testing.T) {
	preference := &model.MujianUserPreference{EnabledSkills: `["短剧编剧","画面提示词"]`}

	require.NoError(t, validateEnabledSkill(preference, "短剧编剧"))
	require.EqualError(t, validateEnabledSkill(preference, "分镜导演"), "Skill 分镜导演 未启用")
	require.EqualError(t, validateEnabledSkill(preference, "自定义 Skill"), "Skill 无效")
}

func TestAgentPromptAndPayloadArePureTextOnly(t *testing.T) {
	workspace := &Workspace{
		Project: model.MujianProject{ID: "project-1", Title: "项目标题"},
		Scenes:  []model.MujianScene{{ID: "legacy-scene", Title: "不应进入 prompt"}},
		Shots:   []model.MujianShot{{ID: "legacy-shot", Prompt: "不应进入 prompt"}},
	}
	turn := &agentTurn{
		ModelID: "gpt-5.6-terra", Skill: "短剧编剧",
		Workspace: workspace, Content: "生成第一集",
	}

	prompt, err := agentSystemPrompt(turn)
	require.NoError(t, err)
	require.Contains(t, prompt, "只返回文本建议或创作内容")
	require.Contains(t, prompt, "不得调用工具")
	require.NotContains(t, prompt, "legacy-scene")
	require.NotContains(t, prompt, "legacy-shot")
	require.NotContains(t, prompt, "propose_")

	payload, err := agentPayload(turn)
	require.NoError(t, err)
	for _, key := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
		require.NotContains(t, payload, key)
	}
}

func TestStoryboardSkillUsesTheSamePureTextProtocol(t *testing.T) {
	turn := &agentTurn{
		ModelID: "gpt-5.6-terra", Skill: "分镜导演",
		Workspace: &Workspace{Project: model.MujianProject{ID: "project-1", Title: "项目标题"}},
		Content:   "请拆解分镜",
	}

	prompt, err := agentSystemPrompt(turn)
	require.NoError(t, err)
	require.Contains(t, prompt, "当前专业 Skill：分镜导演")
	require.Contains(t, prompt, "只返回文本建议或创作内容")
	require.NotContains(t, prompt, "MUJIAN_SEEDANCE")
	require.NotContains(t, prompt, `"shots"`)
	require.NotContains(t, prompt, "Seedance 2.0")

	payload, err := agentPayload(turn)
	require.NoError(t, err)
	require.Equal(t, agentDefaultMaxTokens, payload["max_tokens"])
	for _, key := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
		require.NotContains(t, payload, key)
	}
}

func TestAgentPayloadKeepsOnlyCompleteSanitizedHistory(t *testing.T) {
	reasoning := "私密推理"
	workspace := &Workspace{Messages: []model.MujianAgentMessage{
		{Role: "user", Content: "历史用户", Proposal: `{"secret":true}`, Reasoning: &reasoning},
		{Role: "assistant", Content: "历史助手", PreviousValues: `{"secret":true}`},
		{Role: "user", Content: "残缺轮次"},
	}}
	payload, err := agentPayload(&agentTurn{
		ModelID: "gpt-5.6-terra", Skill: "短剧编剧", Workspace: workspace, Content: "当前问题",
	})
	require.NoError(t, err)
	messages, ok := payload["messages"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, messages, 4)
	require.Equal(t, "历史用户", messages[1]["content"])
	require.Equal(t, "历史助手", messages[2]["content"])
	require.Equal(t, "当前问题", messages[3]["content"])
	encoded, err := common.Marshal(messages)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "私密推理")
	require.NotContains(t, string(encoded), "secret")
	require.NotContains(t, string(encoded), "残缺轮次")
}

func TestAgentPayloadReasoningFallbackRemovesOnlyProviderControls(t *testing.T) {
	payload, err := agentPayload(&agentTurn{
		ModelID: "deepseek-v4-flash", Skill: "短剧编剧", Workspace: &Workspace{}, Content: "构思剧本",
	})
	require.NoError(t, err)
	require.Equal(t, agentReasoningEffortHigh, payload["reasoning_effort"])
	require.Equal(t, agentReasoningMaxTokens, payload["max_tokens"])

	fallback := withoutReasoningParameters(payload, mujianprovider.ReasoningProtocolOpenAIEffort)
	require.NotContains(t, fallback, "reasoning_effort")
	require.Equal(t, agentDefaultMaxTokens, fallback["max_tokens"])
	require.Equal(t, agentDefaultTemperature, fallback["temperature"])
	require.NotContains(t, fallback, "tools")
}

func TestConsumeAgentStreamReturnsOnlyMarkedTextAndIgnoresToolCalls(t *testing.T) {
	stream := streamData(t, map[string]interface{}{
		"choices": []interface{}{map[string]interface{}{"delta": map[string]interface{}{
			"reasoning_content": "先分析。",
			"content":           "不可见分析" + finalResponseMarker + "最终正文",
			"tool_calls": []interface{}{map[string]interface{}{
				"index": 0, "function": map[string]string{"name": "legacy_tool", "arguments": `{}`},
			}},
		}}},
	}) + "data: [DONE]\n\n"
	var visible []string

	reply, reasoning, usage, err := consumeAgentStream(strings.NewReader(stream), false, discardAgentDelta, func(delta string) error {
		visible = append(visible, delta)
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, "最终正文", reply)
	require.Equal(t, "先分析。", reasoning)
	require.Empty(t, usage)
	require.Equal(t, []string{"最终正文"}, visible)
}

func TestConsumeAgentStreamDoesNotCorruptChineseDeltas(t *testing.T) {
	text := "好的，我来为你创作张磊大战屎壳郎的故事。第一集大纲，场景1清晨垃圾站。"
	stream := streamData(t, map[string]interface{}{
		"choices": []interface{}{map[string]interface{}{"delta": map[string]string{
			"content": finalResponseMarker + text,
		}}},
	}) + "data: [DONE]\n\n"

	var emitted []string
	reply, _, _, err := consumeAgentStream(strings.NewReader(stream), false, discardAgentDelta, func(delta string) error {
		payload, marshalErr := common.Marshal(map[string]string{"text": delta})
		require.NoError(t, marshalErr)
		require.NotContains(t, string(payload), `\ufffd`)
		emitted = append(emitted, delta)
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, text, reply)
	require.Equal(t, text, strings.Join(emitted, ""))
}

func TestConsumeAgentStreamAllowsUnmarkedSeparatedFinal(t *testing.T) {
	stream := streamData(t, map[string]interface{}{
		"choices": []interface{}{map[string]interface{}{"delta": map[string]string{
			"reasoning_content": "先分析创作方向。",
			"content":           "第一段正文。",
		}}},
	}) + streamData(t, map[string]interface{}{
		"choices": []interface{}{map[string]interface{}{"delta": map[string]string{
			"content": "第二段正文。",
		}}},
	}) + "data: [DONE]\n\n"
	var visible []string

	reply, reasoning, _, err := consumeAgentStream(strings.NewReader(stream), true, discardAgentDelta, func(delta string) error {
		visible = append(visible, delta)
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, "第一段正文。第二段正文。", reply)
	require.Equal(t, "先分析创作方向。", reasoning)
	require.Equal(t, []string{"第一段正文。第二段正文。"}, visible)
}

func TestConsumeAgentStreamKeepsMarkedIsolationWhenUnmarkedFinalIsAllowed(t *testing.T) {
	stream := streamData(t, map[string]interface{}{
		"choices": []interface{}{map[string]interface{}{"delta": map[string]string{
			"content": "不可见文本" + finalResponseMarker[:8],
		}}},
	}) + streamData(t, map[string]interface{}{
		"choices": []interface{}{map[string]interface{}{"delta": map[string]string{
			"content": finalResponseMarker[8:] + "最终正文",
		}}},
	}) + "data: [DONE]\n\n"

	reply, _, _, err := consumeAgentStream(strings.NewReader(stream), true, discardAgentDelta, discardAgentDelta)

	require.NoError(t, err)
	require.Equal(t, "最终正文", reply)
}

func TestConsumeAgentStreamUsesOnlyChoiceZero(t *testing.T) {
	stream := streamData(t, map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{"index": 0, "delta": map[string]string{"content": finalResponseMarker + "主答案"}},
			map[string]interface{}{"index": 1, "delta": map[string]string{"content": "候选答案不应串入"}},
		},
	}) + "data: [DONE]\n\n"

	reply, _, _, err := consumeAgentStream(strings.NewReader(stream), false, discardAgentDelta, discardAgentDelta)

	require.NoError(t, err)
	require.Equal(t, "主答案", reply)
}

func TestConsumeAgentStreamWaitsForRelayEOF(t *testing.T) {
	reader, writer := io.Pipe()
	release := make(chan struct{})
	wroteDone := make(chan struct{})
	chunk := streamData(t, map[string]interface{}{
		"choices": []interface{}{map[string]interface{}{"delta": map[string]string{"content": finalResponseMarker + "已完成"}}},
	})
	go func() {
		_, _ = io.WriteString(writer, chunk+"data: [DONE]\n\n")
		close(wroteDone)
		<-release
		_ = writer.Close()
	}()
	consumed := make(chan error, 1)
	go func() {
		_, _, _, err := consumeAgentStream(reader, false, discardAgentDelta, discardAgentDelta)
		consumed <- err
	}()
	<-wroteDone
	select {
	case err := <-consumed:
		close(release)
		require.Failf(t, "stream returned before EOF", "error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-consumed)
}

func TestConsumeAgentStreamRejectsIncompleteOrUnmarkedText(t *testing.T) {
	t.Run("missing done", func(t *testing.T) {
		stream := streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]string{"content": finalResponseMarker + "未完成"}}},
		})
		_, _, _, err := consumeAgentStream(strings.NewReader(stream), false, discardAgentDelta, discardAgentDelta)
		require.EqualError(t, err, "Agent 流式响应意外结束")
	})
	t.Run("missing marker", func(t *testing.T) {
		stream := streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]string{"content": "内部文本"}}},
		}) + "data: [DONE]\n\n"
		_, _, _, err := consumeAgentStream(strings.NewReader(stream), false, discardAgentDelta, discardAgentDelta)
		require.EqualError(t, err, "Agent 流式响应缺少正文标记")
	})
	t.Run("length", func(t *testing.T) {
		stream := streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{
				"delta": map[string]string{"content": finalResponseMarker + "截断"}, "finish_reason": constant.FinishReasonLength,
			}},
		}) + "data: [DONE]\n\n"
		_, _, _, err := consumeAgentStream(strings.NewReader(stream), true, discardAgentDelta, discardAgentDelta)
		require.Contains(t, err.Error(), "超过长度限制")
	})
	t.Run("reasoning only", func(t *testing.T) {
		stream := streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]string{"reasoning_content": "只有分析"}}},
		}) + "data: [DONE]\n\n"
		_, _, _, err := consumeAgentStream(strings.NewReader(stream), true, discardAgentDelta, discardAgentDelta)
		require.EqualError(t, err, "Agent 回复内容为空")
	})
}

func TestPersistAgentTurnNeverStoresProposal(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "plain-persist-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	session, err := agentSessionFromWorkspace(workspace)
	require.NoError(t, err)
	turn := &agentTurn{
		UserID: user.Id, ProjectID: project.ID, SessionID: session.ID, Revision: session.Revision,
		Skill: "短剧编剧",
		UserMessage: model.MujianAgentMessage{
			ID: "plain-user", ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
			Role: "user", Content: "请给建议", Skill: "短剧编剧", Mode: AgentModeConsult, ApplyStatus: "none",
		},
	}

	result, err := persistAgentTurn(turn, `{"reply":"这是普通文本","proposal":{"summary":"不解析"}}`, nil, RelayUsage{}, "request-1")

	require.NoError(t, err)
	require.Equal(t, AgentModeConsult, result.Message.Mode)
	require.Empty(t, result.Message.Proposal)
	require.Empty(t, result.Message.PreviousValues)
	require.Equal(t, "none", result.Message.ApplyStatus)
	var stored model.MujianAgentMessage
	require.NoError(t, model.DB.First(&stored, "id = ?", result.Message.ID).Error)
	require.Empty(t, stored.Proposal)
	require.Equal(t, "none", stored.ApplyStatus)
}

func TestStreamAgentMessageUsesSkillAndPersistsPlainText(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "plain-stream-owner")
	project := createEmptyTestProject(t, user.Id)
	_, err := UpdateSkills(user.Id, []string{"短剧编剧", "分镜导演"})
	require.NoError(t, err)
	require.NoError(t, model.DB.Exec(`CREATE TABLE mujian_seedance_shots (id text primary key, project_id text)`).Error)
	require.NoError(t, model.DB.Exec(`INSERT INTO mujian_seedance_shots (id, project_id) VALUES (?, ?)`, "legacy-board", project.ID).Error)
	enableChatModel(t, "deepseek-v4-flash")
	requestPayload := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]interface{}
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		requestPayload <- payload
		writer.Header().Set(common.RequestIdKey, "plain-stream-request")
		_, _ = fmt.Fprint(writer, streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]string{"content": finalResponseMarker + "纯文本回复"}}},
		}), "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", server.URL)

	var events []AgentStreamEvent
	err = StreamAgentMessage(context.Background(), user.Id, project.ID, "请创作", "分镜导演", "deepseek-v4-flash", nil, func(event AgentStreamEvent) error {
		events = append(events, event)
		return nil
	})

	require.NoError(t, err)
	payload := <-requestPayload
	for _, key := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
		require.NotContains(t, payload, key)
	}
	messages, ok := payload["messages"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, messages)
	systemMessage, ok := messages[0].(map[string]interface{})
	require.True(t, ok)
	systemPrompt, ok := systemMessage["content"].(string)
	require.True(t, ok)
	require.NotContains(t, systemPrompt, "MUJIAN_SEEDANCE")
	require.NotContains(t, systemPrompt, `"shots"`)
	require.Equal(t, []string{"started", "delta", "completed"}, []string{events[0].Type, events[1].Type, events[2].Type})
	started := events[0].Data.(map[string]interface{})
	require.Equal(t, "分镜导演", started["skill"])
	result := events[2].Data.(*RelayResult)
	require.Equal(t, "纯文本回复", result.Message.Content)
	require.Empty(t, result.Message.Proposal)
	require.Equal(t, "none", result.Message.ApplyStatus)
	completedJSON, err := common.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(completedJSON), "seedance")
	var legacyRows int64
	require.NoError(t, model.DB.Raw(`SELECT count(*) FROM mujian_seedance_shots`).Scan(&legacyRows).Error)
	require.Equal(t, int64(1), legacyRows)
}

func TestStreamAgentMessagePersistsUnmarkedClaudeAdaptiveFinal(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "claude-unmarked-owner")
	project := createEmptyTestProject(t, user.Id)
	_, err := UpdateSkills(user.Id, []string{"短剧编剧"})
	require.NoError(t, err)
	enableChatModel(t, "claude-opus-5")
	requestPayload := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]interface{}
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		requestPayload <- payload
		writer.Header().Set(common.RequestIdKey, "claude-unmarked-request")
		writer.Header().Set(common.ReasoningContentSeparatedHeader, "true")
		_, _ = fmt.Fprint(writer, streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]string{
				"reasoning_content": "分析创作要求",
				"content":           "人物小传和分集大纲",
			}}},
		}), "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", server.URL)

	var events []AgentStreamEvent
	err = StreamAgentMessage(context.Background(), user.Id, project.ID, "继续创作", "短剧编剧", "claude-opus-5", nil, func(event AgentStreamEvent) error {
		events = append(events, event)
		return nil
	})

	require.NoError(t, err)
	payload := <-requestPayload
	require.Contains(t, payload, "thinking")
	require.Contains(t, payload, "output_config")
	require.Equal(t, []string{"started", "reasoning_delta", "delta", "completed"}, []string{events[0].Type, events[1].Type, events[2].Type, events[3].Type})
	result := events[3].Data.(*RelayResult)
	require.Equal(t, "人物小传和分集大纲", result.Message.Content)
	require.NotNil(t, result.Message.Reasoning)
	require.Equal(t, "分析创作要求", *result.Message.Reasoning)
	var stored model.MujianAgentMessage
	require.NoError(t, model.DB.First(&stored, "id = ?", result.Message.ID).Error)
	require.Equal(t, result.Message.Content, stored.Content)
	require.Equal(t, result.Message.Reasoning, stored.Reasoning)
}

func TestStreamAgentMessageClaudeAdaptiveRejectsUnattestedUnmarkedFinal(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "claude-unattested-owner")
	project := createEmptyTestProject(t, user.Id)
	_, err := UpdateSkills(user.Id, []string{"短剧编剧"})
	require.NoError(t, err)
	enableChatModel(t, "claude-opus-5")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprint(writer, streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]string{
				"reasoning_content": "声称分离的分析",
				"content":           "没有通道证明的无标记文本",
			}}},
		}), "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", server.URL)

	err = StreamAgentMessage(context.Background(), user.Id, project.ID, "继续创作", "短剧编剧", "claude-opus-5", nil, discardAgentEvent)

	require.EqualError(t, err, "Agent 流式响应缺少正文标记")
	var messageCount int64
	require.NoError(t, model.DB.Model(&model.MujianAgentMessage{}).Where("project_id = ?", project.ID).Count(&messageCount).Error)
	require.Zero(t, messageCount)
}

func TestStreamAgentMessageNonClaudeReasoningCannotUseClaudeAttestation(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "non-claude-attested-owner")
	project := createEmptyTestProject(t, user.Id)
	_, err := UpdateSkills(user.Id, []string{"短剧编剧"})
	require.NoError(t, err)
	enableChatModel(t, "deepseek-v4-flash")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set(common.ReasoningContentSeparatedHeader, "true")
		_, _ = fmt.Fprint(writer, streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]string{
				"content": "其他推理协议的无标记文本",
			}}},
		}), "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", server.URL)

	err = StreamAgentMessage(context.Background(), user.Id, project.ID, "继续创作", "短剧编剧", "deepseek-v4-flash", nil, discardAgentEvent)

	require.EqualError(t, err, "Agent 流式响应缺少正文标记")
	var messageCount int64
	require.NoError(t, model.DB.Model(&model.MujianAgentMessage{}).Where("project_id = ?", project.ID).Count(&messageCount).Error)
	require.Zero(t, messageCount)
}

func TestStreamAgentMessageClaudeFallbackStillRequiresMarker(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "claude-fallback-owner")
	project := createEmptyTestProject(t, user.Id)
	_, err := UpdateSkills(user.Id, []string{"短剧编剧"})
	require.NoError(t, err)
	enableChatModel(t, "claude-opus-5")
	var requestCount atomic.Int32
	requestPayloads := make(chan map[string]interface{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]interface{}
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		requestPayloads <- payload
		if requestCount.Add(1) == 1 {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(writer, `{"error":{"message":"unsupported parameter: thinking","param":"thinking"}}`)
			return
		}
		_, _ = fmt.Fprint(writer, streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]string{
				"content": "降级后的无标记文本",
			}}},
		}), "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", server.URL)

	err = StreamAgentMessage(context.Background(), user.Id, project.ID, "继续创作", "短剧编剧", "claude-opus-5", nil, discardAgentEvent)

	require.EqualError(t, err, "Agent 流式响应缺少正文标记")
	firstPayload := <-requestPayloads
	secondPayload := <-requestPayloads
	require.Contains(t, firstPayload, "thinking")
	require.Contains(t, firstPayload, "output_config")
	require.NotContains(t, secondPayload, "thinking")
	require.NotContains(t, secondPayload, "output_config")
	var messageCount int64
	require.NoError(t, model.DB.Model(&model.MujianAgentMessage{}).Where("project_id = ?", project.ID).Count(&messageCount).Error)
	require.Zero(t, messageCount)
}

func TestSendAgentMessageTreatsJSONLookingOutputAsPlainText(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "plain-nonstream-owner")
	project := createEmptyTestProject(t, user.Id)
	enableChatModel(t, "gpt-5.6-terra")
	output := `{"reply":"不应解析","proposal":{"summary":"也不应持久化"}}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]interface{}
		require.NoError(t, common.DecodeJson(request.Body, &payload))
		require.NotContains(t, payload, "tools")
		response, err := common.Marshal(map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"message": map[string]string{"content": output}}},
		})
		require.NoError(t, err)
		_, _ = writer.Write(response)
	}))
	defer server.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", server.URL)

	result, err := SendAgentMessage(user.Id, project.ID, "请回复", "短剧编剧", "gpt-5.6-terra")

	require.NoError(t, err)
	require.Equal(t, output, result.Message.Content)
	require.Empty(t, result.Message.Proposal)
	require.Equal(t, "none", result.Message.ApplyStatus)
}

func TestStreamAgentMessageRejectsPersistenceAfterSessionClear(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "stream-clear-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	enableChatModel(t, "deepseek-v4-flash")
	upstreamStarted := make(chan struct{})
	releaseUpstream := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(upstreamStarted)
		<-releaseUpstream
		_, _ = fmt.Fprint(writer, streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]string{"content": finalResponseMarker + "迟到回复"}}},
		}), "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", server.URL)

	result := make(chan error, 1)
	go func() {
		result <- StreamAgentMessage(context.Background(), user.Id, project.ID, "生成中会被清空", "短剧编剧", "deepseek-v4-flash", nil, func(AgentStreamEvent) error { return nil }, workspace.ActiveSessionID)
	}()
	select {
	case <-upstreamStarted:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "upstream did not start")
	}
	_, err = ClearAgentSession(user.Id, project.ID, workspace.ActiveSessionID)
	require.NoError(t, err)
	close(releaseUpstream)
	require.ErrorIs(t, <-result, ErrSessionChanged)
	var messageCount int64
	require.NoError(t, model.DB.Model(&model.MujianAgentMessage{}).Where("session_id = ?", workspace.ActiveSessionID).Count(&messageCount).Error)
	require.Zero(t, messageCount)
}

func TestStreamAgentMessageDoesNotPersistWhenClientStopsReceiving(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "stream-cancel-owner")
	project := createEmptyTestProject(t, user.Id)
	enableChatModel(t, "deepseek-v4-flash")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(writer, streamData(t, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"delta": map[string]string{
				"reasoning_content": "分析", "content": finalResponseMarker + "正文",
			}}},
		}), "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", server.URL)

	err := StreamAgentMessage(context.Background(), user.Id, project.ID, "请回复", "短剧编剧", "deepseek-v4-flash", nil, func(event AgentStreamEvent) error {
		if event.Type == "reasoning_delta" {
			return context.Canceled
		}
		return nil
	})

	require.ErrorIs(t, err, context.Canceled)
	var messageCount int64
	require.NoError(t, model.DB.Model(&model.MujianAgentMessage{}).Where("project_id = ?", project.ID).Count(&messageCount).Error)
	require.Zero(t, messageCount)
}
