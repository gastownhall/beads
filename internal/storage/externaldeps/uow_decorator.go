package externaldeps

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// WrapUOWProvider installs the external capability policy on the server UOW
// path. Proxied commands otherwise bypass storage decorators entirely.
func WrapUOWProvider(inner uow.UnitOfWorkProvider, locate ProjectLocator, open StoreOpener) uow.UnitOfWorkProvider {
	if inner == nil {
		return nil
	}
	return &uowProvider{UnitOfWorkProvider: inner, policy: New(nil, locate, open)}
}

type uowProvider struct {
	uow.UnitOfWorkProvider
	policy *Store
}

var _ uow.UnitOfWorkProvider = (*uowProvider)(nil)
var _ uow.MaintenanceProvider = (*uowProvider)(nil)

// RunNonTx preserves the optional maintenance capability exposed by the
// proxied provider. Wrapping the provider must not make unrelated commands
// such as compact lose access to their pinned connection.
func (p *uowProvider) RunNonTx(ctx context.Context, fn func(context.Context, *sql.Conn) error) error {
	provider, ok := p.UnitOfWorkProvider.(uow.MaintenanceProvider)
	if !ok {
		return fmt.Errorf("external dependency UOW wrapper: maintenance operations unsupported")
	}
	return provider.RunNonTx(ctx, fn)
}

func (p *uowProvider) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	inner, err := p.UnitOfWorkProvider.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	return &unitOfWork{UnitOfWork: inner, policy: p.policy}, nil
}

type unitOfWork struct {
	uow.UnitOfWork
	policy *Store
	issue  domain.IssueUseCase
	deps   domain.DependencyUseCase
}

var _ uow.UnitOfWork = (*unitOfWork)(nil)

func (u *unitOfWork) IssueUseCase() domain.IssueUseCase {
	if u.issue == nil {
		u.issue = &issueUseCase{
			IssueUseCase: u.UnitOfWork.IssueUseCase(),
			deps:         u.DependencyUseCase(),
			policy:       u.policy,
		}
	}
	return u.issue
}

func (u *unitOfWork) DependencyUseCase() domain.DependencyUseCase {
	if u.deps == nil {
		u.deps = &dependencyUseCase{
			DependencyUseCase: u.UnitOfWork.DependencyUseCase(),
			policy:            u.policy,
		}
	}
	return u.deps
}

type issueUseCase struct {
	domain.IssueUseCase
	deps   domain.DependencyUseCase
	policy *Store
}

func (u *issueUseCase) blockingState(ctx context.Context) (blockingState, error) {
	deps, err := u.deps.GetExternalBlockingDependencyRecords(ctx)
	if err != nil {
		return blockingState{}, err
	}
	return u.policy.blockingStateFromRecords(ctx, deps)
}

func (u *issueUseCase) GetReadyWork(ctx context.Context, filter types.WorkFilter) (domain.SearchPage, error) {
	state, err := u.blockingState(ctx)
	if err != nil {
		return domain.SearchPage{}, fmt.Errorf("external dependencies: %w", err)
	}
	return u.IssueUseCase.GetReadyWork(ctx, withExternalExclusions(filter, state.refsByIssue))
}

func (u *issueUseCase) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) (domain.SearchCountsPage, error) {
	state, err := u.blockingState(ctx)
	if err != nil {
		return domain.SearchCountsPage{}, fmt.Errorf("external dependencies: %w", err)
	}
	return u.IssueUseCase.GetReadyWorkWithCounts(ctx, withExternalExclusions(filter, state.refsByIssue))
}

func (u *issueUseCase) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (domain.ClaimReadyResult, error) {
	state, err := u.blockingState(ctx)
	if err != nil {
		return domain.ClaimReadyResult{}, fmt.Errorf("external dependencies: %w", err)
	}
	return u.IssueUseCase.ClaimReadyIssue(ctx, withExternalExclusions(filter, state.refsByIssue), actor)
}

func (u *issueUseCase) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	base, err := u.IssueUseCase.GetBlockedIssues(ctx, filter)
	if err != nil {
		return nil, err
	}
	state, err := u.blockingState(ctx)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: %w", err)
	}

	result := make([]*types.BlockedIssue, 0, len(base)+len(state.refsByIssue))
	byID := make(map[string]bool, len(base))
	for _, item := range base {
		if item == nil {
			continue
		}
		clone := *item
		clone.BlockedBy = slices.Clone(item.BlockedBy)
		for _, ref := range state.refsByIssue[item.ID] {
			clone.BlockedBy = appendUnique(clone.BlockedBy, ref)
		}
		clone.BlockedByCount = len(clone.BlockedBy)
		result = append(result, &clone)
		byID[item.ID] = true
	}

	missing := make([]string, 0, len(state.refsByIssue))
	for id := range state.refsByIssue {
		if !byID[id] {
			missing = append(missing, id)
		}
	}
	parentDeps := make(map[string][]*types.Dependency)
	if filter.ParentID != nil && len(missing) > 0 {
		parentDeps, err = u.deps.GetIssueDependencyRecords(ctx, missing)
		if err != nil {
			return nil, fmt.Errorf("external dependencies: load blocked parent edges: %w", err)
		}
	}
	issues, err := u.IssueUseCase.GetIssuesByIDs(ctx, missing)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: load blocked sources: %w", err)
	}
	for _, issue := range issues {
		if issue == nil || issue.Status == types.StatusClosed || issue.Status == types.StatusPinned {
			continue
		}
		if !matchesParentFilter(issue.ID, filter.ParentID, parentDeps) {
			continue
		}
		refs := slices.Clone(state.refsByIssue[issue.ID])
		result = append(result, &types.BlockedIssue{Issue: *issue, BlockedByCount: len(refs), BlockedBy: refs})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (u *issueUseCase) CloseIssueChecked(ctx context.Context, id string, params domain.CloseIssueParams, actor string, force bool) (domain.CloseIssueResult, error) {
	if !force {
		issue, err := u.IssueUseCase.GetIssue(ctx, id)
		if err != nil {
			return domain.CloseIssueResult{}, err
		}
		if issue != nil && issue.Status != types.StatusClosed {
			state, err := u.blockingState(ctx)
			if err != nil {
				return domain.CloseIssueResult{}, fmt.Errorf("external dependencies: %w", err)
			}
			if blockers := state.refsByIssue[id]; len(blockers) > 0 {
				return domain.CloseIssueResult{}, fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, id, blockers)
			}
		}
	}
	return u.IssueUseCase.CloseIssueChecked(ctx, id, params, actor, force)
}

type dependencyUseCase struct {
	domain.DependencyUseCase
	policy *Store
}

func (u *dependencyUseCase) GetDependencyTree(ctx context.Context, rootID string, opts domain.DepTreeOpts) ([]*types.TreeNode, error) {
	tree, err := u.DependencyUseCase.GetDependencyTree(ctx, rootID, opts)
	if err != nil || opts.Direction == domain.DepDirectionIn || len(tree) == 0 {
		return tree, err
	}
	ids := make([]string, 0, len(tree))
	for _, node := range tree {
		if node != nil && !isExternalReference(node.ID) {
			ids = append(ids, node.ID)
		}
	}
	deps, err := u.GetIssueDependencyRecords(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: load tree edges: %w", err)
	}
	return u.policy.appendTreeExternalReferences(ctx, tree, deps, opts.MaxDepth, opts.ShowAllPaths)
}
