//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestEmbeddedVCStatusReportsUncommittedWorkingSet pins GH#7783 shape 2:
// `bd vc status` ("Show current branch and uncommitted changes") must name
// the tables a dirty working set has modified, as `dolt status` does, instead
// of printing only the branch and commit.
func TestEmbeddedVCStatusReportsUncommittedWorkingSet(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt vc tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "vcdirty")
	issue := bdCreate(t, bd, dir, "dirty working set", "--type", "task")

	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	// An uncommitted label row, as `bd dolt pull --strategy theirs` leaves.
	if _, err := db.ExecContext(t.Context(), "INSERT INTO labels (issue_id, label) VALUES (?, ?)", issue.ID, "uncommitted"); err != nil {
		cleanup()
		t.Fatalf("insert label: %v", err)
	}
	var dirty int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'labels'").Scan(&dirty); err != nil {
		cleanup()
		t.Fatalf("read dolt_status: %v", err)
	}
	cleanup()
	if dirty == 0 {
		t.Fatal("precondition: labels is not modified in the working set")
	}

	out := bdVC(t, bd, dir, "status")
	if !strings.Contains(out, "labels") {
		t.Errorf("bd vc status hides the uncommitted working set (dolt_status lists labels as modified); got:\n%s", out)
	}
}

// doltStatusSnapshot is one row of the dolt_status system table as the
// observe-only checks compare them.
type doltStatusSnapshot struct {
	table  string
	staged bool
	status string
}

// snapshotDoltStatus reads dolt_status through an open embedded SQL
// connection, sorted for comparison.
func snapshotDoltStatus(t *testing.T, db *sql.DB) []doltStatusSnapshot {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT table_name, staged, status FROM dolt_status ORDER BY table_name, staged, status")
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []doltStatusSnapshot
	for rows.Next() {
		var s doltStatusSnapshot
		if err := rows.Scan(&s.table, &s.staged, &s.status); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_status: %v", err)
	}
	return out
}

// TestEmbeddedVCStatusReportsStagedAndUnstaged pins the working-set half of
// GH#7783: `bd vc status` must report staged and unstaged non-bookkeeping
// tables — table, status and staging state — in text and JSON, keep the JSON
// branch and commit fields, sort each list by table name, keep a table with
// both staged and unstaged changes in BOTH lists, and leave the working set
// byte-for-byte as it found it (the command observes; it never stages,
// commits or resolves).
func TestEmbeddedVCStatusReportsStagedAndUnstaged(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt vc tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "vcstag")
	issue := bdCreate(t, bd, dir, "staged and unstaged", "--type", "task")

	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	// labels: one staged row plus a later unstaged one, so the table must
	// appear in BOTH lists — the shape `bd dolt pull --strategy theirs`
	// leaves behind plus an operator's partial staging.
	if _, err := db.ExecContext(t.Context(),
		"INSERT INTO labels (issue_id, label) VALUES (?, ?)", issue.ID, "staged-one"); err != nil {
		cleanup()
		t.Fatalf("insert staged label: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), "CALL DOLT_ADD('labels')"); err != nil {
		cleanup()
		t.Fatalf("stage labels: %v", err)
	}
	if _, err := db.ExecContext(t.Context(),
		"INSERT INTO labels (issue_id, label) VALUES (?, ?)", issue.ID, "unstaged-two"); err != nil {
		cleanup()
		t.Fatalf("insert unstaged label: %v", err)
	}
	// issues: a second non-bookkeeping table with an unstaged edit, so the
	// sorting across tables is pinned too.
	if _, err := db.ExecContext(t.Context(),
		"UPDATE issues SET title = ? WHERE id = ?", "unstaged title", issue.ID); err != nil {
		cleanup()
		t.Fatalf("dirty issues: %v", err)
	}
	before := snapshotDoltStatus(t, db)
	cleanup()
	if len(before) < 3 {
		t.Fatalf("precondition: expected at least 3 dolt_status rows, got %v", before)
	}

	text := bdVC(t, bd, dir, "status")
	for _, want := range []string{
		"Uncommitted changes:",
		"labels: modified (staged)",
		"labels: modified (unstaged)",
		"issues: modified (unstaged)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}

	// A second run in JSON mode must see the same working set (observe-only)
	// and report both lists with the branch and commit preserved.
	var out struct {
		Branch string `json:"branch"`
		Commit string `json:"commit"`
		Staged []struct {
			Table  string `json:"table"`
			Status string `json:"status"`
		} `json:"staged"`
		Unstaged []struct {
			Table  string `json:"table"`
			Status string `json:"status"`
		} `json:"unstaged"`
	}
	js := bdVC(t, bd, dir, "status", "--json")
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		t.Fatalf("parse vc status --json: %v\n%s", err, js)
	}
	if out.Branch != "main" {
		t.Errorf("json branch = %q, want main", out.Branch)
	}
	if out.Commit == "" {
		t.Error("json commit must stay present and non-empty")
	}
	if len(out.Staged) != 1 || out.Staged[0].Table != "labels" || out.Staged[0].Status != "modified" {
		t.Errorf("json staged = %+v, want exactly [labels modified]", out.Staged)
	}
	if len(out.Unstaged) != 2 ||
		out.Unstaged[0].Table != "issues" || out.Unstaged[0].Status != "modified" ||
		out.Unstaged[1].Table != "labels" || out.Unstaged[1].Status != "modified" {
		t.Errorf("json unstaged = %+v, want [issues modified] then [labels modified] (sorted by table)", out.Unstaged)
	}

	// The command observed twice and must have left the working set alone.
	db2, cleanup2, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("reopen SQL: %v", err)
	}
	after := snapshotDoltStatus(t, db2)
	if err := cleanup2(); err != nil {
		t.Fatalf("cleanup reopen: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("bd vc status changed the working set:\nbefore: %v\nafter:  %v", before, after)
	}
}
