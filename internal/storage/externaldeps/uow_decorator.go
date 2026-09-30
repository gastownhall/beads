package externaldeps

import (
	"context"
	"database/sql"
	"errors"
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
	return &uowProvider{UnitOfWorkProvider: inner, policy: NewPolicy(locate, open)}
}

type uowProvider struct {
	uow.UnitOfWorkProvider
	policy *Policy
	// resolved is set only on the per-call copy a guarded mutation role builds
	// (withResolved): the foreign half of that call's verdict, resolved before
	// its write transaction opened. Nil on the provider every caller holds, so a
	// raw unit of work keeps resolving inside its own transaction.
	resolved *preResolved
}

// preResolved is the FOREIGN half of one mutation's external-dependency
// verdict — whether each `external:` ref the target carried is satisfied —
// resolved BEFORE the mutation's write transaction opens.
//
// Resolving a ref opens the foreign project's store and queries it. Doing that
// inside a claim's or close's write transaction held a pooled connection (and
// the transaction) across foreign IO, once more on every RunTxResult retry — on
// bd serve, a handful of concurrent claims against a slow foreign project could
// drain the pool. So the guarded roles resolve first, in no transaction, and
// the check inside the transaction only re-reads the target's own edges (one
// indexed query, no foreign IO) and looks each ref up here.
//
// A ref the transaction sees that is NOT in satisfied — an edge committed
// after the resolution — counts as unsatisfied: the policy fails closed, and
// the caller's retry resolves it.
type preResolved struct {
	satisfied map[string]bool
}

// resolveFor resolves the external refs ids carry now: one read-only unit of
// work of the undecorated provider for ids' OWN edges (never the whole
// workspace's), then the foreign lookups with no transaction open. An id with
// no external edge costs no foreign IO.
func (p *uowProvider) resolveFor(ctx context.Context, ids ...string) (*preResolved, error) {
	edges, err := p.ownEdges(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: list blocking records: %w", err)
	}
	var refs []reference
	for _, id := range ids {
		for _, dep := range edges[id] {
			if dep != nil && dep.Type.IsBlockingEdge() && isExternalReference(dep.DependsOnID) {
				refs = append(refs, parseReference(dep.DependsOnID))
			}
		}
	}
	satisfied, err := p.policy.resolveReferences(ctx, refs)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: resolve blockers: %w", err)
	}
	return &preResolved{satisfied: satisfied}, nil
}

// PreResolve prepares provider for ONE write transaction a caller opens itself
// whose guarded claims or closes target ids: when provider is this package's
// policy provider, it returns a copy carrying the ids' `external:` refs
// resolved NOW, with no unit of work open, so the checks inside the caller's
// transaction open no foreign store (a ref the resolution did not see fails
// closed). Any other provider — the policy is not composed there, or a server
// enforces it — is returned unchanged.
//
// It is for callers that must run a claim or close inside a transaction they
// open (the molecule port's step claim after `bd close --continue`); a caller
// holding a role gets this from the role.
func PreResolve(ctx context.Context, provider uow.UnitOfWorkProvider, ids ...string) (uow.UnitOfWorkProvider, error) {
	p, ok := provider.(*uowProvider)
	if !ok {
		return provider, nil
	}
	resolved, err := p.resolveFor(ctx, ids...)
	if err != nil {
		return nil, err
	}
	return p.withResolved(resolved), nil
}

// withResolved is this provider for ONE guarded call: same inner provider, same
// policy, and the verdict resolveFor produced for it.
func (p *uowProvider) withResolved(r *preResolved) *uowProvider {
	return &uowProvider{UnitOfWorkProvider: p.UnitOfWorkProvider, policy: p.policy, resolved: r}
}

var _ uow.UnitOfWorkProvider = (*uowProvider)(nil)
var _ uow.MaintenanceProvider = (*uowProvider)(nil)
var _ uow.ProviderUnwrapper = (*uowProvider)(nil)
var _ uow.ProviderRewrapper = (*uowProvider)(nil)

