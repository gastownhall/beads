package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/types"
)

type InsertIssueOpts struct {
	UseWispsTable bool
}

type IssueTableOpts struct {
	UseWispsTable bool
}

type IssueSQLRepository interface {
	Insert(ctx context.Context, issue *types.Issue, actor string, opts InsertIssueOpts) error
	InsertBatch(ctx context.Context, issues []*types.Issue, actor string, opts InsertIssueOpts) error
	Update(ctx context.Context, id string, updates map[string]any, actor string, opts IssueTableOpts) error
	Get(ctx context.Context, id string, opts IssueTableOpts) (*types.Issue, error)
	GetByIDs(ctx context.Context, ids []string, opts IssueTableOpts) ([]*types.Issue, error)
	Search(ctx context.Context, filter types.IssueFilter, opts IssueTableOpts) ([]*types.Issue, error)
	Exists(ctx context.Context, id string, opts IssueTableOpts) (bool, error)
	CountForPrefix(ctx context.Context, prefix string, opts IssueTableOpts) (int, error)
	NextCounterID(ctx context.Context, prefix string) (int, error)
	SearchAcrossIssuesAndWisps(ctx context.Context, query string, filter types.IssueFilter) (SearchPage, error)
	SearchAcrossIssuesAndWispsWithCounts(ctx context.Context, query string, filter types.IssueFilter) (SearchCountsPage, error)
	GetReadyWork(ctx context.Context, filter types.WorkFilter) (SearchPage, error)
	GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) (SearchCountsPage, error)
	GetDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error)
}

type SearchPage struct {
	Items   []*types.Issue
	HasMore bool
}

type SearchCountsPage struct {
	Items   []*types.IssueWithCounts
	HasMore bool
}

type CreateIssueParams struct {
	Issue                   *types.Issue
	ExplicitID              string
	ParentID                string
	Labels                  []string
	InheritLabelsFromParent bool
	Dependencies            []DependencySpec
	WaitsFor                *WaitsForSpec
	DiscoveredFromParent    string
	ForcePrefix             bool
}

type DependencySpec struct {
	Type          types.DependencyType
	TargetID      string
	SwapDirection bool
	Metadata      string
}

type WaitsForSpec struct {
	SpawnerID string
	Gate      string
}

type CreateIssueResult struct {
	Issue            *types.Issue
	InheritedLabels  []string
	PostCreateWrites bool
}

type CreateIssuesResult struct {
	Issues []*types.Issue
}

type ListProjection struct {
	Labels           bool
	Dependencies     bool
	DependencyCounts bool
	Parent           bool
	CommentCounts    bool
	Comments         bool
}

type ListResult struct {
	Issues    []*types.IssueWithCounts
	Labels    map[string][]string
	BlockedBy map[string][]string
	Blocks    map[string][]string
	Parent    map[string]string
}

type GraphPlan struct {
	Nodes []GraphNode
	Edges []GraphEdge
}

type GraphNode struct {
	Key               string
	Issue             *types.Issue
	ParentKey         string
	ParentID          string
	Assignee          string
	AssignAfterCreate bool
	MetadataRefs      map[string]string
	Labels            []string
}

type GraphEdge struct {
	FromKey string
	FromID  string
	ToKey   string
	ToID    string
	Type    types.DependencyType
}

type GraphApplyResult struct {
	IDs map[string]string
}

type IssueUseCase interface {
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
	GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error)
	ListIssues(ctx context.Context, filter types.IssueFilter, proj ListProjection) (ListResult, error)
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) (SearchPage, error)
	SearchIssuesWithCounts(ctx context.Context, query string, filter types.IssueFilter) (SearchCountsPage, error)
	GetReadyWork(ctx context.Context, filter types.WorkFilter) (SearchPage, error)
	GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) (SearchCountsPage, error)
	GetDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error)

	CreateIssue(ctx context.Context, params CreateIssueParams, actor string) (CreateIssueResult, error)
	CreateIssues(ctx context.Context, params []CreateIssueParams, actor string) (CreateIssuesResult, error)
	UpdateIssue(ctx context.Context, id string, updates map[string]any, actor string) error
	ApplyIssueGraph(ctx context.Context, plan GraphPlan, actor string) (GraphApplyResult, error)

	GetWisp(ctx context.Context, id string) (*types.Issue, error)
	GetWispsByIDs(ctx context.Context, ids []string) ([]*types.Issue, error)
	ListWisps(ctx context.Context, filter types.IssueFilter, proj ListProjection) (ListResult, error)
	CreateWisp(ctx context.Context, params CreateIssueParams, actor string) (CreateIssueResult, error)
	CreateWisps(ctx context.Context, params []CreateIssueParams, actor string) (CreateIssuesResult, error)
	UpdateWisp(ctx context.Context, id string, updates map[string]any, actor string) error
	ApplyWispGraph(ctx context.Context, plan GraphPlan, actor string) (GraphApplyResult, error)
}

