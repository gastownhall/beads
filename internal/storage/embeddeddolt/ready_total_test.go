//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// countingTx counts the statements a read issues. Against a remote SQL server
// every statement is one sequential round trip, so this number — not rows
// scanned — is what `bd ready` latency is made of.
type countingTx struct {
	tx    *sql.Tx
	stmts []string
}

func (c *countingTx) record(q string) {
	c.stmts = append(c.stmts, strings.Join(strings.Fields(q), " "))
}

func (c *countingTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	c.record(q)
	return c.tx.ExecContext(ctx, q, args...)
}

func (c *countingTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.record(q)
	return c.tx.QueryContext(ctx, q, args...)
}

func (c *countingTx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	c.record(q)
	return c.tx.QueryRowContext(ctx, q, args...)
}

// withCountingReadTx runs fn inside one read transaction on a raw connection
// to te's database and returns the statements fn issued.
func withCountingReadTx(t *testing.T, te *testEnv, fn func(tx issueops.DBTX) error) []string {
	t.Helper()
	ctx := t.Context()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	c := &countingTx{tx: tx}
	if err := fn(c); err != nil {
		t.Fatalf("read: %v", err)
	}
	return c.stmts
}

func readyIDs(items []*types.IssueWithCounts) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.Issue.ID)
	}
	return ids
}

type readyTotalWorld struct {
	wisps          bool
	deferredParent bool
	collision      bool
}

// seedReadyTotalWorld builds a ready front with every shape the total has to
// get right: plain ready issues, a blocked issue (its blocker stays ready), a
// future-deferred parent whose children are hidden, ready wisps, and an ID
// present in both tables (the overlap the merge dedupes wisp-wins).
func seedReadyTotalWorld(t *testing.T, te *testEnv, prefix string, w readyTotalWorld) {
	t.Helper()
	ctx := t.Context()
	create := func(iss *types.Issue) {
		t.Helper()
		if iss.Status == "" {
			iss.Status = types.StatusOpen
		}
		if iss.IssueType == "" {
			iss.IssueType = types.TypeTask
		}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}
	dep := func(from, to string, typ types.DependencyType) {
		t.Helper()
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: from, DependsOnID: to, Type: typ}, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", from, to, err)
		}
	}
	for i := 0; i < 5; i++ {
		create(&types.Issue{ID: fmt.Sprintf("%s-r%d", prefix, i), Title: fmt.Sprintf("ready %d", i), Priority: i % 3})
	}
	create(&types.Issue{ID: prefix + "-blocker", Title: "blocker", Priority: 1})
	create(&types.Issue{ID: prefix + "-blocked", Title: "blocked", Priority: 1})
	dep(prefix+"-blocked", prefix+"-blocker", types.DepBlocks)

	if w.deferredParent {
		future := time.Now().UTC().Add(72 * time.Hour)
		create(&types.Issue{ID: prefix + "-dparent", Title: "deferred parent", IssueType: types.TypeEpic, DeferUntil: &future})
		create(&types.Issue{ID: prefix + "-dchild", Title: "child of deferred", Priority: 0})
		dep(prefix+"-dchild", prefix+"-dparent", types.DepParentChild)
	}
	if w.wisps {
		for i := 0; i < 3; i++ {
			create(&types.Issue{ID: fmt.Sprintf("%s-w%d", prefix, i), Title: fmt.Sprintf("wisp %d", i), Priority: 1, Ephemeral: true})
		}
		if w.deferredParent {
			create(&types.Issue{ID: prefix + "-wdchild", Title: "wisp child of deferred", Priority: 0, Ephemeral: true})
			dep(prefix+"-wdchild", prefix+"-dparent", types.DepParentChild)
		}
	}
	if w.collision {
		// GH#4455 made creates refuse this, so it is written the way a store
		// corrupted before that fix holds it: the same row in both tables.
		te.exec(t, ctx, "INSERT INTO wisps (id, title, status, priority, issue_type, ephemeral, created_at, updated_at) "+
			"SELECT id, title, status, priority, issue_type, 1, created_at, updated_at FROM issues WHERE id = ?", prefix+"-r0")
	}
}

