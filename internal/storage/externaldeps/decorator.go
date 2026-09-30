package externaldeps

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// Store decorates a local store with query-time external capability handling.
type Store struct {
	storage.DoltStorage
	*Policy
	inner storage.DoltStorage
}

// Policy is the one definition of which issues external capability blockers
// hold back. It resolves `external:<project>:<capability>` edges against the
// configured foreign projects and knows nothing about where those edges were
// read from: the store decorator and the unit-of-work decorator each hand it
// an EdgeSource over their own seam, and every ready, count, claim and close
// guard in this package derives its answer from Exclusions.
type Policy struct {
	locateProject ProjectLocator
	openProject   StoreOpener
	warnProject   func(ProjectName)
	warnMu        sync.Mutex
	warned        map[ProjectName]struct{}
}

// EdgeSource reads the explicit external blocking edges of one workspace,
// keyed by source issue id: a store's GetExternalBlockingDependencyRecords,
// or a read-only unit of work's DependencyUseCase one.
type EdgeSource func(context.Context) (map[string][]*types.Dependency, error)

// OwnEdgeSource reads the dependency records whose SOURCE is one of ids, from
// both planes, keyed by source id: a store's GetDependencyRecordsForIssues, or
// a unit of work's DependencyUseCase one. It is what a guard on named work
// reads instead of the whole workspace's external edges, so an unrelated
// issue's `external:` edge costs it neither a foreign open nor a warning.
type OwnEdgeSource func(ctx context.Context, ids []string) (map[string][]*types.Dependency, error)

// NewPolicy constructs the resolving half of the external capability policy.
func NewPolicy(locateProject ProjectLocator, openProject StoreOpener) *Policy {
	return &Policy{
		locateProject: locateProject,
		openProject:   openProject,
		warnProject:   defaultProjectWarning,
		warned:        make(map[ProjectName]struct{}),
	}
}

// New constructs an external-capability-aware storage decorator.
func New(inner storage.DoltStorage, locateProject ProjectLocator, openProject StoreOpener) *Store {
	return &Store{
		DoltStorage: inner,
		Policy:      NewPolicy(locateProject, openProject),
		inner:       inner,
	}
}

// Exclusions reads the workspace's external blocking edges from edges once
// and returns every source issue that still has an unsatisfied one, mapped to
// those refs. Malformed refs, unconfigured projects and foreign read failures
// all count as unsatisfied: the policy fails closed.
//
// The ids are what a ready role puts in ReadyRequest.ExcludeIDs, and a
// source's refs are what a close or claim guard reports in ErrCloseBlocked.
func (p *Policy) Exclusions(ctx context.Context, edges EdgeSource) (map[string][]string, error) {
	state, err := p.exclusionState(ctx, edges)
	if err != nil {
		return nil, err
	}
	return state.refsByIssue, nil
}

// blockersOf reads ids' OWN edges from edges and returns every one of ids an
// unsatisfied `external:` blocker holds, mapped to those refs. Only the refs
// ids carry are resolved, so a foreign project only some other issue
// references is neither opened nor warned about. It fails closed exactly as
// Exclusions does.
func (p *Policy) blockersOf(ctx context.Context, edges OwnEdgeSource, ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return map[string][]string{}, nil
	}
	deps, err := edges(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: list blocking records: %w", err)
	}
	own := make(map[string][]*types.Dependency, len(ids))
	for _, id := range ids {
		own[id] = deps[id]
	}
	state, err := p.blockingStateFromRecords(ctx, own)
	if err != nil {
		return nil, err
	}
	return state.refsByIssue, nil
}

func (p *Policy) exclusionState(ctx context.Context, edges EdgeSource) (blockingState, error) {
	allDeps, err := edges(ctx)
	if err != nil {
		return blockingState{}, fmt.Errorf("external dependencies: list blocking records: %w", err)
	}
	return p.blockingStateFromRecords(ctx, allDeps)
}

