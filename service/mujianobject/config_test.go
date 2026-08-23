package mujianobject

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var obsEnvironmentNames = []string{
	"MUJIAN_IMAGE_STORAGE", "OBS_REGION", "OBS_ENDPOINT", "OBS_BUCKET",
	"OBS_PREFIX", "OBS_PUBLIC_BASE_URL", "OBS_AUTH_MODE",
	"OBS_ACCESS_KEY_ID", "OBS_SECRET_ACCESS_KEY", "OBS_SECURITY_TOKEN",
}

func clearOBSEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range obsEnvironmentNames {
		t.Setenv(name, "")
	}
}

func setValidOBSConfig(t *testing.T) {
	t.Helper()
	t.Setenv("OBS_REGION", "cn-north-4")
	t.Setenv("OBS_ENDPOINT", "https://obs.cn-north-4.myhuaweicloud.com")
	t.Setenv("OBS_BUCKET", "mujianai")
	t.Setenv("OBS_PREFIX", "/mujian/prod/public/")
	t.Setenv("OBS_PUBLIC_BASE_URL", "https://static.mujianai.com/")
	t.Setenv("OBS_AUTH_MODE", "ecs")
}

func TestLoadConfigOnlyAcceptsECSAuthentication(t *testing.T) {
	clearOBSEnvironment(t)
	setValidOBSConfig(t)
	// Long-term credentials may exist in a shell, but ECS mode does not load or
	// expose them in Config.
	t.Setenv("OBS_ACCESS_KEY_ID", "must-not-be-used")
	t.Setenv("OBS_SECRET_ACCESS_KEY", "must-not-be-used")
	config, err := LoadConfigFromEnv()
	require.NoError(t, err)
	require.Equal(t, AuthModeECS, config.AuthMode)
	require.Equal(t, "mujian/prod/public", config.Prefix)
	require.Equal(t, "https://static.mujianai.com", config.PublicBaseURL)

	t.Setenv("OBS_AUTH_MODE", "static")
	_, err = LoadConfigFromEnv()
	require.ErrorContains(t, err, "unsupported OBS_AUTH_MODE")
}

func TestOpenConfiguredEnvironmentDistinguishesAbsentAndPartialConfig(t *testing.T) {
	clearOBSEnvironment(t)
	store, enabled, err := OpenConfiguredFromEnvironment()
	require.NoError(t, err)
	require.False(t, enabled)
	require.Nil(t, store)

	t.Setenv("OBS_BUCKET", "mujianai")
	store, enabled, err = OpenConfiguredFromEnvironment()
	require.Error(t, err)
	require.True(t, enabled)
	require.Nil(t, store)

	clearOBSEnvironment(t)
	t.Setenv("MUJIAN_IMAGE_STORAGE", "obs")
	store, enabled, err = OpenConfiguredFromEnvironment()
	require.Error(t, err)
	require.True(t, enabled)
	require.Nil(t, store)

	clearOBSEnvironment(t)
	t.Setenv("MUJIAN_IMAGE_STORAGE", "db")
	store, enabled, err = OpenConfiguredFromEnvironment()
	require.NoError(t, err)
	require.False(t, enabled)
	require.Nil(t, store)

	clearOBSEnvironment(t)
	t.Setenv("MUJIAN_IMAGE_STORAGE", "unexpected")
	store, enabled, err = OpenConfiguredFromEnvironment()
	require.ErrorContains(t, err, "unsupported MUJIAN_IMAGE_STORAGE")
	require.False(t, enabled)
	require.Nil(t, store)
}

func TestFromEnvironmentDoesNotSilentlyFallback(t *testing.T) {
	clearOBSEnvironment(t)
	store, enabled, err := FromEnvironment()
	require.NoError(t, err)
	require.False(t, enabled)
	require.Nil(t, store)

	t.Setenv("MUJIAN_IMAGE_STORAGE", "obs")
	store, enabled, err = FromEnvironment()
	require.Error(t, err)
	require.True(t, enabled)
	require.Nil(t, store)
}

func TestBuildObjectKeyAndPublicURL(t *testing.T) {
	id := uuid.NewString()
	key, err := BuildObjectKey("mujian/prod/public", ObjectKindGeneration, id, "image/jpeg; charset=binary")
	require.NoError(t, err)
	require.Equal(t, "mujian/prod/public/generations/"+id+"/result.jpg", key)

	backend := &obsBackend{prefix: "mujian/prod/public", publicBaseURL: "https://static.mujianai.com"}
	publicURL, err := backend.PublicURL(key)
	require.NoError(t, err)
	require.Equal(t, "https://static.mujianai.com/"+key, publicURL)
	_, err = backend.PublicURL("private/" + id + ".png")
	require.ErrorContains(t, err, "outside the configured OBS prefix")
	_, err = backend.PublicURL(" " + key)
	require.ErrorContains(t, err, "surrounding whitespace")
}

func TestConfigRejectsEndpointOrPublicBasePath(t *testing.T) {
	config := Config{
		Region: "cn-north-4", Endpoint: "https://obs.example.com/api",
		Bucket: "mujianai", Prefix: "mujian/prod/public",
		PublicBaseURL: "https://static.example.com", AuthMode: AuthModeECS,
	}
	require.ErrorContains(t, config.Validate(), "without credentials, path")

	config.Endpoint = "https://obs.example.com"
	config.PublicBaseURL = "https://static.example.com/images"
	require.ErrorContains(t, config.Validate(), "without credentials, path")
}

func TestOpenFromEnvironmentCachesByDatabaseAndConfig(t *testing.T) {
	clearOBSEnvironment(t)
	setValidOBSConfig(t)
	resetConfiguredStoresForTest()
	previousDB := model.DB
	dbOne, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	dbTwo, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		resetConfiguredStoresForTest()
		model.DB = previousDB
		sqlOne, sqlErr := dbOne.DB()
		require.NoError(t, sqlErr)
		require.NoError(t, sqlOne.Close())
		sqlTwo, sqlErr := dbTwo.DB()
		require.NoError(t, sqlErr)
		require.NoError(t, sqlTwo.Close())
	})

	model.DB = dbOne
	first, err := OpenFromEnvironment()
	require.NoError(t, err)
	second, err := OpenFromEnvironment()
	require.NoError(t, err)
	require.True(t, first == second)

	model.DB = dbTwo
	third, err := OpenFromEnvironment()
	require.NoError(t, err)
	require.False(t, first == third)
}
