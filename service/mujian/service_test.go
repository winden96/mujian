package mujian

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared&_pragma=busy_timeout(30000)"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Log{}, &model.Ability{}, &model.Channel{}, &model.ChannelModelPrice{},
		&model.TopUp{},
		&model.MujianProject{}, &model.MujianScene{}, &model.MujianShot{},
		&model.MujianAgentSession{}, &model.MujianAgentMessage{},
		&model.MujianImageGeneration{}, &model.MujianImageReference{},
		&model.MujianObjectOperation{}, &model.MujianUserPreference{}, &model.Task{},
	))
}

func createTestUser(t *testing.T, username string) model.User {
	t.Helper()
	user := model.User{Username: username, Password: "hashed-password", DisplayName: username, AffCode: username, Quota: 0}
	require.NoError(t, model.DB.Create(&user).Error)
	return user
}

func createEmptyTestProject(t *testing.T, userID int) *model.MujianProject {
	t.Helper()
	project, err := CreateProject(userID, "空项目", "现代修仙喜剧")
	require.NoError(t, err)
	return project
}

func createLegacyTestScene(t *testing.T, projectID string) model.MujianScene {
	t.Helper()
	scene := model.MujianScene{
		ID: uuid.NewString(), ProjectID: projectID, Episode: 1, SceneNumber: 1,
		Title: "测试场景", Environment: "测试环境", Action: "测试动作", Version: 1,
	}
	require.NoError(t, model.DB.Create(&scene).Error)
	return scene
}

func blockFirstProjectClaim(t *testing.T) (<-chan struct{}, chan<- struct{}) {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	var blocked atomic.Bool
	callbackName := "test:block-project-claim:" + t.Name()
	require.NoError(t, model.DB.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "mujian_projects" && blocked.CompareAndSwap(false, true) {
			close(started)
			<-release
		}
	}))
	t.Cleanup(func() { require.NoError(t, model.DB.Callback().Update().Remove(callbackName)) })
	return started, release
}

func waitForProjectClaim(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "project claim did not start")
	}
}

func requireWriteBlocked(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNowf(t, "write was not serialized behind project deletion", "error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func createPendingSceneBatchMessage(t *testing.T, userID int, projectID, messageID string, scenes []AgentSceneProposal) model.MujianAgentMessage {
	t.Helper()
	proposalJSON, err := common.Marshal(map[string]interface{}{
		"kind": "create_scenes", "summary": "生成第一集", "scenes": scenes,
	})
	require.NoError(t, err)
	message := model.MujianAgentMessage{
		ID: messageID, ProjectID: projectID, UserID: userID, Role: "assistant",
		Content: "已生成第一集场景提案", Skill: "短剧编剧", Mode: AgentModeExecute,
		Proposal: string(proposalJSON), ApplyStatus: "pending",
	}
	require.NoError(t, model.DB.Create(&message).Error)
	return message
}

func validSceneBatch() []AgentSceneProposal {
	return []AgentSceneProposal{
		{
			Title: "屋顶苏醒", Environment: "深夜的现代城市屋顶",
			Action:    "主角从金光中跌出，看向陌生天际线。",
			Dialogues: []AgentProposedDialogue{{Character: "白小纯", Line: "这年代的灵气怎么这么稀薄？"}},
		},
		{
			Title: "地铁奇遇", Environment: "早高峰的地铁车厢",
			Action:    "主角追逐一道只有他能看见的灵光。",
			Dialogues: []AgentProposedDialogue{},
		},
	}
}

func TestEnsureOnboardedIsIdempotentAndHidesInternalToken(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "creator")

	require.NoError(t, EnsureOnboarded(user.Id))
	require.NoError(t, EnsureOnboarded(user.Id))

	var projectCount, tokenCount, sessionCount, sceneCount, shotCount int64
	require.NoError(t, model.DB.Model(&model.MujianProject{}).Where("user_id = ?", user.Id).Count(&projectCount).Error)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ? AND name = ?", user.Id, model.MujianInternalTokenName).Count(&tokenCount).Error)
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).Where("user_id = ?", user.Id).Count(&sessionCount).Error)
	require.NoError(t, model.DB.Model(&model.MujianScene{}).Count(&sceneCount).Error)
	require.NoError(t, model.DB.Model(&model.MujianShot{}).Count(&shotCount).Error)
	require.Equal(t, int64(3), projectCount)
	require.Equal(t, int64(1), tokenCount)
	require.Equal(t, projectCount, sessionCount)
	require.Zero(t, sceneCount)
	require.Zero(t, shotCount)
	var preference model.MujianUserPreference
	require.NoError(t, model.DB.First(&preference, "user_id = ?", user.Id).Error)
	require.Equal(t, "claude-opus-5", DefaultChatModel)
	require.Equal(t, "claude-opus-5", preference.DefaultChatModel)
	var enabledSkills []string
	require.NoError(t, common.UnmarshalJsonStr(preference.EnabledSkills, &enabledSkills))
	require.Equal(t, []string{"短剧编剧", "画面提示词"}, enabledSkills)

	quota, err := model.GetUserQuota(user.Id, true)
	require.NoError(t, err)
	require.Equal(t, InitialQuota, quota)
	visibleTokens, err := model.GetAllUserTokens(user.Id, 0, 20)
	require.NoError(t, err)
	require.Empty(t, visibleTokens)

	require.NoError(t, model.DB.Exec("INSERT INTO tokens (user_id, key, status, name, expired_time) VALUES (?, ?, 1, NULL, -1)", user.Id, "legacy-null-name-token").Error)
	visibleTokens, err = model.GetAllUserTokens(user.Id, 0, 20)
	require.NoError(t, err)
	require.Len(t, visibleTokens, 1)
}

func TestEnsureOnboardedPreservesExistingChatPreference(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "existing-creator")
	preference := model.MujianUserPreference{
		UserID: user.Id, DefaultChatModel: "deepseek-v4-flash", DefaultImageModel: DefaultImageModels[0],
		EnabledSkills: `["短剧编剧"]`, Onboarded: false,
	}
	require.NoError(t, model.DB.Create(&preference).Error)

	require.NoError(t, EnsureOnboarded(user.Id))
	require.NoError(t, model.DB.First(&preference, "user_id = ?", user.Id).Error)
	require.Equal(t, "deepseek-v4-flash", preference.DefaultChatModel)
	require.True(t, preference.Onboarded)
}

