package issueops

import (
	"errors"
	"fmt"
	"testing"

	gmssql "github.com/dolthub/go-mysql-server/sql"
	"github.com/go-sql-driver/mysql"
)

func TestIsCreateOnlyDuplicateError(t *testing.T) {
	for _, err := range []error{
		&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"},
		fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1062}),
		gmssql.ErrPrimaryKeyViolation.New(),
		gmssql.ErrUniqueKeyViolation.New(),
	} {
		if !isCreateOnlyDuplicateError(err) {
			t.Fatalf("isCreateOnlyDuplicateError(%v) = false, want true", err)
		}
	}
}

func TestIsCreateOnlyDuplicateErrorPreservesOtherFailures(t *testing.T) {
	err := &mysql.MySQLError{Number: 1045, Message: "access denied"}
	if isCreateOnlyDuplicateError(err) {
		t.Fatal("isCreateOnlyDuplicateError() = true for non-duplicate MySQL error")
	}
	if isCreateOnlyDuplicateError(errors.New("duplicate wording only")) {
		t.Fatal("isCreateOnlyDuplicateError() = true for untyped error")
	}
}