// TestReadyWorkPageTotal pins the in-band total GetReadyWorkWithCountsAndTotal
// hands `bd ready`: for every page size it must equal both the ReadyCounter
// identity (CountReadyWorkInTx) and the length of the unbounded page, and the
// page itself must be the unbounded page's prefix — the counting pass it
// replaced proved nothing more.
func TestReadyWorkPageTotal(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	// The expected ready sets are written out by hand rather than derived
	// from another reader in this package: GetReadyWorkWithCountsInTx,
	// CountReadyWorkInTx and the method under test all share
	// probeReadyWorkInTx, so agreement among them alone could not catch a
	// probe that hid (or leaked) a row in all three at once. Keyed by
	// IncludeEphemeral; suffixes are appended to the world's prefix.
	plain := []string{"blocker", "r0", "r1", "r2", "r3", "r4"}
	withWisps := []string{"blocker", "r0", "r1", "r2", "r3", "r4", "w0", "w1", "w2"}
	worlds := []struct {
		name  string
		world readyTotalWorld
		want  map[bool][]string
	}{
		{"issues_only", readyTotalWorld{}, map[bool][]string{false: plain, true: plain}},
		// dparent is future-deferred, so it and its child are hidden.
		{"issues_deferred_parent", readyTotalWorld{deferredParent: true}, map[bool][]string{false: plain, true: plain}},
		{"wisps", readyTotalWorld{wisps: true}, map[bool][]string{false: plain, true: withWisps}},
		// The wisp child of the deferred parent is hidden too.
		{"wisps_deferred_parent", readyTotalWorld{wisps: true, deferredParent: true}, map[bool][]string{false: plain, true: withWisps}},
		// r0 sits in both tables; it is listed (and counted) exactly once.
		{"wisps_collision", readyTotalWorld{wisps: true, collision: true}, map[bool][]string{false: plain, true: withWisps}},
	}
	for i, wc := range worlds {
		t.Run(wc.name, func(t *testing.T) {
			prefix := fmt.Sprintf("rt%d", i)
			te := newTestEnv(t, prefix)
			seedReadyTotalWorld(t, te, prefix, wc.world)

			for _, includeEphemeral := range []bool{false, true} {
				base := types.WorkFilter{IncludeEphemeral: includeEphemeral}
				var unbounded []*types.IssueWithCounts
				var counted int
				withCountingReadTx(t, te, func(tx issueops.DBTX) error {
					var err error
					if unbounded, err = issueops.GetReadyWorkWithCountsInTx(t.Context(), tx, base); err != nil {
						return err
					}
					counted, err = issueops.CountReadyWorkInTx(t.Context(), tx, base)
					return err
				})
				if counted != len(unbounded) {
					t.Fatalf("ephemeral=%v: CountReadyWorkInTx = %d, unbounded page has %d rows %v",
						includeEphemeral, counted, len(unbounded), readyIDs(unbounded))
				}
				all := readyIDs(unbounded)
				if got, want := sortedIDs(all), prefixed(prefix, wc.want[includeEphemeral]); strings.Join(got, ",") != strings.Join(want, ",") {
					t.Fatalf("ephemeral=%v: ready set = %v, want %v", includeEphemeral, got, want)
				}
				if len(all) < 3 {
					t.Fatalf("ephemeral=%v: seeded world too small to page: %v", includeEphemeral, all)
				}
				sawWisp := false
				for _, id := range all {
					if strings.Contains(id, "-w") {
						sawWisp = true
					}
					if strings.HasSuffix(id, "-blocked") || strings.Contains(id, "dchild") {
						t.Fatalf("ephemeral=%v: %s is not ready but was listed: %v", includeEphemeral, id, all)
					}
				}

				// Identity alone cannot see a wisp family that was never read:
				// the page and the count would agree on leaving it out.
				if wantWisp := wc.world.wisps && includeEphemeral; sawWisp != wantWisp {
					t.Fatalf("ephemeral=%v: wisps listed = %v, want %v: %v", includeEphemeral, sawWisp, wantWisp, all)
				}

				for _, limit := range []int{0, 1, 2, len(all) - 1, len(all), len(all) + 3, len(all) + 5} {
					filter := base
					filter.Limit = limit
					var page []*types.IssueWithCounts
					var total int
					withCountingReadTx(t, te, func(tx issueops.DBTX) error {
						var err error
						page, total, err = issueops.GetReadyWorkWithCountsAndTotalInTx(t.Context(), tx, filter)
						return err
					})
					if total != len(all) {
						t.Errorf("ephemeral=%v limit=%d: total = %d, want %d", includeEphemeral, limit, total, len(all))
					}
					want := all
					if limit > 0 && limit < len(all) {
						want = all[:limit]
					}
					if got := readyIDs(page); strings.Join(got, ",") != strings.Join(want, ",") {
						t.Errorf("ephemeral=%v limit=%d: page = %v, want %v", includeEphemeral, limit, got, want)
					}
				}
			}
		})
	}
}

