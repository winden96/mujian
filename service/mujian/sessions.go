package mujian

import (
	"errors"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultAgentSessionTitle  = "新会话"
	agentSessionTitleMaxRunes = 24
)

func claimProjectWrite(tx *gorm.DB, userID int, projectID string) error {
	claimed, err := model.ClaimMujianProjectWrite(tx, userID, projectID)
	if err != nil {
		return err
	}
	if !claimed {
		return ErrNotFound
	}
	return nil
}

func createAgentSessionRecord(tx *gorm.DB, userID int, projectID string) (*model.MujianAgentSession, error) {
	now := time.Now().UnixMilli()
	session := &model.MujianAgentSession{
		ID: uuid.NewString(), ProjectID: projectID, UserID: userID,
		Title: defaultAgentSessionTitle, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	// Keep ownership validation and creation in one write-first statement. This
	// also avoids a deferred SQLite transaction upgrading from a read lock.
	result := tx.Exec(`
		INSERT INTO mujian_agent_sessions (id, project_id, user_id, title, revision, created_at, updated_at)
		SELECT ?, id, user_id, ?, ?, ?, ?
		FROM mujian_projects
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL
	`, session.ID, session.Title, session.Revision, session.CreatedAt, session.UpdatedAt, projectID, userID)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrNotFound
	}
	return session, nil
}

func listAgentSessions(db *gorm.DB, userID int, projectID string) ([]model.MujianAgentSession, error) {
	var sessions []model.MujianAgentSession
	err := db.Where("project_id = ? AND user_id = ?", projectID, userID).
		Order("updated_at DESC, created_at DESC, id DESC").Find(&sessions).Error
	return sessions, err
}

