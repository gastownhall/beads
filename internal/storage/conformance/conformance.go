// Package conformance provides backend-agnostic tests for Storage implementations.
//
// Usage from a backend test file:
//
//	func TestConformance(t *testing.T) {
//	    conformance.RunAll(t, func(t *testing.T) storage.DoltStorage {
//	        return newTestStore(t)
//	    })
//	}
package conformance

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Factory creates a fresh, empty store for each test.
//
// The returned store must be initialized and ready to accept writes: in
// particular the backend's required config (e.g. issue_prefix) must already be
// set, exactly as `bd init` would leave it. The suite only exercises behavior;
// it does not perform backend initialization.
type Factory func(t *testing.T) storage.DoltStorage

// RunAll runs the full conformance suite against the given factory.
func RunAll(t *testing.T, factory Factory) {
	t.Helper()

	// Issue CRUD
	t.Run("CreateAndGet", func(t *testing.T) { testCreateAndGet(t, factory) })
	t.Run("CreateDuplicate", func(t *testing.T) { testCreateDuplicate(t, factory) })
	t.Run("GetNotFound", func(t *testing.T) { testGetNotFound(t, factory) })
	t.Run("GetByExternalRef", func(t *testing.T) { testGetByExternalRef(t, factory) })
	t.Run("GetByIDs", func(t *testing.T) { testGetByIDs(t, factory) })
	t.Run("Update", func(t *testing.T) { testUpdate(t, factory) })
	t.Run("UpdatePreservesCreatedAt", func(t *testing.T) { testUpdatePreservesCreatedAt(t, factory) })
	t.Run("UpdateNotFound", func(t *testing.T) { testUpdateNotFound(t, factory) })
	t.Run("UpdateIssueType", func(t *testing.T) { testUpdateIssueType(t, factory) })
	t.Run("CloseAndReopen", func(t *testing.T) { testCloseAndReopen(t, factory) })
	t.Run("Delete", func(t *testing.T) { testDelete(t, factory) })
	t.Run("DeleteNotFound", func(t *testing.T) { testDeleteNotFound(t, factory) })

	// Search and filter
	t.Run("SearchNoFilter", func(t *testing.T) { testSearchNoFilter(t, factory) })
	t.Run("SearchTextQuery", func(t *testing.T) { testSearchTextQuery(t, factory) })
	t.Run("SearchStatusFilter", func(t *testing.T) { testSearchStatusFilter(t, factory) })
	t.Run("SearchPriorityFilter", func(t *testing.T) { testSearchPriorityFilter(t, factory) })
	t.Run("SearchLimit", func(t *testing.T) { testSearchLimit(t, factory) })
	t.Run("CountIssues", func(t *testing.T) { testCountIssues(t, factory) })
	t.Run("CountByGroup", func(t *testing.T) { testCountByGroup(t, factory) })

	// Dependencies
	t.Run("AddAndGetDeps", func(t *testing.T) { testAddAndGetDeps(t, factory) })
	t.Run("RemoveDep", func(t *testing.T) { testRemoveDep(t, factory) })
	t.Run("DepCounts", func(t *testing.T) { testDepCounts(t, factory) })

	// Ready/Blocked
	t.Run("ReadyNoDeps", func(t *testing.T) { testReadyNoDeps(t, factory) })
	t.Run("ReadyBlockedByOpenDep", func(t *testing.T) { testReadyBlockedByOpenDep(t, factory) })
	t.Run("ReadyUnblockedByClose", func(t *testing.T) { testReadyUnblockedByClose(t, factory) })
	t.Run("BlockedIssues", func(t *testing.T) { testBlockedIssues(t, factory) })
	t.Run("EpicsEligibleForClosure", func(t *testing.T) { testEpicsEligible(t, factory) })

	// Labels
	t.Run("Labels", func(t *testing.T) { testLabels(t, factory) })
	t.Run("LabelIdempotent", func(t *testing.T) { testLabelIdempotent(t, factory) })
	t.Run("GetIssuesByLabel", func(t *testing.T) { testGetIssuesByLabel(t, factory) })

	// Comments
	t.Run("Comments", func(t *testing.T) { testComments(t, factory) })
	t.Run("CommentCount", func(t *testing.T) { testCommentCount(t, factory) })

	// Config
	t.Run("Config", func(t *testing.T) { testConfig(t, factory) })
	t.Run("LocalMetadata", func(t *testing.T) { testLocalMetadata(t, factory) })

	// Slots
	t.Run("MetadataSlots", func(t *testing.T) { testMetadataSlots(t, factory) })

	// Statistics
	t.Run("Statistics", func(t *testing.T) { testStatistics(t, factory) })

	// Iterators
	t.Run("IterIssues", func(t *testing.T) { testIterIssues(t, factory) })
	t.Run("IterComments", func(t *testing.T) { testIterComments(t, factory) })

	// Transaction
	t.Run("Transaction", func(t *testing.T) { testTransaction(t, factory) })
}

