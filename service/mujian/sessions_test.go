package mujian

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestAgentSessionsIsolateWorkspaceAndClearOnlyCurrentMessages(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "session-isolation-owner")
	project := createEmptyTestProject(t, user.Id)
	initial, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	firstSessionID := initial.ActiveSessionID

	second, err := CreateAgentSession(user.Id, project.ID)
	require.NoError(t, err)
	require.Len(t, second.AgentSessions, 2)
	require.Empty(t, second.Messages)
	secondSessionID := second.ActiveSessionID
	require.NotEqual(t, firstSessionID, secondSessionID)

	messages := []model.MujianAgentMessage{
		{ID: "first-session-user", ProjectID: project.ID, UserID: user.Id, SessionID: firstSessionID, Role: "user", Content: "第一会话", Mode: AgentModeExecute, CreatedAt: 1},
		{ID: "first-session-assistant", ProjectID: project.ID, UserID: user.Id, SessionID: firstSessionID, Role: "assistant", Content: "第一回复", Mode: AgentModeExecute, CreatedAt: 2},
		{ID: "second-session-user", ProjectID: project.ID, UserID: user.Id, SessionID: secondSessionID, Role: "user", Content: "第二会话", Mode: AgentModeExecute, CreatedAt: 3},
		{ID: "second-session-assistant", ProjectID: project.ID, UserID: user.Id, SessionID: secondSessionID, Role: "assistant", Content: "待应用提案", Mode: AgentModeExecute, ApplyStatus: "pending", Proposal: `{"summary":"pending"}`, CreatedAt: 4},
		{ID: "second-session-applied", ProjectID: project.ID, UserID: user.Id, SessionID: secondSessionID, Role: "assistant", Content: "已应用提案", Mode: AgentModeExecute, ApplyStatus: "applied", Proposal: `{"summary":"applied"}`, PreviousValues: `{}`, CreatedAt: 5},
	}
	require.NoError(t, model.DB.Create(&messages).Error)
	images := []model.MujianImageGeneration{
		{ID: "first-session-image", ProjectID: project.ID, UserID: user.Id, SessionID: firstSessionID, Prompt: "第一会话图片", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1", Status: "succeeded", TaskID: "first-session-image-task", CreatedAt: 6},
		{ID: "second-session-image", ProjectID: project.ID, UserID: user.Id, SessionID: secondSessionID, Prompt: "第二会话图片标题", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1", Status: "succeeded", TaskID: "second-session-image-task", CreatedAt: 7},
	}
	require.NoError(t, model.DB.Create(&images).Error)

	first, err := GetWorkspace(user.Id, project.ID, firstSessionID)
	require.NoError(t, err)
	require.Equal(t, []string{"first-session-user", "first-session-assistant"}, []string{first.Messages[0].ID, first.Messages[1].ID})
	require.Len(t, first.ImageGenerations, 1)
	require.Equal(t, "first-session-image", first.ImageGenerations[0].ID)
	current, err := GetWorkspace(user.Id, project.ID, secondSessionID)
	require.NoError(t, err)
	require.Equal(t, []string{"second-session-user", "second-session-assistant", "second-session-applied"}, []string{
		current.Messages[0].ID, current.Messages[1].ID, current.Messages[2].ID,
	})
	require.Len(t, current.ImageGenerations, 1)
	require.Equal(t, "second-session-image", current.ImageGenerations[0].ID)
	futureActivity := time.Now().UnixMilli() + 1000
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).
		Where("id = ?", secondSessionID).UpdateColumn("updated_at", futureActivity).Error)

	cleared, err := ClearAgentSession(user.Id, project.ID, secondSessionID)
	require.NoError(t, err)
	require.Equal(t, secondSessionID, cleared.ActiveSessionID)
	require.Empty(t, cleared.Messages)
	require.Len(t, cleared.ImageGenerations, 1)
	require.Equal(t, "second-session-image", cleared.ImageGenerations[0].ID)
	clearedSession, err := agentSessionFromWorkspace(cleared)
	require.NoError(t, err)
	require.Equal(t, 2, clearedSession.Revision)
	require.Equal(t, "第二会话图片标题", clearedSession.Title)
	require.Greater(t, clearedSession.UpdatedAt, futureActivity)

	first, err = GetWorkspace(user.Id, project.ID, firstSessionID)
	require.NoError(t, err)
	require.Len(t, first.Messages, 2)
	var pendingCount int64
	require.NoError(t, model.DB.Model(&model.MujianAgentMessage{}).
		Where("session_id = ? AND apply_status = ?", secondSessionID, "pending").Count(&pendingCount).Error)
	require.Zero(t, pendingCount)
	_, err = UndoAgentMessage(user.Id, project.ID, "second-session-applied")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAgentSessionOperationsEnforceProjectOwnership(t *testing.T) {
	setupTestDB(t)
	owner := createTestUser(t, "session-owner")
	other := createTestUser(t, "session-other")
	project := createEmptyTestProject(t, owner.Id)
	workspace, err := GetWorkspace(owner.Id, project.ID)
	require.NoError(t, err)

	_, err = GetWorkspace(other.Id, project.ID, workspace.ActiveSessionID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = CreateAgentSession(other.Id, project.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = ClearAgentSession(other.Id, project.ID, workspace.ActiveSessionID)
	require.ErrorIs(t, err, ErrNotFound)

	otherProject := createEmptyTestProject(t, other.Id)
	otherWorkspace, err := GetWorkspace(other.Id, otherProject.ID)
	require.NoError(t, err)
	_, err = GetWorkspace(owner.Id, project.ID, otherWorkspace.ActiveSessionID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestClearedSessionRevisionRejectsLateAgentPersistence(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "session-revision-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	session, err := agentSessionFromWorkspace(workspace)
	require.NoError(t, err)
	turn := &agentTurn{
		UserID: user.Id, ProjectID: project.ID, SessionID: session.ID, Revision: session.Revision,
		Skill: "短剧编剧",
		UserMessage: model.MujianAgentMessage{
			ID: "late-user", ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
			Role: "user", Content: "已经过期的请求", Mode: AgentModeConsult,
		},
	}

	_, err = ClearAgentSession(user.Id, project.ID, session.ID)
	require.NoError(t, err)
	_, err = persistAgentTurn(turn, "这条回复不应写回", nil, RelayUsage{}, "late-request")
	require.ErrorIs(t, err, ErrSessionChanged)

	var messageCount int64
	require.NoError(t, model.DB.Model(&model.MujianAgentMessage{}).Where("session_id = ?", session.ID).Count(&messageCount).Error)
	require.Zero(t, messageCount)
}

func TestPersistAgentMessagePairKeepsTurnOrderWithinSameMillisecond(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "session-message-order-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	session, err := agentSessionFromWorkspace(workspace)
	require.NoError(t, err)

	for _, turn := range []struct{ userID, assistantID string }{
		{"turn-1-user", "turn-1-assistant"},
		{"turn-2-user", "turn-2-assistant"},
	} {
		userMessage := model.MujianAgentMessage{
			ID: turn.userID, ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
			Role: "user", Content: turn.userID, Mode: AgentModeExecute,
		}
		assistant := model.MujianAgentMessage{
			ID: turn.assistantID, ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
			Role: "assistant", Content: turn.assistantID, Mode: AgentModeExecute,
		}
		require.NoError(t, persistAgentMessagePair(
			user.Id, project.ID, session.ID, session.Revision, userMessage.Content, &userMessage, &assistant,
		))
	}

	workspace, err = GetWorkspace(user.Id, project.ID, session.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"turn-1-user", "turn-1-assistant", "turn-2-user", "turn-2-assistant"}, []string{
		workspace.Messages[0].ID, workspace.Messages[1].ID, workspace.Messages[2].ID, workspace.Messages[3].ID,
	})
	for index := 1; index < len(workspace.Messages); index++ {
		require.Greater(t, workspace.Messages[index].CreatedAt, workspace.Messages[index-1].CreatedAt)
	}
}

func requireWaitsForConcurrentSQLiteWriter(t *testing.T, userID int, label string, operation func() error) {
	t.Helper()
	writer := model.DB.Begin()
	require.NoError(t, writer.Error)
	defer writer.Rollback()
	require.NoError(t, writer.Create(&model.Log{UserId: userID, Type: model.LogTypeConsume, Content: label}).Error)

	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		result <- operation()
	}()
	<-started
	select {
	case operationErr := <-result:
		require.Failf(t, label+" returned while the concurrent writer still held the lock", "error: %v", operationErr)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, writer.Commit().Error)
	require.NoError(t, <-result)
}

func TestAgentSessionWritesWaitForConcurrentSQLiteWriter(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "session-sqlite-writer-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	session, err := agentSessionFromWorkspace(workspace)
	require.NoError(t, err)

	userMessage := model.MujianAgentMessage{
		ID: "sqlite-writer-user", ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
		Role: "user", Content: "等待并发写入", Mode: AgentModeExecute,
	}
	assistant := model.MujianAgentMessage{
		ID: "sqlite-writer-assistant", ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
		Role: "assistant", Content: "已等待", Mode: AgentModeExecute,
	}

	requireWaitsForConcurrentSQLiteWriter(t, user.Id, "persist", func() error {
		return persistAgentMessagePair(
			user.Id, project.ID, session.ID, session.Revision, userMessage.Content, &userMessage, &assistant,
		)
	})
	requireWaitsForConcurrentSQLiteWriter(t, user.Id, "clear", func() error {
		_, clearErr := ClearAgentSession(user.Id, project.ID, session.ID)
		return clearErr
	})
	requireWaitsForConcurrentSQLiteWriter(t, user.Id, "create", func() error {
		_, createErr := CreateAgentSession(user.Id, project.ID)
		return createErr
	})

	var messageCount int64
	require.NoError(t, model.DB.Model(&model.MujianAgentMessage{}).Where("session_id = ?", session.ID).Count(&messageCount).Error)
	require.Zero(t, messageCount)
	var sessionCount int64
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).Where("project_id = ?", project.ID).Count(&sessionCount).Error)
	require.Equal(t, int64(2), sessionCount)

	requireWaitsForConcurrentSQLiteWriter(t, user.Id, "delete", func() error {
		return DeleteProject(user.Id, project.ID)
	})
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).Where("project_id = ?", project.ID).Count(&sessionCount).Error)
	require.Zero(t, sessionCount)
}

