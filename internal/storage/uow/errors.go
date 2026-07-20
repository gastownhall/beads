package uow

import (
	"errors"

	mysql "github.com/go-sql-driver/mysql"
)

// IsSerializationError reports whether err is a Dolt/MySQL serialization
// failure that guarantees the server rolled the transaction back. Because the
// rollback discards every uncommitted write in the session, the only safe
// retry is to redo the WHOLE unit of work (read, merge, write, commit) —
// retrying just the commit re-commits an empty session, which Dolt reports as
// "nothing to commit" while the write is silently lost.
func IsSerializationError(err error) bool {
	return isSerializationError(err)
}

// isSerializationError returns true if the error is a Dolt/MySQL serialization
// failure that guarantees the transaction was rolled back. Safe to retry.
//   - 1213 (ER_LOCK_DEADLOCK): concurrent transactions conflict at commit time
//   - 1205 (ER_LOCK_WAIT_TIMEOUT): lock wait exceeded, transaction rolled back
func isSerializationError(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == 1213 || mysqlErr.Number == 1205
}
