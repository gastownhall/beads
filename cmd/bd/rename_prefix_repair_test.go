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
	"time"

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

// TestRenameRewritesCloseReasonAndOwnText is #7399's regression for GH#7040,
// kept and extended: a rename must rewrite the old ID in other issues'
// close_reason and in the renamed issue's own six prose fields, leave
// closed_at and status alone, and preserve dotted-child and adjacent
// spellings while doing it.
func TestRenameRewritesCloseReasonAndOwnText(t *testing.T) {
	ctx := context.Background()
	testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

	for _, issue := range []*types.Issue{
		{ID: "test-a", Title: "A tracks test-a", Description: "test-a tracks test-a.1; test-a,test-a; see test-a.", Design: "per test-a's design", Notes: "test-a history here", AcceptanceCriteria: "test-a behavior kept", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "test-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue %s: %v", issue.ID, err)
		}
	}
	if err := testStore.CloseIssue(ctx, "test-b", "duplicate of test-a", "test", ""); err != nil {
		t.Fatalf("failed to close test-b: %v", err)
	}
	if err := testStore.CloseIssue(ctx, "test-a", "superseded by test-a cleanup", "test", ""); err != nil {
		t.Fatalf("failed to close test-a: %v", err)
	}

	a, err := testStore.GetIssue(ctx, "test-a")
	if err != nil {
		t.Fatalf("failed to get test-a: %v", err)
	}
	if err := renameIssueKeepingBeadGates(ctx, testStore, a, "test-z", nil, "test"); err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	if err := updateReferencesInAllIssues(ctx, testStore, "test-a", "test-z", "test"); err != nil {
		t.Fatalf("reference update failed: %v", err)
	}

	z, err := testStore.GetIssue(ctx, "test-z")
	if err != nil {
		t.Fatalf("failed to get test-z: %v", err)
	}
	for field, want := range map[string]string{
		"title":               "A tracks test-z",
		"description":         "test-z tracks test-a.1; test-z,test-z; see test-z.",
		"design":              "per test-z's design",
		"notes":               "test-z history here",
		"acceptance_criteria": "test-z behavior kept",
		"close_reason":        "superseded by test-z cleanup",
	} {
		if got := map[string]string{
			"title":               z.Title,
			"description":         z.Description,
			"design":              z.Design,
			"notes":               z.Notes,
			"acceptance_criteria": z.AcceptanceCriteria,
			"close_reason":        z.CloseReason,
		}[field]; got != want {
			t.Errorf("renamed issue %s = %q, want %q", field, got, want)
		}
	}
	if z.Status != types.StatusClosed {
		t.Errorf("renamed issue status = %q, want closed", z.Status)
	}
	if !z.ClosedAt.Equal(*a.ClosedAt) {
		t.Errorf("renamed issue closed_at = %v, want %v unchanged by the reference rewrite", z.ClosedAt, a.ClosedAt)
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
}

// failingRefUpdateStore fails UpdateIssue for failID: the identity rename
// commits, but that issue's reference rewrite does not.
type failingRefUpdateStore struct {
	storage.DoltStorage
	failID string
}

func (s *failingRefUpdateStore) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if id == s.failID {
		return errors.New("injected reference update failure")
	}
	return s.DoltStorage.UpdateIssue(ctx, id, updates, actor)
}