func TestAgentSessionsOrderByLatestMessageTimestamp(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "session-latest-order-owner")
	project := createEmptyTestProject(t, user.Id)
	initial, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	firstSession, err := agentSessionFromWorkspace(initial)
	require.NoError(t, err)
	created, err := CreateAgentSession(user.Id, project.ID)
	require.NoError(t, err)
	require.Equal(t, created.ActiveSessionID, created.AgentSessions[0].ID)

	userMessage := model.MujianAgentMessage{
		ID: "latest-order-user", ProjectID: project.ID, UserID: user.Id, SessionID: firstSession.ID,
		Role: "user", Content: "让第一个会话成为最新", Mode: AgentModeExecute,
	}
	assistant := model.MujianAgentMessage{
		ID: "latest-order-assistant", ProjectID: project.ID, UserID: user.Id, SessionID: firstSession.ID,
		Role: "assistant", Content: "reply", Mode: AgentModeExecute,
	}
	require.NoError(t, persistAgentMessagePair(
		user.Id, project.ID, firstSession.ID, firstSession.Revision, userMessage.Content, &userMessage, &assistant,
	))

	refreshed, err := GetWorkspace(user.Id, project.ID, firstSession.ID)
	require.NoError(t, err)
	require.Equal(t, firstSession.ID, refreshed.AgentSessions[0].ID)
	require.Equal(t, assistant.CreatedAt, refreshed.AgentSessions[0].UpdatedAt)
}

