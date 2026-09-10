package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

func IsChannelEnabledForGroupModel(group string, modelName string, channelID int) bool {
	if group == "" || modelName == "" || channelID <= 0 {
		return false
	}
	if !common.MemoryCacheEnabled {
		enabled, _ := IsChannelEnabledForGroupModelDB(group, modelName, channelID)
		return enabled
	}

	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	if group2model2channels == nil {
		return false
	}

	if isChannelIDInList(group2model2channels[group][modelName], channelID) {
		return true
	}
	normalized := ratio_setting.FormatMatchingModelName(modelName)
	if normalized != "" && normalized != modelName {
		return isChannelIDInList(group2model2channels[group][normalized], channelID)
	}
	return false
}

func IsChannelEnabledForAnyGroupModel(groups []string, modelName string, channelID int) bool {
	if len(groups) == 0 {
		return false
	}
	for _, g := range groups {
		if IsChannelEnabledForGroupModel(g, modelName, channelID) {
			return true
		}
	}
	return false
}

func IsChannelEnabledForGroupModelDB(group string, modelName string, channelID int) (bool, error) {
	if group == "" || modelName == "" || channelID <= 0 {
		return false, nil
	}
	var count int64
	err := DB.Model(&Ability{}).
		Where(map[string]interface{}{"group": group, "model": modelName, "channel_id": channelID, "enabled": true}).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	normalized := ratio_setting.FormatMatchingModelName(modelName)
	if normalized == "" || normalized == modelName {
		return false, nil
	}
	count = 0
	err = DB.Model(&Ability{}).
		Where(map[string]interface{}{"group": group, "model": normalized, "channel_id": channelID, "enabled": true}).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func isChannelIDInList(list []int, channelID int) bool {
	for _, id := range list {
		if id == channelID {
			return true
		}
	}
	return false
}