// Wrap installs the external-capability policy on store unless the store's
// server already enforces it. It is the one composition entry point every
// storage chain should use: a store whose innermost layer implements
// storage.ServerEnforcedPolicy and answers true is returned unchanged, so the
// request is forwarded and the server applies the policy once. Every other
// store — including backends this package has never heard of — is wrapped,
// so an unrecognized store fails closed rather than silently unpoliced.
func Wrap(store storage.DoltStorage, locateProject ProjectLocator, openProject StoreOpener) storage.DoltStorage {
	if store == nil {
		return nil
	}
	if remote, ok := storage.UnwrapStore(store).(storage.ServerEnforcedPolicy); ok && remote.PolicyEnforcedByServer() {
		return store
	}
	return New(store, locateProject, openProject)
}

// Composed reports whether v IS this package's policy layer — a store built by
// New or Wrap (when Wrap wrapped), or a provider built by WrapUOWProvider — so
// that every role its own accessors build carries the external-dependency
// policy.
//
// It looks at v itself and never beneath it, on purpose: a policy layer buried
// under another decorator reaches the roles taken off the outer value only if
// that decorator delegates its accessors, which this package cannot see. A
// caller that must advertise the policy (bd serve's
// httpapi.Config.ExternalDependencyPolicy) therefore asks about the exact value
// it takes its roles from, and a store Wrap returned unchanged — a client of a
// bd server that enforces the policy itself — answers false.
func Composed(v any) bool {
	switch v.(type) {
	case *Store, *uowProvider:
		return true
	default:
		return false
	}
}

// Unwrap exposes the decorated store to storage.UnwrapStore.
func (s *Store) Unwrap() storage.DoltStorage { return s.inner }

// IssueLifecycle preserves the external close policy for public lifecycle
// operations. Returning the inner lifecycle directly would promote around the
// decorator when bd close or bd update uses the lifecycle seam.
func (s *Store) IssueLifecycle() (publicops.Lifecycle, error) {
	inner, err := s.inner.IssueLifecycle()
	if err != nil {
		return nil, err
	}
	return &lifecycle{inner: inner, policy: s}, nil
}

type lifecycle struct {
	inner  publicops.Lifecycle
	policy *Store
}

var _ publicops.Lifecycle = (*lifecycle)(nil)

func (l *lifecycle) Create(ctx context.Context, request publicops.CreateRequest) (publicops.CreateResult, error) {
	return l.inner.Create(ctx, request)
}

// Update guards a claim (request.Claim, `bd update --claim`) and a close
// (a status patch to closed). The inner lifecycle claims through the backend's
// own compare-and-set, which knows nothing of `external:` edges, so without
// the claim half `bd update --claim` took externally blocked work on this arm
// while `bd ready --claim` and the claim-by-id role refused it. A claim has no
// force bypass; ForceClosePolicy applies to the close half only.
func (l *lifecycle) Update(ctx context.Context, request publicops.UpdateRequest) (publicops.UpdateResult, error) {
	if request.Claim {
		if err := l.policy.guardExternalClaim(ctx, request.IssueID); err != nil {
			return publicops.UpdateResult{}, err
		}
	}
	if request.Patch.Status.Set && string(request.Patch.Status.Value) == string(types.StatusClosed) {
		if err := l.policy.guardExternalClose(ctx, request.IssueID, request.ForceClosePolicy); err != nil {
			return publicops.UpdateResult{}, err
		}
	}
	return l.inner.Update(ctx, request)
}

func (l *lifecycle) Close(ctx context.Context, request publicops.CloseRequest) (publicops.CloseResult, error) {
	if err := l.policy.guardExternalClose(ctx, request.IssueID, request.Force); err != nil {
		return publicops.CloseResult{}, err
	}
	return l.inner.Close(ctx, request)
}

func (l *lifecycle) Reopen(ctx context.Context, request publicops.ReopenRequest) (publicops.ReopenResult, error) {
	return l.inner.Reopen(ctx, request)
}

// guardExternalClaim refuses claiming id while an unsatisfied external blocker
// holds it back — the SAME set the unit-of-work arm refuses: external blockers
// only. A local blocker does not refuse a claim-by-id on either arm, as it
// never has on the unpoliced backend. The store arm has no transaction to
// share with the claim, so this reads as of the call, like every other
// store-arm guard; it resolves only id's own refs.
func (s *Store) guardExternalClaim(ctx context.Context, id string) error {
	blockers, err := s.externalBlockersOf(ctx, id)
	if err != nil {
		return err
	}
	if len(blockers) > 0 {
		return externallyBlockedClaim(id, blockers)
	}
	return nil
}

