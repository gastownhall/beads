//go:build cgo

package dolt

import (
	"context"
	"errors"
	"testing"
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

func TestWithRetry_EmbeddedMode(t *testing.T) {
	// In embedded mode (serverMode=false), withRetry should just call the operation once
	store := &DoltStore{serverMode: false}

	callCount := 0
	err := store.withRetry(context.Background(), func() error {
		callCount++
		return errors.New("driver: bad connection")
	})

	if err == nil {
		t.Error("expected error, got nil")
	}
	if callCount != 1 {
		t.Errorf("expected 1 call in embedded mode, got %d", callCount)
	}
}

func TestWithRetry_ServerMode_Success(t *testing.T) {
	store := &DoltStore{serverMode: true}

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

func TestWithRetry_ServerMode_RetryOnBadConnection(t *testing.T) {
	store := &DoltStore{serverMode: true}

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

func TestWithRetry_ServerMode_RetryOnUnknownDatabase(t *testing.T) {
	// Simulates the GH-1851 race: "Unknown database" is transient after CREATE DATABASE
	store := &DoltStore{serverMode: true}

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

func TestWithRetry_ServerMode_NonRetryableError(t *testing.T) {
	store := &DoltStore{serverMode: true}

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
