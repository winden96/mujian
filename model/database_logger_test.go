package model

import (
	"bytes"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestParameterizedDatabaseLoggerDoesNotRenderChannelKey(t *testing.T) {
	var output bytes.Buffer
	db, err := gorm.Open(sqlite.Open("file:parameterized-logger?mode=memory&cache=shared"), &gorm.Config{
		Logger: parameterizedDatabaseLogger(&output),
	})
	require.NoError(t, err)

	const secret = "tabcode-provider-secret-that-must-not-be-logged"
	err = db.Model(&Channel{}).Where("key = ?", secret).Update("name", "trigger-error-log").Error
	require.Error(t, err)
	require.NotContains(t, output.String(), secret)
	require.Contains(t, output.String(), "UPDATE")
}
