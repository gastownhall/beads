//go:build cgo

package dolt

import (
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// GetReadyWork tests
// =============================================================================

func TestGetReadyWork_EmptyStore(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	work, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(work) != 0 {
		t.Errorf("expected 0 ready work from empty store, got %d", len(work))
	}
}

func TestGetReadyWork_ExcludesClosedIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "rw-closed",
		Title:     "Closed Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	work, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, w := range work {
		if w.ID == issue.ID {
			t.Error("closed issue should not appear in ready work")
		}
	}
}

func TestGetReadyWork_StatusFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	open := &types.Issue{
		ID:        "rw-open",
		Title:     "Open Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	inProgress := &types.Issue{
		ID:        "rw-inprog",
		Title:     "In Progress",
		Status:    types.StatusInProgress,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{open, inProgress} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue %s: %v", iss.ID, err)
		}
	}

	// Filter by in_progress only
	work, err := store.GetReadyWork(ctx, types.WorkFilter{Status: types.StatusInProgress})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundOpen := false
	foundInProgress := false
	for _, w := range work {
		if w.ID == open.ID {
			foundOpen = true
		}
		if w.ID == inProgress.ID {
			foundInProgress = true
		}
	}
	if foundOpen {
		t.Error("open issue should not appear when filtering for in_progress")
	}
	if !foundInProgress {
		t.Error("in_progress issue should appear when filtering for in_progress")
	}
}

func TestGetReadyWork_PriorityFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	p1 := &types.Issue{
		ID:        "rw-p1",
		Title:     "Priority 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	p3 := &types.Issue{
		ID:        "rw-p3",
		Title:     "Priority 3",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{p1, p3} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue %s: %v", iss.ID, err)
		}
	}

	priority := 1
	work, err := store.GetReadyWork(ctx, types.WorkFilter{Priority: &priority})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, w := range work {
		if w.ID == p3.ID {
			t.Error("priority 3 issue should not appear when filtering for priority 1")
		}
	}
}

func TestGetReadyWork_ExcludesPinnedIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	pinned := &types.Issue{
		ID:        "rw-pinned",
		Title:     "Pinned Context",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		Pinned:    true,
	}
	if err := store.CreateIssue(ctx, pinned, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	work, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, w := range work {
		if w.ID == pinned.ID {
			t.Error("pinned issue should not appear in ready work")
		}
	}
}

func TestGetReadyWork_ExcludesBlockedIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocker := &types.Issue{
		ID:        "rw-blocker",
		Title:     "Blocker",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	blocked := &types.Issue{
		ID:        "rw-blocked",
		Title:     "Blocked",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{blocker, blocked} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	dep := &types.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	work, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, w := range work {
		if w.ID == blocked.ID {
			t.Error("blocked issue should not appear in ready work")
		}
	}
}

func TestGetReadyWork_UnassignedFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	assigned := &types.Issue{
		ID:        "rw-assigned",
		Title:     "Assigned Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Assignee:  "alice",
	}
	unassigned := &types.Issue{
		ID:        "rw-unassigned",
		Title:     "Unassigned Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{assigned, unassigned} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	work, err := store.GetReadyWork(ctx, types.WorkFilter{Unassigned: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, w := range work {
		if w.ID == assigned.ID {
			t.Error("assigned issue should not appear when filtering for unassigned")
		}
	}
}

