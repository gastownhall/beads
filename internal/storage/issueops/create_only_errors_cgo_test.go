//go:build cgo

package issueops

import (
	"testing"

	gmssql "github.com/dolthub/go-mysql-server/sql"
)

func TestIsCreateOnlyDuplicateErrorRecognizesEmbeddedErrors(t *testing.T) {
	for _, err := range []error{
		gmssql.ErrPrimaryKeyViolation.New(),
		gmssql.ErrUniqueKeyViolation.New(),
	} {
		if !isCreateOnlyDuplicateError(err) {
			t.Fatalf("isCreateOnlyDuplicateError(%v) = false, want true", err)
		}
	}
}
