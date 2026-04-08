package dolt

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

func TestIsRetryableError(t *testing.T) {
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
			name:     "driver bad connection",
			err:      errors.New("driver: bad connection"),
			expected: true,
		},
		{
			name:     "Driver Bad Connection (case insensitive)",
			err:      errors.New("Driver: Bad Connection"),
			expected: true,
		},
		{
			name:     "invalid connection",
			err:      errors.New("invalid connection"),
			expected: true,
		},
		{
			name:     "broken pipe",
			err:      errors.New("write: broken pipe"),
			expected: true,
		},
		{
			name:     "connection reset",
			err:      errors.New("read: connection reset by peer"),
			expected: true,
		},
		{
			name:     "connection refused - retryable (server restart)",
			err:      errors.New("dial tcp: connection refused"),
			expected: true,
		},
		{
			name:     "database is read only - retryable",
			err:      errors.New("cannot update manifest: database is read only"),
			expected: true,
		},
		{
			name:     "Database Is Read Only (case insensitive)",
			err:      errors.New("Database Is Read Only"),
			expected: true,
		},
		{
			name:     "lost connection - retryable (MySQL error 2013)",
			err:      errors.New("Error 2013: Lost connection to MySQL server during query"),
			expected: true,
		},
		{
			name:     "server gone away - retryable (MySQL error 2006)",
			err:      errors.New("Error 2006: MySQL server has gone away"),
			expected: true,
		},
		{
			name:     "i/o timeout - retryable",
			err:      errors.New("read tcp 127.0.0.1:3307: i/o timeout"),
			expected: true,
		},
		{
			name:     "unknown database - retryable (catalog race GH-1851)",
			err:      errors.New("Error 1049 (42000): Unknown database 'beads_test'"),
			expected: true,
		},
		{
			name:     "Unknown Database (case insensitive)",
			err:      errors.New("Unknown Database 'beads_test'"),
			expected: true,
		},
		{
			name:     "no root value found in session",
			err:      errors.New("Error 1105 (HY000): no root value found in session"),
			expected: true,
		},
		{
			name:     "syntax error - not retryable",
			err:      errors.New("Error 1064: You have an error in your SQL syntax"),
			expected: false,
		},
		{
			name:     "table not found - not retryable",
			err:      errors.New("Error 1146: Table 'beads.foo' doesn't exist"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err)
			if got != tt.expected {
				t.Errorf("isRetryableError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestWithRetry_Success(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		return nil
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 call on success, got %d", callCount)
	}
}

func TestWithRetry_RetryOnBadConnection(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		if callCount < 3 {
			return errors.New("driver: bad connection")
		}
		return nil // Success on 3rd attempt
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 retries + success), got %d", callCount)
	}
}

func TestWithRetry_RetryOnUnknownDatabase(t *testing.T) {
	// Simulates the GH-1851 race: "Unknown database" is transient after CREATE DATABASE
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		if callCount < 3 {
			return errors.New("Error 1049 (42000): Unknown database 'beads_test'")
		}
		return nil // Catalog caught up on 3rd attempt
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 retries + success), got %d", callCount)
	}
}

func TestWithRetry_NonRetryableError(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		return errors.New("syntax error in SQL")
	})

	if err == nil {
		t.Error("expected error, got nil")
	}
	if callCount != 1 {
		t.Errorf("expected 1 call for non-retryable error, got %d", callCount)
	}
}

func TestIsRetryableWriteError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "database is locked",
			err:      errors.New("database is locked"),
			expected: true,
		},
		{
			name:     "serialization error",
			err:      &mysql.MySQLError{Number: 1213, Message: "deadlock found when trying to get lock"},
			expected: true,
		},
		{
			name:     "database is read only",
			err:      errors.New("cannot update manifest: database is read only"),
			expected: true,
		},
		{
			name:     "write lock timeout",
			err:      errors.New("failed to acquire write lock: timeout after 2s"),
			expected: true,
		},
		{
			name:     "syntax error",
			err:      errors.New("syntax error in SQL"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableWriteError(tt.err)
			if got != tt.expected {
				t.Errorf("isRetryableWriteError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestWithWriteRetry_RetryableError(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withWriteRetry(context.Background(), func() error {
		callCount++
		if callCount < 3 {
			return errors.New("database is locked")
		}
		return nil
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls (2 retries + success), got %d", callCount)
	}
}

func TestWithWriteRetry_NonRetryableError(t *testing.T) {
	store := &DoltStore{}

	callCount := 0
	err := store.withWriteRetry(context.Background(), func() error {
		callCount++
		return errors.New("validation failed")
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if callCount != 1 {
		t.Errorf("expected 1 call for non-retryable error, got %d", callCount)
	}
}

func TestWithSerializedWrite_SkipsLockOutsideServerMode(t *testing.T) {
	store := &DoltStore{serverMode: false}

	called := false
	err := store.withSerializedWrite(context.Background(), func() error {
		called = true
		return nil
	})

	if err != nil {
		t.Fatalf("withSerializedWrite returned error: %v", err)
	}
	if !called {
		t.Fatal("expected callback to run without server-mode lock")
	}
}

func TestCommitVersionedWrite_WrapsPartialWriteError(t *testing.T) {
	store := &DoltStore{
		commitVersionedWriteFn: func(context.Context, []string, string) error {
			return errors.New("commit failed")
		},
	}

	err := store.commitVersionedWrite(context.Background(), "add label", []string{"labels"}, "bd: label add test-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var partialErr *PartialWriteError
	if !errors.As(err, &partialErr) {
		t.Fatalf("expected PartialWriteError, got %T", err)
	}
	if partialErr.Operation != "add label" {
		t.Fatalf("expected operation to be recorded, got %q", partialErr.Operation)
	}
	if !strings.Contains(partialErr.Error(), "SQL write committed but Dolt history commit failed") {
		t.Fatalf("expected repairable-state wording, got %q", partialErr.Error())
	}
}

func TestReleaseServerWriteLock_LogsWarningOnFailure(t *testing.T) {
	store := &DoltStore{
		releaseServerWriteLockFn: func(context.Context, *sql.Conn, string) error {
			return errors.New("release failed")
		},
	}

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = origStderr
	})

	store.releaseServerWriteLock(context.Background(), nil, "bd_write_testdb")

	if err := w.Close(); err != nil {
		t.Fatalf("close write pipe: %v", err)
	}
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close read pipe: %v", err)
	}

	if !strings.Contains(string(output), `failed to release write lock "bd_write_testdb"`) {
		t.Fatalf("expected release warning, got %q", string(output))
	}
}
