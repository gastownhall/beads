package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestPollutionTitlePredicateKeepsPrefixSemantics pins the title predicate
// bd create (test-issue warning, description requirement) and bd export
// --scrub share with doctor: moving it into doctor.IsTestTitle must not change
// which titles it accepts (GH#5025 tightened only the pollution classifier).
func TestPollutionTitlePredicateKeepsPrefixSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		title string
		want  bool
	}{
		{"test-something", true},
		{"Test-quality architecture", true},
		{"TEST issue", true},
		{"benchmark-perf test", true},
		{"sample_data", true},
		{"tmp-throwaway", true},
		{"temp\tnote", true},
		{"Debug_log", true},
		{"dummy x", true},
		{"testing harness", false},
		{"test", false},
		{" test-leading-space", false},
		{"Fix test-driver.sh", false},
		{"Real feature request", false},
		{"Test Issue 1", true},
		{"issue for testing", false},
	}

	var issues []*types.Issue
	var kept []string
	for _, tt := range tests {
		if got := isTestIssue(tt.title); got != tt.want {
			t.Errorf("isTestIssue(%q) = %v, want %v", tt.title, got, tt.want)
		}
		if got := doctor.IsTestTitle(tt.title); got != tt.want {
			t.Errorf("doctor.IsTestTitle(%q) = %v, want %v", tt.title, got, tt.want)
		}
		issues = append(issues, &types.Issue{ID: tt.title, Title: tt.title})
		if !tt.want {
			kept = append(kept, tt.title)
		}
	}

	var got []string
	for _, issue := range filterOutPollution(issues) {
		got = append(got, issue.Title)
	}
	if len(got) != len(kept) {
		t.Fatalf("filterOutPollution kept %q, want %q", got, kept)
	}
	for i := range kept {
		if got[i] != kept[i] {
			t.Fatalf("filterOutPollution kept %q, want %q", got, kept)
		}
	}
}

// fakePollutionCleanupStore is a pollutionCleanupStore over an in-memory
// issue map; it records deletions and can fail any read.
type fakePollutionCleanupStore struct {
	issues      map[string]*types.Issue
	incoming    map[string][]*types.Dependency
	comments    map[string]int
	getErr      error
	outgoingErr error
	incomingErr error
	commentsErr error
	deleted     []string
}

func (f *fakePollutionCleanupStore) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	var out []*types.Issue
	for _, id := range ids {
		if issue, ok := f.issues[id]; ok {
			copied := *issue
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (f *fakePollutionCleanupStore) GetDependencyRecordsForIssues(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, f.outgoingErr
}

func (f *fakePollutionCleanupStore) GetDependentRecordsForIssues(context.Context, []string) (map[string][]*types.Dependency, error) {
	return f.incoming, f.incomingErr
}

func (f *fakePollutionCleanupStore) GetCommentCounts(context.Context, []string) (map[string]int, error) {
	return f.comments, f.commentsErr
}

func (f *fakePollutionCleanupStore) DeleteIssue(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.issues, id)
	return nil
}

// newPollutionCleanupFixture returns a store of pollution candidates and the
// matches a user confirmed for them: bd-1..bd-5 across both heuristic
// groups.
func newPollutionCleanupFixture(t *testing.T) (*fakePollutionCleanupStore, []doctor.PollutionMatch) {
	t.Helper()
	store := &fakePollutionCleanupStore{issues: map[string]*types.Issue{
		"bd-1": {ID: "bd-1", Title: "test-one", Status: types.StatusOpen, IssueType: types.TypeTask},
		"bd-2": {ID: "bd-2", Title: "tmp-two", Status: types.StatusOpen, IssueType: types.TypeTask},
		"bd-3": {ID: "bd-3", Title: "debug-three", Status: types.StatusOpen, IssueType: types.TypeTask},
		"bd-4": {ID: "bd-4", Title: "Test Issue 4", Status: types.StatusOpen, IssueType: types.TypeTask},
		"bd-5": {ID: "bd-5", Title: "dummy-five", Status: types.StatusOpen, IssueType: types.TypeTask},
		"bd-6": {ID: "bd-6", Title: "temp-six", Status: types.StatusOpen, IssueType: types.TypeTask},
	}}
	var all []*types.Issue
	for _, id := range []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-5"} {
		all = append(all, store.issues[id])
	}
	confirmed, err := doctor.DetectTestPollution(context.Background(), store, all)
	if err != nil {
		t.Fatalf("DetectTestPollution: %v", err)
	}
	if len(confirmed) != 5 || confirmed[0].Strong() || !confirmed[3].Strong() {
		t.Fatalf("fixture: confirmed %d matches, want 5 across both groups", len(confirmed))
	}
	return store, confirmed
}