// GuardClaim refuses claiming id through store while an unsatisfied
// `external:` blocker holds it back (ErrClaimBlocked, which wraps
// ErrNotClaimable), when store's decorator chain carries this package's
// policy; any other store — unpoliced, or a client of a server that enforces
// the policy itself — answers nil.
//
// It is for callers that claim inside a storage transaction they open
// themselves, which no Store override can see (the molecule port's step claim,
// storeMolWriter.ClaimStepIfOpen). It resolves before that transaction opens,
// and only id's own refs.
func GuardClaim(ctx context.Context, store storage.DoltStorage, id string) error {
	for store != nil {
		if policy, ok := store.(*Store); ok {
			return policy.guardExternalClaim(ctx, id)
		}
		inner, ok := store.(interface{ Unwrap() storage.DoltStorage })
		if !ok {
			return nil
		}
		store = inner.Unwrap()
	}
	return nil
}

// externalBlockersOf returns id's unsatisfied external refs, reading and
// resolving only id's own edges.
func (s *Store) externalBlockersOf(ctx context.Context, id string) ([]string, error) {
	refs, err := s.blockersOf(ctx, s.inner.GetDependencyRecordsForIssues, []string{id})
	if err != nil {
		return nil, err
	}
	return refs[id], nil
}

// guardExternalClose refuses closing id while an unsatisfied external blocker
// holds it, unless forced or id is already closed. It reads and resolves only
// id's own edges: it used to read every external edge in the workspace and
// open every foreign project they named, so closing one issue warned about an
// unrelated issue's unavailable project.
func (s *Store) guardExternalClose(ctx context.Context, id string, force bool) error {
	if force {
		return nil
	}
	blockers, err := s.externalBlockersOf(ctx, id)
	if err != nil {
		return err
	}
	refused, err := closeRefused(ctx, s.issueClosed, id, blockers)
	if err != nil {
		return err
	}
	if refused {
		return externallyBlocked(id, blockers)
	}
	return nil
}

func (p *Policy) warnUnresolvedProject(project ProjectName) {
	p.warnMu.Lock()
	defer p.warnMu.Unlock()
	if _, warned := p.warned[project]; warned {
		return
	}
	p.warned[project] = struct{}{}
	if p.warnProject != nil {
		p.warnProject(project)
	}
}

type blockingState struct {
	refsByIssue map[string][]string
}

func (s *Store) loadBlockingState(ctx context.Context) (blockingState, error) {
	return s.exclusionState(ctx, s.edgeSource())
}

// edgeSource is where this store's external blocking edges are read from. The
// narrow query is taken from beneath every decorator, so it is not spanned or
// hooked; that is unchanged from before the policy moved to Exclusions.
func (s *Store) edgeSource() EdgeSource {
	if queryStore, ok := storage.UnwrapStore(s.inner).(storage.ExternalDependencyQueryStore); ok {
		return queryStore.GetExternalBlockingDependencyRecords
	}
	// Compatibility fallback for third-party stores that predate the narrow
	// optional capability. First-party stores implement the indexed query.
	return s.inner.GetAllDependencyRecords
}

func (p *Policy) blockingStateFromRecords(ctx context.Context, allDeps map[string][]*types.Dependency) (blockingState, error) {
	refs := make([]reference, 0)
	refsByIssue := make(map[string][]string)
	for issueID, deps := range allDeps {
		for _, dep := range deps {
			if dep == nil || !dep.Type.IsBlockingEdge() || !isExternalReference(dep.DependsOnID) {
				continue
			}
			refs = append(refs, parseReference(dep.DependsOnID))
			refsByIssue[issueID] = appendUnique(refsByIssue[issueID], dep.DependsOnID)
		}
	}

	satisfied, err := p.resolveReferences(ctx, refs)
	if err != nil {
		return blockingState{}, fmt.Errorf("external dependencies: resolve blockers: %w", err)
	}
	for issueID, issueRefs := range refsByIssue {
		unsatisfied := issueRefs[:0]
		for _, ref := range issueRefs {
			if !satisfied[ref] {
				unsatisfied = append(unsatisfied, ref)
			}
		}
		if len(unsatisfied) == 0 {
			delete(refsByIssue, issueID)
			continue
		}
		refsByIssue[issueID] = unsatisfied
	}

	return blockingState{refsByIssue: refsByIssue}, nil
}

