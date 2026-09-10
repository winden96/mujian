package model

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"

	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Ability struct {
	Group     string  `json:"group" gorm:"type:varchar(64);primaryKey;autoIncrement:false"`
	Model     string  `json:"model" gorm:"type:varchar(255);primaryKey;autoIncrement:false"`
	ChannelId int     `json:"channel_id" gorm:"primaryKey;autoIncrement:false;index"`
	Enabled   bool    `json:"enabled"`
	Priority  *int64  `json:"priority" gorm:"bigint;default:0;index"`
	Weight    uint    `json:"weight" gorm:"default:0;index"`
	Tag       *string `json:"tag" gorm:"index"`
}

type AbilityWithChannel struct {
	Ability
	ChannelType int `json:"channel_type"`
}

func GetAllEnableAbilityWithChannels() ([]AbilityWithChannel, error) {
	var abilities []AbilityWithChannel
	err := DB.Table("abilities").
		Select("abilities.*, channels.type as channel_type").
		Joins("left join channels on abilities.channel_id = channels.id").
		Where("abilities.enabled = ?", true).
		Scan(&abilities).Error
	return abilities, err
}

func GetGroupEnabledModels(group string) []string {
	var models []string
	// Find distinct models
	DB.Table("abilities").Where(commonGroupCol+" = ? and enabled = ?", group, true).Distinct("model").Pluck("model", &models)
	return models
}

func GetEnabledModels() []string {
	var models []string
	// Find distinct models
	DB.Table("abilities").Where("enabled = ?", true).Distinct("model").Pluck("model", &models)
	return models
}

func GetAllEnableAbilities() []Ability {
	var abilities []Ability
	DB.Find(&abilities, "enabled = ?", true)
	return abilities
}

func getChannelQuery(group string, model string, retry int, allowedChannelIDs, excludedChannelIDs []int) (*gorm.DB, error) {
	priorityQuery := DB.Model(&Ability{}).Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, model, true)
	if allowedChannelIDs != nil {
		if len(allowedChannelIDs) == 0 {
			return nil, nil
		}
		priorityQuery = priorityQuery.Where("channel_id IN ?", allowedChannelIDs)
	}
	if len(excludedChannelIDs) > 0 {
		priorityQuery = priorityQuery.Where("channel_id NOT IN ?", excludedChannelIDs)
		// A retry should use the best channel that has not been attempted yet.
		// Advancing both a global priority index and the exclusion set skips a
		// same-tier backup and, after excluding a primary, skips the next tier.
		retry = 0
	}
	var priorities []int
	if err := priorityQuery.Distinct("priority").Order("priority DESC").Pluck("priority", &priorities).Error; err != nil {
		return nil, err
	}
	if len(priorities) == 0 || retry < 0 {
		return nil, nil
	}
	if retry >= len(priorities) {
		retry = len(priorities) - 1
	}

	channelQuery := DB.Where(
		commonGroupCol+" = ? and model = ? and enabled = ? and priority = ?",
		group, model, true, priorities[retry],
	)
	if allowedChannelIDs != nil {
		channelQuery = channelQuery.Where("channel_id IN ?", allowedChannelIDs)
	}
	if len(excludedChannelIDs) > 0 {
		channelQuery = channelQuery.Where("channel_id NOT IN ?", excludedChannelIDs)
	}
	return channelQuery, nil
}

func GetChannel(group string, model string, retry int) (*Channel, error) {
	return GetChannelWithAllowedChannelIDs(group, model, retry, nil)
}

func GetChannelWithAllowedChannelIDs(group string, model string, retry int, allowedChannelIDs []int) (*Channel, error) {
	return GetChannelWithFilters(group, model, retry, allowedChannelIDs, nil)
}

// GetChannelWithFilters excludes channels already attempted by the current
// request and selects the highest-priority remaining tier.
func GetChannelWithFilters(group string, model string, retry int, allowedChannelIDs, excludedChannelIDs []int) (*Channel, error) {
	var abilities []Ability

	channelQuery, err := getChannelQuery(group, model, retry, allowedChannelIDs, excludedChannelIDs)
	if err != nil {
		return nil, err
	}
	if channelQuery == nil {
		return nil, nil
	}
	if err := channelQuery.Order("weight DESC").Find(&abilities).Error; err != nil {
		return nil, err
	}
	channel := Channel{}
	if len(abilities) > 0 {
		// Randomly choose one
		weightSum := uint(0)
		for _, ability_ := range abilities {
			weightSum += ability_.Weight + 10
		}
		// Randomly choose one
		weight := common.GetRandomInt(int(weightSum))
		for _, ability_ := range abilities {
			weight -= int(ability_.Weight) + 10
			//log.Printf("weight: %d, ability weight: %d", weight, *ability_.Weight)
			if weight <= 0 {
				channel.Id = ability_.ChannelId
				break
			}
		}
	} else {
		return nil, nil
	}
	return &channel, DB.First(&channel, "id = ?", channel.Id).Error
}