func readPollutionBackupIDs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			t.Fatalf("backup line %q: %v", line, err)
		}
		ids = append(ids, issue.ID)
	}
	return ids
}

// TestPollutionCleanupRechecksConfirmedIssues pins GH#5025's cleanup
// contract: after confirmation, --clean re-reads the reported issues and
// deletes (and backs up) only those still eligible, reports the ones that
// changed, and never adds an issue that started to match meanwhile.
func TestPollutionCleanupRechecksConfirmedIssues(t *testing.T) {
	t.Parallel()

	store, confirmed := newPollutionCleanupFixture(t)
	// While the prompt waited: bd-2 was closed, bd-3 gained a dependent,
	// bd-5 was written up and bd-6 (never reported) still matches.
	store.issues["bd-2"].Status = types.StatusClosed
	store.incoming = map[string][]*types.Dependency{"bd-3": {{IssueID: "bd-9", DependsOnID: "bd-3", Type: types.DepBlocks}}}
	store.issues["bd-5"].Description = "Reproduces the login race; see the attached trace."

	var out bytes.Buffer
	backup := filepath.Join(t.TempDir(), "pollution-backup.jsonl")
	cleanup := pollutionCleanup{store: store, out: &out, backupPath: backup}
	if err := cleanup.run(context.Background(), confirmed); err != nil {
		t.Fatalf("run: %v", err)
	}

	if want := []string{"bd-1", "bd-4"}; !slices.Equal(store.deleted, want) {
		t.Fatalf("deleted %v, want %v", store.deleted, want)
	}
	if got, want := readPollutionBackupIDs(t, backup), []string{"bd-1", "bd-4"}; !slices.Equal(got, want) {
		t.Fatalf("backed up %v, want %v", got, want)
	}
	for _, id := range []string{"bd-2", "bd-3", "bd-5"} {
		if !strings.Contains(out.String(), "Skipping "+id+":") {
			t.Errorf("output does not report skipping %s:\n%s", id, out.String())
		}
	}
	if strings.Contains(out.String(), "bd-6") {
		t.Errorf("output mentions unreported bd-6:\n%s", out.String())
	}
}

// TestPollutionCleanupDeletedCandidateAndNothingLeft pins that a reported
// issue deleted meanwhile is skipped, and that with nothing left eligible
// cleanup neither backs up nor deletes.
func TestPollutionCleanupDeletedCandidateAndNothingLeft(t *testing.T) {
	t.Parallel()

	store, confirmed := newPollutionCleanupFixture(t)
	for _, id := range []string{"bd-1", "bd-2", "bd-3", "bd-4"} {
		delete(store.issues, id)
	}
	store.comments = map[string]int{"bd-5": 1}

	var out bytes.Buffer
	backup := filepath.Join(t.TempDir(), "pollution-backup.jsonl")
	cleanup := pollutionCleanup{store: store, out: &out, backupPath: backup}
	if err := cleanup.run(context.Background(), confirmed); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("deleted %v, want nothing", store.deleted)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup written with nothing to delete (stat err %v)", err)
	}
	if !strings.Contains(out.String(), "Skipping bd-1:") || !strings.Contains(out.String(), "nothing deleted") {
		t.Fatalf("output = %q, want skip report and nothing deleted", out.String())
	}
}

