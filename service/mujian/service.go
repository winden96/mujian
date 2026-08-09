package mujian

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	InitialQuota  = 8767123
	QuotaPerUSD   = 500000
	CreditsPerUSD = 73
)

var (
	ErrNotFound         = errors.New("资源不存在")
	ErrConflict         = errors.New("内容已被更新，请刷新后重试")
	ErrRelayUnavailable = errors.New("创作模型暂不可用，请联系管理员检查渠道与价格配置")
)

var DefaultChatModels = []string{
	"deepseek-v4-flash", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
	"claude-fable-5-nc", "claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", "grok-4.5",
}

var DefaultImageModels = []string{
	"nano-banana", "nano-banana-pro", "nano-banana-2", "gpt-image-2",
}

type Workspace struct {
	Project  model.MujianProject        `json:"project"`
	Scenes   []model.MujianScene        `json:"scenes"`
	Shots    []model.MujianShot         `json:"shots"`
	Messages []model.MujianAgentMessage `json:"messages"`
}

type AgentProposal struct {
	Summary         string            `json:"summary"`
	TargetType      string            `json:"target_type"`
	TargetID        string            `json:"target_id"`
	ExpectedVersion int               `json:"expected_version"`
	Changes         map[string]string `json:"changes"`
}

type undoState struct {
	Values         map[string]string `json:"values"`
	AppliedVersion int               `json:"applied_version"`
}

type RelayUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type RelayResult struct {
	Message        model.MujianAgentMessage `json:"message"`
	Usage          RelayUsage               `json:"usage"`
	RelayRequestID string                   `json:"relay_request_id"`
}

type ImageResult struct {
	Shot           model.MujianShot `json:"shot"`
	RelayRequestID string           `json:"relay_request_id"`
}

func chatModels() []string  { return envModels("MUJIAN_CHAT_MODELS", DefaultChatModels) }
func imageModels() []string { return envModels("MUJIAN_IMAGE_MODELS", DefaultImageModels) }

func CatalogModels() map[string][]string {
	enabled := model.GetEnabledModels()
	return map[string][]string{
		"chat":  intersectModels(chatModels(), enabled),
		"image": intersectModels(imageModels(), enabled),
	}
}

func envModels(name string, fallback []string) []string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parts := strings.Split(value, ",")
	models := make([]string, 0, len(parts))
	for _, part := range parts {
		if candidate := strings.TrimSpace(part); candidate != "" {
			models = append(models, candidate)
		}
	}
	return models
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func intersectModels(catalog, enabled []string) []string {
	available := make([]string, 0, len(catalog))
	for _, candidate := range catalog {
		_, _, priced := ratio_setting.GetModelRatioOrPrice(candidate)
		if contains(enabled, candidate) && priced {
			available = append(available, candidate)
		}
	}
	return available
}

func modelAvailable(kind, modelID string) bool {
	catalog := CatalogModels()
	return contains(catalog[kind], modelID)
}

func CreditsFromQuota(quota int) float64 {
	return float64(quota) * CreditsPerUSD / QuotaPerUSD
}

func EnsureOnboarded(userID int) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var preference model.MujianUserPreference
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&preference, "user_id = ?", userID).Error
		if err == nil && preference.Onboarded {
			return nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		if errors.Is(err, gorm.ErrRecordNotFound) {
			skills, marshalErr := common.Marshal([]string{"短剧编剧", "分镜导演", "画面提示词"})
			if marshalErr != nil {
				return marshalErr
			}
			preference = model.MujianUserPreference{
				UserID: userID, DefaultChatModel: DefaultChatModels[0], DefaultImageModel: DefaultImageModels[0],
				EnabledSkills: string(skills), Onboarded: true,
			}
			if err = tx.Create(&preference).Error; err != nil {
				return err
			}
		} else {
			preference.Onboarded = true
			if err = tx.Save(&preference).Error; err != nil {
				return err
			}
		}

		if err = tx.Model(&model.User{}).Where("id = ? AND quota < ?", userID, InitialQuota).Update("quota", InitialQuota).Error; err != nil {
			return err
		}

		var tokenCount int64
		if err = tx.Model(&model.Token{}).Where("user_id = ? AND name = ?", userID, model.MujianInternalTokenName).Count(&tokenCount).Error; err != nil {
			return err
		}
		if tokenCount == 0 {
			key, keyErr := common.GenerateKey()
			if keyErr != nil {
				return keyErr
			}
			allModels := append(append([]string{}, chatModels()...), imageModels()...)
			token := model.Token{
				UserId: userID, Key: key, Name: model.MujianInternalTokenName, Status: common.TokenStatusEnabled,
				CreatedTime: common.GetTimestamp(), AccessedTime: common.GetTimestamp(), ExpiredTime: -1,
				UnlimitedQuota: true, ModelLimitsEnabled: true, ModelLimits: strings.Join(allModels, ","),
			}
			if err = tx.Create(&token).Error; err != nil {
				return err
			}
		}

		var projectCount int64
		if err = tx.Model(&model.MujianProject{}).Where("user_id = ?", userID).Count(&projectCount).Error; err != nil {
			return err
		}
		if projectCount == 0 {
			return seedProjects(tx, userID)
		}
		return nil
	})
}

