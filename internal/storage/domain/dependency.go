package domain

import (
	"context"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
)

// DependencyTypeConflictError is returned when an edge already exists between
// the same pair with a DIFFERENT type. Its message is byte-identical to the
// embedded issueops path (issueops/dependencies.go) so `bd dep add` surfaces
// the same user-facing retype error on the domain/db seam as on the embedded
// store. It is a typed error so the use-case can pass it through
// unwrapped instead of burying it under an "add dep: insert:" prefix.
type DependencyTypeConflictError struct {
	IssueID       string
	DependsOnID   string
	ExistingType  string
	RequestedType string
}

func (e *DependencyTypeConflictError) Error() string {
	return fmt.Sprintf("dependency %s -> %s already exists with type %q (requested %q); remove it first with 'bd dep remove' then re-add",
		e.IssueID, e.DependsOnID, e.ExistingType, e.RequestedType)
}

type DepDirection int

const (
	DepDirectionBoth DepDirection = iota
	DepDirectionOut
	DepDirectionIn
)

type DepInsertOpts struct {
	UseWispsTable bool
}

type DepListOpts struct {
	Types         []types.DependencyType
	Direction     DepDirection
	UseWispsTable bool
}

type DepCountsOpts struct {
	UseWispsTable bool
}

type DepBulkResult struct {
	Outgoing map[string][]*types.Dependency
	Incoming map[string][]*types.Dependency
}

type DepListFilter struct {
	Types     []types.DependencyType
	Direction DepDirection
}

type BlockingInfo struct {
	BlockedBy map[string][]string
	Blocks    map[string][]string
	Parent    map[string]string
}

type DepDeleteResult struct {
	Found       bool
	Type        types.DependencyType
	DependsOnID string
}

type DepTreeOpts struct {
	MaxDepth     int
	ShowAllPaths bool
	Direction    DepDirection
}

type BulkAddDepsOpts struct {
	SkipPerEdgeCycleCheck bool
}

type BulkAddDepsResult struct {
	Added []*types.Dependency
}

type DependencySQLRepository interface {
	Insert(ctx context.Context, dep *types.Dependency, actor string, opts DepInsertOpts) error
	Delete(ctx context.Context, issueID, dependsOnID, actor string, opts DepInsertOpts) (DepDeleteResult, error)
	HasCycle(ctx context.Context, issueID, dependsOnID string) (bool, error)
	ListByIssueIDs(ctx context.Context, issueIDs []string, opts DepListOpts) (DepBulkResult, error)
	ListWithIssueMetadata(ctx context.Context, sourceID string, opts DepListOpts) ([]*types.IssueWithDependencyMetadata, error)
	IterWithIssueMetadata(ctx context.Context, sourceID string, opts DepListOpts) (storage.Iter[types.IssueWithDependencyMetadata], error)
	CountByID(ctx context.Context, sourceID string, opts DepListOpts) (int64, error)
	CountsByIssueIDs(ctx context.Context, issueIDs []string, opts DepCountsOpts) (map[string]*types.DependencyCounts, error)

	GetBlockingInfo(ctx context.Context, issueIDs []string, opts DepListOpts) (BlockingInfo, error)
	GetBlockingInfoAcrossIssuesAndWisps(ctx context.Context, issueIDs []string) (BlockingInfo, error)
	IsBlocked(ctx context.Context, issueID string, opts DepListOpts) (bool, []string, error)

	DeleteAllForIDs(ctx context.Context, ids []string, opts DepInsertOpts) (int, error)
	CountAllForIDs(ctx context.Context, ids []string, opts DepCountsOpts) (int, error)
	DetectCycles(ctx context.Context) ([][]*types.Issue, error)

	GetTree(ctx context.Context, rootID string, opts DepTreeOpts) ([]*types.TreeNode, error)
	CycleThroughEdges(ctx context.Context, edges [][2]string) (string, error)
	GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error)
	GetWispDependencyRecordsForIDs(ctx context.Context, wispIDs []string) (map[string][]*types.Dependency, error)
}

