//go:build cgo

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// One database can hold several id prefixes (a shared ledger, a hydrated
// repo, a forced --id), so a dependency target whose prefix differs from its
// source's may still live right here. These tests pin GH#8299 (bd-6jk1y2):
// such a target is typed by where it lives and blocks like any local one;
// only a target this database does not hold falls back to
// depends_on_external.

// crossPrefixDB is the database bdInit creates for crossPrefixSourcePrefix.
const (
	crossPrefixDB           = "xpb"
	crossPrefixSourcePrefix = "xpb"
)

func skipUnlessEmbeddedCrossPrefix(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
}

// depTargetColumn names the one typed target column holding the edge
// source -> target in table, or "" when there is no such edge.
func depTargetColumn(t *testing.T, beadsDir, table, source, target string) string {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), crossPrefixDB, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var issueCol, wispCol, externalCol sql.NullString
	//nolint:gosec // G201: table is one of the two dependency tables named by the caller.
	err = db.QueryRowContext(t.Context(),
		"SELECT depends_on_issue_id, depends_on_wisp_id, depends_on_external FROM "+table+
			" WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ?",
		source, target).Scan(&issueCol, &wispCol, &externalCol)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("read %s edge %s -> %s: %v", table, source, target, err)
	}
	switch {
	case issueCol.Valid:
		return "depends_on_issue_id"
	case wispCol.Valid:
		return "depends_on_wisp_id"
	default:
		return "depends_on_external"
	}
}

// blockedBy returns the blockers bd blocked --json names for id, and whether
// id is listed at all.
func blockedBy(t *testing.T, bd, dir, id string) ([]string, bool) {
	t.Helper()
	for _, entry := range bdBlockedJSON(t, bd, dir) {
		if entry["id"] != id {
			continue
		}
		var blockers []string
		raw, _ := entry["blocked_by"].([]interface{})
		for _, b := range raw {
			if s, ok := b.(string); ok {
				blockers = append(blockers, s)
			}
		}
		return blockers, true
	}
	return nil, false
}