func seedProjects(tx *gorm.DB, userID int) error {
	demos := []struct{ title, synopsis string }{
		{"雨夜便利店", "一个落魄青年在雨夜便利店里，遇见改变命运的陌生人。"},
		{"最后一班地铁", "末班车驶入不存在的站台，乘客必须在黎明前找出出口。"},
		{"旧相机里的夏天", "一卷未冲洗的胶片，让多年后的朋友重新面对青春的遗憾。"},
	}
	for index, demo := range demos {
		projectID := uuid.NewString()
		project := model.MujianProject{ID: projectID, UserID: userID, Title: demo.title, Type: "短剧", Synopsis: demo.synopsis, CurrentEpisode: 1, Progress: 25 + index*15}
		if err := tx.Create(&project).Error; err != nil {
			return err
		}
		sceneID := uuid.NewString()
		scene := model.MujianScene{ID: sceneID, ProjectID: projectID, Episode: 1, SceneNumber: 1, Title: "雨夜 · 室内", Environment: "霓虹灯映在湿漉漉的玻璃上，店里只剩冰柜低鸣。", Action: "主人公推门进入，停在收银台前。", Dialogues: `[{"character":"林夏","line":"还有热咖啡吗？"},{"character":"店员","line":"最后一杯，刚好等你。"}]`, Version: 1}
		if err := tx.Create(&scene).Error; err != nil {
			return err
		}
		shotTypes := []string{"全景", "中景", "近景"}
		for shotIndex, shotType := range shotTypes {
			shot := model.MujianShot{ID: uuid.NewString(), ProjectID: projectID, SceneID: sceneID, Sequence: shotIndex + 1, ShotType: shotType, DurationSeconds: 3 + shotIndex, Prompt: fmt.Sprintf("%s，电影感雨夜便利店，人物情绪克制", shotType), AspectRatio: "9:16", Model: DefaultImageModels[0], Status: "draft", Version: 1}
			if err := tx.Create(&shot).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func ListProjects(userID int) ([]model.MujianProject, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return nil, err
	}
	var projects []model.MujianProject
	err := model.DB.Where("user_id = ?", userID).Order("updated_at DESC").Find(&projects).Error
	return projects, err
}

func CreateProject(userID int, title, synopsis string) (*model.MujianProject, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return nil, err
	}
	project := &model.MujianProject{ID: uuid.NewString(), UserID: userID, Title: strings.TrimSpace(title), Type: "短剧", Synopsis: strings.TrimSpace(synopsis), CurrentEpisode: 1}
	if project.Title == "" {
		return nil, errors.New("项目名称不能为空")
	}
	return project, model.DB.Create(project).Error
}

func GetProject(userID int, projectID string) (*model.MujianProject, error) {
	var project model.MujianProject
	if err := model.DB.Where("id = ? AND user_id = ?", projectID, userID).First(&project).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &project, nil
}

func UpdateProject(userID int, projectID string, values map[string]interface{}) (*model.MujianProject, error) {
	project, err := GetProject(userID, projectID)
	if err != nil {
		return nil, err
	}
	allowed := map[string]interface{}{}
	for _, key := range []string{"title", "type", "synopsis", "current_episode", "progress", "cover_url"} {
		if value, ok := values[key]; ok {
			allowed[key] = value
		}
	}
	if len(allowed) > 0 {
		if err = model.DB.Model(project).Updates(allowed).Error; err != nil {
			return nil, err
		}
	}
	return GetProject(userID, projectID)
}

func DeleteProject(userID int, projectID string) error {
	project, err := GetProject(userID, projectID)
	if err != nil {
		return err
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err = tx.Where("project_id = ?", projectID).Delete(&model.MujianShot{}).Error; err != nil {
			return err
		}
		if err = tx.Where("project_id = ?", projectID).Delete(&model.MujianScene{}).Error; err != nil {
			return err
		}
		if err = tx.Where("project_id = ?", projectID).Delete(&model.MujianAgentMessage{}).Error; err != nil {
			return err
		}
		return tx.Delete(project).Error
	})
}

