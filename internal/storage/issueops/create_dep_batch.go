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
// import with one blocks edge each spent ~73s of ~82s there. depGraph walks
// the edges in memory instead. It loads a node's outgoing edges (from both
// tables, through the issue_id index) the first time a walk reaches it —
// the batch's own endpoints up front, one IN-list read per BFS level after
// that — and keeps every loaded node equal to its stored rows by applying each
// edge the pass writes. Memory and reads therefore scale with the subgraph the
// batch's walks touch, not with the size of the edge tables, and each check
// sees exactly the graph the CTE would have seen at that point.
type depBatchLookups struct {
	wisps        map[string]struct{}
	issuesExists map[string]bool
	graph        *depGraph
}

// depBatchGraphMinEdges is the number of scheduling edges in a batch at which
// the in-memory walk replaces the per-edge CTEs. Each per-edge CTE scans the
// edge tables at least once, while the walk reads only the edges it reaches,
// so the threshold is small.
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
		l.graph = newDepGraph()
		// Every edge the pass writes has its source here and every walk
		// starts at one of these, so loading them now keeps recordInsert and
		// the first step of each walk free of reads.
		if err = l.graph.load(ctx, tx, ids); err != nil {
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
			// The wisps set IS the presence proof: classify returned
			// DepTargetWisp only because the id is in it, and the pass writes
			// no wisp rows, so the per-edge `SELECT 1 FROM wisps` would find it.
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
	ancestor, err := l.graph.reaches(ctx, tx, dep.IssueID, dep.DependsOnID, true)
	if err != nil {
		return fmt.Errorf("failed to check blocker ancestry: %w", err)
	}
	if ancestor {
		return &domain.DependencyHierarchyConflictError{
			IssueID: dep.IssueID, BlockerID: dep.DependsOnID, BlockerIsAncestor: true,
		}
	}
	descendant, err := l.graph.reaches(ctx, tx, dep.DependsOnID, dep.IssueID, true)
	if err != nil {
		return fmt.Errorf("failed to check blocker ancestry: %w", err)
	}
	if descendant {
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
	cycle, err := l.graph.reaches(ctx, tx, dep.DependsOnID, dep.IssueID, false)
	if err != nil {
		return fmt.Errorf("failed to check for dependency cycle: %w", err)
	}
	if cycle {
		return domain.ErrDependencyCycle
	}
	return nil
}

// recordInsert keeps the in-memory graph equal to the edge tables after the
// pass's INSERT ... ON DUPLICATE KEY UPDATE type = type for dep. rowsAffected
// alone cannot say whether that statement inserted (a connection with
// clientFoundRows reports a matched duplicate as 1), so: a pair the graph has
// never seen was certainly inserted with dep.Type; a pair it has seen is
// re-read from the table. A source the graph has not loaded yet is simply
// loaded now, which reads the written row along with the rest.
func (l *depBatchLookups) recordInsert(ctx context.Context, tx DBTX, depTable string, dep *types.Dependency, rowsAffected int64) error {
	if l == nil || l.graph == nil || rowsAffected == 0 {
		return nil
	}
	if !l.graph.loaded[dep.IssueID] {
		return l.graph.load(ctx, tx, []string{dep.IssueID})
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

// depGraph mirrors, for every node it has loaded, that node's outgoing rows
// in both dependency tables as (table, source, target) -> types, with
// adjacency counts for the two walks: scheduling (blocks, conditional-blocks,
// parent-child — cycleReachabilityQuery's edge set) and parent-child alone
// (isAncestorInTx's). A node's rows are loaded at most once; after that the
// graph tracks them through add/reload as the pass writes.
type depGraph struct {
	loaded  map[string]bool
	rows    map[depEdgeKey][]types.DependencyType
	sched   map[string]map[string]int
	parents map[string]map[string]int
}

func newDepGraph() *depGraph {
	return &depGraph{
		loaded:  map[string]bool{},
		rows:    map[depEdgeKey][]types.DependencyType{},
		sched:   map[string]map[string]int{},
		parents: map[string]map[string]int{},
	}
}

// load reads the outgoing rows of every id in ids the graph has not loaded,
// one IN-list read per table per queryBatchSize chunk.
//
//nolint:gosec // G201: table names are the fixed cycleDetectionTables; only placeholders are formatted in.
func (g *depGraph) load(ctx context.Context, tx DBTX, ids []string) error {
	var todo []string
	for _, id := range ids {
		if id != "" && !g.loaded[id] {
			g.loaded[id] = true
			todo = append(todo, id)
		}
	}
	for start := 0; start < len(todo); start += queryBatchSize {
		end := min(start+queryBatchSize, len(todo))
		placeholders, args := buildSQLInClause(todo[start:end])
		for _, table := range cycleDetectionTables() {
			rows, err := tx.QueryContext(ctx, fmt.Sprintf(
				"SELECT issue_id, %s, type FROM %s WHERE issue_id IN (%s)", DepTargetExpr, table, placeholders), args...)
			if err != nil {
				return fmt.Errorf("load dependency edges from %s: %w", table, err)
			}
			for rows.Next() {
				var source string
				var target sql.NullString
				var depType string
				if err := rows.Scan(&source, &target, &depType); err != nil {
					_ = rows.Close()
					return fmt.Errorf("load dependency edges from %s: %w", table, err)
				}
				// A row with no target column set joins to a NULL node in the
				// CTE, which never equals a probed id: track it (target "")
				// for duplicate detection only.
				g.add(depEdgeKey{table: table, source: source, target: target.String}, types.DependencyType(depType))
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return fmt.Errorf("load dependency edges from %s: %w", table, err)
			}
		}
	}
	return nil
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
// or parent-child edges only when parentsOnly is set. It walks breadth-first,
// loading each level's not-yet-loaded nodes in one read.
func (g *depGraph) reaches(ctx context.Context, tx DBTX, start, goal string, parentsOnly bool) (bool, error) {
	if start == goal {
		return true, nil
	}
	visited := map[string]bool{start: true}
	frontier := []string{start}
	for len(frontier) > 0 {
		if err := g.load(ctx, tx, frontier); err != nil {
			return false, err
		}
		adj := g.sched
		if parentsOnly {
			adj = g.parents
		}
		var next []string
		for _, node := range frontier {
			for to := range adj[node] {
				if to == goal {
					return true, nil
				}
				if !visited[to] {
					visited[to] = true
					next = append(next, to)
				}
			}
		}
		frontier = next
	}
	return false, nil
}