// GetReadyWork excludes sources with unsatisfied external blocking edges.
func (s *Store) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	state, err := s.loadBlockingState(ctx)
	if err != nil {
		return nil, err
	}
	filter = withExternalExclusions(filter, state.refsByIssue)
	return s.inner.GetReadyWork(ctx, filter)
}

// GetReadyWorkWithCounts is the counts-bearing equivalent of GetReadyWork.
func (s *Store) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	state, err := s.loadBlockingState(ctx)
	if err != nil {
		return nil, err
	}
	filter = withExternalExclusions(filter, state.refsByIssue)
	return s.inner.GetReadyWorkWithCounts(ctx, filter)
}

// GetReadyWorkWithCountsAndTotal applies the same external exclusions as
// GetReadyWorkWithCounts, so the page and its total describe one ready set.
// It must be overridden here: the embedded passthrough would reach the inner
// store without the exclusions.
func (s *Store) GetReadyWorkWithCountsAndTotal(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, int, error) {
	state, err := s.loadBlockingState(ctx)
	if err != nil {
		return nil, 0, err
	}
	filter = withExternalExclusions(filter, state.refsByIssue)
	return s.inner.GetReadyWorkWithCountsAndTotal(ctx, filter)
}

func withExternalExclusions(filter types.WorkFilter, refsByIssue map[string][]string) types.WorkFilter {
	filter.ExcludeIDs = unionExcludedIDs(filter.ExcludeIDs, refsByIssue)
	return filter
}

// unionExcludedIDs is the one merge rule for exclusions, shared by the
// store-level filter overrides and the role wrappers: the caller's ids first,
// in their order, then every newly excluded source in sorted order. It always
// returns a fresh slice, so the caller's is never written through.
func unionExcludedIDs(existing []string, refsByIssue map[string][]string) []string {
	out := slices.Clone(existing)
	newIDs := make([]string, 0, len(refsByIssue))
	for issueID := range refsByIssue {
		if !slices.Contains(out, issueID) {
			newIDs = append(newIDs, issueID)
		}
	}
	sort.Strings(newIDs)
	return append(out, newIDs...)
}

// CountReadyWork reports the externally filtered ready count.
func (s *Store) CountReadyWork(ctx context.Context, filter types.WorkFilter) (int, error) {
	filter.Limit = 0
	filter.Offset = 0
	state, err := s.loadBlockingState(ctx)
	if err != nil {
		return 0, err
	}
	filter = withExternalExclusions(filter, state.refsByIssue)
	if counter, ok := storage.UnwrapStore(s.inner).(storage.ReadyWorkCounter); ok {
		return counter.CountReadyWork(ctx, filter)
	}
	issues, err := s.inner.GetReadyWork(ctx, filter)
	if err != nil {
		return 0, err
	}
	return len(issues), nil
}

// ClaimReadyIssue resolves external blockers before using the existing local
// compare-and-swap claim operation. Cross-project state cannot be atomic with
// the local claim, but local claim ownership remains race-safe.
func (s *Store) ClaimReadyIssue(ctx context.Context, filter types.WorkFilter, actor string) (*types.Issue, error) {
	state, err := s.loadBlockingState(ctx)
	if err != nil {
		return nil, err
	}
	filter = withExternalExclusions(filter, state.refsByIssue)
	return s.inner.ClaimReadyIssue(ctx, filter, actor)
}