func NewIssueUseCase(
	issueRepo IssueSQLRepository,
	depRepo DependencySQLRepository,
	labelRepo LabelSQLRepository,
	counterRepo ChildCounterSQLRepository,
	commentRepo CommentSQLRepository,
	cfgRepo ConfigSQLRepository,
) IssueUseCase {
	return &issueUseCaseImpl{
		issueRepo:   issueRepo,
		depRepo:     depRepo,
		labelRepo:   labelRepo,
		counterRepo: counterRepo,
		commentRepo: commentRepo,
		cfgRepo:     cfgRepo,
	}
}

type issueUseCaseImpl struct {
	issueRepo   IssueSQLRepository
	depRepo     DependencySQLRepository
	labelRepo   LabelSQLRepository
	counterRepo ChildCounterSQLRepository
	commentRepo CommentSQLRepository
	cfgRepo     ConfigSQLRepository
}

var _ IssueUseCase = (*issueUseCaseImpl)(nil)

func (u *issueUseCaseImpl) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return u.get(ctx, id, false)
}

func (u *issueUseCaseImpl) GetWisp(ctx context.Context, id string) (*types.Issue, error) {
	return u.get(ctx, id, true)
}

func (u *issueUseCaseImpl) get(ctx context.Context, id string, useWisp bool) (*types.Issue, error) {
	if id == "" {
		return nil, fmt.Errorf("get: id must not be empty")
	}
	issue, err := u.issueRepo.Get(ctx, id, IssueTableOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", id, err)
	}
	return issue, nil
}

func (u *issueUseCaseImpl) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	return u.getByIDs(ctx, ids, false)
}

func (u *issueUseCaseImpl) GetWispsByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	return u.getByIDs(ctx, ids, true)
}