type DependencyUseCase interface {
	AddDependency(ctx context.Context, dep *types.Dependency, actor string) error
	RemoveDependency(ctx context.Context, issueID, dependsOnID, actor string) error
	Reparent(ctx context.Context, childID, newParentID, actor string) error
	ListByIssueIDs(ctx context.Context, issueIDs []string, filter DepListFilter) (DepBulkResult, error)
	ListWithIssueMetadata(ctx context.Context, issueID string, filter DepListFilter) ([]*types.IssueWithDependencyMetadata, error)
	IterWithIssueMetadata(ctx context.Context, issueID string, filter DepListFilter) (storage.Iter[types.IssueWithDependencyMetadata], error)
	CountByIssueID(ctx context.Context, issueID string, filter DepListFilter) (int64, error)
	CountsByIssueIDs(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error)
	GetBlockingInfo(ctx context.Context, issueIDs []string) (BlockingInfo, error)
	IsBlocked(ctx context.Context, issueID string) (bool, []string, error)
	GetForIssueIDs(ctx context.Context, ids []string) (map[string][]*types.Dependency, error)
	DetectCycles(ctx context.Context) ([][]*types.Issue, error)

	GetDependencyTree(ctx context.Context, rootID string, opts DepTreeOpts) ([]*types.TreeNode, error)
	AddDependencies(ctx context.Context, deps []*types.Dependency, actor string, opts BulkAddDepsOpts) (BulkAddDepsResult, error)
	GetIssueDependencyRecords(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error)

	AddWispDependencies(ctx context.Context, deps []*types.Dependency, actor string, opts BulkAddDepsOpts) (BulkAddDepsResult, error)
	GetWispDependencyRecords(ctx context.Context, wispIDs []string) (map[string][]*types.Dependency, error)

	AddWispDependency(ctx context.Context, dep *types.Dependency, actor string) error
	RemoveWispDependency(ctx context.Context, wispID, dependsOnID, actor string) error
	ReparentWisp(ctx context.Context, childWispID, newParentID, actor string) error
	ListByWispIDs(ctx context.Context, wispIDs []string, filter DepListFilter) (DepBulkResult, error)
	ListWispWithIssueMetadata(ctx context.Context, wispID string, filter DepListFilter) ([]*types.IssueWithDependencyMetadata, error)
	IterWispWithIssueMetadata(ctx context.Context, wispID string, filter DepListFilter) (storage.Iter[types.IssueWithDependencyMetadata], error)
	CountByWispID(ctx context.Context, wispID string, filter DepListFilter) (int64, error)
	CountsByWispIDs(ctx context.Context, wispIDs []string) (map[string]*types.DependencyCounts, error)
	IsWispBlocked(ctx context.Context, wispID string) (bool, []string, error)
}

func NewDependencyUseCase(depRepo DependencySQLRepository) DependencyUseCase {
	return &dependencyUseCaseImpl{depRepo: depRepo}
}

type dependencyUseCaseImpl struct {
	depRepo DependencySQLRepository
}

var _ DependencyUseCase = (*dependencyUseCaseImpl)(nil)

func (u *dependencyUseCaseImpl) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return u.add(ctx, dep, actor, false)
}

func (u *dependencyUseCaseImpl) AddWispDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return u.add(ctx, dep, actor, true)
}

