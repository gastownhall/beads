package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// depBatchLookups answers the per-edge reads PersistDependenciesWithOptionsResult
// makes — plane routing, target presence, and the cycle and hierarchy walks —
// from a few batch reads instead of one or more statements per edge.
//
// Routing and presence: the dependency pass runs after every row of the batch
// is written and writes no issue or wisp rows itself, so one read of the wisps
// set and of the target ids present in issues answers every per-edge
// IsActiveWispInTx / SELECT 1 the pass would make, identically.
//
// The walks: per edge, CheckBlockingHierarchyInTx and CheckDependencyCycleInTx
// each run a recursive CTE over the union of both dependency tables. Dolt
// cannot index into that union, so every recursion step rescans the edge
// tables, and a batch whose edges form a chain pays that per edge — a 400-issue
// import with one blocks edge each spent ~73s of ~82s there. depGraph loads
// the edge tables once and walks them in memory, updating itself with every
// edge the pass writes, so each check sees exactly the graph the CTE would
// have seen at that point.
type depBatchLookups struct {
	wisps        map[string]struct{}
	issuesExists map[string]bool
	graph        *depGraph
}

// depBatchGraphMinEdges is the number of scheduling edges in a batch at which
// loading the edge tables once beats walking them per edge. Each per-edge
// walk scans the edge tables at least once, so the threshold is small.
const depBatchGraphMinEdges = 2

// depBatchLookupsMinDeps is the number of edges at which the batch routing
// read replaces the per-edge reads.
const depBatchLookupsMinDeps = 2

func newDepBatchLookups(ctx context.Context, tx DBTX, deps []*types.Dependency) (*depBatchLookups, error) {
	l := &depBatchLookups{}
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	scheduling := 0
	for _, dep := range deps {
		add(dep.IssueID)
		add(dep.DependsOnID)
		if types.IsSchedulingEdge(dep.Type) && dep.IssueID != dep.DependsOnID {
			scheduling++
		}
	}
	wisps, err := WispIDSetInTx(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("route batch dependencies: %w", err)
	}
	l.wisps = wisps
	var issueTargets []string
	seenTarget := map[string]bool{}
	for _, dep := range deps {
		if _, isWisp := wisps[dep.DependsOnID]; isWisp || seenTarget[dep.DependsOnID] {
			continue
		}
		seenTarget[dep.DependsOnID] = true
		issueTargets = append(issueTargets, dep.DependsOnID)
	}
	if l.issuesExists, err = idSetInTx(ctx, tx, "issues", issueTargets); err != nil {
		return nil, err
	}
	if scheduling >= depBatchGraphMinEdges {
		if l.graph, err = loadDepGraph(ctx, tx); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// isWisp is IsActiveWispInTx for an id the batch read covered.
func (l *depBatchLookups) isWisp(ctx context.Context, tx DBTX, id string) bool {
	if l == nil {
		return IsActiveWispInTx(ctx, tx, id)
	}
	_, ok := l.wisps[id]
	return ok
}

// classify is ClassifyDepTarget using the batch routing read.
func (l *depBatchLookups) classify(ctx context.Context, tx DBTX, dep *types.Dependency, isCrossPrefix bool) DepTargetKind {
	if l == nil {
		return ClassifyDepTarget(ctx, tx, dep, isCrossPrefix)
	}
	if isCrossPrefix || IsExternalDepTarget(dep.IssueID, dep.DependsOnID) {
		return DepTargetExternal
	}
	if l.isWisp(ctx, tx, dep.DependsOnID) {
		return DepTargetWisp
	}
	return DepTargetIssue
}

// targetExists is the per-edge `SELECT 1 FROM <issues|wisps> WHERE id = ?`.
//
//nolint:gosec // G201: lookupTable is one of two hardcoded constants.
func (l *depBatchLookups) targetExists(ctx context.Context, tx DBTX, kind DepTargetKind, id string) (bool, error) {
	if l != nil {
		if kind == DepTargetWisp {
			// classify found it in the wisps set, which the pass cannot change.
			return true, nil
		}
		return l.issuesExists[id], nil
	}
	lookupTable := "issues"
	if kind == DepTargetWisp {
		lookupTable = "wisps"
	}
	var exists int
	err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT 1 FROM %s WHERE id = ?", lookupTable), id).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// checkHierarchy is CheckBlockingHierarchyInTx(ctx, tx, dep, nil).
func (l *depBatchLookups) checkHierarchy(ctx context.Context, tx DBTX, dep *types.Dependency) error {
	if l == nil || l.graph == nil {
		return CheckBlockingHierarchyInTx(ctx, tx, dep, nil)
	}
	if dep.Type != types.DepBlocks && dep.Type != types.DepConditionalBlocks {
		return nil
	}
	if dep.IssueID == dep.DependsOnID {
		return nil
	}
	if l.graph.reaches(dep.IssueID, dep.DependsOnID, true) {
		return &domain.DependencyHierarchyConflictError{
			IssueID: dep.IssueID, BlockerID: dep.DependsOnID, BlockerIsAncestor: true,
		}
	}
	if l.graph.reaches(dep.DependsOnID, dep.IssueID, true) {
		return &domain.DependencyHierarchyConflictError{
			IssueID: dep.IssueID, BlockerID: dep.DependsOnID,
		}
	}
	return nil
}

// checkCycle is CheckDependencyCycleInTx(ctx, tx, dep, nil).
func (l *depBatchLookups) checkCycle(ctx context.Context, tx DBTX, dep *types.Dependency) error {
	if l == nil || l.graph == nil {
		return CheckDependencyCycleInTx(ctx, tx, dep, nil)
	}
	if dep.IssueID == dep.DependsOnID {
		return fmt.Errorf("%w: %s cannot depend on itself", domain.ErrSelfDependency, dep.IssueID)
	}
	if !types.IsSchedulingEdge(dep.Type) {
		return nil
	}
	if l.graph.reaches(dep.DependsOnID, dep.IssueID, false) {
		return domain.ErrDependencyCycle
	}
	return nil
}

// recordInsert keeps the in-memory graph equal to the edge tables after the
// pass's INSERT ... ON DUPLICATE KEY UPDATE type = type for dep. rowsAffected
// alone cannot say whether that statement inserted (a connection with
// clientFoundRows reports a matched duplicate as 1), so: a pair the graph has
// never seen was certainly inserted with dep.Type; a pair it has seen is
// re-read from the table.
func (l *depBatchLookups) recordInsert(ctx context.Context, tx DBTX, depTable string, dep *types.Dependency, rowsAffected int64) error {
	if l == nil || l.graph == nil || rowsAffected == 0 {
		return nil
	}
	key := depEdgeKey{table: depTable, source: dep.IssueID, target: dep.DependsOnID}
	if _, known := l.graph.rows[key]; !known {
		l.graph.add(key, dep.Type)
		return nil
	}
	return l.graph.reload(ctx, tx, key)
}

type depEdgeKey struct {
	table, source, target string
}

// depGraph mirrors the rows of both dependency tables as (table, source,
// target) -> types, with adjacency counts for the two walks: scheduling
// (blocks, conditional-blocks, parent-child — cycleReachabilityQuery's edge
// set) and parent-child alone (isAncestorInTx's).
type depGraph struct {
	rows    map[depEdgeKey][]types.DependencyType
	sched   map[string]map[string]int
	parents map[string]map[string]int
}

func newDepGraph() *depGraph {
	return &depGraph{
		rows:    map[depEdgeKey][]types.DependencyType{},
		sched:   map[string]map[string]int{},
		parents: map[string]map[string]int{},
	}
}

//nolint:gosec // G201: table names are the fixed cycleDetectionTables.
func loadDepGraph(ctx context.Context, tx DBTX) (*depGraph, error) {
	g := newDepGraph()
	for _, table := range cycleDetectionTables() {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			"SELECT issue_id, %s, type FROM %s", DepTargetExpr, table))
		if err != nil {
			return nil, fmt.Errorf("load dependency graph from %s: %w", table, err)
		}
		for rows.Next() {
			var source string
			var target sql.NullString
			var depType string
			if err := rows.Scan(&source, &target, &depType); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("load dependency graph from %s: %w", table, err)
			}
			if !target.Valid {
				// No target column set: the CTE's join produces a NULL node,
				// which can never equal a probed id. Track the row for
				// duplicate detection only.
				key := depEdgeKey{table: table, source: source}
				g.rows[key] = append(g.rows[key], types.DependencyType(depType))
				continue
			}
			g.add(depEdgeKey{table: table, source: source, target: target.String}, types.DependencyType(depType))
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("load dependency graph from %s: %w", table, err)
		}
	}
	return g, nil
}