func (u *issueUseCaseImpl) getByIDs(ctx context.Context, ids []string, useWisp bool) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out, err := u.issueRepo.GetByIDs(ctx, ids, IssueTableOpts{UseWispsTable: useWisp})
	if err != nil {
		return nil, fmt.Errorf("getByIDs: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) UpdateIssue(ctx context.Context, id string, updates map[string]any, actor string) error {
	return u.update(ctx, id, updates, actor, false)
}

func (u *issueUseCaseImpl) UpdateWisp(ctx context.Context, id string, updates map[string]any, actor string) error {
	return u.update(ctx, id, updates, actor, true)
}

func (u *issueUseCaseImpl) update(ctx context.Context, id string, updates map[string]any, actor string, useWisp bool) error {
	if id == "" {
		return fmt.Errorf("update: id must not be empty")
	}
	if len(updates) == 0 {
		return nil
	}
	return u.issueRepo.Update(ctx, id, updates, actor, IssueTableOpts{UseWispsTable: useWisp})
}

func (u *issueUseCaseImpl) ListIssues(ctx context.Context, filter types.IssueFilter, proj ListProjection) (ListResult, error) {
	return u.list(ctx, filter, proj, false)
}

func (u *issueUseCaseImpl) ListWisps(ctx context.Context, filter types.IssueFilter, proj ListProjection) (ListResult, error) {
	return u.list(ctx, filter, proj, true)
}

func (u *issueUseCaseImpl) list(ctx context.Context, filter types.IssueFilter, proj ListProjection, useWisp bool) (ListResult, error) {
	issues, err := u.issueRepo.Search(ctx, filter, IssueTableOpts{UseWispsTable: useWisp})
	if err != nil {
		return ListResult{}, fmt.Errorf("list: search: %w", err)
	}

	out := ListResult{
		Issues:    make([]*types.IssueWithCounts, 0, len(issues)),
		Labels:    map[string][]string{},
		BlockedBy: map[string][]string{},
		Blocks:    map[string][]string{},
		Parent:    map[string]string{},
	}
	if len(issues) == 0 {
		return out, nil
	}

	ids := make([]string, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
	}

	if proj.Labels {
		labels, err := u.labelRepo.ListByIssueIDs(ctx, ids, LabelOpts{UseWispsTable: useWisp})
		if err != nil {
			return ListResult{}, fmt.Errorf("list: labels: %w", err)
		}
		out.Labels = labels
		for _, issue := range issues {
			if l, ok := labels[issue.ID]; ok {
				issue.Labels = l
			}
		}
	}

	counts := make(map[string]*types.DependencyCounts, len(ids))
	for _, id := range ids {
		counts[id] = &types.DependencyCounts{}
	}

	needDeps := proj.Dependencies || proj.DependencyCounts || proj.Parent
	if needDeps {
		depBulk, err := u.depRepo.ListByIssueIDs(ctx, ids, DepListOpts{Direction: DepDirectionBoth, UseWispsTable: useWisp})
		if err != nil {
			return ListResult{}, fmt.Errorf("list: deps: %w", err)
		}
		for _, issue := range issues {
			outgoing := depBulk.Outgoing[issue.ID]
			if proj.Dependencies {
				issue.Dependencies = outgoing
			}
			for _, d := range outgoing {
				if d.Type == types.DepBlocks {
					counts[issue.ID].DependencyCount++
				}
				if d.Type == types.DepParentChild {
					out.Parent[issue.ID] = d.DependsOnID
				}
			}
			for _, d := range depBulk.Incoming[issue.ID] {
				if d.Type == types.DepBlocks {
					counts[issue.ID].DependentCount++
					out.BlockedBy[issue.ID] = append(out.BlockedBy[issue.ID], d.IssueID)
				}
			}
		}
		for from, deps := range depBulk.Outgoing {
			for _, d := range deps {
				if d.Type == types.DepBlocks {
					out.Blocks[from] = append(out.Blocks[from], d.DependsOnID)
				}
			}
		}
	} else if proj.DependencyCounts {
		c, err := u.depRepo.CountsByIssueIDs(ctx, ids, DepCountsOpts{UseWispsTable: useWisp})
		if err != nil {
			return ListResult{}, fmt.Errorf("list: dep counts: %w", err)
		}
		for id, v := range c {
			counts[id] = v
		}
	}

	var commentCounts map[string]int
	if proj.CommentCounts {
		commentCounts, err = u.commentRepo.CountsByIssueIDs(ctx, ids, CommentOpts{UseWispsTable: useWisp})
		if err != nil {
			return ListResult{}, fmt.Errorf("list: comment counts: %w", err)
		}
	}

	if proj.Comments {
		comments, err := u.commentRepo.ListByIssueIDs(ctx, ids, CommentOpts{UseWispsTable: useWisp})
		if err != nil {
			return ListResult{}, fmt.Errorf("list: comments: %w", err)
		}
		for _, issue := range issues {
			issue.Comments = comments[issue.ID]
		}
	}

	for _, issue := range issues {
		c := counts[issue.ID]
		if c == nil {
			c = &types.DependencyCounts{}
		}
		var parentPtr *string
		if p, ok := out.Parent[issue.ID]; ok {
			pp := p
			parentPtr = &pp
		}
		out.Issues = append(out.Issues, &types.IssueWithCounts{
			Issue:           issue,
			DependencyCount: c.DependencyCount,
			DependentCount:  c.DependentCount,
			CommentCount:    commentCounts[issue.ID],
			Parent:          parentPtr,
		})
	}

	return out, nil
}

func (u *issueUseCaseImpl) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) (SearchPage, error) {
	out, err := u.issueRepo.SearchAcrossIssuesAndWisps(ctx, query, filter)
	if err != nil {
		return SearchPage{}, fmt.Errorf("SearchIssues: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) SearchIssuesWithCounts(ctx context.Context, query string, filter types.IssueFilter) (SearchCountsPage, error) {
	out, err := u.issueRepo.SearchAcrossIssuesAndWispsWithCounts(ctx, query, filter)
	if err != nil {
		return SearchCountsPage{}, fmt.Errorf("SearchIssuesWithCounts: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) GetReadyWork(ctx context.Context, filter types.WorkFilter) (SearchPage, error) {
	out, err := u.issueRepo.GetReadyWork(ctx, filter)
	if err != nil {
		return SearchPage{}, fmt.Errorf("GetReadyWork: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) (SearchCountsPage, error) {
	out, err := u.issueRepo.GetReadyWorkWithCounts(ctx, filter)
	if err != nil {
		return SearchCountsPage{}, fmt.Errorf("GetReadyWorkWithCounts: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) GetDescendants(ctx context.Context, rootID string, filter types.IssueFilter) ([]*types.Issue, error) {
	if rootID == "" {
		return nil, fmt.Errorf("GetDescendants: rootID must not be empty")
	}
	out, err := u.issueRepo.GetDescendants(ctx, rootID, filter)
	if err != nil {
		return nil, fmt.Errorf("GetDescendants: %w", err)
	}
	return out, nil
}

func (u *issueUseCaseImpl) CreateIssue(ctx context.Context, params CreateIssueParams, actor string) (CreateIssueResult, error) {
	return u.create(ctx, params, actor, false)
}

func (u *issueUseCaseImpl) CreateWisp(ctx context.Context, params CreateIssueParams, actor string) (CreateIssueResult, error) {
	return u.create(ctx, params, actor, true)
}

func (u *issueUseCaseImpl) create(ctx context.Context, params CreateIssueParams, actor string, useWisp bool) (CreateIssueResult, error) {
	if params.Issue == nil {
		return CreateIssueResult{}, fmt.Errorf("create: Issue must not be nil")
	}
	issue := params.Issue

	if issue.Status == "" {
		issue.Status = types.StatusOpen
	}

	// Set CreatedAt before the mint path: GenerateHashID hashes timestamp,
	// so it must be stable across the candidate loop and the eventual
	// db.Insert (which would otherwise normalize a zero value to a later
	// time and break candidate reproducibility on retry).
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = time.Now().UTC()
	}

	switch {
	case params.ExplicitID != "":
		issue.ID = params.ExplicitID
	case params.ParentID != "":
		childID, err := u.counterRepo.NextChildID(ctx, params.ParentID, ChildCounterOpts{UseWispsTable: useWisp})
		if err != nil {
			return CreateIssueResult{}, fmt.Errorf("create: next child ID for %s: %w", params.ParentID, err)
		}
		issue.ID = childID
	case issue.ID == "":
		minted, err := u.mintTopLevelID(ctx, issue, actor, useWisp)
		if err != nil {
			return CreateIssueResult{}, fmt.Errorf("create: mint top-level ID: %w", err)
		}
		issue.ID = minted
	}

	if params.DiscoveredFromParent != "" {
		if parent, err := u.GetIssue(ctx, params.DiscoveredFromParent); err == nil && parent.SourceRepo != "" {
			issue.SourceRepo = parent.SourceRepo
		}
	}

	insertOpts := InsertIssueOpts{UseWispsTable: useWisp}
	if err := u.issueRepo.Insert(ctx, issue, actor, insertOpts); err != nil {
		return CreateIssueResult{}, fmt.Errorf("create: insert: %w", err)
	}

	result := CreateIssueResult{Issue: issue}

	if params.ParentID != "" {
		pcDep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: params.ParentID,
			Type:        types.DepParentChild,
		}
		if err := u.depRepo.Insert(ctx, pcDep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
			return result, fmt.Errorf("create: add parent-child dep: %w", err)
		}
		result.PostCreateWrites = true
	}

	if params.InheritLabelsFromParent && params.ParentID != "" {
		parentLabels, err := u.labelRepo.List(ctx, params.ParentID, LabelOpts{UseWispsTable: useWisp})
		if err == nil {
			existing := make(map[string]bool, len(params.Labels))
			for _, l := range params.Labels {
				existing[l] = true
			}
			for _, l := range parentLabels {
				if !existing[l] {
					result.InheritedLabels = append(result.InheritedLabels, l)
				}
			}
		}
	}

	for _, label := range params.Labels {
		if err := u.labelRepo.Insert(ctx, issue.ID, label, actor, LabelOpts{UseWispsTable: useWisp}); err != nil {
			return result, fmt.Errorf("create: add label %s: %w", label, err)
		}
		result.PostCreateWrites = true
	}
	for _, label := range result.InheritedLabels {
		if err := u.labelRepo.Insert(ctx, issue.ID, label, actor, LabelOpts{UseWispsTable: useWisp}); err != nil {
			return result, fmt.Errorf("create: add inherited label %s: %w", label, err)
		}
		result.PostCreateWrites = true
	}

	for _, spec := range params.Dependencies {
		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: spec.TargetID,
			Type:        spec.Type,
			Metadata:    spec.Metadata,
		}
		if spec.SwapDirection {
			dep.IssueID, dep.DependsOnID = dep.DependsOnID, dep.IssueID
		}
		if err := u.depRepo.Insert(ctx, dep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
			return result, fmt.Errorf("create: add dep %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
		}
		result.PostCreateWrites = true
	}

	if params.WaitsFor != nil {
		gate := params.WaitsFor.Gate
		if gate == "" {
			gate = types.WaitsForAllChildren
		}
		metaJSON, err := json.Marshal(types.WaitsForMeta{Gate: gate})
		if err != nil {
			return result, fmt.Errorf("create: marshal waits-for meta: %w", err)
		}
		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: params.WaitsFor.SpawnerID,
			Type:        types.DepWaitsFor,
			Metadata:    string(metaJSON),
		}
		if err := u.depRepo.Insert(ctx, dep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
			return result, fmt.Errorf("create: add waits-for: %w", err)
		}
		result.PostCreateWrites = true
	}

	return result, nil
}

func (u *issueUseCaseImpl) CreateIssues(ctx context.Context, params []CreateIssueParams, actor string) (CreateIssuesResult, error) {
	return u.createMany(ctx, params, actor, false)
}

func (u *issueUseCaseImpl) CreateWisps(ctx context.Context, params []CreateIssueParams, actor string) (CreateIssuesResult, error) {
	return u.createMany(ctx, params, actor, true)
}

func (u *issueUseCaseImpl) createMany(ctx context.Context, params []CreateIssueParams, actor string, useWisp bool) (CreateIssuesResult, error) {
	result := CreateIssuesResult{}
	for i := range params {
		r, err := u.create(ctx, params[i], actor, useWisp)
		if err != nil {
			return result, fmt.Errorf("createMany[%d]: %w", i, err)
		}
		result.Issues = append(result.Issues, r.Issue)
	}
	return result, nil
}

func (u *issueUseCaseImpl) ApplyIssueGraph(ctx context.Context, plan GraphPlan, actor string) (GraphApplyResult, error) {
	return u.applyGraph(ctx, plan, actor, false)
}

func (u *issueUseCaseImpl) ApplyWispGraph(ctx context.Context, plan GraphPlan, actor string) (GraphApplyResult, error) {
	return u.applyGraph(ctx, plan, actor, true)
}

func (u *issueUseCaseImpl) applyGraph(ctx context.Context, plan GraphPlan, actor string, useWisp bool) (GraphApplyResult, error) {
	keyToID := make(map[string]string, len(plan.Nodes))
	pendingAssignees := make(map[int]string, len(plan.Nodes))

	// Pass 1 — create every node as a top-level issue. We deliberately do
	// not pass ParentID to u.create: graph nodes with parent_key/parent_id
	// receive top-level hash (or counter) IDs and have their parent linkage
	// added as a separate parent-child dep below. This matches embedded
	// executeGraphApply (cmd/bd/graph_apply.go) and lets children precede
	// parents in plan order — keyToID is only consulted after every node
	// has minted its ID.
	for i, node := range plan.Nodes {
		if node.Issue == nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %d (key=%q) has nil Issue", i, node.Key)
		}

		if node.AssignAfterCreate {
			pendingAssignees[i] = node.Assignee
			node.Issue.Assignee = ""
		} else if node.Assignee != "" {
			node.Issue.Assignee = node.Assignee
		}

		params := CreateIssueParams{
			Issue:  node.Issue,
			Labels: node.Labels,
		}
		r, err := u.create(ctx, params, actor, useWisp)
		if err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: %w", node.Key, err)
		}
		keyToID[node.Key] = r.Issue.ID
	}

	// Pass 2 — resolve MetadataRefs now that every node has a minted ID.
	// Merges the resolved IDs into the issue's existing metadata JSON and
	// writes the result back via Update. Kept inside applyGraph so the CLI
	// cannot bypass this step; the proxied caller used to do it post-call.
	for _, node := range plan.Nodes {
		if len(node.MetadataRefs) == 0 {
			continue
		}
		merged := make(map[string]string, len(node.MetadataRefs))
		if len(node.Issue.Metadata) > 0 {
			if err := json.Unmarshal(node.Issue.Metadata, &merged); err != nil {
				return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: re-parsing metadata: %w", node.Key, err)
			}
		}
		for metaKey, refKey := range node.MetadataRefs {
			resolvedID, ok := keyToID[refKey]
			if !ok {
				return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: metadata_ref %q references unknown key %q", node.Key, metaKey, refKey)
			}
			merged[metaKey] = resolvedID
		}
		metaJSON, err := json.Marshal(merged)
		if err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: marshaling merged metadata: %w", node.Key, err)
		}
		updates := map[string]any{"metadata": json.RawMessage(metaJSON)}
		if err := u.issueRepo.Update(ctx, keyToID[node.Key], updates, actor, IssueTableOpts{UseWispsTable: useWisp}); err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: updating metadata refs: %w", node.Key, err)
		}
	}

	// Build the (childID, parentID) pair set and validate that any planned
	// parent-child link does not close a cycle through planned edges or
	// already-existing dependencies in the store. This must run before any
	// dep inserts to catch the violation before we've written anything.
	parentDepPairs := graphParentDepPairs(plan.Nodes, keyToID)
	if err := u.validatePlannedBlockingPaths(ctx, plan, keyToID, parentDepPairs, useWisp); err != nil {
		return GraphApplyResult{}, err
	}

	// Pass 3 — insert edge deps. Deduplicate against the parent-child pairs:
	//   - Same pair, parent-child type → skip (pass 4 will insert it).
	//   - Same pair, different type   → error (conflicting edge over a parent-child link).
	//   - Reverse pair, blocking type → error (creates a parent → child blocking cycle).
	//
	// Cycle-check skip optimization (matches embedded executeGraphApply):
	// when every cycle-relevant edge in the plan is fully local (both
	// endpoints by key, neither by ID), the CLI-side local cycle validator
	// has already proven the planned subgraph is acyclic and
	// validatePlannedBlockingPaths has covered parent-child paths against
	// live deps. In that case the per-edge HasCycle SQL probe is skipped.
	// Edges that reference external IDs always pay for HasCycle.
	planCanSkipCycleCheck := applyGraphPlanCanSkipSQLCycleChecks(plan)
	for i, edge := range plan.Edges {
		fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
		if fromID == "" {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d references undefined from_key %q", i, edge.FromKey)
		}
		toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
		if toID == "" {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d references undefined to_key %q", i, edge.ToKey)
		}
		depType := edge.Type
		if depType == "" {
			depType = types.DepBlocks
		}

		if parentDepPairs[depPairKey(fromID, toID)] {
			if depType == types.DepParentChild {
				continue
			}
			return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d %s->%s duplicates a parent-child relationship with dependency type %q", i, fromID, toID, depType)
		}
		if parentDepPairs[depPairKey(toID, fromID)] && cycleRelevantDepType(depType) {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d %s->%s creates a blocking reverse of a parent-child relationship", i, fromID, toID)
		}

		if cycleRelevantDepType(depType) && !(planCanSkipCycleCheck && applyGraphEdgeCanSkipSQLCycleCheck(edge, depType)) {
			cycle, err := u.depRepo.HasCycle(ctx, fromID, toID)
			if err != nil {
				return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d cycle check: %w", i, err)
			}
			if cycle {
				return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d (%s -> %s): would create a cycle", i, fromID, toID)
			}
		}

		dep := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        depType,
		}
		if err := u.depRepo.Insert(ctx, dep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: edge %d (%s -> %s): %w", i, fromID, toID, err)
		}
	}

	// Pass 4 — insert parent-child deps now that all IDs are known.
	for _, node := range plan.Nodes {
		parentID := node.ParentID
		if node.ParentKey != "" {
			parentID = keyToID[node.ParentKey]
		}
		if parentID == "" {
			continue
		}
		childID := keyToID[node.Key]
		dep := &types.Dependency{
			IssueID:     childID,
			DependsOnID: parentID,
			Type:        types.DepParentChild,
		}
		if err := u.depRepo.Insert(ctx, dep, actor, DepInsertOpts{UseWispsTable: useWisp}); err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: parent-child dep %s->%s: %w", node.Key, childID, parentID, err)
		}
	}

	// Pass 5 — apply deferred assignees.
	for i, assignee := range pendingAssignees {
		if assignee == "" {
			continue
		}
		id := keyToID[plan.Nodes[i].Key]
		if err := u.issueRepo.Update(ctx, id, map[string]any{"assignee": assignee}, actor, IssueTableOpts{UseWispsTable: useWisp}); err != nil {
			return GraphApplyResult{}, fmt.Errorf("applyGraph: node %q: defer assignee: %w", plan.Nodes[i].Key, err)
		}
	}

	return GraphApplyResult{IDs: keyToID}, nil
}

