package dberrors

import (
	"database/sql"
	"errors"
	"regexp"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
)

// IsNoRows reports whether err is a "no rows in result set" error — a single-row
// query (QueryRow/Scan) that matched nothing. Repository Get methods surface a
// missing row as the bare sql.ErrNoRows; classifying it here lets callers detect
// a missing row without importing database/sql into higher layers.
func IsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

var (
	quotedTableMissingPattern   = regexp.MustCompile(`(?i)\btable\s+'[^']+'\s+(doesn't exist|does not exist)\b`)
	unquotedTableMissingPattern = regexp.MustCompile("(?i)^table\\s+`?[^\\s'`]+`?\\s+(doesn't exist|does not exist)\\b")
)

// IsTableNotExist reports whether err is specifically a MySQL/Dolt
// table-not-found error. It intentionally does not classify missing columns,
// schemas, or other objects as optional-table absence.
func IsTableNotExist(err error) bool {
	if err == nil {
		return false
	}

	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1146
	}

	s := strings.ToLower(err.Error())
	return strings.Contains(s, "error 1146") ||
		quotedTableMissingPattern.MatchString(s) ||
		unquotedTableMissingPattern.MatchString(s)
}
