package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

type graphApplyFakeStore struct {
	storage.DoltStorage
	issues  map[string]*types.Issue
	deps    []*types.Dependency
	addOpts []storage.DependencyAddOptions
	nextID  int
}

func newGraphApplyFakeStore() *graphApplyFakeStore {
	return &graphApplyFakeStore{
		issues: make(map[string]*types.Issue),
	}
}

func (s *graphApplyFakeStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	cp := *issue
	if cp.ID == "" {
		cp.ID = s.nextIssueID(&cp)
	}
	issue.ID = cp.ID
	s.issues[cp.ID] = &cp
	return nil
}

func (s *graphApplyFakeStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	issue, ok := s.issues[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	cp := *issue
	return &cp, nil
}

func (s *graphApplyFakeStore) GetDependenciesWithMetadata(_ context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	var out []*types.IssueWithDependencyMetadata
	for _, dep := range s.deps {
		if dep.IssueID != issueID {
			continue
		}
		issue, ok := s.issues[dep.DependsOnID]
		if !ok {
			continue
		}
		cp := *issue
		out = append(out, &types.IssueWithDependencyMetadata{
			Issue:          cp,
			DependencyType: dep.Type,
		})
	}
	return out, nil
}

func (s *graphApplyFakeStore) GetDependencyRecords(_ context.Context, issueID string) ([]*types.Dependency, error) {
	var out []*types.Dependency
	for _, dep := range s.deps {
		if dep.IssueID != issueID {
			continue
		}
		cp := *dep
		out = append(out, &cp)
	}
	return out, nil
}

func (s *graphApplyFakeStore) RunInTransaction(ctx context.Context, _ string, fn func(storage.Transaction) error) error {
	txStore := s.clone()
	tx := &graphApplyFakeTx{store: txStore}
	if err := fn(tx); err != nil {
		return err
	}
	s.issues = txStore.issues
	s.deps = txStore.deps
	s.addOpts = txStore.addOpts
	s.nextID = txStore.nextID
	return nil
}

func (s *graphApplyFakeStore) clone() *graphApplyFakeStore {
	cp := &graphApplyFakeStore{
		issues:  make(map[string]*types.Issue, len(s.issues)),
		deps:    make([]*types.Dependency, 0, len(s.deps)),
		addOpts: append([]storage.DependencyAddOptions(nil), s.addOpts...),
		nextID:  s.nextID,
	}
	for id, issue := range s.issues {
		issueCopy := *issue
		cp.issues[id] = &issueCopy
	}
	for _, dep := range s.deps {
		depCopy := *dep
		cp.deps = append(cp.deps, &depCopy)
	}
	return cp
}

func (s *graphApplyFakeStore) nextIssueID(issue *types.Issue) string {
	s.nextID++
	if issue.Ephemeral {
		return fmt.Sprintf("ga-wisp-%d", s.nextID)
	}
	return fmt.Sprintf("ga-%d", s.nextID)
}

type graphApplyFakeTx struct {
	storage.Transaction
	store *graphApplyFakeStore
}

func (tx *graphApplyFakeTx) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	for _, issue := range issues {
		if err := tx.store.CreateIssue(ctx, issue, actor); err != nil {
			return err
		}
	}
	return nil
}

func (tx *graphApplyFakeTx) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return tx.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{})
}

func (tx *graphApplyFakeTx) AddDependencyWithOptions(_ context.Context, dep *types.Dependency, _ string, opts storage.DependencyAddOptions) error {
	tx.store.addOpts = append(tx.store.addOpts, opts)
	for _, existing := range tx.store.deps {
		if existing.IssueID == dep.IssueID && existing.DependsOnID == dep.DependsOnID {
			if existing.Type == dep.Type {
				// Real storage accepts this duplicate and updates metadata; graph
				// apply never sets dependency metadata, so the fake treats it as
				// idempotent for duplicate-suppression tests.
				return nil
			}
			return fmt.Errorf("dependency already exists with type %s", existing.Type)
		}
	}
	if isGraphApplyFakeBlocking(dep.Type) && !opts.SkipCycleCheck && tx.hasBlockingPath(dep.DependsOnID, dep.IssueID) {
		return fmt.Errorf("adding dependency would create a cycle")
	}
	depCopy := *dep
	tx.store.deps = append(tx.store.deps, &depCopy)
	return nil
}

func (tx *graphApplyFakeTx) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	return tx.store.GetDependencyRecords(ctx, issueID)
}

