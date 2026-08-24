package uow

import (
	"context"
	"database/sql/driver"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
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

// TestOpenDBWrapsPingFailure asserts openDB's give-up wrapping
// (errors.Join(fmt.Errorf("uow: ping db: %w", err), conn.Close())) survives
// unchanged. Port 1 is a reserved TCP port nothing listens on, so the dial
// is refused immediately — a non-retryable failure per isRetryablePingError
// — which returns from pingUntilReady through the same `return lastErr` line
// the deadline-exhausted branch uses, so this exercises openDB's wrap
// without waiting out pingRetryDeadline.
func TestOpenDBWrapsPingFailure(t *testing.T) {
	dsn := buildDSN(proxy.Endpoint{Host: "127.0.0.1", Port: 1}, "beads", "root", "", "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := openDB(ctx, dsn)
	if conn != nil {
		t.Fatalf("openDB(%q) returned a non-nil *sql.DB alongside an error", dsn)
	}
	if err == nil {
		t.Fatalf("openDB(%q) succeeded against a closed port; want an error", dsn)
	}

	const wantPrefix = "uow: ping db: "
	if !strings.HasPrefix(err.Error(), wantPrefix) {
		t.Fatalf("openDB error = %q, want prefix %q", err.Error(), wantPrefix)
	}
}
