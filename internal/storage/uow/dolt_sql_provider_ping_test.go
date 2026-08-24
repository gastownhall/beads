package uow

import (
	"database/sql/driver"
	"io"
	"testing"
)

func TestIsRetryablePingError(t *testing.T) {
	for _, tt := range []struct {
		name      string
		err       error
		retryable bool
	}{
		{name: "bad connection", err: driver.ErrBadConn, retryable: true},
		{name: "EOF", err: io.EOF, retryable: true},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, retryable: true},
		{name: "access denied is not retried", err: newMySQLError(1045), retryable: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryablePingError(tt.err); got != tt.retryable {
				t.Fatalf("isRetryablePingError(%v) = %t, want %t", tt.err, got, tt.retryable)
			}
		})
	}
}
