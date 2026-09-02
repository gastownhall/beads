package externaldeps

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
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
	ready   []*types.Issue
	wisps   []*types.Issue
	blocked []*types.BlockedIssue
	closed  []string
}

func (u *fakeIssueUseCase) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	for _, issue := range u.ready {
		if issue.ID == id {
			return issue, nil
		}
	}
	return nil, nil
}

func (u *fakeIssueUseCase) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	result := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		if issue, err := u.GetIssue(ctx, id); err != nil {
			return nil, err
		} else if issue != nil {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (u *fakeIssueUseCase) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	for _, issue := range u.wisps {
		if issue.ID == id {
			return issue, nil
		}
	}
	return nil, nil
}

func (u *fakeIssueUseCase) GetWispsByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	result := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		issue, err := u.GetWisp(ctx, id)
		if err != nil {
			return nil, err
		}
		if issue != nil {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (u *fakeIssueUseCase) CloseIssueChecked(_ context.Context, id string, _ domain.CloseIssueParams, _ string, _ bool) (domain.CloseIssueResult, error) {
	u.closed = append(u.closed, id)
	return domain.CloseIssueResult{}, nil
}

func (u *fakeIssueUseCase) CloseWispChecked(_ context.Context, id string, _ domain.CloseIssueParams, _ string, _ bool) (domain.CloseIssueResult, error) {
	u.closed = append(u.closed, id)
	return domain.CloseIssueResult{}, nil
}

func (u *fakeIssueUseCase) ApplyUpdate(_ context.Context, id string, spec domain.UpdateSpec, _ string) (*types.Issue, error) {
	u.closed = append(u.closed, id)
	status, _ := spec.Fields["status"].(string)
	return &types.Issue{ID: id, Status: types.Status(status)}, nil
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

func (u *fakeIssueUseCase) GetBlockedIssues(_ context.Context, _ types.WorkFilter) ([]*types.BlockedIssue, error) {
	return slices.Clone(u.blocked), nil
}

type fakeDependencyUseCase struct {
	domain.DependencyUseCase
	external map[string][]*types.Dependency
	records  map[string][]*types.Dependency
	wispDeps map[string][]*types.Dependency
}

func (u *fakeDependencyUseCase) GetExternalBlockingDependencyRecords(context.Context) (map[string][]*types.Dependency, error) {
	return u.external, nil
}

func (u *fakeDependencyUseCase) GetIssueDependencyRecords(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	result := make(map[string][]*types.Dependency, len(ids))
	for _, id := range ids {
		result[id] = u.records[id]
	}
	return result, nil
}

func (u *fakeDependencyUseCase) GetWispDependencyRecords(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	result := make(map[string][]*types.Dependency, len(ids))
	for _, id := range ids {
		result[id] = u.wispDeps[id]
	}
	return result, nil
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

func TestWrapUOWProviderKeepsUnitOfWorkUnwrappable(t *testing.T) {
	inner := &fakeUOW{}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, nil, nil)
	wrapped, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}

	unwrapper, ok := wrapped.(interface{ Unwrap() uow.UnitOfWork })
	if !ok {
		t.Fatalf("wrapped UOW %T does not expose Unwrap", wrapped)
	}
	if got := unwrapper.Unwrap(); got != inner {
		t.Fatalf("Unwrap() = %T, want the wrapped UOW", got)
	}
}

func TestWrapUOWProviderPreservesIssueReader(t *testing.T) {
	inner := &fakeUOWProvider{uw: &fakeUOW{}}
	provider := WrapUOWProvider(inner, nil, nil)
	source, ok := provider.(uow.IssueReaderSource)
	if !ok {
		t.Fatalf("wrapped provider %T does not preserve IssueReaderSource", provider)
	}
	if reader, err := source.IssueReader(); err != nil || reader == nil {
		t.Fatalf("IssueReader() = %T, %v", reader, err)
	}
	if got := uow.UnwrapProvider(provider); got != inner {
		t.Fatalf("UnwrapProvider() = %T, want the wrapped provider", got)
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

func TestWrapUOWProviderFiltersExternalBlockedWorkByParent(t *testing.T) {
	parent, child := issue("be-parent"), issue("be-child")
	inner := &fakeUOW{
		issues: &fakeIssueUseCase{ready: []*types.Issue{child}},
		deps: &fakeDependencyUseCase{
			external: map[string][]*types.Dependency{
				child.ID: {externalDep(child.ID, "external:remote:payments", types.DepBlocks)},
			},
			records: map[string][]*types.Dependency{
				child.ID: {{IssueID: child.ID, DependsOnID: parent.ID, Type: types.DepParentChild}},
			},
		},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	got, err := uw.IssueUseCase().GetBlockedIssues(t.Context(), types.WorkFilter{ParentID: &parent.ID})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	if len(got) != 1 || got[0].ID != child.ID {
		t.Fatalf("blocked issues = %v, want [%s]", got, child.ID)
	}
}

func TestWrapUOWProviderGuardsDoneUpdatesAndWispCloses(t *testing.T) {
	durable, wisp := issue("be-durable"), issue("be-wisp")
	issues := &fakeIssueUseCase{ready: []*types.Issue{durable}, wisps: []*types.Issue{wisp}}
	inner := &fakeUOW{
		issues: issues,
		deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
			durable.ID: {externalDep(durable.ID, "external:remote:payments", types.DepBlocks)},
			wisp.ID:    {externalDep(wisp.ID, "external:remote:payments", types.DepBlocks)},
		}},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	issueUC := uw.IssueUseCase()
	if _, err := issueUC.ApplyUpdate(t.Context(), durable.ID, domain.UpdateSpec{Fields: map[string]any{"status": string(types.StatusClosed)}}, "tester"); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("ApplyUpdate error = %v, want ErrCloseBlocked", err)
	}
	if _, err := issueUC.CloseWispChecked(t.Context(), wisp.ID, domain.CloseIssueParams{}, "tester", false); !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("CloseWispChecked error = %v, want ErrCloseBlocked", err)
	}
	if len(issues.closed) != 0 {
		t.Fatalf("inner mutation calls = %v, want none", issues.closed)
	}
	blocked, err := issueUC.GetBlockedIssues(t.Context(), types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetBlockedIssues: %v", err)
	}
	if ids := blockedIssueIDs(blocked); !slices.Equal(ids, []string{durable.ID, wisp.ID}) {
		t.Fatalf("blocked IDs = %v, want [%s %s]", ids, durable.ID, wisp.ID)
	}
}

func TestWrapUOWProviderPaginatesCombinedExternalBlockedWork(t *testing.T) {
	local, external := issue("be-local"), issue("be-external")
	createdAt := time.Date(2026, time.August, 2, 0, 0, 0, 0, time.UTC)
	local.Priority, external.Priority = 0, 0
	local.CreatedAt, external.CreatedAt = createdAt, createdAt
	inner := &fakeUOW{
		issues: &fakeIssueUseCase{
			ready:   []*types.Issue{external},
			blocked: []*types.BlockedIssue{{Issue: *local, BlockedBy: []string{"be-local-blocker"}, BlockedByCount: 1}},
		},
		deps: &fakeDependencyUseCase{external: map[string][]*types.Dependency{
			external.ID: {externalDep(external.ID, "external:remote:payments", types.DepBlocks)},
		}},
	}
	provider := WrapUOWProvider(&fakeUOWProvider{uw: inner}, func(ProjectName) (string, bool) {
		return "", false
	}, nil)
	uw, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	first, err := uw.IssueUseCase().GetBlockedIssues(t.Context(), types.WorkFilter{Limit: 1})
	if err != nil {
		t.Fatalf("GetBlockedIssues first page: %v", err)
	}
	if ids := blockedIssueIDs(first); !slices.Equal(ids, []string{external.ID}) {
		t.Fatalf("first page IDs = %v, want [%s]", ids, external.ID)
	}
	second, err := uw.IssueUseCase().GetBlockedIssues(t.Context(), types.WorkFilter{Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("GetBlockedIssues second page: %v", err)
	}
	if ids := blockedIssueIDs(second); !slices.Equal(ids, []string{local.ID}) {
		t.Fatalf("second page IDs = %v, want [%s]", ids, local.ID)
	}
	_, err = uw.IssueUseCase().GetBlockedIssues(t.Context(), types.WorkFilter{MaxRows: 1, MaxRowsSource: "--max-rows"})
	var capErr *issueops.ErrTooManyRows
	if !errors.As(err, &capErr) {
		t.Fatalf("GetBlockedIssues MaxRows error = %v, want ErrTooManyRows", err)
	}
}
