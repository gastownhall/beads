package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

type DepTargetKind int

const (
	DepTargetIssue DepTargetKind = iota
	DepTargetWisp
	DepTargetExternal
)

func (k DepTargetKind) Column() string {
	switch k {
	case DepTargetWisp:
		return "depends_on_wisp_id"
	case DepTargetExternal:
		return "depends_on_external"
	default:
		return "depends_on_issue_id"
	}
}

// DepTargetExpr is the SQL expression that resolves a dependency row's target
// id from its three typed columns. Use this in SELECT projections (aliased as
// depends_on_id) and in WHERE clauses when the caller doesn't know the target
// kind ahead of time.
const DepTargetExpr = sqlbuild.DepTargetExpr

func depTargetExpr(alias string) string {
	if alias == "" {
		return DepTargetExpr
	}
	return fmt.Sprintf("COALESCE(%s.depends_on_issue_id, %s.depends_on_wisp_id, %s.depends_on_external)", alias, alias, alias)
}

func depTargetEquals(alias string) string {
	return depTargetExpr(alias) + " = ?"
}

func depTargetIn(alias, placeholders string) string {
	return depTargetExpr(alias) + " IN (" + placeholders + ")"
}

func ClassifyDepTarget(ctx context.Context, tx *sql.Tx, dep *types.Dependency, isCrossPrefix bool) DepTargetKind {
	if isCrossPrefix || strings.HasPrefix(dep.DependsOnID, "external:") {
		return DepTargetExternal
	}
	if IsActiveWispInTx(ctx, tx, dep.DependsOnID) {
		return DepTargetWisp
	}
	return DepTargetIssue
}

// AddDependencyOpts configures AddDependencyInTx behavior.
// When fields are left empty, AddDependencyInTx performs wisp routing
// automatically via IsActiveWispInTx. Callers that have already determined
// routing (e.g., DoltStore with its pre-tx wisp cache) can set fields
// explicitly to skip the redundant DB check.
type AddDependencyOpts struct {
	// SourceTable is the table to validate the source issue exists in.
	// Auto-detected via wisp routing if empty.
	SourceTable string
	// TargetTable is the table to validate the target issue exists in.
	// Auto-detected via wisp routing if empty. Ignored when target validation is skipped.
	TargetTable string
	// WriteTable is the dependency table to insert/update/check existing deps in.
	// Auto-detected from source wisp routing if empty.
	WriteTable string
	// DepTables are the tables to scan for cycle detection. Defaults to both
	// dependency tables; edge storage is source-routed, so same-class endpoints
	// can still have mixed-table interior paths.
	DepTables []string
	// IsCrossPrefix is true when source and target have different prefixes,
	// meaning the target lives in another rig's database.
	IsCrossPrefix bool
	// SkipCycleCheck skips the recursive pre-insert cycle check for callers
	// that intentionally trade validation cost for bulk graph wiring speed.
	SkipCycleCheck bool
	TargetKind     *DepTargetKind
	// PrecheckedTarget, when non-nil, replaces the in-tx target read: the
	// target is known to exist with this type and status. Callers that set
	// it must also set TargetKind, since target classification otherwise
	// queries tx too.
	PrecheckedTarget *DepTargetPrecheck
}

// DepTargetPrecheck carries a target-issue row the caller has already read
// from the transaction that can see it. Dolt server mode splits one logical
// transaction across two SQL sessions (versioned tables vs dolt-ignored wisp
// tables); when the dependency write table lives on one session and the
// target issue on the other, a target read on tx misses rows created earlier
// in the same logical transaction. The caller reads the target on its own
// session and passes the row here; existence validation, cross-type blocking
// validation, and the direct is_blocked mark then use these values instead
// of querying the target table on tx.
type DepTargetPrecheck struct {
	IssueType string
	Status    string
}