// TestPollutionCleanupErrorsDeleteNothing pins that --clean deletes nothing
// when it cannot re-read the confirmed issues, cannot read their dependency
// or comment evidence, or cannot write the backup.
func TestPollutionCleanupErrorsDeleteNothing(t *testing.T) {
	t.Parallel()

	failure := errors.New("store unavailable")
	tests := []struct {
		name      string
		breakIt   func(*fakePollutionCleanupStore)
		backupDir func(t *testing.T) string
		wantErr   string
	}{
		{"refresh", func(f *fakePollutionCleanupStore) { f.getErr = failure }, nil, "re-reading confirmed issues"},
		{"dependencies", func(f *fakePollutionCleanupStore) { f.outgoingErr = failure }, nil, "re-checking confirmed issues"},
		{"dependents", func(f *fakePollutionCleanupStore) { f.incomingErr = failure }, nil, "re-checking confirmed issues"},
		{"comments", func(f *fakePollutionCleanupStore) { f.commentsErr = failure }, nil, "re-checking confirmed issues"},
		{"backup", func(*fakePollutionCleanupStore) {}, func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "missing")
		}, "backing up issues"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, confirmed := newPollutionCleanupFixture(t)
			tt.breakIt(store)
			dir := t.TempDir()
			if tt.backupDir != nil {
				dir = tt.backupDir(t)
			}
			backup := filepath.Join(dir, "pollution-backup.jsonl")

			var out bytes.Buffer
			cleanup := pollutionCleanup{store: store, out: &out, backupPath: backup}
			err := cleanup.run(context.Background(), confirmed)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("run error = %v, want one containing %q", err, tt.wantErr)
			}
			if tt.name != "backup" && !errors.Is(err, failure) {
				t.Fatalf("run error = %v, want it to wrap %v", err, failure)
			}
			if len(store.deleted) != 0 {
				t.Fatalf("deleted %v after %s failure, want nothing", store.deleted, tt.name)
			}
			if _, statErr := os.Stat(backup); !os.IsNotExist(statErr) {
				t.Fatalf("backup written after %s failure (stat err %v)", tt.name, statErr)
			}
			if len(store.issues) != 6 {
				t.Fatalf("store holds %d issues after %s failure, want all 6", len(store.issues), tt.name)
			}
		})
	}
}

// TestPollutionReportWording pins GH#5025's human report: matches are
// grouped as stronger and possible heuristic matches, scores are not
// presented as probabilities, the next step is reviewing each match with
// bd show, and the report no longer invites bulk deletion with --clean.
func TestPollutionReportWording(t *testing.T) {
	t.Parallel()

	_, confirmed := newPollutionCleanupFixture(t)
	var stronger, possible []doctor.PollutionMatch
	for _, m := range confirmed {
		if m.Strong() {
			stronger = append(stronger, m)
		} else {
			possible = append(possible, m)
		}
	}

	var out bytes.Buffer
	writePollutionReport(&out, stronger, possible)
	report := out.String()

	for _, want := range []string{
		"Found 5 possible test leftovers",
		"Stronger heuristic matches (score ≥ 0.9):",
		"Possible heuristic matches (score < 0.9):",
		`bd-4: "Test Issue 4" (score: 1.20)`,
		`bd-1: "test-one" (score: 0.70)`,
		"not probabilities",
		"bd show <id>",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Index(report, "Stronger heuristic") > strings.Index(report, "Possible heuristic") {
		t.Errorf("stronger group is not listed first:\n%s", report)
	}
	for _, banned := range []string{"--clean", "Confidence", "delete these issues"} {
		if strings.Contains(report, banned) {
			t.Errorf("report contains %q:\n%s", banned, report)
		}
	}
}

// pollutionCommandStore drives runPollutionCheck without a database: the
// initial full scan returns initial, and the post-confirmation refresh
// (GetIssuesByIDs) returns only the rows still present in refresh, so a test
// can change an issue between report and refresh like a concurrent editor.
// Any method the pollution path should not touch panics via the nil
// embedded interface.
type pollutionCommandStore struct {
	storage.DoltStorage
	initial     []*types.Issue
	refresh     map[string]*types.Issue
	evidenceErr error
	deleted     []string
	refreshes   int
}

func (f *pollutionCommandStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return f.initial, nil
}

func (f *pollutionCommandStore) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	f.refreshes++
	var out []*types.Issue
	for _, id := range ids {
		if issue, ok := f.refresh[id]; ok {
			out = append(out, issue)
		}
	}
	return out, nil
}

