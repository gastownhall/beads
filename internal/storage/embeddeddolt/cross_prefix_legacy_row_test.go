//go:build cgo

package embeddeddolt_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedCrossPrefixLegacyExternalRowRepair pins GH#8299's compatibility
// contract for rows written before presence-first classification. An edge
// stored external while its target was absent KEEPS that column when the
// target later appears locally: a same-type re-add is not a repair — it is
// change-free and rewrites nothing. The explicit remove + re-add is the
// repair: the fresh classification sees the target this database now holds,
// lands the edge in its local column, and the re-named type and metadata ride
// along. On base the re-add after the removal re-filed the edge external, so
// the repair never happened and the freshly-present target never blocked its
// dependent.
func TestEmbeddedCrossPrefixLegacyExternalRowRepair(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "a")
	ctx := t.Context()
	const source, target = "a-lgsrc", "b-lgtgt"

	addBlocks := func() {
		t.Helper()
		if err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID: source, DependsOnID: target, Type: types.DepBlocks,
			Metadata: `{"origin":"legacy"}`,
		}, "tester"); err != nil {
			t.Fatalf("AddDependency %s -> %s: %v", source, target, err)
		}
	}

	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: source, Title: "dependent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue %s: %v", source, err)
	}

	// The target is absent from both tables: the edge stores unverified in
	// depends_on_external (GH#6556).
	addBlocks()
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", source, target, "depends_on_external")

	// The target appears locally later — an import, say. The legacy row keeps
	// its column: the same-type re-add is change-free, so nothing is
	// rewritten and no duplicate row appears.
	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: target, Title: "target that arrived later", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue %s: %v", target, err)
	}
	addBlocks()
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", source, target, "depends_on_external")
	var rows int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", []any{source}, &rows)
	if rows != 1 {
		t.Errorf("dependencies rows for %s = %d after the re-add, want 1 (change-free, no duplicate)", source, rows)
	}
	var blockedEarly bool
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{source}, &blockedEarly)
	if blockedEarly {
		t.Errorf("is_blocked for %s = true while the edge still sits in its legacy external column", source)
	}

	// Remove + re-add is the repair: the edge takes its local column, keeps
	// its type and metadata, and — being a real local blocks edge on an open
	// target — actually blocks.
	if err := te.store.RemoveDependency(ctx, source, target, "tester"); err != nil {
		t.Fatalf("RemoveDependency %s -> %s: %v", source, target, err)
	}
	addBlocks()
	assertCrossPrefixEdgeColumn(t, ctx, te, "dependencies", source, target, "depends_on_issue_id")
	var metadata string
	te.queryScalar(t, ctx,
		"SELECT metadata FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
		[]any{source, target}, &metadata)
	if !strings.Contains(metadata, "origin") || !strings.Contains(metadata, "legacy") {
		t.Errorf("repaired edge metadata = %q, want the caller's {\"origin\":\"legacy\"} "+
			"preserved across the repair", metadata)
	}
	var blocked bool
	te.queryScalar(t, ctx, "SELECT is_blocked FROM issues WHERE id = ?", []any{source}, &blocked)
	if !blocked {
		t.Errorf("is_blocked for %s = false after the repaired local blocks edge on open %s", source, target)
	}
}
