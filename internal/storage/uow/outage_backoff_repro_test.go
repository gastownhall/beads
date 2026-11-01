package uow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// TestPingWithRetryKeepsBackingOffAfterTheOutageWindow: one upstream-outage
// report arms the window; every later attempt fails with an ordinary transient
// error. Once the window has passed, those waits must still follow the
// exponential backoff, not clamp to the past deadline (zero) and spin (#7183).
func TestPingWithRetryKeepsBackingOffAfterTheOutageWindow(t *testing.T) {
	shortOutageWindow(t, 20*time.Millisecond)
	var calls atomic.Int64
	p := pingerFunc(func(context.Context) error {
		if calls.Add(1) == 1 {
			return proxyOutageErr()
		}
		return mysql.ErrInvalidConn
	})
	bo := testPingBackOff()
	bo.InitialInterval = 50 * time.Millisecond
	bo.MaxInterval = 50 * time.Millisecond // capped below the budget, so only Stop ends it
	bo.MaxElapsedTime = 300 * time.Millisecond
	err := pingWithRetry(context.Background(), p, bo, testPingAttemptTimeout)
	if err == nil {
		t.Fatal("pingWithRetry() = nil, want the transient error once the budget is spent")
	}
	// Waits of 25-75ms over a 300ms budget: a real backoff makes well under 20
	// attempts; a zero wait makes tens of thousands.
	if n := calls.Load(); n > 20 {
		t.Fatalf("PingContext called %d times in a 300ms budget, want a backoff (<= 20 calls)", n)
	}
}
