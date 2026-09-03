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
	inner         storage.DoltStorage
	locateProject ProjectLocator
	openProject   StoreOpener
	warnProject   func(ProjectName)
	warnMu        sync.Mutex
	warned        map[ProjectName]struct{}
}

// New constructs an external-capability-aware storage decorator.
func New(inner storage.DoltStorage, locateProject ProjectLocator, openProject StoreOpener) *Store {
	return &Store{
		DoltStorage:   inner,
		inner:         inner,
		locateProject: locateProject,
		openProject:   openProject,
		warnProject:   defaultProjectWarning,
		warned:        make(map[ProjectName]struct{}),
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

func (l *lifecycle) Update(ctx context.Context, request publicops.UpdateRequest) (publicops.UpdateResult, error) {
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

func (s *Store) guardExternalClose(ctx context.Context, id string, force bool) error {
	if force {
		return nil
	}
	state, err := s.loadBlockingState(ctx)
	if err != nil {
		return err
	}
	if blockers := state.refsByIssue[id]; len(blockers) > 0 {
		return fmt.Errorf("%w: %s is blocked by %v", storage.ErrCloseBlocked, id, blockers)
	}
	return nil
}

func (s *Store) warnUnresolvedProject(project ProjectName) {
	s.warnMu.Lock()
	defer s.warnMu.Unlock()
	if _, warned := s.warned[project]; warned {
		return
	}
	s.warned[project] = struct{}{}
	if s.warnProject != nil {
		s.warnProject(project)
	}
}

type blockingState struct {
	refsByIssue map[string][]string
}

func (s *Store) loadBlockingState(ctx context.Context) (blockingState, error) {
	queryStore, ok := storage.UnwrapStore(s.inner).(storage.ExternalDependencyQueryStore)
	var allDeps map[string][]*types.Dependency
	var err error
	if ok {
		allDeps, err = queryStore.GetExternalBlockingDependencyRecords(ctx)
	} else {
		// Compatibility fallback for third-party stores that predate the narrow
		// optional capability. First-party stores implement the indexed query.
		allDeps, err = s.inner.GetAllDependencyRecords(ctx)
	}
	if err != nil {
		return blockingState{}, fmt.Errorf("external dependencies: list blocking records: %w", err)
	}

	return s.blockingStateFromRecords(ctx, allDeps)
}

func (s *Store) blockingStateFromRecords(ctx context.Context, allDeps map[string][]*types.Dependency) (blockingState, error) {
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

	satisfied, err := s.resolveReferences(ctx, refs)
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

func withExternalExclusions(filter types.WorkFilter, refsByIssue map[string][]string) types.WorkFilter {
	filter.ExcludeIDs = slices.Clone(filter.ExcludeIDs)
	newIDs := make([]string, 0, len(refsByIssue))
	for issueID := range refsByIssue {
		if !slices.Contains(filter.ExcludeIDs, issueID) {
			newIDs = append(newIDs, issueID)
		}
	}
	sort.Strings(newIDs)
	filter.ExcludeIDs = append(filter.ExcludeIDs, newIDs...)
	return filter
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
	deps, err := s.inner.GetDependencyRecordsForIssues(ctx, []string{issueID})
	if err != nil {
		return false, nil, err
	}
	refs := make([]reference, 0)
	for _, dep := range deps[issueID] {
		if dep != nil && dep.Type.IsBlockingEdge() && isExternalReference(dep.DependsOnID) {
			refs = append(refs, parseReference(dep.DependsOnID))
		}
	}
	satisfied, err := s.resolveReferences(ctx, refs)
	if err != nil {
		return false, nil, err
	}
	for _, ref := range refs {
		if !satisfied[ref.raw] {
			blockers = appendUnique(blockers, ref.raw)
		}
	}
	return blocked || len(blockers) > 0, blockers, nil
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

func (s *Store) appendTreeExternalReferences(ctx context.Context, tree []*types.TreeNode, deps map[string][]*types.Dependency, maxDepth int, showAllPaths bool) ([]*types.TreeNode, error) {
	refs := make([]reference, 0)
	for _, issueDeps := range deps {
		for _, dep := range issueDeps {
			if dep != nil && isExternalReference(dep.DependsOnID) {
				refs = append(refs, parseReference(dep.DependsOnID))
			}
		}
	}
	satisfied, err := s.resolveReferences(ctx, refs)
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