func TestProjectIsolationAndOptimisticLock(t *testing.T) {
	setupTestDB(t)
	owner := createTestUser(t, "owner")
	other := createTestUser(t, "other")
	projects, err := ListProjects(owner.Id)
	require.NoError(t, err)
	require.NotEmpty(t, projects)

	_, err = GetProject(other.Id, projects[0].ID)
	require.ErrorIs(t, err, ErrNotFound)

	createLegacyTestScene(t, projects[0].ID)
	workspace, err := getLegacyWorkspace(owner.Id, projects[0].ID)
	require.NoError(t, err)
	require.NotEmpty(t, workspace.Scenes)
	scene := workspace.Scenes[0]
	scene.Title = "新的场景标题"
	updated, err := UpdateWorkspace(owner.Id, projects[0].ID, []model.MujianScene{scene})
	require.NoError(t, err)
	require.Equal(t, "新的场景标题", updated.Scenes[0].Title)

	_, err = UpdateWorkspace(owner.Id, projects[0].ID, []model.MujianScene{scene})
	require.True(t, errors.Is(err, ErrConflict))
}

func TestDeletingUserCascadesMujianData(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "delete-me")
	require.NoError(t, EnsureOnboarded(user.Id))
	projects, err := ListProjects(user.Id)
	require.NoError(t, err)
	generation := model.MujianImageGeneration{
		ID: "delete-generation", ProjectID: projects[0].ID, UserID: user.Id, Prompt: "delete",
		Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1", Status: "queued", TaskID: "delete-task",
	}
	require.NoError(t, model.DB.Create(&generation).Error)
	require.NoError(t, model.DB.Create(&model.MujianImageReference{
		ID: "delete-reference", GenerationID: generation.ID, Position: 0, Name: "delete.png",
		MIMEType: "image/png", SizeBytes: int64(len(testPNG(1))), Data: testPNG(1),
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID: generation.TaskID, Platform: constant.TaskPlatformMujianImage, UserId: user.Id,
		Action: imageGenerationAction, Status: model.TaskStatusSubmitted,
	}).Error)
	require.NoError(t, model.HardDeleteUserById(user.Id))

	var projectCount, preferences, tokens, sessions, generations, references, tasks int64
	require.NoError(t, model.DB.Unscoped().Model(&model.MujianProject{}).Where("user_id = ?", user.Id).Count(&projectCount).Error)
	require.NoError(t, model.DB.Unscoped().Model(&model.MujianUserPreference{}).Where("user_id = ?", user.Id).Count(&preferences).Error)
	require.NoError(t, model.DB.Unscoped().Model(&model.Token{}).Where("user_id = ? AND name = ?", user.Id, model.MujianInternalTokenName).Count(&tokens).Error)
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).Where("user_id = ?", user.Id).Count(&sessions).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Where("user_id = ?", user.Id).Count(&generations).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageReference{}).Where("generation_id = ?", generation.ID).Count(&references).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("user_id = ? AND platform = ?", user.Id, constant.TaskPlatformMujianImage).Count(&tasks).Error)
	require.Zero(t, projectCount)
	require.Zero(t, preferences)
	require.Zero(t, tokens)
	require.Zero(t, sessions)
	require.Zero(t, generations)
	require.Zero(t, references)
	require.Zero(t, tasks)
}

func TestDeleteProjectRemovesItsImageTasks(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "delete-project-images")
	projects, err := ListProjects(user.Id)
	require.NoError(t, err)
	projectID := projects[0].ID
	generationTaskID := model.GenerateTaskID()
	require.NoError(t, model.DB.Create(&model.MujianImageGeneration{
		ID: "project-delete-generation", ProjectID: projectID, UserID: user.Id, Prompt: "delete",
		Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1", Status: "queued", TaskID: generationTaskID,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Task{
		TaskID: generationTaskID, Platform: constant.TaskPlatformMujianImage, UserId: user.Id,
		Action: imageGenerationAction, Status: model.TaskStatusSubmitted,
	}).Error)

	require.NoError(t, DeleteProject(user.Id, projectID))
	var tasks, sessions int64
	require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id = ?", generationTaskID).Count(&tasks).Error)
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).Where("project_id = ?", projectID).Count(&sessions).Error)
	require.Zero(t, tasks)
	require.Zero(t, sessions)
}

func TestDeleteProjectEnqueuesAllStoredImageObjects(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "delete-project-objects")
	project := createEmptyTestProject(t, user.Id)
	scene := createLegacyTestScene(t, project.ID)
	generation := model.MujianImageGeneration{
		ID: uuid.NewString(), ProjectID: project.ID, UserID: user.Id, Prompt: "delete objects",
		Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1", Status: "succeeded",
		TaskID: model.GenerateTaskID(), ResultObjectKey: "mujian/prod/public/generations/generation/result.png",
	}
	reference := model.MujianImageReference{
		ID: uuid.NewString(), GenerationID: generation.ID, Position: 0, Name: "reference.png",
		MIMEType: "image/png", SizeBytes: int64(len(testPNG(1))), Data: testPNG(1),
		ObjectKey: "mujian/prod/public/references/reference.png",
	}
	shot := model.MujianShot{
		ID: uuid.NewString(), ProjectID: project.ID, SceneID: scene.ID, Sequence: 1,
		ResultObjectKey: "mujian/prod/public/generations/legacy-shot/result.png",
	}
	archivedShot := model.MujianShot{
		ID: uuid.NewString(), ProjectID: project.ID, SceneID: scene.ID, Sequence: 2,
		ResultObjectKey: "mujian/prod/public/generations/archived-shot/result.png",
	}
	require.NoError(t, model.DB.Create(&generation).Error)
	require.NoError(t, model.DB.Create(&reference).Error)
	require.NoError(t, model.DB.Create(&shot).Error)
	require.NoError(t, model.DB.Create(&archivedShot).Error)
	require.NoError(t, model.DB.Delete(&archivedShot).Error)

	require.NoError(t, DeleteProject(user.Id, project.ID))

	var operations []model.MujianObjectOperation
	require.NoError(t, model.DB.Order("object_key").Find(&operations).Error)
	require.Len(t, operations, 4)
	for _, operation := range operations {
		require.Equal(t, model.MujianObjectStatusPendingDelete, operation.Status)
	}
	require.Equal(t, []string{
		"mujian/prod/public/generations/archived-shot/result.png",
		"mujian/prod/public/generations/generation/result.png",
		"mujian/prod/public/generations/legacy-shot/result.png",
		"mujian/prod/public/references/reference.png",
	}, []string{operations[0].ObjectKey, operations[1].ObjectKey, operations[2].ObjectKey, operations[3].ObjectKey})
}