func bump(m map[string]map[string]int, from, to string, delta int) {
	inner := m[from]
	if inner == nil {
		if delta <= 0 {
			return
		}
		inner = map[string]int{}
		m[from] = inner
	}
	inner[to] += delta
	if inner[to] <= 0 {
		delete(inner, to)
		if len(inner) == 0 {
			delete(m, from)
		}
	}
}

func (g *depGraph) add(key depEdgeKey, depType types.DependencyType) {
	g.rows[key] = append(g.rows[key], depType)
	g.adjust(key, depType, 1)
}

func (g *depGraph) adjust(key depEdgeKey, depType types.DependencyType, delta int) {
	if key.target == "" {
		return
	}
	if types.IsSchedulingEdge(depType) {
		bump(g.sched, key.source, key.target, delta)
	}
	if depType == types.DepParentChild {
		bump(g.parents, key.source, key.target, delta)
	}
}

// reload replaces the graph's view of one (table, source, target) pair with
// the rows the table holds now.
//
//nolint:gosec // G201: key.table is one of the fixed dependency tables.
func (g *depGraph) reload(ctx context.Context, tx DBTX, key depEdgeKey) error {
	for _, t := range g.rows[key] {
		g.adjust(key, t, -1)
	}
	delete(g.rows, key)
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(
		"SELECT type FROM %s WHERE issue_id = ? AND %s = ?", key.table, DepTargetExpr), key.source, key.target)
	if err != nil {
		return fmt.Errorf("reload dependency %s -> %s: %w", key.source, key.target, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var depType string
		if err := rows.Scan(&depType); err != nil {
			return fmt.Errorf("reload dependency %s -> %s: %w", key.source, key.target, err)
		}
		g.add(key, types.DependencyType(depType))
	}
	return rows.Err()
}

// reaches reports whether goal is reachable from start (start itself
// included, as in the recursive CTEs' anchor row), walking scheduling edges,
// or parent-child edges only when parentsOnly is set.
func (g *depGraph) reaches(start, goal string, parentsOnly bool) bool {
	if start == goal {
		return true
	}
	adj := g.sched
	if parentsOnly {
		adj = g.parents
	}
	visited := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for next := range adj[node] {
			if next == goal {
				return true
			}
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}
