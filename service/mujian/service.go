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
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianconfig"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/bytedance/gopkg/util/gopool"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	InitialQuota             = 8767123
	QuotaPerUSD              = 500000
	CreditsPerUSD            = 73
	DefaultChatModel         = mujianconfig.DefaultChatModel
	createScenesProposalKind = "create_scenes"
)

var (
	ErrNotFound         = errors.New("资源不存在")
	ErrConflict         = errors.New("内容已被更新，请刷新后重试")
	ErrSessionChanged   = errors.New("会话已被清空，请在当前会话重新发送")
	ErrRelayUnavailable = errors.New("创作模型暂不可用，请联系管理员检查渠道与价格配置")
)

var DefaultChatModels = mujianconfig.DefaultChatModels()

var DefaultImageModels = []string{
	"nano-banana", "nano-banana-pro", "nano-banana-2", "gpt-image-2",
}

type Workspace struct {
	Project          model.MujianProject        `json:"project"`
	Scenes           []model.MujianScene        `json:"-"`
	Shots            []model.MujianShot         `json:"-"`
	AgentSessions    []model.MujianAgentSession `json:"agent_sessions"`
	ActiveSessionID  string                     `json:"active_session_id"`
	Messages         []model.MujianAgentMessage `json:"messages"`
	ImageGenerations []ImageGeneration          `json:"image_generations"`
}

type AgentProposal struct {
	Kind            string               `json:"kind,omitempty"`
	Summary         string               `json:"summary"`
	Scenes          []AgentSceneProposal `json:"scenes,omitempty"`
	TargetType      string               `json:"target_type,omitempty"`
	TargetID        string               `json:"target_id,omitempty"`
	ExpectedVersion int                  `json:"expected_version,omitempty"`
	Changes         map[string]string    `json:"changes,omitempty"`
}

type AgentSceneProposal struct {
	Title       string                  `json:"title"`
	Environment string                  `json:"environment"`
	Action      string                  `json:"action"`
	Dialogues   []AgentProposedDialogue `json:"dialogues"`
}

type AgentProposedDialogue struct {
	Character string `json:"character"`
	Line      string `json:"line"`
}

type agentResponse struct {
	Reply    string         `json:"reply"`
	Proposal *AgentProposal `json:"proposal"`
}

type undoState struct {
	Values          map[string]string `json:"values,omitempty"`
	AppliedVersion  int               `json:"applied_version,omitempty"`
	CreatedSceneIDs []string          `json:"created_scene_ids,omitempty"`
}

type RelayUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type RelayResult struct {
	UserMessage    model.MujianAgentMessage `json:"user_message"`
	Message        model.MujianAgentMessage `json:"message"`
	SessionID      string                   `json:"session_id"`
	Usage          RelayUsage               `json:"usage"`
	RelayRequestID string                   `json:"relay_request_id"`
}

type ImageResult struct {
	TaskID         string            `json:"task_id"`
	Status         string            `json:"status"`
	Shot           *model.MujianShot `json:"shot,omitempty"`
	RelayRequestID string            `json:"relay_request_id,omitempty"`
	Error          string            `json:"error,omitempty"`
}

func chatModels() []string  { return mujianconfig.EnabledChatModels() }
func imageModels() []string { return envModels("MUJIAN_IMAGE_MODELS", DefaultImageModels) }

func ConfiguredDefaultChatModel() (string, error) {
	return mujianconfig.ConfiguredDefaultChatModel()
}

// CatalogForUser returns the complete catalog annotated for the user's group,
// while the chat and image lists contain only models the group can route.
// A zero user ID is the public catalog and is intentionally scoped to default.
func CatalogForUser(userID int) (map[string][]string, []mujianprovider.CatalogAvailability, error) {
	group := "default"
	if userID > 0 {
		var err error
		group, err = userGroup(userID)
		if err != nil {
			return nil, nil, err
		}
	}
	return catalogForGroup(group)
}

func catalogForGroup(group string) (map[string][]string, []mujianprovider.CatalogAvailability, error) {
	items, err := mujianprovider.CatalogAvailabilityListForGroup(group)
	if err != nil {
		return nil, nil, err
	}
	availableIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item.Available {
			availableIDs = append(availableIDs, item.ID)
		}
	}
	return map[string][]string{
		"chat":  intersectModels(chatModels(), availableIDs),
		"image": intersectModels(imageModels(), availableIDs),
	}, items, nil
}