func TestDeleteProjectSerializesImagePersistenceWithoutOrphans(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "delete-project-image-race")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	sessionID := workspace.ActiveSessionID
	existingGeneration := model.MujianImageGeneration{
		ID: uuid.NewString(), ProjectID: project.ID, UserID: user.Id, SessionID: sessionID,
		Prompt: "existing", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		Status: imageGenerationStatusQueued, TaskID: model.GenerateTaskID(),
	}
	existingReference := model.MujianImageReference{
		ID: uuid.NewString(), GenerationID: existingGeneration.ID, Position: 0, Name: "existing.png",
		MIMEType: "image/png", SizeBytes: int64(len(testPNG(1))), Data: testPNG(1),
	}
	existingTask := model.Task{
		TaskID: existingGeneration.TaskID, Platform: constant.TaskPlatformMujianImage, UserId: user.Id,
		Action: imageGenerationAction, Status: model.TaskStatusSubmitted,
	}
	require.NoError(t, model.DB.Create(&existingGeneration).Error)
	require.NoError(t, model.DB.Create(&existingReference).Error)
	require.NoError(t, model.DB.Create(&existingTask).Error)
	require.NoError(t, model.DB.Create(&model.MujianAgentMessage{
		ID: uuid.NewString(), ProjectID: project.ID, UserID: user.Id, SessionID: sessionID,
		Role: "user", Content: "existing", Mode: AgentModeConsult,
	}).Error)

	generation, references, task := newImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: sessionID, Prompt: "racing", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		References: []ImageReferenceInput{{Name: "racing.png", Data: testPNG(2)}},
	})
	started, release := blockFirstProjectClaim(t)
	deleted := make(chan error, 1)
	go func() { deleted <- DeleteProject(user.Id, project.ID) }()
	waitForProjectClaim(t, started)
	persisted := make(chan error, 1)
	go func() { persisted <- persistImageGeneration(generation, references, task) }()
	requireWriteBlocked(t, persisted)
	close(release)
	require.NoError(t, <-deleted)
	require.ErrorIs(t, <-persisted, ErrNotFound)

	var sessions, messages, generations, storedReferences, tasks int64
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).Where("project_id = ?", project.ID).Count(&sessions).Error)
	require.NoError(t, model.DB.Model(&model.MujianAgentMessage{}).Where("project_id = ?", project.ID).Count(&messages).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Where("project_id = ?", project.ID).Count(&generations).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageReference{}).
		Where("generation_id IN ?", []string{existingGeneration.ID, generation.ID}).Count(&storedReferences).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).
		Where("task_id IN ?", []string{existingTask.TaskID, task.TaskID}).Count(&tasks).Error)
	require.Zero(t, sessions)
	require.Zero(t, messages)
	require.Zero(t, generations)
	require.Zero(t, storedReferences)
	require.Zero(t, tasks)
}

func TestDeleteProjectSerializesAgentPersistence(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "delete-project-message-race")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	session, err := agentSessionFromWorkspace(workspace)
	require.NoError(t, err)
	userMessage := model.MujianAgentMessage{
		ID: uuid.NewString(), ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
		Role: "user", Content: "racing", Mode: AgentModeConsult,
	}
	assistant := model.MujianAgentMessage{
		ID: uuid.NewString(), ProjectID: project.ID, UserID: user.Id, SessionID: session.ID,
		Role: "assistant", Content: "racing reply", Mode: AgentModeConsult,
	}

	started, release := blockFirstProjectClaim(t)
	deleted := make(chan error, 1)
	go func() { deleted <- DeleteProject(user.Id, project.ID) }()
	waitForProjectClaim(t, started)
	persisted := make(chan error, 1)
	go func() {
		persisted <- persistAgentMessagePair(
			user.Id, project.ID, session.ID, session.Revision, userMessage.Content, &userMessage, &assistant,
		)
	}()
	requireWriteBlocked(t, persisted)
	close(release)
	require.NoError(t, <-deleted)
	require.ErrorIs(t, <-persisted, ErrNotFound)

	var sessions, messages int64
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).Where("project_id = ?", project.ID).Count(&sessions).Error)
	require.NoError(t, model.DB.Model(&model.MujianAgentMessage{}).Where("project_id = ?", project.ID).Count(&messages).Error)
	require.Zero(t, sessions)
	require.Zero(t, messages)
}

func TestDeleteProjectWinningImageTaskStartSkipsDispatch(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "delete-project-task-start-race")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	generation, references, task := newImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: workspace.ActiveSessionID, Prompt: "must not dispatch", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.NoError(t, persistImageGeneration(generation, references, task))

	originalDispatcher := dispatchProjectImage
	var dispatches atomic.Int32
	dispatchProjectImage = func(projectImageDispatchInput) (projectImageDispatchResult, error) {
		dispatches.Add(1)
		return projectImageDispatchResult{ResultURL: testImageDataURL(31)}, nil
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })

	started, release := blockFirstProjectClaim(t)
	deleted := make(chan error, 1)
	go func() { deleted <- DeleteProject(user.Id, project.ID) }()
	waitForProjectClaim(t, started)
	workerFinished := make(chan error, 1)
	go func() {
		runProjectImageTask(user.Id, project.ID, generation.ID, task.TaskID)
		workerFinished <- nil
	}()
	requireWriteBlocked(t, workerFinished)
	close(release)
	require.NoError(t, <-deleted)
	require.NoError(t, <-workerFinished)
	require.Zero(t, dispatches.Load())

	var generations, tasks int64
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Where("id = ?", generation.ID).Count(&generations).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id = ?", task.TaskID).Count(&tasks).Error)
	require.Zero(t, generations)
	require.Zero(t, tasks)
}

func TestActiveImageLeaseBlocksProjectDeletionUntilDispatchCompletes(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "active-project-image-lease")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	generation, references, task := newImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: workspace.ActiveSessionID, Prompt: "active project lease", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.NoError(t, persistImageGeneration(generation, references, task))

	dispatchEntered := make(chan struct{})
	releaseDispatch := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(releaseDispatch)
		}
	})
	originalDispatcher := dispatchProjectImage
	var dispatches atomic.Int32
	dispatchProjectImage = func(projectImageDispatchInput) (projectImageDispatchResult, error) {
		close(dispatchEntered)
		<-releaseDispatch
		dispatches.Add(1)
		return projectImageDispatchResult{ResultURL: testImageDataURL(32)}, nil
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })

	workerFinished := make(chan struct{})
	go func() {
		runProjectImageTask(user.Id, project.ID, generation.ID, task.TaskID)
		close(workerFinished)
	}()
	select {
	case <-dispatchEntered:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "worker did not reach the pre-dispatch barrier")
	}
	require.Zero(t, dispatches.Load())
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).
		Update("start_time", time.Now().Add(-5*time.Minute).Unix()).Error,
		"a normal two-phase dispatch/materialization window must still hold the lease")
	err = DeleteProject(user.Id, project.ID)
	require.ErrorIs(t, err, ErrConflict)
	require.ErrorIs(t, err, model.ErrMujianImageGenerationActive)
	storedProject, err := GetProject(user.Id, project.ID)
	require.NoError(t, err)
	require.Equal(t, project.ID, storedProject.ID)

	close(releaseDispatch)
	released = true
	select {
	case <-workerFinished:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "worker did not finish after dispatch was released")
	}
	require.Equal(t, int32(1), dispatches.Load())
	succeeded, err := GetImageGeneration(user.Id, project.ID, workspace.ActiveSessionID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, imageGenerationStatusSucceeded, succeeded.Status)

	require.NoError(t, DeleteProject(user.Id, project.ID))
	var projects, sessions, generations, tasks int64
	require.NoError(t, model.DB.Model(&model.MujianProject{}).Where("id = ?", project.ID).Count(&projects).Error)
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).Where("project_id = ?", project.ID).Count(&sessions).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Where("project_id = ?", project.ID).Count(&generations).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id = ?", task.TaskID).Count(&tasks).Error)
	require.Zero(t, projects)
	require.Zero(t, sessions)
	require.Zero(t, generations)
	require.Zero(t, tasks)
}

