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