// --- helpers ---

func ctx() context.Context { return context.Background() }

func seedStore(t *testing.T, s storage.DoltStorage) {
	t.Helper()
	c := ctx()
	must(t, s.SetConfig(c, "issue_prefix", "test"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-1", Title: "Alpha", Priority: 0, IssueType: "bug", Status: types.StatusOpen}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-2", Title: "Beta", Priority: 1, IssueType: "task", Assignee: "alice", Status: types.StatusOpen}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-3", Title: "Gamma", Priority: 2, IssueType: "feature", Status: types.StatusInProgress}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-4", Title: "Delta", Priority: 1, IssueType: "bug", Status: types.StatusClosed}), "actor"))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// withDefaults fills the Status and IssueType that the storage contract
// requires on every issue (the storage layer validates them and does not
// default them; bd's CLI layer is what normally supplies open/task). Tests
// that do not care about these fields can leave them unset and stay terse;
// explicit values set by a test are preserved.
func withDefaults(i *types.Issue) *types.Issue {
	if i.Status == "" {
		i.Status = types.StatusOpen
	}
	if i.IssueType == "" {
		i.IssueType = types.TypeTask
	}
	return i
}

func issueIDs(issues []*types.Issue) []string {
	ids := make([]string, len(issues))
	for i, iss := range issues {
		ids[i] = iss.ID
	}
	sort.Strings(ids)
	return ids
}

// --- Issue CRUD ---

func testCreateAndGet(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "c-1", Title: "Test", Priority: 2}), "actor"))
	got, err := s.GetIssue(ctx(), "c-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Title != "Test" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Status != types.StatusOpen {
		t.Errorf("Status = %q, want open", got.Status)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
}

func testCreateDuplicate(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "d-1", Title: "First"}), "actor"))

	// Creating an issue with an existing ID must not silently fork into a
	// second, divergent row. Backends differ on how they reconcile the second
	// write (beads' storage layer upserts; another backend might error or
	// skip), so the contract asserts only the invariant that matters for data
	// integrity: exactly one row exists for the ID afterward. It deliberately
	// does NOT bless last-writer-wins overwrite — whether a duplicate create
	// should instead surface a conflict is tracked separately.
	_ = s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "d-1", Title: "Second"}), "actor")
	got, err := s.GetIssuesByIDs(ctx(), []string{"d-1"})
	must(t, err)
	if len(got) != 1 {
		t.Errorf("after duplicate create: %d rows for d-1, want exactly 1", len(got))
	}
}

func testGetNotFound(t *testing.T, f Factory) {
	s := f(t)
	_, err := s.GetIssue(ctx(), "nonexistent")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func testGetByExternalRef(t *testing.T, f Factory) {
	s := f(t)
	ref := "gh-42"
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "e-1", Title: "Ext", ExternalRef: &ref}), "actor"))
	got, err := s.GetIssueByExternalRef(ctx(), "gh-42")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef: %v", err)
	}
	if got.ID != "e-1" {
		t.Errorf("ID = %q", got.ID)
	}
	_, err = s.GetIssueByExternalRef(ctx(), "gh-999")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("missing ref: %v, want ErrNotFound", err)
	}
}

func testGetByIDs(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "g-1", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "g-2", Title: "B"}), "a"))
	got, err := s.GetIssuesByIDs(ctx(), []string{"g-1", "missing", "g-2"})
	if err != nil {
		t.Fatalf("GetIssuesByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func testUpdate(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "u-1", Title: "Old", Priority: 1}), "a"))
	must(t, s.UpdateIssue(ctx(), "u-1", map[string]interface{}{"title": "New", "priority": 3}, "a"))
	got, _ := s.GetIssue(ctx(), "u-1")
	if got.Title != "New" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Priority != 3 {
		t.Errorf("Priority = %d", got.Priority)
	}
}

func testUpdatePreservesCreatedAt(t *testing.T, f Factory) {
	s := f(t)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "up-1", Title: "T", CreatedAt: created, CreatedBy: "orig"}), "orig"))
	must(t, s.UpdateIssue(ctx(), "up-1", map[string]interface{}{"title": "Changed"}, "updater"))
	got, _ := s.GetIssue(ctx(), "up-1")
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt changed to %v", got.CreatedAt)
	}
	if got.CreatedBy != "orig" {
		t.Errorf("CreatedBy = %q", got.CreatedBy)
	}
}