func TestStaleImageLeaseDoesNotBlockProjectDeletion(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "stale-project-image-lease")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	generation, references, task := newImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: workspace.ActiveSessionID, Prompt: "stale lease", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.NoError(t, persistImageGeneration(generation, references, task))
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"status": model.TaskStatusInProgress, "start_time": time.Now().Add(-model.MujianImageGenerationLeaseTimeout - time.Second).Unix(),
	}).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Where("id = ?", generation.ID).
		Update("status", imageGenerationStatusRunning).Error)

	require.NoError(t, DeleteProject(user.Id, project.ID))
	var projects, generations, tasks int64
	require.NoError(t, model.DB.Model(&model.MujianProject{}).Where("id = ?", project.ID).Count(&projects).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Where("id = ?", generation.ID).Count(&generations).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id = ?", task.TaskID).Count(&tasks).Error)
	require.Zero(t, projects)
	require.Zero(t, generations)
	require.Zero(t, tasks)
}

func TestDeleteUserSerializesImagePersistence(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "delete-user-image-race")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	generation, references, task := newImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: workspace.ActiveSessionID, Prompt: "racing user delete", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
		References: []ImageReferenceInput{{Name: "racing.png", Data: testPNG(3)}},
	})

	started, release := blockFirstProjectClaim(t)
	deleted := make(chan error, 1)
	go func() { deleted <- model.HardDeleteUserById(user.Id) }()
	waitForProjectClaim(t, started)
	persisted := make(chan error, 1)
	go func() { persisted <- persistImageGeneration(generation, references, task) }()
	requireWriteBlocked(t, persisted)
	close(release)
	require.NoError(t, <-deleted)
	require.ErrorIs(t, <-persisted, ErrNotFound)

	var users, projects, sessions, generations, storedReferences, tasks int64
	require.NoError(t, model.DB.Unscoped().Model(&model.User{}).Where("id = ?", user.Id).Count(&users).Error)
	require.NoError(t, model.DB.Unscoped().Model(&model.MujianProject{}).Where("user_id = ?", user.Id).Count(&projects).Error)
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).Where("user_id = ?", user.Id).Count(&sessions).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Where("user_id = ?", user.Id).Count(&generations).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageReference{}).Where("generation_id = ?", generation.ID).Count(&storedReferences).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id = ?", task.TaskID).Count(&tasks).Error)
	require.Zero(t, users)
	require.Zero(t, projects)
	require.Zero(t, sessions)
	require.Zero(t, generations)
	require.Zero(t, storedReferences)
	require.Zero(t, tasks)
}

func TestActiveImageLeaseBlocksUserDeletionUntilDispatchCompletes(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "active-user-image-lease")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := GetWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	generation, references, task := newImageGeneration(user.Id, project.ID, CreateImageGenerationInput{
		SessionID: workspace.ActiveSessionID, Prompt: "active user lease", Engine: "nano", ModelID: "nano-banana", AspectRatio: "1:1",
	})
	require.NoError(t, persistImageGeneration(generation, references, task))

	dispatchEntered := make(chan struct{})
	releaseDispatch := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(releaseDispatch)
		}
	})
	originalDispatcher := dispatchProjectImage
	var dispatches atomic.Int32
	dispatchProjectImage = func(projectImageDispatchInput) (projectImageDispatchResult, error) {
		close(dispatchEntered)
		<-releaseDispatch
		dispatches.Add(1)
		return projectImageDispatchResult{ResultURL: testImageDataURL(33)}, nil
	}
	t.Cleanup(func() { dispatchProjectImage = originalDispatcher })

	workerFinished := make(chan struct{})
	go func() {
		runProjectImageTask(user.Id, project.ID, generation.ID, task.TaskID)
		close(workerFinished)
	}()
	select {
	case <-dispatchEntered:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "worker did not reach the pre-dispatch barrier")
	}
	require.Zero(t, dispatches.Load())
	err = model.HardDeleteUserById(user.Id)
	require.ErrorIs(t, err, model.ErrMujianImageGenerationActive)
	var users, projects int64
	require.NoError(t, model.DB.Unscoped().Model(&model.User{}).Where("id = ?", user.Id).Count(&users).Error)
	require.NoError(t, model.DB.Unscoped().Model(&model.MujianProject{}).Where("id = ?", project.ID).Count(&projects).Error)
	require.Equal(t, int64(1), users)
	require.Equal(t, int64(1), projects)

	close(releaseDispatch)
	released = true
	select {
	case <-workerFinished:
	case <-time.After(3 * time.Second):
		require.FailNow(t, "worker did not finish after dispatch was released")
	}
	require.Equal(t, int32(1), dispatches.Load())
	succeeded, err := GetImageGeneration(user.Id, project.ID, workspace.ActiveSessionID, generation.ID)
	require.NoError(t, err)
	require.Equal(t, imageGenerationStatusSucceeded, succeeded.Status)

	require.NoError(t, model.HardDeleteUserById(user.Id))
	var sessions, generations, tasks int64
	require.NoError(t, model.DB.Unscoped().Model(&model.User{}).Where("id = ?", user.Id).Count(&users).Error)
	require.NoError(t, model.DB.Unscoped().Model(&model.MujianProject{}).Where("user_id = ?", user.Id).Count(&projects).Error)
	require.NoError(t, model.DB.Model(&model.MujianAgentSession{}).Where("user_id = ?", user.Id).Count(&sessions).Error)
	require.NoError(t, model.DB.Model(&model.MujianImageGeneration{}).Where("user_id = ?", user.Id).Count(&generations).Error)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id = ?", task.TaskID).Count(&tasks).Error)
	require.Zero(t, users)
	require.Zero(t, projects)
	require.Zero(t, sessions)
	require.Zero(t, generations)
	require.Zero(t, tasks)
}

