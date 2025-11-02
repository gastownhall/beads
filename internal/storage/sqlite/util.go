package sqlite

import (
	"context"
	"database/sql"
	"fmt"
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
// If the function returns an error, the transaction is rolled back.
// Otherwise, the transaction is committed.
func (s *SQLiteStorage) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
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