// AddDependencyInTx validates and inserts a dependency within an existing
// transaction. It handles:
//   - Wisp routing (auto-detected or caller-provided)
//   - Source/target existence validation
//   - Hierarchy deadlock validation for blocking deps (GH#1495, bd-wg7ve)
//   - Cycle detection via recursive CTE across both dependency tables
//   - Idempotent same-type updates (metadata only)
//   - Type conflict detection
//
// The caller is responsible for transaction lifecycle, dolt commits, and
// any cache invalidation.
func AddDependencyInTx(ctx context.Context, tx *sql.Tx, dep *types.Dependency, actor string, opts AddDependencyOpts) error {
	// Auto-detect source routing if not provided.
	sourceTable := opts.SourceTable
	writeTable := opts.WriteTable
	if sourceTable == "" || writeTable == "" {
		sourceIsWisp := IsActiveWispInTx(ctx, tx, dep.IssueID)
		st, _, _, dt := WispTableRouting(sourceIsWisp)
		if sourceTable == "" {
			sourceTable = st
		}
		if writeTable == "" {
			writeTable = dt
		}
	}

	// Auto-detect target routing if not provided (skip for external/cross-prefix).
	targetTable := opts.TargetTable
	if targetTable == "" && !strings.HasPrefix(dep.DependsOnID, "external:") && !opts.IsCrossPrefix {
		targetIsWisp := IsActiveWispInTx(ctx, tx, dep.DependsOnID)
		targetTable, _, _, _ = WispTableRouting(targetIsWisp)
	}
	if targetTable == "" {
		targetTable = "issues"
	}

	depTables := opts.DepTables
	if len(depTables) == 0 {
		depTables = cycleDetectionTables()
	}

	metadata := dep.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	// Validate source issue exists and get its type.
	var sourceType string
	//nolint:gosec // G201: sourceTable is from WispTableRouting ("issues" or "wisps")
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT issue_type FROM %s WHERE id = ?`, sourceTable), dep.IssueID).Scan(&sourceType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("issue %s not found", dep.IssueID)
		}
		return fmt.Errorf("failed to check issue existence: %w", err)
	}

	// Validate target issue exists (skip for external and cross-prefix refs,
	// and for targets the caller already read on their own transaction).
	var targetType string
	switch {
	case opts.PrecheckedTarget != nil:
		targetType = opts.PrecheckedTarget.IssueType
	case !strings.HasPrefix(dep.DependsOnID, "external:") && !opts.IsCrossPrefix:
		//nolint:gosec // G201: targetTable is from WispTableRouting ("issues" or "wisps")
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT issue_type FROM %s WHERE id = ?`, targetTable), dep.DependsOnID).Scan(&targetType); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("issue %s not found", dep.DependsOnID)
			}
			return fmt.Errorf("failed to check target issue existence: %w", err)
		}
	}

	if targetType != "" {
		if err := CheckBlockingHierarchyInTx(ctx, tx, dep, depTables); err != nil {
			return err
		}
	}

	if !opts.SkipCycleCheck {
		if err := CheckDependencyCycleInTx(ctx, tx, dep, depTables); err != nil {
			return err
		}
	}

	var kind DepTargetKind
	if opts.TargetKind != nil {
		kind = *opts.TargetKind
	} else {
		kind = ClassifyDepTarget(ctx, tx, dep, opts.IsCrossPrefix)
	}
	targetCol := kind.Column()

	// Check for existing dependency between the same pair. Use the resolved
	// target expression defensively so stale/reclassified rows in another typed
	// target column cannot bypass the idempotency/conflict check.
	var existingType string
	//nolint:gosec // G201: writeTable from WispTableRouting; depTargetEquals has no user input.
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT type FROM %s WHERE issue_id = ? AND %s`, writeTable, depTargetEquals("")),
		dep.IssueID, dep.DependsOnID).Scan(&existingType)
	if err == nil {
		if existingType == string(dep.Type) {
			// Same type — idempotent; update metadata.
			//nolint:gosec // G201: writeTable from WispTableRouting; depTargetEquals has no user input.
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET metadata = ? WHERE issue_id = ? AND %s`, writeTable, depTargetEquals("")),
				metadata, dep.IssueID, dep.DependsOnID); err != nil {
				return fmt.Errorf("failed to update dependency metadata: %w", err)
			}
			return nil
		}
		return fmt.Errorf("dependency %s -> %s already exists with type %q (requested %q); remove it first with 'bd dep remove' then re-add",
			dep.IssueID, dep.DependsOnID, existingType, dep.Type)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to check existing dependency: %w", err)
	}

	// id is derived deterministically from the natural edge key (issue_id,
	// target) so the same edge gets the same primary key on every clone and the
	// dependencies table merges cleanly across Dolt clones (#4259). DependsOnID
	// is the resolved target written into targetCol.
	//nolint:gosec // G201: writeTable from WispTableRouting; targetCol from DepTargetKind.Column()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, issue_id, %s, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, ?, NOW(), ?, ?, ?)
	`, writeTable, targetCol), depid.New(dep.IssueID, dep.DependsOnID), dep.IssueID, dep.DependsOnID, dep.Type, actor, metadata, dep.ThreadID); err != nil {
		return fmt.Errorf("failed to add dependency: %w", err)
	}

	srcIsWisp := writeTable == "wisp_dependencies"
	var affectedIssues, affectedWisps []string
	var aerr error
	if srcIsWisp {
		affectedIssues, affectedWisps, aerr = AffectedByDepChangeForWispInTx(ctx, tx, dep.IssueID, dep.DependsOnID, dep.Type)
	} else {
		affectedIssues, affectedWisps, aerr = AffectedByDepChangeInTx(ctx, tx, dep.IssueID, dep.DependsOnID, dep.Type)
	}
	if aerr != nil {
		return fmt.Errorf("affected by add dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, aerr)
	}
	if dep.Type == types.DepBlocks || dep.Type == types.DepConditionalBlocks {
		if err := markDirectBlockingDependencySourceInTx(ctx, tx, dep.IssueID, srcIsWisp, dep.DependsOnID, kind, opts.PrecheckedTarget); err != nil {
			return fmt.Errorf("mark direct is_blocked after add dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
		}
		affectedIssues, affectedWisps = RemoveSourceFromAffected(dep.IssueID, srcIsWisp, affectedIssues, affectedWisps)
	}
	if dep.Type == types.DepParentChild {
		// Parent-child adds are not monotonic: adding an already-closed child can
		// satisfy an any-children waits-for gate and unblock the waiter.
		if err := RecomputeIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
			return fmt.Errorf("recompute is_blocked after add dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
		}
		return nil
	}
	if err := MarkIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
		return fmt.Errorf("mark is_blocked after add dependency %s -> %s: %w", dep.IssueID, dep.DependsOnID, err)
	}
	return nil
}

// RemoveSourceFromAffected drops the dep source from the affected-ID sets
// after a direct is_blocked mark, so the follow-up Mark/Recompute pass does
// not redo it. Shared with the domain/db dependency repository.
func RemoveSourceFromAffected(source string, srcIsWisp bool, issueIDs, wispIDs []string) ([]string, []string) {
	if srcIsWisp {
		return issueIDs, removeID(wispIDs, source)
	}
	return removeID(issueIDs, source), wispIDs
}

func removeID(ids []string, remove string) []string {
	if len(ids) == 0 {
		return ids
	}
	out := ids[:0]
	for _, id := range ids {
		if id != remove {
			out = append(out, id)
		}
	}
	return out
}

//nolint:gosec // G201: table names are selected from fixed issue/wisp tables.
func markDirectBlockingDependencySourceInTx(ctx context.Context, tx *sql.Tx, source string, srcIsWisp bool, target string, targetKind DepTargetKind, precheckedTarget *DepTargetPrecheck) error {
	sourceTable := "issues"
	if srcIsWisp {
		sourceTable = "wisps"
	}
	targetTable := ""
	switch targetKind {
	case DepTargetIssue:
		targetTable = "issues"
	case DepTargetWisp:
		targetTable = "wisps"
	default:
		return nil
	}

	if precheckedTarget != nil {
		// The target row lives on another session; the EXISTS gate below
		// would miss it there. Its openness is already known, so gate in Go
		// and mark the source (whose table always matches tx) directly.
		if types.Status(precheckedTarget.Status) == types.StatusClosed || types.Status(precheckedTarget.Status) == types.StatusPinned {
			return nil
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s s SET s.is_blocked = 1, s.updated_at = s.updated_at
			WHERE s.id = ?
			  AND s.is_blocked = 0
			  AND s.status <> 'closed' AND s.status <> 'pinned'
		`, sourceTable), source)
		return err
	}

	// MySQL 8.0+ rejects UPDATE <T> ... WHERE EXISTS (SELECT FROM <T> ...)
	// even with distinct aliases (Error 1093: can't specify target table
	// for update in FROM clause), which fires here whenever sourceTable ==
	// targetTable. Wrapping the inner SELECT in a derived table makes MySQL
	// treat it as an independent rowset, satisfying the restriction. Dolt
	// accepts both forms, so this is a no-op there.
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s s SET s.is_blocked = 1, s.updated_at = s.updated_at
		WHERE s.id = ?
		  AND s.is_blocked = 0
		  AND s.status <> 'closed' AND s.status <> 'pinned'
		  AND EXISTS (
		    SELECT 1 FROM (
		      SELECT id, status FROM %s WHERE id = ?
		    ) AS t
		    WHERE t.status <> 'closed' AND t.status <> 'pinned'
		  )
	`, sourceTable, targetTable), source, target)
	return err
}

// CheckDependencyCycleInTx rejects self-dependencies and cycles across the
// combined blocks, conditional-blocks, and parent-child graph before insert.
// The caller may pass a restricted depTables list for a known storage bucket;
// nil uses all dependency tables.
func CheckDependencyCycleInTx(ctx context.Context, tx DBTX, dep *types.Dependency, depTables []string) error {
	if dep.IssueID == dep.DependsOnID {
		return fmt.Errorf("%w: %s cannot depend on itself", domain.ErrSelfDependency, dep.IssueID)
	}
	if !isSchedulingEdge(dep.Type) {
		return nil
	}
	wouldCycle, err := WouldCreateSchedulingCycleInTx(ctx, tx, dep.IssueID, dep.DependsOnID, depTables)
	if err != nil {
		return fmt.Errorf("failed to check for dependency cycle: %w", err)
	}
	if wouldCycle {
		return domain.ErrDependencyCycle
	}
	return nil
}

// WouldCreateSchedulingCycleInTx reports whether adding issueID -> dependsOnID
// would close a cycle in the combined scheduling graph. It is shared by the
// classic and domain storage stacks so both traverse the same dependency types
// and typed target columns.
func WouldCreateSchedulingCycleInTx(ctx context.Context, tx DBTX, issueID, dependsOnID string, depTables []string) (bool, error) {
	if len(depTables) == 0 {
		depTables = cycleDetectionTables()
	}
	var reachable int
	query := cycleReachabilityQuery(depTables)
	if err := tx.QueryRowContext(ctx, query, dependsOnID, issueID).Scan(&reachable); err != nil {
		return false, err
	}
	return reachable > 0, nil
}

// cycleReachabilityQuery uses UNION distinct recursion so cyclic and diamond
// graphs terminate by unique reachable node instead of enumerating paths.
//
// The walked edge set is the union of scheduling-relevant edges: blocks,
// conditional-blocks, and parent-child. Parent-child is included because a
// blocked parent propagates its blocked state to its children in the
// ready-work computation, so a chain mixing blocks and parent-child edges
// can form a logical livelock that prevents anything from being ready.
func cycleReachabilityQuery(depTables []string) string {
	if len(depTables) == 1 {
		return fmt.Sprintf(`
			WITH RECURSIVE reachable(node) AS (
				SELECT ?
				UNION
				SELECT %s
				FROM reachable r
				JOIN %s d ON d.issue_id = r.node AND d.type IN ('blocks', 'conditional-blocks', 'parent-child')
			)
			SELECT COUNT(*) FROM reachable WHERE node = ?
		`, DepTargetExpr, depTables[0])
	}

	var unions []string
	for _, t := range depTables {
		unions = append(unions, fmt.Sprintf("SELECT issue_id, %s AS depends_on_id FROM %s WHERE type IN ('blocks', 'conditional-blocks', 'parent-child')", DepTargetExpr, t))
	}
	unionQuery := strings.Join(unions, " UNION ")
	return fmt.Sprintf(`
		WITH RECURSIVE reachable(node) AS (
			SELECT ?
			UNION
			SELECT d.depends_on_id
			FROM reachable r
			JOIN (%s) d ON d.issue_id = r.node
		)
		SELECT COUNT(*) FROM reachable WHERE node = ?
	`, unionQuery)
}

func cycleDetectionTables() []string {
	return []string{"dependencies", "wisp_dependencies"}
}

// isSchedulingEdge reports whether a dependency type belongs to the static
// combined-cycle set: blocks, conditional-blocks, and parent-child. Waits-for
// also affects readiness but is intentionally outside this validation rule.
func isSchedulingEdge(t types.DependencyType) bool {
	switch t {
	case types.DepBlocks, types.DepConditionalBlocks, types.DepParentChild:
		return true
	default:
		return false
	}
}

// CheckBlockingHierarchyInTx rejects blocking dependencies between an issue
// and its own ancestor or descendant. Cross-prefix/external targets must be
// filtered by the caller because no local hierarchy can connect them.
func CheckBlockingHierarchyInTx(ctx context.Context, tx DBTX, dep *types.Dependency, depTables []string) error {
	if dep.Type != types.DepBlocks && dep.Type != types.DepConditionalBlocks {
		return nil
	}
	if dep.IssueID == dep.DependsOnID {
		return nil // The dedicated self-dependency check owns this error.
	}
	if len(depTables) == 0 {
		depTables = cycleDetectionTables()
	}
	blockerIsAncestor, err := isAncestorInTx(ctx, tx, dep.IssueID, dep.DependsOnID, depTables)
	if err != nil {
		return fmt.Errorf("failed to check blocker ancestry: %w", err)
	}
	if blockerIsAncestor {
		return &domain.DependencyHierarchyConflictError{
			IssueID: dep.IssueID, BlockerID: dep.DependsOnID, BlockerIsAncestor: true,
		}
	}
	blockerIsDescendant, err := isAncestorInTx(ctx, tx, dep.DependsOnID, dep.IssueID, depTables)
	if err != nil {
		return fmt.Errorf("failed to check blocker ancestry: %w", err)
	}
	if blockerIsDescendant {
		return &domain.DependencyHierarchyConflictError{
			IssueID: dep.IssueID, BlockerID: dep.DependsOnID,
		}
	}
	return nil
}

// isAncestorInTx reports whether candidate is an ancestor of node along
// parent-child dependency edges (walking child -> parent only, so siblings
// and cousins in the same hierarchy do not match). Uses UNION distinct
// recursion so diamond/cyclic parentage terminates by unique reachable node.
func isAncestorInTx(ctx context.Context, tx DBTX, node, candidate string, depTables []string) (bool, error) {
	var unions []string
	for _, t := range depTables {
		unions = append(unions, fmt.Sprintf("SELECT issue_id, %s AS parent_id FROM %s WHERE type = 'parent-child'", DepTargetExpr, t))
	}
	//nolint:gosec // G201: depTables are fixed dependency table names from cycleDetectionTables/opts.
	query := fmt.Sprintf(`
		WITH RECURSIVE ancestors(node) AS (
			SELECT ?
			UNION
			SELECT d.parent_id
			FROM ancestors a
			JOIN (%s) d ON d.issue_id = a.node
		)
		SELECT COUNT(*) FROM ancestors WHERE node = ?
	`, strings.Join(unions, " UNION "))
	var n int
	if err := tx.QueryRowContext(ctx, query, node, candidate).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func DeleteWispFromDependenciesInTx(ctx context.Context, tx *sql.Tx, wispID string) error {
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM dependencies WHERE depends_on_wisp_id = ?", wispID); err != nil {
		return fmt.Errorf("delete wisp %s from dependencies: %w", wispID, err)
	}
	return nil
}

//nolint:gosec // G201: inClause contains only ? placeholders
func DeleteWispsFromDependenciesInTx(ctx context.Context, tx *sql.Tx, wispIDs []string) error {
	if len(wispIDs) == 0 {
		return nil
	}
	inClause, args := buildSQLInClause(wispIDs)
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM dependencies WHERE depends_on_wisp_id IN (%s)", inClause),
		args...); err != nil {
		return fmt.Errorf("delete wisps from dependencies: %w", err)
	}
	return nil
}

// Dependency target rewrites reinsert matching rows because Dolt can leave the
// stored generated depends_on_id column stale after a split target column is
// updated by FK cascade.
func UpdateWispIDInDependenciesInTx(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		if err := replaceDependencyTargetInTx(ctx, tx, table, "depends_on_wisp_id", oldID, newID); err != nil {
			return fmt.Errorf("update wisp %s -> %s in %s: %w", oldID, newID, table, err)
		}
	}
	return nil
}

func UpdateIssueIDInDependenciesInTx(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		if err := replaceDependencyTargetInTx(ctx, tx, table, "depends_on_issue_id", oldID, newID); err != nil {
			return fmt.Errorf("update issue target %s -> %s in %s: %w", oldID, newID, table, err)
		}
	}
	// Re-derive the deterministic primary key for rows whose SOURCE issue was
	// renamed. dependencies.issue_id carries fk_dep_issue ... ON UPDATE CASCADE, so
	// renaming the issues row (updateIssueIDInTx updates issues.id first) cascades
	// issue_id from oldID to newID before we get here — but the cascade leaves the
	// surrogate id at depid.New(oldID, target). A stale id re-forks the primary key
	// across clones (#4259) and breaks the same-PK => same-edge invariant the pull
	// conflict resolver relies on, so recompute it from the post-rename (newID, target).
	if err := rekeyDependencySourceInTx(ctx, tx, oldID, newID); err != nil {
		return fmt.Errorf("rekey dependency sources %s -> %s: %w", oldID, newID, err)
	}
	return nil
}

// rekeyDependencySourceInTx rewrites dependencies.id for every edge whose source
// issue was renamed to newID so the stored id equals depid.New(newID, target). It
// matches rows by both newID (the normal post-FK-cascade state) and oldID (defensive,
// in case a caller reaches here before the cascade) and re-asserts issue_id = newID
// so the row converges either way. Only rows whose id is actually stale are touched.
func rekeyDependencySourceInTx(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	queryRows, err := tx.QueryContext(ctx, `
		SELECT id, depends_on_issue_id, depends_on_wisp_id, depends_on_external
		FROM dependencies
		WHERE issue_id = ? OR issue_id = ?
	`, newID, oldID)
	if err != nil {
		return fmt.Errorf("query dependency sources: %w", err)
	}
	type rekey struct{ oldRowID, newRowID string }
	var rekeys []rekey
	for queryRows.Next() {
		var id string
		var issueTarget, wispTarget, external sql.NullString
		if err := queryRows.Scan(&id, &issueTarget, &wispTarget, &external); err != nil {
			_ = queryRows.Close()
			return fmt.Errorf("scan dependency source: %w", err)
		}
		target, ok := resolveDependencyTarget(issueTarget, wispTarget, external)
		if !ok {
			continue // ck_dep_one_target guarantees one target; skip defensively
		}
		if want := depid.New(newID, target); want != id {
			rekeys = append(rekeys, rekey{oldRowID: id, newRowID: want})
		}
	}
	_ = queryRows.Close()
	if err := queryRows.Err(); err != nil {
		return fmt.Errorf("iterate dependency sources: %w", err)
	}
	for _, rk := range rekeys {
		if _, err := tx.ExecContext(ctx,
			"UPDATE dependencies SET id = ?, issue_id = ? WHERE id = ?",
			rk.newRowID, newID, rk.oldRowID); err != nil {
			return fmt.Errorf("rekey dependency source id %s -> %s: %w", rk.oldRowID, rk.newRowID, err)
		}
	}
	return nil
}

// resolveDependencyTarget returns the single non-null dependency target — the value
// depid.New and the uk_dep_* unique keys treat as the edge's target — following the
// same precedence as DepTargetExpr (issue, then wisp, then external).
func resolveDependencyTarget(issueTarget, wispTarget, external sql.NullString) (string, bool) {
	switch {
	case issueTarget.Valid:
		return issueTarget.String, true
	case wispTarget.Valid:
		return wispTarget.String, true
	case external.Valid:
		return external.String, true
	default:
		return "", false
	}
}

func replaceDependencyTargetInTx(ctx context.Context, tx *sql.Tx, table, column, oldID, newID string) error {
	// Dolt does not reliably recompute the stored generated depends_on_id when
	// only the split target column changes. Reinsert rows so the generated key
	// is calculated from the new target value.
	if err := checkRenameTargetCollision(ctx, tx, table, column, newID); err != nil {
		return err
	}

	type depRow struct {
		issueID     string
		issueTarget sql.NullString
		wispTarget  sql.NullString
		external    sql.NullString
		depType     string
		createdAt   sql.NullTime
		createdBy   sql.NullString
		metadata    sql.NullString
		threadID    sql.NullString
	}

	rows := make([]depRow, 0)
	//nolint:gosec // table and column are hardcoded by callers.
	queryRows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id
		FROM %s
		WHERE %s = ? OR (%s = ? AND depends_on_external IS NULL)
	`, table, column, DepTargetExpr), oldID, oldID)
	if err != nil {
		return fmt.Errorf("query dependency targets: %w", err)
	}
	for queryRows.Next() {
		var row depRow
		if err := queryRows.Scan(&row.issueID, &row.issueTarget, &row.wispTarget, &row.external, &row.depType, &row.createdAt, &row.createdBy, &row.metadata, &row.threadID); err != nil {
			_ = queryRows.Close()
			return fmt.Errorf("scan dependency target: %w", err)
		}
		switch column {
		case "depends_on_issue_id":
			row.issueTarget = sql.NullString{String: newID, Valid: true}
			row.wispTarget = sql.NullString{}
			row.external = sql.NullString{}
		case "depends_on_wisp_id":
			row.issueTarget = sql.NullString{}
			row.wispTarget = sql.NullString{String: newID, Valid: true}
			row.external = sql.NullString{}
		default:
			_ = queryRows.Close()
			return fmt.Errorf("replace dependency target: unsupported typed column %q", column)
		}
		rows = append(rows, row)
	}
	_ = queryRows.Close()
	if err := queryRows.Err(); err != nil {
		return fmt.Errorf("iterate dependency targets: %w", err)
	}

	//nolint:gosec // table and column are hardcoded by callers.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = ? OR (%s = ? AND depends_on_external IS NULL)`, table, column, DepTargetExpr), oldID, oldID); err != nil {
		return fmt.Errorf("delete old dependency target: %w", err)
	}
	for _, row := range rows {
		// The retargeted edge's natural key is (issue_id, newID): the switch above
		// set exactly one typed target column to newID. Re-derive id from it so the
		// rewritten row stays merge-safe and keeps a clone-stable primary key (#4259).
		//nolint:gosec // table is hardcoded by callers.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, table), depid.New(row.issueID, newID), row.issueID, nullStringValue(row.issueTarget), nullStringValue(row.wispTarget), nullStringValue(row.external), row.depType, nullTimeValue(row.createdAt), nullStringValue(row.createdBy), nullStringValue(row.metadata), nullStringValue(row.threadID)); err != nil {
			return fmt.Errorf("insert replacement dependency target: %w", err)
		}
	}
	return nil
}

func nullStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullTimeValue(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time
}

func RetargetInboundDependenciesToWispInTx(ctx context.Context, tx *sql.Tx, id string) error {
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		if err := checkRetargetTargetCollision(ctx, tx, table, "depends_on_issue_id", "depends_on_wisp_id", id); err != nil {
			return err
		}
		if err := checkRenameTargetCollision(ctx, tx, table, "depends_on_wisp_id", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET depends_on_wisp_id = ?, depends_on_issue_id = NULL
			WHERE depends_on_issue_id = ?
		`, table), id, id); err != nil {
			return fmt.Errorf("retarget inbound dependencies to wisp in %s for %s: %w", table, id, err)
		}
	}
	return nil
}

func RetargetInboundDependenciesToIssueInTx(ctx context.Context, tx *sql.Tx, id string) error {
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		if err := checkRetargetTargetCollision(ctx, tx, table, "depends_on_wisp_id", "depends_on_issue_id", id); err != nil {
			return err
		}
		if err := checkRenameTargetCollision(ctx, tx, table, "depends_on_issue_id", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			UPDATE %s
			SET depends_on_issue_id = ?, depends_on_wisp_id = NULL
			WHERE depends_on_wisp_id = ?
		`, table), id, id); err != nil {
			return fmt.Errorf("retarget inbound dependencies to issue in %s for %s: %w", table, id, err)
		}
	}
	return nil
}