func testUpdateNotFound(t *testing.T, f Factory) {
	s := f(t)
	err := s.UpdateIssue(ctx(), "missing", map[string]interface{}{"title": "x"}, "a")
	if err == nil {
		t.Error("expected error")
	}
}

func testUpdateIssueType(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ut-1", Title: "T", IssueType: "task"}), "a"))
	must(t, s.UpdateIssueType(ctx(), "ut-1", "epic", "a"))
	got, _ := s.GetIssue(ctx(), "ut-1")
	if got.IssueType != "epic" {
		t.Errorf("IssueType = %q", got.IssueType)
	}
}

func testCloseAndReopen(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cr-1", Title: "T"}), "a"))
	must(t, s.CloseIssue(ctx(), "cr-1", "done", "closer", "sess"))
	got, _ := s.GetIssue(ctx(), "cr-1")
	if got.Status != types.StatusClosed {
		t.Errorf("after close: Status = %q", got.Status)
	}
	if got.ClosedAt == nil {
		t.Error("ClosedAt is nil")
	}
	must(t, s.ReopenIssue(ctx(), "cr-1", "not done", "opener"))
	got, _ = s.GetIssue(ctx(), "cr-1")
	if got.Status != types.StatusOpen {
		t.Errorf("after reopen: Status = %q", got.Status)
	}
	if got.ClosedAt != nil {
		t.Error("ClosedAt should be nil after reopen")
	}
}

func testDelete(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "del-1", Title: "T"}), "a"))
	must(t, s.DeleteIssue(ctx(), "del-1"))
	_, err := s.GetIssue(ctx(), "del-1")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

func testDeleteNotFound(t *testing.T, f Factory) {
	s := f(t)
	err := s.DeleteIssue(ctx(), "nonexistent")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// --- Search ---

func testSearchNoFilter(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	results, err := s.SearchIssues(ctx(), "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("len = %d, want 4", len(results))
	}
}

func testSearchTextQuery(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	results, _ := s.SearchIssues(ctx(), "Alpha", types.IssueFilter{})
	if len(results) != 1 || results[0].ID != "test-1" {
		t.Errorf("text query 'Alpha': got %v", issueIDs(results))
	}
}

func testSearchStatusFilter(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	open := types.StatusOpen
	results, _ := s.SearchIssues(ctx(), "", types.IssueFilter{Status: &open})
	if len(results) != 2 {
		t.Errorf("status=open: len = %d, want 2", len(results))
	}
}

func testSearchPriorityFilter(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	p := 1
	results, _ := s.SearchIssues(ctx(), "", types.IssueFilter{Priority: &p})
	if len(results) != 2 {
		t.Errorf("priority=1: len = %d, want 2", len(results))
	}
}

func testSearchLimit(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	results, _ := s.SearchIssues(ctx(), "", types.IssueFilter{Limit: 2})
	if len(results) != 2 {
		t.Errorf("limit=2: len = %d", len(results))
	}
}

func testCountIssues(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	count, err := s.CountIssues(ctx(), "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("CountIssues: %v", err)
	}
	if count != 4 {
		t.Errorf("count = %d, want 4", count)
	}
}

func testCountByGroup(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	counts, _ := s.CountIssuesByGroup(ctx(), types.IssueFilter{}, "status")
	if counts["open"] != 2 {
		t.Errorf("open = %d, want 2", counts["open"])
	}
	if counts["closed"] != 1 {
		t.Errorf("closed = %d, want 1", counts["closed"])
	}
}

// --- Dependencies ---

func testAddAndGetDeps(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "dep-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "dep-b", Title: "B"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "dep-b", DependsOnID: "dep-a", Type: types.DepBlocks}, "a"))
	deps, _ := s.GetDependencies(ctx(), "dep-b")
	if len(deps) != 1 || deps[0].ID != "dep-a" {
		t.Errorf("GetDependencies = %v", issueIDs(deps))
	}
	dependents, _ := s.GetDependents(ctx(), "dep-a")
	if len(dependents) != 1 || dependents[0].ID != "dep-b" {
		t.Errorf("GetDependents = %v", issueIDs(dependents))
	}
}

func testRemoveDep(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rd-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rd-b", Title: "B"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "rd-b", DependsOnID: "rd-a", Type: types.DepBlocks}, "a"))
	must(t, s.RemoveDependency(ctx(), "rd-b", "rd-a", "a"))
	deps, _ := s.GetDependencies(ctx(), "rd-b")
	if len(deps) != 0 {
		t.Errorf("after remove: len = %d", len(deps))
	}
}

func testDepCounts(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "dc-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "dc-b", Title: "B"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "dc-b", DependsOnID: "dc-a", Type: types.DepBlocks}, "a"))
	depCount, _ := s.CountDependencies(ctx(), "dc-b")
	if depCount != 1 {
		t.Errorf("CountDependencies = %d", depCount)
	}
	deptCount, _ := s.CountDependents(ctx(), "dc-a")
	if deptCount != 1 {
		t.Errorf("CountDependents = %d", deptCount)
	}
}

// --- Ready/Blocked ---

func testReadyNoDeps(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "r-1", Title: "T", Status: types.StatusOpen}), "a"))
	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	if len(ready) != 1 || ready[0].ID != "r-1" {
		t.Errorf("ready = %v", issueIDs(ready))
	}
}