// Unwrap lets callers deliberately peel policy decorators. In particular,
// bd serve must get beneath the notifying provider before handing a provider
// to HTTP handlers, which must never run workspace hooks.
func (p *uowProvider) Unwrap() uow.UnitOfWorkProvider { return p.UnitOfWorkProvider }

// Rewrap puts this policy, unchanged, over a different inner provider. bd
// serve's HTTP layer uses it to slide its per-request timing provider BENEATH
// the policy, so served requests reach their roles through the accessors below
// — the same role-level policy the CLI uses — instead of around them.
func (p *uowProvider) Rewrap(inner uow.UnitOfWorkProvider) uow.UnitOfWorkProvider {
	if inner == nil {
		return nil
	}
	return &uowProvider{UnitOfWorkProvider: inner, policy: p.policy, resolved: p.resolved}
}

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
	return &unitOfWork{UnitOfWork: inner, policy: p.policy, resolved: p.resolved}, nil
}

// Provider capability accessors build roles on this wrapper. Delegating them
// to the inner provider would silently discard external-dependency policy for
// every command that reaches the proxied seam through an optional source.
func (p *uowProvider) IssueLifecycle() (publicops.Lifecycle, error) {
	if _, err := uow.NewIssueOperations(p); err != nil {
		return nil, err
	}
	return &resolvingLifecycle{provider: p}, nil
}

// resolvingLifecycle is the lifecycle role over this provider, with the
// external refs of a guarded mutation — a claim, a close, or an update that
// does either — resolved before its write transaction (preResolved). Create
// and Reopen carry no external-dependency guard and go straight through.
type resolvingLifecycle struct {
	provider *uowProvider
}

var _ publicops.Lifecycle = (*resolvingLifecycle)(nil)

func (l *resolvingLifecycle) ops(ctx context.Context, guardedID string) (publicops.Lifecycle, error) {
	if guardedID == "" {
		return uow.NewIssueOperations(l.provider)
	}
	resolved, err := l.provider.resolveFor(ctx, guardedID)
	if err != nil {
		return nil, err
	}
	return uow.NewIssueOperations(l.provider.withResolved(resolved))
}

func (l *resolvingLifecycle) Create(ctx context.Context, req publicops.CreateRequest) (publicops.CreateResult, error) {
	ops, err := l.ops(ctx, "")
	if err != nil {
		return publicops.CreateResult{}, err
	}
	return ops.Create(ctx, req)
}

func (l *resolvingLifecycle) Update(ctx context.Context, req publicops.UpdateRequest) (publicops.UpdateResult, error) {
	guarded := ""
	closing := req.Patch.Status.Set && req.Patch.Status.Value == types.StatusClosed && !req.ForceClosePolicy
	if req.Claim || closing {
		guarded = req.IssueID // a forced close is not guarded, so alone it resolves nothing
	}
	ops, err := l.ops(ctx, guarded)
	if err != nil {
		return publicops.UpdateResult{}, err
	}
	return ops.Update(ctx, req)
}

func (l *resolvingLifecycle) Close(ctx context.Context, req publicops.CloseRequest) (publicops.CloseResult, error) {
	guarded := req.IssueID
	if req.Force {
		guarded = "" // a forced close is not guarded, so it resolves nothing
	}
	ops, err := l.ops(ctx, guarded)
	if err != nil {
		return publicops.CloseResult{}, err
	}
	return ops.Close(ctx, req)
}

func (l *resolvingLifecycle) Reopen(ctx context.Context, req publicops.ReopenRequest) (publicops.ReopenResult, error) {
	ops, err := l.ops(ctx, "")
	if err != nil {
		return publicops.ReopenResult{}, err
	}
	return ops.Reopen(ctx, req)
}

// IssueReader, ReadyCounter, ReadyLister and ReadyClaimer use the SAME role wrappers as the
// store decorator (policy_roles.go): the exclusions are read once, in a
// read-only unit of work of the undecorated provider, and the request is
// delegated to a role over that undecorated provider. A role built over this
// wrapper instead would apply the policy a second time through the use-case
// overrides below, which remain for raw-UOW callers and List(ReadyFlag).
func (p *uowProvider) IssueReader() (publicops.Reader, error) {
	rest, err := uow.NewIssueReader(p)
	if err != nil {
		return nil, err
	}
	inner, err := uow.NewIssueReader(p.UnitOfWorkProvider)
	if err != nil {
		return nil, err
	}
	return newPolicyReader(rest, inner, p.readyPolicy()), nil
}

