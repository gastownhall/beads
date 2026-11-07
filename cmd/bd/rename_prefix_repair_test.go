//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestRepairMultiplePrefixes(t *testing.T) {
	tmpDir := t.TempDir()
	testDBPath := filepath.Join(tmpDir, "test.db")

	ctx := context.Background()

	testStore := newTestStore(t, testDBPath)

	// Set the globals this test's code path actually reads. Deliberately NOT
	// dbPath: on the shared-branch fast path newTestStore writes a
	// metadata.json naming the shared database with no branch qualifier
	// (test_helpers_test.go newTestStoreSharedBranch) while the live store sits
	// on this test's own branch, so a global dbPath would point anything that
	// reopens by path at shared `main` instead. repairPrefixes takes the store
	// explicitly and never reopens, so the global is left alone rather than
	// made to lie.
	oldStore := store
	oldActor := actor
	store = testStore
	actor = "test"
	defer func() {
		store = oldStore
		actor = oldActor
	}()

	// Create issues with multiple prefixes (simulating corruption).
	// CreateIssue accepts explicit IDs without prefix validation,
	// so we can create issues with different prefixes to simulate
	// a corrupted database state.
	testIssues := []types.Issue{
		{ID: "test-1", Title: "Test issue 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-2", Title: "Test issue 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "old-1", Title: "Old issue 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "old-2", Title: "Old issue 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "another-1", Title: "Another issue 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}

	for i := range testIssues {
		if err := testStore.CreateIssue(ctx, &testIssues[i], "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", testIssues[i].ID, err)
		}
	}

	// Verify we have multiple prefixes
	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	prefixes := detectPrefixes(allIssues)
	if len(prefixes) != 3 {
		t.Fatalf("expected 3 prefixes, got %d: %v", len(prefixes), prefixes)
	}

	// Test repair — now uses UpdateIssueID (Dolt rename semantics)
	// instead of the old CreateIssue+DeleteIssue approach that caused deadlocks
	if err := repairPrefixes(ctx, testStore, "test", "test", allIssues, prefixes, false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	// Verify all issues now have correct prefix
	allIssues, err = testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues after repair: %v", err)
	}

	prefixes = detectPrefixes(allIssues)
	if len(prefixes) != 1 {
		t.Fatalf("expected 1 prefix after repair, got %d: %v", len(prefixes), prefixes)
	}

	if _, ok := prefixes["test"]; !ok {
		t.Fatalf("expected prefix 'test', got %v", prefixes)
	}

	// Verify the original test-1 and test-2 are unchanged
	for _, id := range []string{"test-1", "test-2"} {
		issue, err := testStore.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("expected issue %s to exist unchanged: %v", id, err)
		}
		if issue == nil {
			t.Fatalf("expected issue %s to exist", id)
		}
	}

	// Verify total count: 2 original (test-1, test-2) + 3 renamed = 5
	if len(allIssues) != 5 {
		t.Fatalf("expected 5 issues total, got %d", len(allIssues))
	}

	// Count issues with correct prefix
	testPrefixCount := 0
	for _, issue := range allIssues {
		if len(issue.ID) > 5 && issue.ID[:5] == "test-" {
			testPrefixCount++
		}
	}
	if testPrefixCount != 5 {
		t.Fatalf("expected all 5 issues to have 'test-' prefix, got %d", testPrefixCount)
	}

	// Verify old IDs no longer exist
	for _, oldID := range []string{"old-1", "old-2", "another-1"} {
		issue, err := testStore.GetIssue(ctx, oldID)
		if err == nil && issue != nil {
			t.Fatalf("expected old ID %s to no longer exist", oldID)
		}
	}
}

