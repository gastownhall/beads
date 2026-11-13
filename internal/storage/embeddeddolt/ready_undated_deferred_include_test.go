//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestIncludeDeferredListsUndatedChildButNotDeferredParent pins what
// `bd ready --include-deferred` does around GH#7300: the flag lifts the
// deferred-child exclusion — the child of a plain `bd defer` parent (status
// deferred, no defer_until) is listed — but not the ready status filter, so
// the deferred parent itself still stays off the list.
func TestIncludeDeferredListsUndatedChildButNotDeferredParent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	te := newTestEnv(t, "ui")

	for _, iss := range []*types.Issue{
		{ID: "ui-parent", Title: "undated deferred parent", Status: types.StatusDeferred, IssueType: types.TypeEpic},
		{ID: "ui-child", Title: "child of deferred", Status: types.StatusOpen, IssueType: types.TypeTask},
		{ID: "ui-free", Title: "free", Status: types.StatusOpen, IssueType: types.TypeTask},
	} {
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", iss.ID, err)
		}
	}
	if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: "ui-child", DependsOnID: "ui-parent", Type: types.DepParentChild}, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}

	listed := readyTxIDs(t, te, types.WorkFilter{IncludeDeferred: true})
	for _, want := range []string{"ui-child", "ui-free"} {
		if !strings.Contains(","+strings.Join(listed, ",")+",", ","+want+",") {
			t.Errorf("--include-deferred should list %s; ready set = %v", want, listed)
		}
	}
	if strings.Contains(strings.Join(listed, ","), "ui-parent") {
		t.Errorf("--include-deferred must not list the deferred-status parent; ready set = %v", listed)
	}
}

// TestReadyUndatedDeferredEdgeMatrix walks every (status, defer_until) shape
// a parent can hold and pins whether its child is ready. The deferred-parent
// rule is status='deferred' OR defer_until in the future (GH#7300), so the
// undated icebox and the stale dated deferral — status deferred, date
// already past — hide their children just like a future-dated one, while an
// open parent with a past or NULL date leaves the child listed.
//
// The read goes through a raw transaction rather than the store: the store's
// GetReadyWork wakes dated deferrals first, which would rewrite the stale
// row this matrix needs to observe in place.
func TestReadyUndatedDeferredEdgeMatrix(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	now := time.Now().UTC()
	past := now.Add(-48 * time.Hour)
	future := now.Add(48 * time.Hour)
	cases := []struct {
		name         string
		status       types.Status
		deferUntil   *time.Time
		childHidden  bool
		parentListed bool
	}{
		// Plain `bd defer`: the GH#7300 icebox.
		{"undated_status_deferred", types.StatusDeferred, nil, true, false},
		{"dated_status_open_future", types.StatusOpen, &future, true, false},
		// The wake sweep has not run yet: status still deferred, date past.
		{"stale_status_deferred_past_date", types.StatusDeferred, &past, true, false},
		{"status_deferred_future_date", types.StatusDeferred, &future, true, false},
		{"status_open_past_date", types.StatusOpen, &past, false, true},
		{"status_open_null_date", types.StatusOpen, nil, false, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix := fmt.Sprintf("ue%d", i)
			te := newTestEnv(t, prefix)
			ctx := t.Context()
			for _, iss := range []*types.Issue{
				{ID: prefix + "-parent", Title: "parent", Status: tc.status, IssueType: types.TypeTask, DeferUntil: tc.deferUntil},
				{ID: prefix + "-child", Title: "child", Status: types.StatusOpen, IssueType: types.TypeTask},
				{ID: prefix + "-free", Title: "free", Status: types.StatusOpen, IssueType: types.TypeTask},
			} {
				if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
					t.Fatalf("CreateIssue %s: %v", iss.ID, err)
				}
			}
			if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: prefix + "-child", DependsOnID: prefix + "-parent", Type: types.DepParentChild}, "tester"); err != nil {
				t.Fatalf("AddDependency: %v", err)
			}

			listed := readyTxIDs(t, te, types.WorkFilter{})
			joined := "," + strings.Join(listed, ",") + ","
			has := func(suffix string) bool { return strings.Contains(joined, ","+prefix+"-"+suffix+",") }
			if has("child") == tc.childHidden {
				t.Errorf("child listed = %v, want %v (parent status %q, defer_until %v); ready set = %v",
					has("child"), !tc.childHidden, tc.status, tc.deferUntil, listed)
			}
			if has("parent") != tc.parentListed {
				t.Errorf("parent listed = %v, want %v; ready set = %v", has("parent"), tc.parentListed, listed)
			}
			if !has("free") {
				t.Errorf("ready control %s-free missing; ready set = %v", prefix, listed)
			}
		})
	}
}

// readyTxIDs reads the ready set through a raw transaction, bypassing the
// store's wake sweep, and returns the listed IDs in listed order.
func readyTxIDs(t *testing.T, te *testEnv, filter types.WorkFilter) []string {
	t.Helper()
	var ready []*types.IssueWithCounts
	withCountingReadTx(t, te, func(tx issueops.DBTX) error {
		var err error
		ready, err = issueops.GetReadyWorkWithCountsInTx(t.Context(), tx, filter)
		return err
	})
	return readyIDs(ready)
}