// UpdateIssueIDInDependencyTargetsInTx is called after the issues PK is updated
// from oldID to newID. FK ON UPDATE CASCADE has already propagated
// depends_on_issue_id from oldID to newID across dependencies and
// wisp_dependencies, so no rewrite is needed.
func UpdateIssueIDInDependencyTargetsInTx(ctx context.Context, tx *sql.Tx, _, newID string) error {
	for _, table := range []string{"dependencies", "wisp_dependencies"} {
		if err := checkRenameTargetCollision(ctx, tx, table, "depends_on_issue_id", newID); err != nil {
			return err
		}
	}
	return nil
}

//nolint:gosec // G201: table and typed columns are hardcoded constants.
func checkRetargetTargetCollision(ctx context.Context, tx *sql.Tx, table, sourceCol, destCol, id string) error {
	var conflictCols []string
	switch destCol {
	case "depends_on_issue_id":
		conflictCols = []string{"depends_on_issue_id", "depends_on_external"}
	case "depends_on_wisp_id":
		conflictCols = []string{"depends_on_wisp_id", "depends_on_external"}
	default:
		return fmt.Errorf("checkRetargetTargetCollision: unsupported destination column %q", destCol)
	}
	if sourceCol != "depends_on_issue_id" && sourceCol != "depends_on_wisp_id" {
		return fmt.Errorf("checkRetargetTargetCollision: unsupported source column %q", sourceCol)
	}

	query := fmt.Sprintf(`
		SELECT 1 FROM %s moving
		JOIN %s existing ON moving.issue_id = existing.issue_id
		WHERE moving.%s = ?
		  AND (existing.%s = ? OR existing.%s = ?)
		LIMIT 1
	`, table, table, sourceCol, conflictCols[0], conflictCols[1])

	var found int
	err := tx.QueryRowContext(ctx, query, id, id, id).Scan(&found)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		if isTableNotExistError(err) {
			return nil
		}
		return fmt.Errorf("check retarget collision in %s: %w", table, err)
	}
	return fmt.Errorf("retarget to %s collides with existing dependency target in %s", id, table)
}

