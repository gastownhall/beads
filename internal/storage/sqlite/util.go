package sqlite

import (
	"context"
	"database/sql"
	"strings"
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
// Uses BEGIN IMMEDIATE to acquire the write lock early, preventing deadlocks
// in concurrent scenarios. If the function returns an error, the transaction
// is rolled back. Otherwise, the transaction is committed.
//
// The connection's busy_timeout pragma (30s by default) handles SQLITE_BUSY
// retries internally - no additional retry logic is needed here.
//
// This fixes GH#1272: database lock errors during concurrent operations.
func (s *SQLiteStorage) withTx(ctx context.Context, fn func(*sql.Conn) error) error {
	// Acquire a dedicated connection for the transaction.
	// This ensures all operations in the transaction use the same connection.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return wrapDBError("acquire connection", err)
	}
	defer func() { _ = conn.Close() }()

	// Start IMMEDIATE transaction to acquire write lock early.
	// BEGIN IMMEDIATE prevents deadlocks by acquiring the write lock upfront
	// rather than upgrading from a read lock later. The connection's
	// busy_timeout pragma (30s) handles retries if another writer holds the lock.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return wrapDBError("begin transaction", err)
	}

	// Track commit state for cleanup
	committed := false
	defer func() {
		if !committed {
			// Use background context to ensure rollback completes even if ctx is canceled
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Execute user function
	if err := fn(conn); err != nil {
		return err
	}

	// Commit the transaction
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return wrapDBError("commit transaction", err)
	}
	committed = true
	return nil
}

// ExecInTransaction is deprecated. Use withTx instead.
func (s *SQLiteStorage) ExecInTransaction(ctx context.Context, fn func(*sql.Conn) error) error {
	return s.withTx(ctx, fn)
}

// dbExecutor is an interface satisfied by both *sql.Tx and *sql.Conn.
// This allows helper functions to work with either transaction type.
type dbExecutor interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
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
