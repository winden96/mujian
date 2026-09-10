package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParseCommandRequiresExactDatabaseGuard(t *testing.T) {
	_, err := parseCommand([]string{"report", "--report", "-"})
	require.ErrorContains(t, err, "--expect-database")

	options, err := parseCommand([]string{
		"schema", "--expect-database", "mujian_rehearsal", "--report", "schema.json",
	})
	require.NoError(t, err)
	require.Equal(t, "schema", options.command)
	require.Equal(t, "mujian_rehearsal", options.expectDatabase)
	require.False(t, options.apply)
}

func TestParseDefaultChatModelCommandsAreDryRunByDefault(t *testing.T) {
	options, err := parseCommand([]string{
		defaultChatModelCommand,
		"--expect-database", "mujian_production",
		"--deployment-id", "production-cluster",
		"--report", "default-model.json",
	})
	require.NoError(t, err)
	require.False(t, options.apply)

	_, err = parseCommand([]string{
		defaultChatModelCommand,
		"--expect-database", "mujian_production",
		"--deployment-id", "production-cluster",
		"--report", "-",
		"--apply",
	})
	require.ErrorContains(t, err, "file-backed --report")
}

func TestParseDefaultChatModelRollbackRequiresAndPreservesSnapshot(t *testing.T) {
	reportPath := filepath.Join("reports", "rollback.json")
	_, err := parseCommand([]string{
		rollbackDefaultChatModelCommand,
		"--expect-database", "mujian_production",
		"--deployment-id", "production-cluster",
		"--report", reportPath,
	})
	require.ErrorContains(t, err, "--snapshot")

	_, err = parseCommand([]string{
		rollbackDefaultChatModelCommand,
		"--expect-database", "mujian_production",
		"--deployment-id", "production-cluster",
		"--snapshot", "-",
		"--report", reportPath,
	})
	require.ErrorContains(t, err, "file-backed applied report")

	_, err = parseCommand([]string{
		rollbackDefaultChatModelCommand,
		"--expect-database", "mujian_production",
		"--deployment-id", "production-cluster",
		"--snapshot", reportPath,
		"--report", filepath.Join("reports", ".", "rollback.json"),
	})
	require.ErrorContains(t, err, "must not overwrite")

	options, err := parseCommand([]string{
		rollbackDefaultChatModelCommand,
		"--expect-database", "mujian_production",
		"--deployment-id", "production-cluster",
		"--snapshot", "default-model.json",
		"--report", reportPath,
		"--apply",
	})
	require.NoError(t, err)
	require.True(t, options.apply)
	require.Equal(t, "default-model.json", options.snapshotPath)
	require.Equal(t, "production-cluster", options.deploymentID)
}

func TestParseDefaultChatModelRequiresDeploymentGuard(t *testing.T) {
	t.Setenv("MUJIAN_DEPLOYMENT_ID", "")
	_, err := parseCommand([]string{
		defaultChatModelCommand,
		"--expect-database", "mujian_production",
		"--report", "default-model.json",
	})
	require.ErrorContains(t, err, "--deployment-id")
}

func TestAppliedDefaultModelCommandsRequireTheMatchingRuntimeDefault(t *testing.T) {
	t.Setenv("MUJIAN_CHAT_MODELS", "")

	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", legacyDefaultChatModel)
	err := validateDefaultModelCommandEnvironment(commandOptions{command: defaultChatModelCommand, apply: true})
	require.ErrorContains(t, err, newDefaultChatModel)
	require.NoError(t, validateDefaultModelCommandEnvironment(commandOptions{command: defaultChatModelCommand}))

	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", newDefaultChatModel)
	require.NoError(t, validateDefaultModelCommandEnvironment(commandOptions{command: defaultChatModelCommand, apply: true}))
	err = validateDefaultModelCommandEnvironment(commandOptions{command: rollbackDefaultChatModelCommand, apply: true})
	require.ErrorContains(t, err, legacyDefaultChatModel)

	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", legacyDefaultChatModel)
	require.NoError(t, validateDefaultModelCommandEnvironment(commandOptions{command: rollbackDefaultChatModelCommand, apply: true}))
}

func TestDefaultModelCommandRejectsInvalidModelBeforeExecution(t *testing.T) {
	t.Setenv("MUJIAN_DEFAULT_CHAT_MODEL", "not-in-catalog")
	t.Setenv("MUJIAN_CHAT_MODELS", "")
	err := validateDefaultModelCommandEnvironment(commandOptions{command: defaultChatModelCommand})
	require.ErrorContains(t, err, "必须是已登记且已启用的对话模型")
}

func TestSameFilePathRecognizesFilesystemAliases(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "forward.json")
	require.NoError(t, os.WriteFile(reportPath, []byte("snapshot"), 0600))
	aliasPath := filepath.Join(directory, "forward-alias.json")
	if err := os.Symlink(reportPath, aliasPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	require.True(t, sameFilePath(reportPath, aliasPath))
}

func TestConfigureMigrationLoggingUsesStderr(t *testing.T) {
	common.LogWriterMu.Lock()
	previousWriter := gin.DefaultWriter
	previousErrorWriter := gin.DefaultErrorWriter
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultWriter = previousWriter
		gin.DefaultErrorWriter = previousErrorWriter
		common.LogWriterMu.Unlock()
	})

	configureMigrationLogging()
	require.Same(t, os.Stderr, gin.DefaultWriter)
	require.Same(t, os.Stderr, gin.DefaultErrorWriter)
}
