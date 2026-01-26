package sqlite

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// testEnv provides a test environment with common setup and helpers.
// Use newTestEnv(t) to create a test environment with automatic cleanup.
type testEnv struct {
	t     *testing.T
	Store *SQLiteStorage
	Ctx   context.Context
}

// newTestEnv creates a new test environment with a configured store.
// The store is automatically cleaned up when the test completes.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	store := newTestStore(t, "")
	return &testEnv{
		t:     t,
		Store: store,
		Ctx:   context.Background(),
	}
}

// CreateIssue creates a test issue with the given title and defaults.
// Returns the created issue with ID populated.
func (e *testEnv) CreateIssue(title string) *types.Issue {
	e.t.Helper()
	return e.CreateIssueWith(title, types.StatusOpen, 2, types.TypeTask)
}

// CreateIssueWith creates a test issue with specified attributes.
func (e *testEnv) CreateIssueWith(title string, status types.Status, priority int, issueType types.IssueType) *types.Issue {
	e.t.Helper()
	issue := &types.Issue{
		Title:     title,
		Status:    status,
		Priority:  priority,
		IssueType: issueType,
	}
	if err := e.Store.CreateIssue(e.Ctx, issue, "test-user"); err != nil {
		e.t.Fatalf("CreateIssue(%q) failed: %v", title, err)
	}
	return issue
}

// CreateIssueWithAssignee creates a test issue with an assignee.
func (e *testEnv) CreateIssueWithAssignee(title, assignee string) *types.Issue {
	e.t.Helper()
	issue := &types.Issue{
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Assignee:  assignee,
	}
	if err := e.Store.CreateIssue(e.Ctx, issue, "test-user"); err != nil {
		e.t.Fatalf("CreateIssue(%q) failed: %v", title, err)
	}
	return issue
}

// CreateEpic creates an epic issue.
func (e *testEnv) CreateEpic(title string) *types.Issue {
	e.t.Helper()
	return e.CreateIssueWith(title, types.StatusOpen, 1, types.TypeEpic)
}

// CreateBug creates a bug issue.
func (e *testEnv) CreateBug(title string, priority int) *types.Issue {
	e.t.Helper()
	return e.CreateIssueWith(title, types.StatusOpen, priority, types.TypeBug)
}

// AddDep adds a blocking dependency (issue depends on dependsOn).
func (e *testEnv) AddDep(issue, dependsOn *types.Issue) {
	e.t.Helper()
	e.AddDepType(issue, dependsOn, types.DepBlocks)
}

// AddDepType adds a dependency with the specified type.
func (e *testEnv) AddDepType(issue, dependsOn *types.Issue, depType types.DependencyType) {
	e.t.Helper()
	dep := &types.Dependency{
		IssueID:     issue.ID,
		DependsOnID: dependsOn.ID,
		Type:        depType,
	}
	if err := e.Store.AddDependency(e.Ctx, dep, "test-user"); err != nil {
		e.t.Fatalf("AddDependency(%s -> %s) failed: %v", issue.ID, dependsOn.ID, err)
	}
}

// AddParentChild adds a parent-child dependency (child belongs to parent).
func (e *testEnv) AddParentChild(child, parent *types.Issue) {
	e.t.Helper()
	e.AddDepType(child, parent, types.DepParentChild)
}

// Close closes the issue with the given reason.
func (e *testEnv) Close(issue *types.Issue, reason string) {
	e.t.Helper()
	if err := e.Store.CloseIssue(e.Ctx, issue.ID, reason, "test-user", ""); err != nil {
		e.t.Fatalf("CloseIssue(%s) failed: %v", issue.ID, err)
	}
}

// GetReadyWork gets ready work with the given filter.
func (e *testEnv) GetReadyWork(filter types.WorkFilter) []*types.Issue {
	e.t.Helper()
	ready, err := e.Store.GetReadyWork(e.Ctx, filter)
	if err != nil {
		e.t.Fatalf("GetReadyWork failed: %v", err)
	}
	return ready
}

// GetReadyIDs returns a map of issue IDs that are ready (open status).
func (e *testEnv) GetReadyIDs() map[string]bool {
	e.t.Helper()
	ready := e.GetReadyWork(types.WorkFilter{Status: types.StatusOpen})
	ids := make(map[string]bool)
	for _, issue := range ready {
		ids[issue.ID] = true
	}
	return ids
}

// AssertReady asserts that the issue is in the ready work list.
func (e *testEnv) AssertReady(issue *types.Issue) {
	e.t.Helper()
	ids := e.GetReadyIDs()
	if !ids[issue.ID] {
		e.t.Errorf("expected %s (%s) to be ready, but it was blocked", issue.ID, issue.Title)
	}
}

// AssertBlocked asserts that the issue is NOT in the ready work list.
func (e *testEnv) AssertBlocked(issue *types.Issue) {
	e.t.Helper()
	ids := e.GetReadyIDs()
	if ids[issue.ID] {
		e.t.Errorf("expected %s (%s) to be blocked, but it was ready", issue.ID, issue.Title)
	}
}

// CreateIssueWithID creates a test issue with an explicit ID.
// Useful for testing ID-based filtering (e.g., mol step exclusion).
func (e *testEnv) CreateIssueWithID(id, title string) *types.Issue {
	e.t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := e.Store.CreateIssue(e.Ctx, issue, "test-user"); err != nil {
		e.t.Fatalf("CreateIssue(%q, %q) failed: %v", id, title, err)
	}
	return issue
}

// newTestStore creates a SQLiteStorage with issue_prefix configured (bd-166)
// This prevents "database not initialized" errors in tests
//
// Test Isolation Pattern (bd-2e80):
// By default, uses "file::memory:?mode=memory&cache=private" for proper test isolation.
// The standard ":memory:" creates a SHARED database across all tests in the same process,
// which can cause test interference and flaky behavior. The private mode ensures each
// test gets its own isolated in-memory database.
//
// To override (e.g., for file-based tests), pass a custom dbPath:
//   - For temp files: t.TempDir()+"/test.db"
//   - For shared memory (not recommended): ":memory:"
func newTestStore(t *testing.T, dbPath string) *SQLiteStorage {
	t.Helper()

	// Default to temp file for test isolation
	// File-based databases are more reliable than in-memory for connection pool scenarios
	if dbPath == "" {
		dbPath = t.TempDir() + "/test.db"
	}

	ctx := context.Background()
	store, err := New(ctx, dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Fatalf("Failed to close test database: %v", cerr)
		}
	})

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		_ = store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	return store
}