// TestEmbeddedDepAddCrossPrefixLocalTargetBlocks is the report's
// reproduction: a blocks edge onto an open blocker whose prefix differs from
// the dependent's, held in the same database. The edge is typed as a local
// issue, ready leaves the dependent out, and blocked names the blocker.
func TestEmbeddedDepAddCrossPrefixLocalTargetBlocks(t *testing.T) {
	skipUnlessEmbeddedCrossPrefix(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", crossPrefixSourcePrefix)

	blocker := bdCreate(t, bd, dir, "Open blocker", "--type", "task")
	dependent := bdCreate(t, bd, dir, "Dependent", "--type", "task", "--id", "xpa-x1", "--force")
	bdDep(t, bd, dir, "add", dependent.ID, blocker.ID)

	if got := depTargetColumn(t, beadsDir, "dependencies", dependent.ID, blocker.ID); got != "depends_on_issue_id" {
		t.Fatalf("edge %s -> %s stored in %q, want depends_on_issue_id: the blocker lives in this database", dependent.ID, blocker.ID, got)
	}
	ready := readyIDs(t, bd, dir)
	if ready[dependent.ID] {
		t.Errorf("%s is ready while its same-database blocker %s is open: %v", dependent.ID, blocker.ID, ready)
	}
	if !ready[blocker.ID] {
		t.Errorf("blocker %s missing from ready: %v", blocker.ID, ready)
	}
	blockers, listed := blockedBy(t, bd, dir, dependent.ID)
	if !listed {
		t.Fatalf("bd blocked does not list %s", dependent.ID)
	}
	if len(blockers) != 1 || blockers[0] != blocker.ID {
		t.Errorf("bd blocked names blockers %v for %s, want [%s]", blockers, dependent.ID, blocker.ID)
	}
}

// TestEmbeddedDepCrossPrefixLocalBlockerCloseAndReopen follows that blocker
// through its lifecycle: closing it frees the dependent, and reopening it
// blocks the dependent again, as for a same-prefix blocker.
func TestEmbeddedDepCrossPrefixLocalBlockerCloseAndReopen(t *testing.T) {
	skipUnlessEmbeddedCrossPrefix(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", crossPrefixSourcePrefix)

	blocker := bdCreate(t, bd, dir, "Blocker", "--type", "task")
	dependent := bdCreate(t, bd, dir, "Dependent", "--type", "task", "--id", "xpa-x2", "--force")
	bdDep(t, bd, dir, "add", dependent.ID, blocker.ID)

	bdClose(t, bd, dir, blocker.ID, "--reason", "done")
	if ready := readyIDs(t, bd, dir); !ready[dependent.ID] {
		t.Fatalf("%s is not ready after its blocker %s closed: %v", dependent.ID, blocker.ID, ready)
	}
	if _, listed := blockedBy(t, bd, dir, dependent.ID); listed {
		t.Fatalf("bd blocked still lists %s after its blocker %s closed", dependent.ID, blocker.ID)
	}

	bdReopen(t, bd, dir, blocker.ID)
	if ready := readyIDs(t, bd, dir); ready[dependent.ID] {
		t.Errorf("%s is ready again although its blocker %s was reopened: %v", dependent.ID, blocker.ID, ready)
	}
	blockers, listed := blockedBy(t, bd, dir, dependent.ID)
	if !listed || len(blockers) != 1 || blockers[0] != blocker.ID {
		t.Errorf("bd blocked names blockers %v (listed=%v) for %s after reopen, want [%s]", blockers, listed, dependent.ID, blocker.ID)
	}
}

// TestEmbeddedDepAddCrossPrefixAbsentTargetStaysExternal pins the fallback
// the fix keeps: a cross-prefix target this database does not hold, with no
// route to it, is stored in depends_on_external with the unresolved-target
// warning and its removal hint, and blocks nothing here.
func TestEmbeddedDepAddCrossPrefixAbsentTargetStaysExternal(t *testing.T) {
	skipUnlessEmbeddedCrossPrefix(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", crossPrefixSourcePrefix)

	source := bdCreate(t, bd, dir, "Source", "--type", "task")
	const absent = "zz-404"
	_, stderr := bdRunSplit(t, bd, dir, "dep", "add", source.ID, absent)

	if got := depTargetColumn(t, beadsDir, "dependencies", source.ID, absent); got != "depends_on_external" {
		t.Fatalf("edge %s -> %s stored in %q, want depends_on_external: this database holds no %s", source.ID, absent, got, absent)
	}
	for _, want := range []string{"was not found in this database or through routing", "bd dep remove " + source.ID + " " + absent} {
		if !strings.Contains(stderr, want) {
			t.Errorf("dep add stderr lacks %q:\n%s", want, stderr)
		}
	}
	if ready := readyIDs(t, bd, dir); !ready[source.ID] {
		t.Errorf("%s is not ready although its only edge names a target outside this database: %v", source.ID, ready)
	}
}

// isBlocked reads the stored is_blocked flag of id from table.
func isBlocked(t *testing.T, beadsDir, table, id string) bool {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), crossPrefixDB, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	var blocked bool
	//nolint:gosec // G201: table is one of the two issue tables named by the caller.
	if err := db.QueryRowContext(t.Context(), "SELECT is_blocked FROM "+table+" WHERE id = ?", id).Scan(&blocked); err != nil {
		t.Fatalf("read %s.is_blocked for %s: %v", table, id, err)
	}
	return blocked
}

// TestEmbeddedDepAddCrossPrefixLocalWispTargetBlocks covers the wisp plane:
// a cross-prefix target held here as a wisp is typed depends_on_wisp_id in
// the table the source routes to (dependencies for an issue source,
// wisp_dependencies for a wisp source), and both sources are blocked while
// the wisp is open and freed once it closes.
func TestEmbeddedDepAddCrossPrefixLocalWispTargetBlocks(t *testing.T) {
	skipUnlessEmbeddedCrossPrefix(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", crossPrefixSourcePrefix)

	target := bdCreate(t, bd, dir, "Wisp blocker", "--type", "task", "--ephemeral", "--id", "xpa-w1", "--force")
	issueSource := bdCreate(t, bd, dir, "Issue source", "--type", "task")
	wispSource := bdCreate(t, bd, dir, "Wisp source", "--type", "task", "--ephemeral")
	bdDep(t, bd, dir, "add", issueSource.ID, target.ID)
	bdDep(t, bd, dir, "add", wispSource.ID, target.ID)

	sources := []struct{ id, issueTable, depTable string }{
		{issueSource.ID, "issues", "dependencies"},
		{wispSource.ID, "wisps", "wisp_dependencies"},
	}
	for _, s := range sources {
		if got := depTargetColumn(t, beadsDir, s.depTable, s.id, target.ID); got != "depends_on_wisp_id" {
			t.Errorf("%s edge %s -> %s stored in %q, want depends_on_wisp_id: the target is a wisp in this database", s.depTable, s.id, target.ID, got)
		}
		if !isBlocked(t, beadsDir, s.issueTable, s.id) {
			t.Errorf("%s is not blocked while its wisp blocker %s is open", s.id, target.ID)
		}
	}

	bdClose(t, bd, dir, target.ID, "--reason", "done")
	for _, s := range sources {
		if isBlocked(t, beadsDir, s.issueTable, s.id) {
			t.Errorf("%s is still blocked after its wisp blocker %s closed", s.id, target.ID)
		}
	}
}

// blocksEdgeCount counts the blocks rows from source to target in both
// dependency tables.
func blocksEdgeCount(t *testing.T, beadsDir, source, target string) int {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), crossPrefixDB, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	total := 0
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		var n int
		//nolint:gosec // G201: table is one of the two hardcoded dependency tables.
		if err := db.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM "+table+" WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ? AND type = 'blocks'",
			source, target).Scan(&n); err != nil {
			t.Fatalf("count %s blocks edges %s -> %s: %v", table, source, target, err)
		}
		total += n
	}
	return total
}

