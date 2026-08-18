//go:build cgo

package issueops

import gmssql "github.com/dolthub/go-mysql-server/sql"

func isEmbeddedDuplicateError(err error) bool {
	return gmssql.ErrPrimaryKeyViolation.Is(err) || gmssql.ErrUniqueKeyViolation.Is(err)
}