func catalogModelsForGroup(group string) (map[string][]string, error) {
	availableIDs, err := mujianprovider.AvailableCatalogIDsForGroup(group)
	if err != nil {
		return nil, err
	}
	return map[string][]string{
		"chat":  intersectModels(chatModels(), availableIDs),
		"image": intersectModels(imageModels(), availableIDs),
	}, nil
}

func userGroup(userID int) (string, error) {
	user, err := model.GetUserById(userID, false)
	if err != nil {
		return "", err
	}
	return user.Group, nil
}

func CatalogModelsForUser(userID int) (map[string][]string, error) {
	group := "default"
	if userID > 0 {
		var err error
		group, err = userGroup(userID)
		if err != nil {
			return nil, err
		}
	}
	return catalogModelsForGroup(group)
}

func relayCatalogModelsForUser(userID int) (map[string][]string, error) {
	models, err := CatalogModelsForUser(userID)
	if err != nil {
		common.SysError("mujian model catalog unavailable: " + err.Error())
		return nil, ErrRelayUnavailable
	}
	return models, nil
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
		if contains(enabled, candidate) {
			available = append(available, candidate)
		}
	}
	return available
}

func modelAvailableForUser(userID int, kind, modelID string) (bool, error) {
	catalog, err := relayCatalogModelsForUser(userID)
	if err != nil {
		return false, err
	}
	return contains(catalog[kind], modelID), nil
}

func CreditsFromQuota(quota int) float64 {
	return float64(quota) * CreditsPerUSD / QuotaPerUSD
}

func validateMujianInternalToken(token model.Token, userID int) error {
	if token.UserId != userID || token.Name != model.MujianInternalTokenName ||
		strings.TrimSpace(token.Key) == "" || token.Status != common.TokenStatusEnabled ||
		token.ExpiredTime != -1 || !token.UnlimitedQuota ||
		strings.TrimSpace(token.Group) != "" || token.CrossGroupRetry ||
		(token.AllowIps != nil && strings.TrimSpace(*token.AllowIps) != "") {
		return errors.New("幕间内部令牌状态异常，请管理员检查保留令牌")
	}
	return nil
}

func loadMujianInternalTokens(db *gorm.DB, userID int) ([]model.Token, error) {
	tokens := make([]model.Token, 0, 2)
	err := db.Where("user_id = ? AND name = ?", userID, model.MujianInternalTokenName).
		Limit(2).Find(&tokens).Error
	return tokens, err
}

func validateExistingMujianInternalTokens(tokens []model.Token, userID int) error {
	if len(tokens) != 1 {
		return errors.New("幕间内部令牌数量异常，请管理员清理保留令牌")
	}
	return validateMujianInternalToken(tokens[0], userID)
}

