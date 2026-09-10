package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupUserEditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousRedis := common.RedisEnabled
	t.Cleanup(func() {
		DB = previousDB
		common.RedisEnabled = previousRedis
	})

	common.RedisEnabled = false
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	DB = db

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func TestUserEditRefreshesCacheSnapshotAfterGroupChange(t *testing.T) {
	db := setupUserEditTestDB(t)
	stored := User{
		Username: "group-cache-user", DisplayName: "Before", Password: "hashed-password",
		Group: "default", Email: "cache@example.com", Quota: 321, Status: common.UserStatusEnabled,
	}
	require.NoError(t, db.Create(&stored).Error)

	edit := User{
		Id: stored.Id, Username: stored.Username, DisplayName: "After",
		Group: "mujian-canary", Remark: "moved",
	}
	require.NoError(t, edit.Edit(false))

	// Edit passes this database-authoritative receiver to updateUserCache.
	require.Equal(t, "mujian-canary", edit.Group)
	require.Equal(t, "After", edit.DisplayName)
	require.Equal(t, "cache@example.com", edit.Email)
	require.Equal(t, 321, edit.Quota)
	require.Equal(t, common.UserStatusEnabled, edit.Status)

	var persisted User
	require.NoError(t, db.First(&persisted, stored.Id).Error)
	require.Equal(t, "mujian-canary", persisted.Group)
}

func TestUserUpdateRefreshesPersistedReceiver(t *testing.T) {
	db := setupUserEditTestDB(t)
	stored := User{
		Username: "profile-cache-user", DisplayName: "Before", Password: "hashed-password",
		Group: "default", Email: "profile@example.com", Quota: 654, Status: common.UserStatusEnabled,
	}
	require.NoError(t, db.Create(&stored).Error)

	update := User{Id: stored.Id, Username: "renamed-user", DisplayName: "After"}
	require.NoError(t, update.Update(false))

	require.Equal(t, "renamed-user", update.Username)
	require.Equal(t, "After", update.DisplayName)
	require.Equal(t, "default", update.Group)
	require.Equal(t, "profile@example.com", update.Email)
	require.Equal(t, 654, update.Quota)
}
