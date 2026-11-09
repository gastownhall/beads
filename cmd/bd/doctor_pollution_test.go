package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestDetectTestPollutionSparesLegitimateWork pins GH#5025: substantive work
// about test infrastructure — and an open P1 epic whose title merely starts
// with "Test-" that was bulk-imported alongside other issues — must not be
// flagged as test pollution. The old scorer gave the epic 1.00 and offered
// --clean deletion on the strength of a title word plus a bulk-import
// timestamp. The fixture ran at the old detector seam at base (see the
// triage repro); it now runs against the shared classifier.
func TestDetectTestPollutionSparesLegitimateWork(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	epic := &types.Issue{
		ID:          "dcr-9f3",
		Title:       "Test-quality architecture",
		Description: "Restructure the component test suite so each layer owns its fixtures and assertions.",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		CreatedAt:   created,
	}
	// Real engineering work about the project's own test-infrastructure
	// scripts — exactly the kind of title a healthy repo produces.
	testInfraWork := []*types.Issue{
		{ID: "dcr-lzse", Title: "test-driver.sh isolation broken — constants.sh clobbers sibling runs", Description: "constants.sh writes a fixed path shared by parallel test-driver invocations; scope it per run.", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeBug, CreatedAt: created},
		{ID: "dcr-3t0", Title: "test-agents-md-currency.sh asserts the retired wording", Description: "The script still greps for the pre-1.0 wording of AGENTS.md; update it to the current section names.", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeBug, CreatedAt: created},
		{ID: "dcr-dk2n", Title: "test-check-19-spec-a11y-gating.sh scatters advisory residue on failure", Description: "On failure the script leaves advisory .residue files in the repo root; clean them up in the failure path.", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeBug, CreatedAt: created},
		{ID: "dcr-3db", Title: "test-shellcheck.sh teeth fixtures use fixed repo paths", Description: "The fixture scripts hardcode /tmp/teeth; derive the paths from the test's own temp dir.", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeBug, CreatedAt: created},
	}
	issues := append([]*types.Issue{epic}, testInfraWork...)

	// A bulk import created ten more issues in the same minute — an import
	// signature, not pollution evidence.
	for i := 0; i < 10; i++ {
		issues = append(issues, &types.Issue{
			ID:          fmt.Sprintf("dcr-imp%d", i),
			Title:       fmt.Sprintf("Imported report finding %d", i),
			Description: "Derived from the audit report; see linked findings.",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeBug,
			CreatedAt:   created.Add(time.Duration(i) * time.Second),
		})
	}

	if matches := doctor.ClassifyTestPollution(issues); len(matches) != 0 {
		for _, m := range matches {
			t.Errorf("legitimate issue %s %q flagged as test pollution (score %.2f, reasons %v)", m.Issue.ID, m.Issue.Title, m.Score, m.Reasons)
		}
	}
}

// fakePollutionCleanupStore drives runPollutionCheck without a database: the
// initial unrestricted scan returns initial, and the post-confirmation reload
// (an IDs filter) returns only the rows still present in refresh, so a test
// can mutate an issue between report and refresh exactly like a concurrent
// editor would. Any method the pollution path should not touch panics via
// the nil embedded interface.
type fakePollutionCleanupStore struct {
	storage.DoltStorage
	initial []*types.Issue
	refresh map[string]*types.Issue
	deleted []string
	reloads int
	scans   int
}

func (f *fakePollutionCleanupStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	if len(filter.IDs) > 0 {
		f.reloads++
		var out []*types.Issue
		for _, id := range filter.IDs {
			if issue, ok := f.refresh[id]; ok {
				out = append(out, issue)
			}
		}
		return out, nil
	}
	f.scans++
	return f.initial, nil
}

func (f *fakePollutionCleanupStore) DeleteIssue(_ context.Context, issueID string) error {
	f.deleted = append(f.deleted, issueID)
	return nil
}

func (f *fakePollutionCleanupStore) GetDependencyRecordsForIssues(_ context.Context, _ []string) (map[string][]*types.Dependency, error) {
	return map[string][]*types.Dependency{}, nil
}

func (f *fakePollutionCleanupStore) GetDependentRecordsForIssues(_ context.Context, _ []string) (map[string][]*types.Dependency, error) {
	return map[string][]*types.Dependency{}, nil
}

func (f *fakePollutionCleanupStore) GetCommentCounts(_ context.Context, _ []string) (map[string]int, error) {
	return map[string]int{}, nil
}

// pollutionCleanupTestEnv installs fake as the active store in a temp working
// directory (for the backup file) and returns the fake.
func pollutionCleanupTestEnv(t *testing.T, fake *fakePollutionCleanupStore) *fakePollutionCleanupStore {
	t.Helper()
	saveAndRestoreGlobals(t)
	store = fake
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	origWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWd) })
	return fake
}

