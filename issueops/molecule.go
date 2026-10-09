package issueops

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/types"
)

// MoleculeTemplateLabel is the label that marks an epic as a template-driven
// molecule root. `bd mol distill` writes it; a poured molecule's root is
// TypeMolecule instead and carries no label.
const MoleculeTemplateLabel = "template"

// MoleculeReader is the read surface every molecule operation in this file is
// composed over: three roles every backend serves.
//
//   - BatchGetter answers the molecule root and the parent walk's candidate
//     roots, labeled, in either plane.
//   - Relations answers a node's inbound parent-child steps as FULL rows, in
//     either plane.
//   - EdgeReader answers the parent walk (a step's outgoing parent-child edge)
//     and the blocking edges between a molecule's steps.
//
// THE MOLECULE RULES HAVE ONE IMPLEMENTATION, and it is the functions below.
// A caller that reads committed state binds them to the roles
// (NewMoleculeReader). A close that must decide inside its own
// transaction binds them to the same three role BODIES running against that
// transaction (internal/storage/issueops.TxMoleculeReader, and the unit-of-work
// backend's MoleculeReaderInUOW), so a molecule's membership, completion and
// readiness mean one thing on every backend and every route.
//
// Membership is the parent-child EDGE. A child whose id merely looks
// hierarchical (<root>.<n>) but carries no edge is not a step: no role can
// express an id-pattern scan, and a rule only some routes could apply would
// make the same molecule answer differently per route.
type MoleculeReader struct {
	BatchGetter
	Relations
	EdgeReader
}

// MoleculeReaderOver binds one value that answers all three role reads — an
// in-transaction binding, a test double — as a MoleculeReader.
func MoleculeReaderOver(r interface {
	BatchGetter
	Relations
	EdgeReader
}) MoleculeReader {
	return MoleculeReader{BatchGetter: r, Relations: r, EdgeReader: r}
}

func (r MoleculeReader) usable() bool {
	return r.BatchGetter != nil && r.Relations != nil && r.EdgeReader != nil
}

// moleculeRoleSource is the accessor set a store or a unit-of-work provider
// offers for the three roles a MoleculeReader composes.
type moleculeRoleSource interface {
	BatchGetter() (BatchGetter, error)
	IssueRelations() (Relations, error)
	EdgeReader() (EdgeReader, error)
}

// NewMoleculeReader binds a MoleculeReader to src's own role accessors, so
// every decorator src wears (hooks, telemetry, policy) stays in the path. src
// is a storage.DoltStorage or a unit-of-work provider.
func NewMoleculeReader(src moleculeRoleSource) (MoleculeReader, error) {
	if src == nil {
		return MoleculeReader{}, fmt.Errorf("%w: molecule reader needs a role source", ErrValidation)
	}
	getter, err := src.BatchGetter()
	if err != nil {
		return MoleculeReader{}, err
	}
	relations, err := src.IssueRelations()
	if err != nil {
		return MoleculeReader{}, err
	}
	edges, err := src.EdgeReader()
	if err != nil {
		return MoleculeReader{}, err
	}
	return MoleculeReader{BatchGetter: getter, Relations: relations, EdgeReader: edges}, nil
}

// IsMoleculeRoot reports whether issue is the root of a molecule: an epic, a
// TypeMolecule root (`bd mol pour`), or anything carrying the template label.
func IsMoleculeRoot(issue *Issue) bool {
	if issue == nil {
		return false
	}
	if issue.IssueType == types.TypeEpic || issue.IssueType == types.TypeMolecule {
		return true
	}
	return hasLabel(issue, MoleculeTemplateLabel)
}

// AutoClosesWhenComplete reports whether a molecule root closes itself when
// its last step closes. Molecule-typed and ephemeral roots do, and so does a
// template-driven epic; an ordinary epic stays open so it becomes explicit,
// close-eligible work instead of closing as a side effect.
func AutoClosesWhenComplete(root *Issue) bool {
	if root == nil {
		return false
	}
	if root.IssueType == types.TypeMolecule || root.Ephemeral {
		return true
	}
	return root.IssueType == types.TypeEpic && hasLabel(root, MoleculeTemplateLabel)
}

