package dolt

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestPooledReadTimeoutBreakerAccounting pins how a read that ran its pool
// deadline feeds the circuit breaker: it is a connection failure, recorded
// exactly once even though the read is not retried, and the typed error
// survives the wrapper the breaker adds when that failure trips it.
func TestPooledReadTimeoutBreakerAccounting(t *testing.T) {
	ctx := context.Background()
	slowCounts := searchCountsHandler(func(int) (driver.Rows, error) { return failSlowly() })

	t.Run("records one failure", func(t *testing.T) {
		s, d := newPooledReadStore(t, testPoolReadDeadline, slowCounts)
		s.breaker = newTestCircuitBreaker(t)

		_, err := s.SearchIssuesWithCounts(ctx, "", types.IssueFilter{})
		var rte *storage.ReadTimeoutError
		if !errors.As(err, &rte) {
			t.Fatalf("error = %v, want *storage.ReadTimeoutError", err)
		}
		if got := s.breaker.readState().Failures; got != 1 {
			t.Errorf("breaker recorded %d failures, want exactly 1", got)
		}
		if got := d.Begins(); got != 1 {
			t.Errorf("read transaction opened %d times, want 1", got)
		}
	})

	t.Run("trip keeps the typed error", func(t *testing.T) {
		s, d := newPooledReadStore(t, testPoolReadDeadline, slowCounts)
		s.breaker = newTestCircuitBreaker(t)
		for i := 0; i < circuitFailureThreshold-1; i++ {
			s.breaker.RecordFailure()
		}

		_, err := s.SearchIssuesWithCounts(ctx, "", types.IssueFilter{})
		if got := s.breaker.State(); got != circuitOpen {
			t.Fatalf("breaker state = %q after the threshold-th failure, want %q", got, circuitOpen)
		}
		if got := s.breaker.readState().Failures; got != circuitFailureThreshold {
			t.Errorf("breaker recorded %d failures, want %d", got, circuitFailureThreshold)
		}
		if err == nil || !strings.Contains(err.Error(), "circuit breaker tripped") {
			t.Errorf("error = %v, want the breaker-trip wrapper", err)
		}
		var rte *storage.ReadTimeoutError
		if !errors.As(err, &rte) {
			t.Errorf("errors.As(%v, *storage.ReadTimeoutError) = false through the breaker-trip wrapper", err)
		}
		if got := d.Begins(); got != 1 {
			t.Errorf("read transaction opened %d times, want 1", got)
		}
	})
}