// IssueClaimer builds the claim-by-id role over THIS wrapper, so its unit of
// work's IssueUseCase is the policy's and ClaimIssue below refuses externally
// blocked work inside the claim's own transaction. That override covers this
// accessor and any raw-UOW caller of ClaimIssue/ClaimWisp. `bd update --claim`
// does NOT reach it: the lifecycle's Update runs ApplyUpdate, whose claim
// calls the undecorated use case's own ClaimIssue, so ApplyUpdate below
// carries the same guard for a spec with Claim set.
//
// Each Claim resolves the claimed issue's `external:` refs FIRST, with no
// transaction open (resolvingClaimer, preResolved), so the check inside the
// claim's write transaction touches no foreign project. IssueLifecycle does
// the same for a claim or a close.
func (p *uowProvider) IssueClaimer() (publicops.Claimer, error) {
	if _, err := uow.NewIssueClaimer(p); err != nil {
		return nil, err
	}
	return &resolvingClaimer{provider: p}, nil
}

// resolvingClaimer is the claim-by-id role over this provider with the claimed
// issue's external refs resolved before the claim's write transaction.
type resolvingClaimer struct {
	provider *uowProvider
}

var _ publicops.Claimer = (*resolvingClaimer)(nil)

func (c *resolvingClaimer) Claim(ctx context.Context, req publicops.ClaimRequest) (publicops.ClaimResult, error) {
	resolved, err := c.provider.resolveFor(ctx, req.IssueID)
	if err != nil {
		return publicops.ClaimResult{}, err
	}
	role, err := uow.NewIssueClaimer(c.provider.withResolved(resolved))
	if err != nil {
		return publicops.ClaimResult{}, err
	}
	return role.Claim(ctx, req)
}

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
	inner, err := uow.NewReadyCounter(p.UnitOfWorkProvider)
	if err != nil {
		return nil, err
	}
	return newPolicyReadyCounter(inner, p.readyPolicy()), nil
}
func (p *uowProvider) ReadyLister() (publicops.ReadyLister, error) {
	inner, err := uow.NewReadyLister(p.UnitOfWorkProvider)
	if err != nil {
		return nil, err
	}
	return newPolicyReadyLister(inner, p.readyPolicy()), nil
}
func (p *uowProvider) ReadyClaimer() (publicops.ReadyClaimer, error) {
	inner, err := uow.NewReadyClaimer(p.UnitOfWorkProvider)
	if err != nil {
		return nil, err
	}
	return newPolicyReadyClaimer(inner, p.readyPolicy()), nil
}

// readyPolicy reads the edges — the workspace's, or a guard's named issues'
// own — in a read-only unit of work of the undecorated provider, one per call.
func (p *uowProvider) readyPolicy() readyPolicy {
	return readyPolicy{policy: p.policy, edges: p.externalEdges, own: p.ownEdges}
}

// ownEdges reads ids' own dependency records, from both planes, in one
// read-only unit of work of the undecorated provider.
func (p *uowProvider) ownEdges(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
	return uow.RunTxRead(ctx, p.UnitOfWorkProvider, func(ctx context.Context, uw uow.UnitOfWork) (map[string][]*types.Dependency, error) {
		return uw.DependencyUseCase().GetIssueDependencyRecords(ctx, ids)
	})
}

func (p *uowProvider) externalEdges(ctx context.Context) (map[string][]*types.Dependency, error) {
	return uow.RunTxRead(ctx, p.UnitOfWorkProvider, func(ctx context.Context, uw uow.UnitOfWork) (map[string][]*types.Dependency, error) {
		return uw.DependencyUseCase().GetExternalBlockingDependencyRecords(ctx)
	})
}
func (p *uowProvider) Querier() (publicops.Querier, error) { return uow.NewQuerier(p) }
func (p *uowProvider) StatsReporter() (publicops.StatsReporter, error) {
	return uow.NewStatsReporter(p)
}
func (p *uowProvider) CycleDetector() (publicops.CycleDetector, error) {
	return uow.NewCycleDetector(p)
}
func (p *uowProvider) Commenter() (publicops.Commenter, error) { return uow.NewCommenter(p) }

