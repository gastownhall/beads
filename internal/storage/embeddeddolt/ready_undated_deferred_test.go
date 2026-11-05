//go:build cgo

package embeddeddolt_test

import (
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// seedUndatedDeferredParent puts an epic, a child hanging off it by a
// parent-child edge, and one unrelated free issue into the store, then defers
// the epic the way an undated `bd defer` (or `bd update --status deferred`)
// does: status only, defer_until left NULL. No row in the database carries a
// defer_until, so only the undated status can make the epic a deferred parent.
func seedUndatedDeferredParent(t *testing.T, te *testEnv, prefix string) {
	t.Helper()
	ctx := t.Context()
	for _, iss := range []*types.Issue{
		{ID: prefix + "-parent", Title: "undated deferred epic", IssueType: types.TypeEpic},
		{ID: prefix + "-child", Title: "child of undated deferred epic"},
		{ID: prefix + "-free", Title: "unrelated"},
	} {
		iss.Status = types.StatusOpen
		iss.Priority = 2
		if iss.IssueType == "" {
			iss.IssueType = types.TypeTask
		}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{
		IssueID: prefix + "-child", DependsOnID: prefix + "-parent", Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	deferUndated(t, te, prefix+"-parent")
}

// deferUndated writes what an undated `bd defer` writes and checks that
// defer_until stayed NULL, so a fixture can never pass on a dated defer.
func deferUndated(t *testing.T, te *testEnv, id string) {
	t.Helper()
	ctx := t.Context()
	if err := te.store.UpdateIssue(ctx, id, map[string]interface{}{
		"status": string(types.StatusDeferred),
	}, "tester"); err != nil {
		t.Fatalf("defer %s: %v", id, err)
	}
	got, err := te.store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue %s: %v", id, err)
	}
	if got.Status != types.StatusDeferred || got.DeferUntil != nil {
		t.Fatalf("%s: status=%s defer_until=%v, want deferred with no defer_until", id, got.Status, got.DeferUntil)
	}
}

// readyIDsByStack reads the ready IDs for filter through each storage stack:
// the classic store and the domain/db repository over an embedded connection.
func readyIDsByStack(t *testing.T, te *testEnv, filter types.WorkFilter) map[string][]string {
	t.Helper()
	ctx := t.Context()
	classic, err := te.store.GetReadyWork(ctx, filter)
	if err != nil {
		t.Fatalf("classic GetReadyWork: %v", err)
	}
	raw, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	page, err := db.NewIssueSQLRepository(raw).GetReadyWork(ctx, filter)
	if err != nil {
		t.Fatalf("repository GetReadyWork: %v", err)
	}
	return map[string][]string{
		"classic":    issueIDsOf(classic),
		"repository": issueIDsOf(page.Items),
	}
}

func issueIDsOf(issues []*types.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, iss := range issues {
		ids = append(ids, iss.ID)
	}
	return ids
}

// TestReadyWorkHidesChildrenOfUndatedDeferredParent pins GH#7300: a parent put
// in the indefinite icebox (status 'deferred', no defer_until) hides its direct
// children from ready work, as a parent deferred to a future date does.
//
// The test name and the shape of its first fixture come from @drakeo338's
// PR #7304.
func TestReadyWorkHidesChildrenOfUndatedDeferredParent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("classic", func(t *testing.T) {
		te := newTestEnv(t, "udc")
		seedUndatedDeferredParent(t, te, "udc")

		issues, err := te.store.GetReadyWork(t.Context(), types.WorkFilter{})
		if err != nil {
			t.Fatalf("GetReadyWork: %v", err)
		}
		if got, want := issueIDsOf(issues), []string{"udc-free"}; !slices.Equal(got, want) {
			t.Fatalf("GetReadyWork = %v, want %v (the undated deferred epic and its child must both be withheld)", got, want)
		}
	})

	t.Run("repository", func(t *testing.T) {
		te := newTestEnv(t, "udr")
		seedUndatedDeferredParent(t, te, "udr")

		ids, err := readyWorkIDs(t, te)
		if err != nil {
			t.Fatalf("repository GetReadyWork: %v", err)
		}
		if want := []string{"udr-free"}; !slices.Equal(ids, want) {
			t.Fatalf("repository GetReadyWork = %v, want %v (the undated deferred epic's child must be withheld)", ids, want)
		}
	})

	// The counts readers and the in-band total resolve the deferred parents in
	// their own one-statement probe, not through the classic gate.
	t.Run("counts_and_total", func(t *testing.T) {
		te := newTestEnv(t, "udt")
		seedUndatedDeferredParent(t, te, "udt")
		ctx := t.Context()
		create := func(id string, ephemeral bool) {
			t.Helper()
			if err := te.store.CreateIssue(ctx, &types.Issue{
				ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral,
			}, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", id, err)
			}
		}
		dep := func(child, parent string) {
			t.Helper()
			if err := te.store.AddDependency(ctx, &types.Dependency{
				IssueID: child, DependsOnID: parent, Type: types.DepParentChild,
			}, "tester"); err != nil {
				t.Fatalf("AddDependency %s -> %s: %v", child, parent, err)
			}
		}
		// An ephemeral child of the durable parent, and an undated deferred
		// wisp parent with an ephemeral child of its own: both dependency
		// planes and both parent tables.
		create("udt-wchild", true)
		dep("udt-wchild", "udt-parent")
		create("udt-wparent", true)
		create("udt-wpchild", true)
		dep("udt-wpchild", "udt-wparent")
		create("udt-wfree", true)
		deferUndated(t, te, "udt-wparent")

		filter := types.WorkFilter{IncludeEphemeral: true}
		want := []string{"udt-free", "udt-wfree"}

		listed, total, err := te.store.GetReadyWorkWithCountsAndTotal(ctx, filter)
		if err != nil {
			t.Fatalf("GetReadyWorkWithCountsAndTotal: %v", err)
		}
		if got := sortedIDs(readyIDs(listed)); !slices.Equal(got, want) || total != len(want) {
			t.Fatalf("GetReadyWorkWithCountsAndTotal = %v (total %d), want %v (total %d)", got, total, want, len(want))
		}
		n, err := te.store.CountReadyWork(ctx, filter)
		if err != nil {
			t.Fatalf("CountReadyWork: %v", err)
		}
		if n != len(want) {
			t.Fatalf("CountReadyWork = %d, want %d", n, len(want))
		}
	})

	// IncludeDeferred lifts the parent exclusion on both stacks. The default
	// read of the same database is the control: it proves the child was being
	// withheld, so the bypass is what lists it.
	t.Run("include_deferred", func(t *testing.T) {
		te := newTestEnv(t, "udi")
		seedUndatedDeferredParent(t, te, "udi")

		for stack, ids := range readyIDsByStack(t, te, types.WorkFilter{}) {
			if slices.Contains(ids, "udi-child") {
				t.Errorf("%s: default ready = %v, want the child withheld", stack, ids)
			}
		}
		for stack, ids := range readyIDsByStack(t, te, types.WorkFilter{IncludeDeferred: true}) {
			if !slices.Contains(ids, "udi-child") || !slices.Contains(ids, "udi-free") {
				t.Errorf("%s: IncludeDeferred ready = %v, want it to list udi-child and udi-free", stack, ids)
			}
		}
	})
}

// TestReadyWorkDeferredParentShapes pins which parents hide their direct
// children from ready work: a future defer_until (any status) or an undated
// 'deferred' status. An expired date does not, even while the status still says
// deferred (the wake sweep owns that transition, and a failed wake is
// advisory), and only parent-child edges carry the hold.
//
// Each shape gets its own database, so one shape's parent can never be what
// opens the deferred-parent gate for another. The reads go straight to
// issueops in one read transaction, so the store's wake sweep cannot reshape
// an expired-date fixture first.
func TestReadyWorkDeferredParentShapes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	const (
		future  = "DATE_ADD(UTC_TIMESTAMP(), INTERVAL 3 DAY)"
		expired = "DATE_SUB(UTC_TIMESTAMP(), INTERVAL 3 DAY)"
	)
	cases := []struct {
		name       string
		status     types.Status
		deferUntil string // SQL expression; "" leaves defer_until NULL
		edge       types.DependencyType
		childReady bool
	}{
		{"undated_deferred", types.StatusDeferred, "", types.DepParentChild, false},
		{"future_deferred", types.StatusDeferred, future, types.DepParentChild, false},
		{"future_open", types.StatusOpen, future, types.DepParentChild, false},
		{"expired_deferred", types.StatusDeferred, expired, types.DepParentChild, true},
		{"undated_open", types.StatusOpen, "", types.DepParentChild, true},
		{"undated_deferred_related_edge", types.StatusDeferred, "", types.DepRelated, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const prefix = "uds"
			te := newTestEnv(t, prefix)
			ctx := t.Context()
			for _, id := range []string{prefix + "-parent", prefix + "-child", prefix + "-free"} {
				if err := te.store.CreateIssue(ctx, &types.Issue{
					ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
				}, "tester"); err != nil {
					t.Fatalf("CreateIssue %s: %v", id, err)
				}
			}
			if err := te.store.AddDependency(ctx, &types.Dependency{
				IssueID: prefix + "-child", DependsOnID: prefix + "-parent", Type: tc.edge,
			}, "tester"); err != nil {
				t.Fatalf("AddDependency: %v", err)
			}
			deferUntil := "NULL"
			if tc.deferUntil != "" {
				deferUntil = tc.deferUntil
			}
			te.exec(t, ctx, "UPDATE issues SET status = ?, defer_until = "+deferUntil+" WHERE id = ?",
				string(tc.status), prefix+"-parent")

			var ready []*types.Issue
			withCountingReadTx(t, te, func(tx issueops.DBTX) error {
				var err error
				ready, err = issueops.GetReadyWorkInTx(ctx, tx, types.WorkFilter{})
				return err
			})
			ids := issueIDsOf(ready)
			if !slices.Contains(ids, prefix+"-free") {
				t.Fatalf("ready = %v, want it to list the unrelated %s-free", ids, prefix)
			}
			if got := slices.Contains(ids, prefix+"-child"); got != tc.childReady {
				t.Fatalf("child ready = %v, want %v; ready = %v", got, tc.childReady, ids)
			}
		})
	}
}

// TestClaimReadyIssueSkipsChildOfUndatedDeferredParent: claim reads the same
// ready predicate, so a claim must never pick up work under an iced epic.
func TestClaimReadyIssueSkipsChildOfUndatedDeferredParent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "udk")
	seedUndatedDeferredParent(t, te, "udk")
	ctx := t.Context()
	// Make the child the top of the queue, so only the deferred-parent
	// exclusion can stop a claim from taking it.
	if err := te.store.UpdateIssue(ctx, "udk-child", map[string]interface{}{"priority": 0}, "tester"); err != nil {
		t.Fatalf("raise child priority: %v", err)
	}

	for _, want := range []string{"udk-free", ""} {
		claimed, err := te.store.ClaimReadyIssue(ctx, types.WorkFilter{}, "agent")
		if err != nil {
			t.Fatalf("ClaimReadyIssue: %v", err)
		}
		got := ""
		if claimed != nil {
			got = claimed.ID
		}
		if got != want {
			t.Fatalf("ClaimReadyIssue claimed %q, want %q", got, want)
		}
	}
}