func TestCreditsFromQuota(t *testing.T) {
	require.InDelta(t, 1280, CreditsFromQuota(InitialQuota), 0.01)
	require.Equal(t, float64(CreditsPerUSD), CreditsFromQuota(QuotaPerUSD))
}

func TestCherryStudioConfigUsesUserTokenAndRefreshesModelLimits(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "cherry-user")
	require.NoError(t, EnsureOnboarded(user.Id))

	var token model.Token
	require.NoError(t, model.DB.Where("user_id = ? AND name = ?", user.Id, model.MujianInternalTokenName).First(&token).Error)
	require.NoError(t, model.DB.Model(&token).Updates(map[string]interface{}{
		"model_limits_enabled": false,
		"model_limits":         "stale-model",
	}).Error)
	channel := model.Channel{Name: "test-provider", Key: "provider-secret", Status: 1}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "deepseek-v4-flash", UpstreamModelID: "deepseek-v4-flash",
		Provider: "test", BillingType: model.ChannelModelBillingToken, InputPrice: 0.1, OutputPrice: 0.2,
		Currency: "USD", Available: true,
	}).Error)

	config, err := GetCherryStudioConfig(user.Id)
	require.NoError(t, err)
	require.Equal(t, "sk-"+token.Key, config.APIKey)
	require.Equal(t, token.Key[len(token.Key)-4:], config.TokenLast4)
	require.Equal(t, []string{"deepseek-v4-flash"}, config.Models)
	require.Equal(t, "deepseek-v4-flash", config.DefaultModel)

	require.NoError(t, model.DB.First(&token, token.Id).Error)
	require.True(t, token.ModelLimitsEnabled)
	require.Equal(t, "deepseek-v4-flash", token.ModelLimits)
	visibleTokens, err := model.GetAllUserTokens(user.Id, 0, 20)
	require.NoError(t, err)
	require.Empty(t, visibleTokens)
}

func TestAgentProposalApplyAndUndoAreSingleUse(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "proposal-owner")
	projects, err := ListProjects(user.Id)
	require.NoError(t, err)
	createLegacyTestScene(t, projects[0].ID)
	workspace, err := getLegacyWorkspace(user.Id, projects[0].ID)
	require.NoError(t, err)
	scene := workspace.Scenes[0]
	proposal := AgentProposal{
		Summary: "调整场景标题", TargetType: "scene", TargetID: scene.ID,
		ExpectedVersion: scene.Version, Changes: map[string]string{"title": "修改后的标题"},
	}
	proposalJSON, err := common.Marshal(proposal)
	require.NoError(t, err)
	message := model.MujianAgentMessage{
		ID: "message-1", ProjectID: projects[0].ID, UserID: user.Id, Role: "assistant",
		Content: proposal.Summary, Proposal: string(proposalJSON), ApplyStatus: "pending",
	}
	require.NoError(t, model.DB.Create(&message).Error)

	applied, err := ApplyAgentMessage(user.Id, projects[0].ID, message.ID)
	require.NoError(t, err)
	require.Equal(t, "修改后的标题", applied.Scenes[0].Title)
	require.Equal(t, scene.Version+1, applied.Scenes[0].Version)
	_, err = ApplyAgentMessage(user.Id, projects[0].ID, message.ID)
	require.ErrorIs(t, err, ErrNotFound)

	undone, err := UndoAgentMessage(user.Id, projects[0].ID, message.ID)
	require.NoError(t, err)
	require.Equal(t, scene.Title, undone.Scenes[0].Title)
	require.Equal(t, scene.Version+2, undone.Scenes[0].Version)
	_, err = UndoAgentMessage(user.Id, projects[0].ID, message.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSceneBatchProposalApplyCreatesServerOwnedScenesAtomically(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "scene-batch-owner")
	project := createEmptyTestProject(t, user.Id)
	expectedScenes := validSceneBatch()
	message := createPendingSceneBatchMessage(t, user.Id, project.ID, "scene-batch-apply", expectedScenes)

	workspace, err := ApplyAgentMessage(user.Id, project.ID, message.ID)

	require.NoError(t, err)
	require.Len(t, workspace.Scenes, 2)
	seenIDs := map[string]bool{}
	for index, scene := range workspace.Scenes {
		require.NotEmpty(t, scene.ID)
		require.False(t, seenIDs[scene.ID])
		seenIDs[scene.ID] = true
		require.Equal(t, project.ID, scene.ProjectID)
		require.Equal(t, 1, scene.Episode)
		require.Equal(t, index+1, scene.SceneNumber)
		require.Equal(t, 1, scene.Version)
		require.Equal(t, expectedScenes[index].Title, scene.Title)
		require.Equal(t, expectedScenes[index].Environment, scene.Environment)
		require.Equal(t, expectedScenes[index].Action, scene.Action)
		expectedDialoguesJSON, marshalErr := common.Marshal(expectedScenes[index].Dialogues)
		require.NoError(t, marshalErr)
		require.JSONEq(t, string(expectedDialoguesJSON), scene.Dialogues)
		var dialogues []AgentProposedDialogue
		require.NoError(t, common.UnmarshalJsonStr(scene.Dialogues, &dialogues))
		require.Equal(t, expectedScenes[index].Dialogues, dialogues)
		if len(expectedScenes[index].Dialogues) == 0 {
			require.Equal(t, "[]", scene.Dialogues)
		}
	}
	require.NoError(t, model.DB.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, "applied", message.ApplyStatus)
	var state undoState
	require.NoError(t, common.UnmarshalJsonStr(message.PreviousValues, &state))
	require.ElementsMatch(t, mapKeys(seenIDs), state.CreatedSceneIDs)

	_, err = ApplyAgentMessage(user.Id, project.ID, message.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSceneBatchProposalRejectsInvalidBatchWithoutPartialWrites(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "scene-batch-atomic-owner")
	project := createEmptyTestProject(t, user.Id)
	scenes := validSceneBatch()
	for index := len(scenes); index < 9; index++ {
		scenes = append(scenes, AgentSceneProposal{
			Title: fmt.Sprintf("额外场景 %d", index), Environment: "环境", Action: "动作",
			Dialogues: []AgentProposedDialogue{},
		})
	}
	message := createPendingSceneBatchMessage(t, user.Id, project.ID, "scene-batch-invalid", scenes)

	_, err := ApplyAgentMessage(user.Id, project.ID, message.ID)

	require.Error(t, err)
	var sceneCount int64
	require.NoError(t, model.DB.Model(&model.MujianScene{}).Where("project_id = ?", project.ID).Count(&sceneCount).Error)
	require.Zero(t, sceneCount)
	require.NoError(t, model.DB.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, "pending", message.ApplyStatus)
}

func TestSceneBatchProposalApplyRejectsMessageOutsideScriptExecution(t *testing.T) {
	tests := []struct {
		name    string
		updates map[string]interface{}
	}{
		{name: "consult mode", updates: map[string]interface{}{"mode": AgentModeConsult}},
		{name: "non-script skill", updates: map[string]interface{}{"skill": "分镜导演"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setupTestDB(t)
			user := createTestUser(t, "scene-batch-boundary-owner")
			project := createEmptyTestProject(t, user.Id)
			message := createPendingSceneBatchMessage(t, user.Id, project.ID, "scene-batch-wrong-boundary", validSceneBatch())
			require.NoError(t, model.DB.Model(&message).Updates(test.updates).Error)

			_, err := ApplyAgentMessage(user.Id, project.ID, message.ID)

			require.Error(t, err)
			var sceneCount int64
			require.NoError(t, model.DB.Model(&model.MujianScene{}).Where("project_id = ?", project.ID).Count(&sceneCount).Error)
			require.Zero(t, sceneCount)
		})
	}
}

func TestSceneBatchProposalDatabaseFailureRollsBackEntireBatch(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "scene-batch-db-rollback-owner")
	project := createEmptyTestProject(t, user.Id)
	message := createPendingSceneBatchMessage(t, user.Id, project.ID, "scene-batch-db-rollback", validSceneBatch())
	require.NoError(t, model.DB.Exec(fmt.Sprintf(`
		CREATE TRIGGER fail_second_scene
		BEFORE INSERT ON mujian_scenes
		WHEN NEW.project_id = '%s' AND NEW.scene_number = 2
		BEGIN SELECT RAISE(ABORT, 'forced scene insert failure'); END
	`, project.ID)).Error)

	_, err := ApplyAgentMessage(user.Id, project.ID, message.ID)

	require.Error(t, err)
	var sceneCount int64
	require.NoError(t, model.DB.Model(&model.MujianScene{}).Where("project_id = ?", project.ID).Count(&sceneCount).Error)
	require.Zero(t, sceneCount)
	require.NoError(t, model.DB.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, "pending", message.ApplyStatus)
}

