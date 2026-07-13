package externaldeps

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

type fakeStore struct {
	storage.DoltStorage
	ready      []*types.Issue
	blocked    []*types.BlockedIssue
	tree       []*types.TreeNode
	deps       map[string][]*types.Dependency
	labels     map[string][]*types.Issue
	claimed    []string
	isBlocked  bool
	blockerIDs []string
}

func (f *fakeStore) GetReadyWork(_ context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	candidates := make([]*types.Issue, 0, len(f.ready))
	for _, issue := range f.ready {
		if !slices.Contains(filter.ExcludeIDs, issue.ID) {
			candidates = append(candidates, issue)
		}
	}
	return page(candidates, filter.Offset, filter.Limit), nil
}

func (f *fakeStore) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	issues, err := f.GetReadyWork(ctx, filter)
	if err != nil {
		return nil, err
	}
	result := make([]*types.IssueWithCounts, 0, len(issues))
	for _, issue := range issues {
		result = append(result, &types.IssueWithCounts{Issue: issue})
	}
	return result, nil
}

func (f *fakeStore) GetBlockedIssues(_ context.Context, _ types.WorkFilter) ([]*types.BlockedIssue, error) {
	return slices.Clone(f.blocked), nil
}

func (f *fakeStore) IsBlocked(_ context.Context, _ string) (bool, []string, error) {
	return f.isBlocked, slices.Clone(f.blockerIDs), nil
}

func (f *fakeStore) GetAllDependencyRecords(_ context.Context) (map[string][]*types.Dependency, error) {
	return f.deps, nil
}

func (f *fakeStore) GetExternalBlockingDependencyRecords(_ context.Context) (map[string][]*types.Dependency, error) {
	return f.deps, nil
}

func (f *fakeStore) GetDependencyRecordsForIssues(_ context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	result := make(map[string][]*types.Dependency)
	for _, id := range issueIDs {
		result[id] = f.deps[id]
	}
	return result, nil
}

func (f *fakeStore) GetIssuesByLabel(_ context.Context, label string) ([]*types.Issue, error) {
	return f.labels[label], nil
}

func (f *fakeStore) GetDependencyTree(_ context.Context, _ string, _ int, _ bool, _ bool) ([]*types.TreeNode, error) {
	return slices.Clone(f.tree), nil
}

func (f *fakeStore) ClaimIssue(_ context.Context, id, actor string) error {
	for _, issue := range f.ready {
		if issue.ID == id {
			if issue.Assignee != "" {
				return storage.ErrAlreadyClaimed
			}
			issue.Assignee = actor
			issue.Status = types.StatusInProgress
			f.claimed = append(f.claimed, id)
			return nil
		}
	}
	return storage.ErrNotFound
}

func (f *fakeStore) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error) {
	filter.Limit = 1
	issues, err := f.GetReadyWork(ctx, filter)
	if err != nil || len(issues) == 0 {
		return nil, err
	}
	if err := f.ClaimIssue(ctx, issues[0].ID, actor); err != nil {
		return nil, err
	}
	return f.GetIssue(ctx, issues[0].ID)
}