func TestGetReadyWork_LimitFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	for i := 0; i < 5; i++ {
		iss := &types.Issue{
			ID:        fmt.Sprintf("rw-limit-%d", i),
			Title:     fmt.Sprintf("Limit Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	work, err := store.GetReadyWork(ctx, types.WorkFilter{Limit: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(work) > 2 {
		t.Errorf("expected at most 2 results with Limit=2, got %d", len(work))
	}
}

func TestGetReadyWork_TypeFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	task := &types.Issue{
		ID:        "rw-task",
		Title:     "A Task",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	bug := &types.Issue{
		ID:        "rw-bug",
		Title:     "A Bug",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeBug,
	}

	for _, iss := range []*types.Issue{task, bug} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	work, err := store.GetReadyWork(ctx, types.WorkFilter{Type: string(types.TypeBug)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundTask := false
	foundBug := false
	for _, w := range work {
		if w.ID == task.ID {
			foundTask = true
		}
		if w.ID == bug.ID {
			foundBug = true
		}
	}
	if foundTask {
		t.Error("task should not appear when filtering for bug type")
	}
	if !foundBug {
		t.Error("bug should appear when filtering for bug type")
	}
}

// =============================================================================
// GetBlockedIssues tests
// =============================================================================

func TestGetBlockedIssues_EmptyStore(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocked, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blocked) != 0 {
		t.Errorf("expected 0 blocked issues from empty store, got %d", len(blocked))
	}
}

func TestGetBlockedIssues_ReturnsBlockedWithBlockers(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocker := &types.Issue{
		ID:        "bi-blocker",
		Title:     "Blocker Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	blocked := &types.Issue{
		ID:        "bi-blocked",
		Title:     "Blocked Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{blocker, blocked} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	dep := &types.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	results, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, bi := range results {
		if bi.Issue.ID == blocked.ID {
			found = true
			if bi.BlockedByCount != 1 {
				t.Errorf("expected 1 blocker, got %d", bi.BlockedByCount)
			}
			if len(bi.BlockedBy) != 1 || bi.BlockedBy[0] != blocker.ID {
				t.Errorf("expected blocker %s, got %v", blocker.ID, bi.BlockedBy)
			}
		}
	}
	if !found {
		t.Error("expected to find the blocked issue in results")
	}
}

func TestGetBlockedIssues_ExcludesClosedBlockers(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocker := &types.Issue{
		ID:        "bi-closeblocker",
		Title:     "Closed Blocker",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	blocked := &types.Issue{
		ID:        "bi-wouldbeblocked",
		Title:     "Would Be Blocked",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{blocker, blocked} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	dep := &types.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// Close the blocker
	if err := store.CloseIssue(ctx, blocker.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close blocker: %v", err)
	}

	results, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, bi := range results {
		if bi.Issue.ID == blocked.ID {
			t.Error("issue should not be blocked when its blocker is closed")
		}
	}
}

func TestGetBlockedIssues_MultipleBlockers(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blockerA := &types.Issue{
		ID:        "bi-blockerA",
		Title:     "Blocker A",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	blockerB := &types.Issue{
		ID:        "bi-blockerB",
		Title:     "Blocker B",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	blocked := &types.Issue{
		ID:        "bi-multiblocked",
		Title:     "Multi Blocked",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{blockerA, blockerB, blocked} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue %s: %v", iss.ID, err)
		}
	}

	for _, blockerID := range []string{blockerA.ID, blockerB.ID} {
		dep := &types.Dependency{
			IssueID:     blocked.ID,
			DependsOnID: blockerID,
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, "tester"); err != nil {
			t.Fatalf("failed to add dependency: %v", err)
		}
	}

	results, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, bi := range results {
		if bi.Issue.ID == blocked.ID {
			if bi.BlockedByCount != 2 {
				t.Errorf("expected 2 blockers, got %d", bi.BlockedByCount)
			}
		}
	}
}

// =============================================================================
// SearchIssues tests
// =============================================================================

func TestSearchIssues_EmptyStore(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	results, err := store.SearchIssues(ctx, "anything", types.IssueFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty store, got %d", len(results))
	}
}

func TestSearchIssues_ByTitle(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "si-title",
		Title:     "Unique Searchable Title",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	results, err := store.SearchIssues(ctx, "Unique Searchable", types.IssueFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != issue.ID {
		t.Errorf("expected issue %s, got %s", issue.ID, results[0].ID)
	}
}

func TestSearchIssues_ByDescription(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:          "si-desc",
		Title:       "Normal Title",
		Description: "Special unique description text",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	results, err := store.SearchIssues(ctx, "Special unique description", types.IssueFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != issue.ID {
		t.Errorf("expected issue %s, got %s", issue.ID, results[0].ID)
	}
}

func TestSearchIssues_ByID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "si-searchbyid-xyz",
		Title:     "ID Search",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	results, err := store.SearchIssues(ctx, "si-searchbyid-xyz", types.IssueFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestSearchIssues_NoMatch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "si-nomatch",
		Title:     "Existing Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	results, err := store.SearchIssues(ctx, "zzz-never-matches-zzz", types.IssueFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSearchIssues_StatusFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	open := &types.Issue{
		ID:          "si-stat-open",
		Title:       "Status Filter Test",
		Description: "Open issue",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	closed := &types.Issue{
		ID:          "si-stat-closed",
		Title:       "Status Filter Test Closed",
		Description: "Closed issue",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}

	for _, iss := range []*types.Issue{open, closed} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}
	if err := store.CloseIssue(ctx, closed.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	openStatus := types.StatusOpen
	results, err := store.SearchIssues(ctx, "Status Filter Test", types.IssueFilter{Status: &openStatus})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 open result, got %d", len(results))
	}
	if results[0].ID != open.ID {
		t.Errorf("expected open issue, got %s", results[0].ID)
	}
}

func TestSearchIssues_PriorityFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	p1 := &types.Issue{
		ID:        "si-pri-1",
		Title:     "Priority Filter",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	p4 := &types.Issue{
		ID:        "si-pri-4",
		Title:     "Priority Filter Low",
		Status:    types.StatusOpen,
		Priority:  4,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{p1, p4} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	priority := 1
	results, err := store.SearchIssues(ctx, "Priority Filter", types.IssueFilter{Priority: &priority})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result for priority 1, got %d", len(results))
	}
	if results[0].ID != p1.ID {
		t.Errorf("expected p1 issue, got %s", results[0].ID)
	}
}

func TestSearchIssues_LimitFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	for i := 0; i < 5; i++ {
		iss := &types.Issue{
			ID:        fmt.Sprintf("si-limit-%d", i),
			Title:     "Limit Test Issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	results, err := store.SearchIssues(ctx, "Limit Test", types.IssueFilter{Limit: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) > 3 {
		t.Errorf("expected at most 3 results with Limit=3, got %d", len(results))
	}
}

func TestSearchIssues_LabelFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	labeled := &types.Issue{
		ID:        "si-labeled",
		Title:     "Label Filter Test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	unlabeled := &types.Issue{
		ID:        "si-unlabeled",
		Title:     "Label Filter Test No Label",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{labeled, unlabeled} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	if err := store.AddLabel(ctx, labeled.ID, "important", "tester"); err != nil {
		t.Fatalf("failed to add label: %v", err)
	}

	results, err := store.SearchIssues(ctx, "Label Filter Test", types.IssueFilter{Labels: []string{"important"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with label filter, got %d", len(results))
	}
	if results[0].ID != labeled.ID {
		t.Errorf("expected labeled issue, got %s", results[0].ID)
	}
}

func TestSearchIssues_EmptyQuery(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	for i := 0; i < 3; i++ {
		iss := &types.Issue{
			ID:        fmt.Sprintf("si-empty-%d", i),
			Title:     fmt.Sprintf("Empty Query Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	results, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) < 3 {
		t.Errorf("expected at least 3 results with empty query, got %d", len(results))
	}
}

func TestSearchIssues_IssueTypeFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	task := &types.Issue{
		ID:        "si-type-task",
		Title:     "Type Filter",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	bug := &types.Issue{
		ID:        "si-type-bug",
		Title:     "Type Filter Bug",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeBug,
	}

	for _, iss := range []*types.Issue{task, bug} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	bugType := types.TypeBug
	results, err := store.SearchIssues(ctx, "Type Filter", types.IssueFilter{IssueType: &bugType})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 bug result, got %d", len(results))
	}
	if results[0].ID != bug.ID {
		t.Errorf("expected bug issue, got %s", results[0].ID)
	}
}

// =============================================================================
// GetStatistics tests
// =============================================================================

func TestGetStatistics_EmptyStore(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.TotalIssues != 0 {
		t.Errorf("expected 0 total issues, got %d", stats.TotalIssues)
	}
	if stats.OpenIssues != 0 {
		t.Errorf("expected 0 open issues, got %d", stats.OpenIssues)
	}
	if stats.ClosedIssues != 0 {
		t.Errorf("expected 0 closed issues, got %d", stats.ClosedIssues)
	}
	if stats.BlockedIssues != 0 {
		t.Errorf("expected 0 blocked issues, got %d", stats.BlockedIssues)
	}
}

func TestGetStatistics_CountsByStatus(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issues := []*types.Issue{
		{ID: "stat-open-1", Title: "Open 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "stat-open-2", Title: "Open 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "stat-inprog", Title: "In Progress", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeTask},
		{ID: "stat-closed-1", Title: "Closed 1", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask},
		{ID: "stat-closed-2", Title: "Closed 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	for _, iss := range issues {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue %s: %v", iss.ID, err)
		}
	}

	// Close two issues
	for _, id := range []string{"stat-closed-1", "stat-closed-2"} {
		if err := store.CloseIssue(ctx, id, "done", "tester", "s1"); err != nil {
			t.Fatalf("failed to close issue %s: %v", id, err)
		}
	}

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stats.TotalIssues != 5 {
		t.Errorf("expected 5 total issues, got %d", stats.TotalIssues)
	}
	if stats.OpenIssues != 2 {
		t.Errorf("expected 2 open issues, got %d", stats.OpenIssues)
	}
	if stats.InProgressIssues != 1 {
		t.Errorf("expected 1 in-progress issue, got %d", stats.InProgressIssues)
	}
	if stats.ClosedIssues != 2 {
		t.Errorf("expected 2 closed issues, got %d", stats.ClosedIssues)
	}
}

func TestGetStatistics_BlockedCount(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocker := &types.Issue{
		ID:        "stat-blocker",
		Title:     "Blocker",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	blocked := &types.Issue{
		ID:        "stat-blocked",
		Title:     "Blocked",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{blocker, blocked} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	dep := &types.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stats.BlockedIssues != 1 {
		t.Errorf("expected 1 blocked issue, got %d", stats.BlockedIssues)
	}
}

func TestGetStatistics_PinnedCount(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	pinned := &types.Issue{
		ID:        "stat-pinned",
		Title:     "Pinned Issue",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
		Pinned:    true,
	}
	if err := store.CreateIssue(ctx, pinned, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stats.PinnedIssues != 1 {
		t.Errorf("expected 1 pinned issue, got %d", stats.PinnedIssues)
	}
}

func TestGetStatistics_DeferredCount(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	deferred := &types.Issue{
		ID:        "stat-deferred",
		Title:     "Deferred Issue",
		Status:    types.StatusDeferred,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, deferred, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stats.DeferredIssues != 1 {
		t.Errorf("expected 1 deferred issue, got %d", stats.DeferredIssues)
	}
}

func TestGetStatistics_ReadyIssuesExcludesBlocked(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocker := &types.Issue{
		ID:        "stat-r-blocker",
		Title:     "Blocker",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	blocked := &types.Issue{
		ID:        "stat-r-blocked",
		Title:     "Blocked",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	ready := &types.Issue{
		ID:        "stat-r-ready",
		Title:     "Ready",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{blocker, blocked, ready} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	dep := &types.Dependency{
		IssueID:     blocked.ID,
		DependsOnID: blocker.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	stats, err := store.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 3 open issues, 1 blocked => ready = 3 - 1 = 2
	if stats.ReadyIssues != 2 {
		t.Errorf("expected 2 ready issues (3 open - 1 blocked), got %d", stats.ReadyIssues)
	}
}

// =============================================================================
// GetStaleIssues tests
// =============================================================================

func TestGetStaleIssues_EmptyStore(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	stale, err := store.GetStaleIssues(ctx, types.StaleFilter{Days: 7})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("expected 0 stale issues from empty store, got %d", len(stale))
	}
}

func TestGetStaleIssues_ReturnsStale(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "stale-old",
		Title:     "Old Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Backdate updated_at to 15 days ago
	oldDate := time.Now().UTC().AddDate(0, 0, -15)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET updated_at = ? WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to backdate: %v", err)
	}

	stale, err := store.GetStaleIssues(ctx, types.StaleFilter{Days: 7})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale issue, got %d", len(stale))
	}
	if stale[0].ID != issue.ID {
		t.Errorf("expected issue %s, got %s", issue.ID, stale[0].ID)
	}
}

func TestGetStaleIssues_ExcludesRecent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "stale-fresh",
		Title:     "Fresh Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	// updated_at is "now" (set by CreateIssue)

	stale, err := store.GetStaleIssues(ctx, types.StaleFilter{Days: 7})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range stale {
		if s.ID == issue.ID {
			t.Error("recently updated issue should not be stale")
		}
	}
}

func TestGetStaleIssues_ExcludesClosed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	issue := &types.Issue{
		ID:        "stale-closed",
		Title:     "Closed Stale",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "s1"); err != nil {
		t.Fatalf("failed to close issue: %v", err)
	}

	// Backdate updated_at
	oldDate := time.Now().UTC().AddDate(0, 0, -15)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET updated_at = ? WHERE id = ?", oldDate, issue.ID)
	if err != nil {
		t.Fatalf("failed to backdate: %v", err)
	}

	stale, err := store.GetStaleIssues(ctx, types.StaleFilter{Days: 7})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range stale {
		if s.ID == issue.ID {
			t.Error("closed issue should not appear in stale results")
		}
	}
}

func TestGetStaleIssues_StatusFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	open := &types.Issue{
		ID:        "stale-sf-open",
		Title:     "Open Stale",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	inProg := &types.Issue{
		ID:        "stale-sf-inprog",
		Title:     "In Progress Stale",
		Status:    types.StatusInProgress,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	for _, iss := range []*types.Issue{open, inProg} {
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Backdate both
	oldDate := time.Now().UTC().AddDate(0, 0, -15)
	for _, id := range []string{open.ID, inProg.ID} {
		_, err := store.db.ExecContext(ctx,
			"UPDATE issues SET updated_at = ? WHERE id = ?", oldDate, id)
		if err != nil {
			t.Fatalf("failed to backdate: %v", err)
		}
	}

	// Filter for in_progress only
	stale, err := store.GetStaleIssues(ctx, types.StaleFilter{Days: 7, Status: string(types.StatusInProgress)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	foundOpen := false
	foundInProg := false
	for _, s := range stale {
		if s.ID == open.ID {
			foundOpen = true
		}
		if s.ID == inProg.ID {
			foundInProg = true
		}
	}
	if foundOpen {
		t.Error("open issue should not appear when filtering for in_progress")
	}
	if !foundInProg {
		t.Error("in_progress issue should appear when filtering for in_progress")
	}
}

func TestGetStaleIssues_LimitFilter(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	for i := 0; i < 5; i++ {
		iss := &types.Issue{
			ID:        fmt.Sprintf("stale-lim-%d", i),
			Title:     fmt.Sprintf("Stale Limit %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
	}

	// Backdate all
	oldDate := time.Now().UTC().AddDate(0, 0, -15)
	for i := 0; i < 5; i++ {
		_, err := store.db.ExecContext(ctx,
			"UPDATE issues SET updated_at = ? WHERE id = ?", oldDate, fmt.Sprintf("stale-lim-%d", i))
		if err != nil {
			t.Fatalf("failed to backdate: %v", err)
		}
	}

	stale, err := store.GetStaleIssues(ctx, types.StaleFilter{Days: 7, Limit: 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stale) > 2 {
		t.Errorf("expected at most 2 results with Limit=2, got %d", len(stale))
	}
}

func TestGetStaleIssues_ExcludesEphemeral(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	eph := &types.Issue{
		ID:        "stale-eph",
		Title:     "Ephemeral Stale",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, eph, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Backdate
	oldDate := time.Now().UTC().AddDate(0, 0, -15)
	_, err := store.db.ExecContext(ctx,
		"UPDATE issues SET updated_at = ? WHERE id = ?", oldDate, eph.ID)
	if err != nil {
		t.Fatalf("failed to backdate: %v", err)
	}

	stale, err := store.GetStaleIssues(ctx, types.StaleFilter{Days: 7})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, s := range stale {
		if s.ID == eph.ID {
			t.Error("ephemeral issue should not appear in stale results")
		}
	}
}
