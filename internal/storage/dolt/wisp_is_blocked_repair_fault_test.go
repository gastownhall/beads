package dolt

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestDoltNew_WispIsBlockedRepairFailureRetries_RealDolt covers AC-6: a fault
// at any stage of the restore must fail the open with the fault surfaced,
// record no ignored 0032 row (the marker stays pending), and the next open
// with the fault cleared must resume the repair to completion -- shape,
// recomputed flags, and exactly one 32. The stage strings deliberately mirror
// schema's wispIsBlockedRestoreAfter* constants as literals: if the two ever
// drift apart, the fault stops firing and these assertions fail rather than
// silently skipping.
func TestDoltNew_WispIsBlockedRepairFailureRetries_RealDolt(t *testing.T) {
	for _, stage := range []string{"after_column", "after_recompute", "after_index"} {
		t.Run(stage, func(t *testing.T) {
			env := newWispHealEnv(t)

			store, err := env.open()
			if err != nil {
				t.Fatalf("New (fresh): %v", err)
			}
			seedWispBlockedGraph(t, store)
			driftWispIsBlockedForHeal(t, store)
			rewindIgnoredCursor(t, store, 32)
			if err := store.Close(); err != nil {
				t.Fatalf("close drifted store: %v", err)
			}

			fault := errors.New("injected fault")
			restore := schema.SetWispIsBlockedRestoreFaultHookForTest(func(at string) error {
				if at == stage {
					return fault
				}
				return nil
			})
			broken, err := env.open()
			if broken != nil {
				_ = broken.Close()
			}
			if err == nil {
				restore()
				t.Fatalf("open with a fault pinned at %s must fail and leave 0032 pending", stage)
			}
			if !strings.Contains(err.Error(), "wisp is_blocked restore fault at "+stage) {
				restore()
				t.Fatalf("open error = %v, want the %s fault surfaced", err, stage)
			}
			restore()

			// The failed pass recorded nothing, so the retry re-runs the
			// whole repair rather than skipping past a recorded marker.
			raw := rawTestConn(t, testServerPort)
			defer raw.Close()
			ctx, cancel := testContext(t)
			defer cancel()
			var recorded int
			if err := raw.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM `"+env.dbName+"`.ignored_schema_migrations WHERE version = 32").
				Scan(&recorded); err != nil {
				t.Fatalf("read ignored cursor over a raw conn: %v", err)
			}
			if recorded != 0 {
				t.Fatalf("failed pass recorded ignored 0032 %d times, want 0 (the repair must stay pending for retry)", recorded)
			}

			healed, err := env.open()
			if err != nil {
				t.Fatalf("New after the fault cleared: %v", err)
			}
			defer func() { _ = healed.Close() }()
			env.requireWispIsBlockedShape(healed)
			for id, want := range map[string]bool{"wh-b": true, "wh-c": true, "wh-d": false} {
				if got := env.wispFlags(healed)[id]; got != want {
					t.Fatalf("retried flag for %s = %v, want %v: the retry must resume through the recompute", id, got, want)
				}
			}
			if n := env.scalarInt(healed, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); n != 1 {
				t.Fatalf("healed open recorded ignored 0032 %d times, want exactly 1", n)
			}
		})
	}
}