func (f *fakeStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	for _, issue := range f.ready {
		if issue.ID == id {
			return issue, nil
		}
	}
	for _, blocked := range f.blocked {
		if blocked.ID == id {
			return &blocked.Issue, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	result := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		issue, err := f.GetIssue(ctx, id)
		if err != nil {
			return nil, err
		}
		if issue != nil {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (f *fakeStore) Close() error { return nil }

func page[T any](items []*T, offset, limit int) []*T {
	if offset >= len(items) {
		return nil
	}
	if offset < 0 {
		offset = 0
	}
	end := len(items)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return slices.Clone(items[offset:end])
}

func issue(id string) *types.Issue {
	return &types.Issue{ID: id, Title: id, Status: types.StatusOpen, IssueType: types.TypeTask}
}

func externalDep(source, ref string, depType types.DependencyType) *types.Dependency {
	return &types.Dependency{IssueID: source, DependsOnID: ref, Type: depType}
}

func testStore(raw, foreign *fakeStore, configured bool) *Store {
	return New(
		raw,
		func(project ProjectName) (string, bool) {
			if !configured || project != "remote" {
				return "", false
			}
			return "/projects/remote", true
		},
		func(_ context.Context, path string) (storage.DoltStorage, error) {
			if path != "/projects/remote" {
				return nil, errors.New("unexpected path")
			}
			return foreign, nil
		},
	)
}

func TestGetReadyWorkFiltersExternalBlockerAndFillsPage(t *testing.T) {
	a, b, c := issue("be-a"), issue("be-b"), issue("be-c")
	raw := &fakeStore{
		ready: []*types.Issue{a, b, c},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, "external:remote:payments", types.DepBlocks)},
		},
	}
	store := testStore(raw, &fakeStore{}, true)

	got, err := store.GetReadyWork(t.Context(), types.WorkFilter{Limit: 2})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if ids := issueIDs(got); !slices.Equal(ids, []string{"be-b", "be-c"}) {
		t.Fatalf("ready IDs = %v, want [be-b be-c]", ids)
	}
}

func TestGetReadyWorkHonorsShippedAndNonBlockingExternalRefs(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := &fakeStore{
		ready: []*types.Issue{a, b},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, "external:remote:payments", types.DepBlocks)},
			b.ID: {externalDep(b.ID, "external:remote:future", types.DepTracks)},
		},
	}
	foreign := &fakeStore{labels: map[string][]*types.Issue{
		"provides:payments": {{ID: "remote-done", Status: types.StatusClosed}},
	}}
	store := testStore(raw, foreign, true)

	got, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if ids := issueIDs(got); !slices.Equal(ids, []string{"be-a", "be-b"}) {
		t.Fatalf("ready IDs = %v, want [be-a be-b]", ids)
	}
}

func TestGetReadyWorkFailsClosedForUnconfiguredProject(t *testing.T) {
	a := issue("be-a")
	raw := &fakeStore{
		ready: []*types.Issue{a},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, "external:remote:payments", types.DepBlocks)},
		},
	}
	store := testStore(raw, &fakeStore{}, false)

	got, err := store.GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ready = %v, want no issues", issueIDs(got))
	}
}

func TestGetReadyWorkOpensEachForeignProjectOnce(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := &fakeStore{
		ready: []*types.Issue{a, b},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, "external:remote:payments", types.DepBlocks)},
			b.ID: {externalDep(b.ID, "external:remote:identity", types.DepBlocks)},
		},
	}
	foreign := &fakeStore{}
	openCount := 0
	store := New(
		raw,
		func(ProjectName) (string, bool) { return "/projects/remote", true },
		func(context.Context, string) (storage.DoltStorage, error) {
			openCount++
			return foreign, nil
		},
	)

	if _, err := store.GetReadyWork(t.Context(), types.WorkFilter{}); err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if openCount != 1 {
		t.Fatalf("foreign project opens = %d, want 1", openCount)
	}
}

func TestClaimReadyIssueSkipsExternalBlocker(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := &fakeStore{
		ready: []*types.Issue{a, b},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, "external:remote:payments", types.DepBlocks)},
		},
	}
	store := testStore(raw, &fakeStore{}, true)

	got, err := store.ClaimReadyIssue(t.Context(), types.WorkFilter{}, "worker")
	if err != nil {
		t.Fatalf("ClaimReadyIssue: %v", err)
	}
	if got == nil || got.ID != b.ID {
		t.Fatalf("claimed = %+v, want %s", got, b.ID)
	}
	if !slices.Equal(raw.claimed, []string{b.ID}) {
		t.Fatalf("claim attempts = %v, want [%s]", raw.claimed, b.ID)
	}
}

