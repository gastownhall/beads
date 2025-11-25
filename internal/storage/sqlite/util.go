package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// QueryContext exposes the underlying database QueryContext method for advanced queries
func (s *SQLiteStorage) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, query, args...)
}

// BeginTx starts a new database transaction
// This is used by commands that need to perform multiple operations atomically
func (s *SQLiteStorage) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, nil)
}

// withTx executes a function within a database transaction.
// If the function returns an error, the transaction is rolled back.
// Otherwise, the transaction is committed.
func (s *SQLiteStorage) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDBError("begin transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return wrapDBError("commit transaction", err)
	}

	return nil
}

// ExecInTransaction is deprecated. Use withTx instead.
func (s *SQLiteStorage) ExecInTransaction(ctx context.Context, fn func(*sql.Tx) error) error {
	return s.withTx(ctx, fn)
}

// IsUniqueConstraintError checks if an error is a UNIQUE constraint violation
func IsUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// IsForeignKeyConstraintError checks if an error is a FOREIGN KEY constraint violation
// This can occur when importing issues that reference deleted issues (e.g., after merge)
func IsForeignKeyConstraintError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "FOREIGN KEY constraint failed") ||
		strings.Contains(errStr, "foreign key constraint failed")
}

// IsBusyError checks if an error is a database busy/locked error
func IsBusyError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "database is locked") ||
		strings.Contains(errStr, "SQLITE_BUSY")
}

// beginImmediateWithRetry starts an IMMEDIATE transaction with exponential backoff retry
// on SQLITE_BUSY errors. This addresses bd-ola6: under concurrent write load, BEGIN IMMEDIATE
// can fail with SQLITE_BUSY, so we retry with exponential backoff instead of failing immediately.
//
// Parameters:
//   - ctx: context for cancellation checking
//   - conn: dedicated database connection (must use same connection for entire transaction)
//   - maxRetries: maximum number of retry attempts (default: 5)
//   - initialDelay: initial backoff delay (default: 10ms)
//
// Returns error if:
//   - Context is canceled
//   - BEGIN IMMEDIATE fails with non-busy error
//   - All retries exhausted with SQLITE_BUSY
func beginImmediateWithRetry(ctx context.Context, conn *sql.Conn, maxRetries int, initialDelay time.Duration) error {
	if maxRetries <= 0 {
		maxRetries = 5
	}
	if initialDelay <= 0 {
		initialDelay = 10 * time.Millisecond
	}

	var lastErr error
	delay := initialDelay

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Check context cancellation before each attempt
		if err := ctx.Err(); err != nil {
			return err
		}

		// Attempt to begin transaction
		_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		if err == nil {
			return nil // Success
		}

		lastErr = err

		// If not a busy error, fail immediately
		if !IsBusyError(err) {
			return err
		}

		// On last attempt, don't sleep
		if attempt == maxRetries {
			break
		}

		// Exponential backoff: sleep before retry
		select {
		case <-time.After(delay):
			delay *= 2 // Double the delay for next attempt
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return lastErr // Return the last SQLITE_BUSY error after exhausting retries
}