func testReadyBlockedByOpenDep(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rb-1", Title: "Blocker", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rb-2", Title: "Blocked", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "rb-2", DependsOnID: "rb-1", Type: types.DepBlocks}, "a"))
	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	ids := issueIDs(ready)
	if len(ids) != 1 || ids[0] != "rb-1" {
		t.Errorf("ready = %v, want [rb-1]", ids)
	}
}

func testReadyUnblockedByClose(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ru-1", Title: "Dep", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ru-2", Title: "Waiter", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ru-2", DependsOnID: "ru-1", Type: types.DepBlocks}, "a"))
	must(t, s.CloseIssue(ctx(), "ru-1", "done", "a", "s"))
	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	ids := issueIDs(ready)
	if len(ids) != 1 || ids[0] != "ru-2" {
		t.Errorf("after close: ready = %v, want [ru-2]", ids)
	}
}

func testBlockedIssues(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "bl-1", Title: "Blocker", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "bl-2", Title: "Blocked", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "bl-2", DependsOnID: "bl-1", Type: types.DepBlocks}, "a"))
	blocked, _ := s.GetBlockedIssues(ctx(), types.WorkFilter{})
	if len(blocked) != 1 || blocked[0].ID != "bl-2" {
		t.Errorf("blocked = %v", blocked)
	}
	if blocked[0].BlockedByCount != 1 {
		t.Errorf("BlockedByCount = %d", blocked[0].BlockedByCount)
	}
}

func testEpicsEligible(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ep-1", Title: "Epic", IssueType: types.TypeEpic, Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ep-1a", Title: "Child", Status: types.StatusClosed}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ep-1a", DependsOnID: "ep-1", Type: types.DepParentChild}, "a"))
	epics, _ := s.GetEpicsEligibleForClosure(ctx())
	if len(epics) != 1 || !epics[0].EligibleForClose {
		t.Errorf("expected epic eligible for closure")
	}
}

// --- Labels ---

func testLabels(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "lb-1", Title: "T"}), "a"))
	must(t, s.AddLabel(ctx(), "lb-1", "bug", "a"))
	must(t, s.AddLabel(ctx(), "lb-1", "urgent", "a"))
	labels, _ := s.GetLabels(ctx(), "lb-1")
	if len(labels) != 2 {
		t.Errorf("labels = %v", labels)
	}
	must(t, s.RemoveLabel(ctx(), "lb-1", "bug", "a"))
	labels, _ = s.GetLabels(ctx(), "lb-1")
	if len(labels) != 1 || labels[0] != "urgent" {
		t.Errorf("after remove: labels = %v", labels)
	}
}

func testLabelIdempotent(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "li-1", Title: "T"}), "a"))
	must(t, s.AddLabel(ctx(), "li-1", "x", "a"))
	must(t, s.AddLabel(ctx(), "li-1", "x", "a"))
	labels, _ := s.GetLabels(ctx(), "li-1")
	if len(labels) != 1 {
		t.Errorf("labels = %v, want 1", labels)
	}
}

func testGetIssuesByLabel(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "gl-1", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "gl-2", Title: "B"}), "a"))
	must(t, s.AddLabel(ctx(), "gl-1", "shared", "a"))
	must(t, s.AddLabel(ctx(), "gl-2", "shared", "a"))
	issues, _ := s.GetIssuesByLabel(ctx(), "shared")
	if len(issues) != 2 {
		t.Errorf("GetIssuesByLabel: len = %d", len(issues))
	}
}

// --- Comments ---