// backupIDs reads the JSONL cleanup backup and returns the issue IDs it holds.
func backupIDs(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(".beads/pollution-backup.jsonl")
	if err != nil {
		t.Fatalf("reading pollution backup: %v", err)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var row struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("parsing backup line %q: %v", line, err)
		}
		ids = append(ids, row.ID)
	}
	return ids
}

// TestDoctorPollutionCleanRefreshesAfterConfirmation pins the post-confirmation
// refresh (GH#5025): after the user confirms cleanup, the originally reported
// IDs are reloaded and reclassified, so an issue that became protected (closed)
// or disappeared between report and refresh is skipped and reported, never
// deleted, and never joined by newly discovered IDs.
func TestDoctorPollutionCleanRefreshesAfterConfirmation(t *testing.T) {
	newIssue := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "test leftover " + id, Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	stays := newIssue("pol-stays")
	turnsClosed := newIssue("pol-closed")
	closedCopy := *turnsClosed
	closedCopy.Status = types.StatusClosed
	disappears := newIssue("pol-gone")

	fake := pollutionCleanupTestEnv(t, &fakePollutionCleanupStore{
		initial: []*types.Issue{stays, turnsClosed, disappears},
		refresh: map[string]*types.Issue{stays.ID: stays, turnsClosed.ID: &closedCopy},
	})

	out := captureStdout(t, func() error { return runPollutionCheck("", true, true) })

	if len(fake.deleted) != 1 || fake.deleted[0] != stays.ID {
		t.Errorf("deleted %v, want only [%s] (protected and disappeared issues must be skipped)", fake.deleted, stays.ID)
	}
	if fake.reloads != 1 {
		t.Errorf("post-confirmation reloads = %d, want 1", fake.reloads)
	}
	if got := backupIDs(t); len(got) != 1 || got[0] != stays.ID {
		t.Errorf("backup held %v, want only [%s] (the refreshed eligible rows)", got, stays.ID)
	}
	for _, skipped := range []string{turnsClosed.ID, disappears.ID} {
		if !strings.Contains(out, skipped) {
			t.Errorf("cleanup output did not report skipped issue %s:\n%s", skipped, out)
		}
	}
}

// TestDoctorPollutionCleanDeclinedIsNonDestructive pins that a declined
// confirmation deletes nothing and writes no backup.
func TestDoctorPollutionCleanDeclinedIsNonDestructive(t *testing.T) {
	newIssue := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "test leftover " + id, Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	candidate := newIssue("pol-decline")
	fake := pollutionCleanupTestEnv(t, &fakePollutionCleanupStore{
		initial: []*types.Issue{candidate},
		refresh: map[string]*types.Issue{candidate.ID: candidate},
	})

	// Answer the confirmation prompt with "n" through a stand-in stdin.
	// The mutex guards only the swap: captureStdout takes it too, so
	// holding it across the run would deadlock.
	origStdin := os.Stdin
	stdinFile, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("creating stand-in stdin: %v", err)
	}
	if _, err := stdinFile.WriteString("n\n"); err != nil {
		t.Fatalf("writing stand-in stdin: %v", err)
	}
	if _, err := stdinFile.Seek(0, 0); err != nil {
		t.Fatalf("rewinding stand-in stdin: %v", err)
	}
	stdioMutex.Lock()
	os.Stdin = stdinFile
	stdioMutex.Unlock()
	t.Cleanup(func() {
		stdioMutex.Lock()
		os.Stdin = origStdin
		stdioMutex.Unlock()
	})

	_ = captureStdout(t, func() error { return runPollutionCheck("", true, false) })

	if len(fake.deleted) != 0 {
		t.Errorf("declined cleanup deleted %v, want nothing", fake.deleted)
	}
	if _, err := os.Stat(".beads/pollution-backup.jsonl"); err == nil {
		t.Error("declined cleanup wrote a backup file, want none")
	}
}

// TestDoctorPollutionJSONReturnsBeforeClean pins the JSON mode's early
// return: with --json the report is emitted and cleanup never runs, even
// with --clean --yes.
func TestDoctorPollutionJSONReturnsBeforeClean(t *testing.T) {
	newIssue := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "test leftover " + id, Description: "", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	candidate := newIssue("pol-json")
	fake := pollutionCleanupTestEnv(t, &fakePollutionCleanupStore{
		initial: []*types.Issue{candidate},
		refresh: map[string]*types.Issue{candidate.ID: candidate},
	})
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })

	out := captureStdout(t, func() error { return runPollutionCheck("", true, true) })

	if len(fake.deleted) != 0 {
		t.Errorf("JSON mode deleted %v, want nothing (JSON returns before cleanup)", fake.deleted)
	}
	if !strings.Contains(out, "\"polluted_count\": 1") {
		t.Errorf("JSON report missing polluted_count:\n%s", out)
	}
}
