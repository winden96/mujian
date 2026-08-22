package mujian

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// CherryStudioConfig exposes a user-owned, curated-model token for Cherry Studio.
// It never contains an upstream provider credential.
type CherryStudioConfig struct {
	APIKey       string   `json:"api_key"`
	TokenLast4   string   `json:"token_last4"`
	DefaultModel string   `json:"default_model"`
	Models       []string `json:"models"`
}

func GetCherryStudioConfig(userID int) (*CherryStudioConfig, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return nil, err
	}

	catalog := CatalogModels()
	chat := catalog["chat"]
	if len(chat) == 0 {
		return nil, ErrRelayUnavailable
	}
	allowed := append(append([]string{}, chat...), catalog["image"]...)

	var token model.Token
	if err := model.DB.Where(
		"user_id = ? AND name = ? AND status = ?",
		userID,
		model.MujianInternalTokenName,
		common.TokenStatusEnabled,
	).First(&token).Error; err != nil {
		return nil, err
	}

	limits := strings.Join(allowed, ",")
	if token.ModelLimits != limits || !token.ModelLimitsEnabled {
		if err := model.DB.Model(&token).Updates(map[string]interface{}{
			"model_limits_enabled": true,
			"model_limits":         limits,
		}).Error; err != nil {
			return nil, err
		}
	}

	defaultModel := chat[0]
	var preference model.MujianUserPreference
	if err := model.DB.Where("user_id = ?", userID).First(&preference).Error; err == nil && contains(chat, preference.DefaultChatModel) {
		defaultModel = preference.DefaultChatModel
	}

	last4 := token.Key
	if len(last4) > 4 {
		last4 = last4[len(last4)-4:]
	}
	return &CherryStudioConfig{
		APIKey:       "sk-" + token.Key,
		TokenLast4:   last4,
		DefaultModel: defaultModel,
		Models:       chat,
	}, nil
}
