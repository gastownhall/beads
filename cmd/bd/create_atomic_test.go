package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// createAtomicFakeTx records mutations made inside a fake transaction.
type createAtomicFakeTx struct {
	storage.Transaction
	created []*types.Issue
	deps    []*types.Dependency
	failDep string // DependsOnID whose AddDependency fails
}

func (t *createAtomicFakeTx) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	if issue.ID == "" {
		issue.ID = "fake-1"
	}
	cp := *issue
	t.created = append(t.created, &cp)
	return nil
}

func (t *createAtomicFakeTx) AddDependency(_ context.Context, dep *types.Dependency, _ string) error {
	if t.failDep != "" && dep.DependsOnID == t.failDep {
		return errors.New("issue " + dep.DependsOnID + " not found")
	}
	cp := *dep
	t.deps = append(t.deps, &cp)
	return nil
}

// createAtomicFakeStore counts store-level calls and applies transaction
// writes only when the callback commits (returns nil).
type createAtomicFakeStore struct {
	storage.DoltStorage
	txCount          int
	storeCreateCalls int
	committedIssues  []*types.Issue
	committedDeps    []*types.Dependency
	failDep          string
}

func (s *createAtomicFakeStore) IsInfraTypeCtx(context.Context, types.IssueType) bool { return false }

func (s *createAtomicFakeStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	s.storeCreateCalls++
	if issue.ID == "" {
		issue.ID = "fake-1"
	}
	cp := *issue
	s.committedIssues = append(s.committedIssues, &cp)
	return nil
}

func (s *createAtomicFakeStore) RunInTransaction(_ context.Context, _ string, fn func(tx storage.Transaction) error) error {
	s.txCount++
	tx := &createAtomicFakeTx{failDep: s.failDep}
	if err := fn(tx); err != nil {
		// Rollback: discard everything recorded inside the transaction.
		return err
	}
	s.committedIssues = append(s.committedIssues, tx.created...)
	s.committedDeps = append(s.committedDeps, tx.deps...)
	return nil
}

// saveCreateAtomicGlobals snapshots the auto-commit globals that
// transactHonoringAutoCommit reads and writes.
func saveCreateAtomicGlobals(t *testing.T) {
	t.Helper()
	origAutoCommit := doltAutoCommit
	origExplicit := commandDidExplicitDoltCommit
	t.Cleanup(func() {
		doltAutoCommit = origAutoCommit
		commandDidExplicitDoltCommit = origExplicit
	})
}

func TestCreateIssueWithDepsRunsOneTransaction(t *testing.T) {
	saveCreateAtomicGlobals(t)
	st := &createAtomicFakeStore{}
	issue := &types.Issue{Title: "atomic create", Status: types.StatusOpen, IssueType: types.TypeTask}

	edges := createDepEdges{
		parentID: "fake-parent",
		specs: []domain.DependencySpec{
			{Type: types.DepBlocks, TargetID: "fake-dep1"},
			{Type: types.DepBlocks, TargetID: "fake-dep2", SwapDirection: true},
		},
		waitsFor: &domain.WaitsForSpec{SpawnerID: "fake-spawner", Gate: types.WaitsForAllChildren},
	}
	if err := createIssueWithDeps(context.Background(), st, issue, "tester", edges); err != nil {
		t.Fatalf("createIssueWithDeps: %v", err)
	}

	if st.txCount != 1 {
		t.Errorf("RunInTransaction calls = %d, want 1", st.txCount)
	}
	if st.storeCreateCalls != 0 {
		t.Errorf("store-level CreateIssue calls = %d, want 0 (create must run inside the transaction)", st.storeCreateCalls)
	}
	if len(st.committedIssues) != 1 || st.committedIssues[0].Title != "atomic create" {
		t.Fatalf("committed issues = %+v, want the one created issue", st.committedIssues)
	}
	if len(st.committedDeps) != 4 {
		t.Fatalf("committed deps = %d, want 4 (parent + 2 specs + waits-for)", len(st.committedDeps))
	}

	parent := st.committedDeps[0]
	if parent.Type != types.DepParentChild || parent.IssueID != "fake-1" || parent.DependsOnID != "fake-parent" {
		t.Errorf("parent edge = %+v, want fake-1 -> fake-parent (parent-child)", parent)
	}
	plain := st.committedDeps[1]
	if plain.IssueID != "fake-1" || plain.DependsOnID != "fake-dep1" {
		t.Errorf("plain edge = %+v, want fake-1 -> fake-dep1", plain)
	}
	swapped := st.committedDeps[2]
	if swapped.IssueID != "fake-dep2" || swapped.DependsOnID != "fake-1" {
		t.Errorf("swapped edge = %+v, want fake-dep2 -> fake-1", swapped)
	}
	wf := st.committedDeps[3]
	if wf.Type != types.DepWaitsFor || wf.DependsOnID != "fake-spawner" || !strings.Contains(wf.Metadata, types.WaitsForAllChildren) {
		t.Errorf("waits-for edge = %+v, want fake-1 -> fake-spawner with %q gate metadata", wf, types.WaitsForAllChildren)
	}
}

func TestCreateIssueWithDepsRollsBackOnDepFailure(t *testing.T) {
	saveCreateAtomicGlobals(t)
	st := &createAtomicFakeStore{failDep: "fake-missing"}
	issue := &types.Issue{Title: "doomed create", Status: types.StatusOpen, IssueType: types.TypeTask}

	edges := createDepEdges{
		specs: []domain.DependencySpec{
			{Type: types.DepBlocks, TargetID: "fake-dep1"},
			{Type: types.DepBlocks, TargetID: "fake-missing"},
		},
	}
	err := createIssueWithDeps(context.Background(), st, issue, "tester", edges)
	if err == nil {
		t.Fatal("expected error when a dep-add fails")
	}
	if !strings.Contains(err.Error(), "fake-missing") {
		t.Errorf("error should name the failing dependency, got: %v", err)
	}
	if len(st.committedIssues) != 0 {
		t.Errorf("committed issues = %+v, want none (create must roll back with the failed dep)", st.committedIssues)
	}
	if len(st.committedDeps) != 0 {
		t.Errorf("committed deps = %+v, want none", st.committedDeps)
	}
}

func TestCreateIssueWithDepsEmptyEdgesUsesStoreCreate(t *testing.T) {
	saveCreateAtomicGlobals(t)
	st := &createAtomicFakeStore{}
	issue := &types.Issue{Title: "bare create", Status: types.StatusOpen, IssueType: types.TypeTask}

	if err := createIssueWithDeps(context.Background(), st, issue, "tester", createDepEdges{}); err != nil {
		t.Fatalf("createIssueWithDeps: %v", err)
	}
	if st.storeCreateCalls != 1 {
		t.Errorf("store-level CreateIssue calls = %d, want 1 (bare create keeps the store path)", st.storeCreateCalls)
	}
	if st.txCount != 0 {
		t.Errorf("RunInTransaction calls = %d, want 0 for a bare create", st.txCount)
	}
}
