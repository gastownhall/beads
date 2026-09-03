//go:build cgo

package embeddeddolt_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestDescendantWalkReachesEveryTargetKindAndStopsOnCycles pins the semantics
// the reshaped descendant walk (sqlbuild.DescendantWalkQuery) has to preserve
// from the parent_edges form it replaces: a parent may be named through any of
// the three typed target columns (issue, wisp, external / cross-prefix) in
// either dependency table, only parent-child edges count, the root is never
// its own descendant even when an edge cycle leads back to it, and maxDepth
// is a runaway guard: a tree that reaches it is an error, not a truncated
// answer. The expected sets are written by hand from the seeded edges, so this
// is a reference walk rather than a comparison against the previous query
// text; it passes on the previous shape as well.
func TestDescendantWalkReachesEveryTargetKindAndStopsOnCycles(t *testing.T) {
	te := newTestEnv(t, "dw")
	ctx := context.Background()

	const (
		root       = "dw-root"
		childA     = "dw-a"
		childB     = "dw-b"
		grandchild = "dw-c"    // child of A
		wispW      = "dw-w"    // wisp, child of A (wisp_dependencies, issue target)
		wispW2     = "dw-w2"   // wisp, child of W (wisp_dependencies, wisp target)
		issueD     = "dw-d"    // issue, child of W (dependencies, wisp target)
		blockerX   = "dw-x"    // blocks-edge to root: not a descendant
		otherU     = "dw-u"    // unrelated tree
		otherV     = "dw-v"    // child of U
		crossRoot  = "zz-root" // cross-prefix parent: lands in depends_on_external
		crossF     = "dw-f"    // child of the cross-prefix parent
		crossG     = "dw-g"    // child of F
	)

	for _, id := range []string{root, childA, childB, grandchild, issueD, blockerX, otherU, otherV, crossF, crossG} {
		if err := te.store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, id := range []string{wispW, wispW2} {
		if err := te.store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}, "tester"); err != nil {
			t.Fatalf("create wisp %s: %v", id, err)
		}
	}

	// Edges are written directly so each lands in the exact table and typed
	// column the case is about; the API's own classification is covered by
	// the dependency tests. Rows are (child, parent).
	for _, e := range []struct {
		table, targetCol, child, parent, depType string
	}{
		{"dependencies", "depends_on_issue_id", childA, root, "parent-child"},
		{"dependencies", "depends_on_issue_id", childB, root, "parent-child"},
		{"dependencies", "depends_on_issue_id", grandchild, childA, "parent-child"},
		{"dependencies", "depends_on_issue_id", root, grandchild, "parent-child"}, // cycle back to the root
		{"wisp_dependencies", "depends_on_issue_id", wispW, childA, "parent-child"},
		{"wisp_dependencies", "depends_on_wisp_id", wispW2, wispW, "parent-child"},
		{"dependencies", "depends_on_wisp_id", issueD, wispW, "parent-child"},
		{"dependencies", "depends_on_issue_id", blockerX, root, "blocks"},
		{"dependencies", "depends_on_issue_id", otherV, otherU, "parent-child"},
		{"dependencies", "depends_on_external", crossF, crossRoot, "parent-child"},
		{"dependencies", "depends_on_issue_id", crossG, crossF, "parent-child"},
	} {
		te.exec(t, ctx, "INSERT INTO "+e.table+" (id, issue_id, "+e.targetCol+", type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'tester')",
			e.child, e.parent, e.depType)
	}

	walk := func(t *testing.T, rootID string, maxDepth int) ([]string, error) {
		t.Helper()
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer cleanup()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		ids, err := issueops.GetDescendantIDsInTx(ctx, tx, rootID, maxDepth)
		sort.Strings(ids)
		return ids, err
	}

	for _, tc := range []struct {
		name     string
		root     string
		maxDepth int
		want     []string
		wantErr  string
	}{
		{name: "issue root reaches issue, wisp and wisp-parented descendants; excludes itself, the blocker and other trees",
			root: root, want: []string{childA, childB, grandchild, issueD, wispW, wispW2}},
		{name: "a maxDepth the tree does not reach changes nothing",
			root: root, maxDepth: 10, want: []string{childA, childB, grandchild, issueD, wispW, wispW2}},
		{name: "a maxDepth the tree reaches is an error, not a truncated answer",
			root: root, maxDepth: 2, wantErr: "reached max depth 2"},
		{name: "cross-prefix parent is walked through depends_on_external",
			root: crossRoot, want: []string{crossF, crossG}},
		{name: "unrelated tree is its own answer", root: otherU, want: []string{otherV}},
		{name: "walking from inside the cycle reaches around it and excludes the start",
			root: grandchild, want: []string{root, childA, childB, issueD, wispW, wispW2}},
		{name: "a leaf has no descendants", root: crossG, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := walk(t, tc.root, tc.maxDepth)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("walk(%s, %d) error = %v, want one containing %q", tc.root, tc.maxDepth, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("walk(%s, %d): %v", tc.root, tc.maxDepth, err)
			}
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("walk(%s, %d) = %v, want %v", tc.root, tc.maxDepth, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("walk(%s, %d) = %v, want %v", tc.root, tc.maxDepth, got, want)
				}
			}
		})
	}
}