func TestSceneBatchProposalConflictsWhenProjectNoLongerEmpty(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "scene-batch-conflict-owner")
	project := createEmptyTestProject(t, user.Id)
	first := createPendingSceneBatchMessage(t, user.Id, project.ID, "scene-batch-first", validSceneBatch())
	second := createPendingSceneBatchMessage(t, user.Id, project.ID, "scene-batch-second", []AgentSceneProposal{
		{Title: "不应创建", Environment: "环境", Action: "动作", Dialogues: []AgentProposedDialogue{}},
	})

	workspace, err := ApplyAgentMessage(user.Id, project.ID, first.ID)
	require.NoError(t, err)
	require.Len(t, workspace.Scenes, 2)

	_, err = ApplyAgentMessage(user.Id, project.ID, second.ID)
	require.ErrorIs(t, err, ErrConflict)
	workspace, err = getLegacyWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	require.Len(t, workspace.Scenes, 2)
	require.NoError(t, model.DB.First(&second, "id = ?", second.ID).Error)
	require.Equal(t, "pending", second.ApplyStatus)
}

func TestSceneBatchProposalUndoDeletesAllUnmodifiedScenes(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "scene-batch-undo-owner")
	project := createEmptyTestProject(t, user.Id)
	message := createPendingSceneBatchMessage(t, user.Id, project.ID, "scene-batch-undo", validSceneBatch())

	applied, err := ApplyAgentMessage(user.Id, project.ID, message.ID)
	require.NoError(t, err)
	require.Len(t, applied.Scenes, 2)
	undone, err := UndoAgentMessage(user.Id, project.ID, message.ID)

	require.NoError(t, err)
	require.Empty(t, undone.Scenes)
	require.NoError(t, model.DB.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, "undone", message.ApplyStatus)
	_, err = UndoAgentMessage(user.Id, project.ID, message.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestSceneBatchProposalUndoKeepsWholeBatchWhenSceneWasModified(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "scene-batch-modified-owner")
	project := createEmptyTestProject(t, user.Id)
	message := createPendingSceneBatchMessage(t, user.Id, project.ID, "scene-batch-modified", validSceneBatch())
	applied, err := ApplyAgentMessage(user.Id, project.ID, message.ID)
	require.NoError(t, err)
	modified := applied.Scenes[0]
	modified.Title = "用户后续修改的标题"
	_, err = UpdateWorkspace(user.Id, project.ID, []model.MujianScene{modified})
	require.NoError(t, err)

	_, err = UndoAgentMessage(user.Id, project.ID, message.ID)

	require.ErrorIs(t, err, ErrConflict)
	workspace, err := getLegacyWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	require.Len(t, workspace.Scenes, 2)
	require.Equal(t, "用户后续修改的标题", workspace.Scenes[0].Title)
	require.Equal(t, validSceneBatch()[1].Title, workspace.Scenes[1].Title)
	require.NoError(t, model.DB.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, "applied", message.ApplyStatus)
}

func TestSceneBatchProposalUndoKeepsWholeBatchWhenSceneHasShot(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "scene-batch-shot-owner")
	project := createEmptyTestProject(t, user.Id)
	message := createPendingSceneBatchMessage(t, user.Id, project.ID, "scene-batch-shot", validSceneBatch())
	applied, err := ApplyAgentMessage(user.Id, project.ID, message.ID)
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.MujianShot{
		ID: "created-scene-shot", ProjectID: project.ID, SceneID: applied.Scenes[0].ID,
		Sequence: 1, ShotType: "全景", DurationSeconds: 3, Version: 1,
	}).Error)

	_, err = UndoAgentMessage(user.Id, project.ID, message.ID)

	require.ErrorIs(t, err, ErrConflict)
	workspace, err := getLegacyWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	require.Len(t, workspace.Scenes, 2)
	require.Len(t, workspace.Shots, 1)
	require.NoError(t, model.DB.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, "applied", message.ApplyStatus)
}