//nolint:gosec // G201: table and typedCol are hardcoded constants.
func checkRenameTargetCollision(ctx context.Context, tx *sql.Tx, table, typedCol, newID string) error {
	var otherCols []string
	switch typedCol {
	case "depends_on_issue_id":
		otherCols = []string{"depends_on_wisp_id", "depends_on_external"}
	case "depends_on_wisp_id":
		otherCols = []string{"depends_on_issue_id", "depends_on_external"}
	default:
		return fmt.Errorf("checkRenameTargetCollision: unsupported typed column %q", typedCol)
	}

	query := fmt.Sprintf(`
		SELECT 1 FROM %s a
		JOIN %s b ON a.issue_id = b.issue_id
		WHERE a.%s = ?
		  AND (b.%s = ? OR b.%s = ?)
		LIMIT 1
	`, table, table, typedCol, otherCols[0], otherCols[1])

	var found int
	err := tx.QueryRowContext(ctx, query, newID, newID, newID).Scan(&found)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		if isTableNotExistError(err) {
			return nil
		}
		return fmt.Errorf("check rename collision in %s: %w", table, err)
	}
	return fmt.Errorf("rename to %s collides with existing dependency target in %s", newID, table)
}

// RemoveDependencyInTx removes a dependency between two issues within an
// existing transaction. Automatically routes to wisp_dependencies if the
// source issue is an active wisp.
//
//nolint:gosec // G201: depTable from WispTableRouting (hardcoded constants)
func RemoveDependencyInTx(ctx context.Context, tx *sql.Tx, issueID, dependsOnID string) error {
	isWisp := IsActiveWispInTx(ctx, tx, issueID)
	_, _, _, depTable := WispTableRouting(isWisp)

	// Capture the row's type before deleting so we can dispatch the right
	// affected-set helper. If no row matches, treat as a no-op.
	var depType string
	row := tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT type FROM %s WHERE issue_id = ? AND %s = ?`, depTable, DepTargetExpr),
		issueID, dependsOnID)
	if err := row.Scan(&depType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lookup dependency type for %s -> %s: %w", issueID, dependsOnID, err)
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE issue_id = ? AND %s = ?`, depTable, DepTargetExpr),
		issueID, dependsOnID); err != nil {
		return fmt.Errorf("remove dependency: %w", err)
	}

	var affectedIssues, affectedWisps []string
	var aerr error
	if isWisp {
		affectedIssues, affectedWisps, aerr = AffectedByDepChangeForWispInTx(ctx, tx, issueID, dependsOnID, types.DependencyType(depType))
	} else {
		affectedIssues, affectedWisps, aerr = AffectedByDepChangeInTx(ctx, tx, issueID, dependsOnID, types.DependencyType(depType))
	}
	if aerr != nil {
		return fmt.Errorf("affected by remove dependency %s -> %s: %w", issueID, dependsOnID, aerr)
	}
	if err := RecomputeIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
		return fmt.Errorf("recompute is_blocked after remove dependency %s -> %s: %w", issueID, dependsOnID, err)
	}
	return nil
}

