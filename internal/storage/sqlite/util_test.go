package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestIsUniqueConstraintError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "UNIQUE constraint error",
			err:      errors.New("UNIQUE constraint failed: issues.id"),
			expected: true,
		},
		{
			name:     "unique constraint lowercase",
			err:      errors.New("unique constraint failed: issues.id"),
			expected: false, // SQLite uses uppercase "UNIQUE"
		},
		{
			name:     "other error",
			err:      errors.New("some other database error"),
			expected: false,
		},
		{
			name:     "empty error message",
			err:      errors.New(""),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsUniqueConstraintError(tt.err)
			if result != tt.expected {
				t.Errorf("IsUniqueConstraintError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

func TestIsForeignKeyConstraintError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "FOREIGN KEY constraint error (uppercase)",
			err:      errors.New("FOREIGN KEY constraint failed"),
			expected: true,
		},
		{
			name:     "foreign key constraint error (lowercase)",
			err:      errors.New("foreign key constraint failed"),
			expected: true,
		},
		{
			name:     "FOREIGN KEY with details",
			err:      errors.New("FOREIGN KEY constraint failed: dependencies.depends_on_id"),
			expected: true,
		},
		{
			name:     "UNIQUE constraint error",
			err:      errors.New("UNIQUE constraint failed: issues.id"),
			expected: false,
		},
		{
			name:     "other error",
			err:      errors.New("some other database error"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsForeignKeyConstraintError(tt.err)
			if result != tt.expected {
				t.Errorf("IsForeignKeyConstraintError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

func TestExecInTransaction(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, t.TempDir()+"/test.db")
	defer store.Close()

	t.Run("successful transaction", func(t *testing.T) {
		err := store.ExecInTransaction(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO config (key, value) VALUES (?, ?)", "test_key", "test_value")
			return err
		})
		if err != nil {
			t.Errorf("Transaction failed: %v", err)
		}

		// Verify the data was committed
		var value string
		err = store.db.QueryRowContext(ctx, "SELECT value FROM config WHERE key = ?", "test_key").Scan(&value)
		if err != nil {
			t.Errorf("Failed to query inserted value: %v", err)
		}
		if value != "test_value" {
			t.Errorf("Expected value 'test_value', got '%s'", value)
		}
	})

	t.Run("failed transaction rolls back", func(t *testing.T) {
		expectedErr := errors.New("intentional error")
		err := store.ExecInTransaction(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO config (key, value) VALUES (?, ?)", "rollback_key", "rollback_value")
			if err != nil {
				return err
			}
			return expectedErr
		})
		if err != expectedErr {
			t.Errorf("Expected error %v, got %v", expectedErr, err)
		}

		// Verify the data was not committed
		var value string
		err = store.db.QueryRowContext(ctx, "SELECT value FROM config WHERE key = ?", "rollback_key").Scan(&value)
		if err != sql.ErrNoRows {
			t.Errorf("Expected no rows, but got value: %s (err: %v)", value, err)
		}
	})
}

func TestBeginTx(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, t.TempDir()+"/test.db")
	defer store.Close()

	tx, err := store.BeginTx(ctx)
	if err != nil {
		t.Fatalf("Failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	// Verify transaction is active
	_, err = tx.ExecContext(ctx, "INSERT INTO config (key, value) VALUES (?, ?)", "tx_test", "value")
	if err != nil {
		t.Errorf("Failed to execute in transaction: %v", err)
	}

	// Rollback and verify data not committed
	if err := tx.Rollback(); err != nil {
		t.Errorf("Failed to rollback: %v", err)
	}

	var value string
	err = store.db.QueryRowContext(ctx, "SELECT value FROM config WHERE key = ?", "tx_test").Scan(&value)
	if err != sql.ErrNoRows {
		t.Errorf("Expected no rows after rollback, got: %s", value)
	}
}

func TestQueryContext(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, t.TempDir()+"/test.db")
	defer store.Close()

	// Insert test data
	_, err := store.db.ExecContext(ctx, "INSERT INTO config (key, value) VALUES (?, ?)", "query_test", "query_value")
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	rows, err := store.QueryContext(ctx, "SELECT key, value FROM config WHERE key = ?", "query_test")
	if err != nil {
		t.Fatalf("QueryContext failed: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("Expected at least one row")
	}

	var key, value string
	if err := rows.Scan(&key, &value); err != nil {
		t.Errorf("Failed to scan row: %v", err)
	}

	if key != "query_test" || value != "query_value" {
		t.Errorf("Expected (query_test, query_value), got (%s, %s)", key, value)
	}

	if rows.Next() {
		t.Error("Expected only one row")
	}
}

func TestIsBusyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "database is locked",
			err:      errors.New("database is locked"),
			expected: true,
		},
		{
			name:     "SQLITE_BUSY",
			err:      errors.New("SQLITE_BUSY"),
			expected: true,
		},
		{
			name:     "SQLITE_BUSY with context",
			err:      errors.New("failed to begin: SQLITE_BUSY: database is locked"),
			expected: true,
		},
		{
			name:     "other error",
			err:      errors.New("some other database error"),
			expected: false,
		},
		{
			name:     "UNIQUE constraint error",
			err:      errors.New("UNIQUE constraint failed: issues.id"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsBusyError(tt.err)
			if result != tt.expected {
				t.Errorf("IsBusyError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}

func TestBeginImmediateWithRetry(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, t.TempDir()+"/test.db")
	defer store.Close()

	t.Run("successful on first try", func(t *testing.T) {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Failed to acquire connection: %v", err)
		}
		defer conn.Close()

		err = beginImmediateWithRetry(ctx, conn, 5, 10)
		if err != nil {
			t.Errorf("beginImmediateWithRetry failed: %v", err)
		}

		// Rollback to clean up
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
	})

	t.Run("context cancellation", func(t *testing.T) {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Failed to acquire connection: %v", err)
		}
		defer conn.Close()

		cancelCtx, cancel := context.WithCancel(ctx)
		cancel() // Cancel immediately

		err = beginImmediateWithRetry(cancelCtx, conn, 5, 10)
		if err == nil {
			t.Error("Expected context cancellation error, got nil")
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Expected context.Canceled, got %v", err)
		}
	})

	t.Run("defaults for invalid parameters", func(t *testing.T) {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Failed to acquire connection: %v", err)
		}
		defer conn.Close()

		// Should use defaults (5 retries, 10ms delay) when passed invalid values
		err = beginImmediateWithRetry(ctx, conn, 0, 0)
		if err != nil {
			t.Errorf("beginImmediateWithRetry with invalid params failed: %v", err)
		}

		// Rollback to clean up
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
	})
}
