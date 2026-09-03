package externaldeps

import (
	"context"
	"database/sql"
	"fmt"
	"slices"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/memoryops"
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
var _ uow.ProviderUnwrapper = (*uowProvider)(nil)

// Unwrap lets callers deliberately peel policy decorators. In particular,
// bd serve must get beneath the notifying provider before handing a provider
// to HTTP handlers, which must never run workspace hooks.
func (p *uowProvider) Unwrap() uow.UnitOfWorkProvider { return p.UnitOfWorkProvider }

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

// Provider capability accessors build roles on this wrapper. Delegating them
// to the inner provider would silently discard external-dependency policy for
// every command that reaches the proxied seam through an optional source.
func (p *uowProvider) IssueLifecycle() (publicops.Lifecycle, error) { return uow.NewIssueOperations(p) }
func (p *uowProvider) IssueReader() (publicops.Reader, error)       { return uow.NewIssueReader(p) }
func (p *uowProvider) IssueClaimer() (publicops.Claimer, error)     { return uow.NewIssueClaimer(p) }
func (p *uowProvider) IssueRelations() (publicops.Relations, error) { return uow.NewIssueRelations(p) }
func (p *uowProvider) EdgeReader() (publicops.EdgeReader, error)    { return uow.NewEdgeReader(p) }
func (p *uowProvider) BlockingAnnotator() (publicops.BlockingAnnotator, error) {
	return uow.NewBlockingAnnotator(p)
}
func (p *uowProvider) TreeWalker() (publicops.TreeWalker, error) { return uow.NewTreeWalker(p) }
func (p *uowProvider) GraphCounter() (publicops.GraphCounter, error) {
	return uow.NewGraphCounter(p)
}
func (p *uowProvider) Counter() (publicops.Counter, error) { return uow.NewCounter(p) }
func (p *uowProvider) ReadyCounter() (publicops.ReadyCounter, error) {
	return uow.NewReadyCounter(p)
}
func (p *uowProvider) ReadyClaimer() (publicops.ReadyClaimer, error) {
	return uow.NewReadyClaimer(p)
}
func (p *uowProvider) Querier() (publicops.Querier, error) { return uow.NewQuerier(p) }
func (p *uowProvider) StatsReporter() (publicops.StatsReporter, error) {
	return uow.NewStatsReporter(p)
}
func (p *uowProvider) CycleDetector() (publicops.CycleDetector, error) {
	return uow.NewCycleDetector(p)
}
func (p *uowProvider) Commenter() (publicops.Commenter, error) { return uow.NewCommenter(p) }
func (p *uowProvider) BatchCloser() (publicops.BatchCloser, error) {
	return uow.NewBatchCloser(p)
}
func (p *uowProvider) BatchCreator() (publicops.BatchCreator, error) {
	return uow.NewBatchCreator(p)
}
func (p *uowProvider) DependencyEditor() (publicops.DependencyEditor, error) {
	return uow.NewDependencyEditor(p)
}
func (p *uowProvider) BatchApplier() (publicops.BatchApplier, error) {
	return uow.NewBatchApplier(p)
}
func (p *uowProvider) Deleter() (publicops.Deleter, error)   { return uow.NewDeleter(p) }
func (p *uowProvider) Sweeper() (publicops.Sweeper, error)   { return uow.NewSweeper(p) }
func (p *uowProvider) Importer() (publicops.Importer, error) { return uow.NewImporter(p) }
func (p *uowProvider) Bootstrapper() (publicops.Bootstrapper, error) {
	return uow.NewBootstrapper(p)
}
func (p *uowProvider) InitVerifier() (publicops.InitVerifier, error) {
	return uow.NewInitVerifier(p)
}
func (p *uowProvider) WorkspaceConfig() (publicops.WorkspaceConfig, error) {
	return uow.NewWorkspaceConfig(p)
}
func (p *uowProvider) VersionReconciler() (publicops.VersionReconciler, error) {
	return uow.NewVersionReconciler(p)
}
func (p *uowProvider) MetadataCAS() (publicops.MetadataCAS, error) { return uow.NewMetadataCAS(p) }
func (p *uowProvider) Releaser() (publicops.Releaser, error)       { return uow.NewReleaser(p) }
func (p *uowProvider) Memories() (memoryops.Memories, error)       { return uow.NewMemories(p) }
func (p *uowProvider) EventsJournalCursor() (storage.EventsJournalCursor, error) {
	return uow.NewEventsJournalCursor(p)
}

func (p *uowProvider) SetPoolLimits(limits uow.PoolLimits) {
	if tuner, ok := p.UnitOfWorkProvider.(uow.PoolTuner); ok {
		tuner.SetPoolLimits(limits)
	}
}

func (p *uowProvider) SetEventsJournalEnabled(enabled bool) {
	if configurer, ok := p.UnitOfWorkProvider.(storage.EventsJournalConfigurer); ok {
		configurer.SetEventsJournalEnabled(enabled)
	}
}

func (p *uowProvider) RunEventsMaintenanceTx(ctx context.Context, fn func(context.Context, issueops.DBTX) error) error {
	runner, ok := p.UnitOfWorkProvider.(issueops.EventsMaintenanceRunner)
	if !ok {
		return fmt.Errorf("external dependency UOW wrapper: events-journal maintenance unsupported")
	}
	return runner.RunEventsMaintenanceTx(ctx, fn)
}

var (
	_ uow.PoolTuner                    = (*uowProvider)(nil)
	_ storage.EventsJournalConfigurer  = (*uowProvider)(nil)
	_ issueops.EventsMaintenanceRunner = (*uowProvider)(nil)
	_ uow.IssueLifecycleSource         = (*uowProvider)(nil)
	_ uow.IssueReaderSource            = (*uowProvider)(nil)
	_ uow.IssueClaimerSource           = (*uowProvider)(nil)
	_ uow.RelationsSource              = (*uowProvider)(nil)
	_ uow.EdgeReaderSource             = (*uowProvider)(nil)
	_ uow.BlockingAnnotatorSource      = (*uowProvider)(nil)
	_ uow.TreeWalkerSource             = (*uowProvider)(nil)
	_ uow.GraphCounterSource           = (*uowProvider)(nil)
	_ uow.CounterSource                = (*uowProvider)(nil)
	_ uow.ReadyCounterSource           = (*uowProvider)(nil)
	_ uow.ReadyClaimerSource           = (*uowProvider)(nil)
	_ uow.QuerierSource                = (*uowProvider)(nil)
	_ uow.StatsReporterSource          = (*uowProvider)(nil)
	_ uow.CycleDetectorSource          = (*uowProvider)(nil)
	_ uow.CommenterSource              = (*uowProvider)(nil)
	_ uow.BatchCloserSource            = (*uowProvider)(nil)
	_ uow.BatchCreatorSource           = (*uowProvider)(nil)
	_ uow.DependencyEditorSource       = (*uowProvider)(nil)
	_ uow.BatchApplierSource           = (*uowProvider)(nil)
	_ uow.DeleterSource                = (*uowProvider)(nil)
	_ uow.SweeperSource                = (*uowProvider)(nil)
	_ uow.ImporterSource               = (*uowProvider)(nil)
	_ uow.BootstrapperSource           = (*uowProvider)(nil)
	_ uow.InitVerifierSource           = (*uowProvider)(nil)
	_ uow.WorkspaceConfigSource        = (*uowProvider)(nil)
	_ uow.VersionReconcilerSource      = (*uowProvider)(nil)
	_ uow.MetadataCASSource            = (*uowProvider)(nil)
	_ uow.ReleaserSource               = (*uowProvider)(nil)
	_ uow.MemoriesSource               = (*uowProvider)(nil)
	_ uow.EventsJournalCursorSource    = (*uowProvider)(nil)
)

type unitOfWork struct {
	uow.UnitOfWork
	policy *Store
	issue  domain.IssueUseCase
	deps   domain.DependencyUseCase
}

var _ uow.UnitOfWork = (*unitOfWork)(nil)

// Unwrap keeps the transaction runner reachable to infrastructure roles such
// as import. The policy only decorates use-case methods, so peeling it does
// not bypass a mutation guard for callers that use the domain surface.
func (u *unitOfWork) Unwrap() uow.UnitOfWork { return u.UnitOfWork }

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
	base, err := u.IssueUseCase.GetBlockedIssues(ctx, unpagedBlockedFilter(filter))
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
		wispParentDeps, err := u.deps.GetWispDependencyRecords(ctx, missing)
		if err != nil {
			return nil, fmt.Errorf("external dependencies: load blocked wisp parent edges: %w", err)
		}
		for id, deps := range wispParentDeps {
			parentDeps[id] = append(parentDeps[id], deps...)
		}
	}
	issues, err := u.IssueUseCase.GetIssuesByIDs(ctx, missing)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: load blocked sources: %w", err)
	}
	wisps, err := u.IssueUseCase.GetWispsByIDs(ctx, missing)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: load blocked wisp sources: %w", err)
	}
	issues = append(issues, wisps...)
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
	return finishBlockedIssues(result, filter)
}

