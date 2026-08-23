package mujian

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validateSceneBatchProposal(proposal *AgentProposal) error {
	if strings.TrimSpace(proposal.Summary) == "" {
		return errors.New("Agent 提案缺少摘要")
	}
	if len(proposal.Scenes) < 1 || len(proposal.Scenes) > 8 {
		return errors.New("Agent 场景提案数量必须为 1 到 8 个")
	}
	for _, scene := range proposal.Scenes {
		if strings.TrimSpace(scene.Title) == "" || strings.TrimSpace(scene.Environment) == "" || strings.TrimSpace(scene.Action) == "" {
			return errors.New("Agent 场景提案内容不完整")
		}
		if scene.Dialogues == nil {
			return errors.New("Agent 场景提案缺少对白数组")
		}
		for _, dialogue := range scene.Dialogues {
			if strings.TrimSpace(dialogue.Character) == "" || strings.TrimSpace(dialogue.Line) == "" {
				return errors.New("Agent 场景对白内容不完整")
			}
		}
	}
	return nil
}

func applySceneBatchProposal(tx *gorm.DB, userID int, projectID string, proposal *AgentProposal) (undoState, error) {
	if err := validateSceneBatchProposal(proposal); err != nil {
		return undoState{}, err
	}
	var project model.MujianProject
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", projectID, userID).First(&project).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return undoState{}, ErrNotFound
		}
		return undoState{}, err
	}
	var existingSceneCount int64
	if err := tx.Model(&model.MujianScene{}).Where("project_id = ?", projectID).Count(&existingSceneCount).Error; err != nil {
		return undoState{}, err
	}
	if existingSceneCount != 0 {
		return undoState{}, ErrConflict
	}

	scenes := make([]model.MujianScene, 0, len(proposal.Scenes))
	createdIDs := make([]string, 0, len(proposal.Scenes))
	for index, input := range proposal.Scenes {
		dialoguesJSON, err := common.Marshal(input.Dialogues)
		if err != nil {
			return undoState{}, err
		}
		id := uuid.NewString()
		createdIDs = append(createdIDs, id)
		scenes = append(scenes, model.MujianScene{
			ID: id, ProjectID: projectID, Episode: project.CurrentEpisode, SceneNumber: index + 1,
			Title: strings.TrimSpace(input.Title), Environment: strings.TrimSpace(input.Environment),
			Action: strings.TrimSpace(input.Action), Dialogues: string(dialoguesJSON), Version: 1,
		})
	}
	if err := tx.Create(&scenes).Error; err != nil {
		return undoState{}, err
	}
	return undoState{CreatedSceneIDs: createdIDs}, nil
}

func undoSceneBatchProposal(tx *gorm.DB, projectID string, sceneIDs []string) error {
	if len(sceneIDs) == 0 {
		return ErrConflict
	}
	var scenes []model.MujianScene
	if err := tx.Where("project_id = ? AND id IN ?", projectID, sceneIDs).Find(&scenes).Error; err != nil {
		return err
	}
	if len(scenes) != len(sceneIDs) {
		return ErrConflict
	}
	for _, scene := range scenes {
		if scene.Version != 1 {
			return ErrConflict
		}
	}
	var shotCount int64
	if err := tx.Model(&model.MujianShot{}).Where("project_id = ? AND scene_id IN ?", projectID, sceneIDs).Count(&shotCount).Error; err != nil {
		return err
	}
	if shotCount != 0 {
		return ErrConflict
	}
	result := tx.Where("project_id = ? AND id IN ? AND version = ?", projectID, sceneIDs, 1).Delete(&model.MujianScene{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != int64(len(sceneIDs)) {
		return ErrConflict
	}
	return nil
}