func TestSceneBatchProposalApplyAndUndoRejectOtherUsers(t *testing.T) {
	setupTestDB(t)
	owner := createTestUser(t, "scene-batch-isolation-owner")
	other := createTestUser(t, "scene-batch-isolation-other")
	project := createEmptyTestProject(t, owner.Id)
	message := createPendingSceneBatchMessage(t, owner.Id, project.ID, "scene-batch-isolation", validSceneBatch())

	_, err := ApplyAgentMessage(other.Id, project.ID, message.ID)
	require.ErrorIs(t, err, ErrNotFound)
	workspace, err := ApplyAgentMessage(owner.Id, project.ID, message.ID)
	require.NoError(t, err)
	require.Len(t, workspace.Scenes, 2)

	_, err = UndoAgentMessage(other.Id, project.ID, message.ID)
	require.ErrorIs(t, err, ErrNotFound)
	workspace, err = getLegacyWorkspace(owner.Id, project.ID)
	require.NoError(t, err)
	require.Len(t, workspace.Scenes, 2)
	require.NoError(t, model.DB.First(&message, "id = ?", message.ID).Error)
	require.Equal(t, "applied", message.ApplyStatus)
}

func TestAgentProposalRequiresTargetVersion(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "proposal-version")
	projects, err := ListProjects(user.Id)
	require.NoError(t, err)
	createLegacyTestScene(t, projects[0].ID)
	workspace, err := getLegacyWorkspace(user.Id, projects[0].ID)
	require.NoError(t, err)

	proposal := AgentProposal{
		Summary: "调整场景标题", TargetType: "scene", TargetID: workspace.Scenes[0].ID,
		Changes: map[string]string{"title": "修改后的标题"},
	}

	require.EqualError(t, validateProposal(user.Id, projects[0].ID, &proposal), "Agent 提案缺少目标版本")
}

func TestValidateSceneBatchProposalRequiresNativeCompleteDialogues(t *testing.T) {
	valid := AgentProposal{
		Kind: createScenesProposalKind, Summary: "生成第一集",
		Scenes: []AgentSceneProposal{{
			Title: "场景", Environment: "环境", Action: "动作",
			Dialogues: []AgentProposedDialogue{{Character: "主角", Line: "台词"}},
		}},
	}
	require.NoError(t, validateSceneBatchProposal(&valid))
	emptyDialogues := valid
	emptyDialogues.Scenes = []AgentSceneProposal{{
		Title: "无对白场景", Environment: "环境", Action: "动作", Dialogues: []AgentProposedDialogue{},
	}}
	require.NoError(t, validateSceneBatchProposal(&emptyDialogues))

	tests := []struct {
		name     string
		proposal AgentProposal
	}{
		{name: "missing summary", proposal: AgentProposal{Kind: createScenesProposalKind, Scenes: valid.Scenes}},
		{name: "no scenes", proposal: AgentProposal{Kind: createScenesProposalKind, Summary: "空", Scenes: nil}},
		{name: "missing title", proposal: AgentProposal{Kind: createScenesProposalKind, Summary: "空", Scenes: []AgentSceneProposal{{Environment: "环境", Action: "动作", Dialogues: []AgentProposedDialogue{}}}}},
	}
	tooMany := valid
	tooMany.Scenes = make([]AgentSceneProposal, 9)
	for index := range tooMany.Scenes {
		tooMany.Scenes[index] = valid.Scenes[0]
	}
	tests = append(tests, struct {
		name     string
		proposal AgentProposal
	}{name: "too many scenes", proposal: tooMany})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, validateSceneBatchProposal(&test.proposal))
		})
	}

	wireTests := []struct {
		name        string
		raw         string
		decodeError bool
	}{
		{name: "missing dialogues", raw: `{"summary":"空","scenes":[{"title":"场景","environment":"环境","action":"动作"}]}`},
		{name: "null dialogues", raw: `{"summary":"空","scenes":[{"title":"场景","environment":"环境","action":"动作","dialogues":null}]}`},
		{name: "missing character", raw: `{"summary":"空","scenes":[{"title":"场景","environment":"环境","action":"动作","dialogues":[{"line":"台词"}]}]}`},
		{name: "missing line", raw: `{"summary":"空","scenes":[{"title":"场景","environment":"环境","action":"动作","dialogues":[{"character":"主角"}]}]}`},
		{name: "string encoded dialogues", raw: `{"summary":"空","scenes":[{"title":"场景","environment":"环境","action":"动作","dialogues":"[]"}]}`, decodeError: true},
	}
	for _, test := range wireTests {
		t.Run(test.name, func(t *testing.T) {
			var proposal AgentProposal
			err := common.Unmarshal([]byte(test.raw), &proposal)
			if test.decodeError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			proposal.Kind = createScenesProposalKind
			require.Error(t, validateSceneBatchProposal(&proposal))
		})
	}
}

func TestParseAgentResponseAllowsInformationalReply(t *testing.T) {
	response, err := parseAgentResponse(`{"reply":"你好，我可以帮你调整剧本或分镜。","proposal":null}`)

	require.NoError(t, err)
	require.Equal(t, "你好，我可以帮你调整剧本或分镜。", response.Reply)
	require.Nil(t, response.Proposal)
}

func TestParseAgentResponseValidatesReplyAndProposalSummary(t *testing.T) {
	response, err := parseAgentResponse("```json\n{\"reply\":\"已准备好修改。\",\"proposal\":{\"summary\":\"调整场景标题\",\"target_type\":\"scene\",\"target_id\":\"scene-1\",\"expected_version\":1,\"changes\":{\"title\":\"新的标题\"}}}\n```")

	require.NoError(t, err)
	require.Equal(t, "调整场景标题", response.Proposal.Summary)
	require.Equal(t, "新的标题", response.Proposal.Changes["title"])

	_, err = parseAgentResponse(`{"reply":"","proposal":null}`)
	require.EqualError(t, err, "Agent 回复内容为空")

	_, err = parseAgentResponse(`{"reply":"已准备好修改。","proposal":{"summary":"","target_type":"scene","target_id":"scene-1","expected_version":1,"changes":{"title":"新的标题"}}}`)
	require.EqualError(t, err, "Agent 提案缺少摘要")
}

