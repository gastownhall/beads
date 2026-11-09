//go:build cgo

// bd-fnvnuq: behavioral tests for `bd doctor --check=pollution --clean`
// (GH#5025). Cleanup must delete only unlinked high-confidence hits, keep
// medium-confidence hits as review-only, and refuse to touch any hit that has
// dependency links (its edges cannot be restored from the issue-object
// backup). Each test seeds a dedicated database, drives the real bd binary as
// a subprocess, and asserts on the surviving rows, the backup file and the
// output — the same harness doctor_max_rows_test.go uses.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// newPollutionCleanWorkspace creates a temp .beads workspace whose
// metadata.json points at an isolated database on the shared test server, for
// one cleanup test.
func newPollutionCleanWorkspace(t *testing.T, prefix string) (string, string, *dolt.DoltStore) {
	t.Helper()
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	tmpDir := t.TempDir()
	beads := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beads, 0755); err != nil {
		t.Fatal(err)
	}
	store := newTestStoreIsolatedDB(t, filepath.Join(beads, "dolt"), prefix)
	return tmpDir, beads, store
}

// pollutionCleanCreateIssue seeds one issue with an explicit ID matching the
// workspace prefix.
func pollutionCleanCreateIssue(t *testing.T, store *dolt.DoltStore, issue *types.Issue) {
	t.Helper()
	if err := store.CreateIssue(context.Background(), issue, "test"); err != nil {
		t.Fatalf("seeding issue %s: %v", issue.ID, err)
	}
}

// pollutionCleanRun runs the real bd binary's pollution cleanup in the
// workspace and requires a zero exit.
func pollutionCleanRun(t *testing.T, tmpDir string, args ...string) string {
	t.Helper()
	bdBin := buildBDForInitTests(t)
	all := append([]string{"doctor", "--check=pollution", "--clean"}, args...)
	out, code := bdRunRaw(t, bdBin, tmpDir, doctorMaxRowsEnv(), all...)
	if code != 0 {
		t.Fatalf("bd %s exited %d:\n%s", strings.Join(all, " "), code, out)
	}
	return out
}

// pollutionCleanBackupIDs parses the IDs the cleanup backed up, or nil when no
// backup file was written.
func pollutionCleanBackupIDs(t *testing.T, beads string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(beads, "pollution-backup.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading pollution backup: %v", err)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var record struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("parsing backup line %q: %v", line, err)
		}
		ids = append(ids, record.ID)
	}
	return ids
}

// pollutionCleanIssueSurvived asserts a seeded issue is still there.
func pollutionCleanIssueSurvived(t *testing.T, store *dolt.DoltStore, id string) {
	t.Helper()
	if _, err := store.GetIssue(context.Background(), id); err != nil {
		t.Errorf("issue %s did not survive cleanup: %v", id, err)
	}
}

// pollutionCleanIssueDeleted asserts a seeded issue is gone.
func pollutionCleanIssueDeleted(t *testing.T, store *dolt.DoltStore, id string) {
	t.Helper()
	if issue, err := store.GetIssue(context.Background(), id); err == nil && issue != nil {
		t.Errorf("issue %s survived cleanup (status %q) but was selected for deletion", id, issue.Status)
	}
}