func EnsureOnboarded(userID int) error {
	defaultChatModel, err := ConfiguredDefaultChatModel()
	if err != nil {
		return err
	}
	var preference model.MujianUserPreference
	if err = model.DB.Select("user_id", "onboarded").First(&preference, "user_id = ?", userID).Error; err == nil && preference.Onboarded {
		tokens, tokenErr := loadMujianInternalTokens(model.DB, userID)
		if tokenErr != nil {
			return tokenErr
		}
		if len(tokens) > 0 {
			return validateExistingMujianInternalTokens(tokens, userID)
		}
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		claimed, err := model.ClaimMujianUserWrite(tx, userID)
		if err != nil {
			return err
		}
		if !claimed {
			return ErrNotFound
		}
		var user model.User
		if err = tx.Select("id", "group").First(&user, "id = ?", userID).Error; err != nil {
			return err
		}
		preference = model.MujianUserPreference{}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&preference, "user_id = ?", userID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		preferenceExists := err == nil
		wasOnboarded := preferenceExists && preference.Onboarded
		if !preferenceExists {
			if defaultChatModel == mujianconfig.CutoverChatModel {
				routable, routeErr := mujianprovider.CatalogModelRoutableForGroupTx(tx, defaultChatModel, user.Group)
				if routeErr != nil {
					return routeErr
				}
				if !routable {
					return fmt.Errorf("%w: %s 未在用户分组 %q 中就绪", ErrRelayUnavailable, defaultChatModel, user.Group)
				}
			}
			skills, marshalErr := common.Marshal([]string{"短剧编剧", "分镜导演", "画面提示词"})
			if marshalErr != nil {
				return marshalErr
			}
			preference = model.MujianUserPreference{
				UserID: userID, DefaultChatModel: defaultChatModel, DefaultImageModel: DefaultImageModels[0],
				EnabledSkills: string(skills), Onboarded: true,
			}
			if err = tx.Create(&preference).Error; err != nil {
				return err
			}
			if defaultChatModel == mujianconfig.CutoverChatModel {
				if err = mujianconfig.AddAutomaticDefaultChatModelUsers(tx, userID); err != nil {
					return err
				}
			}
		} else if !preference.Onboarded {
			preference.Onboarded = true
			if err = tx.Save(&preference).Error; err != nil {
				return err
			}
		}

		tokens, err := loadMujianInternalTokens(tx, userID)
		if err != nil {
			return err
		}
		switch len(tokens) {
		case 0:
			key, keyErr := common.GenerateKey()
			if keyErr != nil {
				return keyErr
			}
			token := model.Token{
				UserId: userID, Key: key, Name: model.MujianInternalTokenName, Status: common.TokenStatusEnabled,
				CreatedTime: common.GetTimestamp(), AccessedTime: common.GetTimestamp(), ExpiredTime: -1,
				UnlimitedQuota: true, ModelLimitsEnabled: true, ModelLimits: "",
			}
			if err = tx.Create(&token).Error; err != nil {
				return err
			}
		case 1:
			if !wasOnboarded {
				return errors.New("检测到未绑定的幕间保留令牌，请管理员先清理名称冲突")
			}
			if err = validateMujianInternalToken(tokens[0], userID); err != nil {
				return err
			}
		default:
			return errors.New("幕间内部令牌数量异常，请管理员清理保留令牌")
		}
		return nil
	})
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
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		claimed, claimErr := model.ClaimMujianUserWrite(tx, userID)
		if claimErr != nil {
			return claimErr
		}
		if !claimed {
			return ErrNotFound
		}
		if err := tx.Create(project).Error; err != nil {
			return err
		}
		_, err := createAgentSessionRecord(tx, userID, project.ID)
		return err
	})
	return project, err
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
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := claimProjectWrite(tx, userID, projectID); err != nil {
			return err
		}
		if err := model.GuardMujianImageGenerationLeases(tx, userID, []string{projectID}, time.Now()); err != nil {
			if errors.Is(err, model.ErrMujianImageGenerationActive) {
				return fmt.Errorf("%w: %w", err, ErrConflict)
			}
			return err
		}
		var project model.MujianProject
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ?", projectID, userID).First(&project).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var generationIDs []string
		if err := tx.Model(&model.MujianImageGeneration{}).Where("project_id = ?", projectID).Pluck("id", &generationIDs).Error; err != nil {
			return err
		}
		var generationObjectKeys []string
		if err := tx.Model(&model.MujianImageGeneration{}).
			Where("project_id = ? AND result_object_key <> ?", projectID, "").
			Pluck("result_object_key", &generationObjectKeys).Error; err != nil {
			return err
		}
		var referenceObjectKeys []string
		if len(generationIDs) > 0 {
			if err := tx.Model(&model.MujianImageReference{}).
				Where("generation_id IN ? AND object_key <> ?", generationIDs, "").
				Pluck("object_key", &referenceObjectKeys).Error; err != nil {
				return err
			}
			if err := tx.Where("generation_id IN ?", generationIDs).Delete(&model.MujianImageReference{}).Error; err != nil {
				return err
			}
		}
		var shotObjectKeys []string
		if err := tx.Unscoped().Model(&model.MujianShot{}).
			Where("project_id = ? AND result_object_key <> ?", projectID, "").
			Pluck("result_object_key", &shotObjectKeys).Error; err != nil {
			return err
		}
		objectKeys := append(generationObjectKeys, referenceObjectKeys...)
		objectKeys = append(objectKeys, shotObjectKeys...)
		if err := model.EnqueueMujianObjectDeletes(tx, objectKeys); err != nil {
			return err
		}
		var imageTaskIDs []string
		if err := tx.Model(&model.MujianImageGeneration{}).Where("project_id = ?", projectID).Pluck("task_id", &imageTaskIDs).Error; err != nil {
			return err
		}
		var shotTaskIDs []string
		if err := tx.Model(&model.MujianShot{}).Where("project_id = ? AND generation_task_id <> ?", projectID, "").
			Pluck("generation_task_id", &shotTaskIDs).Error; err != nil {
			return err
		}
		imageTaskIDs = append(imageTaskIDs, shotTaskIDs...)
		if len(imageTaskIDs) > 0 {
			if err := tx.Where("user_id = ? AND platform = ? AND task_id IN ?", userID, constant.TaskPlatformMujianImage, imageTaskIDs).
				Delete(&model.Task{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("project_id = ?", projectID).Delete(&model.MujianImageGeneration{}).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id = ?", projectID).Delete(&model.MujianShot{}).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id = ?", projectID).Delete(&model.MujianScene{}).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id = ?", projectID).Delete(&model.MujianAgentMessage{}).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id = ?", projectID).Delete(&model.MujianAgentSession{}).Error; err != nil {
			return err
		}
		return tx.Delete(&project).Error
	})
}

