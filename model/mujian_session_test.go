package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupMujianSessionMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&MujianProject{}, &MujianAgentSession{}, &MujianAgentMessage{}, &MujianImageGeneration{}))
	return db
}

func TestMigrateMujianAgentSessionsBackfillsLegacyMessagesIdempotently(t *testing.T) {
	db := setupMujianSessionMigrationTestDB(t)

	projects := []MujianProject{
		{ID: "legacy-project", UserID: 7, Title: "有历史消息"},
		{ID: "empty-project", UserID: 7, Title: "无历史消息"},
	}
	require.NoError(t, db.Create(&projects).Error)
	legacyMessage := MujianAgentMessage{
		ID: "legacy-message", ProjectID: projects[0].ID, UserID: 7,
		Role: "user", Content: "迁移前的消息", Mode: "execute",
	}
	require.NoError(t, db.Create(&legacyMessage).Error)
	legacyImage := MujianImageGeneration{
		ID: "legacy-image", ProjectID: projects[0].ID, UserID: 7, Prompt: "迁移前图片",
		Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1", Status: "succeeded", TaskID: "legacy-image-task",
	}
	require.NoError(t, db.Create(&legacyImage).Error)

	require.NoError(t, migrateMujianAgentSessions())
	require.NoError(t, migrateMujianAgentSessions())

	var sessions []MujianAgentSession
	require.NoError(t, db.Order("project_id").Find(&sessions).Error)
	require.Len(t, sessions, 2)
	var legacySession, emptySession MujianAgentSession
	require.NoError(t, db.First(&legacySession, "project_id = ?", projects[0].ID).Error)
	require.NoError(t, db.First(&emptySession, "project_id = ?", projects[1].ID).Error)
	require.Equal(t, "历史会话", legacySession.Title)
	require.Equal(t, "新会话", emptySession.Title)
	require.Equal(t, migratedMujianAgentSessionID(projects[0].ID, "history"), legacySession.ID)
	require.Equal(t, migratedMujianAgentSessionID(projects[1].ID, "default"), emptySession.ID)
	require.NoError(t, db.First(&legacyMessage, "id = ?", legacyMessage.ID).Error)
	require.Equal(t, legacySession.ID, legacyMessage.SessionID)
	require.NoError(t, db.First(&legacyImage, "id = ?", legacyImage.ID).Error)
	require.Equal(t, legacySession.ID, legacyImage.SessionID)
	require.True(t, db.Migrator().HasIndex(&MujianAgentMessage{}, "idx_mujian_agent_messages_session"))
	require.True(t, db.Migrator().HasIndex(&MujianImageGeneration{}, "idx_mujian_image_generations_session"))
	require.True(t, db.Migrator().HasColumn(&MujianImageGeneration{}, "result_data"))
	require.True(t, db.Migrator().HasColumn(&MujianImageGeneration{}, "result_mime_type"))
}

func TestMigrateMujianAgentSessionsReusesExistingHistorySession(t *testing.T) {
	db := setupMujianSessionMigrationTestDB(t)

	project := MujianProject{ID: "rolling-history-project", UserID: 9, Title: "滚动升级"}
	require.NoError(t, db.Create(&project).Error)
	require.NoError(t, db.Create(&MujianAgentSession{
		ID: "old-random-history-session", ProjectID: project.ID, UserID: project.UserID,
		Title: "历史会话", Revision: 1,
	}).Error)
	message := MujianAgentMessage{
		ID: "late-legacy-message", ProjectID: project.ID, UserID: project.UserID,
		Role: "user", Content: "旧节点迟到消息", Mode: "execute",
	}
	require.NoError(t, db.Create(&message).Error)

	require.NoError(t, migrateMujianAgentSessions())

	var sessionCount int64
	require.NoError(t, db.Model(&MujianAgentSession{}).Where("project_id = ?", project.ID).Count(&sessionCount).Error)
	require.Equal(t, int64(1), sessionCount)
	require.NoError(t, db.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, "old-random-history-session", message.SessionID)
}

func TestMigrateMujianAgentSessionsReusesUniqueEmptyDefaultForLateHistory(t *testing.T) {
	db := setupMujianSessionMigrationTestDB(t)

	project := MujianProject{ID: "rolling-default-project", UserID: 10, Title: "空项目滚动升级"}
	require.NoError(t, db.Create(&project).Error)
	defaultSessionID := migratedMujianAgentSessionID(project.ID, "default")
	require.NoError(t, db.Create(&MujianAgentSession{
		ID: defaultSessionID, ProjectID: project.ID, UserID: project.UserID,
		Title: "新会话", Revision: 1,
	}).Error)
	message := MujianAgentMessage{
		ID: "late-default-legacy-message", ProjectID: project.ID, UserID: project.UserID,
		Role: "user", Content: "旧节点在默认会话后写入", Mode: "execute",
	}
	require.NoError(t, db.Create(&message).Error)

	require.NoError(t, migrateMujianAgentSessions())

	var sessions []MujianAgentSession
	require.NoError(t, db.Where("project_id = ?", project.ID).Find(&sessions).Error)
	require.Len(t, sessions, 1)
	require.NoError(t, db.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, defaultSessionID, message.SessionID)
	var defaultSession MujianAgentSession
	require.NoError(t, db.First(&defaultSession, "id = ?", defaultSessionID).Error)
	require.Equal(t, "历史会话", defaultSession.Title)
}

