package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// tryQuery runs a SQL query against the given *sql.DB, recovering from a panic.
// Returns (panicMsg, execErr). If panicMsg is non-empty the query triggered a panic.
func tryQuery(db *sql.DB, query string, args ...interface{}) (panicMsg string, execErr error) {
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicMsg = fmt.Sprintf("%v", r)
			}
		}()
		rows, err := db.QueryContext(context.Background(), query, args...)
		if err != nil {
			execErr = err
			return
		}
		// Drain rows to trigger any deferred panic in the iterator.
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
		}
		_ = rows.Close()
	}()
	return panicMsg, execErr
}

// TestJoinIterPanic_Spike validates which JOIN/EXISTS shapes trigger Dolt's
// joinIter panic ("slice bounds out of range at join_iters.go:192") on the
// current pinned go-mysql-server version.
//
// Each sub-test tries a query shape. If the panic fires the sub-test is marked
// t.Error (the bug is still present). If no panic fires the sub-test is skipped
// with a note that the workaround can be considered for removal.
//
// Reference: bd-wsgws (joinIter spike). Workarounds are documented throughout
// internal/storage/dolt/queries.go and internal/storage/issueops/.
func TestJoinIterPanic_Spike(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Seed minimal data: two issues, one blocking the other.
	parent := &types.Issue{ID: "test-jip-1", Title: "blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	child := &types.Issue{ID: "test-jip-2", Title: "blocked", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	for _, issue := range []*types.Issue{parent, child} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create issue %s: %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: parent.ID,
		Type:        types.DepBlocks,
	}, "tester"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	db := store.DB()

	// shape1: simple inner JOIN between issues and dependencies.
	// The workaround in GetMoleculeProgress avoids this exact shape.
	t.Run("inner_join_issues_deps", func(t *testing.T) {
		q := `SELECT i.id FROM issues i JOIN dependencies d ON d.issue_id = i.id WHERE d.type = 'blocks'`
		if panicMsg, err := tryQuery(db, q); panicMsg != "" {
			t.Errorf("joinIter panic still fires on JOIN issues+dependencies: %s", panicMsg)
		} else if err != nil {
			t.Skipf("query error (not a panic): %v", err)
		} else {
			t.Skip("joinIter panic no longer fires for inner JOIN — workaround in GetMoleculeProgress may be liftable")
		}
	})

	// shape2: correlated EXISTS subquery referencing outer table.
	// Pattern that ComputeBlockedIDsInTx was originally written to avoid.
	t.Run("correlated_exists_subquery", func(t *testing.T) {
		q := `
			SELECT i.id FROM issues i
			WHERE i.status NOT IN ('closed', 'pinned')
			AND EXISTS (
				SELECT 1 FROM dependencies d
				WHERE d.issue_id = i.id
				AND d.type IN ('blocks', 'waits-for')
			)`
		if panicMsg, err := tryQuery(db, q); panicMsg != "" {
			t.Errorf("joinIter panic still fires on correlated EXISTS: %s", panicMsg)
		} else if err != nil {
			t.Skipf("query error (not a panic): %v", err)
		} else {
			t.Skip("joinIter panic no longer fires for correlated EXISTS — ComputeBlockedIDsInTx may be liftable")
		}
	})

	// shape3: double correlated subquery (blocker must itself be active).
	// This is the full 'is this issue blocked by an active issue?' check.
	t.Run("double_correlated_exists", func(t *testing.T) {
		q := `
			SELECT i.id FROM issues i
			WHERE i.status NOT IN ('closed', 'pinned')
			AND EXISTS (
				SELECT 1 FROM dependencies d
				WHERE d.issue_id = i.id AND d.type = 'blocks'
				AND EXISTS (
					SELECT 1 FROM issues blocker
					WHERE blocker.id = d.depends_on_id
					AND blocker.status NOT IN ('closed', 'pinned')
				)
			)`
		if panicMsg, err := tryQuery(db, q); panicMsg != "" {
			t.Errorf("joinIter panic still fires on double-correlated EXISTS: %s", panicMsg)
		} else if err != nil {
			t.Skipf("query error (not a panic): %v", err)
		} else {
			t.Skip("joinIter panic no longer fires for double-correlated EXISTS — full blocked query SQL pushdown may be viable")
		}
	})

	// shape4: non-correlated IN subquery (used safely in ready_work.go).
	// Baseline sanity check — this form is already used in production.
	t.Run("non_correlated_in_subquery", func(t *testing.T) {
		q := `SELECT id FROM issues WHERE id IN (SELECT issue_id FROM dependencies WHERE type = 'blocks')`
		if panicMsg, err := tryQuery(db, q); panicMsg != "" {
			t.Errorf("unexpected panic on non-correlated IN subquery: %s (regression!)", panicMsg)
		} else if err != nil {
			t.Errorf("non-correlated IN subquery errored: %v", err)
		}
		// This shape is expected to work fine — no Skip.
	})

	// shape5: mergeJoinIter variant — type+status+priority multi-column filter with
	// an IN subquery. Workaround in ready_work.go uses isolated subquery per filter.
	t.Run("multi_column_filter_with_type_subquery", func(t *testing.T) {
		q := `
			SELECT id FROM issues
			WHERE status = 'open'
			AND priority <= 2
			AND issue_type IN (SELECT issue_type FROM issues WHERE issue_type = 'task')`
		if panicMsg, err := tryQuery(db, q); panicMsg != "" {
			t.Errorf("mergeJoinIter panic still fires on type+status+priority: %s", panicMsg)
		} else if err != nil {
			t.Skipf("query error (not a panic): %v", err)
		} else {
			t.Skip("mergeJoinIter panic no longer fires — type isolation workaround in ready_work.go may be liftable")
		}
	})
}

// joinIterShapesSummary is a compile-time documentation string captured here so
// greps for bd-wsgws surface it. It lists the query shapes tested and whether
// they currently panic.
var joinIterShapesSummary = strings.Join([]string{
	"bd-wsgws joinIter spike: shapes tested against go-mysql-server v0.20.1-0.20260507202550-43d6daf5958b",
	"shape1 inner_join_issues_deps: JOIN issues+dependencies",
	"shape2 correlated_exists_subquery: EXISTS referencing outer table",
	"shape3 double_correlated_exists: nested EXISTS",
	"shape4 non_correlated_in_subquery: IN (SELECT ...) — baseline",
	"shape5 multi_column_filter_with_type_subquery: mergeJoinIter type+status+priority",
}, "\n")
