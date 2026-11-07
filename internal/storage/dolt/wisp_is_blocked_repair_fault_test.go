package dolt

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestDoltNew_WispIsBlockedRepairFailureRetries_RealDolt is AC-6 on the real
// open path: a fault at any stage of the ignored 0032 restore must fail the
// open with the fault surfaced, record no ignored 0032 row (the marker stays
// pending), and the next open with the fault cleared must resume the repair
// to completion -- shape, recomputed flags, and exactly one 32. The stage
// strings deliberately mirror schema's wispIsBlockedRestoreAfter* constants as
// literals: if the two ever drift apart, the fault stops firing and these
// assertions fail rather than silently skipping.
func TestDoltNew_WispIsBlockedRepairFailureRetries_RealDolt(t *testing.T) {
	for _, stage := range []string{"after_column", "after_recompute", "after_index"} {
		t.Run(stage, func(t *testing.T) {
			c := newWispIsBlockedClone(t)
			g := c.seedGraph()
			c.breakShape()
			c.store.Close()

			fault := errors.New("injected fault")
			restore := schema.SetWispIsBlockedRestoreFaultHookForTest(func(at string) error {
				if at == stage {
					return fault
				}
				return nil
			})
			broken, err := openStoreWithOwnBudget(t, c.cfg)
			restore()
			if err == nil {
				c.store = broken
				t.Fatalf("open with a fault pinned at %s must fail and leave 0032 pending", stage)
			}
			if !strings.Contains(err.Error(), "wisp is_blocked restore fault at "+stage) {
				t.Errorf("open error = %v, want the %s fault surfaced", err, stage)
			}

			// The failed pass recorded nothing, so the retry re-runs the
			// whole repair rather than skipping past a recorded marker.
			raw := rawTestConn(t, testServerPort)
			defer raw.Close()
			var recorded int
			if err := raw.QueryRowContext(c.ctx,
				"SELECT COUNT(*) FROM `"+c.cfg.Database+"`.ignored_schema_migrations WHERE version = 32").
				Scan(&recorded); err != nil {
				t.Errorf("read ignored cursor over a raw conn: %v", err)
			} else if recorded != 0 {
				t.Errorf("failed pass recorded ignored 0032 %d times, want 0 (the repair must stay pending for retry)", recorded)
			}

			healed, err := openStoreWithOwnBudget(t, c.cfg)
			if err != nil {
				t.Fatalf("New after the fault cleared: %v", err)
			}
			c.store = healed
			if c.count("column probe", wispIsBlockedColumnProbe) != 1 || c.count("index probe", wispIsBlockedIndexProbe) == 0 {
				t.Fatal("retried open did not restore wisps.is_blocked and idx_wisps_is_blocked")
			}
			if got := c.blockedFlags("wisps", g.b, g.c, g.d); !slices.Equal(got, []int{1, 1, 0}) {
				t.Fatalf("retried wisp is_blocked (b, c, d) = %v, want [1 1 0]: the retry must resume through the recompute", got)
			}
			if n := c.count("cursor rows", "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); n != 1 {
				t.Fatalf("healed open recorded ignored 0032 %d times, want exactly 1", n)
			}
		})
	}
}