// graphParentDepPairs encodes the (childID, parentID) parent-child pairs
// implied by the plan's node ParentKey/ParentID fields. Used by applyGraph
// to dedup explicit edges against implicit parent-child relationships and
// to seed the in-memory adjacency for live cycle detection.
func graphParentDepPairs(nodes []GraphNode, keyToID map[string]string) map[string]bool {
	pairs := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		parentID := n.ParentID
		if n.ParentKey != "" {
			parentID = keyToID[n.ParentKey]
		}
		childID := keyToID[n.Key]
		if childID == "" || parentID == "" {
			continue
		}
		pairs[depPairKey(childID, parentID)] = true
	}
	return pairs
}

// depPairKey encodes an ordered (issueID, dependsOnID) pair using a NUL
// separator so the two halves can be recovered unambiguously without ID
// characters colliding with the delimiter.
func depPairKey(issueID, dependsOnID string) string {
	return issueID + "\x00" + dependsOnID
}

// depPairIDs decodes a key produced by depPairKey, returning (from, to, ok).
func depPairIDs(pair string) (string, string, bool) {
	for i := 0; i < len(pair); i++ {
		if pair[i] == 0 {
			return pair[:i], pair[i+1:], true
		}
	}
	return "", "", false
}

// cycleRelevantDepType returns true for dep types whose presence in the
// reverse direction of a parent-child link would form a cycle.
func cycleRelevantDepType(t types.DependencyType) bool {
	return t == types.DepBlocks || t == types.DepConditionalBlocks
}

