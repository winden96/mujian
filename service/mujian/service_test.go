package mujian

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Log{}, &model.Ability{},
		&model.MujianProject{}, &model.MujianScene{}, &model.MujianShot{},
		&model.MujianAgentMessage{}, &model.MujianUserPreference{},
	))
}

func createTestUser(t *testing.T, username string) model.User {
	t.Helper()
	user := model.User{Username: username, Password: "hashed-password", DisplayName: username, AffCode: username, Quota: 0}
	require.NoError(t, model.DB.Create(&user).Error)
	return user
}

func TestEnsureOnboardedIsIdempotentAndHidesInternalToken(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "creator")

	require.NoError(t, EnsureOnboarded(user.Id))
	require.NoError(t, EnsureOnboarded(user.Id))

	var projectCount, tokenCount int64
	require.NoError(t, model.DB.Model(&model.MujianProject{}).Where("user_id = ?", user.Id).Count(&projectCount).Error)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ? AND name = ?", user.Id, model.MujianInternalTokenName).Count(&tokenCount).Error)
	require.Equal(t, int64(3), projectCount)
	require.Equal(t, int64(1), tokenCount)

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

func TestProjectIsolationAndOptimisticLock(t *testing.T) {
	setupTestDB(t)
	owner := createTestUser(t, "owner")
	other := createTestUser(t, "other")
	projects, err := ListProjects(owner.Id)
	require.NoError(t, err)
	require.NotEmpty(t, projects)

	_, err = GetProject(other.Id, projects[0].ID)
	require.ErrorIs(t, err, ErrNotFound)

	workspace, err := GetWorkspace(owner.Id, projects[0].ID)
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
	require.NoError(t, model.HardDeleteUserById(user.Id))

	var projects, preferences, tokens int64
	require.NoError(t, model.DB.Unscoped().Model(&model.MujianProject{}).Where("user_id = ?", user.Id).Count(&projects).Error)
	require.NoError(t, model.DB.Unscoped().Model(&model.MujianUserPreference{}).Where("user_id = ?", user.Id).Count(&preferences).Error)
	require.NoError(t, model.DB.Unscoped().Model(&model.Token{}).Where("user_id = ? AND name = ?", user.Id, model.MujianInternalTokenName).Count(&tokens).Error)
	require.Zero(t, projects)
	require.Zero(t, preferences)
	require.Zero(t, tokens)
}

func TestCreditsFromQuota(t *testing.T) {
	require.InDelta(t, 1280, CreditsFromQuota(InitialQuota), 0.01)
	require.Equal(t, float64(CreditsPerUSD), CreditsFromQuota(QuotaPerUSD))
}

func TestDemoRechargeIsUnavailableInProduction(t *testing.T) {
	t.Setenv("MUJIAN_PRODUCTION", "true")
	t.Setenv("MUJIAN_DEMO_RECHARGE_ENABLED", "true")

	_, err := DemoRecharge(1, 100)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected production recharge to return ErrNotFound, got %v", err)
	}
}

func TestAgentProposalApplyAndUndoAreSingleUse(t *testing.T) {
	setupTestDB(t)
	user := createTestUser(t, "proposal-owner")
	projects, err := ListProjects(user.Id)
	require.NoError(t, err)
	workspace, err := GetWorkspace(user.Id, projects[0].ID)
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
