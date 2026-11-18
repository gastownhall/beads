//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

// vcStatusOpenSQL opens a workspace's embedded database directly (bd must
// not be running against it).
func vcStatusOpenSQL(t *testing.T, beadsDir string) (*sql.DB, func()) {
	t.Helper()
	cfg, _ := configfile.Load(beadsDir)
	database := ""
	if cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	return db, func() { _ = cleanup() }
}

// vcStatusSnapshot returns every dolt_status row as "table staged status".
func vcStatusSnapshot(t *testing.T, beadsDir string) []string {
	t.Helper()
	db, cleanup := vcStatusOpenSQL(t, beadsDir)
	defer cleanup()
	rows, err := db.QueryContext(t.Context(), "SELECT table_name, staged, status FROM dolt_status ORDER BY table_name, staged, status")
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var table, status string
		var staged bool
		if err := rows.Scan(&table, &staged, &status); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		out = append(out, fmt.Sprintf("%s %t %s", table, staged, status))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	return out
}

// vcStatusJSON is the --json shape of bd vc status.
type vcStatusJSON struct {
	Branch   string             `json:"branch"`
	Commit   string             `json:"commit"`
	Staged   []vcStatusJSONItem `json:"staged"`
	Unstaged []vcStatusJSONItem `json:"unstaged"`
}

type vcStatusJSONItem struct {
	Table  string `json:"table"`
	Status string `json:"status"`
}

// TestEmbeddedVCStatusReportsStagedAndUnstaged pins that bd vc status reports
// a dirty working set's staged and unstaged tables in text and JSON, keeps the
// branch and commit in JSON, and leaves the working set exactly as it found
// it: labels is staged only, comments is unstaged only, and issues is both
// (staged, then modified again).
func TestEmbeddedVCStatusReportsStagedAndUnstaged(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt vc tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "vcws")
	issue := bdCreate(t, bd, dir, "working set", "--type", "task")

	db, cleanup := vcStatusOpenSQL(t, beadsDir)
	for _, stmt := range []string{
		"INSERT INTO labels (issue_id, label) VALUES ('" + issue.ID + "', 'staged-label')",
		"CALL DOLT_ADD('labels')",
		"UPDATE issues SET title = 'staged title' WHERE id = '" + issue.ID + "'",
		"CALL DOLT_ADD('issues')",
		"UPDATE issues SET title = 'unstaged title' WHERE id = '" + issue.ID + "'",
		"INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (UUID(), '" + issue.ID + "', 'tester', 'unstaged comment', NOW())",
	} {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			cleanup()
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	cleanup()
	before := vcStatusSnapshot(t, beadsDir)

	text := bdVC(t, bd, dir, "status")
	for _, want := range []string{
		"Branch: main",
		"Uncommitted changes:",
		"labels: modified (staged)",
		"issues: modified (staged)",
		"issues: modified (unstaged)",
		"comments: modified (unstaged)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text output must contain %q; got:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"labels: modified (unstaged)", "comments: modified (staged)", "none"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("text output must not contain %q; got:\n%s", unwanted, text)
		}
	}

	out := bdVC(t, bd, dir, "status", "--json")
	var got vcStatusJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, out)
	}
	if got.Branch != "main" || got.Commit == "" {
		t.Errorf("JSON branch/commit = %q/%q, want main and a hash\n%s", got.Branch, got.Commit, out)
	}
	hasEntry := func(list []vcStatusJSONItem, table string) bool {
		for _, e := range list {
			if e.Table == table && e.Status == "modified" {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		table            string
		staged, unstaged bool
	}{
		{"labels", true, false},
		{"issues", true, true},
		{"comments", false, true},
	} {
		if hasEntry(got.Staged, c.table) != c.staged || hasEntry(got.Unstaged, c.table) != c.unstaged {
			t.Errorf("JSON %s: staged=%t unstaged=%t, want staged=%t unstaged=%t\n%s",
				c.table, hasEntry(got.Staged, c.table), hasEntry(got.Unstaged, c.table), c.staged, c.unstaged, out)
		}
	}

	if after := vcStatusSnapshot(t, beadsDir); !reflect.DeepEqual(before, after) {
		t.Errorf("bd vc status changed the working set\nbefore: %v\nafter:  %v", before, after)
	}
}