func TestAgentSessionTitleUsesFirstLineAndRuneLimit(t *testing.T) {
	require.Equal(t, "第一行标题", agentSessionTitle("  第一行标题  \n第二行"))
	require.Equal(t, "一二三四五六七八九十一二三四五六七八九十一二三四", agentSessionTitle("一二三四五六七八九十一二三四五六七八九十一二三四五"))
}

func TestAgentSessionTitleIsSetOnlyByFirstSuccessfulTurn(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "session-first-title-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	session, err := agentSessionFromWorkspace(workspace)
	require.NoError(t, err)

	for index, content := range []string{defaultAgentSessionTitle, "第二轮不应改标题"} {
		userMessage := model.MujianAgentMessage{
			ID: fmt.Sprintf("title-user-%d", index), ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
			Role: "user", Content: content, Mode: AgentModeExecute,
		}
		assistant := model.MujianAgentMessage{
			ID: fmt.Sprintf("title-assistant-%d", index), ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
			Role: "assistant", Content: "reply", Mode: AgentModeExecute,
		}
		require.NoError(t, persistAgentMessagePair(
			user.Id, project.ID, session.ID, session.Revision, content, &userMessage, &assistant,
		))
	}

	workspace, err = GetWorkspace(user.Id, project.ID, session.ID)
	require.NoError(t, err)
	session, err = agentSessionFromWorkspace(workspace)
	require.NoError(t, err)
	require.Equal(t, defaultAgentSessionTitle, session.Title)
}

