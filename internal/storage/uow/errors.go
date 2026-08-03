package uow

import (
	"errors"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
)

// IsSerializationError reports whether err is a retryable database transaction
// failure that guarantees the server rolled the transaction back. Because the
// rollback discards every uncommitted write in the session, the only safe
// retry is to redo the WHOLE unit of work (read, merge, write, commit) —
// retrying just the commit re-commits an empty session, which Dolt reports as
// "nothing to commit" while the write is silently lost.
func IsSerializationError(err error) bool {
	return isSerializationError(err)
}

// isSerializationError returns true when a MySQL or PostgreSQL transaction
// error guarantees the transaction was rolled back. Safe to retry.
//   - MySQL 1213 (ER_LOCK_DEADLOCK) and 1205 (ER_LOCK_WAIT_TIMEOUT)
//   - SQLSTATE 40001 (serialization failure) and 40P01 (deadlock detected)
func isSerializationError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && (mysqlErr.Number == 1213 || mysqlErr.Number == 1205) {
		return true
	}
	var stateErr interface{ SQLState() string }
	if errors.As(err, &stateErr) {
		return stateErr.SQLState() == "40001" || stateErr.SQLState() == "40P01"
	}
	return false
}

// isDatabaseExistsError reports whether err is the server refusing a bare
// CREATE DATABASE because the database already exists (MySQL 1007,
// ER_DB_CREATE_EXISTS). The message fallback matches how the rest of the
// codebase detects Dolt's variant of this error (see internal/doltserver and
// internal/storage/dolt), whose text is "can't create database ...; database
// exists" but whose driver error number has not always been populated.
func isDatabaseExistsError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1007 {
		return true
	}
	if err == nil {
		return false
	}
	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "database exists") || strings.Contains(errLower, "1007")
}
