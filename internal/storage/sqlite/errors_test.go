package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// TestWrapDBError tests the wrapDBError function
func TestWrapDBError(t *testing.T) {
	tests := []struct {
		name      string
		op        string
		err       error
		wantNil   bool
		wantError string
		wantType  error
	}{
		{
			name:    "nil error returns nil",
			op:      "test operation",
			err:     nil,
			wantNil: true,
		},
		{
			name:      "sql.ErrNoRows converted to ErrNotFound",
			op:        "get issue",
			err:       sql.ErrNoRows,
			wantError: "get issue: not found",
			wantType:  ErrNotFound,
		},
		{
			name:      "generic error wrapped with context",
			op:        "update issue",
			err:       errors.New("database locked"),
			wantError: "update issue: database locked",
		},
		{
			name:      "already wrapped error preserved",
			op:        "delete issue",
			err:       fmt.Errorf("constraint violation: %w", ErrConflict),
			wantError: "delete issue: constraint violation: conflict",
			wantType:  ErrConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := wrapDBError(tt.op, tt.err)

			if tt.wantNil {
				if result != nil {
					t.Errorf("wrapDBError() = %v, want nil", result)
				}
				return
			}

			if result == nil {
				t.Fatal("wrapDBError() returned nil, want error")
			}

			if tt.wantError != "" && result.Error() != tt.wantError {
				t.Errorf("wrapDBError() error = %q, want %q", result.Error(), tt.wantError)
			}

			if tt.wantType != nil && !errors.Is(result, tt.wantType) {
				t.Errorf("wrapDBError() error doesn't wrap %v", tt.wantType)
			}
		})
	}
}

// TestWrapDBErrorf tests the wrapDBErrorf function
func TestWrapDBErrorf(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		format    string
		args      []interface{}
		wantNil   bool
		wantError string
		wantType  error
	}{
		{
			name:    "nil error returns nil",
			err:     nil,
			format:  "operation %s on %s",
			args:    []interface{}{"update", "issue-123"},
			wantNil: true,
		},
		{
			name:      "sql.ErrNoRows converted to ErrNotFound with formatting",
			err:       sql.ErrNoRows,
			format:    "get issue %s",
			args:      []interface{}{"bd-abc"},
			wantError: "get issue bd-abc: not found",
			wantType:  ErrNotFound,
		},
		{
			name:      "generic error with formatted context",
			err:       errors.New("timeout"),
			format:    "query %s with filter %s",
			args:      []interface{}{"issues", "status=open"},
			wantError: "query issues with filter status=open: timeout",
		},
		{
			name:      "multiple format args",
			err:       errors.New("invalid value"),
			format:    "update %s field %s to %v",
			args:      []interface{}{"issue-123", "priority", 1},
			wantError: "update issue-123 field priority to 1: invalid value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := wrapDBErrorf(tt.err, tt.format, tt.args...)

			if tt.wantNil {
				if result != nil {
					t.Errorf("wrapDBErrorf() = %v, want nil", result)
				}
				return
			}

			if result == nil {
				t.Fatal("wrapDBErrorf() returned nil, want error")
			}

			if tt.wantError != "" && result.Error() != tt.wantError {
				t.Errorf("wrapDBErrorf() error = %q, want %q", result.Error(), tt.wantError)
			}

			if tt.wantType != nil && !errors.Is(result, tt.wantType) {
				t.Errorf("wrapDBErrorf() error doesn't wrap %v", tt.wantType)
			}
		})
	}
}

// TestSentinelErrors tests the sentinel error constants
func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		check func(error) bool
		want  bool
	}{
		{
			name:  "ErrNotFound detected by IsNotFound",
			err:   ErrNotFound,
			check: IsNotFound,
			want:  true,
		},
		{
			name:  "wrapped ErrNotFound detected",
			err:   fmt.Errorf("get issue: %w", ErrNotFound),
			check: IsNotFound,
			want:  true,
		},
		{
			name:  "other error not detected as ErrNotFound",
			err:   errors.New("other error"),
			check: IsNotFound,
			want:  false,
		},
		{
			name:  "ErrConflict detected by IsConflict",
			err:   ErrConflict,
			check: IsConflict,
			want:  true,
		},
		{
			name:  "wrapped ErrConflict detected",
			err:   fmt.Errorf("unique constraint: %w", ErrConflict),
			check: IsConflict,
			want:  true,
		},
		{
			name:  "ErrCycle detected by IsCycle",
			err:   ErrCycle,
			check: IsCycle,
			want:  true,
		},
		{
			name:  "wrapped ErrCycle detected",
			err:   fmt.Errorf("dependency check: %w", ErrCycle),
			check: IsCycle,
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.check(tt.err)
			if got != tt.want {
				t.Errorf("check(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestErrorChaining tests that error chains are preserved through operations
func TestErrorChaining(t *testing.T) {
	// Create a chain: root -> middle -> top
	root := errors.New("root cause")
	middle := fmt.Errorf("middle layer: %w", root)
	top := wrapDBError("top operation", middle)

	// Verify we can unwrap to each level
	if !errors.Is(top, middle) {
		t.Error("top error doesn't wrap middle error")
	}
	if !errors.Is(top, root) {
		t.Error("top error doesn't wrap root error")
	}

	// Verify error message includes all context
	want := "top operation: middle layer: root cause"
	if top.Error() != want {
		t.Errorf("error message = %q, want %q", top.Error(), want)
	}
}

// TestSQLErrNoRowsConversion tests that sql.ErrNoRows is consistently converted
func TestSQLErrNoRowsConversion(t *testing.T) {
	// Both wrapping functions should convert sql.ErrNoRows to ErrNotFound
	err1 := wrapDBError("get config", sql.ErrNoRows)
	err2 := wrapDBErrorf(sql.ErrNoRows, "get metadata %s", "key")

	if !IsNotFound(err1) {
		t.Error("wrapDBError didn't convert sql.ErrNoRows to ErrNotFound")
	}
	if !IsNotFound(err2) {
		t.Error("wrapDBErrorf didn't convert sql.ErrNoRows to ErrNotFound")
	}

	// The conversion replaces sql.ErrNoRows with ErrNotFound (not wrapped together)
	// This is intentional - we want a single, clean error type for "not found" conditions
	// The error message should indicate the operation context
	if err1.Error() != "get config: not found" {
		t.Errorf("err1 message = %q, want %q", err1.Error(), "get config: not found")
	}
	if err2.Error() != "get metadata key: not found" {
		t.Errorf("err2 message = %q, want %q", err2.Error(), "get metadata key: not found")
	}
}