func TestRepairPrefixesRetargetsBeadGates(t *testing.T) {
	// The repair gives every off-prefix bead a new ID. A bead gate waiting on
	// one must follow it, sighting included, or its next check would read the
	// rename as a deletion and resolve. That holds whether the gate itself is
	// renamed before its bead, after it, or not at all.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

	for _, issue := range []*types.Issue{
		{ID: "test-1", Title: "Kept bead", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "old-1", Title: "Renamed bead", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-gate", Title: "Gate on the renamed bead", Status: types.StatusOpen, Priority: 2, IssueType: "gate",
			AwaitType: "bead", AwaitID: "old-1", Metadata: json.RawMessage(`{"await_seen":"old-1"}`)},
		{ID: "old-gate", Title: "Renamed gate on the kept bead", Status: types.StatusOpen, Priority: 2, IssueType: "gate",
			AwaitType: "bead", AwaitID: "test-1", Metadata: json.RawMessage(`{"await_seen":"test-1"}`)},
		// The repair renames in ID-number order: old-0, old-1, old-2.
		{ID: "old-0", Title: "Gate renamed before its bead", Status: types.StatusOpen, Priority: 2, IssueType: "gate",
			AwaitType: "bead", AwaitID: "old-1", Metadata: json.RawMessage(`{"await_seen":"old-1"}`)},
		{ID: "old-2", Title: "Gate renamed after its bead", Status: types.StatusOpen, Priority: 2, IssueType: "gate",
			AwaitType: "bead", AwaitID: "old-1", Metadata: json.RawMessage(`{"await_seen":"old-1"}`)},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}

	allIssues, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}
	if err := repairPrefixes(ctx, testStore, "test", "test", allIssues, detectPrefixes(allIssues), false); err != nil {
		t.Fatalf("repair failed: %v", err)
	}

	allIssues, err = testStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues after repair: %v", err)
	}
	idByTitle := make(map[string]string, len(allIssues))
	for _, issue := range allIssues {
		idByTitle[issue.Title] = issue.ID
	}
	renamedBead := idByTitle["Renamed bead"]
	if renamedBead == "" || renamedBead == "old-1" {
		t.Fatalf("old-1 was not renamed: %v", idByTitle)
	}

	for _, tt := range []struct {
		title       string
		wantAwaitID string
	}{
		{title: "Gate on the renamed bead", wantAwaitID: renamedBead},
		{title: "Renamed gate on the kept bead", wantAwaitID: "test-1"},
		{title: "Gate renamed before its bead", wantAwaitID: renamedBead},
		{title: "Gate renamed after its bead", wantAwaitID: renamedBead},
	} {
		gate, err := testStore.GetIssue(ctx, idByTitle[tt.title])
		if err != nil {
			t.Fatalf("%s: %v", tt.title, err)
		}
		if gate.AwaitID != tt.wantAwaitID || !beadGateTargetSeen(gate) {
			t.Errorf("%s: await_id=%q metadata=%s, want %q recorded as seen", tt.title, gate.AwaitID, gate.Metadata, tt.wantAwaitID)
		}
	}
}

// failingRenameStore fails UpdateIssueID for failID. With landed, the rename
// is applied before the error is returned, like a commit whose response was
// lost.
type failingRenameStore struct {
	storage.DoltStorage
	failID   string
	landed   bool
	injected bool
}

func (s *failingRenameStore) UpdateIssueID(ctx context.Context, oldID, newID string, issue *types.Issue, actor string) error {
	if oldID != s.failID {
		return s.DoltStorage.UpdateIssueID(ctx, oldID, newID, issue, actor)
	}
	if s.landed {
		if err := s.DoltStorage.UpdateIssueID(ctx, oldID, newID, issue, actor); err != nil {
			return err
		}
	}
	s.injected = true
	return errors.New("injected UpdateIssueID failure")
}

// withRenameStore points the store and actor globals at st while run runs.
func withRenameStore(st storage.DoltStorage, run func() error) error {
	oldStore, oldActor := store, actor
	store, actor = st, "test"
	storeMutex.Lock()
	oldActive := storeActive
	storeActive = true
	storeMutex.Unlock()
	defer func() {
		store, actor = oldStore, oldActor
		storeMutex.Lock()
		storeActive = oldActive
		storeMutex.Unlock()
	}()
	return run()
}

