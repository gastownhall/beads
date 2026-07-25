package externaldeps

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

type fakeUOWProvider struct {
	uow.UnitOfWorkProvider
	uw uow.UnitOfWork
}

func (p *fakeUOWProvider) NewUOW(context.Context) (uow.UnitOfWork, error) { return p.uw, nil }

type fakeUOW struct {
	uow.UnitOfWork
	issues domain.IssueUseCase
	deps   domain.DependencyUseCase
}

func (u *fakeUOW) IssueUseCase() domain.IssueUseCase           { return u.issues }
func (u *fakeUOW) DependencyUseCase() domain.DependencyUseCase { return u.deps }

type fakeIssueUseCase struct {
	domain.IssueUseCase
	ready  []*types.Issue
	closed []string
}

func (u *fakeIssueUseCase) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	for _, issue := range u.ready {
		if issue.ID == id {
			return issue, nil
		}
	}
	return nil, nil
}

func (u *fakeIssueUseCase) CloseIssueChecked(_ context.Context, id string, _ domain.CloseIssueParams, _ string, _ bool) (domain.CloseIssueResult, error) {
	u.closed = append(u.closed, id)
	return domain.CloseIssueResult{}, nil
}

func (u *fakeIssueUseCase) GetReadyWork(_ context.Context, filter types.WorkFilter) (domain.SearchPage, error) {
	items := make([]*types.Issue, 0, len(u.ready))
	for _, issue := range u.ready {
		if !slices.Contains(filter.ExcludeIDs, issue.ID) {
			items = append(items, issue)
		}
	}
	return domain.SearchPage{Items: items}, nil
}

type fakeDependencyUseCase struct {
	domain.DependencyUseCase
	external map[string][]*types.Dependency
}

func (u *fakeDependencyUseCase) GetExternalBlockingDependencyRecords(context.Context) (map[string][]*types.Dependency, error) {
	return u.external, nil
}

func TestWrapUOWProviderFiltersProxiedReadyWork(t *testing.T) {
	blocked, ready := issue("be-blocked"), issue("be-ready")
	inner := &fakeUOW{
		issues: &fakeIssueUseCase{ready: []*types.Issue{blocked, ready}},
		deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
			blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
		}},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	got, err := uw.IssueUseCase().GetReadyWork(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	if ids := issueIDs(got.Items); !slices.Equal(ids, []string{ready.ID}) {
		t.Fatalf("proxied ready IDs = %v, want [%s]", ids, ready.ID)
	}
}

func TestWrapUOWProviderRefusesProxiedCheckedClose(t *testing.T) {
	blocked := issue("be-blocked")
	issues := &fakeIssueUseCase{ready: []*types.Issue{blocked}}
	inner := &fakeUOW{
		issues: issues,
		deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
			blocked.ID: {externalDep(blocked.ID, "external:remote:payments", types.DepBlocks)},
		}},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	if _, err := uw.IssueUseCase().CloseIssueChecked(t.Context(), blocked.ID, domain.CloseIssueParams{}, "tester", false); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("CloseIssueChecked error = %v, want ErrCloseBlocked", err)
	}
	if len(issues.closed) != 0 {
		t.Fatalf("inner close calls = %v, want none", issues.closed)
	}
}