// TestDoctorPollutionClean_DeletesOnlyUnlinkedHighConfidence pins AC-6: with
// confirmation bypassed, cleanup backs up and deletes exactly the unlinked
// high-confidence hit; the medium-confidence hit and everything ineligible
// (substantive work, closed, epic) survive, and the output says the medium
// hit needs manual review.
func TestDoctorPollutionClean_DeletesOnlyUnlinkedHighConfidence(t *testing.T) {
	tmpDir, beads, store := newPollutionCleanWorkspace(t, "dpcl")
	now := time.Now()
	pollutionCleanCreateIssue(t, store, &types.Issue{ID: "dpcl-high", Title: "Test issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})
	pollutionCleanCreateIssue(t, store, &types.Issue{ID: "dpcl-med", Title: "Debug sync hangs", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})
	pollutionCleanCreateIssue(t, store, &types.Issue{ID: "dpcl-substantive", Title: "test-runner hangs on CI", Description: "The test runner deadlocks when two suites contend on the lock.", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask})
	pollutionCleanCreateIssue(t, store, &types.Issue{ID: "dpcl-closed", Title: "test-closed leftover", Status: types.StatusClosed, Priority: 2, IssueType: types.TypeTask, ClosedAt: &now})
	pollutionCleanCreateIssue(t, store, &types.Issue{ID: "dpcl-epic", Title: "test-quality architecture", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic})

	out := pollutionCleanRun(t, tmpDir, "--yes")

	pollutionCleanIssueDeleted(t, store, "dpcl-high")
	for _, id := range []string{"dpcl-med", "dpcl-substantive", "dpcl-closed", "dpcl-epic"} {
		pollutionCleanIssueSurvived(t, store, id)
	}
	if got := pollutionCleanBackupIDs(t, beads); len(got) != 1 || got[0] != "dpcl-high" {
		t.Errorf("backup IDs = %v, want exactly [dpcl-high]", got)
	}
	if !strings.Contains(out, "dpcl-med") || !strings.Contains(out, "manual review") {
		t.Errorf("output does not explain that the medium hit needs manual review:\n%s", out)
	}
}

// TestDoctorPollutionClean_NoDeletableHits_NoBackup pins the empty-selection
// contract: when only review-only hits remain (here a single medium hit),
// cleanup writes no backup, deletes nothing and prompts nobody.
func TestDoctorPollutionClean_NoDeletableHits_NoBackup(t *testing.T) {
	tmpDir, beads, store := newPollutionCleanWorkspace(t, "dpnone")
	pollutionCleanCreateIssue(t, store, &types.Issue{ID: "dpnone-med", Title: "Debug sync hangs", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})

	out := pollutionCleanRun(t, tmpDir, "--yes")

	pollutionCleanIssueSurvived(t, store, "dpnone-med")
	if got := pollutionCleanBackupIDs(t, beads); got != nil {
		t.Errorf("backup IDs = %v, want none (nothing was deletable)", got)
	}
	if !strings.Contains(out, "dpnone-med") {
		t.Errorf("output does not name the review-only hit:\n%s", out)
	}
}

// TestDoctorPollutionClean_PromptCanceled pins the prompt contract: without
// --yes and an empty stdin, cleanup is canceled before any backup or delete.
func TestDoctorPollutionClean_PromptCanceled(t *testing.T) {
	tmpDir, beads, store := newPollutionCleanWorkspace(t, "dpcancel")
	pollutionCleanCreateIssue(t, store, &types.Issue{ID: "dpcancel-high", Title: "Test issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})

	bdBin := buildBDForInitTests(t)
	out, code := bdRunRaw(t, bdBin, tmpDir, doctorMaxRowsEnv(), "doctor", "--check=pollution", "--clean")
	if code != 0 {
		t.Fatalf("canceled cleanup exited %d:\n%s", code, out)
	}

	pollutionCleanIssueSurvived(t, store, "dpcancel-high")
	if got := pollutionCleanBackupIDs(t, beads); got != nil {
		t.Errorf("canceled cleanup wrote a backup: %v", got)
	}
	if !strings.Contains(out, "Canceled") {
		t.Errorf("output does not report the cancellation:\n%s", out)
	}
}

// TestDoctorPollutionClean_BackupErrorDeletesNothing pins the backup-error
// contract: when the backup file cannot be written, cleanup fails before
// deleting anything.
func TestDoctorPollutionClean_BackupErrorDeletesNothing(t *testing.T) {
	tmpDir, beads, store := newPollutionCleanWorkspace(t, "dpbak")
	pollutionCleanCreateIssue(t, store, &types.Issue{ID: "dpbak-high", Title: "Test issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})

	// A directory where the backup file would be created makes os.Create fail.
	if err := os.MkdirAll(filepath.Join(beads, "pollution-backup.jsonl"), 0755); err != nil {
		t.Fatal(err)
	}

	bdBin := buildBDForInitTests(t)
	out, code := bdRunRaw(t, bdBin, tmpDir, doctorMaxRowsEnv(), "doctor", "--check=pollution", "--clean", "--yes")
	if code == 0 {
		t.Fatalf("cleanup succeeded despite an unwritable backup path:\n%s", out)
	}

	pollutionCleanIssueSurvived(t, store, "dpbak-high")
	if !strings.Contains(out, "backing up issues") {
		t.Errorf("output does not report the backup failure:\n%s", out)
	}
}
