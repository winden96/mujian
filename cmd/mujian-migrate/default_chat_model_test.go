package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/mujianconfig"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDefaultChatModelDryRunSelectsOnlyExactLegacyDefault(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 1, legacyDefaultChatModel)
	createDefaultModelPreference(t, db, 2, newDefaultChatModel)
	createDefaultModelPreference(t, db, 3, "deepseek-v4-flash")

	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}
	plan, err := engine.planDefaultChatModel(context.Background(), false)
	require.NoError(t, err)
	require.True(t, plan.DryRun)
	require.Len(t, plan.Entries, 1)

	report := plan.report()
	require.True(t, report.DryRun)
	require.Equal(t, 1, report.Summary["would_update"])
	require.Equal(t, 1, report.Entries[0].UserID)
	require.Equal(t, legacyDefaultChatModel, report.Entries[0].From)
	require.Equal(t, newDefaultChatModel, report.Entries[0].To)
	require.Equal(t, legacyDefaultChatModel, readDefaultModelPreference(t, db, 1))

	encoded, err := common.Marshal(report)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"user_id":1`)
	require.Contains(t, string(encoded), `"from":"claude-opus-5"`)
	require.Contains(t, string(encoded), `"to":"claude-sonnet-4-6"`)
}

func TestDefaultChatModelApplyUsesCompareAndSwap(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 10, legacyDefaultChatModel)
	createDefaultModelPreference(t, db, 20, legacyDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}

	plan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	require.False(t, plan.DryRun)
	require.Equal(t, 2, plan.report().Summary["will_update"])

	require.NoError(t, db.Model(&model.MujianUserPreference{}).
		Where("user_id = ?", 20).Update("default_chat_model", "gpt-5.6-sol").Error)
	report, err := engine.runDefaultModelPlan(context.Background(), filepath.Join(t.TempDir(), "apply.json"), plan)
	require.NoError(t, err)
	require.Equal(t, defaultModelReportVersion, report.Version)
	require.NotEmpty(t, report.OperationID)
	require.NotEmpty(t, report.CommitDigest)
	require.Equal(t, 1, report.Summary["updated"])
	require.Equal(t, 1, report.Summary["skipped_changed"])
	require.Equal(t, newDefaultChatModel, readDefaultModelPreference(t, db, 10))
	require.Equal(t, "gpt-5.6-sol", readDefaultModelPreference(t, db, 20))
	require.Equal(t, defaultModelCommittedValue(report.CommitDigest), readDefaultModelMarker(t, db, report.OperationID))
	var gate model.Option
	require.NoError(t, db.First(&gate, "key = ?", "_mujian_default_chat_model_auto:gate").Error)
	require.Equal(t, "open:"+report.OperationID, gate.Value)
	var ledgerCount int64
	require.NoError(t, db.Model(&model.Option{}).
		Where("key LIKE ?", "_mujian_default_chat_model_auto:sonnet-4-6:%").Count(&ledgerCount).Error)
	require.Equal(t, int64(64), ledgerCount)
}

func TestDefaultChatModelForwardReopensAClosedAssignmentGate(t *testing.T) {
	db := newMigrationTestDB(t)
	previousOperationID := uuid.NewString()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := mujianconfig.OpenAutomaticDefaultChatModelAssignments(tx, previousOperationID); err != nil {
			return err
		}
		return mujianconfig.ConsumeAutomaticDefaultChatModelUsers(tx, previousOperationID, nil)
	}))
	createDefaultModelPreference(t, db, 15, legacyDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}

	plan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	_, err = engine.runDefaultModelPlan(context.Background(), filepath.Join(t.TempDir(), "reopen.json"), plan)
	require.NoError(t, err)
	require.Equal(t, newDefaultChatModel, readDefaultModelPreference(t, db, 15))
	userIDs, err := mujianconfig.AutomaticDefaultChatModelUserIDs(db, plan.OperationID)
	require.NoError(t, err)
	require.Equal(t, []int{15}, userIDs)
}

func TestDefaultChatModelForwardSkipsUsersWhoseGroupCannotRouteSonnet(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 21, legacyDefaultChatModel)
	createDefaultModelPreference(t, db, 22, legacyDefaultChatModel)
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", 22).Update("group", "isolated").Error)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}

	plan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	require.Len(t, plan.Entries, 2)
	require.Equal(t, "will_update", plan.Entries[0].Status)
	require.Equal(t, "skipped_unavailable", plan.Entries[1].Status)
	require.Equal(t, "isolated", plan.Entries[1].Change.Group)

	report, err := engine.runDefaultModelPlan(context.Background(), filepath.Join(t.TempDir(), "group-apply.json"), plan)
	require.NoError(t, err)
	require.Equal(t, 1, report.Summary["updated"])
	require.Equal(t, 1, report.Summary["skipped_unavailable"])
	require.Equal(t, newDefaultChatModel, readDefaultModelPreference(t, db, 21))
	require.Equal(t, legacyDefaultChatModel, readDefaultModelPreference(t, db, 22))
}

func TestDefaultChatModelForwardRechecksRouteBeforeWriting(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 23, legacyDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}
	plan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, "will_update", plan.Entries[0].Status)
	require.NoError(t, db.Model(&model.Ability{}).
		Where("model = ?", newDefaultChatModel).Update("enabled", false).Error)

	report, err := engine.runDefaultModelPlan(context.Background(), filepath.Join(t.TempDir(), "route-race.json"), plan)
	require.NoError(t, err)
	require.Equal(t, 1, report.Summary["skipped_unavailable"])
	require.Equal(t, legacyDefaultChatModel, readDefaultModelPreference(t, db, 23))
	automaticUserIDs, err := mujianconfig.AutomaticDefaultChatModelUserIDs(db, plan.OperationID)
	require.NoError(t, err)
	require.Empty(t, automaticUserIDs)
}

func TestDefaultChatModelApplyDoesNotWriteBeforeSnapshotIsDurable(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 30, legacyDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}
	plan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)

	directory := t.TempDir()
	notDirectory := filepath.Join(directory, "not-a-directory")
	require.NoError(t, os.WriteFile(notDirectory, []byte("occupied"), 0600))
	_, err = engine.runDefaultModelPlan(context.Background(), filepath.Join(notDirectory, "report.json"), plan)
	require.Error(t, err)
	require.Equal(t, legacyDefaultChatModel, readDefaultModelPreference(t, db, 30))
	var internalOptionCount int64
	require.NoError(t, db.Model(&model.Option{}).Where("key LIKE ?", "_mujian_default_%").Count(&internalOptionCount).Error)
	require.Zero(t, internalOptionCount, "a failed forward transaction must not open the assignment gate")
}

func TestDefaultChatModelRollbackRejectsMissingOperationLedgerShard(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 29, legacyDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}
	forwardPlan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	snapshotPath := filepath.Join(t.TempDir(), "forward.json")
	_, err = engine.runDefaultModelPlan(context.Background(), snapshotPath, forwardPlan)
	require.NoError(t, err)
	require.NoError(t, db.Delete(&model.Option{},
		"key = ?", "_mujian_default_chat_model_auto:sonnet-4-6:17").Error)

	_, err = engine.planDefaultChatModelRollback(context.Background(), snapshotPath, true)
	require.ErrorContains(t, err, "ledger is incomplete")
	require.Equal(t, newDefaultChatModel, readDefaultModelPreference(t, db, 29))
}

func TestDefaultChatModelRejectsASecondActiveForwardCutover(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 33, legacyDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}
	firstPlan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	_, err = engine.runDefaultModelPlan(context.Background(), filepath.Join(t.TempDir(), "first.json"), firstPlan)
	require.NoError(t, err)

	createDefaultModelPreference(t, db, 34, legacyDefaultChatModel)
	secondPlan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	_, err = engine.runDefaultModelPlan(context.Background(), filepath.Join(t.TempDir(), "second.json"), secondPlan)
	require.ErrorContains(t, err, "active default model cutover")
	require.Equal(t, legacyDefaultChatModel, readDefaultModelPreference(t, db, 34))
}

func TestDefaultChatModelCrashSnapshotIsSafeBeforeCommit(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 31, legacyDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}
	plan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	snapshotPath := filepath.Join(t.TempDir(), "crash.json")

	_, err = engine.executeDefaultModelPlan(context.Background(), plan, func(report migrationReport) error {
		require.NoError(t, writeMigrationReport(snapshotPath, report))
		return errors.New("simulated interruption before update")
	})
	require.ErrorContains(t, err, "simulated interruption")
	require.Equal(t, legacyDefaultChatModel, readDefaultModelPreference(t, db, 31))

	_, err = engine.planDefaultChatModelRollback(context.Background(), snapshotPath, false)
	require.ErrorContains(t, err, "no committed database marker")
}

func TestDefaultChatModelKeepsSealedSnapshotWhenTransactionFailsAfterPersistence(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 32, legacyDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}
	plan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Option{
		Key: defaultModelCommitMarkerKey(plan.OperationID), Value: "collision",
	}).Error)

	snapshotPath := filepath.Join(t.TempDir(), "transaction-error.json")
	report, err := engine.runDefaultModelPlan(context.Background(), snapshotPath, plan)
	require.ErrorContains(t, err, "create default model commit marker")
	require.NotEmpty(t, report.CommitDigest)
	require.Equal(t, legacyDefaultChatModel, readDefaultModelPreference(t, db, 32))

	parsed, parseErr := readDefaultModelSnapshot(snapshotPath, "mujian_test", "test-cluster")
	require.NoError(t, parseErr)
	require.Equal(t, report.OperationID, parsed.OperationID)
	_, err = engine.planDefaultChatModelRollback(context.Background(), snapshotPath, false)
	require.ErrorContains(t, err, "commit marker is invalid")
}

func TestDefaultChatModelReportPathCannotOverwriteEarlierSnapshot(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "forward.json")
	require.NoError(t, reserveMigrationReport(reportPath))
	report := newMigrationReport(defaultChatModelCommand, false)
	report.add(defaultModelReportEntry(44, "updated"))
	report.finish()
	require.NoError(t, writeMigrationReport(reportPath, report))
	before, err := os.ReadFile(reportPath)
	require.NoError(t, err)

	err = reserveMigrationReport(reportPath)
	require.ErrorContains(t, err, "already exists")
	after, readErr := os.ReadFile(reportPath)
	require.NoError(t, readErr)
	require.Equal(t, before, after)
}

func TestDefaultChatModelRollbackRestoresOnlySnapshotRowsStillAtTarget(t *testing.T) {
	db := newMigrationTestDB(t)
	for _, userID := range []int{1, 2, 3, 4} {
		createDefaultModelPreference(t, db, userID, legacyDefaultChatModel)
	}
	createDefaultModelPreference(t, db, 5, newDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}

	forwardPlan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	// User 2 chose another model after planning, so the forward CAS must skip it.
	setDefaultModelPreference(t, db, 2, "deepseek-v4-flash")
	forwardReport, err := engine.runDefaultModelPlan(context.Background(), filepath.Join(t.TempDir(), "apply.json"), forwardPlan)
	require.NoError(t, err)
	require.Equal(t, 3, forwardReport.Summary["updated"])
	require.Equal(t, 1, forwardReport.Summary["skipped_changed"])

	snapshotPath := filepath.Join(t.TempDir(), "default-model.json")
	require.NoError(t, writeMigrationReport(snapshotPath, forwardReport))
	// User 2 later chose the target model themselves. Since the migration skipped
	// them, the applied report must not authorize rollback of that choice.
	setDefaultModelPreference(t, db, 2, newDefaultChatModel)
	// User 3 changed their preference after migration and must be preserved.
	setDefaultModelPreference(t, db, 3, "deepseek-v4-flash")

	rollbackPlan, err := engine.planDefaultChatModelRollback(context.Background(), snapshotPath, true)
	require.NoError(t, err)
	require.Len(t, rollbackPlan.Entries, 3, "the skipped forward row must not enter the rollback plan")
	require.Equal(t, 1, rollbackPlan.report().Summary["skipped_changed"])
	// User 1 changes after rollback planning; the rollback CAS must also preserve it.
	setDefaultModelPreference(t, db, 1, "gpt-5.6-terra")

	rollbackReport, err := engine.runDefaultModelPlan(context.Background(), filepath.Join(t.TempDir(), "rollback.json"), rollbackPlan)
	require.NoError(t, err)
	require.Equal(t, 1, rollbackReport.Summary["restored"])
	require.Equal(t, 2, rollbackReport.Summary["skipped_changed"])
	require.Equal(t, defaultModelCommittedValue(rollbackReport.CommitDigest), readDefaultModelMarker(t, db, rollbackReport.OperationID))
	require.Equal(t,
		defaultModelRolledBackPrefix+rollbackReport.OperationID+":"+rollbackReport.CommitDigest,
		readDefaultModelMarker(t, db, forwardReport.OperationID),
	)
	require.Equal(t, "gpt-5.6-terra", readDefaultModelPreference(t, db, 1))
	require.Equal(t, newDefaultChatModel, readDefaultModelPreference(t, db, 2))
	require.Equal(t, "deepseek-v4-flash", readDefaultModelPreference(t, db, 3))
	require.Equal(t, legacyDefaultChatModel, readDefaultModelPreference(t, db, 4))
	require.Equal(t, newDefaultChatModel, readDefaultModelPreference(t, db, 5), "rows outside the snapshot must remain untouched")
	var activeCutovers int64
	require.NoError(t, db.Model(&model.Option{}).Where("key = ?", defaultModelActiveCutoverKey).Count(&activeCutovers).Error)
	require.Zero(t, activeCutovers)

	_, err = engine.planDefaultChatModelRollback(context.Background(), snapshotPath, false)
	require.ErrorContains(t, err, "already consumed")
}

func TestDefaultChatModelRollbackIncludesNewAutomaticUsersAndPreservesExplicitSonnet(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 61, legacyDefaultChatModel)
	createDefaultModelPreference(t, db, 62, legacyDefaultChatModel)
	createDefaultModelPreference(t, db, 64, newDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}

	forwardPlan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	snapshotPath := filepath.Join(t.TempDir(), "forward.json")
	_, err = engine.runDefaultModelPlan(context.Background(), snapshotPath, forwardPlan)
	require.NoError(t, err)

	// User 63 onboarded after the forward snapshot and is still an automatic
	// Sonnet assignment, so rollback must include it.
	createDefaultModelPreference(t, db, 63, newDefaultChatModel)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return mujianconfig.AddAutomaticDefaultChatModelUsers(tx, 63)
	}))
	// User 62 explicitly kept Sonnet after cutover; the product removes their
	// automatic provenance in the same preference transaction.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return mujianconfig.RemoveAutomaticDefaultChatModelUser(tx, 62)
	}))

	rollbackPlan, err := engine.planDefaultChatModelRollback(context.Background(), snapshotPath, true)
	require.NoError(t, err)
	require.Equal(t, []int{61, 63}, rollbackPlan.AutomaticUserIDs)
	rollbackReport, err := engine.runDefaultModelPlan(context.Background(), filepath.Join(t.TempDir(), "rollback.json"), rollbackPlan)
	require.NoError(t, err)
	require.Equal(t, 2, rollbackReport.Summary["restored"])
	require.Equal(t, legacyDefaultChatModel, readDefaultModelPreference(t, db, 61))
	require.Equal(t, newDefaultChatModel, readDefaultModelPreference(t, db, 62), "explicit post-cutover choice must be preserved")
	require.Equal(t, legacyDefaultChatModel, readDefaultModelPreference(t, db, 63), "post-snapshot automatic assignment must be restored")
	require.Equal(t, newDefaultChatModel, readDefaultModelPreference(t, db, 64), "pre-existing explicit Sonnet choice must be preserved")
	_, err = mujianconfig.AutomaticDefaultChatModelUserIDs(db, forwardPlan.OperationID)
	require.ErrorContains(t, err, "assignments are closed")
}

func TestDefaultChatModelRollbackPreflightRejectsNewAutomaticAssignment(t *testing.T) {
	db := newMigrationTestDB(t)
	createDefaultModelPreference(t, db, 71, legacyDefaultChatModel)
	engine := migrationEngine{db: db, database: "mujian_test", deploymentID: "test-cluster"}
	forwardPlan, err := engine.planDefaultChatModel(context.Background(), true)
	require.NoError(t, err)
	snapshotPath := filepath.Join(t.TempDir(), "forward.json")
	_, err = engine.runDefaultModelPlan(context.Background(), snapshotPath, forwardPlan)
	require.NoError(t, err)

	rollbackPlan, err := engine.planDefaultChatModelRollback(context.Background(), snapshotPath, true)
	require.NoError(t, err)
	createDefaultModelPreference(t, db, 72, newDefaultChatModel)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return mujianconfig.AddAutomaticDefaultChatModelUsers(tx, 72)
	}))

	_, err = engine.runDefaultModelPlan(context.Background(), filepath.Join(t.TempDir(), "rollback.json"), rollbackPlan)
	require.ErrorContains(t, err, "assignments changed")
	require.Equal(t, newDefaultChatModel, readDefaultModelPreference(t, db, 71))
	require.Equal(t, newDefaultChatModel, readDefaultModelPreference(t, db, 72))
}

func TestDefaultChatModelRollbackRejectsDryRunAndDuplicateSnapshots(t *testing.T) {
	directory := t.TempDir()
	dryRun := newMigrationReport(defaultChatModelCommand, true)
	dryRun.Version = defaultModelReportVersion
	dryRun.Database = "mujian_test"
	dryRun.DeploymentID = "test-cluster"
	dryRun.OperationID = uuid.NewString()
	dryRun.add(defaultModelReportEntry(7, "would_update"))
	dryRun.finish()
	dryRunPath := filepath.Join(directory, "dry-run.json")
	require.NoError(t, writeMigrationReport(dryRunPath, dryRun))
	_, err := readDefaultModelSnapshot(dryRunPath, "mujian_test", "test-cluster")
	require.ErrorContains(t, err, "applied")

	incomplete := newMigrationReport(defaultChatModelCommand, false)
	incomplete.Version = defaultModelReportVersion
	incomplete.Database = "mujian_test"
	incomplete.DeploymentID = "test-cluster"
	incomplete.OperationID = uuid.NewString()
	incomplete.add(defaultModelReportEntry(7, "will_update"))
	incomplete.finish()
	require.NoError(t, sealDefaultModelReport(&incomplete))
	incompletePath := filepath.Join(directory, "incomplete.json")
	require.NoError(t, writeMigrationReport(incompletePath, incomplete))
	_, err = readDefaultModelSnapshot(incompletePath, "mujian_test", "test-cluster")
	require.ErrorContains(t, err, "unsupported status")

	duplicate := newMigrationReport(defaultChatModelCommand, false)
	duplicate.Version = defaultModelReportVersion
	duplicate.Database = "mujian_test"
	duplicate.DeploymentID = "test-cluster"
	duplicate.OperationID = uuid.NewString()
	duplicate.add(defaultModelReportEntry(7, "updated"))
	duplicate.add(defaultModelReportEntry(7, "updated"))
	duplicate.finish()
	require.NoError(t, sealDefaultModelReport(&duplicate))
	duplicatePath := filepath.Join(directory, "duplicate.json")
	require.NoError(t, writeMigrationReport(duplicatePath, duplicate))
	_, err = readDefaultModelSnapshot(duplicatePath, "mujian_test", "test-cluster")
	require.ErrorContains(t, err, "duplicate user_id")

	wrongDatabase := newMigrationReport(defaultChatModelCommand, false)
	wrongDatabase.Version = defaultModelReportVersion
	wrongDatabase.Database = "staging"
	wrongDatabase.DeploymentID = "test-cluster"
	wrongDatabase.OperationID = uuid.NewString()
	wrongDatabase.add(defaultModelReportEntry(8, "updated"))
	wrongDatabase.finish()
	require.NoError(t, sealDefaultModelReport(&wrongDatabase))
	wrongDatabasePath := filepath.Join(directory, "wrong-database.json")
	require.NoError(t, writeMigrationReport(wrongDatabasePath, wrongDatabase))
	_, err = readDefaultModelSnapshot(wrongDatabasePath, "production", "test-cluster")
	require.ErrorContains(t, err, "does not match target database")

	wrongDeployment := newMigrationReport(defaultChatModelCommand, false)
	wrongDeployment.Version = defaultModelReportVersion
	wrongDeployment.Database = "mujian_test"
	wrongDeployment.DeploymentID = "staging-cluster"
	wrongDeployment.OperationID = uuid.NewString()
	wrongDeployment.add(defaultModelReportEntry(9, "updated"))
	wrongDeployment.finish()
	require.NoError(t, sealDefaultModelReport(&wrongDeployment))
	wrongDeploymentPath := filepath.Join(directory, "wrong-deployment.json")
	require.NoError(t, writeMigrationReport(wrongDeploymentPath, wrongDeployment))
	_, err = readDefaultModelSnapshot(wrongDeploymentPath, "mujian_test", "production-cluster")
	require.ErrorContains(t, err, "does not match target deployment")
}

func TestDefaultChatModelRollbackRejectsTamperedSnapshot(t *testing.T) {
	report := newMigrationReport(defaultChatModelCommand, false)
	report.Version = defaultModelReportVersion
	report.Database = "mujian_test"
	report.DeploymentID = "test-cluster"
	report.OperationID = uuid.NewString()
	report.add(defaultModelReportEntry(11, "updated"))
	report.finish()
	require.NoError(t, sealDefaultModelReport(&report))
	report.Entries[0].UserID = 12

	path := filepath.Join(t.TempDir(), "tampered.json")
	require.NoError(t, writeMigrationReport(path, report))
	_, err := readDefaultModelSnapshot(path, "mujian_test", "test-cluster")
	require.ErrorContains(t, err, "digest does not match")
}

func defaultModelReportEntry(userID int, status string) reportEntry {
	return reportEntry{
		Kind: defaultModelReportKind, ID: strconv.Itoa(userID), UserID: userID,
		From: legacyDefaultChatModel, To: newDefaultChatModel, Status: status,
	}
}

func createDefaultModelPreference(t *testing.T, db *gorm.DB, userID int, chatModel string) {
	t.Helper()
	ensureDefaultModelRoute(t, db, "default")
	username := fmt.Sprintf("migration-user-%d", userID)
	require.NoError(t, db.Create(&model.User{
		Id: userID, Username: username, Password: "hashed-password", DisplayName: username,
		AffCode: username, Group: "default",
	}).Error)
	require.NoError(t, db.Create(&model.MujianUserPreference{
		UserID: userID, DefaultChatModel: chatModel, DefaultImageModel: "nano-banana",
	}).Error)
}

func ensureDefaultModelRoute(t *testing.T, db *gorm.DB, group string) {
	t.Helper()
	var channel model.Channel
	result := db.Where("name = ?", "default-model-route").First(&channel)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		priority := int64(100)
		channel = model.Channel{
			Name: "default-model-route", Key: "test-key", Group: group, Models: newDefaultChatModel,
			Status: common.ChannelStatusEnabled, Priority: &priority,
		}
		require.NoError(t, db.Create(&channel).Error)
		require.NoError(t, db.Create(&model.Ability{
			Group: group, Model: newDefaultChatModel, ChannelId: channel.Id, Enabled: true, Priority: &priority,
		}).Error)
		require.NoError(t, db.Create(&model.ChannelModelPrice{
			ChannelID: channel.Id, CatalogID: newDefaultChatModel, UpstreamModelID: newDefaultChatModel,
			Provider: "test", BillingType: model.ChannelModelBillingToken,
			InputPrice: 3, OutputPrice: 15, Currency: "USD", Available: true,
		}).Error)
		return
	}
	require.NoError(t, result.Error)
}

func setDefaultModelPreference(t *testing.T, db *gorm.DB, userID int, chatModel string) {
	t.Helper()
	require.NoError(t, db.Model(&model.MujianUserPreference{}).
		Where("user_id = ?", userID).Update("default_chat_model", chatModel).Error)
}

func readDefaultModelPreference(t *testing.T, db *gorm.DB, userID int) string {
	t.Helper()
	var preference model.MujianUserPreference
	require.NoError(t, db.First(&preference, "user_id = ?", userID).Error)
	return preference.DefaultChatModel
}

func readDefaultModelMarker(t *testing.T, db *gorm.DB, operationID string) string {
	t.Helper()
	var option model.Option
	require.NoError(t, db.First(&option, "key = ?", defaultModelCommitMarkerKey(operationID)).Error)
	return option.Value
}