func GetWorkspace(userID int, projectID string) (*Workspace, error) {
	project, err := GetProject(userID, projectID)
	if err != nil {
		return nil, err
	}
	workspace := &Workspace{Project: *project}
	if err = model.DB.Where("project_id = ?", projectID).Order("episode, scene_number").Find(&workspace.Scenes).Error; err != nil {
		return nil, err
	}
	if err = model.DB.Where("project_id = ?", projectID).Order("sequence").Find(&workspace.Shots).Error; err != nil {
		return nil, err
	}
	if err = model.DB.Where("project_id = ? AND user_id = ?", projectID, userID).Order("created_at").Find(&workspace.Messages).Error; err != nil {
		return nil, err
	}
	return workspace, nil
}

func UpdateWorkspace(userID int, projectID string, scenes []model.MujianScene) (*Workspace, error) {
	if _, err := GetProject(userID, projectID); err != nil {
		return nil, err
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		for _, input := range scenes {
			var existing model.MujianScene
			if err := tx.Where("id = ? AND project_id = ?", input.ID, projectID).First(&existing).Error; err != nil {
				return ErrNotFound
			}
			result := tx.Model(&model.MujianScene{}).Where("id = ? AND project_id = ? AND version = ?", input.ID, projectID, input.Version).Updates(map[string]interface{}{
				"title": input.Title, "environment": input.Environment, "action": input.Action, "dialogues": input.Dialogues, "version": gorm.Expr("version + 1"),
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetWorkspace(userID, projectID)
}

func GetPreference(userID int) (*model.MujianUserPreference, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return nil, err
	}
	var preference model.MujianUserPreference
	return &preference, model.DB.First(&preference, "user_id = ?", userID).Error
}

func UpdatePreference(userID int, chatModel, imageModel string) (*model.MujianUserPreference, error) {
	preference, err := GetPreference(userID)
	if err != nil {
		return nil, err
	}
	updates := map[string]interface{}{}
	if chatModel != "" {
		if !modelAvailable("chat", chatModel) {
			return nil, errors.New("未登记的对话模型")
		}
		updates["default_chat_model"] = chatModel
	}
	if imageModel != "" {
		if !modelAvailable("image", imageModel) {
			return nil, errors.New("未登记的图像模型")
		}
		updates["default_image_model"] = imageModel
	}
	if len(updates) > 0 {
		if err = model.DB.Model(preference).Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	return GetPreference(userID)
}

func GetSkills(userID int) ([]string, error) {
	preference, err := GetPreference(userID)
	if err != nil {
		return nil, err
	}
	var skills []string
	if err = common.UnmarshalJsonStr(preference.EnabledSkills, &skills); err != nil {
		return nil, err
	}
	return skills, nil
}

func UpdateSkills(userID int, skills []string) ([]string, error) {
	if _, err := GetPreference(userID); err != nil {
		return nil, err
	}
	data, err := common.Marshal(skills)
	if err != nil {
		return nil, err
	}
	if err = model.DB.Model(&model.MujianUserPreference{}).Where("user_id = ?", userID).Update("enabled_skills", string(data)).Error; err != nil {
		return nil, err
	}
	return skills, nil
}

func GetWallet(userID int) (map[string]interface{}, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return nil, err
	}
	quota, err := model.GetUserQuota(userID, true)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"quota": quota, "credits": CreditsFromQuota(quota), "quota_per_usd": QuotaPerUSD, "credits_per_usd": CreditsPerUSD}, nil
}

func DemoRecharge(userID, credits int) (map[string]interface{}, error) {
	if os.Getenv("MUJIAN_PRODUCTION") == "true" || os.Getenv("MUJIAN_DEMO_RECHARGE_ENABLED") != "true" {
		return nil, ErrNotFound
	}
	if credits <= 0 || credits > 10000 {
		return nil, errors.New("演示充值积分须在 1 到 10000 之间")
	}
	quota := int(float64(credits) / CreditsPerUSD * QuotaPerUSD)
	if err := model.IncreaseUserQuota(userID, quota, true); err != nil {
		return nil, err
	}
	model.RecordLog(userID, model.LogTypeTopup, fmt.Sprintf("幕间演示充值 %d 积分（不发起真实支付）", credits))
	return GetWallet(userID)
}

func internalToken(userID int) (string, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return "", err
	}
	var token model.Token
	err := model.DB.Where("user_id = ? AND name = ? AND status = ?", userID, model.MujianInternalTokenName, common.TokenStatusEnabled).First(&token).Error
	return token.Key, err
}

func relayURL(path string) string {
	base := strings.TrimRight(os.Getenv("MUJIAN_RELAY_BASE_URL"), "/")
	if base == "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "3000"
		}
		base = "http://127.0.0.1:" + port
	}
	return base + path
}

