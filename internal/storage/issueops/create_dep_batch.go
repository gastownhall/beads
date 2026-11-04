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
// import with one blocks edge each spent ~73s of ~82s there. depGraph answers
// the same reachability questions in memory with a bidirectional search: it
// walks forward from the start along outgoing edges and backward from the goal
// along incoming ones, expanding the smaller frontier, and loads a node's
// edges in a direction (through the issue_id index, or the target-column
// indexes) the first time the search needs them. The batch's endpoints are
// loaded up front. A batch's goal is nearly always a row the batch itself just
// created, whose only incoming edges are the batch's own, so the backward
// side usually settles a check without a read. Memory and reads scale with
// the subgraph the searches touch, not with the edge tables, and every
// written edge is applied, so each check sees exactly the graph the CTE would
// have seen at that point.
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
		// Every edge the pass writes has its source here, and every search
		// starts (forward) and ends (backward) at one of these.
		if err = l.graph.load(ctx, tx, ids, false); err != nil {
			return nil, err
		}
		if err = l.graph.load(ctx, tx, ids, true); err != nil {
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
// never seen, while it holds every outgoing row of the source (or every
// incoming row of the target), was certainly inserted with dep.Type; any other
// pair is re-read from the table.
func (l *depBatchLookups) recordInsert(ctx context.Context, tx DBTX, depTable string, dep *types.Dependency, rowsAffected int64) error {
	if l == nil || l.graph == nil || rowsAffected == 0 {
		return nil
	}
	key := depEdgeKey{table: depTable, source: dep.IssueID, target: dep.DependsOnID}
	_, known := l.graph.rows[key]
	if !known && (l.graph.loadedOut[dep.IssueID] || l.graph.loadedIn[dep.DependsOnID]) {
		l.graph.add(key, dep.Type)
		return nil
	}
	return l.graph.reload(ctx, tx, key)
}

type depEdgeKey struct {
	table, source, target string
}

// depGraph mirrors the dependency rows it has read, from both tables, as
// (table, source, target) -> types — a key holds at most one row (the primary
// key is derived from the pair) — with adjacency counts for both search
// directions and both edge sets: scheduling (blocks, conditional-blocks,
// parent-child — cycleReachabilityQuery's) and parent-child alone
// (isAncestorInTx's). loadedOut[n] means every outgoing row of n is known,
// loadedIn[n] every incoming one; each is read at most once, and add/reload
// keep the known rows equal to the stored ones as the pass writes.
type depGraph struct {
	loadedOut, loadedIn map[string]bool
	rows                map[depEdgeKey][]types.DependencyType
	out, in             depAdjacency
}

type depAdjacency struct {
	sched, parents map[string]map[string]int
}

func newDepAdjacency() depAdjacency {
	return depAdjacency{sched: map[string]map[string]int{}, parents: map[string]map[string]int{}}
}

func (a depAdjacency) edges(parentsOnly bool) map[string]map[string]int {
	if parentsOnly {
		return a.parents
	}
	return a.sched
}

func newDepGraph() *depGraph {
	return &depGraph{
		loadedOut: map[string]bool{},
		loadedIn:  map[string]bool{},
		rows:      map[depEdgeKey][]types.DependencyType{},
		out:       newDepAdjacency(),
		in:        newDepAdjacency(),
	}
}

// load reads, for every id in ids not yet loaded in that direction, its
// outgoing rows (incoming false: the issue_id index) or its incoming rows
// (incoming true: one read per target-column index), per table per
// queryBatchSize chunk. Rows already known are skipped, so a row reached from
// both ends is counted once.
//
//nolint:gosec // G201: table names are the fixed cycleDetectionTables; only placeholders are formatted in.
func (g *depGraph) load(ctx context.Context, tx DBTX, ids []string, incoming bool) error {
	loaded := g.loadedOut
	if incoming {
		loaded = g.loadedIn
	}
	var todo []string
	for _, id := range ids {
		if id != "" && !loaded[id] {
			loaded[id] = true
			todo = append(todo, id)
		}
	}
	for start := 0; start < len(todo); start += queryBatchSize {
		chunk := todo[start:min(start+queryBatchSize, len(todo))]
		placeholders, args := buildSQLInClause(chunk)
		want := make(map[string]bool, len(chunk))
		for _, id := range chunk {
			want[id] = true
		}
		for _, table := range cycleDetectionTables() {
			// Outgoing rows come through the issue_id index. Incoming rows
			// come through each target column's own index, one read per
			// column: Dolt plans a UNION ALL of the three as a scan (~0.7 s
			// on a 100k-edge table) where each leg alone is an index lookup.
			queries := []string{fmt.Sprintf("SELECT issue_id, %s, type FROM %s WHERE issue_id IN (%s)", DepTargetExpr, table, placeholders)}
			if incoming {
				queries = queries[:0]
				for _, col := range []string{"depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"} {
					queries = append(queries, fmt.Sprintf("SELECT issue_id, %s, type FROM %s WHERE %s IN (%s)", DepTargetExpr, table, col, placeholders))
				}
			}
			read := map[depEdgeKey][]types.DependencyType{}
			for _, query := range queries {
				if err := g.readRows(ctx, tx, table, query, args, incoming, want, read); err != nil {
					return err
				}
			}
			for key, depTypes := range read {
				if incoming && len(depTypes) > 1 {
					// The same row matched through two target columns.
					depTypes = depTypes[:1]
				}
				for _, depType := range depTypes {
					g.add(key, depType)
				}
			}
		}
	}
	return nil
}

// readRows collects the rows query returns into read, skipping rows the graph
// already knows and, for an incoming read, rows whose target (the first set
// target column, as in the CTEs) is not one of the ids asked for. A row with
// no target set (target "") joins to a NULL node in the CTE and is tracked
// for duplicate detection only.
func (g *depGraph) readRows(ctx context.Context, tx DBTX, table, query string, args []any, incoming bool, want map[string]bool, read map[depEdgeKey][]types.DependencyType) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("load dependency edges from %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var source string
		var target sql.NullString
		var depType string
		if err := rows.Scan(&source, &target, &depType); err != nil {
			return fmt.Errorf("load dependency edges from %s: %w", table, err)
		}
		if incoming && !want[target.String] {
			continue
		}
		key := depEdgeKey{table: table, source: source, target: target.String}
		if _, known := g.rows[key]; known {
			continue
		}
		read[key] = append(read[key], types.DependencyType(depType))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load dependency edges from %s: %w", table, err)
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
		bump(g.out.sched, key.source, key.target, delta)
		bump(g.in.sched, key.target, key.source, delta)
	}
	if depType == types.DepParentChild {
		bump(g.out.parents, key.source, key.target, delta)
		bump(g.in.parents, key.target, key.source, delta)
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
//
// It searches from both ends — forward from start along outgoing edges,
// backward from goal along incoming ones — always expanding the smaller
// frontier one level, after loading that level's unloaded nodes in that
// direction in one read; the two meet exactly when a path exists. Either
// frontier running dry proves there is none.
func (g *depGraph) reaches(ctx context.Context, tx DBTX, start, goal string, parentsOnly bool) (bool, error) {
	if start == goal {
		return true, nil
	}
	seenFwd := map[string]bool{start: true}
	seenBwd := map[string]bool{goal: true}
	fwd, bwd := []string{start}, []string{goal}
	for len(fwd) > 0 && len(bwd) > 0 {
		backward := len(bwd) < len(fwd)
		frontier, seen, other, adj := fwd, seenFwd, seenBwd, g.out
		if backward {
			frontier, seen, other, adj = bwd, seenBwd, seenFwd, g.in
		}
		if err := g.load(ctx, tx, frontier, backward); err != nil {
			return false, err
		}
		edges := adj.edges(parentsOnly)
		var next []string
		for _, node := range frontier {
			for to := range edges[node] {
				if other[to] {
					return true, nil
				}
				if !seen[to] {
					seen[to] = true
					next = append(next, to)
				}
			}
		}
		if backward {
			bwd = next
		} else {
			fwd = next
		}
	}
	return false, nil
}