func TestCountReadyWorkUsesExternalPolicy(t *testing.T) {
	a, b := issue("be-a"), issue("be-b")
	raw := &fakeStore{
		ready: []*types.Issue{a, b},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, "external:remote:payments", types.DepBlocks)},
		},
	}
	store := testStore(raw, &fakeStore{}, true)

	got, err := store.CountReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("CountReadyWork: %v", err)
	}
	if got != 1 {
		t.Fatalf("ready count = %d, want 1", got)
	}
}

func TestGetBlockedIssuesAddsUnsatisfiedExternalRefs(t *testing.T) {
	a, c := issue("be-a"), issue("be-c")
	raw := &fakeStore{
		ready: []*types.Issue{a},
		blocked: []*types.BlockedIssue{{
			Issue:          *c,
			BlockedByCount: 1,
			BlockedBy:      []string{"be-local"},
		}},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, "external:remote:payments", types.DepBlocks)},
			c.ID: {externalDep(c.ID, "external:remote:identity", types.DepBlocks)},
		},
	}
	store := testStore(raw, &fakeStore{}, true)

	got, err := store.GetBlockedIssues(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	byID := make(map[string]*types.BlockedIssue, len(got))
	for _, blocked := range got {
		byID[blocked.ID] = blocked
	}
	if !slices.Equal(byID[a.ID].BlockedBy, []string{"external:remote:payments"}) {
		t.Fatalf("%s blockers = %v", a.ID, byID[a.ID].BlockedBy)
	}
	if !slices.Equal(byID[c.ID].BlockedBy, []string{"be-local", "external:remote:identity"}) {
		t.Fatalf("%s blockers = %v", c.ID, byID[c.ID].BlockedBy)
	}
}

func TestIsBlockedIncludesUnsatisfiedExternalRef(t *testing.T) {
	a := issue("be-a")
	ref := "external:remote:payments"
	raw := &fakeStore{
		ready: []*types.Issue{a},
		deps: map[string][]*types.Dependency{
			a.ID: {externalDep(a.ID, ref, types.DepBlocks)},
		},
	}
	store := testStore(raw, &fakeStore{}, true)

	blocked, blockers, err := store.IsBlocked(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if !blocked || !slices.Equal(blockers, []string{ref}) {
		t.Fatalf("IsBlocked = %v, %v; want true, [%s]", blocked, blockers, ref)
	}
}

func TestGetDependencyTreeAddsResolvedExternalLeaf(t *testing.T) {
	root := issue("be-root")
	ref := "external:remote:payments"
	raw := &fakeStore{
		tree: []*types.TreeNode{{Issue: *root}},
		deps: map[string][]*types.Dependency{
			root.ID: {externalDep(root.ID, ref, types.DepBlocks)},
		},
	}
	foreign := &fakeStore{labels: make(map[string][]*types.Issue)}
	store := testStore(raw, foreign, true)

	tree, err := store.GetDependencyTree(t.Context(), root.ID, 10, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree: %v", err)
	}
	if len(tree) != 2 {
		t.Fatalf("tree length = %d, want 2", len(tree))
	}
	leaf := tree[1]
	if leaf.ID != ref || leaf.Status != types.StatusBlocked || leaf.ParentID != root.ID || leaf.EdgeFromParent != types.DepBlocks {
		t.Fatalf("external leaf = %+v", leaf)
	}

	foreign.labels["provides:payments"] = []*types.Issue{{ID: "remote-done", Status: types.StatusClosed}}
	tree, err = store.GetDependencyTree(t.Context(), root.ID, 10, false, false)
	if err != nil {
		t.Fatalf("GetDependencyTree after ship: %v", err)
	}
	if tree[1].Status != types.StatusClosed {
		t.Fatalf("shipped external status = %s, want closed", tree[1].Status)
	}
}

func TestUnwrapReturnsInnerStore(t *testing.T) {
	raw := &fakeStore{}
	store := testStore(raw, &fakeStore{}, true)
	if got := store.Unwrap(); got != raw {
		t.Fatalf("Unwrap() = %T %p, want raw %p", got, got, raw)
	}
}

func issueIDs(issues []*types.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}
