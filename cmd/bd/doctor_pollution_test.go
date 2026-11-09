//go:build cgo

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

// pollutionEdgeCount returns how many dependency records, outgoing and
// incoming, touch ids.
func pollutionEdgeCount(t *testing.T, s *dolt.DoltStore, ids []string) int {
	t.Helper()
	ctx := context.Background()
	out, err := s.GetDependencyRecordsForIssues(ctx, ids)
	if err != nil {
		t.Fatalf("GetDependencyRecordsForIssues: %v", err)
	}
	in, err := s.GetDependentRecordsForIssues(ctx, ids)
	if err != nil {
		t.Fatalf("GetDependentRecordsForIssues: %v", err)
	}
	n := 0
	for _, deps := range out {
		n += len(deps)
	}
	for _, deps := range in {
		n += len(deps)
	}
	return n
}

// TestDoctorPollutionClean_SkipsLinkedHighConfidence pins GH#5025: the
// backup holds issue rows only, so deleting a hit that has dependency
// records would lose edges no restore brings back. A high-confidence hit
// with any incoming or outgoing record, of any type and including an
// ephemeral dependent, is neither backed up nor deleted, its edges stay,
// and the output says the links prevented cleanup.
func TestDoctorPollutionClean_SkipsLinkedHighConfidence(t *testing.T) {
	tmpDir, s := pollutionCleanupWorkspace(t, "pck")
	ctx := context.Background()

	unlinked := seedPollutionIssue(t, s, "Test issue", "")
	real := seedPollutionIssue(t, s, "Ship the billing export", "Customers need monthly CSV exports.")
	epic := &types.Issue{Title: "Billing", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic}
	wisp := &types.Issue{Title: "Patrol step", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true}
	for _, issue := range []*types.Issue{epic, wisp} {
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%q): %v", issue.Title, err)
		}
	}

	linked := map[string]string{} // hit ID -> how it is linked
	link := func(how string, edge func(hit string) *types.Dependency) {
		hit := seedPollutionIssue(t, s, "Test issue", "")
		if err := s.AddDependency(ctx, edge(hit), "test"); err != nil {
			t.Fatalf("AddDependency (%s): %v", how, err)
		}
		linked[hit] = how
	}
	link("outgoing blocks", func(hit string) *types.Dependency {
		return &types.Dependency{IssueID: hit, DependsOnID: real, Type: types.DepBlocks}
	})
	link("incoming blocks", func(hit string) *types.Dependency {
		return &types.Dependency{IssueID: real, DependsOnID: hit, Type: types.DepBlocks}
	})
	link("child of an epic", func(hit string) *types.Dependency {
		return &types.Dependency{IssueID: hit, DependsOnID: epic.ID, Type: types.DepParentChild}
	})
	link("outgoing related", func(hit string) *types.Dependency {
		return &types.Dependency{IssueID: hit, DependsOnID: real, Type: types.DepRelated}
	})
	link("incoming discovered-from", func(hit string) *types.Dependency {
		return &types.Dependency{IssueID: real, DependsOnID: hit, Type: types.DepDiscoveredFrom}
	})
	link("incoming from an ephemeral wisp", func(hit string) *types.Dependency {
		return &types.Dependency{IssueID: wisp.ID, DependsOnID: hit, Type: types.DepBlocks}
	})

	var linkedIDs []string
	for id := range linked {
		linkedIDs = append(linkedIDs, id)
	}
	edgesBefore := pollutionEdgeCount(t, s, linkedIDs)
	if edgesBefore != len(linked) {
		t.Fatalf("seeded %d edges on linked hits, read back %d", len(linked), edgesBefore)
	}

	out, code := bdRunRaw(t, buildBDForInitTests(t), tmpDir, doctorMaxRowsEnv(), "doctor", "--check=pollution", "--clean", "--yes")
	if code != 0 {
		t.Fatalf("bd doctor --check=pollution --clean --yes exited %d\n%s", code, out)
	}

	if issueExists(t, s, unlinked) {
		t.Errorf("unlinked high-confidence hit %s survived cleanup\n%s", unlinked, out)
	}
	for id, how := range linked {
		if !issueExists(t, s, id) {
			t.Errorf("hit %s (%s) was deleted; dependency links must prevent cleanup", id, how)
		}
		if !strings.Contains(out, "Skipping "+id+": dependency links") {
			t.Errorf("output does not say dependency links prevented cleanup of %s (%s)", id, how)
		}
	}
	if got := pollutionBackupIDs(t, tmpDir); !slices.Equal(got, []string{unlinked}) {
		t.Errorf("backup IDs = %v, want exactly [%s]", got, unlinked)
	}
	if got := pollutionEdgeCount(t, s, linkedIDs); got != edgesBefore {
		t.Errorf("edges on linked hits after cleanup = %d, want %d unchanged", got, edgesBefore)
	}
	if t.Failed() {
		t.Logf("output:\n%s", out)
	}
}

