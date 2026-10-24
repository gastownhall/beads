package dolt

import (
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// TestWriteTxRetryScheduleKeepsRetryingAcrossItsBudget pins the schedule
// withRetryTx replays a write on. A writer that keeps losing commit-time
// conflicts (routine when every mutation also bumps one shared counter row, as
// with the events journal on) must keep getting attempts through its whole
// budget. Uncapped, the exponential waits grow to several seconds each and the
// 15s server budget holds at most 16 attempts; capped at a second, it holds at
// least 18 even with every wait jittered to its longest.
func TestWriteTxRetryScheduleKeepsRetryingAcrossItsBudget(t *testing.T) {
	for _, tc := range []struct {
		name       string
		serverMode bool
		budget     time.Duration
		minTries   int
	}{
		{name: "server", serverMode: true, budget: 15 * time.Second, minTries: 18},
		{name: "embedded", serverMode: false, budget: 5 * time.Second, minTries: 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for run := 0; run < 50; run++ {
				clock := &steppingClock{now: time.Unix(0, 0)}
				bo := newWriteTxRetryBackOff(tc.serverMode)
				bo.Clock = clock
				bo.Reset()
				tries := 1
				for {
					next := bo.NextBackOff()
					if next == backoff.Stop {
						break
					}
					if longest := writeTxRetryMaxInterval * 3 / 2; next > longest {
						t.Fatalf("wait %v exceeds the jittered cap of %v", next, longest)
					}
					clock.now = clock.now.Add(next)
					tries++
				}
				if tries < tc.minTries {
					t.Fatalf("a writer that keeps losing gets %d attempts in %v, want at least %d", tries, tc.budget, tc.minTries)
				}
				if elapsed := clock.now.Sub(time.Unix(0, 0)); elapsed > tc.budget {
					t.Fatalf("schedule waited %v, past its %v budget", elapsed, tc.budget)
				}
			}
		})
	}
}

// steppingClock is a backoff.Clock the test advances by each wait the
// schedule asks for, so the schedule runs without sleeping.
type steppingClock struct{ now time.Time }

func (c *steppingClock) Now() time.Time { return c.now }