// BatchCloser uses the SAME policy wrapper as the store decorator: one edge
// read guards every item (re-closing an already-closed item stays a no-op) and
// narrows the claim the batch earns, and the batch itself runs on a closer over
// the undecorated provider, so the use-case overrides do not apply the policy
// a second time — once per item — beneath it.
func (p *uowProvider) BatchCloser() (publicops.BatchCloser, error) {
	inner, err := uow.NewBatchCloser(p.UnitOfWorkProvider)
	if err != nil {
		return nil, err
	}
	closer := newPolicyBatchCloser(inner, p.readyPolicy())
	closer.guarded = func(flagged map[string][]string) (publicops.BatchCloser, error) {
		return uow.NewBatchCloser(&batchCloseGuard{UnitOfWorkProvider: p.UnitOfWorkProvider, flagged: flagged})
	}
	return closer, nil
}

// batchCloseGuard is the undecorated provider for ONE batch close, plus the
// items an unsatisfied external blocker holds. Its units of work refuse closing
// a flagged item unless that item is already closed IN THE SAME TRANSACTION,
// so the re-close exemption and the close are one atomic decision.
//
// It overrides the two close use-case methods, and ApplyUpdate for an update
// that sets the status to closed (an apply-batch update item), and nothing
// else — in particular not the ready reads the batch's ClaimNext runs, whose
// exclusions the policy closer already put on the request. Building the batch
// over the policy provider instead would apply the ready policy a second time,
// inside the write transaction. The same guard serves an apply-batch
// (policyBatchApplier), whose close and closing-update items reach exactly
// these three methods.
type batchCloseGuard struct {
	uow.UnitOfWorkProvider
	flagged map[string][]string
}

func (p *batchCloseGuard) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	inner, err := p.UnitOfWorkProvider.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	return &batchCloseGuardUOW{UnitOfWork: inner, flagged: p.flagged}, nil
}

type batchCloseGuardUOW struct {
	uow.UnitOfWork
	flagged map[string][]string
}

// Unwrap keeps the transaction runner reachable, as unitOfWork.Unwrap does.
func (u *batchCloseGuardUOW) Unwrap() uow.UnitOfWork { return u.UnitOfWork }

func (u *batchCloseGuardUOW) IssueUseCase() domain.IssueUseCase {
	return &batchCloseGuardIssues{IssueUseCase: u.UnitOfWork.IssueUseCase(), flagged: u.flagged}
}

type batchCloseGuardIssues struct {
	domain.IssueUseCase
	flagged map[string][]string
}

func (u *batchCloseGuardIssues) guard(ctx context.Context, id string, force bool) error {
	blockers := u.flagged[id]
	if force || len(blockers) == 0 {
		return nil
	}
	closed, err := closedInUOW(ctx, u.IssueUseCase, id)
	if err != nil {
		return err
	}
	if !closed {
		return externallyBlocked(id, blockers)
	}
	return nil
}

func (u *batchCloseGuardIssues) CloseIssueChecked(ctx context.Context, id string, params domain.CloseIssueParams, actor string, force bool) (domain.CloseIssueResult, error) {
	if err := u.guard(ctx, id, force); err != nil {
		return domain.CloseIssueResult{}, err
	}
	return u.IssueUseCase.CloseIssueChecked(ctx, id, params, actor, force)
}

// ApplyUpdate guards an update that sets a flagged item's status to closed —
// an apply-batch update item — with the close methods' check. A forced update
// (ForceClosePolicy, carried as the OpForceClosePolicy marker) is not guarded.
func (u *batchCloseGuardIssues) ApplyUpdate(ctx context.Context, id string, spec domain.UpdateSpec, actor string) (*types.Issue, error) {
	if isClosedUpdate(spec.Fields) {
		if err := u.guard(ctx, id, forcedUpdate(spec.Fields)); err != nil {
			return nil, err
		}
	}
	return u.IssueUseCase.ApplyUpdate(ctx, id, spec, actor)
}