func TestLegacySendAgentMessageUsesSanitizedConversationHistory(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "legacy-history-owner")
	project := createEmptyTestProject(t, user.Id)
	workspace, err := getLegacyWorkspace(user.Id, project.ID)
	require.NoError(t, err)
	sessionID := workspace.ActiveSessionID
	reasoning := "legacy-private-reasoning"
	history := []model.MujianAgentMessage{
		{
			ID: "legacy-history-user", ProjectID: project.ID, UserID: user.Id, SessionID: sessionID, Role: "user",
			Content: "上一轮的完整概要\n📎 legacy.txt", Reasoning: &reasoning, Skill: "短剧编剧",
			Mode: AgentModeExecute, Proposal: `{"private":"proposal"}`, ApplyStatus: "none",
			RelayRequestID: "legacy-private-request", CreatedAt: 1,
		},
		{
			ID: "legacy-history-assistant", ProjectID: project.ID, UserID: user.Id, SessionID: sessionID, Role: "assistant",
			Content: "我已记住设定。", Reasoning: &reasoning, Skill: "短剧编剧",
			Mode: AgentModeExecute, ApplyStatus: "none", CreatedAt: 2,
		},
	}
	require.NoError(t, model.DB.Create(&history).Error)
	require.NoError(t, model.DB.Create(&model.MujianAgentSession{
		ID: "other-secret-session", ProjectID: project.ID, UserID: user.Id,
		Title: "其他会话的隐藏标题", Revision: 1,
	}).Error)
	require.NoError(t, model.DB.Create(&[]model.MujianAgentMessage{
		{
			ID: "other-secret-user", ProjectID: project.ID, UserID: user.Id, SessionID: "other-secret-session",
			Role: "user", Content: "其他会话的秘密用户消息", Mode: AgentModeExecute, CreatedAt: 3,
		},
		{
			ID: "other-secret-assistant", ProjectID: project.ID, UserID: user.Id, SessionID: "other-secret-session",
			Role: "assistant", Content: "其他会话的秘密助手消息", Mode: AgentModeExecute, CreatedAt: 4,
		},
	}).Error)
	channel := model.Channel{Name: "legacy-history-provider", Key: "provider-key", Status: 1}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.ChannelModelPrice{
		ChannelID: channel.Id, CatalogID: "gpt-5.6-terra", UpstreamModelID: "gpt-5.6-terra",
		Provider: "test", BillingType: model.ChannelModelBillingToken, InputPrice: 0.1, OutputPrice: 0.2,
		Currency: "USD", Available: true,
	}).Error)

	type capturedRequest struct {
		payload map[string]interface{}
		err     error
	}
	requestPayload := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]interface{}
		decodeErr := common.DecodeJson(request.Body, &payload)
		requestPayload <- capturedRequest{payload: payload, err: decodeErr}
		if decodeErr != nil {
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"choices":[{"message":{"content":"已按上文继续。"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	defer server.Close()
	t.Setenv("MUJIAN_RELAY_BASE_URL", server.URL)

	result, err := SendAgentMessage(user.Id, project.ID, "按照上文继续", "短剧编剧", "gpt-5.6-terra", sessionID)

	require.NoError(t, err)
	require.Equal(t, "已按上文继续。", result.Message.Content)
	require.Equal(t, sessionID, result.SessionID)
	require.Equal(t, sessionID, result.UserMessage.SessionID)
	require.Equal(t, sessionID, result.Message.SessionID)
	captured := <-requestPayload
	require.NoError(t, captured.err)
	payload := captured.payload
	require.Equal(t, float64(agentDefaultMaxTokens), payload["max_tokens"])
	require.NotContains(t, payload, "tools")
	rawMessages, ok := payload["messages"].([]interface{})
	require.True(t, ok)
	require.Len(t, rawMessages, 4)
	expectedRoles := []string{"system", "user", "assistant", "user"}
	expectedContents := []string{"", history[0].Content, history[1].Content, "按照上文继续"}
	for index, rawMessage := range rawMessages {
		message, ok := rawMessage.(map[string]interface{})
		require.True(t, ok)
		require.Equal(t, expectedRoles[index], message["role"])
		if index > 0 {
			require.Equal(t, expectedContents[index], message["content"])
			require.Len(t, message, 2)
		}
	}
	require.NotContains(t, rawMessages[0].(map[string]interface{})["content"], history[0].Content)
	encodedPayload, err := common.Marshal(payload)
	require.NoError(t, err)
	for _, privateValue := range []string{
		reasoning, "legacy-private-request", `\"private\":\"proposal\"`, "legacy-history-user",
		"其他会话的隐藏标题", "其他会话的秘密用户消息", "其他会话的秘密助手消息",
	} {
		require.NotContains(t, string(encodedPayload), privateValue)
	}
}

func TestFinishImageTaskUpdatesTaskAndShotAtomically(t *testing.T) {
	setupTestDB(t)
	task := model.Task{
		TaskID: "task-image-success", UserId: 1, Status: model.TaskStatusInProgress,
		Platform: "mujian-image", Action: "mujian-image",
	}
	require.NoError(t, model.DB.Create(&task).Error)
	shot := model.MujianShot{ID: "shot-success", ProjectID: "project-success", Status: "generating", GenerationTaskID: task.TaskID}
	require.NoError(t, model.DB.Create(&shot).Error)

	err := finishImageTaskSuccess(&task, shot.ID, "nano-banana", "https://example.com/image.png", "request-1")

	require.NoError(t, err)
	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusSuccess), task.Status)
	require.Equal(t, "https://example.com/image.png", task.PrivateData.ResultURL)
	require.NoError(t, model.DB.First(&shot, "id = ?", shot.ID).Error)
	require.Equal(t, "completed", shot.Status)
	require.Equal(t, "https://example.com/image.png", shot.ResultURL)
	require.ErrorIs(t, finishImageTaskSuccess(&task, shot.ID, "nano-banana", "other", "request-2"), ErrConflict)
}

func TestFinishImageTaskFailureCannotOverwriteSuccess(t *testing.T) {
	setupTestDB(t)
	task := model.Task{
		TaskID: "task-image-final", UserId: 1, Status: model.TaskStatusSuccess,
		Platform: "mujian-image", Action: "mujian-image",
	}
	require.NoError(t, model.DB.Create(&task).Error)
	shot := model.MujianShot{ID: "shot-final", ProjectID: "project-final", Status: "completed", GenerationTaskID: task.TaskID}
	require.NoError(t, model.DB.Create(&shot).Error)

	finishImageTaskFailure(&task, shot.ID, "late failure")

	require.NoError(t, model.DB.First(&shot, "id = ?", shot.ID).Error)
	require.Equal(t, "completed", shot.Status)
}

func TestFinishImageTaskFailureUpdatesTaskAndShotTogether(t *testing.T) {
	setupTestDB(t)
	task := model.Task{
		TaskID: "task-image-failure", UserId: 1, Status: model.TaskStatusInProgress,
		Platform: "mujian-image", Action: "mujian-image",
	}
	require.NoError(t, model.DB.Create(&task).Error)
	shot := model.MujianShot{ID: "shot-failure", ProjectID: "project-failure", Status: "generating", GenerationTaskID: task.TaskID}
	require.NoError(t, model.DB.Create(&shot).Error)

	finishImageTaskFailure(&task, shot.ID, "provider unavailable")

	require.NoError(t, model.DB.First(&task, task.ID).Error)
	require.Equal(t, model.TaskStatus(model.TaskStatusFailure), task.Status)
	require.NoError(t, model.DB.First(&shot, "id = ?", shot.ID).Error)
	require.Equal(t, "failed", shot.Status)
	require.Equal(t, "provider unavailable", shot.GenerationError)
}