func (tx *graphApplyFakeTx) AddLabel(context.Context, string, string, string) error {
	return nil
}

func (tx *graphApplyFakeTx) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	issue, ok := tx.store.issues[id]
	if !ok {
		return storage.ErrNotFound
	}
	if assignee, ok := updates["assignee"].(string); ok {
		issue.Assignee = assignee
	}
	return nil
}

func (tx *graphApplyFakeTx) hasBlockingPath(fromID, toID string) bool {
	seen := make(map[string]bool)
	var visit func(string) bool
	visit = func(id string) bool {
		if id == toID {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		for _, dep := range tx.store.deps {
			if dep.IssueID != id || !isGraphApplyFakeBlocking(dep.Type) {
				continue
			}
			if visit(dep.DependsOnID) {
				return true
			}
		}
		return false
	}
	return visit(fromID)
}

func isGraphApplyFakeBlocking(depType types.DependencyType) bool {
	return graphApplyCycleRelevantDependencyType(depType)
}

func withGraphApplyFakeStore(t *testing.T) (context.Context, *graphApplyFakeStore) {
	t.Helper()
	ctx := context.Background()
	fakeStore := newGraphApplyFakeStore()
	oldStore, oldCtx, oldActor := store, rootCtx, actor
	store, rootCtx, actor = fakeStore, ctx, "graph-apply-test"
	t.Cleanup(func() {
		store, rootCtx, actor = oldStore, oldCtx, oldActor
	})
	return ctx, fakeStore
}

func TestExecuteGraphApplyUnitRejectsMixedLocalExternalBlockingCycle(t *testing.T) {
	ctx, fakeStore := withGraphApplyFakeStore(t)
	if err := fakeStore.CreateIssue(ctx, &types.Issue{
		ID:        "ga-existing",
		Title:     "Existing",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, actor); err != nil {
		t.Fatalf("CreateIssue(existing): %v", err)
	}

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task"},
			{Key: "b", Title: "B", Type: "task"},
		},
		Edges: []GraphApplyEdge{
			{FromID: "ga-existing", ToKey: "a", Type: "blocks"},
			{FromKey: "b", ToID: "ga-existing", Type: "blocks"},
			{FromKey: "a", ToKey: "b", Type: "blocks"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected mixed local/external blocking cycle to be rejected")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error = %q, want cycle rejection", err.Error())
	}
}

func TestExecuteGraphApplyUnitSkipsSQLCycleChecksAfterGraphPreflight(t *testing.T) {
	ctx, fakeStore := withGraphApplyFakeStore(t)
	if err := fakeStore.CreateIssue(ctx, &types.Issue{
		ID:        "ga-existing",
		Title:     "Existing",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, actor); err != nil {
		t.Fatalf("CreateIssue(existing): %v", err)
	}

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task"},
			{Key: "b", Title: "B", Type: "task"},
		},
		Edges: []GraphApplyEdge{
			{FromID: "ga-existing", ToKey: "a", Type: "blocks"},
			{FromKey: "a", ToKey: "b", Type: "blocks"},
			{FromKey: "b", ToID: "external:other:shipped", Type: "blocks"},
		},
	}

	if _, err := executeGraphApply(ctx, plan, GraphApplyOptions{}); err != nil {
		t.Fatalf("executeGraphApply: %v", err)
	}
	if len(fakeStore.addOpts) != len(plan.Edges) {
		t.Fatalf("AddDependencyWithOptions calls = %d, want %d", len(fakeStore.addOpts), len(plan.Edges))
	}
	for i, opts := range fakeStore.addOpts {
		if !opts.SkipCycleCheck {
			t.Fatalf("edge %d SkipCycleCheck = false, want true", i)
		}
	}
}

