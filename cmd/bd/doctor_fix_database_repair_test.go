//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/types"
)

// TestDoctorFixDatabaseRepairLeavesLiveServerRowsUnchanged runs bd doctor
// --fix --yes's Database repair (fix.DatabaseVersionWithBdVersion) in a
// server-mode workspace with no .beads/dolt, as in every fresh clone and on
// every other machine, whose database on the Dolt server holds a row newer
// than the tracked issues.jsonl. Two Database verdicts reach the repair there:
// the check read the database and found it last stamped by another bd
// version, or the check could not open the database while the repair's own
// open can. The repair took the missing directory for a missing database and
// imported the tracked copy over the live row in both. It leaves the row as
// it is.
func TestDoctorFixDatabaseRepairLeavesLiveServerRowsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(repo string) doctorCheck
	}{
		{name: "the check read a database another bd version stamped", check: func(repo string) doctorCheck {
			shared := doctor.NewSharedStore(repo)
			defer shared.Close()
			return convertDoctorCheck(doctor.CheckDatabaseVersionWithStore(shared, Version))
		}},
		{name: "the check could not open the database", check: func(string) doctorCheck {
			return doctorCheck{Name: "Database", Status: statusError, Message: "Unable to open database",
				Fix: "Run 'bd doctor --fix' to attempt repair. Check 'bd dolt status' for server configuration issues"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			beadsDir := filepath.Join(repo, ".beads")
			store := newTestStoreIsolatedDB(t, filepath.Join(beadsDir, "dolt"), "tp")
			ctx := context.Background()
			issue := &types.Issue{Title: "tracked title", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := store.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatal(err)
			}
			tracked, err := store.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Fatal(err)
			}
			line, err := json.Marshal(tracked)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), append(line, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			// The live row moves on past the tracked copy, and another bd
			// version stamped the database last.
			if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{"title": "live title"}, "test"); err != nil {
				t.Fatal(err)
			}
			if err := store.SetLocalMetadata(ctx, "bd_version", "0.0.1"); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(beadsDir, "dolt")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BEADS_DIR", beadsDir)

			check := tc.check(repo)
			if check.Status == statusOK || check.Fix == "" {
				t.Fatalf("Database = %s %q (fix %q), want a verdict bd doctor --fix repairs", check.Status, check.Message, check.Fix)
			}
			origYes := doctorYes
			t.Cleanup(func() { doctorYes = origYes })
			doctorYes = true
			out := captureStdout(t, func() error {
				applyFixes(doctorResult{Path: repo, Checks: []doctorCheck{check}})
				return nil
			})

			got, err := store.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != "live title" {
				t.Errorf("bd doctor --fix imported issues.jsonl over the live row: title %q, want %q\nDatabase: %s %q\n%s", got.Title, "live title", check.Status, check.Message, out)
			}
			if !strings.Contains(out, "Fix summary: 1 fixed, 0 errors") {
				t.Errorf("bd doctor --fix does not report the Database repair fixed:\n%s", out)
			}
		})
	}
}