func TestRenameFailureLeavesBeadGatesPending(t *testing.T) {
	// When renaming the awaited bead fails, whether or not that rename
	// landed, its gate is pointed back at the bead's old ID without a
	// sighting: bd gate check must find it pending, never resolve it as
	// though the bead had been deleted. The prefix renames rename the gate
	// first, so they move it back under its new ID.
	for _, tt := range []struct {
		name        string
		gateRenamed bool
		run         func(ctx context.Context, st storage.DoltStorage, issues []*types.Issue) error
	}{
		{name: "rename", run: func(_ context.Context, st storage.DoltStorage, _ []*types.Issue) error {
			return withRenameStore(st, func() error { return runRename(renameCmd, []string{"old-1", "old-renamed"}) })
		}},
		{name: "repair", gateRenamed: true, run: func(ctx context.Context, st storage.DoltStorage, issues []*types.Issue) error {
			return repairPrefixes(ctx, st, "test", "test", issues, detectPrefixes(issues), false)
		}},
		{name: "rename-prefix", gateRenamed: true, run: func(ctx context.Context, st storage.DoltStorage, issues []*types.Issue) error {
			return withRenameStore(st, func() error { return renamePrefixInDB(ctx, "old", "test", issues) })
		}},
	} {
		for _, landed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/landed=%v", tt.name, landed), func(t *testing.T) {
				ctx := context.Background()
				testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
				issues := []*types.Issue{
					{ID: "old-gate", Title: "Gate", Status: types.StatusOpen, Priority: 2, IssueType: "gate",
						AwaitType: "bead", AwaitID: "old-1", Metadata: json.RawMessage(`{"await_seen":"old-1"}`)},
					{ID: "old-1", Title: "Awaited bead", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
				}
				for _, issue := range issues {
					if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
						t.Fatalf("failed to create issue %s: %v", issue.ID, err)
					}
				}

				st := &failingRenameStore{DoltStorage: testStore, failID: "old-1", landed: landed}
				if err := tt.run(ctx, st, issues); err == nil || !st.injected {
					t.Fatalf("rename error = %v (failure injected: %v), want the injected failure", err, st.injected)
				}

				all, err := testStore.SearchIssues(ctx, "", types.IssueFilter{})
				if err != nil {
					t.Fatalf("failed to search issues: %v", err)
				}
				var gate *types.Issue
				for _, issue := range all {
					if issue.Title == "Gate" {
						gate = issue
					}
				}
				if gate == nil || (gate.ID != "old-gate") != tt.gateRenamed {
					t.Fatalf("gate after the failed rename: %+v, want it renamed: %v", gate, tt.gateRenamed)
				}
				if gate.AwaitID != "old-1" || beadGateTargetSeen(gate) {
					t.Errorf("gate after the failed rename: await_id=%q metadata=%s, want %q without a sighting", gate.AwaitID, gate.Metadata, "old-1")
				}
				resolved, reason, err := evaluateBeadGate(ctx, gate, testStore, nil)
				if err != nil || resolved {
					t.Errorf("bd gate check on the gate: resolved=%v reason=%q err=%v, want it pending", resolved, reason, err)
				}
			})
		}
	}
}