// applyGraphPlanCanSkipSQLCycleChecks returns true when every cycle-relevant
// edge in the plan is purely local (both endpoints by key, neither by ID).
// In that case the CLI-side local cycle validator has already proven the
// planned subgraph is acyclic, and no per-edge SQL HasCycle probe is needed
// in applyGraph's edge pass. Non-cycle-relevant edges (e.g. "related") do
// not gate the skip. Mirrors embedded graphApplyPlanCanSkipSQLCycleChecks.
func applyGraphPlanCanSkipSQLCycleChecks(plan GraphPlan) bool {
	for _, edge := range plan.Edges {
		depType := edge.Type
		if depType == "" {
			depType = types.DepBlocks
		}
		if !cycleRelevantDepType(depType) {
			continue
		}
		if !applyGraphEdgeCanSkipSQLCycleCheck(edge, depType) {
			return false
		}
	}
	return true
}

// applyGraphEdgeCanSkipSQLCycleCheck returns true when the edge is fully
// local (both endpoints by key, neither by ID) and the dep type is
// cycle-relevant. An edge that references an external ID always pays for
// HasCycle because the local validator never saw the external graph.
func applyGraphEdgeCanSkipSQLCycleCheck(edge GraphEdge, depType types.DependencyType) bool {
	if edge.FromKey == "" || edge.ToKey == "" || edge.FromID != "" || edge.ToID != "" {
		return false
	}
	return cycleRelevantDepType(depType)
}

