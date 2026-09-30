//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestReadyTextRouteStatementBudget is the budget for `bd ready`'s TEXT
// rendering now that it lists through issueops.ReadyLister like --json does.
//
// The text route used to list with the bare GetReadyWorkInTx — which hydrates
// labels and dependency state per plane in statements of its own — and then,
// when the page came back full, size the set in a SECOND transaction
// (CountReadyWorkInTx, behind a second defer-wake sweep) for its "Showing X of
// N". It now takes the single-pass read TestReadyWorkStatementBudget pins: the
// counts are hydrated in the page's own statements and the total rides the ID
// query. That is strictly fewer round trips on every shape measured here, so
// this test pins both halves: the new read is under its ceiling, and it is
// cheaper than the legacy composition it replaced (which is measured live, not
// written down, so a change to either path shows up as a changed delta).
func TestReadyTextRouteStatementBudget(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	cases := []struct {
		name             string
		world            readyTotalWorld
		includeEphemeral bool
		limit            int
		want             int
	}{
		// A full page: the legacy route paid page + a second count transaction.
		{"issues_only/limit1", readyTotalWorld{}, false, 1, 3},
		{"wisps/include_ephemeral/limit1", readyTotalWorld{wisps: true}, true, 1, 5},
		{"wisps_deferred_parent/include_ephemeral/limit1", readyTotalWorld{wisps: true, deferredParent: true}, true, 1, 6},
		// The default limit over a small front: no count on either route, and
		// still fewer statements, because the counts read hydrates in bulk.
		{"issues_only/limit100", readyTotalWorld{}, false, 100, 3},
		{"wisps/include_ephemeral/limit100", readyTotalWorld{wisps: true}, true, 100, 5},
		// --limit 0 (unbounded): there is no count on either route, and the
		// page is its own total. The single pass hydrates dependency, dependent
		// and comment COUNTS for every row where the legacy read hydrated
		// labels and dependency state — measured here rather than assumed.
		{"issues_only/limit0", readyTotalWorld{}, false, 0, 2},
		{"wisps/include_ephemeral/limit0", readyTotalWorld{wisps: true}, true, 0, 3},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix := fmt.Sprintf("rtb%d", i)
			te := newTestEnv(t, prefix)
			seedReadyTotalWorld(t, te, prefix, tc.world)
			filter := types.WorkFilter{IncludeEphemeral: tc.includeEphemeral, Limit: tc.limit}

			var page []*types.Issue
			legacy := withCountingReadTx(t, te, func(tx issueops.DBTX) error {
				var err error
				page, err = issueops.GetReadyWorkInTx(t.Context(), tx, filter)
				return err
			})
			legacyTxs := 1
			if len(page) == tc.limit {
				unbounded := filter
				unbounded.Limit = 0
				legacy = append(legacy, withCountingReadTx(t, te, func(tx issueops.DBTX) error {
					_, err := issueops.CountReadyWorkInTx(t.Context(), tx, unbounded)
					return err
				})...)
				legacyTxs = 2
			}

			var total int
			stmts := withCountingReadTx(t, te, func(tx issueops.DBTX) error {
				var err error
				_, total, err = issueops.GetReadyWorkWithCountsAndTotalInTx(t.Context(), tx, filter)
				return err
			})
			if total == 0 {
				t.Fatalf("total = 0; the seeded world has ready work")
			}
			t.Logf("text route: %d statements in %d transaction(s) before, %d in 1 after (delta %d)",
				len(legacy), legacyTxs, len(stmts), len(stmts)-len(legacy))
			if len(stmts) > tc.want {
				t.Errorf("text listing issued %d statements, budget %d:\n  %s", len(stmts), tc.want, strings.Join(stmts, "\n  "))
			}
			if len(stmts) >= len(legacy) {
				t.Errorf("text listing issued %d statements, the legacy page+count issued %d; the single pass must be cheaper", len(stmts), len(legacy))
			}
		})
	}
}