func CreateAgentSession(userID int, projectID string) (*Workspace, error) {
	var session *model.MujianAgentSession
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := claimProjectWrite(tx, userID, projectID); err != nil {
			return err
		}
		var err error
		session, err = createAgentSessionRecord(tx, userID, projectID)
		if err != nil {
			return err
		}
		var latestUpdatedAt []int64
		if err = tx.Model(&model.MujianAgentSession{}).
			Where("project_id = ? AND user_id = ? AND id <> ?", projectID, userID, session.ID).
			Order("updated_at DESC").Limit(1).Pluck("updated_at", &latestUpdatedAt).Error; err != nil {
			return err
		}
		if len(latestUpdatedAt) > 0 && latestUpdatedAt[0] >= session.UpdatedAt {
			session.UpdatedAt = latestUpdatedAt[0] + 1
			if err = tx.Model(session).UpdateColumn("updated_at", session.UpdatedAt).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetWorkspace(userID, projectID, session.ID)
}

func ClearAgentSession(userID int, projectID, sessionID string) (*Workspace, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session_id 不能为空")
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := claimProjectWrite(tx, userID, projectID); err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		result := tx.Model(&model.MujianAgentSession{}).
			Where("id = ? AND project_id = ? AND user_id = ?", sessionID, projectID, userID).
			Updates(map[string]interface{}{
				"revision":   gorm.Expr("revision + 1"),
				"updated_at": gorm.Expr("CASE WHEN updated_at >= ? THEN updated_at + 1 ELSE ? END", now, now),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrNotFound
		}
		title := defaultAgentSessionTitle
		var latestImage model.MujianImageGeneration
		err := tx.Select("prompt").Where(
			"project_id = ? AND user_id = ? AND session_id = ?", projectID, userID, sessionID,
		).Order("created_at DESC, id DESC").First(&latestImage).Error
		if err == nil {
			title = agentSessionTitle(latestImage.Prompt)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Model(&model.MujianAgentSession{}).
			Where("id = ? AND project_id = ? AND user_id = ?", sessionID, projectID, userID).
			UpdateColumn("title", title).Error; err != nil {
			return err
		}
		return tx.Where("session_id = ? AND project_id = ? AND user_id = ?", sessionID, projectID, userID).
			Delete(&model.MujianAgentMessage{}).Error
	})
	if err != nil {
		return nil, err
	}
	return GetWorkspace(userID, projectID, sessionID)
}

func getAgentSession(db *gorm.DB, userID int, projectID, sessionID string) (*model.MujianAgentSession, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session_id 不能为空")
	}
	var session model.MujianAgentSession
	err := db.Where("id = ? AND project_id = ? AND user_id = ?", sessionID, projectID, userID).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func agentSessionFromWorkspace(workspace *Workspace) (*model.MujianAgentSession, error) {
	for index := range workspace.AgentSessions {
		if workspace.AgentSessions[index].ID == workspace.ActiveSessionID {
			return &workspace.AgentSessions[index], nil
		}
	}
	return nil, ErrNotFound
}

func lockAgentSessionRevision(tx *gorm.DB, userID int, projectID, sessionID string, revision int) (*model.MujianAgentSession, error) {
	// Claim the write lock before reading. SQLite starts deferred transactions;
	// reading first and then upgrading while Relay billing is writing can fail
	// immediately with SQLITE_BUSY instead of honoring the busy timeout.
	result := tx.Model(&model.MujianAgentSession{}).
		Where("id = ? AND project_id = ? AND user_id = ? AND revision = ?", sessionID, projectID, userID, revision).
		UpdateColumn("updated_at", gorm.Expr("updated_at + 1"))
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, ErrSessionChanged
	}

	var session model.MujianAgentSession
	err := tx.
		Where("id = ? AND project_id = ? AND user_id = ?", sessionID, projectID, userID).
		First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSessionChanged
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func updateAgentSessionAfterTurn(tx *gorm.DB, session *model.MujianAgentSession, content string, updatedAt int64, firstActivity bool) error {
	updates := map[string]interface{}{"updated_at": updatedAt}
	if firstActivity && session.Title == defaultAgentSessionTitle {
		updates["title"] = agentSessionTitle(content)
	}
	return tx.Model(session).Updates(updates).Error
}

func persistAgentMessagePair(userID int, projectID, sessionID string, revision int, titleContent string, userMessage, assistant *model.MujianAgentMessage) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := claimProjectWrite(tx, userID, projectID); err != nil {
			return err
		}
		session, err := lockAgentSessionRevision(tx, userID, projectID, sessionID, revision)
		if err != nil {
			return err
		}
		createdAt := time.Now().UnixMilli()
		if session.UpdatedAt >= createdAt {
			createdAt = session.UpdatedAt + 1
		}
		var latestCreatedAt []int64
		if err := tx.Model(&model.MujianAgentMessage{}).
			Where("session_id = ? AND project_id = ? AND user_id = ?", sessionID, projectID, userID).
			Order("created_at DESC").Limit(1).Pluck("created_at", &latestCreatedAt).Error; err != nil {
			return err
		}
		if len(latestCreatedAt) > 0 && latestCreatedAt[0] >= createdAt {
			createdAt = latestCreatedAt[0] + 1
		}
		firstActivity := len(latestCreatedAt) == 0
		if firstActivity {
			var imageCount int64
			if err := tx.Model(&model.MujianImageGeneration{}).
				Where("session_id = ? AND project_id = ? AND user_id = ?", sessionID, projectID, userID).
				Count(&imageCount).Error; err != nil {
				return err
			}
			firstActivity = imageCount == 0
		}
		userMessage.CreatedAt = createdAt
		assistant.CreatedAt = createdAt + 1
		if err := tx.Create(userMessage).Error; err != nil {
			return err
		}
		if err := tx.Create(assistant).Error; err != nil {
			return err
		}
		return updateAgentSessionAfterTurn(tx, session, titleContent, assistant.CreatedAt, firstActivity)
	})
}

func agentSessionTitle(content string) string {
	firstLine := strings.TrimSpace(strings.SplitN(strings.TrimSpace(content), "\n", 2)[0])
	if firstLine == "" {
		return defaultAgentSessionTitle
	}
	runes := []rune(firstLine)
	if len(runes) <= agentSessionTitleMaxRunes {
		return firstLine
	}
	return string(runes[:agentSessionTitleMaxRunes])
}