func GetWorkspace(userID int, projectID string, requestedSessionIDs ...string) (*Workspace, error) {
	project, err := GetProject(userID, projectID)
	if err != nil {
		return nil, err
	}
	workspace := &Workspace{Project: *project}
	if workspace.AgentSessions, err = listAgentSessions(model.DB, userID, projectID); err != nil {
		return nil, err
	}
	if len(workspace.AgentSessions) == 0 {
		return nil, ErrNotFound
	}
	requestedSessionID := ""
	if len(requestedSessionIDs) > 0 {
		requestedSessionID = strings.TrimSpace(requestedSessionIDs[0])
	}
	if requestedSessionID == "" {
		workspace.ActiveSessionID = workspace.AgentSessions[0].ID
	} else {
		workspace.ActiveSessionID = requestedSessionID
		if _, err = agentSessionFromWorkspace(workspace); err != nil {
			return nil, err
		}
	}
	if err = model.DB.Where("project_id = ? AND user_id = ? AND session_id = ?", projectID, userID, workspace.ActiveSessionID).
		Order("created_at").Find(&workspace.Messages).Error; err != nil {
		return nil, err
	}
	if workspace.ImageGenerations, err = listImageGenerations(userID, projectID, workspace.ActiveSessionID); err != nil {
		return nil, err
	}
	return workspace, nil
}

func getLegacyWorkspace(userID int, projectID string, requestedSessionIDs ...string) (*Workspace, error) {
	workspace, err := GetWorkspace(userID, projectID, requestedSessionIDs...)
	if err != nil {
		return nil, err
	}
	if err = model.DB.Where("project_id = ?", projectID).Order("episode, scene_number").Find(&workspace.Scenes).Error; err != nil {
		return nil, err
	}
	if err = model.DB.Where("project_id = ?", projectID).Order("sequence").Find(&workspace.Shots).Error; err != nil {
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
	return getLegacyWorkspace(userID, projectID)
}

func GetPreference(userID int) (*model.MujianUserPreference, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return nil, err
	}
	var preference model.MujianUserPreference
	if err := model.DB.First(&preference, "user_id = ?", userID).Error; err != nil {
		return nil, err
	}
	return &preference, nil
}