func hasLabel(issue *Issue, label string) bool {
	for _, l := range issue.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// maxMoleculeDepth bounds the parent walk, so a parent-child cycle ends.
const maxMoleculeDepth = 50

// MoleculeRoots walks parent-child edges up from every id and answers, for
// each id that belongs to a molecule, the id of its molecule root. An id whose
// topmost ancestor is not a molecule root (IsMoleculeRoot) is omitted, and so
// is an id that names nothing. A root that is itself a molecule root answers
// itself.
//
// The walk is level by level, one edge read per level for every chain at
// once, so its cost is the depth of the deepest chain, not the number of ids.
// A read the backend fails or refuses is RETURNED: answering "not in a
// molecule" for a read that never happened is how a molecule root gets
// stranded open.
func MoleculeRoots(ctx context.Context, r MoleculeReader, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	if !r.usable() {
		return nil, fmt.Errorf("%w: molecule reads need a reader", ErrValidation)
	}
	topOf := make(map[string]string, len(ids))
	current := make(map[string]string, len(ids))
	for _, id := range ids {
		current[id] = id
	}
	for depth := 0; depth < maxMoleculeDepth && len(current) > 0; depth++ {
		level := distinctValues(current)
		parentOf, err := parentChildParents(ctx, r, level)
		if err != nil {
			return nil, err
		}
		next := make(map[string]string, len(current))
		for start, at := range current {
			if parent, ok := parentOf[at]; ok {
				next[start] = parent
			} else {
				topOf[start] = at
			}
		}
		current = next
	}
	for start, at := range current {
		topOf[start] = at
	}

	tops, err := getIssues(ctx, r, distinctValues(topOf))
	if err != nil {
		return nil, fmt.Errorf("loading molecule roots: %w", err)
	}
	for start, top := range topOf {
		if IsMoleculeRoot(tops[top]) {
			out[start] = top
		}
	}
	return out, nil
}

// MoleculeRoot is MoleculeRoots for one id: the id's molecule root, or "" when
// it belongs to no molecule.
func MoleculeRoot(ctx context.Context, r MoleculeReader, id string) (string, error) {
	roots, err := MoleculeRoots(ctx, r, []string{id})
	if err != nil {
		return "", err
	}
	return roots[id], nil
}

// parentChildParents answers each id's first outgoing parent-child target.
func parentChildParents(ctx context.Context, r MoleculeReader, ids []string) (map[string]string, error) {
	res, err := r.ReadEdges(ctx, EdgeReadRequest{IDs: ids, Types: []types.DependencyType{types.DepParentChild}})
	if err != nil {
		return nil, fmt.Errorf("walking parent-child edges: %w", err)
	}
	parentOf := make(map[string]string, len(res.Anchors))
	for _, anchor := range res.Anchors {
		for _, edge := range anchor.Edges {
			if edge != nil && edge.Type == types.DepParentChild {
				parentOf[anchor.ID] = edge.DependsOnID
				break
			}
		}
	}
	return parentOf, nil
}

// getIssues reads ids through the BatchGetter in batches the role accepts and
// answers them by id. Missing ids are absent.
func getIssues(ctx context.Context, r MoleculeReader, ids []string) (map[string]*Issue, error) {
	out := make(map[string]*Issue, len(ids))
	for start := 0; start < len(ids); start += MaxGetManyIDs {
		end := min(start+MaxGetManyIDs, len(ids))
		res, err := r.GetMany(ctx, GetManyRequest{IDs: ids[start:end]})
		if err != nil {
			return nil, err
		}
		for _, issue := range res.Issues {
			if issue != nil {
				out[issue.ID] = issue
			}
		}
	}
	return out, nil
}

func distinctValues(m map[string]string) []string {
	seen := make(map[string]bool, len(m))
	out := make([]string, 0, len(m))
	for _, v := range m {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// MoleculeGraph is a molecule root, every step under it and the edges between
// them.
type MoleculeGraph struct {
	// Root is the molecule root, labeled.
	Root *Issue
	// Issues holds the root first, then every descendant in depth-first
	// pre-order of the parent-child edges.
	Issues []*Issue
	// Dependencies holds every stored edge whose two ends are both in Issues.
	Dependencies []*Dependency
	// IssueMap indexes Issues by id.
	IssueMap map[string]*Issue
}

// LoadMolecule reads the molecule rooted at rootID: the root, every
// parent-child descendant as a full row, and the edges between them. A root
// that names nothing is ErrNotFound. A parent-child cycle terminates.
func LoadMolecule(ctx context.Context, r MoleculeReader, rootID string) (*MoleculeGraph, error) {
	if !r.usable() {
		return nil, fmt.Errorf("%w: molecule reads need a reader", ErrValidation)
	}
	roots, err := getIssues(ctx, r, []string{rootID})
	if err != nil {
		return nil, fmt.Errorf("loading molecule %s: %w", rootID, err)
	}
	root := roots[rootID]
	if root == nil {
		return nil, fmt.Errorf("%w: molecule %s", ErrNotFound, rootID)
	}
	g := &MoleculeGraph{Root: root, Issues: []*Issue{root}, IssueMap: map[string]*Issue{root.ID: root}}
	if err := loadMoleculeChildren(ctx, r, g, root.ID); err != nil {
		return nil, err
	}

	ids := make([]string, len(g.Issues))
	for i, issue := range g.Issues {
		ids[i] = issue.ID
	}
	edges, err := r.ReadEdges(ctx, EdgeReadRequest{IDs: ids})
	if err != nil {
		return nil, fmt.Errorf("reading molecule %s edges: %w", rootID, err)
	}
	for _, anchor := range edges.Anchors {
		for _, edge := range anchor.Edges {
			if edge == nil {
				continue
			}
			if _, ok := g.IssueMap[edge.DependsOnID]; ok {
				g.Dependencies = append(g.Dependencies, edge)
			}
		}
	}
	return g, nil
}

func loadMoleculeChildren(ctx context.Context, r MoleculeReader, g *MoleculeGraph, parentID string) error {
	children, err := r.Related(ctx, RelatedRequest{
		ID:        parentID,
		Direction: RelationIn,
		Types:     []types.DependencyType{types.DepParentChild},
	})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading the steps of %s: %w", parentID, err)
	}
	for _, child := range children {
		if child == nil || child.DependencyType != types.DepParentChild {
			continue
		}
		if _, seen := g.IssueMap[child.ID]; seen {
			continue
		}
		issue := child.Issue
		g.Issues = append(g.Issues, &issue)
		g.IssueMap[issue.ID] = &issue
		if err := loadMoleculeChildren(ctx, r, g, issue.ID); err != nil {
			return err
		}
	}
	return nil
}

// MoleculeStepInfo is one node's readiness inside its molecule.
type MoleculeStepInfo struct {
	StepID        string   `json:"step_id"`
	Status        string   `json:"status"`
	IsReady       bool     `json:"is_ready"`       // Can start now (no open blocker)
	ParallelGroup string   `json:"parallel_group"` // Steps sharing a group can run concurrently
	BlockedBy     []string `json:"blocked_by"`     // Open steps blocking this one
	Blocks        []string `json:"blocks"`         // Steps this one blocks
	CanParallel   []string `json:"can_parallel"`   // Steps that can run in parallel with this one
}

// MoleculeAnalysis is the readiness and parallelism of every node of a
// molecule, root included.
type MoleculeAnalysis struct {
	MoleculeID     string                       `json:"molecule_id"`
	TotalSteps     int                          `json:"total_steps"`
	ReadySteps     int                          `json:"ready_steps"`
	ParallelGroups map[string][]string          `json:"parallel_groups"` // group id -> step ids
	Steps          map[string]*MoleculeStepInfo `json:"steps"`
}

// AnalyzeMolecule computes readiness from the edges inside the molecule
// rather than from the workspace ready query, which excludes ephemeral rows (a
// wisp molecule's steps are ephemeral by definition). A node is ready when it
// is open or in progress and no open node blocks it through a blocks,
// conditional-blocks or waits-for edge. Nodes at the same blocking depth that
// do not block each other share a parallel group.
func AnalyzeMolecule(g *MoleculeGraph) *MoleculeAnalysis {
	analysis := &MoleculeAnalysis{
		MoleculeID:     g.Root.ID,
		TotalSteps:     len(g.Issues),
		ParallelGroups: make(map[string][]string),
		Steps:          make(map[string]*MoleculeStepInfo),
	}

	blockedBy := make(map[string]map[string]bool)
	blocks := make(map[string]map[string]bool)
	parentChildren := make(map[string][]string)
	for _, issue := range g.Issues {
		blockedBy[issue.ID] = make(map[string]bool)
		blocks[issue.ID] = make(map[string]bool)
	}
	for _, dep := range g.Dependencies {
		if dep.Type == types.DepParentChild {
			parentChildren[dep.DependsOnID] = append(parentChildren[dep.DependsOnID], dep.IssueID)
		}
	}
	for _, dep := range g.Dependencies {
		switch dep.Type {
		case types.DepBlocks, types.DepConditionalBlocks:
			if _, ok := blockedBy[dep.IssueID]; ok {
				blockedBy[dep.IssueID][dep.DependsOnID] = true
			}
			if _, ok := blocks[dep.DependsOnID]; ok {
				blocks[dep.DependsOnID][dep.IssueID] = true
			}
		case types.DepWaitsFor:
			children := parentChildren[dep.DependsOnID]
			if len(children) == 0 {
				continue
			}
			if types.ParseWaitsForGateMetadata(dep.Metadata) == types.WaitsForAnyChildren && anyClosed(g, children) {
				continue
			}
			// For all-children (and unresolved any-children) every open child
			// blocks the gate.
			for _, childID := range children {
				child := g.IssueMap[childID]
				if child == nil || child.Status == types.StatusClosed {
					continue
				}
				if _, ok := blockedBy[dep.IssueID]; ok {
					blockedBy[dep.IssueID][childID] = true
				}
				if _, ok := blocks[childID]; ok {
					blocks[childID][dep.IssueID] = true
				}
			}
		}
	}

	for _, issue := range g.Issues {
		info := &MoleculeStepInfo{StepID: issue.ID, Status: string(issue.Status), BlockedBy: []string{}, Blocks: []string{}}
		for blockerID := range blockedBy[issue.ID] {
			if blocker := g.IssueMap[blockerID]; blocker != nil && blocker.Status != types.StatusClosed {
				info.BlockedBy = append(info.BlockedBy, blockerID)
			}
		}
		for blockedID := range blocks[issue.ID] {
			info.Blocks = append(info.Blocks, blockedID)
		}
		info.IsReady = (issue.Status == types.StatusOpen || issue.Status == types.StatusInProgress) && len(info.BlockedBy) == 0
		if info.IsReady {
			analysis.ReadySteps++
		}
		sort.Strings(info.BlockedBy)
		sort.Strings(info.Blocks)
		analysis.Steps[issue.ID] = info
	}

	depths := blockingDepths(g, blockedBy)
	depthGroups := make(map[int][]string)
	for _, issue := range g.Issues {
		depthGroups[depths[issue.ID]] = append(depthGroups[depths[issue.ID]], issue.ID)
	}
	groupCounter := 0
	for depth := 0; depth <= len(g.Issues); depth++ {
		groupCounter = groupParallelSteps(analysis, depthGroups[depth], blocks, blockedBy, groupCounter)
	}
	return analysis
}

func anyClosed(g *MoleculeGraph, ids []string) bool {
	for _, id := range ids {
		if child := g.IssueMap[id]; child != nil && child.Status == types.StatusClosed {
			return true
		}
	}
	return false
}

// groupParallelSteps unions the steps at one blocking depth that do not block
// each other and names every group of two or more. It answers the updated
// group counter.
func groupParallelSteps(analysis *MoleculeAnalysis, steps []string, blocks, blockedBy map[string]map[string]bool, counter int) int {
	if len(steps) == 0 {
		return counter
	}
	parent := make(map[string]string, len(steps))
	for _, id := range steps {
		parent[id] = id
	}
	find := func(x string) string {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for i, a := range steps {
		for _, b := range steps[i+1:] {
			if !blocks[a][b] && !blocks[b][a] && !blockedBy[a][b] && !blockedBy[b][a] {
				if pa, pb := find(a), find(b); pa != pb {
					parent[pa] = pb
				}
			}
		}
	}
	groups := make(map[string][]string)
	var order []string
	for _, id := range steps {
		root := find(id)
		if _, ok := groups[root]; !ok {
			order = append(order, root)
		}
		groups[root] = append(groups[root], id)
	}
	for _, root := range order {
		members := groups[root]
		if len(members) < 2 {
			continue
		}
		counter++
		name := fmt.Sprintf("group-%d", counter)
		analysis.ParallelGroups[name] = members
		for _, id := range members {
			info := analysis.Steps[id]
			info.ParallelGroup = name
			for _, other := range members {
				if other != id {
					info.CanParallel = append(info.CanParallel, other)
				}
			}
			sort.Strings(info.CanParallel)
		}
	}
	return counter
}

// blockingDepths answers each node's blocking depth: 0 with no open blocker,
// one more than its deepest open blocker otherwise. A cycle counts as 0.
func blockingDepths(g *MoleculeGraph, blockedBy map[string]map[string]bool) map[string]int {
	depths := make(map[string]int)
	visiting := make(map[string]bool)
	var depthOf func(id string) int
	depthOf = func(id string) int {
		if d, ok := depths[id]; ok {
			return d
		}
		if visiting[id] {
			return 0
		}
		visiting[id] = true
		deepest := -1
		for blockerID := range blockedBy[id] {
			if blocker := g.IssueMap[blockerID]; blocker != nil && blocker.Status != types.StatusClosed {
				deepest = max(deepest, depthOf(blockerID))
			}
		}
		depths[id] = deepest + 1
		return depths[id]
	}
	for _, issue := range g.Issues {
		depthOf(issue.ID)
	}
	return depths
}

// Molecule step states, as MoleculeStep.State spells them.
const (
	MoleculeStepDone    = "done"
	MoleculeStepCurrent = "current"
	MoleculeStepReady   = "ready"
	MoleculeStepBlocked = "blocked"
	MoleculeStepPending = "pending"
)

// MoleculeStep is one step of a MoleculeView.
type MoleculeStep struct {
	Issue *Issue
	// State is one of the MoleculeStep* constants: done (closed), current (in
	// progress), blocked (status blocked), ready (open, no open blocker inside
	// the molecule) or pending.
	State     string
	IsCurrent bool
}

// MoleculeView is where a molecule stands: every step's state, the progress
// counts, the step in progress and the next ready one.
type MoleculeView struct {
	Graph    *MoleculeGraph
	Analysis *MoleculeAnalysis
	// Steps holds every step (the root excluded) ordered by how many blocking
	// edges inside the molecule each one waits on, fewest first, stable over
	// the graph's own order.
	Steps []*MoleculeStep
	// Completed counts the closed steps and Total every step.
	Completed int
	Total     int
	// CurrentStep is the step in progress (the last one in graph order when
	// several are); NextStep the first ready step.
	CurrentStep *Issue
	NextStep    *Issue
	// Ready holds every node the analysis marks ready (open or in progress
	// with no open blocker inside the molecule), root included, in graph
	// order: `bd ready --mol`'s list, hydrated rows (assignee and all).
	Ready []*Issue
}

// Complete reports whether every step of the molecule is closed.
func (v *MoleculeView) Complete() bool { return v.Completed >= v.Total }

// ViewMolecule loads the molecule rooted at rootID and derives its view.
func ViewMolecule(ctx context.Context, r MoleculeReader, rootID string) (*MoleculeView, error) {
	g, err := LoadMolecule(ctx, r, rootID)
	if err != nil {
		return nil, err
	}
	return MoleculeViewOf(g), nil
}

// MoleculeViewOf derives the view of an already loaded graph.
func MoleculeViewOf(g *MoleculeGraph) *MoleculeView {
	analysis := AnalyzeMolecule(g)
	view := &MoleculeView{Graph: g, Analysis: analysis, Total: len(g.Issues) - 1}
	for _, issue := range g.Issues {
		if info := analysis.Steps[issue.ID]; info != nil && info.IsReady {
			view.Ready = append(view.Ready, issue)
		}
		if issue.ID == g.Root.ID {
			continue
		}
		step := &MoleculeStep{Issue: issue}
		switch issue.Status {
		case types.StatusClosed:
			step.State = MoleculeStepDone
			view.Completed++
		case types.StatusInProgress:
			step.State = MoleculeStepCurrent
			step.IsCurrent = true
			view.CurrentStep = issue
		case types.StatusBlocked:
			step.State = MoleculeStepBlocked
		default:
			if info := analysis.Steps[issue.ID]; info != nil && info.IsReady {
				step.State = MoleculeStepReady
				if view.NextStep == nil {
					view.NextStep = issue
				}
			} else {
				step.State = MoleculeStepPending
			}
		}
		view.Steps = append(view.Steps, step)
	}
	sortStepsByBlockers(view.Steps, g)
	return view
}

// ReadySteps answers the steps whose state is ready, in view order.
func (v *MoleculeView) ReadySteps() []*Issue {
	var out []*Issue
	for _, step := range v.Steps {
		if step.State == MoleculeStepReady {
			out = append(out, step.Issue)
		}
	}
	return out
}

func sortStepsByBlockers(steps []*MoleculeStep, g *MoleculeGraph) {
	inSteps := make(map[string]bool, len(steps))
	for _, step := range steps {
		inSteps[step.Issue.ID] = true
	}
	waits := make(map[string]int, len(steps))
	for _, dep := range g.Dependencies {
		if dep.Type == types.DepBlocks && inSteps[dep.IssueID] && inSteps[dep.DependsOnID] {
			waits[dep.IssueID]++
		}
	}
	sort.SliceStable(steps, func(i, j int) bool {
		return waits[steps[i].Issue.ID] < waits[steps[j].Issue.ID]
	})
}

// MoleculeProgress is a molecule's progress counts over its DIRECT
// parent-child steps.
type MoleculeProgress = types.MoleculeProgressStats

// ReadMoleculeProgress counts the direct steps of the molecule rooted at
// rootID without loading the whole graph. A root that names nothing is
// ErrNotFound.
func ReadMoleculeProgress(ctx context.Context, r MoleculeReader, rootID string) (*MoleculeProgress, error) {
	if !r.usable() {
		return nil, fmt.Errorf("%w: molecule reads need a reader", ErrValidation)
	}
	roots, err := getIssues(ctx, r, []string{rootID})
	if err != nil {
		return nil, fmt.Errorf("loading molecule %s: %w", rootID, err)
	}
	root := roots[rootID]
	if root == nil {
		return nil, fmt.Errorf("%w: molecule %s", ErrNotFound, rootID)
	}
	children, err := r.Related(ctx, RelatedRequest{
		ID:        rootID,
		Direction: RelationIn,
		Types:     []types.DependencyType{types.DepParentChild},
	})
	if err != nil {
		return nil, fmt.Errorf("reading the steps of %s: %w", rootID, err)
	}
	return MoleculeProgressOf(root, children), nil
}

// MoleculeProgressOf folds a root's inbound parent-child neighbors into its
// progress counts. CurrentStepID is the first in-progress step in the order
// given.
func MoleculeProgressOf(root *Issue, children []*RelatedIssue) *MoleculeProgress {
	stats := &MoleculeProgress{}
	if root != nil {
		stats.MoleculeID, stats.MoleculeTitle = root.ID, root.Title
	}
	for _, child := range children {
		if child == nil || child.DependencyType != types.DepParentChild {
			continue
		}
		stats.Total++
		switch child.Status {
		case types.StatusClosed:
			stats.Completed++
		case types.StatusInProgress:
			stats.InProgress++
			if stats.CurrentStepID == "" {
				stats.CurrentStepID = child.ID
			}
		}
	}
	return stats
}

// CompletedMolecule answers the molecule root that closing stepID completed
// and that closes itself when complete (AutoClosesWhenComplete): the root is
// open and every step under it is closed. It answers nil when stepID belongs
// to no molecule, when the root is already closed or does not auto-close, or
// when a step is still open. It writes nothing; the caller closes the root,
// guarded on the RowVersion of the root it answers.
func CompletedMolecule(ctx context.Context, r MoleculeReader, stepID string) (*Issue, error) {
	rootID, err := MoleculeRoot(ctx, r, stepID)
	if err != nil || rootID == "" {
		return nil, err
	}
	g, err := LoadMolecule(ctx, r, rootID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if g.Root.Status == types.StatusClosed || !AutoClosesWhenComplete(g.Root) {
		return nil, nil
	}
	for _, issue := range g.Issues[1:] {
		if issue.Status != types.StatusClosed {
			return nil, nil
		}
	}
	return g.Root, nil
}

// MoleculeAutoCloseReason is the close reason a molecule root's auto-close
// records.
const MoleculeAutoCloseReason = "all steps complete"

// IsMoleculeAutoCloseRefusal reports whether err is a close-policy refusal of
// a completed molecule's root — the outcomes that leave the root open without
// failing the step close that completed it.
func IsMoleculeAutoCloseRefusal(err error) bool {
	var openChildren *CloseOpenChildrenError
	return errors.Is(err, ErrCloseBlocked) || errors.As(err, &openChildren) || errors.Is(err, ErrVersionMismatch)
}

// MoleculeRootClose is the ONE close request a molecule auto-close sends for
// the root CompletedMolecule answered: unforced, recording
// MoleculeAutoCloseReason and the step close's session, and guarded on the
// root revision CompletedMolecule read, so a step reopened (or the root
// edited) in between refuses rather than being closed over. Every backend's
// in-transaction auto-close (CloseRequest.AutoCloseMolecule) builds its root
// close from it.
func MoleculeRootClose(root *Issue, actor, session string) CloseRequest {
	version := root.RowVersion
	return CloseRequest{
		Actor:           actor,
		IssueID:         root.ID,
		Reason:          MoleculeAutoCloseReason,
		Session:         session,
		ExpectedVersion: &version,
	}
}