func (f *pollutionCommandStore) DeleteIssue(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *pollutionCommandStore) GetDependencyRecordsForIssues(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, f.evidenceErr
}

func (f *pollutionCommandStore) GetDependentRecordsForIssues(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, f.evidenceErr
}

func (f *pollutionCommandStore) GetCommentCounts(context.Context, []string) (map[string]int, error) {
	return nil, f.evidenceErr
}

// newPollutionCommandFixture reports three candidates; by the refresh one
// is unchanged, one was closed and one was deleted.
func newPollutionCommandFixture() *pollutionCommandStore {
	newIssue := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "test leftover " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	}
	stays := newIssue("pol-stays")
	turnsClosed := newIssue("pol-closed")
	closedCopy := *turnsClosed
	closedCopy.Status = types.StatusClosed
	disappears := newIssue("pol-gone")
	return &pollutionCommandStore{
		initial: []*types.Issue{stays, turnsClosed, disappears},
		refresh: map[string]*types.Issue{stays.ID: stays, turnsClosed.ID: &closedCopy},
	}
}

// pollutionCommandTestEnv installs fake as the active store in a temp
// working directory (for the backup file).
func pollutionCommandTestEnv(t *testing.T, fake storage.DoltStorage) {
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
}

// answerPollutionPrompt feeds answer to the cleanup confirmation prompt
// through a stand-in stdin. The mutex guards only the swap: the capture
// helpers take it too, so holding it across the run would deadlock.
func answerPollutionPrompt(t *testing.T, answer string) {
	t.Helper()
	origStdin := os.Stdin
	stdinFile, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("creating stand-in stdin: %v", err)
	}
	if _, err := stdinFile.WriteString(answer + "\n"); err != nil {
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
}

// assertPollutionCommandCleanedFixture checks the fixture's cleanup: only the
// unchanged candidate is backed up and deleted, after one refresh, and the
// closed and deleted ones are reported as skipped.
func assertPollutionCommandCleanedFixture(t *testing.T, fake *pollutionCommandStore, out string) {
	t.Helper()
	if !slices.Equal(fake.deleted, []string{"pol-stays"}) {
		t.Errorf("deleted %v, want only [pol-stays] (protected and disappeared issues must be skipped)", fake.deleted)
	}
	if fake.refreshes != 1 {
		t.Errorf("post-confirmation refreshes = %d, want 1", fake.refreshes)
	}
	if got := readPollutionBackupIDs(t, ".beads/pollution-backup.jsonl"); !slices.Equal(got, []string{"pol-stays"}) {
		t.Errorf("backup held %v, want only [pol-stays] (the refreshed eligible rows)", got)
	}
	for _, skipped := range []string{"pol-closed", "pol-gone"} {
		if !strings.Contains(out, "Skipping "+skipped) {
			t.Errorf("cleanup output did not report skipped issue %s:\n%s", skipped, out)
		}
	}
}

// TestDoctorPollutionCleanRefreshesAfterConfirmation pins the
// post-confirmation refresh through the command (GH#5025): with --yes, the
// reported IDs are re-read and re-checked, so an issue closed or deleted
// since the report is skipped and reported, never deleted.
func TestDoctorPollutionCleanRefreshesAfterConfirmation(t *testing.T) {
	fake := newPollutionCommandFixture()
	pollutionCommandTestEnv(t, fake)

	out, err := captureStdoutWithError(t, func() error { return runPollutionCheck("", true, true) })
	if err != nil {
		t.Fatalf("runPollutionCheck: %v", err)
	}
	assertPollutionCommandCleanedFixture(t, fake, out)
}