// fakePollutionEdges serves canned dependency records, or an error, to
// selectPollutionCleanup.
type fakePollutionEdges struct {
	out, in       map[string][]*types.Dependency
	outErr, inErr error
	asked         [][]string
}

func (f *fakePollutionEdges) GetDependencyRecordsForIssues(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	f.asked = append(f.asked, ids)
	return f.out, f.outErr
}

func (f *fakePollutionEdges) GetDependentRecordsForIssues(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	f.asked = append(f.asked, ids)
	return f.in, f.inErr
}

// TestSelectPollutionCleanup_LinkLookup pins the link guard's selection:
// only high-confidence hits are looked up, a hit with an outgoing or an
// incoming record is skipped, and a failed lookup in either direction
// selects nothing, so cleanup stops before any prompt, backup or delete.
func TestSelectPollutionCleanup_LinkLookup(t *testing.T) {
	hit := func(id string, score float64) pollutionResult {
		return pollutionResult{issue: &types.Issue{ID: id}, score: score}
	}
	polluted := []pollutionResult{hit("h-free", 1.4), hit("h-out", 1.4), hit("h-in", 1.3), hit("m-1", 0.8)}
	edge := func(from, to string) []*types.Dependency {
		return []*types.Dependency{{IssueID: from, DependsOnID: to, Type: types.DepRelated}}
	}

	edges := &fakePollutionEdges{
		out: map[string][]*types.Dependency{"h-out": edge("h-out", "ext:other:x")},
		in:  map[string][]*types.Dependency{"h-in": edge("wisp-1", "h-in")},
	}
	deletable, skipped, err := selectPollutionCleanup(context.Background(), edges, polluted)
	if err != nil {
		t.Fatalf("selectPollutionCleanup: %v", err)
	}
	if len(deletable) != 1 || deletable[0].issue.ID != "h-free" {
		t.Errorf("deletable = %v, want only h-free", deletable)
	}
	reasons := map[string]string{}
	for _, skip := range skipped {
		reasons[skip.issue.ID] = skip.reason
	}
	for _, id := range []string{"h-out", "h-in"} {
		if !strings.HasPrefix(reasons[id], "dependency links") {
			t.Errorf("%s skip reason = %q, want dependency links", id, reasons[id])
		}
	}
	if !strings.Contains(reasons["m-1"], "manual review") {
		t.Errorf("m-1 skip reason = %q, want manual review", reasons["m-1"])
	}
	for _, ids := range edges.asked {
		if !slices.Equal(ids, []string{"h-free", "h-out", "h-in"}) {
			t.Errorf("looked up %v, want the high-confidence hits only", ids)
		}
	}

	for _, broken := range []*fakePollutionEdges{
		{outErr: errors.New("outbound read failed")},
		{inErr: errors.New("inbound read failed")},
	} {
		deletable, _, err := selectPollutionCleanup(context.Background(), broken, polluted)
		if err == nil || len(deletable) != 0 {
			t.Errorf("lookup failure (%v/%v): deletable %v, err %v; want nothing selected and an error", broken.outErr, broken.inErr, deletable, err)
		}
	}
}
