//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

type epicTestHelper struct {
	s   *dolt.DoltStore
	ctx context.Context
}

func newEpicTestHelper(t *testing.T) *epicTestHelper {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	return &epicTestHelper{
		s:   newTestStore(t, testDB),
		ctx: context.Background(),
	}
}

func (h *epicTestHelper) createIssue(t *testing.T, issue *types.Issue) {
	t.Helper()
	if err := h.s.CreateIssue(h.ctx, issue, "test"); err != nil {
		t.Fatal(err)
	}
}

func (h *epicTestHelper) addDependency(t *testing.T, dep *types.Dependency) {
	t.Helper()
	if err := h.s.AddDependency(h.ctx, dep, "test"); err != nil {
		t.Fatal(err)
	}
}

func (h *epicTestHelper) getEpicStatus(t *testing.T, epicID string) *types.EpicStatus {
	t.Helper()
	epics, err := h.s.GetEpicsEligibleForClosure(h.ctx)
	if err != nil {
		t.Fatalf("GetEpicsEligibleForClosure failed: %v", err)
	}

	for _, epic := range epics {
		if epic.Epic.ID == epicID {
			return epic
		}
	}
	return nil
}

func TestEpicSuite(t *testing.T) {
	h := newEpicTestHelper(t)

	t.Run("MixedChildrenNotEligible", func(t *testing.T) {
		epic := &types.Issue{
			ID:          "test-epic-1",
			Title:       "Test Epic",
			Description: "Epic description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
			CreatedAt:   time.Now(),
		}
		h.createIssue(t, epic)

		child1 := &types.Issue{
			Title:     "Child Task 1",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			ClosedAt:  ptrTime(time.Now()),
		}
		child2 := &types.Issue{
			Title:     "Child Task 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
		}
		h.createIssue(t, child1)
		h.createIssue(t, child2)

		h.addDependency(t, &types.Dependency{
			IssueID:     child1.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})
		h.addDependency(t, &types.Dependency{
			IssueID:     child2.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})

		store = h.s
		epicStatus := h.getEpicStatus(t, "test-epic-1")
		if epicStatus == nil {
			t.Fatal("Epic test-epic-1 not found in results")
		}
		if epicStatus.TotalChildren != 2 {
			t.Errorf("Expected 2 total children, got %d", epicStatus.TotalChildren)
		}
		if epicStatus.ClosedChildren != 1 {
			t.Errorf("Expected 1 closed child, got %d", epicStatus.ClosedChildren)
		}
		if epicStatus.EligibleForClose {
			t.Error("Epic should not be eligible for close with open children")
		}
	})

	t.Run("OpenWispChildNotEligible", func(t *testing.T) {
		epic := &types.Issue{
			ID:          "test-epic-wisp",
			Title:       "Epic with wisp child",
			Description: "Tests that wisp children are counted for closure eligibility",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
			CreatedAt:   time.Now(),
		}
		h.createIssue(t, epic)

		regularChild := &types.Issue{
			Title:     "Regular child",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			ClosedAt:  ptrTime(time.Now()),
		}
		wispChild := &types.Issue{
			Title:     "Wisp child",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
			CreatedAt: time.Now(),
		}
		h.createIssue(t, regularChild)
		h.createIssue(t, wispChild)

		h.addDependency(t, &types.Dependency{
			IssueID:     regularChild.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})
		h.addDependency(t, &types.Dependency{
			IssueID:     wispChild.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		})

		epicStatus := h.getEpicStatus(t, "test-epic-wisp")
		if epicStatus == nil {
			t.Fatal("Epic test-epic-wisp not found in results")
		}
		if epicStatus.TotalChildren != 2 {
			t.Errorf("Expected 2 total children (1 regular + 1 wisp), got %d", epicStatus.TotalChildren)
		}
		if epicStatus.ClosedChildren != 1 {
			t.Errorf("Expected 1 closed child, got %d", epicStatus.ClosedChildren)
		}
		if epicStatus.EligibleForClose {
			t.Error("Epic should NOT be eligible for close with open wisp child")
		}
	})

	t.Run("AllChildrenClosedEligible", func(t *testing.T) {
		epic := &types.Issue{
			ID:          "test-epic-2",
			Title:       "Fully Completed Epic",
			Description: "Epic description",
			Status:      types.StatusOpen,
			Priority:    1,
			IssueType:   types.TypeEpic,
			CreatedAt:   time.Now(),
		}
		h.createIssue(t, epic)

		for i := 1; i <= 3; i++ {
			child := &types.Issue{
				Title:     fmt.Sprintf("Child Task %d", i),
				Status:    types.StatusClosed,
				Priority:  2,
				IssueType: types.TypeTask,
				CreatedAt: time.Now(),
				ClosedAt:  ptrTime(time.Now()),
			}
			h.createIssue(t, child)
			h.addDependency(t, &types.Dependency{
				IssueID:     child.ID,
				DependsOnID: epic.ID,
				Type:        types.DepParentChild,
			})
		}

		epicStatus := h.getEpicStatus(t, "test-epic-2")
		if epicStatus == nil {
			t.Fatal("Epic test-epic-2 not found in results")
		}
		if epicStatus.TotalChildren != 3 {
			t.Errorf("Expected 3 total children, got %d", epicStatus.TotalChildren)
		}
		if epicStatus.ClosedChildren != 3 {
			t.Errorf("Expected 3 closed children, got %d", epicStatus.ClosedChildren)
		}
		if !epicStatus.EligibleForClose {
			t.Error("Epic should be eligible for close when all children are closed")
		}
	})
}

