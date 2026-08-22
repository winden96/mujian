package main

import (
	"os"
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