// GetIssuesByIDsInTx retrieves multiple issues by ID within an existing
// transaction, including labels. Automatically routes each ID to the correct
// table (issues/wisps). Uses batched IN clauses.
//
// wispSet is an optional pre-built set of active wisp IDs scoped to
// cover ids (see WispIDSetInTx). Pass nil to have the helper build
// a scoped set internally; callers hydrating multiple batches inside
// one tx can build the set once over the union of their IDs and
// reuse it across calls.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func GetIssuesByIDsInTx(ctx context.Context, tx DBTX, ids []string, wispSet map[string]struct{}) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	if wispSet == nil {
		var err error
		wispSet, err = WispIDSetInTx(ctx, tx, ids)
		if err != nil {
			return nil, fmt.Errorf("get issues by IDs: build wisp set: %w", err)
		}
	}

	// Partition IDs by wisp status.
	wispIDs, permIDs := partitionByWispSet(ids, wispSet)

	var allIssues []*types.Issue
	for _, pair := range []struct {
		table    string
		labelTbl string
		ids      []string
	}{
		{"issues", "labels", permIDs},
		{"wisps", "wisp_labels", wispIDs},
	} {
		if len(pair.ids) == 0 {
			continue
		}
		for start := 0; start < len(pair.ids); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(pair.ids) {
				end = len(pair.ids)
			}
			batch := pair.ids[start:end]

			placeholders := make([]string, len(batch))
			args := make([]any, len(batch))
			for i, id := range batch {
				placeholders[i] = "?"
				args[i] = id
			}
			inClause := strings.Join(placeholders, ",")

			rows, err := tx.QueryContext(ctx, fmt.Sprintf(
				`SELECT %s FROM %s %s WHERE id IN (%s)`,
				IssueSelectColumns, pair.table, sqlbuild.LeaseJoin(pair.table), inClause), args...)
			if err != nil {
				return nil, fmt.Errorf("get issues by IDs from %s: %w", pair.table, err)
			}
			issueMap := make(map[string]*types.Issue)
			for rows.Next() {
				issue, scanErr := ScanIssueFrom(rows)
				if scanErr != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("get issues by IDs: scan: %w", scanErr)
				}
				allIssues = append(allIssues, issue)
				issueMap[issue.ID] = issue
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("get issues by IDs: rows: %w", err)
			}

			// Hydrate labels.
			if len(issueMap) > 0 {
				labelRows, err := tx.QueryContext(ctx, fmt.Sprintf(
					`SELECT issue_id, label FROM %s WHERE issue_id IN (%s) ORDER BY issue_id, label`,
					pair.labelTbl, inClause), args...)
				if err != nil {
					return nil, fmt.Errorf("get issues by IDs: labels from %s: %w", pair.labelTbl, err)
				}
				for labelRows.Next() {
					var issueID, label string
					if scanErr := labelRows.Scan(&issueID, &label); scanErr != nil {
						_ = labelRows.Close()
						return nil, fmt.Errorf("get issues by IDs: scan label: %w", scanErr)
					}
					if issue, ok := issueMap[issueID]; ok {
						issue.Labels = append(issue.Labels, label)
					}
				}
				_ = labelRows.Close()
				if err := labelRows.Err(); err != nil {
					return nil, fmt.Errorf("get issues by IDs: label rows: %w", err)
				}
			}
		}
	}

	return allIssues, nil
}

