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

// isAccessDeniedError reports whether err is the server refusing an operation
// because the connected credential is not allowed to perform it. MySQL numbers
// these 1044 (ER_DBACCESS_DENIED_ERROR) and 1045 (ER_ACCESS_DENIED_ERROR); Dolt
// reports the same refusals under the generic 1105 with the wording intact, so
// the text fallback is what classifies them there.
func isAccessDeniedError(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && (mysqlErr.Number == 1044 || mysqlErr.Number == 1045) {
		return true
	}
	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "access denied") || strings.Contains(errLower, "command denied")
}

// isDatabaseNotFoundError reports whether the server said the database does not
// exist: MySQL 1049 (ER_BAD_DB_ERROR, "Unknown database"), or Dolt's "database
// not found: <name>".
//
// A false answer is NOT evidence the database exists. A server deliberately
// hides existence from a credential that has not been granted the database,
// answering access-denied for present and absent names alike, so only a
// privileged credential can tell the two apart. Callers must report the denial
// as a denial rather than guessing absence from it.
func isDatabaseNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1049 {
		return true
	}
	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "database not found") || strings.Contains(errLower, "unknown database")
}
