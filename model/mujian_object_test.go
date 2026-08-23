package model

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupMujianObjectModelTest(t *testing.T, models ...interface{}) *gorm.DB {
	t.Helper()
	previousDB := DB
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(models...))
	return db
}

func TestBeginMujianObjectUploadOnlyResumesMatchingPendingState(t *testing.T) {
	db := setupMujianObjectModelTest(t, &MujianObjectOperation{})
	key := "mujian/prod/public/generations/" + uuid.NewString() + "/result.png"
	operation := &MujianObjectOperation{
		ObjectKey: key, ContentType: "image/png", SizeBytes: 7, SHA256: "abc",
	}
	require.NoError(t, BeginMujianObjectUpload(db, operation))
	require.NoError(t, db.Model(&MujianObjectOperation{}).Where("object_key = ?", key).UpdateColumn("updated_at", int64(1)).Error)
	require.NoError(t, BeginMujianObjectUpload(db, &MujianObjectOperation{
		ObjectKey: key, ContentType: "image/png", SizeBytes: 7, SHA256: "ABC",
	}))
	var resumed MujianObjectOperation
	require.NoError(t, db.Where("object_key = ?", key).First(&resumed).Error)
	require.Greater(t, resumed.UpdatedAt, int64(1))
	require.ErrorIs(t, BeginMujianObjectUpload(db, &MujianObjectOperation{
		ObjectKey: key, ContentType: "image/png", SizeBytes: 8, SHA256: "different",
	}), ErrMujianObjectStateConflict)
	require.NoError(t, ActivateMujianObject(db, key, "etag"))
	require.ErrorIs(t, BeginMujianObjectUpload(db, &MujianObjectOperation{
		ObjectKey: key, ContentType: "image/png", SizeBytes: 7, SHA256: "abc",
	}), ErrMujianObjectStateConflict)
	require.NoError(t, EnqueueMujianObjectDeletes(db, []string{key}))
	require.ErrorIs(t, BeginMujianObjectUpload(db, &MujianObjectOperation{
		ObjectKey: key, ContentType: "image/png", SizeBytes: 7, SHA256: "abc",
	}), ErrMujianObjectStateConflict)
}

func TestEnqueueMujianObjectDeletesIsIdempotentWithoutExistingLedger(t *testing.T) {
	db := setupMujianObjectModelTest(t, &MujianObjectOperation{})
	key := "mujian/prod/public/references/" + uuid.NewString() + ".png"
	require.NoError(t, EnqueueMujianObjectDeletes(db, []string{"", key, key, "  "}))
	require.NoError(t, EnqueueMujianObjectDeletes(db, []string{key}))

	var operations []MujianObjectOperation
	require.NoError(t, db.Find(&operations).Error)
	require.Len(t, operations, 1)
	require.Equal(t, key, operations[0].ObjectKey)
	require.Equal(t, MujianObjectStatusPendingDelete, operations[0].Status)
}

func TestRecordMujianObjectDeleteFailureTruncatesAtUTF8Boundary(t *testing.T) {
	db := setupMujianObjectModelTest(t, &MujianObjectOperation{})
	key := "mujian/prod/public/references/" + uuid.NewString() + ".png"
	require.NoError(t, EnqueueMujianObjectDeletes(db, []string{key}))

	require.NoError(t, RecordMujianObjectDeleteFailure(db, key, strings.Repeat("界", 700), 1234))
	var operation MujianObjectOperation
	require.NoError(t, db.Where("object_key = ?", key).First(&operation).Error)
	require.LessOrEqual(t, len(operation.LastError), 2048)
	require.True(t, utf8.ValidString(operation.LastError))
	require.Equal(t, 1, operation.AttemptCount)
	require.Equal(t, int64(1234), operation.NextAttemptAt)
}

func TestHardDeleteUserEnqueuesAllOwnedObjectKeys(t *testing.T) {
	db := setupMujianObjectModelTest(t,
		&User{}, &Token{}, &Task{}, &MujianProject{}, &MujianScene{}, &MujianShot{},
		&MujianAgentSession{}, &MujianAgentMessage{}, &MujianImageGeneration{},
		&MujianImageReference{}, &MujianUserPreference{}, &MujianObjectOperation{},
	)
	user := User{Username: "object-owner", Password: "password", Status: 1}
	require.NoError(t, db.Create(&user).Error)
	projectID := uuid.NewString()
	generationID := uuid.NewString()
	generationKey := "mujian/prod/public/generations/" + generationID + "/result.png"
	referenceKey := "mujian/prod/public/references/" + uuid.NewString() + ".png"
	shotKey := "mujian/prod/public/shots/" + uuid.NewString() + "/result.png"
	require.NoError(t, db.Create(&MujianProject{ID: projectID, UserID: user.Id, Title: "project"}).Error)
	require.NoError(t, db.Create(&MujianImageGeneration{
		ID: generationID, ProjectID: projectID, UserID: user.Id, Prompt: "image",
		Engine: "nano", ModelID: "image", AspectRatio: "1:1", Status: "succeeded",
		TaskID: uuid.NewString(), ResultObjectKey: generationKey,
	}).Error)
	require.NoError(t, db.Create(&MujianImageReference{
		ID: uuid.NewString(), GenerationID: generationID, Name: "reference.png",
		MIMEType: "image/png", SizeBytes: 4, ObjectKey: referenceKey,
	}).Error)
	shot := MujianShot{
		ID: uuid.NewString(), ProjectID: projectID, SceneID: uuid.NewString(), Sequence: 1,
		ResultObjectKey: shotKey,
	}
	require.NoError(t, db.Create(&shot).Error)
	require.NoError(t, db.Delete(&shot).Error)
	for _, key := range []string{generationKey, referenceKey, shotKey} {
		require.NoError(t, BeginMujianObjectUpload(db, &MujianObjectOperation{
			ObjectKey: key, ContentType: "image/png", SizeBytes: 4, SHA256: strings.Repeat("a", 64),
		}))
		require.NoError(t, ActivateMujianObject(db, key, "etag"))
	}

	require.NoError(t, HardDeleteUserById(user.Id))
	var operations []MujianObjectOperation
	require.NoError(t, db.Order("object_key").Find(&operations).Error)
	require.Len(t, operations, 3)
	for _, operation := range operations {
		require.Equal(t, MujianObjectStatusPendingDelete, operation.Status)
		require.NotEmpty(t, operation.ETag)
	}
	var generationCount, referenceCount, shotCount int64
	require.NoError(t, db.Model(&MujianImageGeneration{}).Count(&generationCount).Error)
	require.NoError(t, db.Model(&MujianImageReference{}).Count(&referenceCount).Error)
	require.NoError(t, db.Unscoped().Model(&MujianShot{}).Count(&shotCount).Error)
	require.Zero(t, generationCount)
	require.Zero(t, referenceCount)
	require.Zero(t, shotCount)
}
