package main

import (
	"context"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

type parentEpicFixture struct {
	getIssueCalls   int
	getByIDsCalls   int
	getByIDsRequest []string
}

func (f *parentEpicFixture) issues() []*types.Issue {
	return []*types.Issue{
		{ID: "child-of-epic", Title: "Implement login", IssueType: types.TypeTask},
		{ID: "child-of-task", Title: "Subtask of non-epic", IssueType: types.TypeTask},
	}
}

func (f *parentEpicFixture) deps(ids []string) map[string][]*types.Dependency {
	return map[string][]*types.Dependency{
		"child-of-epic": {{IssueID: "child-of-epic", DependsOnID: "epic-1", Type: types.DepParentChild}},
		"child-of-task": {{IssueID: "child-of-task", DependsOnID: "task-1", Type: types.DepParentChild}},
	}
}

func (f *parentEpicFixture) getIssue(string) (*types.Issue, error) {
	f.getIssueCalls++
	return nil, nil
}

func (f *parentEpicFixture) getIssuesByIDs(ids []string) ([]*types.Issue, error) {
	f.getByIDsCalls++
	f.getByIDsRequest = append([]string(nil), ids...)
	sort.Strings(f.getByIDsRequest)
	return []*types.Issue{
		{ID: "epic-1", Title: "Auth Overhaul", IssueType: types.TypeEpic},
		{ID: "task-1", Title: "Not an epic", IssueType: types.TypeTask},
	}, nil
}

func (f *parentEpicFixture) check(t *testing.T, result map[string]string) {
	t.Helper()
	if result["child-of-epic"] != "Auth Overhaul" {
		t.Errorf("child-of-epic should map to the epic title, got %q", result["child-of-epic"])
	}
	if _, ok := result["child-of-task"]; ok {
		t.Errorf("child-of-task should not be in the map, its parent is not an epic")
	}
	if f.getIssueCalls != 0 {
		t.Errorf("parents were fetched one by one: %d GetIssue calls", f.getIssueCalls)
	}
	if f.getByIDsCalls != 1 {
		t.Errorf("parents should be fetched in one batch, got %d GetIssuesByIDs calls", f.getByIDsCalls)
	}
	if want := []string{"epic-1", "task-1"}; len(f.getByIDsRequest) != 2 || f.getByIDsRequest[0] != want[0] || f.getByIDsRequest[1] != want[1] {
		t.Errorf("batch should name every parent once, got %v", f.getByIDsRequest)
	}
}

type parentEpicStore struct {
	storage.DoltStorage
	f *parentEpicFixture
}

func (s parentEpicStore) GetDependencyRecordsForIssues(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	return s.f.deps(ids), nil
}

func (s parentEpicStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return s.f.getIssue(id)
}

func (s parentEpicStore) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	return s.f.getIssuesByIDs(ids)
}

type parentEpicIssues struct {
	domain.IssueUseCase
	f *parentEpicFixture
}

func (u parentEpicIssues) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return u.f.getIssue(id)
}

func (u parentEpicIssues) GetIssuesByIDs(_ context.Context, ids []string) ([]*types.Issue, error) {
	return u.f.getIssuesByIDs(ids)
}

type parentEpicDeps struct {
	domain.DependencyUseCase
	f *parentEpicFixture
}

func (d parentEpicDeps) GetForIssueIDs(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	return d.f.deps(ids), nil
}

type parentEpicUOW struct {
	uow.UnitOfWork
	f *parentEpicFixture
}

func (u parentEpicUOW) IssueUseCase() domain.IssueUseCase           { return parentEpicIssues{f: u.f} }
func (u parentEpicUOW) DependencyUseCase() domain.DependencyUseCase { return parentEpicDeps{f: u.f} }

func TestBuildParentEpicMap_FetchesParentsInOneBatch(t *testing.T) {
	f := &parentEpicFixture{}
	f.check(t, buildParentEpicMap(context.Background(), parentEpicStore{f: f}, f.issues()))
}

func TestBuildParentEpicMapProxied_FetchesParentsInOneBatch(t *testing.T) {
	f := &parentEpicFixture{}
	f.check(t, buildParentEpicMapProxied(context.Background(), parentEpicUOW{f: f}, f.issues()))
}