// TestReadyWorkStatementBudget is the round-trip regression guard. `bd ready
// --json --limit 1` against a remote store with ~55 ms per round trip took
// 5.4 s — slower than --limit 0 — because a capped page re-ran every probe per
// table family and then sized the set in a second pass. These budgets are the
// statements ONE read transaction issues for the page AND its total; the
// separate count transaction is gone entirely.
func TestReadyWorkStatementBudget(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	cases := []struct {
		name  string
		world readyTotalWorld
		limit int
		want  int
	}{
		// probe + mega-query
		{"issues_only/limit0", readyTotalWorld{}, 0, 2},
		// probe + (ID page with window total + counts hydration)
		{"issues_only/limit1", readyTotalWorld{}, 1, 3},
		// probe + mega-query per family
		{"wisps/limit0", readyTotalWorld{wisps: true}, 0, 3},
		// probe + (ID page + hydration) per family
		{"wisps/limit1", readyTotalWorld{wisps: true}, 1, 5},
		// + one statement for every deferred-parent child leg
		{"wisps_deferred_parent/limit0", readyTotalWorld{wisps: true, deferredParent: true}, 0, 4},
		{"wisps_deferred_parent/limit1", readyTotalWorld{wisps: true, deferredParent: true}, 1, 6},
		// + the overlap count, only because an ID sits in both tables
		{"wisps_collision/limit1", readyTotalWorld{wisps: true, collision: true}, 1, 6},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix := fmt.Sprintf("rb%d", i)
			te := newTestEnv(t, prefix)
			seedReadyTotalWorld(t, te, prefix, tc.world)

			filter := types.WorkFilter{IncludeEphemeral: true, Limit: tc.limit}
			var total int
			stmts := withCountingReadTx(t, te, func(tx issueops.DBTX) error {
				var err error
				_, total, err = issueops.GetReadyWorkWithCountsAndTotalInTx(t.Context(), tx, filter)
				return err
			})
			if total == 0 {
				t.Fatalf("total = 0; the seeded world has ready work")
			}
			// A ceiling, not an exact pin: a change that saves a statement
			// should not have to edit this table, but one that adds a round
			// trip fails. The budgets are today's counts, so there is no slack.
			if len(stmts) > tc.want {
				t.Errorf("page+total issued %d statements, budget %d (+%d):\n  %s",
					len(stmts), tc.want, len(stmts)-tc.want, strings.Join(stmts, "\n  "))
			} else if len(stmts) < tc.want {
				t.Logf("page+total issued %d statements, under budget %d by %d; consider tightening:\n  %s",
					len(stmts), tc.want, tc.want-len(stmts), strings.Join(stmts, "\n  "))
			}
		})
	}
}

