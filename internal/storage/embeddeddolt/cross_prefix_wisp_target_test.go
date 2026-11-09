//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// depEdgeTargetColumns reads the target columns of source's one stored edge in
// table. The three columns answer which target plane the write path routed
// the edge into (GH#8299), so the test asserts them directly instead of through
// a hydrated issue, which cannot see columns.
func depEdgeTargetColumns(t *testing.T, ctx context.Context, store *EmbeddedDoltStore, table, source string) (issue, wisp, external sql.NullString) {
	t.Helper()
	if err := store.withConn(ctx, false, func(tx *sql.Tx) error {
		//nolint:gosec // G201: table is "dependencies" or "wisp_dependencies", a caller-fixed constant.
		rows := tx.QueryRowContext(ctx, "SELECT depends_on_issue_id, depends_on_wisp_id, depends_on_external FROM "+table+" WHERE issue_id = ?", source)
		return rows.Scan(&issue, &wisp, &external)
	}); err != nil {
		t.Fatalf("read stored dep edge for %s from %s: %v", source, table, err)
	}
	return issue, wisp, external
}

// wispIsBlocked reads the STORED is_blocked flag for a wisp id, the wisp-plane
// twin of embeddedIsBlocked (which reads the issues table).
func wispIsBlocked(t *testing.T, ctx context.Context, store *EmbeddedDoltStore, id string) bool {
	t.Helper()
	var blocked int
	if err := store.withConn(ctx, false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT is_blocked FROM wisps WHERE id = ?", id).Scan(&blocked)
	}); err != nil {
		t.Fatalf("read is_blocked for wisp %s: %v", id, err)
	}
	return blocked != 0
}

// TestEmbeddedCrossPrefixWispTargetIsTypedAndBlocks pins GH#8299 (bd-6jk1y2)
// AC-4: a wisp target whose id prefix differs from the configured prefix but
// which THIS database holds is a typed local edge — depends_on_wisp_id, never
// depends_on_external — whether the source is an issue or a wisp, and it
// blocks each source while open and unblocks it when closed. The prefix-era
// classifier filed both shapes under external, where they blocked nothing.
func TestEmbeddedCrossPrefixWispTargetIsTypedAndBlocks(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), ".beads"), "cpwisp", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "b"); err != nil {
		t.Fatal(err)
	}

	// The cross-prefix wisp target: a row THIS database's wisps table holds
	// under a foreign prefix. Explicit-ID creates skip prefix validation, and
	// the ephemeral flag routes the row to the wisp plane.
	target := &types.Issue{ID: "a-wsp", Title: "cross-prefix wisp target", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, target, "tester"); err != nil {
		t.Fatalf("create wisp target %s: %v", target.ID, err)
	}
	src := &types.Issue{ID: "b-src", Title: "issue source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, src, "tester"); err != nil {
		t.Fatalf("create issue source %s: %v", src.ID, err)
	}
	wsrc := &types.Issue{ID: "b-wsrc", Title: "wisp source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	if err := store.CreateIssue(ctx, wsrc, "tester"); err != nil {
		t.Fatalf("create wisp source %s: %v", wsrc.ID, err)
	}

	for _, edge := range []*types.Dependency{
		{IssueID: src.ID, DependsOnID: target.ID, Type: types.DepBlocks},
		{IssueID: wsrc.ID, DependsOnID: target.ID, Type: types.DepBlocks},
	} {
		if err := store.AddDependency(ctx, edge, "tester"); err != nil {
			t.Fatalf("add blocks edge %s -> %s: %v", edge.IssueID, edge.DependsOnID, err)
		}
	}

	// Each source's edge lands in its source-routed table with the wisp
	// target in the typed column; the external column stays empty.
	issueCol, wispCol, externalCol := depEdgeTargetColumns(t, ctx, store, "dependencies", src.ID)
	if !wispCol.Valid || wispCol.String != target.ID || issueCol.Valid || externalCol.Valid {
		t.Fatalf("issue-source edge columns = (issue:%q, wisp:%q, external:%q): want wisp %q only",
			issueCol.String, wispCol.String, externalCol.String, target.ID)
	}
	issueCol, wispCol, externalCol = depEdgeTargetColumns(t, ctx, store, "wisp_dependencies", wsrc.ID)
	if !wispCol.Valid || wispCol.String != target.ID || issueCol.Valid || externalCol.Valid {
		t.Fatalf("wisp-source edge columns = (issue:%q, wisp:%q, external:%q): want wisp %q only",
			issueCol.String, wispCol.String, externalCol.String, target.ID)
	}

	// While the target is open, both sources are blocked by it.
	if !embeddedIsBlocked(t, ctx, store, src.ID) {
		t.Fatalf("%s is not blocked by open wisp target %s", src.ID, target.ID)
	}
	if !wispIsBlocked(t, ctx, store, wsrc.ID) {
		t.Fatalf("%s is not blocked by open wisp target %s", wsrc.ID, target.ID)
	}

	// Closing the wisp target unblocks both: the flag follows the target's
	// status, exactly as a same-database issue target's does (AC-2).
	if err := store.CloseIssue(ctx, target.ID, "", "tester", ""); err != nil {
		t.Fatalf("close wisp target %s: %v", target.ID, err)
	}
	if embeddedIsBlocked(t, ctx, store, src.ID) {
		t.Fatalf("%s is still blocked after wisp target %s was closed", src.ID, target.ID)
	}
	if wispIsBlocked(t, ctx, store, wsrc.ID) {
		t.Fatalf("%s is still blocked after wisp target %s was closed", wsrc.ID, target.ID)
	}
}