// TestEmbeddedDepCrossPrefixHierarchyConflictRefused holds a cross-prefix
// pair held in this database to the blocking hierarchy rules: an issue may
// not be blocked by its own ancestor whatever the ancestor's prefix. A single
// dep add and a graph batch (whose blocks edge precedes the parent edge that
// makes it a conflict, so only the batch's end gate can see it) both return
// the hierarchy error and store no blocks edge.
func TestEmbeddedDepCrossPrefixHierarchyConflictRefused(t *testing.T) {
	skipUnlessEmbeddedCrossPrefix(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", crossPrefixSourcePrefix)

	grandparent := bdCreate(t, bd, dir, "Grandparent", "--type", "epic")
	parent := bdCreate(t, bd, dir, "Parent", "--type", "task", "--parent", grandparent.ID)
	const wantErr = "cannot be blocked by its ancestor"

	t.Run("single dep add", func(t *testing.T) {
		child := bdCreate(t, bd, dir, "Cross-prefix child", "--type", "task", "--id", "xpa-c1", "--force")
		bdDep(t, bd, dir, "add", child.ID, parent.ID, "--type", "parent-child")

		out := bdDepFail(t, bd, dir, "add", child.ID, grandparent.ID)
		if !strings.Contains(out, wantErr) {
			t.Errorf("dep add %s %s: want the hierarchy error %q, got:\n%s", child.ID, grandparent.ID, wantErr, out)
		}
		if n := blocksEdgeCount(t, beadsDir, child.ID, grandparent.ID); n != 0 {
			t.Errorf("%d blocks edge(s) %s -> %s stored after the refusal", n, child.ID, grandparent.ID)
		}
	})

	t.Run("graph batch", func(t *testing.T) {
		const child = "xpa-c2"
		plan := fmt.Sprintf(`{
			"nodes": [{"key": "c", "id": %q, "title": "Batch cross-prefix child", "type": "task"}],
			"edges": [
				{"from_key": "c", "to_id": %q, "type": "blocks"},
				{"from_key": "c", "to_id": %q, "type": "parent-child"}
			]
		}`, child, grandparent.ID, parent.ID)
		planFile := filepath.Join(t.TempDir(), "plan.json")
		if err := os.WriteFile(planFile, []byte(plan), 0o600); err != nil {
			t.Fatalf("write plan: %v", err)
		}
		out := bdCreateFail(t, bd, dir, "--graph", planFile, "--force")
		if !strings.Contains(out, wantErr) {
			t.Errorf("create --graph: want the hierarchy error %q, got:\n%s", wantErr, out)
		}
		if n := blocksEdgeCount(t, beadsDir, child, grandparent.ID); n != 0 {
			t.Errorf("%d blocks edge(s) %s -> %s stored after the refusal", n, child, grandparent.ID)
		}
	})
}