func (u *batchCloseGuardIssues) CloseWispChecked(ctx context.Context, id string, params domain.CloseIssueParams, actor string, force bool) (domain.CloseIssueResult, error) {
	if err := u.guard(ctx, id, force); err != nil {
		return domain.CloseIssueResult{}, err
	}
	return u.IssueUseCase.CloseWispChecked(ctx, id, params, actor, force)
}
func (p *uowProvider) BatchCreator() (publicops.BatchCreator, error) {
	return uow.NewBatchCreator(p)
}
func (p *uowProvider) DependencyEditor() (publicops.DependencyEditor, error) {
	return uow.NewDependencyEditor(p)
}

// BatchApplier guards the apply-batch items that close with the SAME wrapper
// as the store arm (policyBatchApplier): one read of the closing items' OWN
// edges (GetIssueDependencyRecords, both planes) and their foreign refs
// resolved before the batch's transaction, and the batch itself
// run over the UNDECORATED provider — it used to be built over this wrapper,
// whose use-case overrides re-read the edges and opened every referenced
// foreign project inside the write transaction, once per closing item. A
// flagged item's re-close exemption is decided inside the batch's own
// transaction by batchCloseGuard.
func (p *uowProvider) BatchApplier() (publicops.BatchApplier, error) {
	inner, err := uow.NewBatchApplier(p.UnitOfWorkProvider)
	if err != nil {
		return nil, err
	}
	return &policyBatchApplier{
		inner:  inner,
		policy: p.policy,
		own:    p.ownEdges,
		guarded: func(flagged map[string][]string) (publicops.BatchApplier, error) {
			return uow.NewBatchApplier(&batchCloseGuard{UnitOfWorkProvider: p.UnitOfWorkProvider, flagged: flagged})
		},
	}, nil
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

// SetVersionedHistoryEnabled forwards dual-write issue-version activation
// inward, for the same reason SetEventsJournalEnabled does.
//
// This wrapper is the OUTERMOST provider on both real chains — cmd/bd/main.go
// and cmd/bd/serve.go each build wireExternalDependencyUOWProvider(...) around
// everything else — and activation in this family works by type-asserting the
// outermost provider value for the configurer interface. Without this method
// the assertion fails on exactly the chains that matter, so activation would
// silently enable nothing on uow-backed processes while the direct-store plane
// turned on: split-brain, with no error and no log. The inner provider's own
// forwarder cannot be reached from those call sites.
func (p *uowProvider) SetVersionedHistoryEnabled(enabled bool) {
	if configurer, ok := p.UnitOfWorkProvider.(storage.VersionedHistoryConfigurer); ok {
		configurer.SetVersionedHistoryEnabled(enabled)
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
	_ uow.PoolTuner                      = (*uowProvider)(nil)
	_ storage.EventsJournalConfigurer    = (*uowProvider)(nil)
	_ storage.VersionedHistoryConfigurer = (*uowProvider)(nil)
	_ issueops.EventsMaintenanceRunner   = (*uowProvider)(nil)
	_ uow.IssueLifecycleSource           = (*uowProvider)(nil)
	_ uow.IssueReaderSource              = (*uowProvider)(nil)
	_ uow.IssueClaimerSource             = (*uowProvider)(nil)
	_ uow.RelationsSource                = (*uowProvider)(nil)
	_ uow.EdgeReaderSource               = (*uowProvider)(nil)
	_ uow.BlockingAnnotatorSource        = (*uowProvider)(nil)
	_ uow.TreeWalkerSource               = (*uowProvider)(nil)
	_ uow.GraphCounterSource             = (*uowProvider)(nil)
	_ uow.CounterSource                  = (*uowProvider)(nil)
	_ uow.ReadyCounterSource             = (*uowProvider)(nil)
	_ uow.ReadyListerSource              = (*uowProvider)(nil)
	_ uow.ReadyClaimerSource             = (*uowProvider)(nil)
	_ uow.QuerierSource                  = (*uowProvider)(nil)
	_ uow.StatsReporterSource            = (*uowProvider)(nil)
	_ uow.CycleDetectorSource            = (*uowProvider)(nil)
	_ uow.CommenterSource                = (*uowProvider)(nil)
	_ uow.BatchCloserSource              = (*uowProvider)(nil)
	_ uow.BatchCreatorSource             = (*uowProvider)(nil)
	_ uow.DependencyEditorSource         = (*uowProvider)(nil)
	_ uow.BatchApplierSource             = (*uowProvider)(nil)
	_ uow.DeleterSource                  = (*uowProvider)(nil)
	_ uow.SweeperSource                  = (*uowProvider)(nil)
	_ uow.ImporterSource                 = (*uowProvider)(nil)
	_ uow.BootstrapperSource             = (*uowProvider)(nil)
	_ uow.InitVerifierSource             = (*uowProvider)(nil)
	_ uow.WorkspaceConfigSource          = (*uowProvider)(nil)
	_ uow.VersionReconcilerSource        = (*uowProvider)(nil)
	_ uow.MetadataCASSource              = (*uowProvider)(nil)
	_ uow.ReleaserSource                 = (*uowProvider)(nil)
	_ uow.MemoriesSource                 = (*uowProvider)(nil)
	_ uow.EventsJournalCursorSource      = (*uowProvider)(nil)
)

type unitOfWork struct {
	uow.UnitOfWork
	policy   *Policy
	resolved *preResolved
	issue    domain.IssueUseCase
	deps     domain.DependencyUseCase
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
			resolved:     u.resolved,
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
	deps     domain.DependencyUseCase
	policy   *Policy
	resolved *preResolved
}

// blockersOf returns the unsatisfied external refs holding id back, read in
// THIS unit of work.
//
// With a pre-resolved verdict (a guarded role's call) it re-reads only id's
// own edges — one indexed query, no foreign IO inside the write transaction —
// and looks each of id's refs up in it; a ref the resolution did
// not see fails closed. Without one (a raw-UOW caller) it resolves id's own
// refs in place — never another issue's — which is the only answer available
// to a caller that opened the transaction itself.
func (u *issueUseCase) blockersOf(ctx context.Context, id string) ([]string, error) {
	if u.resolved == nil {
		refs, err := u.policy.blockersOf(ctx, u.deps.GetIssueDependencyRecords, []string{id})
		if err != nil {
			return nil, err
		}
		return refs[id], nil
	}
	edges, err := u.deps.GetIssueDependencyRecords(ctx, []string{id})
	if err != nil {
		return nil, fmt.Errorf("external dependencies: list blocking records: %w", err)
	}
	var blockers []string
	for _, dep := range edges[id] {
		if dep == nil || !dep.Type.IsBlockingEdge() || !isExternalReference(dep.DependsOnID) {
			continue
		}
		if !u.resolved.satisfied[dep.DependsOnID] {
			blockers = appendUnique(blockers, dep.DependsOnID)
		}
	}
	return blockers, nil
}

// blockingState reads the edges in THIS unit of work, so raw-UOW callers (the
// proxied `bd ready` / `bd list --ready` paths that consume a filter rather
// than a role) see the policy inside their own transaction.
func (u *issueUseCase) blockingState(ctx context.Context) (blockingState, error) {
	return u.policy.exclusionState(ctx, u.deps.GetExternalBlockingDependencyRecords)
}

func (u *issueUseCase) GetReadyWork(ctx context.Context, filter types.WorkFilter) (domain.SearchPage, error) {
	state, err := u.blockingState(ctx)
	if err != nil {
		return domain.SearchPage{}, err
	}
	return u.IssueUseCase.GetReadyWork(ctx, withExternalExclusions(filter, state.refsByIssue))
}

func (u *issueUseCase) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) (domain.SearchCountsPage, error) {
	state, err := u.blockingState(ctx)
	if err != nil {
		return domain.SearchCountsPage{}, err
	}
	return u.IssueUseCase.GetReadyWorkWithCounts(ctx, withExternalExclusions(filter, state.refsByIssue))
}

func (u *issueUseCase) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (domain.ClaimReadyResult, error) {
	state, err := u.blockingState(ctx)
	if err != nil {
		return domain.ClaimReadyResult{}, err
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
		return nil, err
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

// ClaimIssue refuses a claim-by-id of an issue an unsatisfied external blocker
// holds back, reading the edges in THIS unit of work before the compare-and-set
// runs in it. The store arm has always refused this (externaldeps.Store's
// IssueClaimer); without the override the unit-of-work arm's claim role —
// serve's provider arm builds it over this provider — claimed it.
//
// It does NOT cover `bd update --claim`, whatever this comment used to say:
// that path is Lifecycle.Update -> ApplyUpdate, and the undecorated
// ApplyUpdate calls its OWN ClaimIssue, not this override. ApplyUpdate below
// guards a Claim spec itself.
func (u *issueUseCase) ClaimIssue(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	if err := u.guardExternalClaim(ctx, id); err != nil {
		return domain.ClaimResult{}, err
	}
	return u.IssueUseCase.ClaimIssue(ctx, id, actor)
}

// ClaimWisp is ClaimIssue's guard for the ephemeral plane. Wisps carry
// `external:` edges too (wisp_dependencies is read with dependencies).
func (u *issueUseCase) ClaimWisp(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	if err := u.guardExternalClaim(ctx, id); err != nil {
		return domain.ClaimResult{}, err
	}
	return u.IssueUseCase.ClaimWisp(ctx, id, actor)
}

// ClaimIssueIfOpen is the molecule port's step claim (`bd close --continue`
// auto-advancing to the next ready step, uowMolWriter.ClaimStepIfOpen). The
// molecule's step readiness is computed from within-molecule edges only, so a
// step an unsatisfied `external:` blocker holds looked ready and was claimed.
// It now refuses with ErrClaimBlocked, the claim-by-id refusal, and the caller
// moves on to the next ready step. A caller that pre-resolved the steps
// (PreResolve) pays no foreign IO here.
func (u *issueUseCase) ClaimIssueIfOpen(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	if err := u.guardExternalClaim(ctx, id); err != nil {
		return domain.ClaimResult{}, err
	}
	return u.IssueUseCase.ClaimIssueIfOpen(ctx, id, actor)
}

// ClaimWispIfOpen is ClaimIssueIfOpen's guard for the ephemeral plane, where
// wisp molecules' steps live.
func (u *issueUseCase) ClaimWispIfOpen(ctx context.Context, id, actor string) (domain.ClaimResult, error) {
	if err := u.guardExternalClaim(ctx, id); err != nil {
		return domain.ClaimResult{}, err
	}
	return u.IssueUseCase.ClaimWispIfOpen(ctx, id, actor)
}

// GuardClaimInUOW answers, without claiming, whether the external-dependency
// guard would refuse claiming id in uw: ErrClaimBlocked (which wraps
// ErrNotClaimable) when uw's decorator chain carries this package's policy and
// an unsatisfied `external:` blocker holds id, nil otherwise — an unpoliced
// unit of work answers nil, as GuardClaim does for an unpoliced store.
//
// It is the check ClaimIssueIfOpen / ClaimWispIfOpen make before claiming, for
// a caller that only SUGGESTS a claim (`bd close --continue --no-auto`) and
// must not suggest one the claim would refuse. It reads only id's own edges in
// uw; a caller that PreResolve'd id opens no foreign project here, and a ref
// the pre-resolution did not see fails closed.
func GuardClaimInUOW(ctx context.Context, uw uow.UnitOfWork, id string) error {
	for uw != nil {
		if policy, ok := uw.(*unitOfWork); ok {
			if issues, ok := policy.IssueUseCase().(*issueUseCase); ok {
				return issues.guardExternalClaim(ctx, id)
			}
			return nil
		}
		inner, ok := uw.(interface{ Unwrap() uow.UnitOfWork })
		if !ok {
			return nil
		}
		uw = inner.Unwrap()
	}
	return nil
}

// guardExternalClaim refuses claiming id while an unsatisfied external blocker
// holds it back. There is no force bypass: a claim is not a close, and neither
// ReadyClaimer nor the claim-by-id role has ever offered one.
func (u *issueUseCase) guardExternalClaim(ctx context.Context, id string) error {
	blockers, err := u.blockersOf(ctx, id)
	if err != nil {
		return err
	}
	if len(blockers) > 0 {
		return externallyBlockedClaim(id, blockers)
	}
	return nil
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

// ApplyUpdate guards the two policy-relevant things an update can do: claim
// (spec.Claim — `bd update --claim` and Lifecycle.Update's Claim, which the
// undecorated body claims through its own ClaimIssue rather than the override
// above) and close. Both checks run in THIS unit of work, before the update.
func (u *issueUseCase) ApplyUpdate(ctx context.Context, id string, spec domain.UpdateSpec, actor string) (*types.Issue, error) {
	if spec.Claim {
		if err := u.guardExternalClaim(ctx, id); err != nil {
			return nil, err
		}
	}
	if isClosedUpdate(spec.Fields) {
		// ForceClosePolicy (`bd update --status closed --force`, PATCH's
		// force_close_policy) reaches here as the OpForceClosePolicy marker; it
		// used to be ignored, so this arm refused a forced close the store arm's
		// lifecycle and every close route let through.
		if err := u.guardExternalClose(ctx, id, forcedUpdate(spec.Fields)); err != nil {
			return nil, err
		}
	}
	return u.IssueUseCase.ApplyUpdate(ctx, id, spec, actor)
}

// forcedUpdate reports whether an update spec carries ForceClosePolicy.
func forcedUpdate(fields map[string]any) bool {
	forced, _ := fields[issueops.OpForceClosePolicy].(bool)
	return forced
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
	blockers, err := u.blockersOf(ctx, id)
	if err != nil {
		return err
	}
	refused, err := closeRefused(ctx, u.issueClosed, id, blockers)
	if err != nil {
		return err
	}
	if refused {
		return externallyBlocked(id, blockers)
	}
	return nil
}

// issueClosed answers the re-close exemption in THIS unit of work, from either
// plane.
func (u *issueUseCase) issueClosed(ctx context.Context, id string) (bool, error) {
	return closedInUOW(ctx, u.IssueUseCase, id)
}

// missingAtSeam reports whether err is one of the shapes a storage seam uses to
// say "no such row".
//
// The seams disagree, so the test is a disjunction rather than the
// storage.ErrNotFound check the store arm's siblings use: this decorator sits
// on the DOMAIN seam, where db.issueSQLRepositoryImpl.Get returns sql.ErrNoRows
// unchanged and issueUseCaseImpl.get wraps it with the id. Testing only for
// storage.ErrNotFound here would classify an ordinary miss as a hard error and
// lose the wisp fallback entirely. internal/workapi.isNotFound answers the same
// question for the same reason; it stays unexported there deliberately, so the
// policy layer carries its own copy rather than widening that package's surface.
func missingAtSeam(err error) bool {
	return errors.Is(err, storage.ErrNotFound) || errors.Is(err, sql.ErrNoRows)
}

func closedInUOW(ctx context.Context, issues domain.IssueUseCase, id string) (bool, error) {
	issue, err := issues.GetIssue(ctx, id)
	if err == nil && issue != nil {
		return issue.Status == types.StatusClosed, nil
	}
	if err != nil && !missingAtSeam(err) {
		// A transient DB or transaction failure is not "no such issue". Swallowing
		// it here would hand the caller a false "not closed", which the
		// blockers-non-empty path reports as ErrCloseBlocked — a permanent-looking
		// refusal whose remedy advice is --force, for what was an infrastructure
		// blip. pinReClose and the store arm's issueClosed both propagate; this is
		// the third implementation of one exemption and it answers the same way.
		return false, err
	}
	// A miss on the issues plane is not an answer: the id may be a wisp.
	wisp, werr := issues.GetWisp(ctx, id)
	if werr == nil && wisp != nil {
		return wisp.Status == types.StatusClosed, nil
	}
	if werr != nil && !missingAtSeam(werr) {
		return false, werr
	}
	// On neither plane: not closed, and not an error.
	return false, nil
}

type dependencyUseCase struct {
	domain.DependencyUseCase
	policy *Policy
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
