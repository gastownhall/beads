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

// IsAccessDenied reports whether err is a MySQL/Dolt privilege refusal: the
// connected user lacks the right to run the statement. Covers 1044
// (ER_DBACCESS_DENIED_ERROR), 1045 (ER_ACCESS_DENIED_ERROR), 1142
// (ER_TABLEACCESS_DENIED_ERROR), 1143 (ER_COLUMNACCESS_DENIED_ERROR) and
// 1227 (ER_SPECIFIC_ACCESS_DENIED_ERROR). Callers use it to classify a
// deliberately read-only-privileged client, which is a configuration, not a
// fault worth repeating warnings about.
func IsAccessDenied(err error) bool {
	if err == nil {
		return false
	}

	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1044, 1045, 1142, 1143, 1227:
			return true
		}
		return false
	}

	s := strings.ToLower(err.Error())
	return strings.Contains(s, "access denied") || strings.Contains(s, "command denied")
}

// IsMissingForeignKeyTarget reports whether err is the integrity-constraint
// violation a write hits when it references a row that does not exist: MySQL
// 1452 (ER_NO_REFERENCED_ROW_2) and its older 1216 (ER_NO_REFERENCED_ROW).
// It deliberately does not classify 1451 (ER_ROW_IS_REFERENCED_2), which is
// the opposite direction — deleting a row other rows still point at.
func IsMissingForeignKeyTarget(err error) bool {
	if err == nil {
		return false
	}

	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1452 || mysqlErr.Number == 1216
	}

	s := strings.ToLower(err.Error())
	return strings.Contains(s, "error 1452") || strings.Contains(s, "error 1216")
}