func relayRequest(userID int, path string, payload interface{}) ([]byte, string, error) {
	key, err := internalToken(userID)
	if err != nil {
		return nil, "", ErrRelayUnavailable
	}
	body, err := common.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequest(http.MethodPost, relayURL(path), bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer sk-"+key)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrRelayUnavailable, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	requestID := resp.Header.Get("X-Request-Id")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, requestID, fmt.Errorf("%w: 上游返回 %d", ErrRelayUnavailable, resp.StatusCode)
	}
	return responseBody, requestID, nil
}

func SendAgentMessage(userID int, projectID, content, skill, modelID string) (*RelayResult, error) {
	workspace, err := GetWorkspace(userID, projectID)
	if err != nil {
		return nil, err
	}
	preference, err := GetPreference(userID)
	if err != nil {
		return nil, err
	}
	if modelID == "" {
		modelID = preference.DefaultChatModel
	}
	if !modelAvailable("chat", modelID) {
		return nil, errors.New("对话模型未在可用渠道中开放")
	}
	userMessage := model.MujianAgentMessage{ID: uuid.NewString(), ProjectID: projectID, UserID: userID, Role: "user", Content: strings.TrimSpace(content), Skill: skill, ApplyStatus: "none"}
	if userMessage.Content == "" {
		return nil, errors.New("消息不能为空")
	}
	if err = model.DB.Create(&userMessage).Error; err != nil {
		return nil, err
	}
	contextJSON, err := common.Marshal(workspace)
	if err != nil {
		return nil, err
	}
	prompt := "你是幕间 AI 创作 Agent。只返回 JSON：{\"summary\":string,\"target_type\":\"scene\"或\"shot\",\"target_id\":string,\"expected_version\":number,\"changes\":object}。只能修改给定项目内的对象。项目上下文：" + string(contextJSON)
	payload := map[string]interface{}{"model": modelID, "messages": []map[string]string{{"role": "system", "content": prompt}, {"role": "user", "content": userMessage.Content}}, "max_tokens": 900, "temperature": 0.3}
	responseBody, requestID, err := relayRequest(userID, "/v1/chat/completions", payload)
	if err != nil {
		return nil, err
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage RelayUsage `json:"usage"`
	}
	if err = common.Unmarshal(responseBody, &response); err != nil || len(response.Choices) == 0 {
		return nil, errors.New("Agent 返回结构无效")
	}
	rawProposal := strings.TrimSpace(response.Choices[0].Message.Content)
	rawProposal = strings.TrimPrefix(strings.TrimSuffix(rawProposal, "```"), "```json")
	var proposal AgentProposal
	if err = common.Unmarshal([]byte(strings.TrimSpace(rawProposal)), &proposal); err != nil {
		return nil, errors.New("Agent 提案不是有效 JSON")
	}
	if err = validateProposal(userID, projectID, &proposal); err != nil {
		return nil, err
	}
	proposalJSON, err := common.Marshal(proposal)
	if err != nil {
		return nil, err
	}
	assistant := model.MujianAgentMessage{ID: uuid.NewString(), ProjectID: projectID, UserID: userID, Role: "assistant", Content: proposal.Summary, Skill: skill, Proposal: string(proposalJSON), ApplyStatus: "pending", RelayRequestID: requestID}
	if err = model.DB.Create(&assistant).Error; err != nil {
		return nil, err
	}
	return &RelayResult{Message: assistant, Usage: response.Usage, RelayRequestID: requestID}, nil
}