// TestRenameReportsReferencesItLeft runs the whole rename on a real store:
// field references are rewritten and listed, comment text is left alone and
// listed as such (a wisp comment included), a comment on the renamed issue
// follows it to the new ID, and nothing the reference scan finds fails the
// command.
func TestRenameReportsReferencesItLeft(t *testing.T) {
	ctx := context.Background()

	t.Run("comments follow the rename and are reported as left", func(t *testing.T) {
		testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))

		for _, issue := range []*types.Issue{
			{ID: "test-a", Title: "A mentions test-a", Description: "see test-a.", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "test-b", Title: "B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "test-w", Title: "W", Ephemeral: true, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		} {
			if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("failed to create issue %s: %v", issue.ID, err)
			}
		}
		if err := testStore.CloseIssue(ctx, "test-b", "duplicate of test-a", "test", ""); err != nil {
			t.Fatalf("failed to close test-b: %v", err)
		}
		createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		onA, err := testStore.ImportIssueComment(ctx, "test-a", "commenter", "was test-a and test-a again", createdAt)
		if err != nil {
			t.Fatalf("failed to comment on test-a: %v", err)
		}
		onB, err := testStore.ImportIssueComment(ctx, "test-b", "commenter", "tracked by test-a.", createdAt)
		if err != nil {
			t.Fatalf("failed to comment on test-b: %v", err)
		}
		onW, err := testStore.ImportIssueComment(ctx, "test-w", "commenter", "wisp sees test-a", createdAt)
		if err != nil {
			t.Fatalf("failed to comment on test-w: %v", err)
		}

		out := captureStdout(t, func() error {
			return withRenameStore(testStore, func() error {
				return runRename(renameCmd, []string{"test-a", "test-z"})
			})
		})
		for _, want := range []string{
			"Renamed",
			"Updated references:",
			"  test-b: close_reason",
			"  test-z: title, description",
			"Warning: references left in place:",
			fmt.Sprintf("  test-b comment %s: test-a. (comment text is not rewritten)", onB.ID),
			fmt.Sprintf("  test-w comment %s: test-a (comment text is not rewritten)", onW.ID),
			fmt.Sprintf("  test-z comment %s: test-a (comment text is not rewritten)", onA.ID),
		} {
			if !strings.Contains(out, want) {
				t.Errorf("rename output %q does not contain %q", out, want)
			}
		}

		z, err := testStore.GetIssue(ctx, "test-z")
		if err != nil {
			t.Fatalf("failed to get test-z: %v", err)
		}
		if z.Title != "A mentions test-z" || z.Description != "see test-z." {
			t.Errorf("test-z title/description = %q/%q, want the references rewritten", z.Title, z.Description)
		}
		b, err := testStore.GetIssue(ctx, "test-b")
		if err != nil {
			t.Fatalf("failed to get test-b: %v", err)
		}
		if b.CloseReason != "duplicate of test-z" {
			t.Errorf("test-b close_reason = %q, want %q", b.CloseReason, "duplicate of test-z")
		}

		// Comments are never rewritten, and the renamed issue's comment
		// followed it to the new ID (FK cascade) with everything but its
		// issue_id unchanged.
		after, err := testStore.GetCommentsForIssues(ctx, []string{"test-a", "test-z", "test-b", "test-w"})
		if err != nil {
			t.Fatalf("failed to read comments: %v", err)
		}
		if _, ok := after["test-a"]; ok {
			t.Errorf("comments still keyed under test-a: %v", after["test-a"])
		}
		for _, tt := range []struct {
			key     string
			comment *types.Comment
		}{
			{"test-z", onA},
			{"test-b", onB},
			{"test-w", onW},
		} {
			list := after[tt.key]
			if len(list) != 1 || list[0].ID != tt.comment.ID ||
				list[0].Author != tt.comment.Author || list[0].Text != tt.comment.Text ||
				!list[0].CreatedAt.Equal(tt.comment.CreatedAt) {
				t.Errorf("comment under %s = %+v, want %s unchanged", tt.key, list, tt.comment.ID)
			}
		}
	})

	t.Run("a rename with no references prints only the Renamed line", func(t *testing.T) {
		testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
		if err := testStore.CreateIssue(ctx, &types.Issue{ID: "test-q", Title: "Q", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "test"); err != nil {
			t.Fatalf("failed to create issue test-q: %v", err)
		}
		out := captureStdout(t, func() error {
			return withRenameStore(testStore, func() error {
				return runRename(renameCmd, []string{"test-q", "test-r"})
			})
		})
		if !strings.Contains(out, "Renamed") {
			t.Fatalf("rename output %q does not name the rename", out)
		}
		if strings.Contains(out, "Updated references:") || strings.Contains(out, "Warning") {
			t.Errorf("quiet rename printed %q, want only the Renamed line", out)
		}
	})

	t.Run("a failed reference update is reported, and the command succeeds", func(t *testing.T) {
		testStore := newTestStore(t, filepath.Join(t.TempDir(), "test.db"))
		for _, issue := range []*types.Issue{
			{ID: "test-f1", Title: "F1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "test-f2", Title: "F2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		} {
			if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("failed to create issue %s: %v", issue.ID, err)
			}
		}
		if err := testStore.CloseIssue(ctx, "test-f2", "duplicate of test-f1", "test", ""); err != nil {
			t.Fatalf("failed to close test-f2: %v", err)
		}
		onF2, err := testStore.ImportIssueComment(ctx, "test-f2", "commenter", "refs test-f1", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
		if err != nil {
			t.Fatalf("failed to comment on test-f2: %v", err)
		}

		st := &failingRefUpdateStore{DoltStorage: testStore, failID: "test-f2"}
		out := captureStdout(t, func() error {
			return withRenameStore(st, func() error {
				return runRename(renameCmd, []string{"test-f1", "test-f9"})
			})
		})
		for _, want := range []string{
			"Warning: could not rewrite some references (the rename itself succeeded):",
			"  test-f2 (close_reason): injected reference update failure",
			fmt.Sprintf("  test-f2 comment %s: test-f1 (comment text is not rewritten)", onF2.ID),
		} {
			if !strings.Contains(out, want) {
				t.Errorf("rename output %q does not contain %q", out, want)
			}
		}

		// The identity rename landed; the failed rewrite did not.
		if _, err := testStore.GetIssue(ctx, "test-f9"); err != nil {
			t.Errorf("test-f9: %v, want the rename committed", err)
		}
		f2, err := testStore.GetIssue(ctx, "test-f2")
		if err != nil {
			t.Fatalf("failed to get test-f2: %v", err)
		}
		if f2.CloseReason != "duplicate of test-f1" {
			t.Errorf("test-f2 close_reason = %q, want the failed rewrite not applied", f2.CloseReason)
		}
	})
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