// GetDependenciesWithMetadataInTx returns issues that the given issueID depends on,
// along with the dependency type. Works within an existing transaction.
// Queries both dependency tables to handle cross-table dependencies.
//
//nolint:gosec // G201: table names come from hardcoded constants
func GetDependenciesWithMetadataInTx(ctx context.Context, tx DBTX, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	type depMeta struct {
		depID, depType string
	}

	// Query both dependency tables to find all dependencies.
	var deps []depMeta
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT %s AS depends_on_id, type FROM %s WHERE issue_id = ?`, DepTargetExpr, depTable), issueID)
		if err != nil {
			return nil, fmt.Errorf("get dependencies from %s: %w", depTable, err)
		}
		for rows.Next() {
			var d depMeta
			if scanErr := rows.Scan(&d.depID, &d.depType); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get dependencies: scan: %w", scanErr)
			}
			deps = append(deps, d)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get dependencies: rows from %s: %w", depTable, err)
		}
	}

	if len(deps) == 0 {
		return nil, nil
	}

	// Fetch all dependency target issues.
	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := GetIssuesByIDsInTx(ctx, tx, ids, nil)
	if err != nil {
		return nil, fmt.Errorf("get dependencies: fetch issues: %w", err)
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	var results []*types.IssueWithDependencyMetadata
	for _, d := range deps {
		issue, ok := issueMap[d.depID]
		if !ok {
			continue
		}
		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(d.depType),
		})
	}
	return results, nil
}

// GetDependentsWithMetadataInTx returns issues that depend on the given issueID
// along with the dependency type. Works within an existing transaction.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func GetDependentsWithMetadataInTx(ctx context.Context, tx DBTX, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	type depMeta struct {
		depID, depType string
	}

	// Query both dependency tables to find all dependents.
	var deps []depMeta
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, type FROM %s WHERE %s = ?`, depTable, DepTargetExpr), issueID)
		if err != nil {
			return nil, fmt.Errorf("get dependents from %s: %w", depTable, err)
		}
		for rows.Next() {
			var d depMeta
			if scanErr := rows.Scan(&d.depID, &d.depType); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get dependents: scan: %w", scanErr)
			}
			deps = append(deps, d)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get dependents: rows from %s: %w", depTable, err)
		}
	}

	if len(deps) == 0 {
		return nil, nil
	}

	// Fetch all dependent issues.
	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := GetIssuesByIDsInTx(ctx, tx, ids, nil)
	if err != nil {
		return nil, fmt.Errorf("get dependents: fetch issues: %w", err)
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	var results []*types.IssueWithDependencyMetadata
	for _, d := range deps {
		issue, ok := issueMap[d.depID]
		if !ok {
			continue
		}
		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(d.depType),
		})
	}
	return results, nil
}