func TestImageFirstSessionTitleIsNotOverwrittenByFirstTextTurn(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "session-image-first-title-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	session, err := agentSessionFromWorkspace(workspace)
	require.NoError(t, err)

	firstGeneration, references, task := newImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: session.ID, Prompt: "首张图片定义会话", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.NoError(t, persistImageGeneration(firstGeneration, references, task))
	var titled model.MujianAgentSession
	require.NoError(t, model.DB.First(&titled, "id = ?", session.ID).Error)
	require.Equal(t, "首张图片定义会话", titled.Title)

	secondGeneration, secondReferences, secondTask := newImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: session.ID, Prompt: "后续图片不应改标题", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.NoError(t, persistImageGeneration(secondGeneration, secondReferences, secondTask))
	require.NoError(t, model.DB.First(&titled, "id = ?", session.ID).Error)
	require.Equal(t, "首张图片定义会话", titled.Title)

	userMessage := model.MujianAgentMessage{
		ID: "image-first-user", ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
		Role: "user", Content: "第一条文字不应覆盖图片标题", Mode: AgentModeConsult,
	}
	assistant := model.MujianAgentMessage{
		ID: "image-first-assistant", ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
		Role: "assistant", Content: "文字回复", Mode: AgentModeConsult,
	}
	require.NoError(t, persistAgentMessagePair(
		user.Id, project.ID, session.ID, session.Revision, userMessage.Content, &userMessage, &assistant,
	))
	require.NoError(t, model.DB.First(&titled, "id = ?", session.ID).Error)
	require.Equal(t, "首张图片定义会话", titled.Title)
}

func TestTextFirstSessionTitleIsNotOverwrittenByImage(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "session-text-first-title-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	session, err := agentSessionFromWorkspace(workspace)
	require.NoError(t, err)
	userMessage := model.MujianAgentMessage{
		ID: "text-first-user", ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
		Role: "user", Content: "首条文字定义会话", Mode: AgentModeConsult,
	}
	assistant := model.MujianAgentMessage{
		ID: "text-first-assistant", ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
		Role: "assistant", Content: "文字回复", Mode: AgentModeConsult,
	}
	require.NoError(t, persistAgentMessagePair(
		user.Id, project.ID, session.ID, session.Revision, userMessage.Content, &userMessage, &assistant,
	))

	generation, references, task := newImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: session.ID, Prompt: "图片不应覆盖文字标题", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.NoError(t, persistImageGeneration(generation, references, task))
	var titled model.MujianAgentSession
	require.NoError(t, model.DB.First(&titled, "id = ?", session.ID).Error)
	require.Equal(t, "首条文字定义会话", titled.Title)
}