// TestDoctorPollutionCleanConfirmedAtPromptRefreshes pins the same refresh
// when the user answers the prompt with "y" instead of passing --yes.
func TestDoctorPollutionCleanConfirmedAtPromptRefreshes(t *testing.T) {
	fake := newPollutionCommandFixture()
	pollutionCommandTestEnv(t, fake)
	answerPollutionPrompt(t, "y")

	out, err := captureStdoutWithError(t, func() error { return runPollutionCheck("", true, false) })
	if err != nil {
		t.Fatalf("runPollutionCheck: %v", err)
	}
	if !strings.Contains(out, "Delete the 3 reported issues?") {
		t.Errorf("cleanup did not prompt for the 3 reported issues:\n%s", out)
	}
	assertPollutionCommandCleanedFixture(t, fake, out)
}

// TestDoctorPollutionCleanDeclinedIsNonDestructive pins that a declined
// confirmation re-reads nothing, deletes nothing and writes no backup.
func TestDoctorPollutionCleanDeclinedIsNonDestructive(t *testing.T) {
	fake := newPollutionCommandFixture()
	pollutionCommandTestEnv(t, fake)
	answerPollutionPrompt(t, "n")

	out, err := captureStdoutWithError(t, func() error { return runPollutionCheck("", true, false) })
	if err != nil {
		t.Fatalf("runPollutionCheck: %v", err)
	}
	if !strings.Contains(out, "Canceled.") {
		t.Errorf("declined cleanup did not report cancellation:\n%s", out)
	}
	if len(fake.deleted) != 0 || fake.refreshes != 0 {
		t.Errorf("declined cleanup deleted %v after %d refreshes, want nothing", fake.deleted, fake.refreshes)
	}
	if _, err := os.Stat(".beads/pollution-backup.jsonl"); err == nil {
		t.Error("declined cleanup wrote a backup file, want none")
	}
}

// TestDoctorPollutionJSONReturnsBeforeClean pins the JSON mode's early
// return: with --json the report is emitted and cleanup never runs, even
// with --clean --yes.
func TestDoctorPollutionJSONReturnsBeforeClean(t *testing.T) {
	fake := newPollutionCommandFixture()
	pollutionCommandTestEnv(t, fake)
	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	out, err := captureStdoutWithError(t, func() error { return runPollutionCheck("", true, true) })
	if err != nil {
		t.Fatalf("runPollutionCheck: %v", err)
	}
	if len(fake.deleted) != 0 || fake.refreshes != 0 {
		t.Errorf("JSON mode deleted %v after %d refreshes, want nothing (JSON returns before cleanup)", fake.deleted, fake.refreshes)
	}
	if !strings.Contains(out, "\"polluted_count\": 3") {
		t.Errorf("JSON report missing polluted_count 3:\n%s", out)
	}
}

// TestDoctorPollutionCleanEvidenceFailureDeletesNothing pins that an
// evidence-query failure during the initial detection fails the command
// before any prompt, backup or deletion, rather than reporting a clean bill
// of health.
func TestDoctorPollutionCleanEvidenceFailureDeletesNothing(t *testing.T) {
	fake := newPollutionCommandFixture()
	fake.evidenceErr = errors.New("evidence store unreachable")
	pollutionCommandTestEnv(t, fake)

	out, err := captureStdoutWithError(t, func() error { return runPollutionCheck("", true, true) })
	if err == nil {
		t.Error("cleanup with a failing evidence query returned nil, want an error")
	}
	if strings.Contains(out, "No test pollution detected") {
		t.Errorf("failing evidence query reported a clean result:\n%s", out)
	}
	if len(fake.deleted) != 0 || fake.refreshes != 0 {
		t.Errorf("cleanup with a failing evidence query deleted %v after %d refreshes, want nothing", fake.deleted, fake.refreshes)
	}
	if _, statErr := os.Stat(".beads/pollution-backup.jsonl"); statErr == nil {
		t.Error("cleanup with a failing evidence query wrote a backup, want none")
	}
}