// GetDependenciesInTx returns issues that the given issueID depends on.
// Queries both dependencies and wisp_dependencies tables.
//
//nolint:gosec // G201: table names come from hardcoded constants
func GetDependenciesInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]*types.Issue, error) {
	var ids []string
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT %s AS depends_on_id FROM %s WHERE issue_id = ?`, DepTargetExpr, depTable), issueID)
		if err != nil {
			return nil, fmt.Errorf("get dependencies from %s: %w", depTable, err)
		}
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get dependencies: scan: %w", scanErr)
			}
			ids = append(ids, id)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get dependencies: rows from %s: %w", depTable, err)
		}
	}

	if len(ids) == 0 {
		return nil, nil
	}

	return GetIssuesByIDsInTx(ctx, tx, ids, nil)
}

// GetDependentsInTx returns issues that depend on the given issueID.
// Queries both dependencies and wisp_dependencies tables.
//
//nolint:gosec // G201: table names come from hardcoded constants
func GetDependentsInTx(ctx context.Context, tx *sql.Tx, issueID string) ([]*types.Issue, error) {
	var ids []string
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id FROM %s WHERE %s = ?`, depTable, DepTargetExpr), issueID)
		if err != nil {
			return nil, fmt.Errorf("get dependents from %s: %w", depTable, err)
		}
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("get dependents: scan: %w", scanErr)
			}
			ids = append(ids, id)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get dependents: rows from %s: %w", depTable, err)
		}
	}

	if len(ids) == 0 {
		return nil, nil
	}

	return GetIssuesByIDsInTx(ctx, tx, ids, nil)
}
