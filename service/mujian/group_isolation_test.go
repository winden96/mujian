package mujian

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianprovider"
	"github.com/stretchr/testify/require"
)

func createTestUserInGroup(t *testing.T, username, group string) model.User {
	t.Helper()
	user := createTestUser(t, username)
	require.NoError(t, model.DB.Model(&user).Update("group", group).Error)
	user.Group = group
	return user
}

func enableModelForGroup(t *testing.T, modelID, group, referenceProtocol string) {
	t.Helper()
	priority := int64(100)
	channel := model.Channel{
		Name: "group-provider-" + modelID + "-" + group, Key: "provider-key", Group: group,
		Status: common.ChannelStatusEnabled, Models: modelID, Priority: &priority,
	}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group: group, Model: modelID, ChannelId: channel.Id, Enabled: true, Priority: &priority,
	}).Error)
	price := model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: modelID, UpstreamModelID: modelID, Provider: group,
		BillingType: model.ChannelModelBillingToken, InputPrice: 1, OutputPrice: 2, Currency: "USD", Available: true,
		ReferenceProtocol: referenceProtocol,
	}
	if referenceProtocol != "" {
		price.BillingType = model.ChannelModelBillingFixed
		price.FixedPrice = 0.1
		price.MaxReferenceImages = MaxImageReferenceCount
	}
	require.NoError(t, model.DB.Create(&price).Error)
}

func availabilityForModel(t *testing.T, items []mujianprovider.CatalogAvailability, modelID string) mujianprovider.CatalogAvailability {
	t.Helper()
	for _, item := range items {
		if item.ID == modelID {
			return item
		}
	}
	require.FailNow(t, "catalog model missing", modelID)
	return mujianprovider.CatalogAvailability{}
}

func TestUserCatalogPreferencesAndAgentValidationRespectActualGroup(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "")
	setupTestDB(t)
	defaultUser := createTestUserInGroup(t, "default-creator", "default")
	canaryUser := createTestUserInGroup(t, "canary-creator", "mujian-canary")
	enableModelForGroup(t, DefaultChatModel, "default", "")
	enableModelForGroup(t, "claude-sonnet-4-6", "mujian-canary", "")
	enableModelForGroup(t, "claude-sonnet-4-6", "mujian-canary-extra", "")

	defaultModels, defaultItems, err := CatalogForUser(defaultUser.Id)
	require.NoError(t, err)
	require.Equal(t, []string{DefaultChatModel}, defaultModels["chat"])
	require.False(t, availabilityForModel(t, defaultItems, "claude-sonnet-4-6").Available)
	require.True(t, availabilityForModel(t, defaultItems, DefaultChatModel).Available)

	canaryModels, canaryItems, err := CatalogForUser(canaryUser.Id)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-sonnet-4-6"}, canaryModels["chat"])
	require.True(t, availabilityForModel(t, canaryItems, "claude-sonnet-4-6").Available)
	require.False(t, availabilityForModel(t, canaryItems, DefaultChatModel).Available)

	require.NoError(t, EnsureOnboarded(defaultUser.Id))
	require.NoError(t, EnsureOnboarded(canaryUser.Id))
	require.NoError(t, model.DB.Model(&model.MujianUserPreference{}).
		Where("user_id IN ?", []int{defaultUser.Id, canaryUser.Id}).
		Update("default_chat_model", "claude-sonnet-4-6").Error)

	defaultPreference, err := GetPreference(defaultUser.Id)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-6", defaultPreference.DefaultChatModel)
	canaryPreference, err := GetPreference(canaryUser.Id)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-6", canaryPreference.DefaultChatModel)

	_, err = UpdatePreference(defaultUser.Id, "claude-sonnet-4-6", "")
	require.EqualError(t, err, "未登记的对话模型")
	_, err = UpdatePreference(canaryUser.Id, "claude-sonnet-4-6", "")
	require.NoError(t, err)

	defaultProject := createEmptyTestProject(t, defaultUser.Id)
	_, err = prepareAgentTurn(defaultUser.Id, defaultProject.ID, "test", "短剧编剧", "claude-sonnet-4-6", nil)
	require.EqualError(t, err, "对话模型未在可用渠道中开放")
	_, err = prepareAgentTurn(defaultUser.Id, defaultProject.ID, "test", "短剧编剧", "", nil)
	require.EqualError(t, err, "对话模型未在可用渠道中开放")
	canaryProject := createEmptyTestProject(t, canaryUser.Id)
	turn, err := prepareAgentTurn(canaryUser.Id, canaryProject.ID, "test", "短剧编剧", "claude-sonnet-4-6", nil)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-6", turn.ModelID)
}

