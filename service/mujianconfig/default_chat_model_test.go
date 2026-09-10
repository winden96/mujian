package mujianconfig

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestConfiguredDefaultChatModelUsesValidatedBuiltInDefault(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "")
	t.Setenv("MUJIAN_CHAT_MODELS", "")
	configured, err := ConfiguredDefaultChatModel()
	require.NoError(t, err)
	require.Equal(t, DefaultChatModel, configured)

	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "nano-banana")
	t.Setenv("MUJIAN_CHAT_MODELS", "nano-banana")
	_, err = ConfiguredDefaultChatModel()
	require.ErrorContains(t, err, "已登记且已启用的对话模型")
}

func TestConfiguredDefaultChatModelHonorsEnabledChatModels(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", CutoverChatModel)
	t.Setenv("MUJIAN_CHAT_MODELS", DefaultChatModel)
	_, err := ConfiguredDefaultChatModel()
	require.ErrorContains(t, err, "已登记且已启用的对话模型")

	t.Setenv("MUJIAN_CHAT_MODELS", DefaultChatModel+","+CutoverChatModel)
	configured, err := ConfiguredDefaultChatModel()
	require.NoError(t, err)
	require.Equal(t, CutoverChatModel, configured)
}

func TestAutomaticDefaultChatModelLedgerIsBoundedAndConsumable(t *testing.T) {
	db := newDefaultModelConfigTestDB(t)
	operationID := uuid.NewString()

	err := db.Transaction(func(tx *gorm.DB) error {
		return AddAutomaticDefaultChatModelUsers(tx, 65)
	})
	require.ErrorContains(t, err, "assignments are closed")

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if openErr := OpenAutomaticDefaultChatModelAssignments(tx, operationID); openErr != nil {
			return openErr
		}
		return AddAutomaticDefaultChatModelUsers(tx, 65, 1, 130)
	}))
	userIDs, err := AutomaticDefaultChatModelUserIDs(db, operationID)
	require.NoError(t, err)
	require.Equal(t, []int{1, 65, 130}, userIDs)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return RemoveAutomaticDefaultChatModelUser(tx, 65)
	}))
	userIDs, err = AutomaticDefaultChatModelUserIDs(db, operationID)
	require.NoError(t, err)
	require.Equal(t, []int{1, 130}, userIDs)

	err = db.Transaction(func(tx *gorm.DB) error {
		return ConsumeAutomaticDefaultChatModelUsers(tx, operationID, []int{1})
	})
	require.ErrorContains(t, err, "changed")
	userIDs, err = AutomaticDefaultChatModelUserIDs(db, operationID)
	require.NoError(t, err)
	require.Equal(t, []int{1, 130}, userIDs)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return ConsumeAutomaticDefaultChatModelUsers(tx, operationID, []int{130, 1})
	}))
	_, err = AutomaticDefaultChatModelUserIDs(db, operationID)
	require.ErrorContains(t, err, "assignments are closed")

	var rowCount int64
	require.NoError(t, db.Model(&model.Option{}).Count(&rowCount).Error)
	require.Equal(t, int64(assignmentLedgerShards+1), rowCount)

	nextOperationID := uuid.NewString()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if openErr := OpenAutomaticDefaultChatModelAssignments(tx, nextOperationID); openErr != nil {
			return openErr
		}
		return AddAutomaticDefaultChatModelUsers(tx, 3)
	}))
	userIDs, err = AutomaticDefaultChatModelUserIDs(db, nextOperationID)
	require.NoError(t, err)
	require.Equal(t, []int{3}, userIDs)
}

func TestAutomaticDefaultChatModelLedgerRejectsCorruption(t *testing.T) {
	db := newDefaultModelConfigTestDB(t)
	operationID := uuid.NewString()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return OpenAutomaticDefaultChatModelAssignments(tx, operationID)
	}))
	require.NoError(t, db.Model(&model.Option{}).Where("key = ?", assignmentLedgerKey(1)).Update(
		"value", `{"version":2,"model":"claude-sonnet-4-6","operation_id":"`+operationID+`","shard":1,"user_ids":[2]}`,
	).Error)
	_, err := AutomaticDefaultChatModelUserIDs(db, operationID)
	require.ErrorContains(t, err, "shard 1 is invalid")
}

func TestAutomaticDefaultChatModelLedgerRequiresEveryOperationBoundShard(t *testing.T) {
	db := newDefaultModelConfigTestDB(t)
	operationID := uuid.NewString()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return OpenAutomaticDefaultChatModelAssignments(tx, operationID)
	}))

	wrongOperationID := uuid.NewString()
	_, err := AutomaticDefaultChatModelUserIDs(db, wrongOperationID)
	require.ErrorContains(t, err, "does not match the active cutover")
	err = db.Transaction(func(tx *gorm.DB) error {
		return ConsumeAutomaticDefaultChatModelUsers(tx, wrongOperationID, nil)
	})
	require.ErrorContains(t, err, "does not match the active cutover")

	require.NoError(t, db.Delete(&model.Option{}, "key = ?", assignmentLedgerKey(17)).Error)
	_, err = AutomaticDefaultChatModelUserIDs(db, operationID)
	require.ErrorContains(t, err, "ledger is incomplete")
	err = db.Transaction(func(tx *gorm.DB) error {
		return ConsumeAutomaticDefaultChatModelUsers(tx, operationID, nil)
	})
	require.ErrorContains(t, err, "ledger is incomplete")
}

func TestAutomaticDefaultChatModelLedgerRejectsEmptyShardFromAnotherCutover(t *testing.T) {
	db := newDefaultModelConfigTestDB(t)
	operationID := uuid.NewString()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return OpenAutomaticDefaultChatModelAssignments(tx, operationID)
	}))

	otherOperationID := uuid.NewString()
	tampered, err := encodeAssignmentLedger(newAssignmentLedger(otherOperationID, 9), 9)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Option{}).Where("key = ?", assignmentLedgerKey(9)).Update("value", tampered).Error)
	_, err = AutomaticDefaultChatModelUserIDs(db, operationID)
	require.ErrorContains(t, err, "unsupported format")
}

func newDefaultModelConfigTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}