// resolveEdgeRef returns the ID for an edge endpoint: the keyToID lookup
// when key is set, else the explicit id. Returns "" when neither resolves,
// which the caller should treat as a structural error.
func resolveEdgeRef(key, id string, keyToID map[string]string) string {
	if key != "" {
		return keyToID[key]
	}
	return id
}

// validatePlannedBlockingPaths rejects plans that would close a cycle
// once the parent-child deps are inserted. The adjacency it walks combines
// the planned parent-child pairs (child → parent), the cycle-relevant
// planned edges (excluding reverse-of-parent-child which is rejected by
// applyGraph's dedup pass), and live AffectsReadyWork dependencies pulled
// lazily from the store via depRepo. Mirrors embedded
// validateGraphApplyPlannedParentBlockingPaths.
func (u *issueUseCaseImpl) validatePlannedBlockingPaths(
	ctx context.Context,
	plan GraphPlan,
	keyToID map[string]string,
	parentDepPairs map[string]bool,
	useWisp bool,
) error {
	adj := make(map[string][]string)
	for pair := range parentDepPairs {
		fromID, toID, ok := depPairIDs(pair)
		if ok {
			adj[fromID] = append(adj[fromID], toID)
		}
	}
	for _, edge := range plan.Edges {
		depType := edge.Type
		if depType == "" {
			depType = types.DepBlocks
		}
		if !depType.AffectsReadyWork() {
			continue
		}
		fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
		toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
		if fromID == "" || toID == "" {
			continue
		}
		// Skip the reverse-of-parent-child case for cycle-relevant types —
		// applyGraph's edge dedup already errors on those with a clearer
		// message, so we don't want them showing up here as ambiguous
		// "blocking path" errors.
		if cycleRelevantDepType(depType) && parentDepPairs[depPairKey(toID, fromID)] {
			continue
		}
		adj[fromID] = append(adj[fromID], toID)
	}

	depCache := make(map[string][]*types.Dependency)
	for _, node := range plan.Nodes {
		parentID := node.ParentID
		if node.ParentKey != "" {
			parentID = keyToID[node.ParentKey]
		}
		childID := keyToID[node.Key]
		if childID == "" || parentID == "" {
			continue
		}
		hasPath, err := u.graphHasPath(ctx, adj, depCache, parentID, childID, useWisp)
		if err != nil {
			return err
		}
		if hasPath {
			return fmt.Errorf("applyGraph: node %q: planned blocking dependencies create a path from parent %q to child %q", node.Key, parentID, childID)
		}
	}
	return nil
}