func sortedIDs(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func prefixed(prefix string, suffixes []string) []string {
	out := make([]string, 0, len(suffixes))
	for _, s := range suffixes {
		out = append(out, prefix+"-"+s)
	}
	slices.Sort(out)
	return out
}

// TestReadyWorkPageTotalFilterMatrix runs the in-band total across the
// WorkFilter fields `bd ready` forwards. Each filter's ready set is written
// out by hand (the oracle); the total must equal the unbounded listing's
// length, and every capped page must be that listing's prefix, so a filter
// the window total ignored — or applied to one family only — fails here.
func TestReadyWorkPageTotalFilterMatrix(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	const prefix = "rm"
	te := newTestEnv(t, prefix)
	seedReadyTotalWorld(t, te, prefix, readyTotalWorld{wisps: true, deferredParent: true})
	ctx := t.Context()
	id := func(s string) string { return prefix + "-" + s }
	for issue, labels := range map[string][]string{"r0": {"a", "b"}, "r1": {"a"}, "blocker": {"a"}, "w0": {"b"}} {
		for _, l := range labels {
			if err := te.store.AddLabel(ctx, id(issue), l, "tester"); err != nil {
				t.Fatalf("AddLabel %s %s: %v", issue, l, err)
			}
		}
	}
	for _, issue := range []string{"r2", "w1"} {
		if err := te.store.UpdateIssue(ctx, id(issue), map[string]interface{}{"assignee": "alice"}, "tester"); err != nil {
			t.Fatalf("assign %s: %v", issue, err)
		}
	}

	all := []string{"blocker", "r0", "r1", "r2", "r3", "r4", "w0", "w1", "w2"}
	alice, zero := "alice", 0
	parent := id("dparent")
	cases := []struct {
		name   string
		filter types.WorkFilter
		want   []string
	}{
		{"hybrid", types.WorkFilter{}, all},
		{"oldest", types.WorkFilter{SortPolicy: types.SortPolicyOldest}, all},
		{"priority", types.WorkFilter{SortPolicy: types.SortPolicyPriority}, all},
		{"labels_all", types.WorkFilter{Labels: []string{"a", "b"}}, []string{"r0"}},
		{"labels_any", types.WorkFilter{LabelsAny: []string{"a", "b"}}, []string{"blocker", "r0", "r1", "w0"}},
		{"assignee", types.WorkFilter{Assignee: &alice}, []string{"r2", "w1"}},
		{"unassigned", types.WorkFilter{Unassigned: true}, []string{"blocker", "r0", "r1", "r3", "r4", "w0", "w2"}},
		{"priority_0", types.WorkFilter{Priority: &zero}, []string{"r0", "r3"}},
		{"exclude_ids", types.WorkFilter{ExcludeIDs: []string{id("r1"), id("w2")}}, []string{"blocker", "r0", "r2", "r3", "r4", "w0", "w1"}},
		{"include_deferred", types.WorkFilter{IncludeDeferred: true}, append(slices.Clone(all), "dchild", "dparent", "wdchild")},
		{"parent_deferred", types.WorkFilter{ParentID: &parent, IncludeDeferred: true}, []string{"dchild", "wdchild"}},
		{"max_rows", types.WorkFilter{MaxRows: 50}, all},
		{"type_epic", types.WorkFilter{Type: string(types.TypeEpic), IncludeDeferred: true}, []string{"dparent"}},
		{"type_task_oldest", types.WorkFilter{Type: string(types.TypeTask), SortPolicy: types.SortPolicyOldest}, all},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := tc.filter
			base.IncludeEphemeral = true
			var unbounded []*types.IssueWithCounts
			withCountingReadTx(t, te, func(tx issueops.DBTX) error {
				var err error
				unbounded, err = issueops.GetReadyWorkWithCountsInTx(t.Context(), tx, base)
				return err
			})
			listed := readyIDs(unbounded)
			if got, want := sortedIDs(listed), prefixed(prefix, tc.want); strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("ready set = %v, want %v", got, want)
			}
			n := len(listed)
			for _, limit := range []int{1, 2, n, n + 3} {
				filter := base
				filter.Limit = limit
				var page []*types.IssueWithCounts
				var total int
				withCountingReadTx(t, te, func(tx issueops.DBTX) error {
					var err error
					page, total, err = issueops.GetReadyWorkWithCountsAndTotalInTx(t.Context(), tx, filter)
					return err
				})
				if total != n {
					t.Errorf("limit=%d: total = %d, want %d", limit, total, n)
				}
				want := listed[:min(limit, n)]
				if got := readyIDs(page); strings.Join(got, ",") != strings.Join(want, ",") {
					t.Errorf("limit=%d: page = %v, want prefix %v", limit, got, want)
				}
			}
		})
	}
}