func TestRenameRewritesCloseReasonAndOwnText(t *testing.T) {
	// GH#7040: bd rename left other issues' close_reason and the renamed
	// issue's own text naming the old ID, and rewrote a dotted child's ID that
	// starts with it. Rewriting a close_reason must leave the closed issue
	// closed, at its old closed_at.
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

	for _, issue := range []*types.Issue{
		{ID: "test-a", Title: "A (test-a)", Description: "test-a tracks test-a.1; test-a,test-a; see test-a.", Design: "test-a design",
			Notes: "notes on test-a", AcceptanceCriteria: "test-a done", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}
	if err := testStore.CloseIssue(ctx, "test-b", "duplicate of test-a", "test", ""); err != nil {
		t.Fatalf("failed to close test-b: %v", err)
	}
	if err := testStore.CloseIssue(ctx, "test-a", "test-a is done", "test", ""); err != nil {
		t.Fatalf("failed to close test-a: %v", err)
	}
	closedB, err := testStore.GetIssue(ctx, "test-b")
	if err != nil || closedB.ClosedAt == nil {
		t.Fatalf("test-b after close: %+v, %v; want a closed_at", closedB, err)
	}

	a, err := testStore.GetIssue(ctx, "test-a")
	if err != nil {
		t.Fatalf("failed to get test-a: %v", err)
	}
	if err := renameIssueKeepingBeadGates(ctx, testStore, a, "test-z", nil, "test"); err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	if report := updateReferencesInAllIssues(ctx, testStore, "test-a", "test-z", "test"); len(report.failed)+len(report.unchecked) > 0 {
		t.Fatalf("reference update failed: %+v", report)
	}

	z, err := testStore.GetIssue(ctx, "test-z")
	if err != nil {
		t.Fatalf("failed to get test-z: %v", err)
	}
	for _, f := range []struct{ field, got, want string }{
		{"title", z.Title, "A (test-z)"},
		{"description", z.Description, "test-z tracks test-a.1; test-z,test-z; see test-z."},
		{"design", z.Design, "test-z design"},
		{"notes", z.Notes, "notes on test-z"},
		{"acceptance_criteria", z.AcceptanceCriteria, "test-z done"},
		{"close_reason", z.CloseReason, "test-z is done"},
	} {
		if f.got != f.want {
			t.Errorf("renamed issue %s = %q, want %q", f.field, f.got, f.want)
		}
	}
	b, err := testStore.GetIssue(ctx, "test-b")
	if err != nil {
		t.Fatalf("failed to get test-b: %v", err)
	}
	if want := "duplicate of test-z"; b.CloseReason != want {
		t.Errorf("test-b close_reason = %q, want %q", b.CloseReason, want)
	}
	if b.Status != types.StatusClosed {
		t.Errorf("test-b status = %q, want closed", b.Status)
	}
	if b.ClosedAt == nil || !b.ClosedAt.Equal(*closedB.ClosedAt) {
		t.Errorf("test-b closed_at = %v, want it unchanged at %v", b.ClosedAt, *closedB.ClosedAt)
	}
}

// failingUpdateStore applies the UpdateIssue of failID and then fails it, like
// a commit whose response was lost.
type failingUpdateStore struct {
	storage.DoltStorage
	failID string
}

func (s *failingUpdateStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if err := s.DoltStorage.UpdateIssue(ctx, id, updates, actor); err != nil || id != s.failID {
		return err
	}
	return errors.New("injected UpdateIssue failure")
}

// runRenameOutput runs bd rename old new against st and returns the lines it
// printed after its "Renamed old -> new" line.
func runRenameOutput(t *testing.T, st storage.DoltStorage, oldID, newID string) []string {
	t.Helper()
	oldRootCtx, oldWrote := rootCtx, commandDidWrite.Load()
	rootCtx = context.Background()
	defer func() { rootCtx = oldRootCtx; commandDidWrite.Store(oldWrote) }()
	out, err := captureStdoutWithError(t, func() error {
		return withRenameStore(st, func() error { return runRename(renameCmd, []string{oldID, newID}) })
	})
	if err != nil {
		t.Fatalf("bd rename %s %s: %v\n%s", oldID, newID, err, out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "Renamed ") {
		t.Fatalf("bd rename %s %s printed %q first, want its Renamed line", oldID, newID, lines[0])
	}
	return lines[1:]
}

func TestRenameReportsReferencesItLeft(t *testing.T) {
	// GH#7040: bd rename rewrote references without saying where, so nothing
	// told the user which issues it had changed.
	ctx := context.Background()
	newStore := func(t *testing.T, issues ...*types.Issue) storage.DoltStorage {
		t.Helper()
		testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
		for _, issue := range issues {
			issue.Status, issue.Priority, issue.IssueType = types.StatusOpen, 2, types.TypeTask
			if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("failed to create issue %s: %v", issue.ID, err)
			}
		}
		return testStore
	}
	wantLines := func(t *testing.T, got, want []string) {
		t.Helper()
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("bd rename report:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}

	t.Run("rewritten fields", func(t *testing.T) {
		st := newStore(t,
			&types.Issue{ID: "test-a", Title: "A", Notes: "see test-a"},
			&types.Issue{ID: "test-b", Title: "test-a follow-up", Description: "after test-a"},
			&types.Issue{ID: "test-c", Title: "C", Description: "unrelated test-a.1"},
		)
		wantLines(t, runRenameOutput(t, st, "test-a", "test-z"), []string{
			"Rewrote test-a to test-z in:",
			"  test-b: title, description",
			"  test-z: notes",
		})
	})

	t.Run("failed update", func(t *testing.T) {
		// The update of test-b lands but reports an error. bd rename still
		// succeeds, rewrites test-c after it, and does not count test-b as
		// rewritten.
		st := &failingUpdateStore{DoltStorage: newStore(t,
			&types.Issue{ID: "test-a", Title: "A"},
			&types.Issue{ID: "test-b", Title: "B", Description: "after test-a"},
			&types.Issue{ID: "test-c", Title: "C", Notes: "see test-a"},
		), failID: "test-b"}
		wantLines(t, runRenameOutput(t, st, "test-a", "test-z"), []string{
			"Rewrote test-a to test-z in:",
			"  test-c: notes",
			"Warning: failed to update references in test-b (description); the update may or may not have landed: injected UpdateIssue failure",
		})
		if _, err := st.GetIssue(ctx, "test-z"); err != nil {
			t.Errorf("renamed issue after a failed reference update: %v, want it kept", err)
		}
	})

	t.Run("quiet", func(t *testing.T) {
		st := newStore(t,
			&types.Issue{ID: "test-a", Title: "A"},
			&types.Issue{ID: "test-b", Title: "B", Description: "about test-a.1"},
		)
		wantLines(t, runRenameOutput(t, st, "test-a", "test-z"), nil)
	})
}