// GetBlockedIssues adds unsatisfied external refs to local blocker details and
// includes sources whose only blockers are external.
func (s *Store) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	base, err := s.inner.GetBlockedIssues(ctx, unpagedBlockedFilter(filter))
	if err != nil {
		return nil, err
	}
	state, err := s.loadBlockingState(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]*types.BlockedIssue, 0, len(base)+len(state.refsByIssue))
	byID := make(map[string]*types.BlockedIssue, len(base)+len(state.refsByIssue))
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
		byID[item.ID] = &clone
	}

	missingIDs := make([]string, 0, len(state.refsByIssue))
	for issueID := range state.refsByIssue {
		if byID[issueID] == nil {
			missingIDs = append(missingIDs, issueID)
		}
	}
	parentDeps := make(map[string][]*types.Dependency)
	if filter.ParentID != nil && len(missingIDs) > 0 {
		parentDeps, err = s.inner.GetDependencyRecordsForIssues(ctx, missingIDs)
		if err != nil {
			return nil, fmt.Errorf("external dependencies: load blocked parent edges: %w", err)
		}
	}
	filteredMissingIDs := missingIDs[:0]
	for _, issueID := range missingIDs {
		if matchesParentFilter(issueID, filter.ParentID, parentDeps) {
			filteredMissingIDs = append(filteredMissingIDs, issueID)
		}
	}
	issues, err := s.inner.GetIssuesByIDs(ctx, filteredMissingIDs)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: load blocked sources: %w", err)
	}
	for _, issue := range issues {
		if issue == nil || issue.Status == types.StatusClosed || issue.Status == types.StatusPinned {
			continue
		}
		refs := slices.Clone(state.refsByIssue[issue.ID])
		blocked := &types.BlockedIssue{
			Issue:          *issue,
			BlockedByCount: len(refs),
			BlockedBy:      refs,
		}
		result = append(result, blocked)
	}

	return finishBlockedIssues(result, filter)
}

// unpagedBlockedFilter lets the external policy combine local and external
// blockers before applying the caller's page and row cap.
func unpagedBlockedFilter(filter types.WorkFilter) types.WorkFilter {
	filter.Offset = 0
	filter.Limit = 0
	filter.MaxRows = 0
	filter.MaxRowsSource = ""
	return filter
}

func finishBlockedIssues(items []*types.BlockedIssue, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
	if filter.Offset > 0 {
		if filter.Offset >= len(items) {
			items = nil
		} else {
			items = items[filter.Offset:]
		}
	}
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	if err := issueops.EnforceMaxRowsCap(len(items), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, err
	}
	return items, nil
}

func matchesParentFilter(issueID string, parentID *string, allDeps map[string][]*types.Dependency) bool {
	if parentID == nil {
		return true
	}
	if strings.HasPrefix(issueID, *parentID+".") {
		return true
	}
	for _, dep := range allDeps[issueID] {
		if dep != nil && dep.Type == types.DepParentChild && dep.DependsOnID == *parentID {
			return true
		}
	}
	return false
}

// IsBlocked includes explicit unsatisfied external blockers in the close guard.
func (s *Store) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	blocked, blockers, err := s.inner.IsBlocked(ctx, issueID)
	if err != nil {
		return false, nil, err
	}
	external, err := s.externalBlockersOf(ctx, issueID)
	if err != nil {
		return false, nil, err
	}
	for _, ref := range external {
		blockers = appendUnique(blockers, ref)
	}
	return blocked || len(external) > 0, blockers, nil
}

// IsBlockedBatch preserves the external blocker invariant for batch callers.
// The embedded Dolt implementation promotes this method from the wrapped
// store, so it must be declared explicitly here rather than relying on
// IsBlocked alone.
func (s *Store) IsBlockedBatch(ctx context.Context, issueIDs []string) (map[string]bool, error) {
	blocked, err := s.inner.IsBlockedBatch(ctx, issueIDs)
	if err != nil {
		return nil, err
	}
	deps, err := s.inner.GetDependencyRecordsForIssues(ctx, issueIDs)
	if err != nil {
		return nil, err
	}
	refs := make([]reference, 0)
	for _, issueDeps := range deps {
		for _, dep := range issueDeps {
			if dep != nil && dep.Type.IsBlockingEdge() && isExternalReference(dep.DependsOnID) {
				refs = append(refs, parseReference(dep.DependsOnID))
			}
		}
	}
	satisfied, err := s.resolveReferences(ctx, refs)
	if err != nil {
		return nil, err
	}
	for issueID, issueDeps := range deps {
		for _, dep := range issueDeps {
			if dep != nil && dep.Type.IsBlockingEdge() && isExternalReference(dep.DependsOnID) && !satisfied[dep.DependsOnID] {
				blocked[issueID] = true
			}
		}
	}
	return blocked, nil
}

