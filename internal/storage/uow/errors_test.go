package uow

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsInvalidConnectionError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{name: "nil", err: nil, expected: false},
		{name: "invalid connection", err: errors.New("invalid connection"), expected: true},
		{name: "driver: bad connection", err: errors.New("driver: bad connection"), expected: true},
		{name: "lost connection", err: errors.New("Error 2013: Lost connection to MySQL server"), expected: true},
		{name: "broken pipe", err: errors.New("write: broken pipe"), expected: true},
		// Server restart / network blip — must match the DoltStore-layer
		// classifier so the same failure retries on both parallel write paths.
		{name: "connection reset", err: errors.New("read: connection reset by peer"), expected: true},
		{name: "connection refused", err: errors.New("dial tcp 127.0.0.1:3306: connect: connection refused"), expected: true},
		// Real driver errors arrive wrapped; the substring match must see through it.
		{name: "wrapped connection refused", err: fmt.Errorf("uow: pin connection: %w", errors.New("connection refused")), expected: true},
		{name: "case insensitive", err: errors.New("Invalid Connection"), expected: true},
		{name: "syntax error - not retryable", err: errors.New("Error 1064: syntax error"), expected: false},
		{name: "table not found - not retryable", err: errors.New("Error 1146: Table not found"), expected: false},
		{name: "deadlock - not covered here", err: errors.New("Error 1213: Deadlock found"), expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isInvalidConnectionError(tt.err)
			if got != tt.expected {
				t.Errorf("isInvalidConnectionError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}
