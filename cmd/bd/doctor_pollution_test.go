//go:build cgo

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// testPollutionCheck returns the validate gate's Test Pollution check for
// the workspace at dir.
func testPollutionCheck(t *testing.T, dir string) doctorCheck {
	t.Helper()
	for _, cr := range collectValidateChecks(dir) {
		if cr.check.Name == "Test Pollution" {
			return cr.check
		}
	}
	t.Fatal("Test Pollution check not found")
	return doctorCheck{}
}

// TestValidateCheck_PollutionIgnoresClosedAndEpics pins GH#5025 on the
// validate gate: closed issues and epics with fixture titles are not counted
// as test pollution, matching the detailed pollution check.
func TestValidateCheck_PollutionIgnoresClosedAndEpics(t *testing.T) {
	tmpDir, store := setupValidateTestDB(t, "pce")
	ctx := context.Background()

	closedAt := time.Now().UTC()
	for _, issue := range []*types.Issue{
		{Title: "Test issue", Status: types.StatusClosed, ClosedAt: &closedAt, Priority: 2, IssueType: types.TypeTask},
		{Title: "test-fixture data", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic},
	} {
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%q): %v", issue.Title, err)
		}
	}
	store.Close()

	if check := testPollutionCheck(t, tmpDir); check.Status != statusOK {
		t.Errorf("Test Pollution = %q (%s), want %q: closed issues and epics are not pollution", check.Status, check.Message, statusOK)
	}
}

// pollutionCleanupWorkspace is a workspace on the Dolt test server that a bd
// subprocess opens, with its store for seeding and inspection.
func pollutionCleanupWorkspace(t *testing.T, prefix string) (string, *dolt.DoltStore) {
	t.Helper()
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}
	tmpDir := t.TempDir()
	beadsSubdir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsSubdir, 0o755); err != nil {
		t.Fatal(err)
	}
	return tmpDir, newTestStoreIsolatedDB(t, filepath.Join(beadsSubdir, "dolt"), prefix)
}

// seedPollutionIssue creates an open task with title and description and
// returns its ID.
func seedPollutionIssue(t *testing.T, s *dolt.DoltStore, title, description string) string {
	t.Helper()
	issue := &types.Issue{Title: title, Description: description, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := s.CreateIssue(context.Background(), issue, "test"); err != nil {
		t.Fatalf("CreateIssue(%q): %v", title, err)
	}
	return issue.ID
}

// issueExists reports whether id is still in the issues table.
func issueExists(t *testing.T, s *dolt.DoltStore, id string) bool {
	t.Helper()
	var n int
	if err := s.DB().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&n); err != nil {
		t.Fatalf("count issue %s: %v", id, err)
	}
	return n > 0
}

// pollutionBackupIDs returns the issue IDs in the workspace's pollution
// backup, sorted, or nil when cleanup wrote no backup.
func pollutionBackupIDs(t *testing.T, dir string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, ".beads", "pollution-backup.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ids []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var issue types.Issue
		if err := json.Unmarshal(scanner.Bytes(), &issue); err != nil {
			t.Fatalf("backup line %q: %v", scanner.Text(), err)
		}
		ids = append(ids, issue.ID)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(ids)
	return ids
}

// TestDoctorPollutionClean_DeletesOnlyHighConfidence pins GH#5025: cleanup
// backs up and deletes only high-confidence hits. A medium-confidence hit (a
// single title signal) is review-only: it survives, and the output says so.
func TestDoctorPollutionClean_DeletesOnlyHighConfidence(t *testing.T) {
	tmpDir, s := pollutionCleanupWorkspace(t, "pcl")
	high := seedPollutionIssue(t, s, "Test issue", "")
	medium := seedPollutionIssue(t, s, "Debug sync hangs", "")

	out, code := bdRunRaw(t, buildBDForInitTests(t), tmpDir, doctorMaxRowsEnv(), "doctor", "--check=pollution", "--clean", "--yes")
	if code != 0 {
		t.Fatalf("bd doctor --check=pollution --clean --yes exited %d\n%s", code, out)
	}

	if issueExists(t, s, high) {
		t.Errorf("high-confidence hit %s survived cleanup\n%s", high, out)
	}
	if !issueExists(t, s, medium) {
		t.Errorf("medium-confidence hit %s was deleted; medium hits are review-only\n%s", medium, out)
	}
	if got := pollutionBackupIDs(t, tmpDir); !slices.Equal(got, []string{high}) {
		t.Errorf("backup IDs = %v, want exactly [%s]", got, high)
	}
	if !strings.Contains(out, medium) || !strings.Contains(out, "manual review") {
		t.Errorf("output does not explain that %s was left for manual review:\n%s", medium, out)
	}
}

// TestDoctorPollutionClean_NoHighConfidenceHits pins that cleanup with only
// medium-confidence hits deletes nothing and writes no backup, so an earlier
// backup is not truncated.
func TestDoctorPollutionClean_NoHighConfidenceHits(t *testing.T) {
	tmpDir, s := pollutionCleanupWorkspace(t, "pcn")
	medium := seedPollutionIssue(t, s, "Debug sync hangs", "")
	backupPath := filepath.Join(tmpDir, ".beads", "pollution-backup.jsonl")
	const earlierBackup = "{\"id\":\"pcn-earlier\"}\n"
	if err := os.WriteFile(backupPath, []byte(earlierBackup), 0o600); err != nil {
		t.Fatal(err)
	}

	out, code := bdRunRaw(t, buildBDForInitTests(t), tmpDir, doctorMaxRowsEnv(), "doctor", "--check=pollution", "--clean", "--yes")
	if code != 0 {
		t.Fatalf("bd doctor --check=pollution --clean --yes exited %d\n%s", code, out)
	}
	if !issueExists(t, s, medium) {
		t.Errorf("medium-confidence hit %s was deleted\n%s", medium, out)
	}
	if got, err := os.ReadFile(backupPath); err != nil || string(got) != earlierBackup {
		t.Errorf("earlier backup = %q (err %v), want it untouched %q; nothing was eligible for cleanup", got, err, earlierBackup)
	}
	if !strings.Contains(out, "No issues eligible for cleanup") {
		t.Errorf("output does not say nothing was eligible for cleanup:\n%s", out)
	}
}
