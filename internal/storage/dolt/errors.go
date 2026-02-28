package dolt

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
)

// Sentinel errors for the dolt storage layer.
// These complement the storage-level sentinels (storage.ErrNotFound, etc.)
// with dolt-specific error types.
var (
	// ErrTransaction indicates a transaction begin/commit/rollback failure.
	ErrTransaction = errors.New("transaction error")

	// ErrQuery indicates a database query failure.
	ErrQuery = errors.New("query error")

	// ErrScan indicates a failure scanning database rows into Go values.
	ErrScan = errors.New("scan error")

	// ErrExec indicates a database exec (INSERT/UPDATE/DELETE) failure.
	ErrExec = errors.New("exec error")
)

// wrapDBError wraps a database error with operation context.
// If err is sql.ErrNoRows, it is converted to storage.ErrNotFound.
// If err is nil, nil is returned.
func wrapDBError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storage.ErrNotFound)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// wrapTransactionError wraps a transaction error with operation context.
func wrapTransactionError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w: %w", op, ErrTransaction, err)
}

// wrapScanError wraps a row scan error with operation context.
func wrapScanError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w: %w", op, ErrScan, err)
}

// wrapQueryError wraps a query error with operation context.
func wrapQueryError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w: %w", op, ErrQuery, err)
}

// wrapExecError wraps an exec error with operation context.
func wrapExecError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w: %w", op, ErrExec, err)
}
