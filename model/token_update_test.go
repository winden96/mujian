package model

import (
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupTokenUpdateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := DB
	previousSQLite := common.UsingSQLite
	previousMySQL := common.UsingMySQL
	previousPostgreSQL := common.UsingPostgreSQL
	previousRedis := common.RedisEnabled
	t.Cleanup(func() {
		DB = previousDB
		common.UsingSQLite = previousSQLite
		common.UsingMySQL = previousMySQL
		common.UsingPostgreSQL = previousPostgreSQL
		common.RedisEnabled = previousRedis
	})

	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false
	common.RedisEnabled = false

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite db: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	DB = db
	if err := db.AutoMigrate(&Token{}); err != nil {
		t.Fatalf("failed to migrate token table: %v", err)
	}
	return db
}

func TestUpdateNameAndStatusDoesNotOverwriteNewerRestrictions(t *testing.T) {
	db := setupTokenUpdateTestDB(t)

	initialAllowIps := "192.0.2.10"
	token := &Token{
		UserId:             1,
		Name:               "original-name",
		Key:                "stal1234snap5678", // gitleaks:allow -- deterministic test fixture
		Status:             common.TokenStatusEnabled,
		ExpiredTime:        2_000_000_000,
		RemainQuota:        100,
		ModelLimitsEnabled: true,
		ModelLimits:        "gpt-4o",
		AllowIps:           &initialAllowIps,
		Group:              "default",
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	var staleSnapshot Token
	if err := db.First(&staleSnapshot, token.Id).Error; err != nil {
		t.Fatalf("failed to load token snapshot: %v", err)
	}

	newAllowIps := "198.51.100.20"
	newRestrictions := map[string]any{
		"expired_time":         int64(2_100_000_000),
		"remain_quota":         777,
		"unlimited_quota":      false,
		"model_limits_enabled": true,
		"model_limits":         "claude-3-5-sonnet",
		"allow_ips":            newAllowIps,
		"group":                "auto",
		"cross_group_retry":    true,
	}
	if err := db.Model(&Token{}).Where("id = ?", token.Id).Updates(newRestrictions).Error; err != nil {
		t.Fatalf("failed to simulate a newer restriction update: %v", err)
	}

	staleSnapshot.Name = "renamed-token"
	staleSnapshot.Status = common.TokenStatusDisabled
	if err := staleSnapshot.UpdateNameAndStatus(); err != nil {
		t.Fatalf("failed to update token name and status: %v", err)
	}

	var updated Token
	if err := db.First(&updated, token.Id).Error; err != nil {
		t.Fatalf("failed to reload token: %v", err)
	}
	if updated.Name != "renamed-token" || updated.Status != common.TokenStatusDisabled {
		t.Fatalf("basic fields were not updated: name=%q status=%d", updated.Name, updated.Status)
	}
	if updated.ExpiredTime != 2_100_000_000 ||
		updated.RemainQuota != 777 ||
		updated.UnlimitedQuota ||
		!updated.ModelLimitsEnabled ||
		updated.ModelLimits != "claude-3-5-sonnet" ||
		updated.AllowIps == nil || *updated.AllowIps != newAllowIps ||
		updated.Group != "auto" ||
		!updated.CrossGroupRetry {
		t.Fatalf("newer token restrictions were overwritten by a stale basic update")
	}
}

func TestMujianInternalTokenReloadsNarrowerLimitsFromDatabase(t *testing.T) {
	db := setupTokenUpdateTestDB(t)
	token := &Token{
		UserId: 1, Name: MujianInternalTokenName, Key: "mujianstalesnap1234", // gitleaks:allow -- deterministic test fixture
		Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true,
		ModelLimitsEnabled: true, ModelLimits: "claude-opus-5,claude-sonnet-4-6",
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("failed to create internal token: %v", err)
	}
	staleSnapshot := *token
	if err := db.Model(token).Update("model_limits", "claude-sonnet-4-6").Error; err != nil {
		t.Fatalf("failed to narrow internal token limits: %v", err)
	}

	refreshed, err := refreshMujianInternalToken(token.Key, &staleSnapshot)
	if err != nil {
		t.Fatalf("failed to refresh internal token: %v", err)
	}
	if refreshed.ModelLimits != "claude-sonnet-4-6" {
		t.Fatalf("stale internal token limits were retained: %q", refreshed.ModelLimits)
	}
}

func TestUserTokenWritesRejectMujianReservedName(t *testing.T) {
	db := setupTokenUpdateTestDB(t)
	reserved := &Token{
		UserId: 1, Name: "  " + MujianInternalTokenName + "  ", Key: "reservednamefixture1", // gitleaks:allow -- deterministic test fixture
		Status: common.TokenStatusEnabled, ExpiredTime: -1,
	}
	require.ErrorIs(t, reserved.Insert(), ErrReservedTokenName)

	ordinary := &Token{
		UserId: 1, Name: "ordinary", Key: "ordinarynamefixture1", // gitleaks:allow -- deterministic test fixture
		Status: common.TokenStatusEnabled, ExpiredTime: -1,
	}
	require.NoError(t, db.Create(ordinary).Error)
	ordinary.Name = MujianInternalTokenName
	require.ErrorIs(t, ordinary.Update(), ErrReservedTokenName)
	require.ErrorIs(t, ordinary.UpdateNameAndStatus(), ErrReservedTokenName)

	var stored Token
	require.NoError(t, db.First(&stored, ordinary.Id).Error)
	require.Equal(t, "ordinary", stored.Name)
}