// TestExecuteGraphApplyUnitAllowsBlockingThroughExistingParentChild pins the
// rule that the whole-graph blocking-cycle preflight mirrors the storage SQL
// cycle check: a planned blocking edge whose only return path runs through an
// existing parent-child dep must be allowed (plain `bd dep add` allows it), so
// the preflight's existing-edge walk is restricted to blocking dep types.
func TestExecuteGraphApplyUnitAllowsBlockingThroughExistingParentChild(t *testing.T) {
	ctx, fakeStore := withGraphApplyFakeStore(t)
	for _, id := range []string{"ga-parent", "ga-child"} {
		if err := fakeStore.CreateIssue(ctx, &types.Issue{
			ID:        id,
			Title:     id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}, actor); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	// Existing parent-child dep: ga-child is a child of ga-parent.
	fakeStore.deps = append(fakeStore.deps, &types.Dependency{
		IssueID:     "ga-child",
		DependsOnID: "ga-parent",
		Type:        types.DepParentChild,
	})

	plan := &GraphApplyPlan{
		Edges: []GraphApplyEdge{
			{FromID: "ga-parent", ToID: "ga-child", Type: "blocks"},
		},
	}

	if _, err := executeGraphApply(ctx, plan, GraphApplyOptions{}); err != nil {
		t.Fatalf("blocking edge closing a cycle only through an existing parent-child dep must be allowed, got: %v", err)
	}
}

// TestExecuteGraphApplyUnitRejectsBlockingThroughExistingBlocking is the
// companion guard: a planned blocking edge that closes a cycle through an
// existing blocking dep must still be rejected by the preflight.
func TestExecuteGraphApplyUnitRejectsBlockingThroughExistingBlocking(t *testing.T) {
	ctx, fakeStore := withGraphApplyFakeStore(t)
	for _, id := range []string{"ga-x", "ga-y"} {
		if err := fakeStore.CreateIssue(ctx, &types.Issue{
			ID:        id,
			Title:     id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}, actor); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	// Existing blocking dep: ga-y blocks ga-x.
	fakeStore.deps = append(fakeStore.deps, &types.Dependency{
		IssueID:     "ga-y",
		DependsOnID: "ga-x",
		Type:        types.DepBlocks,
	})

	plan := &GraphApplyPlan{
		Edges: []GraphApplyEdge{
			{FromID: "ga-x", ToID: "ga-y", Type: "blocks"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected blocking cycle through existing blocking dep to be rejected")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error = %q, want cycle rejection", err.Error())
	}
}

func TestExecuteGraphApplyUnitRejectsBlockingChildToParentDuplicate(t *testing.T) {
	ctx, _ := withGraphApplyFakeStore(t)

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root", Type: "epic"},
			{Key: "child", Title: "Child", Type: "task", ParentKey: "root"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "child", ToKey: "root"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected default blocking child-to-parent duplicate to be rejected")
	}
	if !strings.Contains(err.Error(), "parent-child") {
		t.Fatalf("error = %q, want parent-child duplicate rejection", err.Error())
	}
}

func TestExecuteGraphApplyUnitRejectsReverseParentToChildBlockingEdge(t *testing.T) {
	tests := []struct {
		name       string
		depType    string
		externalID bool
	}{
		{name: "local blocks", depType: "blocks"},
		{name: "local conditional blocks", depType: "conditional-blocks"},
		{name: "external blocks", depType: "blocks", externalID: true},
		{name: "external conditional blocks", depType: "conditional-blocks", externalID: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, fakeStore := withGraphApplyFakeStore(t)
			nodes := []GraphApplyNode{
				{Key: "root", Title: "Root", Type: "epic"},
				{Key: "child", Title: "Child", Type: "task", ParentKey: "root"},
			}
			edge := GraphApplyEdge{FromKey: "root", ToKey: "child", Type: tt.depType}
			if tt.externalID {
				if err := fakeStore.CreateIssue(ctx, &types.Issue{
					ID:        "ga-parent",
					Title:     "Existing Parent",
					Status:    types.StatusOpen,
					Priority:  2,
					IssueType: types.TypeEpic,
				}, actor); err != nil {
					t.Fatalf("CreateIssue(existing parent): %v", err)
				}
				nodes = []GraphApplyNode{
					{Key: "child", Title: "Child", Type: "task", ParentID: "ga-parent"},
				}
				edge = GraphApplyEdge{FromID: "ga-parent", ToKey: "child", Type: tt.depType}
			}

			plan := &GraphApplyPlan{
				Nodes: nodes,
				Edges: []GraphApplyEdge{edge},
			}

			_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
			if err == nil {
				t.Fatal("expected reverse parent-to-child blocking edge to be rejected")
			}
			if !strings.Contains(err.Error(), "parent-child") {
				t.Fatalf("error = %q, want parent-child reverse rejection", err.Error())
			}
		})
	}
}

func TestExecuteGraphApplyUnitRejectsExternalParentTransitiveBlockingPath(t *testing.T) {
	ctx, fakeStore := withGraphApplyFakeStore(t)
	if err := fakeStore.CreateIssue(ctx, &types.Issue{
		ID:        "ga-parent",
		Title:     "Existing Parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}, actor); err != nil {
		t.Fatalf("CreateIssue(existing parent): %v", err)
	}

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "mid", Title: "Middle", Type: "task"},
			{Key: "child", Title: "Child", Type: "task", ParentID: "ga-parent"},
		},
		Edges: []GraphApplyEdge{
			{FromID: "ga-parent", ToKey: "mid", Type: "blocks"},
			{FromKey: "mid", ToKey: "child", Type: "blocks"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected external parent transitive blocking path to be rejected")
	}
	if got, want := err.Error(), "planned blocking dependencies create a path from parent"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}
	if _, ok := fakeStore.issues["ga-1"]; ok {
		t.Fatal("transaction committed graph issues after validation failure")
	}
}

func TestExecuteGraphApplyUnitRejectsStoredPrefixParentBlockingPath(t *testing.T) {
	ctx, fakeStore := withGraphApplyFakeStore(t)
	for _, issue := range []*types.Issue{
		{
			ID:        "ga-parent",
			Title:     "Existing Parent",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		},
		{
			ID:        "ga-existing-mid",
			Title:     "Existing Middle",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		},
	} {
		if err := fakeStore.CreateIssue(ctx, issue, actor); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	fakeStore.deps = append(fakeStore.deps, &types.Dependency{
		IssueID:     "ga-parent",
		DependsOnID: "ga-existing-mid",
		Type:        types.DepBlocks,
	})

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "child", Title: "Child", Type: "task", ParentID: "ga-parent"},
		},
		Edges: []GraphApplyEdge{
			{FromID: "ga-existing-mid", ToKey: "child", Type: "blocks"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected stored-prefix parent blocking path to be rejected")
	}
	if got, want := err.Error(), "planned blocking dependencies create a path from parent"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}
	if _, ok := fakeStore.issues["ga-1"]; ok {
		t.Fatal("transaction committed graph issues after validation failure")
	}
}