func validateProposal(userID int, projectID string, proposal *AgentProposal) error {
	if proposal.TargetID == "" || len(proposal.Changes) == 0 {
		return errors.New("Agent 提案缺少修改目标")
	}
	if _, err := GetProject(userID, projectID); err != nil {
		return err
	}
	switch proposal.TargetType {
	case "scene":
		var count int64
		if err := model.DB.Model(&model.MujianScene{}).Where("id = ? AND project_id = ?", proposal.TargetID, projectID).Count(&count).Error; err != nil || count != 1 {
			return ErrNotFound
		}
		for key := range proposal.Changes {
			if !contains([]string{"title", "environment", "action", "dialogues"}, key) {
				return errors.New("Agent 提案包含不允许的场景字段")
			}
		}
	case "shot":
		var count int64
		if err := model.DB.Model(&model.MujianShot{}).Where("id = ? AND project_id = ?", proposal.TargetID, projectID).Count(&count).Error; err != nil || count != 1 {
			return ErrNotFound
		}
		for key := range proposal.Changes {
			if !contains([]string{"shot_type", "prompt", "seedance", "aspect_ratio", "model"}, key) {
				return errors.New("Agent 提案包含不允许的镜头字段")
			}
		}
	default:
		return errors.New("Agent 提案目标类型无效")
	}
	return nil
}

func ApplyAgentMessage(userID int, projectID, messageID string) (*Workspace, error) {
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var message model.MujianAgentMessage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ? AND user_id = ? AND apply_status = ?", messageID, projectID, userID, "pending").First(&message).Error; err != nil {
			return ErrNotFound
		}
		var proposal AgentProposal
		if err := common.UnmarshalJsonStr(message.Proposal, &proposal); err != nil {
			return err
		}
		if err := validateProposal(userID, projectID, &proposal); err != nil {
			return err
		}
		previous := map[string]string{}
		appliedVersion := 0
		if proposal.TargetType == "scene" {
			var scene model.MujianScene
			if err := tx.Where("id = ? AND project_id = ?", proposal.TargetID, projectID).First(&scene).Error; err != nil {
				return err
			}
			if proposal.ExpectedVersion > 0 && scene.Version != proposal.ExpectedVersion {
				return ErrConflict
			}
			for key := range proposal.Changes {
				previous[key] = sceneValue(scene, key)
			}
			updates := stringChanges(proposal.Changes)
			updates["version"] = gorm.Expr("version + 1")
			result := tx.Model(&model.MujianScene{}).Where("id = ? AND project_id = ? AND version = ?", scene.ID, projectID, scene.Version).Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
			appliedVersion = scene.Version + 1
		} else {
			var shot model.MujianShot
			if err := tx.Where("id = ? AND project_id = ?", proposal.TargetID, projectID).First(&shot).Error; err != nil {
				return err
			}
			if proposal.ExpectedVersion > 0 && shot.Version != proposal.ExpectedVersion {
				return ErrConflict
			}
			for key := range proposal.Changes {
				previous[key] = shotValue(shot, key)
			}
			updates := stringChanges(proposal.Changes)
			updates["version"] = gorm.Expr("version + 1")
			result := tx.Model(&model.MujianShot{}).Where("id = ? AND project_id = ? AND version = ?", shot.ID, projectID, shot.Version).Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
			appliedVersion = shot.Version + 1
		}
		previousJSON, err := common.Marshal(undoState{Values: previous, AppliedVersion: appliedVersion})
		if err != nil {
			return err
		}
		result := tx.Model(&message).Where("apply_status = ?", "pending").Updates(map[string]interface{}{"apply_status": "applied", "previous_values": string(previousJSON)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetWorkspace(userID, projectID)
}

func UndoAgentMessage(userID int, projectID, messageID string) (*Workspace, error) {
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var message model.MujianAgentMessage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ? AND user_id = ? AND apply_status = ?", messageID, projectID, userID, "applied").First(&message).Error; err != nil {
			return ErrNotFound
		}
		var proposal AgentProposal
		if err := common.UnmarshalJsonStr(message.Proposal, &proposal); err != nil {
			return err
		}
		var state undoState
		if err := common.UnmarshalJsonStr(message.PreviousValues, &state); err != nil {
			return err
		}
		updates := stringChanges(state.Values)
		updates["version"] = gorm.Expr("version + 1")
		var result *gorm.DB
		if proposal.TargetType == "scene" {
			result = tx.Model(&model.MujianScene{}).Where("id = ? AND project_id = ? AND version = ?", proposal.TargetID, projectID, state.AppliedVersion).Updates(updates)
		} else {
			result = tx.Model(&model.MujianShot{}).Where("id = ? AND project_id = ? AND version = ?", proposal.TargetID, projectID, state.AppliedVersion).Updates(updates)
		}
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		messageResult := tx.Model(&message).Where("apply_status = ?", "applied").Update("apply_status", "undone")
		if messageResult.Error != nil {
			return messageResult.Error
		}
		if messageResult.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetWorkspace(userID, projectID)
}