func TestEpicCommandInit(t *testing.T) {
	if epicCmd == nil {
		t.Fatal("epicCmd should be initialized")
	}

	if epicCmd.Use != "epic" {
		t.Errorf("Expected Use='epic', got %q", epicCmd.Use)
	}

	var hasStatusCmd bool
	for _, cmd := range epicCmd.Commands() {
		if cmd.Use == "status" {
			hasStatusCmd = true
		}
	}

	if !hasStatusCmd {
		t.Error("epic command should have status subcommand")
	}
}

// TestEpicStatusListsEveryOpenEpic pins GH#5720 (bd-ufmqco): `bd epic status`
// shows the status of every open epic, not only the epics that have
// parent-child edge children. A childless epic and an epic whose only child
// is a dotted-ID child without a parent-child edge must both be listed
// (GH#5720's repro: three open epics, `bd epic status` printing one row).
func TestEpicStatusListsEveryOpenEpic(t *testing.T) {
	h := newEpicTestHelper(t)

	withEdge := &types.Issue{ID: "test-ufm-edge", Title: "epic with edge child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic, CreatedAt: time.Now()}
	childless := &types.Issue{ID: "test-ufm-none", Title: "epic childless", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic, CreatedAt: time.Now()}
	dottedOnly := &types.Issue{ID: "test-ufm-dot", Title: "epic dotted child only", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic, CreatedAt: time.Now()}
	for _, e := range []*types.Issue{withEdge, childless, dottedOnly} {
		h.createIssue(t, e)
	}
	realChild := &types.Issue{Title: "real child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()}
	h.createIssue(t, realChild)
	h.addDependency(t, &types.Dependency{IssueID: realChild.ID, DependsOnID: withEdge.ID, Type: types.DepParentChild, CreatedAt: time.Now(), CreatedBy: "test"})
	// Dotted ID only: no parent-child edge, so the child does not count for
	// epic status (linking it would take `bd create --parent` or
	// `bd dep add ... --type parent-child`).
	h.createIssue(t, &types.Issue{ID: "test-ufm-dot.1", Title: "dotted child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()})

	oldStore, oldCtx, oldJSON := store, rootCtx, jsonOutput
	store, rootCtx, jsonOutput = h.s, h.ctx, true
	t.Cleanup(func() { store, rootCtx, jsonOutput = oldStore, oldCtx, oldJSON })

	runStatus := func() string {
		return captureStdout(t, func() error { return epicStatusCmd.RunE(epicStatusCmd, nil) })
	}

	// JSON mode: all three epics, each exactly once, with the right counts.
	out := runStatus()
	var epics []*types.EpicStatus
	if err := json.Unmarshal([]byte(out), &epics); err != nil {
		t.Fatalf("parse epic status JSON: %v\n%s", err, out)
	}
	seen := make(map[string]*types.EpicStatus)
	for _, e := range epics {
		seen[e.Epic.ID] = e
	}
	for _, id := range []string{withEdge.ID, childless.ID, dottedOnly.ID} {
		if seen[id] == nil {
			t.Errorf("bd epic status omitted open epic %s; got %v", id, epicStatusIDsForTest(epics))
		}
	}
	if e := seen[withEdge.ID]; e != nil {
		if e.TotalChildren != 1 || e.ClosedChildren != 0 || e.EligibleForClose {
			t.Errorf("%s = {Total:%d Closed:%d Eligible:%v}, want {1 0 false}", withEdge.ID, e.TotalChildren, e.ClosedChildren, e.EligibleForClose)
		}
	}
	for _, id := range []string{childless.ID, dottedOnly.ID} {
		if e := seen[id]; e != nil {
			if e.TotalChildren != 0 || e.ClosedChildren != 0 || e.EligibleForClose {
				t.Errorf("%s = {Total:%d Closed:%d Eligible:%v}, want {0 0 false}", id, e.TotalChildren, e.ClosedChildren, e.EligibleForClose)
			}
		}
	}

	// Human mode: the row rendering for a zero-child epic is 0/0 (0%) with
	// no eligibility marker.
	jsonOutput = false
	out = runStatus()
	for _, id := range []string{withEdge.ID, childless.ID, dottedOnly.ID} {
		if !strings.Contains(out, id) {
			t.Errorf("bd epic status (human) omitted open epic %s:\n%s", id, out)
		}
	}
	if n := strings.Count(out, "0/0 children closed (0%)"); n != 2 {
		t.Errorf("expected two 0/0 (0%%) rows (childless + dotted-only), got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "0/1 children closed (0%)") {
		t.Errorf("expected one 0/1 (0%%) row for the edge-child epic:\n%s", out)
	}
	if strings.Contains(out, "Eligible for closure") {
		t.Errorf("no epic in this fixture is eligible; eligibility marker printed anyway:\n%s", out)
	}
}

// TestEpicStatusEligibleOnlyReturnsOnlyCompletedEpics pins GH#5720 (bd-ufmqco)
// AC-4: with zero-child, partial and fully completing-closed epics in the
// store, `bd epic status --eligible-only` returns only the fully
// completing-closed one. The "{0,0,false}" rows a childless epic now
// contributes to the unfiltered list must never leak into --eligible-only.
func TestEpicStatusEligibleOnlyReturnsOnlyCompletedEpics(t *testing.T) {
	h := newEpicTestHelper(t)

	childless := &types.Issue{ID: "test-eo-none", Title: "childless epic", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic, CreatedAt: time.Now()}
	done := &types.Issue{ID: "test-eo-done", Title: "completed epic", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic, CreatedAt: time.Now()}
	partial := &types.Issue{ID: "test-eo-part", Title: "partial epic", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeEpic, CreatedAt: time.Now()}
	for _, e := range []*types.Issue{childless, done, partial} {
		h.createIssue(t, e)
	}
	closedChild := &types.Issue{Title: "closed child", Status: types.StatusClosed, Priority: 1, IssueType: types.TypeTask, ClosedAt: ptrTime(time.Now()), CreatedAt: time.Now()}
	openChild := &types.Issue{Title: "open child", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, CreatedAt: time.Now()}
	h.createIssue(t, closedChild)
	h.createIssue(t, openChild)
	h.addDependency(t, &types.Dependency{IssueID: closedChild.ID, DependsOnID: done.ID, Type: types.DepParentChild, CreatedAt: time.Now(), CreatedBy: "test"})
	h.addDependency(t, &types.Dependency{IssueID: closedChild.ID, DependsOnID: partial.ID, Type: types.DepParentChild, CreatedAt: time.Now(), CreatedBy: "test"})
	h.addDependency(t, &types.Dependency{IssueID: openChild.ID, DependsOnID: partial.ID, Type: types.DepParentChild, CreatedAt: time.Now(), CreatedBy: "test"})

	oldStore, oldCtx, oldJSON := store, rootCtx, jsonOutput
	store, rootCtx, jsonOutput = h.s, h.ctx, true
	t.Cleanup(func() { store, rootCtx, jsonOutput = oldStore, oldCtx, oldJSON })

	if err := epicStatusCmd.Flags().Set("eligible-only", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = epicStatusCmd.Flags().Set("eligible-only", "false") })

	out := captureStdout(t, func() error { return epicStatusCmd.RunE(epicStatusCmd, nil) })
	var epics []*types.EpicStatus
	if err := json.Unmarshal([]byte(out), &epics); err != nil {
		t.Fatalf("parse epic status JSON: %v\n%s", err, out)
	}
	if len(epics) != 1 {
		t.Fatalf("--eligible-only returned %d epic(s) (%s), want exactly the completed one %s", len(epics), epicStatusIDsForTest(epics), done.ID)
	}
	if epics[0].Epic.ID != done.ID {
		t.Errorf("--eligible-only returned %s, want %s", epics[0].Epic.ID, done.ID)
	}
}

func epicStatusIDsForTest(epics []*types.EpicStatus) string {
	ids := make([]string, 0, len(epics))
	for _, e := range epics {
		if e != nil && e.Epic != nil {
			ids = append(ids, e.Epic.ID)
		}
	}
	return "[" + strings.Join(ids, " ") + "]"
}
