package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateExplicitChildIDAdvancesTheParentCounter pins GH#4750 on the
// unit-of-work backend: an explicitly named child (P.8) created through
// domain/db's Insert raises its parent's child counter to at least the child's
// suffix, in the PARENT's plane, through both repository insert branches (the
// strict CreateOnly insert and the upsert). Without it the counter stays
// absent, and once P.8 is removed the next generated child of P re-mints a
// suffix P.8's history already used.
func TestCreateExplicitChildIDAdvancesTheParentCounter(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "cxc")
	kit := newUOWRoleFixtureKit(provider, "cxc")

	create := func(t *testing.T, issue *types.Issue, wisp, createOnly bool) {
		t.Helper()
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			params := domain.CreateIssueParams{Issue: issue, ExplicitID: issue.ID, CreateOnly: createOnly}
			var err error
			if wisp {
				_, err = uw.IssueUseCase().CreateWisp(ctx, params, "tester")
			} else {
				_, err = uw.IssueUseCase().CreateIssue(ctx, params, "tester")
			}
			return "create " + issue.ID, err
		}); err != nil {
			t.Fatalf("create %s (wisp=%v, createOnly=%v): %v", issue.ID, wisp, createOnly, err)
		}
	}
	issue := func(id string, wisp bool) *types.Issue {
		return &types.Issue{ID: id, Title: "explicit child " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp}
	}
	counter := func(t *testing.T, table, parent string) int {
		t.Helper()
		var n int
		if err := kit.QueryScalar(ctx, "SELECT COALESCE(MAX(last_child), 0) FROM "+table+" WHERE parent_id = ?", []any{parent}, &n); err != nil {
			t.Fatalf("read %s for %s: %v", table, parent, err)
		}
		return n
	}

	cases := []struct {
		name                     string
		parentWisp, childWisp    bool
		createOnly               bool
		counterTable, otherTable string
	}{
		{"DurableParentCreateOnly", false, false, true, "child_counters", "wisp_child_counters"},
		{"DurableParentUpsert", false, false, false, "child_counters", "wisp_child_counters"},
		{"WispParentCreateOnly", true, true, true, "wisp_child_counters", "child_counters"},
		{"WispParentUpsert", true, true, false, "wisp_child_counters", "child_counters"},
		{"WispChildOfADurableParent", false, true, true, "child_counters", "wisp_child_counters"},
		{"DurableChildOfAWispParent", true, false, false, "wisp_child_counters", "child_counters"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := "cxc-p" + string(rune('a'+i))
			create(t, issue(parent, tc.parentWisp), tc.parentWisp, true)
			if got := counter(t, tc.counterTable, parent); got != 0 {
				t.Fatalf("%s[%s] = %d before the child exists, want no counter", tc.counterTable, parent, got)
			}
			child := parent + ".8"
			create(t, issue(child, tc.childWisp), tc.childWisp, tc.createOnly)
			if got := counter(t, tc.counterTable, parent); got < 8 {
				t.Fatalf("%s[%s] = %d after creating %s, want >= 8", tc.counterTable, parent, got, child)
			}
			if got := counter(t, tc.otherTable, parent); got != 0 {
				t.Fatalf("%s[%s] = %d: the counter must be written in the parent's plane only", tc.otherTable, parent, got)
			}
			if tc.counterTable == "child_counters" {
				// Published with the create's own Dolt commit, not left in the
				// working set.
				var committed int
				if err := kit.QueryScalar(ctx, "SELECT COALESCE(MAX(last_child), 0) FROM child_counters AS OF 'HEAD' WHERE parent_id = ?", []any{parent}, &committed); err != nil {
					t.Fatalf("read committed child_counters for %s: %v", parent, err)
				}
				if committed < 8 {
					t.Fatalf("child_counters[%s] AS OF HEAD = %d, want >= 8", parent, committed)
				}
			}
		})
	}
}