func stringChanges(changes map[string]string) map[string]interface{} {
	updates := make(map[string]interface{}, len(changes)+1)
	for key, value := range changes {
		updates[key] = value
	}
	return updates
}

func sceneValue(scene model.MujianScene, key string) string {
	switch key {
	case "title":
		return scene.Title
	case "environment":
		return scene.Environment
	case "action":
		return scene.Action
	case "dialogues":
		return scene.Dialogues
	}
	return ""
}
func shotValue(shot model.MujianShot, key string) string {
	switch key {
	case "shot_type":
		return shot.ShotType
	case "prompt":
		return shot.Prompt
	case "seedance":
		return shot.Seedance
	case "aspect_ratio":
		return shot.AspectRatio
	case "model":
		return shot.Model
	}
	return ""
}

func GenerateShot(userID int, projectID, shotID, modelID string) (*ImageResult, error) {
	if _, err := GetProject(userID, projectID); err != nil {
		return nil, err
	}
	var shot model.MujianShot
	if err := model.DB.Where("id = ? AND project_id = ?", shotID, projectID).First(&shot).Error; err != nil {
		return nil, ErrNotFound
	}
	preference, err := GetPreference(userID)
	if err != nil {
		return nil, err
	}
	if modelID == "" {
		modelID = preference.DefaultImageModel
	}
	if !modelAvailable("image", modelID) {
		return nil, errors.New("图像模型未在可用渠道中开放")
	}
	payload := map[string]interface{}{"model": modelID, "prompt": shot.Prompt, "n": 1, "size": imageSize(shot.AspectRatio)}
	responseBody, requestID, err := relayRequest(userID, "/v1/images/generations", payload)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err = common.Unmarshal(responseBody, &response); err != nil || len(response.Data) == 0 {
		return nil, errors.New("图像模型返回结构无效")
	}
	resultURL := response.Data[0].URL
	if resultURL == "" && response.Data[0].B64JSON != "" {
		resultURL = "data:image/png;base64," + response.Data[0].B64JSON
	}
	if resultURL == "" {
		return nil, errors.New("图像模型未返回结果")
	}
	if err = model.DB.Model(&shot).Updates(map[string]interface{}{"model": modelID, "result_url": resultURL, "status": "completed", "version": gorm.Expr("version + 1")}).Error; err != nil {
		return nil, err
	}
	shot.Model, shot.ResultURL, shot.Status, shot.Version = modelID, resultURL, "completed", shot.Version+1
	return &ImageResult{Shot: shot, RelayRequestID: requestID}, nil
}

func imageSize(aspectRatio string) string {
	if aspectRatio == "16:9" {
		return "1536x1024"
	}
	if aspectRatio == "1:1" {
		return "1024x1024"
	}
	return "1024x1536"
}