func TestExecuteGraphApplyUnitRejectsTransitiveParentChainBlockingCycle(t *testing.T) {
	ctx, fakeStore := withGraphApplyFakeStore(t)
	if err := fakeStore.CreateIssue(ctx, &types.Issue{
		ID:        "ga-existing-parent",
		Title:     "Existing Parent",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeEpic,
	}, actor); err != nil {
		t.Fatalf("CreateIssue(existing parent): %v", err)
	}

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task", ParentKey: "b"},
			{Key: "b", Title: "B", Type: "task", ParentID: "ga-existing-parent"},
		},
		Edges: []GraphApplyEdge{
			{FromID: "ga-existing-parent", ToKey: "a", Type: "blocks"},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected transitive parent-chain blocking cycle to be rejected")
	}
	if got, want := err.Error(), "planned blocking dependencies create a path from parent"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}
	if _, ok := fakeStore.issues["ga-1"]; ok {
		t.Fatal("transaction committed graph issues after validation failure")
	}
}

func TestExecuteGraphApplyUnitRejectsReverseParentChildEdgeCycle(t *testing.T) {
	ctx, fakeStore := withGraphApplyFakeStore(t)

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root", Type: "epic"},
			{Key: "child", Title: "Child", Type: "task", ParentKey: "root"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "root", ToKey: "child", Type: string(types.DepParentChild)},
		},
	}

	_, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected reverse parent-child edge to be rejected")
	}
	if got, want := err.Error(), "planned blocking dependencies create a path from parent"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}
	if _, ok := fakeStore.issues["ga-1"]; ok {
		t.Fatal("transaction committed graph issues after validation failure")
	}
}

func TestExecuteGraphApplyUnitAllowsExplicitParentChildDuplicate(t *testing.T) {
	ctx, fakeStore := withGraphApplyFakeStore(t)

	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root", Type: "epic"},
			{Key: "child", Title: "Child", Type: "task", ParentKey: "root"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "child", ToKey: "root", Type: string(types.DepParentChild)},
		},
	}

	result, err := executeGraphApply(ctx, plan, GraphApplyOptions{})
	if err != nil {
		t.Fatalf("executeGraphApply: %v", err)
	}
	deps, err := fakeStore.GetDependenciesWithMetadata(ctx, result.IDs["child"])
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadata(child): %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("dependency count = %d, want 1", len(deps))
	}
	if deps[0].ID != result.IDs["root"] {
		t.Fatalf("dependency target = %s, want %s", deps[0].ID, result.IDs["root"])
	}
	if deps[0].DependencyType != types.DepParentChild {
		t.Fatalf("dependency type = %s, want %s", deps[0].DependencyType, types.DepParentChild)
	}
}