func UpdatePreference(userID int, chatModel, imageModel string) (*model.MujianUserPreference, error) {
	preference, err := GetPreference(userID)
	if err != nil {
		return nil, err
	}
	catalog, err := relayCatalogModelsForUser(userID)
	if err != nil {
		return nil, err
	}
	updates := map[string]interface{}{}
	if chatModel != "" {
		if !contains(catalog["chat"], chatModel) {
			return nil, errors.New("未登记的对话模型")
		}
		updates["default_chat_model"] = chatModel
	}
	if imageModel != "" {
		if !contains(catalog["image"], imageModel) {
			return nil, errors.New("未登记的图像模型")
		}
		updates["default_image_model"] = imageModel
	}
	if len(updates) > 0 {
		err = model.DB.Transaction(func(tx *gorm.DB) error {
			if updateErr := tx.Model(preference).Updates(updates).Error; updateErr != nil {
				return updateErr
			}
			if chatModel != "" {
				return mujianconfig.RemoveAutomaticDefaultChatModelUser(tx, userID)
			}
			return nil
		})
		if err != nil {
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

func internalToken(userID int) (string, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return "", err
	}
	var token model.Token
	if err := model.DB.Select("key").Where(
		"user_id = ? AND name = ? AND status = ?",
		userID,
		model.MujianInternalTokenName,
		common.TokenStatusEnabled,
	).First(&token).Error; err != nil {
		return "", err
	}
	return token.Key, nil
}

func relayURL(path string) string {
	base := strings.TrimRight(os.Getenv("MUJIAN_RELAY_BASE_URL"), "/")
	if base == "" {
		base = "http://127.0.0.1:" + common.GetServerPort()
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
	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrRelayUnavailable, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	requestID := resp.Header.Get(common.RequestIdKey)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, requestID, fmt.Errorf("%w: 上游返回 %d", ErrRelayUnavailable, resp.StatusCode)
	}
	return responseBody, requestID, nil
}

func SendAgentMessage(userID int, projectID, content, skill, modelID string, requestedSessionIDs ...string) (*RelayResult, error) {
	workspace, err := GetWorkspace(userID, projectID, requestedSessionIDs...)
	if err != nil {
		return nil, err
	}
	preference, err := GetPreference(userID)
	if err != nil {
		return nil, err
	}
	skill = strings.TrimSpace(skill)
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		modelID = preference.DefaultChatModel
	}
	available, err := modelAvailableForUser(userID, "chat", modelID)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, errors.New("对话模型未在可用渠道中开放")
	}
	if err = validateEnabledSkill(preference, skill); err != nil {
		return nil, err
	}
	session, err := agentSessionFromWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	userMessage := model.MujianAgentMessage{ID: uuid.NewString(), ProjectID: projectID, UserID: userID, SessionID: session.ID, Role: "user", Content: strings.TrimSpace(content), Skill: skill, Mode: AgentModeConsult, ApplyStatus: "none"}
	if userMessage.Content == "" {
		return nil, errors.New("消息不能为空")
	}
	contextJSON, err := common.Marshal(workspace.Project)
	if err != nil {
		return nil, err
	}
	prompt := `你是幕间 AI 创作 Agent。请用自然语言直接回复用户。
只返回文本建议或创作内容，不得输出 JSON 外壳，不得调用工具，不得输出修改提案，也不得声称已直接写入项目。
项目信息只用于理解创作背景；历史对话是用户的创作意图，冲突时以用户最近一次明确指示为准。
当前专业 Skill：` + skill + "。\n\n项目信息：" + string(contextJSON)
	payload := map[string]interface{}{
		"model": modelID, "messages": agentConversationMessages(prompt, workspace.Messages, userMessage.Content),
		"max_tokens": agentDefaultMaxTokens, "temperature": agentDefaultTemperature,
	}
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
	reply := strings.TrimSpace(response.Choices[0].Message.Content)
	if reply == "" {
		return nil, errors.New("Agent 回复内容为空")
	}
	assistant := model.MujianAgentMessage{ID: uuid.NewString(), ProjectID: projectID, UserID: userID, SessionID: session.ID, Role: "assistant", Content: reply, Skill: skill, Mode: AgentModeConsult, ApplyStatus: "none", RelayRequestID: requestID}
	if err = persistAgentMessagePair(userID, projectID, session.ID, session.Revision, userMessage.Content, &userMessage, &assistant); err != nil {
		return nil, err
	}
	return &RelayResult{UserMessage: userMessage, Message: assistant, SessionID: session.ID, Usage: response.Usage, RelayRequestID: requestID}, nil
}

func parseAgentResponse(raw string) (*agentResponse, error) {
	content := strings.TrimSpace(raw)
	content = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(content, "```"), "```json"))
	var response agentResponse
	if err := common.Unmarshal([]byte(content), &response); err != nil {
		return nil, errors.New("Agent 回复不是有效 JSON")
	}
	response.Reply = strings.TrimSpace(response.Reply)
	if response.Reply == "" {
		return nil, errors.New("Agent 回复内容为空")
	}
	if response.Proposal != nil {
		response.Proposal.Summary = strings.TrimSpace(response.Proposal.Summary)
		if response.Proposal.Summary == "" {
			return nil, errors.New("Agent 提案缺少摘要")
		}
		if len(response.Proposal.Scenes) > 0 {
			response.Proposal.Kind = createScenesProposalKind
		}
	}
	return &response, nil
}