// graphHasPath returns true if fromID can reach toID by following the
// in-memory adjacency (planned parent-child + planned blocking edges) and
// existing AffectsReadyWork deps loaded lazily from the store. Per-node
// dep fetches are cached so each visited node hits the DB at most once.
func (u *issueUseCaseImpl) graphHasPath(
	ctx context.Context,
	adj map[string][]string,
	depCache map[string][]*types.Dependency,
	fromID, toID string,
	useWisp bool,
) (bool, error) {
	seen := make(map[string]bool)
	var visit func(string) (bool, error)
	visit = func(id string) (bool, error) {
		if id == toID {
			return true, nil
		}
		if seen[id] {
			return false, nil
		}
		seen[id] = true
		for _, next := range adj[id] {
			found, err := visit(next)
			if err != nil || found {
				return found, err
			}
		}
		deps, ok := depCache[id]
		if !ok {
			res, err := u.depRepo.ListByIssueIDs(ctx, []string{id}, DepListOpts{
				Direction:     DepDirectionOut,
				UseWispsTable: useWisp,
			})
			if err != nil {
				return false, fmt.Errorf("applyGraph: read existing deps for %s: %w", id, err)
			}
			deps = res.Outgoing[id]
			depCache[id] = deps
		}
		for _, dep := range deps {
			if !dep.Type.AffectsReadyWork() {
				continue
			}
			found, err := visit(dep.DependsOnID)
			if err != nil || found {
				return found, err
			}
		}
		return false, nil
	}
	return visit(fromID)
}