func testComments(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cm-1", Title: "T"}), "a"))
	c1, err := s.AddIssueComment(ctx(), "cm-1", "alice", "First")
	if err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	if c1.Text != "First" {
		t.Errorf("comment text = %q", c1.Text)
	}
	_, _ = s.AddIssueComment(ctx(), "cm-1", "bob", "Second")
	comments, _ := s.GetIssueComments(ctx(), "cm-1")
	if len(comments) != 2 {
		t.Errorf("comments len = %d", len(comments))
	}
	// Verify chronological order.
	if comments[0].CreatedAt.After(comments[1].CreatedAt) {
		t.Error("comments not in chronological order")
	}
}

func testCommentCount(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cc-1", Title: "T"}), "a"))
	_, _ = s.AddIssueComment(ctx(), "cc-1", "a", "one")
	_, _ = s.AddIssueComment(ctx(), "cc-1", "a", "two")
	count, _ := s.CountIssueComments(ctx(), "cc-1")
	if count != 2 {
		t.Errorf("count = %d", count)
	}
}

// --- Config ---

func testConfig(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.SetConfig(ctx(), "key1", "val1"))
	must(t, s.SetConfig(ctx(), "key2", "val2"))
	v, _ := s.GetConfig(ctx(), "key1")
	if v != "val1" {
		t.Errorf("GetConfig = %q", v)
	}
	all, _ := s.GetAllConfig(ctx())
	if len(all) < 2 {
		t.Errorf("GetAllConfig len = %d", len(all))
	}
}

func testLocalMetadata(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.SetLocalMetadata(ctx(), "lk", "lv"))
	v, _ := s.GetLocalMetadata(ctx(), "lk")
	if v != "lv" {
		t.Errorf("GetLocalMetadata = %q", v)
	}
	// Missing key returns empty string, not error.
	v, err := s.GetLocalMetadata(ctx(), "missing")
	if err != nil {
		t.Errorf("missing key error: %v", err)
	}
	if v != "" {
		t.Errorf("missing key value = %q", v)
	}
}

// --- Metadata Slots ---

func testMetadataSlots(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "sl-1", Title: "T"}), "a"))
	must(t, s.SlotSet(ctx(), "sl-1", "mykey", "myval", "a"))
	v, _ := s.SlotGet(ctx(), "sl-1", "mykey")
	if v != "myval" {
		t.Errorf("SlotGet = %q", v)
	}
	must(t, s.SlotClear(ctx(), "sl-1", "mykey", "a"))
	v, _ = s.SlotGet(ctx(), "sl-1", "mykey")
	if v != "" {
		t.Errorf("after clear: SlotGet = %q", v)
	}
}

// --- Statistics ---

func testStatistics(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	stats, err := s.GetStatistics(ctx())
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if stats.TotalIssues != 4 {
		t.Errorf("TotalIssues = %d", stats.TotalIssues)
	}
	if stats.ClosedIssues != 1 {
		t.Errorf("ClosedIssues = %d", stats.ClosedIssues)
	}
}

// --- Iterators ---

func testIterIssues(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	it, err := s.IterIssues(ctx(), "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("IterIssues: %v", err)
	}
	defer func() { _ = it.Close() }()
	count := 0
	for it.Next(ctx()) {
		count++
		if it.Value().ID == "" {
			t.Error("empty ID from iterator")
		}
	}
	if err := it.Err(); err != nil {
		t.Fatalf("Iter error: %v", err)
	}
	if count != 4 {
		t.Errorf("iterated %d, want 4", count)
	}
}

func testIterComments(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ic-1", Title: "T"}), "a"))
	_, _ = s.AddIssueComment(ctx(), "ic-1", "a", "one")
	_, _ = s.AddIssueComment(ctx(), "ic-1", "a", "two")
	it, err := s.IterIssueComments(ctx(), "ic-1")
	if err != nil {
		t.Fatalf("IterIssueComments: %v", err)
	}
	defer func() { _ = it.Close() }()
	count := 0
	for it.Next(ctx()) {
		count++
	}
	if count != 2 {
		t.Errorf("iterated %d comments, want 2", count)
	}
}

// --- Transaction ---

func testTransaction(t *testing.T, f Factory) {
	s := f(t)
	err := s.RunInTransaction(ctx(), "test", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "tx-1", Title: "In TX"}), "a"); err != nil {
			return err
		}
		return tx.AddLabel(ctx(), "tx-1", "from-tx", "a")
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	got, err := s.GetIssue(ctx(), "tx-1")
	if err != nil {
		t.Fatalf("GetIssue after tx: %v", err)
	}
	if got.Title != "In TX" {
		t.Errorf("title = %q", got.Title)
	}
	labels, _ := s.GetLabels(ctx(), "tx-1")
	found := false
	for _, l := range labels {
		if l == "from-tx" {
			found = true
		}
	}
	if !found {
		t.Errorf("label 'from-tx' not found, got %v", labels)
	}
}