func (u *issueUseCase) CloseIssueChecked(ctx context.Context, id string, params domain.CloseIssueParams, actor string, force bool) (domain.CloseIssueResult, error) {
	if err := u.guardExternalClose(ctx, id, force); err != nil {
		return domain.CloseIssueResult{}, err
	}
	return u.IssueUseCase.CloseIssueChecked(ctx, id, params, actor, force)
}

func (u *issueUseCase) CloseWispChecked(ctx context.Context, id string, params domain.CloseIssueParams, actor string, force bool) (domain.CloseIssueResult, error) {
	if err := u.guardExternalClose(ctx, id, force); err != nil {
		return domain.CloseIssueResult{}, err
	}
	return u.IssueUseCase.CloseWispChecked(ctx, id, params, actor, force)
}

func (u *issueUseCase) ApplyUpdate(ctx context.Context, id string, spec domain.UpdateSpec, actor string) (*types.Issue, error) {
	if isClosedUpdate(spec.Fields) {
		if err := u.guardExternalClose(ctx, id, false); err != nil {
			return nil, err
		}
	}
	return u.IssueUseCase.ApplyUpdate(ctx, id, spec, actor)
}

func isClosedUpdate(fields map[string]any) bool {
	switch status := fields["status"].(type) {
	case string:
		return status == string(types.StatusClosed)
	case types.Status:
		return status == types.StatusClosed
	default:
		return false
	}
}

func (u *issueUseCase) guardExternalClose(ctx context.Context, id string, force bool) error {
	if force {
		return nil
	}
	state, err := u.blockingState(ctx)
	if err != nil {
		return fmt.Errorf("external dependencies: %w", err)
	}
	if blockers := state.refsByIssue[id]; len(blockers) > 0 {
		return fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, id, blockers)
	}
	return nil
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