// resolveTopLevelPrefix picks the prefix for a freshly-minted top-level ID,
// mirroring the embedded path's precedence (issueops/create.go:88-96 and
// dolt/wisps.go wispPrefix). Reads issue_prefix from config once and trims
// the trailing hyphen so a config value of "bd-" yields "bd-<hash>" rather
// than "bd--<hash>".
func (u *issueUseCaseImpl) resolveTopLevelPrefix(ctx context.Context, issue *types.Issue, useWisp bool) (string, error) {
	if issue.PrefixOverride != "" {
		return issue.PrefixOverride, nil
	}

	configPrefix, err := u.cfgRepo.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return "", fmt.Errorf("read issue_prefix: %w", err)
	}
	configPrefix = strings.TrimSuffix(configPrefix, "-")
	if configPrefix == "" {
		return "", fmt.Errorf("issue_prefix config is missing")
	}

	switch {
	case issue.IDPrefix != "":
		return configPrefix + "-" + issue.IDPrefix, nil
	case useWisp:
		return configPrefix + "-wisp", nil
	}
	return configPrefix, nil
}

// mintTopLevelID generates a fresh top-level ID for an issue that has no
// ExplicitID and no ParentID. Honors counter mode for non-wisps (config key
// issue_id_mode=counter); otherwise uses adaptive hash-mode IDs that mirror
// issueops.GenerateIssueIDInTable. Reads issue.CreatedAt (caller must have
// stabilized it before this call so retries hash the same value).
func (u *issueUseCaseImpl) mintTopLevelID(ctx context.Context, issue *types.Issue, actor string, useWisp bool) (string, error) {
	prefix, err := u.resolveTopLevelPrefix(ctx, issue, useWisp)
	if err != nil {
		return "", err
	}

	// Counter mode applies only to the issues table — wisps always hash-mint
	// because there is no wisp_counter table and ephemeral churn would make
	// a monotonic counter meaningless.
	if !useWisp {
		mode, err := u.cfgRepo.GetConfig(ctx, "issue_id_mode")
		if err != nil {
			return "", fmt.Errorf("read issue_id_mode: %w", err)
		}
		if mode == "counter" {
			n, err := u.issueRepo.NextCounterID(ctx, prefix)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%s-%d", prefix, n), nil
		}
	}

	cfg, err := u.cfgRepo.GetAdaptiveIDConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("read adaptive id config: %w", err)
	}
	tableOpts := IssueTableOpts{UseWispsTable: useWisp}

	count, err := u.issueRepo.CountForPrefix(ctx, prefix, tableOpts)
	if err != nil {
		return "", err
	}
	baseLength := ComputeAdaptiveLength(count, cfg)
	if baseLength > cfg.MaxLength {
		baseLength = cfg.MaxLength
	}

	for length := baseLength; length <= cfg.MaxLength; length++ {
		for nonce := 0; nonce < 10; nonce++ {
			candidate := idgen.GenerateHashID(prefix, issue.Title, issue.Description, actor, issue.CreatedAt, length, nonce)
			exists, err := u.issueRepo.Exists(ctx, candidate, tableOpts)
			if err != nil {
				return "", err
			}
			if !exists {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("failed to generate unique ID for prefix %q after lengths %d..%d with 10 nonces each", prefix, baseLength, cfg.MaxLength)
}
