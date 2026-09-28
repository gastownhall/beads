package main

import (
	"context"
	"errors"
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

	// getByIDsErr makes the batch lookup fail, which is the only contract this
	// conversion changed: the base loop skipped the one parent whose GetIssue
	// failed, the batch drops every suffix on the page.
	getByIDsErr error
	// omitParents drops the named parents from the batch result, standing in
	// for IDs the IN clause matched no row for. Base reached the same state via
	// GetIssue returning a nil parent.
	omitParents map[string]bool
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
	if f.getByIDsErr != nil {
		return nil, f.getByIDsErr
	}
	parents := []*types.Issue{
		{ID: "epic-1", Title: "Auth Overhaul", IssueType: types.TypeEpic},
		{ID: "task-1", Title: "Not an epic", IssueType: types.TypeTask},
	}
	if len(f.omitParents) == 0 {
		return parents, nil
	}
	kept := make([]*types.Issue, 0, len(parents))
	for _, parent := range parents {
		if !f.omitParents[parent.ID] {
			kept = append(kept, parent)
		}
	}
	return kept, nil
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

// checkBatchError asserts the degrade this conversion introduced: a failed
// batch costs every epic suffix on the page, but bd ready still renders the
// list, because displayReadyList treats a nil map as "no annotations".
func (f *parentEpicFixture) checkBatchError(t *testing.T, result map[string]string) {
	t.Helper()
	if result != nil {
		t.Errorf("a failed parent batch should degrade to a nil map, got %v", result)
	}
	if f.getByIDsCalls != 1 {
		t.Errorf("the batch should still be attempted exactly once, got %d calls", f.getByIDsCalls)
	}
}

func TestBuildParentEpicMap_BatchErrorDegradesToNilMap(t *testing.T) {
	f := &parentEpicFixture{getByIDsErr: errors.New("batch lookup failed")}
	f.checkBatchError(t, buildParentEpicMap(context.Background(), parentEpicStore{f: f}, f.issues()))
}

func TestBuildParentEpicMapProxied_BatchErrorDegradesToNilMap(t *testing.T) {
	f := &parentEpicFixture{getByIDsErr: errors.New("batch lookup failed")}
	f.checkBatchError(t, buildParentEpicMapProxied(context.Background(), parentEpicUOW{f: f}, f.issues()))
}

// checkMissingParent pins the other contract the conversion rests on: a parent
// the batch does not return is silently unannotated, matching the base loop's
// `parent == nil -> continue`. It is also the cheap guard on the keying change,
// since the lookup now has to match a returned ID against a requested one.
func (f *parentEpicFixture) checkMissingParent(t *testing.T, result map[string]string) {
	t.Helper()
	if title, ok := result["child-of-epic"]; ok {
		t.Errorf("a parent missing from the batch should leave its child unannotated, got %q", title)
	}
	if f.getByIDsCalls != 1 {
		t.Errorf("the batch should be attempted exactly once, got %d calls", f.getByIDsCalls)
	}
}

func TestBuildParentEpicMap_MissingParentIsOmitted(t *testing.T) {
	f := &parentEpicFixture{omitParents: map[string]bool{"epic-1": true}}
	f.checkMissingParent(t, buildParentEpicMap(context.Background(), parentEpicStore{f: f}, f.issues()))
}

func TestBuildParentEpicMapProxied_MissingParentIsOmitted(t *testing.T) {
	f := &parentEpicFixture{omitParents: map[string]bool{"epic-1": true}}
	f.checkMissingParent(t, buildParentEpicMapProxied(context.Background(), parentEpicUOW{f: f}, f.issues()))
}