func (u *dependencyUseCaseImpl) add(ctx context.Context, dep *types.Dependency, actor string, useWisp bool) error {
	if dep == nil {
		return fmt.Errorf("add dep: dep must not be nil")
	}
	if dep.IssueID == "" || dep.DependsOnID == "" {
		return fmt.Errorf("add dep: IssueID and DependsOnID must be non-empty")
	}

	// Self-dependency guard mirrors issueops.CheckDependencyCycleInTx: it is
	// checked BEFORE the cycle probe and for ALL dep types, and emits the
	// dedicated self-dep message. A blocking self-edge otherwise trips HasCycle
	// and would report the wrong (cycle) error (#4547 F-1).
	if dep.IssueID == dep.DependsOnID {
		return fmt.Errorf("cannot add self-dependency: %s cannot depend on itself", dep.IssueID)
	}

	if isBlockingDep(dep.Type) {
		cycle, err := u.depRepo.HasCycle(ctx, dep.IssueID, dep.DependsOnID)
		if err != nil {
			return fmt.Errorf("add dep: cycle check: %w", err)
		}
		if cycle {
			// Match the embedded store's user-facing wording verbatim (no ids
			// prefix) so gc code that string-matches this error behaves the same
			// on both plumbings (#4547 F-1).
			return fmt.Errorf("adding dependency would create a cycle")
		}
	}

	if err := u.depRepo.Insert(ctx, dep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
		// The retype conflict is a user-facing error whose message already
		// matches embedded verbatim; pass it through unwrapped so the CLI does
		// not prepend "add dep: insert:" (#4547 F-1).
		var conflict *DependencyTypeConflictError
		if errors.As(err, &conflict) {
			return err
		}
		return fmt.Errorf("add dep: insert: %w", err)
	}
	return nil
}

func (u *dependencyUseCaseImpl) RemoveDependency(ctx context.Context, issueID, dependsOnID, actor string) error {
	return u.removeDep(ctx, issueID, dependsOnID, actor, false)
}

func (u *dependencyUseCaseImpl) RemoveWispDependency(ctx context.Context, wispID, dependsOnID, actor string) error {
	return u.removeDep(ctx, wispID, dependsOnID, actor, true)
}

func (u *dependencyUseCaseImpl) removeDep(ctx context.Context, sourceID, dependsOnID, actor string, useWisp bool) error {
	if sourceID == "" || dependsOnID == "" {
		return fmt.Errorf("remove dep: sourceID and dependsOnID must not be empty")
	}
	if _, err := u.depRepo.Delete(ctx, sourceID, dependsOnID, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
		return fmt.Errorf("remove dep %s -> %s: %w", sourceID, dependsOnID, err)
	}
	return nil
}

func (u *dependencyUseCaseImpl) Reparent(ctx context.Context, childID, newParentID, actor string) error {
	return u.reparent(ctx, childID, newParentID, actor, false)
}

func (u *dependencyUseCaseImpl) ReparentWisp(ctx context.Context, childWispID, newParentID, actor string) error {
	return u.reparent(ctx, childWispID, newParentID, actor, true)
}

func (u *dependencyUseCaseImpl) reparent(ctx context.Context, childID, newParentID, actor string, useWisp bool) error {
	if childID == "" {
		return fmt.Errorf("reparent: childID must not be empty")
	}
	if childID == newParentID {
		return fmt.Errorf("reparent: %s cannot be its own parent", childID)
	}

	opts := DepInsertOpts{UseWispsTable: useWisp}
	res, err := u.depRepo.ListByIssueIDs(ctx, []string{childID}, DepListOpts{
		Types:         []types.DependencyType{types.DepParentChild},
		Direction:     DepDirectionOut,
		UseWispsTable: useWisp,
	})
	if err != nil {
		return fmt.Errorf("reparent: list current parent: %w", err)
	}

	var oldParentID string
	for _, dep := range res.Outgoing[childID] {
		if dep.Type == types.DepParentChild {
			oldParentID = dep.DependsOnID
			break
		}
	}

	if oldParentID == newParentID {
		return nil
	}

	if oldParentID != "" {
		if _, err := u.depRepo.Delete(ctx, childID, oldParentID, actor, opts); err != nil {
			return fmt.Errorf("reparent: remove old parent %s: %w", oldParentID, err)
		}
	}

	if newParentID != "" {
		dep := &types.Dependency{
			IssueID:     childID,
			DependsOnID: newParentID,
			Type:        types.DepParentChild,
		}
		if err := u.depRepo.Insert(ctx, dep, actor, opts); err != nil {
			return fmt.Errorf("reparent: add new parent %s: %w", newParentID, err)
		}
	}
	return nil
}

