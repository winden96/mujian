package common

import "strings"

const (
	DatabaseTypeMySQL      = "mysql"
	DatabaseTypeSQLite     = "sqlite"
	DatabaseTypePostgreSQL = "postgres"
)

var UsingSQLite = false
var UsingPostgreSQL = false
var LogSqlType = DatabaseTypeSQLite // Default to SQLite for logging SQL queries
var UsingMySQL = false
var UsingClickHouse = false

const sqliteBusyTimeoutPragma = "_pragma=busy_timeout(30000)"

var SQLitePath = withSQLiteBusyTimeout("one-api.db")

func withSQLiteBusyTimeout(dsn string) string {
	if strings.Contains(strings.ToLower(dsn), "_pragma=busy_timeout") {
		return dsn
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + sqliteBusyTimeoutPragma
}