func (channel *Channel) AddAbilities(tx *gorm.DB) error {
	models_ := strings.Split(channel.Models, ",")
	groups_ := strings.Split(channel.Group, ",")
	abilitySet := make(map[string]struct{})
	abilities := make([]Ability, 0, len(models_))
	for _, model := range models_ {
		for _, group := range groups_ {
			key := group + "|" + model
			if _, exists := abilitySet[key]; exists {
				continue
			}
			abilitySet[key] = struct{}{}
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == common.ChannelStatusEnabled,
				Priority:  channel.Priority,
				Weight:    uint(channel.GetWeight()),
				Tag:       channel.Tag,
			}
			abilities = append(abilities, ability)
		}
	}
	if len(abilities) == 0 {
		return nil
	}
	// choose DB or provided tx
	useDB := DB
	if tx != nil {
		useDB = tx
	}
	for _, chunk := range lo.Chunk(abilities, 50) {
		err := useDB.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunk).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func (channel *Channel) DeleteAbilities() error {
	return DB.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
}

// UpdateAbilities updates abilities of this channel.
// Make sure the channel is completed before calling this function.
func (channel *Channel) UpdateAbilities(tx *gorm.DB) error {
	isNewTx := false
	// 如果没有传入事务，创建新的事务
	if tx == nil {
		tx = DB.Begin()
		if tx.Error != nil {
			return tx.Error
		}
		isNewTx = true
		defer func() {
			if r := recover(); r != nil {
				tx.Rollback()
			}
		}()
	}

	// First delete all abilities of this channel
	err := tx.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
	if err != nil {
		if isNewTx {
			tx.Rollback()
		}
		return err
	}

	// Then add new abilities
	models_ := strings.Split(channel.Models, ",")
	groups_ := strings.Split(channel.Group, ",")
	abilitySet := make(map[string]struct{})
	abilities := make([]Ability, 0, len(models_))
	for _, model := range models_ {
		for _, group := range groups_ {
			key := group + "|" + model
			if _, exists := abilitySet[key]; exists {
				continue
			}
			abilitySet[key] = struct{}{}
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == common.ChannelStatusEnabled,
				Priority:  channel.Priority,
				Weight:    uint(channel.GetWeight()),
				Tag:       channel.Tag,
			}
			abilities = append(abilities, ability)
		}
	}

	if len(abilities) > 0 {
		for _, chunk := range lo.Chunk(abilities, 50) {
			err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunk).Error
			if err != nil {
				if isNewTx {
					tx.Rollback()
				}
				return err
			}
		}
	}

	// 如果是新创建的事务，需要提交
	if isNewTx {
		return tx.Commit().Error
	}

	return nil
}

func UpdateAbilityStatus(channelId int, status bool) error {
	return DB.Model(&Ability{}).Where("channel_id = ?", channelId).Select("enabled").Update("enabled", status).Error
}

func UpdateAbilityStatusByTag(tag string, status bool) error {
	return DB.Model(&Ability{}).Where("tag = ?", tag).Select("enabled").Update("enabled", status).Error
}

func UpdateAbilityByTag(tag string, newTag *string, priority *int64, weight *uint) error {
	ability := Ability{}
	if newTag != nil {
		ability.Tag = newTag
	}
	if priority != nil {
		ability.Priority = priority
	}
	if weight != nil {
		ability.Weight = *weight
	}
	return DB.Model(&Ability{}).Where("tag = ?", tag).Updates(ability).Error
}

var fixLock = sync.Mutex{}

func FixAbility() (int, int, error) {
	lock := fixLock.TryLock()
	if !lock {
		return 0, 0, errors.New("已经有一个修复任务在运行中，请稍后再试")
	}
	defer fixLock.Unlock()

	var channels []*Channel
	err := DB.Transaction(func(tx *gorm.DB) error {
		// Lock channel snapshots before deleting any ability. Provider lifecycle
		// writes then happen wholly before or after this rebuild, never between a
		// stale read and its ability insertion.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Order("id ASC").Find(&channels).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM abilities").Error; err != nil {
			return err
		}
		for _, channel := range channels {
			if err := channel.AddAbilities(tx); err != nil {
				return fmt.Errorf("add abilities for channel %d: %w", channel.Id, err)
			}
		}
		return nil
	})
	if err != nil {
		common.SysLog(fmt.Sprintf("Fix abilities failed: %s", err.Error()))
		return 0, len(channels), err
	}
	InitChannelCache()
	return len(channels), 0, nil
}