func (u *dependencyUseCaseImpl) ListByIssueIDs(ctx context.Context, issueIDs []string, filter DepListFilter) (DepBulkResult, error) {
	return u.list(ctx, issueIDs, filter, false)
}

func (u *dependencyUseCaseImpl) ListWithIssueMetadata(ctx context.Context, issueID string, filter DepListFilter) ([]*types.IssueWithDependencyMetadata, error) {
	return u.listWithMetadata(ctx, issueID, filter, false)
}

func (u *dependencyUseCaseImpl) IterWithIssueMetadata(ctx context.Context, issueID string, filter DepListFilter) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return u.iterWithMetadata(ctx, issueID, filter, false)
}

func (u *dependencyUseCaseImpl) CountByIssueID(ctx context.Context, issueID string, filter DepListFilter) (int64, error) {
	return u.countByID(ctx, issueID, filter, false)
}

func (u *dependencyUseCaseImpl) GetForIssueIDs(ctx context.Context, ids []string) (map[string][]*types.Dependency, error) {
	if len(ids) == 0 {
		return map[string][]*types.Dependency{}, nil
	}
	issueRes, err := u.depRepo.ListByIssueIDs(ctx, ids, DepListOpts{Direction: DepDirectionOut})
	if err != nil {
		return nil, fmt.Errorf("GetForIssueIDs: %w", err)
	}
	out := issueRes.Outgoing
	if out == nil {
		out = make(map[string][]*types.Dependency)
	}
	wispRes, err := u.depRepo.ListByIssueIDs(ctx, ids, DepListOpts{Direction: DepDirectionOut, UseWispsTable: true})
	if err != nil && !dberrors.IsTableNotExist(err) {
		return nil, fmt.Errorf("GetForIssueIDs (wisps): %w", err)
	}
	for id, deps := range wispRes.Outgoing {
		out[id] = append(out[id], deps...)
	}
	return out, nil
}

func (u *dependencyUseCaseImpl) ListByWispIDs(ctx context.Context, wispIDs []string, filter DepListFilter) (DepBulkResult, error) {
	return u.list(ctx, wispIDs, filter, true)
}

func (u *dependencyUseCaseImpl) ListWispWithIssueMetadata(ctx context.Context, wispID string, filter DepListFilter) ([]*types.IssueWithDependencyMetadata, error) {
	return u.listWithMetadata(ctx, wispID, filter, true)
}

func (u *dependencyUseCaseImpl) IterWispWithIssueMetadata(ctx context.Context, wispID string, filter DepListFilter) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	return u.iterWithMetadata(ctx, wispID, filter, true)
}

func (u *dependencyUseCaseImpl) CountByWispID(ctx context.Context, wispID string, filter DepListFilter) (int64, error) {
	return u.countByID(ctx, wispID, filter, true)
}

func (u *dependencyUseCaseImpl) listWithMetadata(ctx context.Context, sourceID string, filter DepListFilter, useWisp bool) ([]*types.IssueWithDependencyMetadata, error) {
	if sourceID == "" {
		return nil, fmt.Errorf("list dep metadata: sourceID must not be empty")
	}
	out, err := u.depRepo.ListWithIssueMetadata(ctx, sourceID, DepListOpts{
		Types:         filter.Types,
		Direction:     filter.Direction,
		UseWispsTable: useWisp,
	})
	if err != nil {
		return nil, fmt.Errorf("list dep metadata: %w", err)
	}
	return out, nil
}

func (u *dependencyUseCaseImpl) iterWithMetadata(ctx context.Context, sourceID string, filter DepListFilter, useWisp bool) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	if sourceID == "" {
		return nil, fmt.Errorf("iter dep metadata: sourceID must not be empty")
	}
	it, err := u.depRepo.IterWithIssueMetadata(ctx, sourceID, DepListOpts{
		Types:         filter.Types,
		Direction:     filter.Direction,
		UseWispsTable: useWisp,
	})
	if err != nil {
		return nil, fmt.Errorf("iter dep metadata: %w", err)
	}
	return it, nil
}

