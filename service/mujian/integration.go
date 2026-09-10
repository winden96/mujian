package mujian

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// CherryStudioConfig exposes a user-owned, curated-model token for Cherry Studio.
// It never contains an upstream provider credential.
type CherryStudioConfig struct {
	APIKey                string   `json:"api_key"`
	TokenLast4            string   `json:"token_last4"`
	DefaultModel          string   `json:"default_model"`
	DefaultModelAvailable bool     `json:"default_model_available"`
	Models                []string `json:"models"`
}

func GetCherryStudioConfig(userID int) (*CherryStudioConfig, error) {
	token, catalog, err := syncInternalTokenModelLimits(userID)
	if err != nil {
		common.SysError("mujian Cherry Studio token sync failed: " + err.Error())
		return nil, ErrRelayUnavailable
	}
	chat := catalog["chat"]
	if len(chat) == 0 {
		return nil, ErrRelayUnavailable
	}
	preference, err := GetPreference(userID)
	if err != nil {
		return nil, err
	}
	defaultModel := preference.DefaultChatModel

	last4 := token.Key
	if len(last4) > 4 {
		last4 = last4[len(last4)-4:]
	}
	return &CherryStudioConfig{
		APIKey:                "sk-" + token.Key,
		TokenLast4:            last4,
		DefaultModel:          defaultModel,
		DefaultModelAvailable: common.StringsContains(chat, defaultModel),
		Models:                chat,
	}, nil
}

// syncInternalTokenModelLimits keeps the DB-authoritative hidden relay token
// fail-closed and scoped to models currently routable by the user's group. It
// runs outside onboarding so catalog reads cannot contend with the user lock.
func syncInternalTokenModelLimits(userID int) (*model.Token, map[string][]string, error) {
	if err := EnsureOnboarded(userID); err != nil {
		return nil, nil, err
	}
	group, err := userGroup(userID)
	if err != nil {
		return nil, nil, err
	}

	var token model.Token
	if err = model.DB.Where(
		"user_id = ? AND name = ? AND status = ?",
		userID,
		model.MujianInternalTokenName,
		common.TokenStatusEnabled,
	).First(&token).Error; err != nil {
		return nil, nil, err
	}
	catalog, err := RefreshInternalTokenModelLimits(&token, group)
	if err != nil {
		return nil, nil, err
	}
	return &token, catalog, nil
}

// RefreshInternalTokenModelLimits makes an already exported Mujian token
// follow the effective group's live routable catalog on every authentication.
// Ordinary user-created tokens keep their existing static allowlist behavior.
func RefreshInternalTokenModelLimits(token *model.Token, group string) (map[string][]string, error) {
	if token == nil || token.Name != model.MujianInternalTokenName {
		return nil, nil
	}
	catalog, err := catalogModelsForGroup(group)
	if err != nil {
		return nil, err
	}
	allowed := append(append([]string{}, catalog["chat"]...), catalog["image"]...)

	limits := strings.Join(allowed, ",")
	if token.ModelLimits != limits || !token.ModelLimitsEnabled {
		result := model.DB.Model(&model.Token{}).
			Where("id = ? AND user_id = ? AND name = ? AND status = ?", token.Id, token.UserId, model.MujianInternalTokenName, common.TokenStatusEnabled).
			Updates(map[string]interface{}{
				"model_limits_enabled": true,
				"model_limits":         limits,
			})
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			if err = model.DB.Where(
				"id = ? AND user_id = ? AND name = ? AND status = ?",
				token.Id,
				token.UserId,
				model.MujianInternalTokenName,
				common.TokenStatusEnabled,
			).First(token).Error; err != nil {
				return nil, err
			}
			if !token.ModelLimitsEnabled || token.ModelLimits != limits {
				return nil, ErrRelayUnavailable
			}
		}
		token.ModelLimitsEnabled = true
		token.ModelLimits = limits
	}
	if len(allowed) == 0 {
		return nil, ErrRelayUnavailable
	}
	return catalog, nil
}
