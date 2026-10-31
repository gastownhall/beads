package main

import (
	"context"
	"errors"
	"fmt"
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
	mintID  string // the ID an auto-minted create receives in this transaction
}

func (t *createAtomicFakeTx) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	if issue.ID == "" {
		issue.ID = t.mintID
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
	// lostConflicts makes the first transactions lose a write conflict at
	// commit: the callback runs, nothing it wrote lands, and the failure is
	// marked as one a replay may retry.
	lostConflicts int
}

func (s *createAtomicFakeStore) IsInfraTypeCtx(context.Context, types.IssueType) bool { return false }

// SearchIssues + GetConfig satisfy utils.ResolvePartialID for --deps target
// normalization (GH#5005). Known IDs resolve to themselves; unknown bare
// slugs become prefix-qualified and also resolve so unit tests can exercise
// the resolve path without a real store.
func (s *createAtomicFakeStore) GetConfig(_ context.Context, key string) (string, error) {
	if key == "issue_prefix" {
		return "fake", nil
	}
	return "", nil
}

func (s *createAtomicFakeStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	out := make([]*types.Issue, 0, len(filter.IDs))
	for _, id := range filter.IDs {
		if id == "" {
			continue
		}
		// Real stores never mint bare slugs as issue IDs; only return hits for
		// prefix-qualified forms so ResolvePartialID exercises the bare→prefix
		// path used by create --deps (GH#5005).
		if !strings.Contains(id, "-") {
			continue
		}
		out = append(out, &types.Issue{ID: id, Title: "resolved", Status: types.StatusOpen, IssueType: types.TypeTask})
	}
	return out, nil
}

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
	// Each transaction mints the next number, as counter mode does when a
	// concurrent writer has taken the previous one.
	tx := &createAtomicFakeTx{failDep: s.failDep, mintID: fmt.Sprintf("fake-%d", s.txCount)}
	if err := fn(tx); err != nil {
		// Rollback: discard everything recorded inside the transaction.
		return err
	}
	if s.txCount <= s.lostConflicts {
		return storage.MarkTransactionConflict(errors.New("sql commit (regular): serialization failure"))
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

// TestCreateIssueWithDepsReplaysFromTheCallersRequest: a create that loses a
// write conflict is replayed, and the replay must mint its own ID. Re-using
// the rolled-back attempt's ID as if the caller had asked for it would take
// the explicit-ID path and could overwrite the writer that won the conflict.
func TestCreateIssueWithDepsReplaysFromTheCallersRequest(t *testing.T) {
	saveCreateAtomicGlobals(t)
	st := &createAtomicFakeStore{lostConflicts: 1}
	issue := &types.Issue{Title: "replayed create", Status: types.StatusOpen, IssueType: types.TypeTask}

	if err := createIssueWithDeps(context.Background(), st, issue, "tester", createDepEdges{parentID: "fake-parent"}); err != nil {
		t.Fatalf("createIssueWithDeps was not replayed to success: %v", err)
	}
	if st.txCount != 2 {
		t.Fatalf("transactions = %d, want 2 (one lost conflict, one replay)", st.txCount)
	}
	if len(st.committedIssues) != 1 || st.committedIssues[0].ID != "fake-2" {
		var ids []string
		for _, committed := range st.committedIssues {
			ids = append(ids, committed.ID)
		}
		t.Fatalf("committed issues = %v, want the one the replay minted (fake-2), not the rolled-back attempt's fake-1", ids)
	}
	if len(st.committedDeps) != 1 || st.committedDeps[0].IssueID != "fake-2" {
		t.Fatalf("committed deps = %+v, want the parent edge from fake-2", st.committedDeps)
	}
	if issue.ID != "fake-2" {
		t.Fatalf("caller's issue reports %q, want the committed fake-2", issue.ID)
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
