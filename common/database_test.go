package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithSQLiteBusyTimeout(t *testing.T) {
	require.Equal(t, "one-api.db?_pragma=busy_timeout(30000)", withSQLiteBusyTimeout("one-api.db"))
	require.Equal(t,
		"file:test.db?mode=memory&cache=shared&_pragma=busy_timeout(30000)",
		withSQLiteBusyTimeout("file:test.db?mode=memory&cache=shared"),
	)
	require.Equal(t,
		"one-api.db?_pragma=busy_timeout(12000)",
		withSQLiteBusyTimeout("one-api.db?_pragma=busy_timeout(12000)"),
	)
}
