package dolt

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestWrapDBError(t *testing.T) {
	t.Run("nil error returns nil", func(t *testing.T) {
		if err := wrapDBError("op", nil); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("sql.ErrNoRows converts to storage.ErrNotFound", func(t *testing.T) {
		err := wrapDBError("get issue", sql.ErrNoRows)
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
		if err.Error() != "get issue: not found" {
			t.Errorf("unexpected message: %s", err.Error())
		}
	})

	t.Run("other errors are wrapped with context", func(t *testing.T) {
		original := fmt.Errorf("connection refused")
		err := wrapDBError("query users", original)
		if !errors.Is(err, original) {
			t.Errorf("expected to wrap original error")
		}
		if err.Error() != "query users: connection refused" {
			t.Errorf("unexpected message: %s", err.Error())
		}
	})
}

func TestWrapTransactionError(t *testing.T) {
	t.Run("nil error returns nil", func(t *testing.T) {
		if err := wrapTransactionError("begin", nil); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("wraps with ErrTransaction sentinel", func(t *testing.T) {
		original := fmt.Errorf("connection reset")
		err := wrapTransactionError("begin tx", original)
		if !errors.Is(err, ErrTransaction) {
			t.Errorf("expected ErrTransaction in chain")
		}
		if !errors.Is(err, original) {
			t.Errorf("expected original error in chain")
		}
	})
}

func TestWrapScanError(t *testing.T) {
	t.Run("wraps with ErrScan sentinel", func(t *testing.T) {
		original := fmt.Errorf("invalid column type")
		err := wrapScanError("scan issue", original)
		if !errors.Is(err, ErrScan) {
			t.Errorf("expected ErrScan in chain")
		}
		if !errors.Is(err, original) {
			t.Errorf("expected original error in chain")
		}
	})
}

func TestWrapQueryError(t *testing.T) {
	t.Run("wraps with ErrQuery sentinel", func(t *testing.T) {
		original := fmt.Errorf("syntax error")
		err := wrapQueryError("search issues", original)
		if !errors.Is(err, ErrQuery) {
			t.Errorf("expected ErrQuery in chain")
		}
	})
}

func TestWrapExecError(t *testing.T) {
	t.Run("wraps with ErrExec sentinel", func(t *testing.T) {
		original := fmt.Errorf("duplicate key")
		err := wrapExecError("insert issue", original)
		if !errors.Is(err, ErrExec) {
			t.Errorf("expected ErrExec in chain")
		}
	})
}