func TestMigrateMujianAgentSessionsBackfillsImageOnlyHistoryIdempotently(t *testing.T) {
	db := setupMujianSessionMigrationTestDB(t)

	project := MujianProject{ID: "image-only-history-project", UserID: 11, Title: "只有旧图片"}
	require.NoError(t, db.Create(&project).Error)
	defaultSessionID := migratedMujianAgentSessionID(project.ID, "default")
	require.NoError(t, db.Create(&MujianAgentSession{
		ID: defaultSessionID, ProjectID: project.ID, UserID: project.UserID, Title: "新会话", Revision: 1,
	}).Error)
	legacyImage := MujianImageGeneration{
		ID: "image-only-legacy", ProjectID: project.ID, UserID: project.UserID, Prompt: "旧图片提示词",
		Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1", Status: "succeeded", TaskID: "image-only-legacy-task",
	}
	require.NoError(t, db.Create(&legacyImage).Error)

	require.NoError(t, migrateMujianAgentSessions())
	require.NoError(t, migrateMujianAgentSessions())

	var sessions []MujianAgentSession
	require.NoError(t, db.Where("project_id = ?", project.ID).Find(&sessions).Error)
	require.Len(t, sessions, 1)
	require.Equal(t, defaultSessionID, sessions[0].ID)
	require.Equal(t, "历史会话", sessions[0].Title)
	require.NoError(t, db.First(&legacyImage, "id = ?", legacyImage.ID).Error)
	require.Equal(t, defaultSessionID, legacyImage.SessionID)
}

func TestMigrateMujianAgentSessionsRefreshesHistoryActivityAndOrderingIdempotently(t *testing.T) {
	db := setupMujianSessionMigrationTestDB(t)

	const (
		historyUpdatedAt   = int64(1_750_000_000_000)
		currentUpdatedAt   = int64(1_760_000_025_000)
		olderMessageMillis = int64(1_759_999_999_000)
		lateImageMillis    = int64(1_760_000_020_000)
		latestMessageSecs  = int64(1_760_000_030)
		latestActivityAt   = latestMessageSecs * 1000
	)
	project := MujianProject{ID: "late-activity-project", UserID: 12, Title: "迟到历史排序"}
	require.NoError(t, db.Create(&project).Error)
	history := MujianAgentSession{
		ID: "existing-history", ProjectID: project.ID, UserID: project.UserID, Title: "历史会话", Revision: 1,
		CreatedAt: historyUpdatedAt - 1000, UpdatedAt: historyUpdatedAt,
	}
	current := MujianAgentSession{
		ID: "current-session", ProjectID: project.ID, UserID: project.UserID, Title: "当前会话", Revision: 1,
		CreatedAt: currentUpdatedAt - 1000, UpdatedAt: currentUpdatedAt,
	}
	require.NoError(t, db.Create(&[]MujianAgentSession{history, current}).Error)

	messages := []MujianAgentMessage{
		{
			ID: "legacy-message-millis", ProjectID: project.ID, UserID: project.UserID,
			Role: "user", Content: "毫秒旧消息", Mode: "execute", CreatedAt: olderMessageMillis,
		},
		{
			ID: "legacy-message-seconds", ProjectID: project.ID, UserID: project.UserID,
			Role: "user", Content: "秒级迟到消息", Mode: "execute", CreatedAt: latestMessageSecs,
		},
	}
	require.NoError(t, db.Create(&messages).Error)
	image := MujianImageGeneration{
		ID: "legacy-image-millis", ProjectID: project.ID, UserID: project.UserID, Prompt: "毫秒迟到图片",
		Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1", Status: "succeeded", TaskID: "legacy-image-millis-task",
		CreatedAt: lateImageMillis,
	}
	require.NoError(t, db.Create(&image).Error)

	require.NoError(t, migrateMujianAgentSessions())

	for i := range messages {
		require.NoError(t, db.First(&messages[i], "id = ?", messages[i].ID).Error)
		require.Equal(t, history.ID, messages[i].SessionID)
	}
	require.NoError(t, db.First(&image, "id = ?", image.ID).Error)
	require.Equal(t, history.ID, image.SessionID)
	require.NoError(t, db.First(&history, "id = ?", history.ID).Error)
	require.Equal(t, latestActivityAt, history.UpdatedAt)

	var ordered []MujianAgentSession
	require.NoError(t, db.Where("project_id = ?", project.ID).
		Order("updated_at DESC, created_at DESC, id DESC").Find(&ordered).Error)
	require.Len(t, ordered, 2)
	require.Equal(t, []string{history.ID, current.ID}, []string{ordered[0].ID, ordered[1].ID})

	require.NoError(t, migrateMujianAgentSessions())
	var sessions []MujianAgentSession
	require.NoError(t, db.Where("project_id = ?", project.ID).
		Order("updated_at DESC, created_at DESC, id DESC").Find(&sessions).Error)
	require.Len(t, sessions, 2)
	require.Equal(t, []string{history.ID, current.ID}, []string{sessions[0].ID, sessions[1].ID})
	require.Equal(t, latestActivityAt, sessions[0].UpdatedAt)
}