func (u *dependencyUseCaseImpl) countByID(ctx context.Context, sourceID string, filter DepListFilter, useWisp bool) (int64, error) {
	if sourceID == "" {
		return 0, fmt.Errorf("count by id: sourceID must not be empty")
	}
	n, err := u.depRepo.CountByID(ctx, sourceID, DepListOpts{
		Types:         filter.Types,
		Direction:     filter.Direction,
		UseWispsTable: useWisp,
	})
	if err != nil {
		return 0, fmt.Errorf("count by id: %w", err)
	}
	return n, nil
}

func (u *dependencyUseCaseImpl) list(ctx context.Context, ids []string, filter DepListFilter, useWisp bool) (DepBulkResult, error) {
	if len(ids) == 0 {
		return DepBulkResult{
			Outgoing: map[string][]*types.Dependency{},
			Incoming: map[string][]*types.Dependency{},
		}, nil
	}
	out, err := u.depRepo.ListByIssueIDs(ctx, ids, DepListOpts{
		Types:         filter.Types,
		Direction:     filter.Direction,
		UseWispsTable: useWisp,
	})
	if err != nil {
		return DepBulkResult{}, fmt.Errorf("list deps: %w", err)
	}
	return out, nil
}

func (u *dependencyUseCaseImpl) CountsByIssueIDs(ctx context.Context, issueIDs []string) (map[string]*types.DependencyCounts, error) {
	return u.counts(ctx, issueIDs, false)
}

func (u *dependencyUseCaseImpl) CountsByWispIDs(ctx context.Context, wispIDs []string) (map[string]*types.DependencyCounts, error) {
	return u.counts(ctx, wispIDs, true)
}

func (u *dependencyUseCaseImpl) counts(ctx context.Context, ids []string, useWisp bool) (map[string]*types.DependencyCounts, error) {
	if len(ids) == 0 {
		return map[string]*types.DependencyCounts{}, nil
	}
	out, err := u.depRepo.CountsByIssueIDs(ctx, ids, DepCountsOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("dep counts: %w", err)
	}
	return out, nil
}

func (u *dependencyUseCaseImpl) GetBlockingInfo(ctx context.Context, issueIDs []string) (BlockingInfo, error) {
	if len(issueIDs) == 0 {
		return BlockingInfo{
			BlockedBy: map[string][]string{},
			Blocks:    map[string][]string{},
			Parent:    map[string]string{},
		}, nil
	}
	out, err := u.depRepo.GetBlockingInfoAcrossIssuesAndWisps(ctx, issueIDs)
	if err != nil {
		return BlockingInfo{}, fmt.Errorf("GetBlockingInfo: %w", err)
	}
	return out, nil
}

func isBlockingDep(t types.DependencyType) bool {
	return t == types.DepBlocks || t == types.DepConditionalBlocks
}

func (u *dependencyUseCaseImpl) IsBlocked(ctx context.Context, issueID string) (bool, []string, error) {
	return u.isBlocked(ctx, issueID, false)
}

func (u *dependencyUseCaseImpl) IsWispBlocked(ctx context.Context, wispID string) (bool, []string, error) {
	return u.isBlocked(ctx, wispID, true)
}

func (u *dependencyUseCaseImpl) isBlocked(ctx context.Context, id string, useWisp bool) (bool, []string, error) {
	if id == "" {
		return false, nil, fmt.Errorf("IsBlocked: id must not be empty")
	}
	blocked, blockers, err := u.depRepo.IsBlocked(ctx, id, DepListOpts{UseWispsTable: useWisp})
	if err != nil {
		return false, nil, fmt.Errorf("IsBlocked %s: %w", id, err)
	}
	return blocked, blockers, nil
}

func (u *dependencyUseCaseImpl) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	out, err := u.depRepo.DetectCycles(ctx)
	if err != nil {
		return nil, fmt.Errorf("DetectCycles: %w", err)
	}
	return out, nil
}

