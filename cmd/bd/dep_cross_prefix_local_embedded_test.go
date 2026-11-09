//go:build cgo

package main

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
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