func validateProposal(userID int, projectID string, proposal *AgentProposal) error {
	if proposal.Kind == createScenesProposalKind {
		if err := validateSceneBatchProposal(proposal); err != nil {
			return err
		}
		if _, err := GetProject(userID, projectID); err != nil {
			return err
		}
		var count int64
		if err := model.DB.Model(&model.MujianScene{}).Where("project_id = ?", projectID).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrConflict
		}
		return nil
	}
	if proposal.Kind != "" {
		return errors.New("Agent 提案类型无效")
	}
	if proposal.TargetID == "" || len(proposal.Changes) == 0 {
		return errors.New("Agent 提案缺少修改目标")
	}
	if proposal.ExpectedVersion < 1 {
		return errors.New("Agent 提案缺少目标版本")
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
	sessionID := ""
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var message model.MujianAgentMessage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ? AND user_id = ? AND apply_status = ?", messageID, projectID, userID, "pending").First(&message).Error; err != nil {
			return ErrNotFound
		}
		sessionID = message.SessionID
		var proposal AgentProposal
		if err := common.UnmarshalJsonStr(message.Proposal, &proposal); err != nil {
			return err
		}
		if proposal.Kind == createScenesProposalKind {
			if message.Mode != AgentModeExecute || message.Skill != "短剧编剧" {
				return errors.New("场景创建提案的模式或职能无效")
			}
			state, err := applySceneBatchProposal(tx, userID, projectID, &proposal)
			if err != nil {
				return err
			}
			previousJSON, err := common.Marshal(state)
			if err != nil {
				return err
			}
			result := tx.Model(&message).Where("apply_status = ?", "pending").Updates(map[string]interface{}{
				"apply_status": "applied", "previous_values": string(previousJSON),
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
			return nil
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
			if scene.Version != proposal.ExpectedVersion {
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
			if shot.Version != proposal.ExpectedVersion {
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
	return getLegacyWorkspace(userID, projectID, sessionID)
}

func UndoAgentMessage(userID int, projectID, messageID string) (*Workspace, error) {
	sessionID := ""
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var message model.MujianAgentMessage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ? AND user_id = ? AND apply_status = ?", messageID, projectID, userID, "applied").First(&message).Error; err != nil {
			return ErrNotFound
		}
		sessionID = message.SessionID
		var proposal AgentProposal
		if err := common.UnmarshalJsonStr(message.Proposal, &proposal); err != nil {
			return err
		}
		var state undoState
		if err := common.UnmarshalJsonStr(message.PreviousValues, &state); err != nil {
			return err
		}
		if proposal.Kind == createScenesProposalKind {
			if err := undoSceneBatchProposal(tx, projectID, state.CreatedSceneIDs); err != nil {
				return err
			}
			messageResult := tx.Model(&message).Where("apply_status = ?", "applied").Update("apply_status", "undone")
			if messageResult.Error != nil {
				return messageResult.Error
			}
			if messageResult.RowsAffected != 1 {
				return ErrConflict
			}
			return nil
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
	return getLegacyWorkspace(userID, projectID, sessionID)
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
	available, err := modelAvailableForUser(userID, "image", modelID)
	if err != nil {
		return nil, err
	}
	if !available {
		return nil, errors.New("图像模型未在可用渠道中开放")
	}
	task := model.Task{
		TaskID: model.GenerateTaskID(), Platform: constant.TaskPlatformMujianImage, UserId: userID,
		Action: "mujian-image", Status: model.TaskStatusSubmitted, Progress: "0%", SubmitTime: time.Now().Unix(),
		Properties: model.Properties{Input: shot.Prompt, OriginModelName: modelID},
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.MujianShot{}).
			Where("id = ? AND project_id = ? AND status != ?", shotID, projectID, "generating").
			Updates(map[string]interface{}{
				"model": modelID, "status": "generating", "generation_task_id": task.TaskID,
				"generation_error": "", "version": gorm.Expr("version + 1"),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		return tx.Create(&task).Error
	})
	if err != nil {
		return nil, err
	}
	startImageTask(userID, projectID, shotID, modelID, task.TaskID)
	return &ImageResult{TaskID: task.TaskID, Status: "queued"}, nil
}

func GetImageTask(userID int, projectID, taskID string) (*ImageResult, error) {
	if _, err := GetProject(userID, projectID); err != nil {
		return nil, err
	}
	task, exists, err := model.GetByTaskId(userID, taskID)
	if err != nil {
		return nil, err
	}
	if !exists || task.Platform != constant.TaskPlatformMujianImage || task.Action != "mujian-image" {
		return nil, ErrNotFound
	}
	var shot model.MujianShot
	if err = model.DB.Where("project_id = ? AND generation_task_id = ?", projectID, taskID).First(&shot).Error; err != nil {
		return nil, ErrNotFound
	}
	if task.Status == model.TaskStatusSubmitted {
		startImageTask(userID, projectID, shot.ID, task.Properties.OriginModelName, taskID)
	}
	if task.Status == model.TaskStatusFailure && shot.Status == "generating" {
		if err = model.DB.Model(&shot).Updates(map[string]interface{}{
			"status": "failed", "generation_error": task.FailReason,
		}).Error; err != nil {
			return nil, err
		}
		shot.Status = "failed"
		shot.GenerationError = task.FailReason
	}
	status := "running"
	if task.Status == model.TaskStatusSubmitted || task.Status == model.TaskStatusQueued {
		status = "queued"
	} else if task.Status == model.TaskStatusSuccess {
		status = "succeeded"
	} else if task.Status == model.TaskStatusFailure {
		status = "failed"
	}
	return &ImageResult{TaskID: taskID, Status: status, Shot: &shot, Error: task.FailReason}, nil
}

func startImageTask(userID int, projectID, shotID, modelID, taskID string) {
	gopool.Go(func() {
		runImageTask(userID, projectID, shotID, modelID, taskID)
	})
}

func runImageTask(userID int, projectID, shotID, modelID, taskID string) {
	task, exists, err := model.GetByTaskId(userID, taskID)
	if err != nil || !exists {
		return
	}
	previousStatus := task.Status
	if previousStatus != model.TaskStatusSubmitted {
		return
	}
	task.Status = model.TaskStatusInProgress
	task.Progress = "10%"
	task.StartTime = time.Now().Unix()
	won, err := task.UpdateWithStatus(previousStatus)
	if err != nil || !won {
		return
	}
	var shot model.MujianShot
	if err = model.DB.Where("id = ? AND project_id = ? AND generation_task_id = ?", shotID, projectID, taskID).First(&shot).Error; err != nil {
		finishImageTaskFailure(task, shotID, "镜头不存在或任务已被替换")
		return
	}
	payload := map[string]interface{}{"model": modelID, "prompt": shot.Prompt, "n": 1, "size": imageSize(shot.AspectRatio)}
	responseBody, requestID, err := relayRequest(userID, "/v1/images/generations", payload)
	if err != nil {
		finishImageTaskFailure(task, shotID, err.Error())
		return
	}
	var response struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err = common.Unmarshal(responseBody, &response); err != nil || len(response.Data) == 0 {
		finishImageTaskFailure(task, shotID, "图像模型返回结构无效")
		return
	}
	resultURL := response.Data[0].URL
	if resultURL == "" && response.Data[0].B64JSON != "" {
		resultURL = "data:image/png;base64," + response.Data[0].B64JSON
	}
	if resultURL == "" {
		finishImageTaskFailure(task, shotID, "图像模型未返回结果")
		return
	}
	err = finishImageTaskSuccess(task, shotID, modelID, resultURL, requestID)
	if err != nil {
		finishImageTaskFailure(task, shotID, err.Error())
	}
}

func finishImageTaskSuccess(task *model.Task, shotID, modelID, resultURL, requestID string) error {
	task.PrivateData.ResultURL = resultURL
	task.SetData(map[string]string{"shot_id": shotID, "relay_request_id": requestID, "result_url": resultURL})
	return model.DB.Transaction(func(tx *gorm.DB) error {
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", task.ID, model.TaskStatusInProgress).
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
		shotResult := tx.Model(&model.MujianShot{}).
			Where("id = ? AND generation_task_id = ?", shotID, task.TaskID).
			Updates(map[string]interface{}{
				"model": modelID, "result_url": resultURL, "status": "completed",
				"generation_error": "", "version": gorm.Expr("version + 1"),
			})
		if shotResult.Error != nil {
			return shotResult.Error
		}
		if shotResult.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
}

func finishImageTaskFailure(task *model.Task, shotID, reason string) {
	_ = model.DB.Transaction(func(tx *gorm.DB) error {
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND status = ?", task.ID, model.TaskStatusInProgress).
			Updates(map[string]interface{}{
				"status": model.TaskStatusFailure, "progress": "100%", "finish_time": time.Now().Unix(),
				"fail_reason": reason,
			})
		if taskResult.Error != nil || taskResult.RowsAffected != 1 {
			return taskResult.Error
		}
		return tx.Model(&model.MujianShot{}).
			Where("id = ? AND generation_task_id = ?", shotID, task.TaskID).
			Updates(map[string]interface{}{"status": "failed", "generation_error": reason}).Error
	})
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