func (u *dependencyUseCaseImpl) GetDependencyTree(ctx context.Context, rootID string, opts DepTreeOpts) ([]*types.TreeNode, error) {
	if rootID == "" {
		return nil, fmt.Errorf("GetDependencyTree: rootID must not be empty")
	}
	out, err := u.depRepo.GetTree(ctx, rootID, opts)
	if err != nil {
		return nil, fmt.Errorf("GetDependencyTree: %w", err)
	}
	return out, nil
}

func (u *dependencyUseCaseImpl) AddDependencies(ctx context.Context, deps []*types.Dependency, actor string, opts BulkAddDepsOpts) (BulkAddDepsResult, error) {
	return u.addBulk(ctx, deps, actor, opts, false)
}

func (u *dependencyUseCaseImpl) AddWispDependencies(ctx context.Context, deps []*types.Dependency, actor string, opts BulkAddDepsOpts) (BulkAddDepsResult, error) {
	return u.addBulk(ctx, deps, actor, opts, true)
}

func (u *dependencyUseCaseImpl) addBulk(ctx context.Context, deps []*types.Dependency, actor string, opts BulkAddDepsOpts, useWisp bool) (BulkAddDepsResult, error) {
	if len(deps) == 0 {
		return BulkAddDepsResult{Added: []*types.Dependency{}}, nil
	}
	insertOpts := DepInsertOpts{UseWispsTable: useWisp}
	for i, dep := range deps {
		if dep == nil {
			return BulkAddDepsResult{}, fmt.Errorf("add deps[%d]: dep must not be nil", i)
		}
		if dep.IssueID == "" || dep.DependsOnID == "" {
			return BulkAddDepsResult{}, fmt.Errorf("add deps[%d]: IssueID and DependsOnID must be non-empty", i)
		}
		if !opts.SkipPerEdgeCycleCheck && isBlockingDep(dep.Type) {
			cycle, err := u.depRepo.HasCycle(ctx, dep.IssueID, dep.DependsOnID)
			if err != nil {
				return BulkAddDepsResult{}, fmt.Errorf("add deps[%d]: cycle check: %w", i, err)
			}
			if cycle {
				return BulkAddDepsResult{}, fmt.Errorf("add deps[%d]: adding %s -> %s would create a cycle", i, dep.IssueID, dep.DependsOnID)
			}
		}
		if err := u.depRepo.Insert(ctx, dep, actor, insertOpts); err != nil {
			return BulkAddDepsResult{}, fmt.Errorf("add deps[%d]: insert: %w", i, err)
		}
	}
	if opts.SkipPerEdgeCycleCheck {
		var pairs [][2]string
		for _, dep := range deps {
			if !isBlockingDep(dep.Type) {
				continue
			}
			pairs = append(pairs, [2]string{dep.IssueID, dep.DependsOnID})
		}
		if len(pairs) > 0 {
			cyclePath, err := u.depRepo.CycleThroughEdges(ctx, pairs)
			if err != nil {
				return BulkAddDepsResult{}, fmt.Errorf("add deps: final cycle check: %w", err)
			}
			if cyclePath != "" {
				return BulkAddDepsResult{}, fmt.Errorf("add deps: dependency cycle would be created: %s", cyclePath)
			}
		}
	}
	return BulkAddDepsResult{Added: deps}, nil
}

func (u *dependencyUseCaseImpl) GetIssueDependencyRecords(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	if len(issueIDs) == 0 {
		return map[string][]*types.Dependency{}, nil
	}
	out, err := u.depRepo.GetDependencyRecordsForIssues(ctx, issueIDs)
	if err != nil {
		return nil, fmt.Errorf("GetIssueDependencyRecords: %w", err)
	}
	return out, nil
}

func (u *dependencyUseCaseImpl) GetWispDependencyRecords(ctx context.Context, wispIDs []string) (map[string][]*types.Dependency, error) {
	if len(wispIDs) == 0 {
		return map[string][]*types.Dependency{}, nil
	}
	out, err := u.depRepo.GetWispDependencyRecordsForIDs(ctx, wispIDs)
	if err != nil {
		return nil, fmt.Errorf("GetWispDependencyRecords: %w", err)
	}
	return out, nil
}