func TestInternalTokenDefersLiveLimitRefreshToAuthenticationBoundary(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "")
	setupTestDB(t)
	defaultUser := createTestUserInGroup(t, "legacy-token-default", "default")
	canaryUser := createTestUserInGroup(t, "legacy-token-canary", "mujian-canary")
	enableModelForGroup(t, DefaultChatModel, "default", "")
	enableModelForGroup(t, "claude-sonnet-4-6", "mujian-canary", "")
	require.NoError(t, EnsureOnboarded(defaultUser.Id))
	require.NoError(t, EnsureOnboarded(canaryUser.Id))

	var token model.Token
	require.NoError(t, model.DB.Where("user_id = ? AND name = ?", canaryUser.Id, model.MujianInternalTokenName).First(&token).Error)
	require.NoError(t, model.DB.Model(&token).Updates(map[string]interface{}{
		"model_limits_enabled": false,
		"model_limits":         DefaultChatModel,
	}).Error)

	key, err := internalToken(canaryUser.Id)
	require.NoError(t, err)
	require.Equal(t, token.Key, key)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	require.False(t, token.ModelLimitsEnabled)
	require.Equal(t, DefaultChatModel, token.ModelLimits)

	_, err = RefreshInternalTokenModelLimits(&token, canaryUser.Group)
	require.NoError(t, err)
	require.True(t, token.ModelLimitsEnabled)
	require.Equal(t, "claude-sonnet-4-6", token.ModelLimits)
	require.NotContains(t, token.GetModelLimits(), DefaultChatModel)

	require.NoError(t, model.DB.Model(&model.Ability{}).
		Where("`group` = ? AND model = ?", "mujian-canary", "claude-sonnet-4-6").
		Update("enabled", false).Error)
	key, err = internalToken(canaryUser.Id)
	require.NoError(t, err)
	require.Equal(t, token.Key, key)
	_, err = RefreshInternalTokenModelLimits(&token, canaryUser.Group)
	require.ErrorIs(t, err, ErrRelayUnavailable)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	require.True(t, token.ModelLimitsEnabled)
	require.Empty(t, token.ModelLimits)
}

func TestExportedInternalTokenLearnsNewGroupRoutesOnNextAuthenticationRefresh(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "")
	setupTestDB(t)
	user := createTestUserInGroup(t, "dynamic-exported-token", "mujian-canary")
	enableModelForGroup(t, "claude-sonnet-4-6", "mujian-canary", "")
	require.NoError(t, EnsureOnboarded(user.Id))

	var token model.Token
	require.NoError(t, model.DB.Where("user_id = ? AND name = ?", user.Id, model.MujianInternalTokenName).First(&token).Error)
	_, err := RefreshInternalTokenModelLimits(&token, user.Group)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-sonnet-4-6"}, token.GetModelLimits())

	enableModelForGroup(t, "deepseek-v4-flash", "mujian-canary", "")
	_, err = RefreshInternalTokenModelLimits(&token, user.Group)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-sonnet-4-6", "deepseek-v4-flash"}, token.GetModelLimits())

	var stored model.Token
	require.NoError(t, model.DB.First(&stored, token.Id).Error)
	require.Equal(t, token.ModelLimits, stored.ModelLimits)
}

func TestImageValidationAndCherryConfigRespectActualGroup(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "")
	setupTestDB(t)
	defaultUser := createTestUserInGroup(t, "default-media", "default")
	canaryUser := createTestUserInGroup(t, "canary-media", "mujian-canary")
	enableModelForGroup(t, DefaultChatModel, "default", "")
	enableModelForGroup(t, "claude-sonnet-4-6", "mujian-canary", "")
	enableModelForGroup(t, "nano-banana", "mujian-canary", mujianprovider.ReferenceProtocolGeminiInline)

	imageInput := CreateImageGenerationInput{
		SessionID: "session", Prompt: "test", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		References: []ImageReferenceInput{{Name: "reference.png", Data: testPNG(1)}},
	}
	_, err := validateImageGenerationInput(defaultUser.Id, imageInput)
	require.EqualError(t, err, "图像模型未在可用渠道中开放")
	validated, err := validateImageGenerationInput(canaryUser.Id, imageInput)
	require.NoError(t, err)
	require.Equal(t, "nano-banana", validated.ModelID)

	require.NoError(t, EnsureOnboarded(defaultUser.Id))
	require.NoError(t, EnsureOnboarded(canaryUser.Id))
	defaultConfig, err := GetCherryStudioConfig(defaultUser.Id)
	require.NoError(t, err)
	require.Equal(t, []string{DefaultChatModel}, defaultConfig.Models)
	require.Equal(t, DefaultChatModel, defaultConfig.DefaultModel)
	require.True(t, defaultConfig.DefaultModelAvailable)
	canaryConfig, err := GetCherryStudioConfig(canaryUser.Id)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-sonnet-4-6"}, canaryConfig.Models)
	require.Equal(t, DefaultChatModel, canaryConfig.DefaultModel)
	require.False(t, canaryConfig.DefaultModelAvailable)

	var defaultToken, canaryToken model.Token
	require.NoError(t, model.DB.Where("user_id = ? AND name = ?", defaultUser.Id, model.MujianInternalTokenName).First(&defaultToken).Error)
	require.NoError(t, model.DB.Where("user_id = ? AND name = ?", canaryUser.Id, model.MujianInternalTokenName).First(&canaryToken).Error)
	require.Equal(t, DefaultChatModel, defaultToken.ModelLimits)
	require.Equal(t, "claude-sonnet-4-6,nano-banana", canaryToken.ModelLimits)
}

func TestEnsureOnboardedValidatesDefaultBeforeExistingPreference(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "invalid-model")
	setupTestDB(t)
	user := createTestUser(t, "existing-invalid-default")
	preference := model.MujianUserPreference{
		UserID: user.Id, DefaultChatModel: DefaultChatModel, DefaultImageModel: DefaultImageModels[0],
		EnabledSkills: `["短剧编剧"]`, Onboarded: true,
	}
	require.NoError(t, model.DB.Create(&preference).Error)

	err := EnsureOnboarded(user.Id)
	require.ErrorContains(t, err, "MUJIAN_DEFAULT_CHAT_MODEL 必须是已登记且已启用的对话模型")
	var stored model.MujianUserPreference
	require.NoError(t, model.DB.First(&stored, "user_id = ?", user.Id).Error)
	require.Equal(t, DefaultChatModel, stored.DefaultChatModel)
	var tokenCount int64
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ?", user.Id).Count(&tokenCount).Error)
	require.Zero(t, tokenCount)
}