// CloseIssueChecked applies the external guard before the atomic local close.
// The local store cannot see foreign capability state, so promoting its method
// would allow an externally blocked issue to close without --force.
func (s *Store) CloseIssueChecked(ctx context.Context, issueID, actor string, opts storage.CloseIssueOptions) (storage.CloseIssueResult, error) {
	if !opts.Force {
		issue, err := s.inner.GetIssue(ctx, issueID)
		if err != nil {
			return storage.CloseIssueResult{}, err
		}
		if issue != nil && issue.Status != types.StatusClosed {
			blocked, blockers, err := s.IsBlocked(ctx, issueID)
			if err != nil {
				return storage.CloseIssueResult{}, err
			}
			if blocked && len(blockers) > 0 {
				return storage.CloseIssueResult{}, fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, issueID, blockers)
			}
		}
	}
	return s.inner.CloseIssueChecked(ctx, issueID, actor, opts)
}

// GetDependencyTree appends external refs as synthetic leaf nodes because no
// local issue row exists for the normal graph hydrator to return.
func (s *Store) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	tree, err := s.inner.GetDependencyTree(ctx, issueID, maxDepth, showAllPaths, reverse)
	if err != nil || reverse || len(tree) == 0 {
		return tree, err
	}

	issueIDs := make([]string, 0, len(tree))
	for _, node := range tree {
		if node != nil && !isExternalReference(node.ID) {
			issueIDs = append(issueIDs, node.ID)
		}
	}
	deps, err := s.inner.GetDependencyRecordsForIssues(ctx, issueIDs)
	if err != nil {
		return nil, fmt.Errorf("external dependencies: load tree edges: %w", err)
	}
	return s.appendTreeExternalReferences(ctx, tree, deps, maxDepth, showAllPaths)
}

func (p *Policy) appendTreeExternalReferences(ctx context.Context, tree []*types.TreeNode, deps map[string][]*types.Dependency, maxDepth int, showAllPaths bool) ([]*types.TreeNode, error) {
	refs := make([]reference, 0)
	for _, issueDeps := range deps {
		for _, dep := range issueDeps {
			if dep != nil && isExternalReference(dep.DependsOnID) {
				refs = append(refs, parseReference(dep.DependsOnID))
			}
		}
	}
	satisfied, err := p.resolveReferences(ctx, refs)
	if err != nil {
		return nil, err
	}

	effectiveMaxDepth := maxDepth
	if effectiveMaxDepth <= 0 {
		effectiveMaxDepth = 50
	}
	seen := make(map[string]bool, len(tree)+len(refs))
	for _, node := range tree {
		if node != nil {
			seen[node.ID] = true
		}
	}
	for _, parent := range tree {
		if parent == nil || parent.Depth >= effectiveMaxDepth {
			continue
		}
		for _, dep := range deps[parent.ID] {
			if dep == nil || !isExternalReference(dep.DependsOnID) {
				continue
			}
			if !showAllPaths && seen[dep.DependsOnID] {
				continue
			}
			ref := parseReference(dep.DependsOnID)
			status := types.StatusOpen
			title := "○ " + externalTitle(ref)
			if satisfied[ref.raw] {
				status = types.StatusClosed
				title = "✓ " + externalTitle(ref)
			}
			tree = append(tree, &types.TreeNode{
				Issue: types.Issue{
					ID:        dep.DependsOnID,
					Title:     title,
					Status:    status,
					IssueType: types.TypeTask,
				},
				Depth:          parent.Depth + 1,
				ParentID:       parent.ID,
				EdgeFromParent: dep.Type,
			})
			seen[dep.DependsOnID] = true
		}
	}
	return tree, nil
}

func externalTitle(ref reference) string {
	if ref.valid {
		return string(ref.capability)
	}
	return ref.raw
}

// IterReadyWork preserves the decorator semantics for iterator callers.
func (s *Store) IterReadyWork(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.Issue], error) {
	issues, err := s.GetReadyWork(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(issues), nil
}

// IterBlockedIssues preserves the decorator semantics for iterator callers.
func (s *Store) IterBlockedIssues(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.BlockedIssue], error) {
	issues, err := s.GetBlockedIssues(ctx, filter)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(issues), nil
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
