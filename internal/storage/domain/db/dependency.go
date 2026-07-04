package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func NewDependencySQLRepository(runner Runner) domain.DependencySQLRepository {
	return &dependencySQLRepositoryImpl{runner: runner}
}

type dependencySQLRepositoryImpl struct {
	runner Runner
}

var _ domain.DependencySQLRepository = (*dependencySQLRepositoryImpl)(nil)

const depTargetExpr = sqlbuild.DepTargetExpr

const depSelectColumns = "issue_id, " + depTargetExpr + " AS depends_on_id, type, created_at, created_by, metadata, thread_id"

func pickDepTable(useWisps bool) string {
	if useWisps {
		return "wisp_dependencies"
	}
	return "dependencies"
}

func (r *dependencySQLRepositoryImpl) pickDepTargetColumn(ctx context.Context, dependsOnID string) (string, error) {
	if strings.HasPrefix(dependsOnID, "external:") {
		return "depends_on_external", nil
	}
	var probe int
	err := r.runner.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", dependsOnID).Scan(&probe)
	switch {
	case err == nil:
		return "depends_on_wisp_id", nil
	case errors.Is(err, sql.ErrNoRows):
		return "depends_on_issue_id", nil
	case dberrors.IsTableNotExist(err):
		return "depends_on_issue_id", nil
	default:
		return "", fmt.Errorf("classify dep target %s: %w", dependsOnID, err)
	}
}

func (r *dependencySQLRepositoryImpl) Insert(ctx context.Context, dep *types.Dependency, actor string, opts domain.DepInsertOpts) error {
	if dep == nil {
		return errors.New("db: DependencySQLRepository.Insert: dep must not be nil")
	}
	if dep.IssueID == "" {
		return errors.New("db: DependencySQLRepository.Insert: IssueID must not be empty")
	}
	if dep.DependsOnID == "" {
		return errors.New("db: DependencySQLRepository.Insert: DependsOnID must not be empty")
	}
	if dep.IssueID == dep.DependsOnID {
		return fmt.Errorf("db: DependencySQLRepository.Insert: %s cannot depend on itself", dep.IssueID)
	}

	metadata := dep.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	table := pickDepTable(opts.UseWispsTable)

	var existingType string
	err := r.runner.QueryRowContext(ctx,
		//nolint:gosec // G201: table and depTargetExpr are hardcoded constants
		fmt.Sprintf("SELECT type FROM %s WHERE issue_id = ? AND %s = ?", table, depTargetExpr),
		dep.IssueID, dep.DependsOnID,
	).Scan(&existingType)
	switch {
	case err == nil:
		if existingType == string(dep.Type) {
			//nolint:gosec // G201: table and depTargetExpr are hardcoded constants
			if _, err := r.runner.ExecContext(ctx,
				fmt.Sprintf("UPDATE %s SET metadata = ? WHERE issue_id = ? AND %s = ?", table, depTargetExpr),
				metadata, dep.IssueID, dep.DependsOnID,
			); err != nil {
				return fmt.Errorf("db: DependencySQLRepository.Insert: refresh metadata: %w", err)
			}
			return nil
		}
		return fmt.Errorf("db: DependencySQLRepository.Insert: %s -> %s already exists with type %q (requested %q)",
			dep.IssueID, dep.DependsOnID, existingType, dep.Type)
	case errors.Is(err, sql.ErrNoRows):
	default:
		return fmt.Errorf("db: DependencySQLRepository.Insert: check existing: %w", err)
	}

	targetCol, err := r.pickDepTargetColumn(ctx, dep.DependsOnID)
	if err != nil {
		return fmt.Errorf("db: DependencySQLRepository.Insert: %w", err)
	}

	// Deterministic id keyed on (issue_id, target), the same derivation as the
	// embedded/issueops path, so server-mode (use-case) dependency creation stays
	// merge-safe across clones and works once the DEFAULT (UUID()) is dropped (#4259).
	//nolint:gosec // G201: table is one of two hardcoded constants; targetCol is from pickDepTargetColumn
	if _, err := r.runner.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, issue_id, %s, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, table, targetCol),
		depid.New(dep.IssueID, dep.DependsOnID), dep.IssueID, dep.DependsOnID, string(dep.Type),
		time.Now().UTC(), actor, metadata, dep.ThreadID,
	); err != nil {
		return fmt.Errorf("db: DependencySQLRepository.Insert: %w", err)
	}

	// is_blocked maintenance mirrors the classic AddDependencyInTx flow
	// (issueops/dependencies.go): the affected set expands the source by its
	// parent-child descendants (plus, for parent-child edges, waiters on the
	// target spawner), then a Mark pass propagates blocked state — or, for
	// parent-child adds (not monotonic: an already-closed child can satisfy an
	// any-children waits-for gate), a full mark/unmark Recompute. Skipping the
	// expansion left descendants stale when a blocking edge landed on their
	// ancestor (bd-6dnrw.44 item 3).
	srcIsWisp := opts.UseWispsTable
	var affectedIssues, affectedWisps []string
	var aerr error
	if srcIsWisp {
		affectedIssues, affectedWisps, aerr = issueops.AffectedByDepChangeForWispInTx(ctx, r.runner, dep.IssueID, dep.DependsOnID, dep.Type)
	} else {
		affectedIssues, affectedWisps, aerr = issueops.AffectedByDepChangeInTx(ctx, r.runner, dep.IssueID, dep.DependsOnID, dep.Type)
	}
	if aerr != nil {
		return fmt.Errorf("db: DependencySQLRepository.Insert: affected set: %w", aerr)
	}
	if dep.Type == types.DepBlocks || dep.Type == types.DepConditionalBlocks {
		if err := r.markDirectBlockedSource(ctx, dep.IssueID, srcIsWisp, dep.DependsOnID, targetCol); err != nil {
			return fmt.Errorf("db: DependencySQLRepository.Insert: mark is_blocked: %w", err)
		}
		affectedIssues, affectedWisps = issueops.RemoveSourceFromAffected(dep.IssueID, srcIsWisp, affectedIssues, affectedWisps)
	}
	if dep.Type == types.DepParentChild {
		if err := issueops.RecomputeIsBlockedInTx(ctx, r.runner, affectedIssues, affectedWisps); err != nil {
			return fmt.Errorf("db: DependencySQLRepository.Insert: recompute is_blocked: %w", err)
		}
		return nil
	}
	if err := issueops.MarkIsBlockedInTx(ctx, r.runner, affectedIssues, affectedWisps); err != nil {
		return fmt.Errorf("db: DependencySQLRepository.Insert: mark is_blocked (affected): %w", err)
	}
	return nil
}

// markDirectBlockedSource mirrors issueops.markDirectBlockingDependencySourceInTx:
// is_blocked is derived state, and ready-work queries filter on it directly
// (is_blocked = 0), so a blocking edge insert must set it on the source row
// while the target is still open. updated_at is pinned because recomputing
// derived state is not an edit.
func (r *dependencySQLRepositoryImpl) markDirectBlockedSource(ctx context.Context, source string, srcIsWisp bool, target, targetCol string) error {
	sourceTable := "issues"
	if srcIsWisp {
		sourceTable = "wisps"
	}
	var targetTable string
	switch targetCol {
	case "depends_on_issue_id":
		targetTable = "issues"
	case "depends_on_wisp_id":
		targetTable = "wisps"
	default:
		// External targets carry no local status to derive from.
		return nil
	}

	//nolint:gosec // G201: sourceTable/targetTable are hardcoded constants
	_, err := r.runner.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s s SET s.is_blocked = 1, s.updated_at = s.updated_at
		WHERE s.id = ?
		  AND s.is_blocked = 0
		  AND s.status <> 'closed' AND s.status <> 'pinned'
		  AND EXISTS (
		    SELECT 1 FROM %s t
		    WHERE t.id = ?
		      AND t.status <> 'closed' AND t.status <> 'pinned'
		  )
	`, sourceTable, targetTable), source, target)
	return err
}

func (r *dependencySQLRepositoryImpl) Delete(ctx context.Context, issueID, dependsOnID, actor string, opts domain.DepInsertOpts) (domain.DepDeleteResult, error) {
	if issueID == "" || dependsOnID == "" {
		return domain.DepDeleteResult{}, errors.New("db: DependencySQLRepository.Delete: issueID and dependsOnID must not be empty")
	}
	table := pickDepTable(opts.UseWispsTable)

	var depType string
	//nolint:gosec // G201: table and depTargetExpr are hardcoded constants
	err := r.runner.QueryRowContext(ctx,
		fmt.Sprintf("SELECT type FROM %s WHERE issue_id = ? AND %s = ?", table, depTargetExpr),
		issueID, dependsOnID,
	).Scan(&depType)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.DepDeleteResult{Found: false}, nil
	case err != nil:
		return domain.DepDeleteResult{}, fmt.Errorf("db: DependencySQLRepository.Delete: lookup type %s -> %s: %w", issueID, dependsOnID, err)
	}

	//nolint:gosec // G201: table and depTargetExpr are hardcoded constants
	if _, err := r.runner.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE issue_id = ? AND %s = ?", table, depTargetExpr),
		issueID, dependsOnID,
	); err != nil {
		return domain.DepDeleteResult{}, fmt.Errorf("db: DependencySQLRepository.Delete: %s -> %s: %w", issueID, dependsOnID, err)
	}

	dt := types.DependencyType(depType)
	var affectedIssues, affectedWisps []string
	var aerr error
	if opts.UseWispsTable {
		affectedIssues, affectedWisps, aerr = issueops.AffectedByDepChangeForWispInTx(ctx, r.runner, issueID, dependsOnID, dt)
	} else {
		affectedIssues, affectedWisps, aerr = issueops.AffectedByDepChangeInTx(ctx, r.runner, issueID, dependsOnID, dt)
	}
	if aerr != nil {
		return domain.DepDeleteResult{}, fmt.Errorf("db: DependencySQLRepository.Delete: affected set: %w", aerr)
	}
	if err := issueops.RecomputeIsBlockedInTx(ctx, r.runner, affectedIssues, affectedWisps); err != nil {
		return domain.DepDeleteResult{}, fmt.Errorf("db: DependencySQLRepository.Delete: recompute is_blocked: %w", err)
	}

	return domain.DepDeleteResult{Found: true, Type: dt, DependsOnID: dependsOnID}, nil
}

func (r *dependencySQLRepositoryImpl) HasCycle(ctx context.Context, issueID, dependsOnID string) (bool, error) {
	if issueID == "" || dependsOnID == "" {
		return false, errors.New("db: DependencySQLRepository.HasCycle: issueID and dependsOnID must not be empty")
	}

	var one int
	err := r.runner.QueryRowContext(ctx, `
		SELECT 1 FROM dependencies
		WHERE issue_id = ? AND depends_on_issue_id = ?
		  AND type IN ('blocks', 'conditional-blocks')
		LIMIT 1
	`, dependsOnID, issueID).Scan(&one)
	switch {
	case err == nil:
		return true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("db: DependencySQLRepository.HasCycle: direct probe (dependencies): %w", err)
	}
	err = r.runner.QueryRowContext(ctx, `
		SELECT 1 FROM wisp_dependencies
		WHERE issue_id = ? AND depends_on_issue_id = ?
		  AND type IN ('blocks', 'conditional-blocks')
		LIMIT 1
	`, dependsOnID, issueID).Scan(&one)
	switch {
	case err == nil:
		return true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return false, fmt.Errorf("db: DependencySQLRepository.HasCycle: direct probe (wisp_dependencies): %w", err)
	}

	var count int
	err = r.runner.QueryRowContext(ctx, `
		WITH RECURSIVE reachable(node) AS (
			SELECT ?
			UNION
			SELECT d.depends_on_issue_id FROM (
				SELECT issue_id, depends_on_issue_id, type FROM dependencies
				UNION ALL
				SELECT issue_id, depends_on_issue_id, type FROM wisp_dependencies
			) d
			JOIN reachable r ON d.issue_id = r.node
			WHERE d.type IN ('blocks', 'conditional-blocks')
			  AND d.depends_on_issue_id IS NOT NULL
		)
		SELECT COUNT(*) FROM reachable WHERE node = ?
	`, dependsOnID, issueID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("db: DependencySQLRepository.HasCycle: %w", err)
	}
	return count > 0, nil
}

func (r *dependencySQLRepositoryImpl) ListByIssueIDs(ctx context.Context, issueIDs []string, opts domain.DepListOpts) (domain.DepBulkResult, error) {
	result := domain.DepBulkResult{
		Outgoing: make(map[string][]*types.Dependency),
		Incoming: make(map[string][]*types.Dependency),
	}
	if len(issueIDs) == 0 {
		return result, nil
	}

	idPlaceholders, idArgs := buildInPlaceholders(issueIDs)
	typeWhere, typeArgs := buildTypeFilter(opts.Types)
	table := pickDepTable(opts.UseWispsTable)

	if opts.Direction == domain.DepDirectionBoth || opts.Direction == domain.DepDirectionOut {
		//nolint:gosec // G201: table and depSelectColumns are hardcoded
		q := fmt.Sprintf(
			`SELECT %s FROM %s WHERE issue_id IN (%s)%s ORDER BY issue_id`,
			depSelectColumns, table, idPlaceholders, typeWhere,
		)
		args := combineArgs(idArgs, typeArgs)
		if err := r.queryDeps(ctx, q, args, result.Outgoing, true); err != nil {
			return domain.DepBulkResult{}, fmt.Errorf("db: DependencySQLRepository.ListByIssueIDs (out): %w", err)
		}
	}

	if opts.Direction == domain.DepDirectionBoth || opts.Direction == domain.DepDirectionIn {
		//nolint:gosec // G201: table, depSelectColumns, depTargetExpr are hardcoded
		q := fmt.Sprintf(
			`SELECT %s FROM %s WHERE %s IN (%s)%s ORDER BY issue_id`,
			depSelectColumns, table, depTargetExpr, idPlaceholders, typeWhere,
		)
		args := combineArgs(idArgs, typeArgs)
		if err := r.queryDeps(ctx, q, args, result.Incoming, false); err != nil {
			return domain.DepBulkResult{}, fmt.Errorf("db: DependencySQLRepository.ListByIssueIDs (in): %w", err)
		}
	}

	return result, nil
}

func (r *dependencySQLRepositoryImpl) CountsByIssueIDs(ctx context.Context, issueIDs []string, opts domain.DepCountsOpts) (map[string]*types.DependencyCounts, error) {
	result := make(map[string]*types.DependencyCounts)
	if len(issueIDs) == 0 {
		return result, nil
	}
	for _, id := range issueIDs {
		result[id] = &types.DependencyCounts{}
	}

	idPlaceholders, idArgs := buildInPlaceholders(issueIDs)
	table := pickDepTable(opts.UseWispsTable)

	//nolint:gosec // G201: table is one of two hardcoded constants
	outQ := fmt.Sprintf(
		`SELECT issue_id, COUNT(*) FROM %s WHERE issue_id IN (%s) AND type = 'blocks' GROUP BY issue_id`,
		table, idPlaceholders,
	)
	if err := scanCounts(ctx, r.runner, outQ, idArgs, result, func(c *types.DependencyCounts, n int) { c.DependencyCount = n }); err != nil {
		return nil, fmt.Errorf("db: DependencySQLRepository.CountsByIssueIDs (out): %w", err)
	}

	//nolint:gosec // G201: table and depTargetExpr are hardcoded
	inQ := fmt.Sprintf(
		`SELECT %s AS depends_on_id, COUNT(*) FROM %s WHERE %s IN (%s) AND type = 'blocks' GROUP BY %s`,
		depTargetExpr, table, depTargetExpr, idPlaceholders, depTargetExpr,
	)
	if err := scanCounts(ctx, r.runner, inQ, idArgs, result, func(c *types.DependencyCounts, n int) { c.DependentCount = n }); err != nil {
		return nil, fmt.Errorf("db: DependencySQLRepository.CountsByIssueIDs (in): %w", err)
	}

	return result, nil
}

func (r *dependencySQLRepositoryImpl) GetBlockingInfo(ctx context.Context, issueIDs []string, opts domain.DepListOpts) (domain.BlockingInfo, error) {
	info := domain.BlockingInfo{
		BlockedBy: make(map[string][]string),
		Blocks:    make(map[string][]string),
		Parent:    make(map[string]string),
	}
	if len(issueIDs) == 0 {
		return info, nil
	}

	table := pickDepTable(opts.UseWispsTable)
	idPlaceholders, idArgs := buildInPlaceholders(issueIDs)

	//nolint:gosec // G201: table and depTargetExpr are hardcoded constants
	outQ := fmt.Sprintf(
		"SELECT issue_id, %s AS depends_on_id, type FROM %s WHERE issue_id IN (%s) AND type IN ('blocks', 'parent-child')",
		depTargetExpr, table, idPlaceholders,
	)
	outRows, err := r.scanBlockingRows(ctx, outQ, idArgs)
	if err != nil {
		return domain.BlockingInfo{}, fmt.Errorf("db: DependencySQLRepository.GetBlockingInfo: outbound: %w", err)
	}

	//nolint:gosec // G201: table and depTargetExpr are hardcoded constants
	inQ := fmt.Sprintf(
		"SELECT issue_id, %s AS depends_on_id, type FROM %s WHERE %s IN (%s) AND type = 'blocks'",
		depTargetExpr, table, depTargetExpr, idPlaceholders,
	)
	inRows, err := r.scanBlockingRows(ctx, inQ, idArgs)
	if err != nil {
		return domain.BlockingInfo{}, fmt.Errorf("db: DependencySQLRepository.GetBlockingInfo: inbound: %w", err)
	}

	statusIDs := make(map[string]struct{})
	for _, row := range outRows {
		statusIDs[row.dependsOnID] = struct{}{}
	}
	for _, row := range inRows {
		statusIDs[row.dependsOnID] = struct{}{}
	}
	statusByID, err := r.loadStatusByID(ctx, statusIDs)
	if err != nil {
		return domain.BlockingInfo{}, fmt.Errorf("db: DependencySQLRepository.GetBlockingInfo: status lookup: %w", err)
	}

	for _, row := range outRows {
		if statusByID[row.dependsOnID] == types.StatusClosed {
			continue
		}
		if row.depType == "parent-child" {
			info.Parent[row.issueID] = row.dependsOnID
		} else {
			info.BlockedBy[row.issueID] = append(info.BlockedBy[row.issueID], row.dependsOnID)
		}
	}
	for _, row := range inRows {
		if statusByID[row.dependsOnID] == types.StatusClosed {
			continue
		}
		info.Blocks[row.dependsOnID] = append(info.Blocks[row.dependsOnID], row.issueID)
	}

	return info, nil
}

func (r *dependencySQLRepositoryImpl) GetBlockingInfoAcrossIssuesAndWisps(ctx context.Context, issueIDs []string) (domain.BlockingInfo, error) {
	perm, err := r.GetBlockingInfo(ctx, issueIDs, domain.DepListOpts{UseWispsTable: false})
	if err != nil {
		return domain.BlockingInfo{}, err
	}
	wisp, err := r.GetBlockingInfo(ctx, issueIDs, domain.DepListOpts{UseWispsTable: true})
	if err != nil {
		if !dberrors.IsTableNotExist(err) {
			return domain.BlockingInfo{}, err
		}
		wisp = domain.BlockingInfo{
			BlockedBy: map[string][]string{},
			Blocks:    map[string][]string{},
			Parent:    map[string]string{},
		}
	}
	for k, v := range wisp.BlockedBy {
		perm.BlockedBy[k] = append(perm.BlockedBy[k], v...)
	}
	for k, v := range wisp.Blocks {
		perm.Blocks[k] = append(perm.Blocks[k], v...)
	}
	for k, v := range wisp.Parent {
		if _, ok := perm.Parent[k]; !ok {
			perm.Parent[k] = v
		}
	}
	return perm, nil
}

type blockingRow struct {
	issueID, dependsOnID, depType string
}

func (r *dependencySQLRepositoryImpl) scanBlockingRows(ctx context.Context, q string, args []any) ([]blockingRow, error) {
	rows, err := r.runner.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []blockingRow
	for rows.Next() {
		var row blockingRow
		if err := rows.Scan(&row.issueID, &row.dependsOnID, &row.depType); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *dependencySQLRepositoryImpl) loadStatusByID(ctx context.Context, idSet map[string]struct{}) (map[string]types.Status, error) {
	statusByID := make(map[string]types.Status, len(idSet))
	if len(idSet) == 0 {
		return statusByID, nil
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	placeholders, args := buildInPlaceholders(ids)
	sourceByID := make(map[string]string, len(idSet))
	for _, table := range []string{"issues", "wisps"} {
		//nolint:gosec // G201: table is a hardcoded constant
		q := fmt.Sprintf("SELECT id, status FROM %s WHERE id IN (%s)", table, placeholders)
		if err := r.scanStatusRows(ctx, q, args, table, statusByID, sourceByID); err != nil {
			return nil, err
		}
	}
	return statusByID, nil
}

func (r *dependencySQLRepositoryImpl) scanStatusRows(ctx context.Context, q string, args []any, table string, statusByID map[string]types.Status, sourceByID map[string]string) error {
	rows, err := r.runner.QueryContext(ctx, q, args...)
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return nil
		}
		return fmt.Errorf("status from %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var status types.Status
		if err := rows.Scan(&id, &status); err != nil {
			return fmt.Errorf("status from %s: scan: %w", table, err)
		}
		if existing, dup := sourceByID[id]; dup {
			return fmt.Errorf("status id %q exists in both %s and %s", id, existing, table)
		}
		sourceByID[id] = table
		statusByID[id] = status
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("status rows from %s: %w", table, err)
	}
	return nil
}

func (r *dependencySQLRepositoryImpl) queryDeps(ctx context.Context, q string, args []any, into map[string][]*types.Dependency, keyByIssueID bool) error {
	rows, err := r.runner.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var d types.Dependency
		var typ string
		var createdBy, metadata, threadID sql.NullString
		var createdAt sql.NullTime
		if err := rows.Scan(&d.IssueID, &d.DependsOnID, &typ, &createdAt, &createdBy, &metadata, &threadID); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		d.Type = types.DependencyType(typ)
		if createdAt.Valid {
			d.CreatedAt = createdAt.Time
		}
		if createdBy.Valid {
			d.CreatedBy = createdBy.String
		}
		if metadata.Valid && metadata.String != "" && metadata.String != "{}" {
			d.Metadata = metadata.String
		}
		if threadID.Valid {
			d.ThreadID = threadID.String
		}
		dd := d
		var key string
		if keyByIssueID {
			key = d.IssueID
		} else {
			key = d.DependsOnID
		}
		into[key] = append(into[key], &dd)
	}
	return rows.Err()
}

func scanCounts(ctx context.Context, runner Runner, q string, args []any, into map[string]*types.DependencyCounts, assign func(c *types.DependencyCounts, n int)) error {
	rows, err := runner.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		if c, ok := into[id]; ok {
			assign(c, n)
		}
	}
	return rows.Err()
}

func buildInPlaceholders[T ~string](values []T) (string, []any) {
	ph := make([]string, len(values))
	args := make([]any, len(values))
	for i, v := range values {
		ph[i] = "?"
		args[i] = string(v)
	}
	return strings.Join(ph, ","), args
}

func buildTypeFilter(depTypes []types.DependencyType) (string, []any) {
	if len(depTypes) == 0 {
		return "", nil
	}
	ph := make([]string, len(depTypes))
	args := make([]any, len(depTypes))
	for i, t := range depTypes {
		ph[i] = "?"
		args[i] = string(t)
	}
	return " AND type IN (" + strings.Join(ph, ",") + ")", args
}

func combineArgs(a, b []any) []any {
	out := make([]any, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func (r *dependencySQLRepositoryImpl) DeleteAllForIDs(ctx context.Context, ids []string, opts domain.DepInsertOpts) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	table := "dependencies"
	if opts.UseWispsTable {
		table = "wisp_dependencies"
	}
	total := 0
	for start := 0; start < len(ids); start += deleteBatchSize {
		end := start + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, 0, 2*len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args = append(args, id)
		}
		for _, id := range batch {
			args = append(args, id)
		}
		ph := strings.Join(placeholders, ",")
		//nolint:gosec // G201: table is one of two hardcoded constants; ? placeholders only.
		res, err := r.runner.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE issue_id IN (%s) OR %s IN (%s)", table, ph, issueops.DepTargetExpr, ph),
			args...)
		if err != nil {
			if opts.UseWispsTable && dberrors.IsTableNotExist(err) {
				return total, nil
			}
			return total, fmt.Errorf("db: DependencySQLRepository.DeleteAllForIDs from %s: %w", table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("db: DependencySQLRepository.DeleteAllForIDs rows affected: %w", err)
		}
		total += int(n)
	}
	return total, nil
}

func (r *dependencySQLRepositoryImpl) CountAllForIDs(ctx context.Context, ids []string, opts domain.DepCountsOpts) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	table := "dependencies"
	if opts.UseWispsTable {
		table = "wisp_dependencies"
	}
	total := 0
	for start := 0; start < len(ids); start += deleteBatchSize {
		end := start + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, 0, 2*len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args = append(args, id)
		}
		for _, id := range batch {
			args = append(args, id)
		}
		ph := strings.Join(placeholders, ",")
		var count int
		//nolint:gosec // G201: table is one of two hardcoded constants; ? placeholders only.
		err := r.runner.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE issue_id IN (%s) OR %s IN (%s)", table, ph, issueops.DepTargetExpr, ph),
			args...).Scan(&count)
		if err != nil {
			if opts.UseWispsTable && dberrors.IsTableNotExist(err) {
				return total, nil
			}
			return total, fmt.Errorf("db: DependencySQLRepository.CountAllForIDs from %s: %w", table, err)
		}
		total += count
	}
	return total, nil
}

func (r *dependencySQLRepositoryImpl) ListWithIssueMetadata(ctx context.Context, sourceID string, opts domain.DepListOpts) ([]*types.IssueWithDependencyMetadata, error) {
	var out []*types.IssueWithDependencyMetadata
	if opts.Direction == domain.DepDirectionOut || opts.Direction == domain.DepDirectionBoth {
		deps, err := issueops.GetDependenciesWithMetadataInTx(ctx, r.runner, sourceID)
		if err != nil {
			return nil, err
		}
		out = append(out, filterDepsByType(deps, opts.Types)...)
	}
	if opts.Direction == domain.DepDirectionIn || opts.Direction == domain.DepDirectionBoth {
		deps, err := issueops.GetDependentsWithMetadataInTx(ctx, r.runner, sourceID)
		if err != nil {
			return nil, err
		}
		out = append(out, filterDepsByType(deps, opts.Types)...)
	}
	return out, nil
}

func (r *dependencySQLRepositoryImpl) IterWithIssueMetadata(ctx context.Context, sourceID string, opts domain.DepListOpts) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	items, err := r.ListWithIssueMetadata(ctx, sourceID, opts)
	if err != nil {
		return nil, err
	}
	return storage.NewSliceIter(items), nil
}

func (r *dependencySQLRepositoryImpl) CountByID(ctx context.Context, sourceID string, opts domain.DepListOpts) (int64, error) {
	return issueops.CountDependencyEdgesInTx(ctx, r.runner, sourceID, opts.Direction, opts.Types)
}

func filterDepsByType(deps []*types.IssueWithDependencyMetadata, filter []types.DependencyType) []*types.IssueWithDependencyMetadata {
	if len(filter) == 0 {
		return deps
	}
	allowed := make(map[types.DependencyType]struct{}, len(filter))
	for _, t := range filter {
		allowed[t] = struct{}{}
	}
	out := make([]*types.IssueWithDependencyMetadata, 0, len(deps))
	for _, d := range deps {
		if _, ok := allowed[d.DependencyType]; ok {
			out = append(out, d)
		}
	}
	return out
}

func (r *dependencySQLRepositoryImpl) IsBlocked(ctx context.Context, issueID string, opts domain.DepListOpts) (bool, []string, error) {
	blocked, blockers, err := issueops.IsBlockedInTx(ctx, r.runner, issueID)
	if err != nil {
		return false, nil, fmt.Errorf("db: DependencySQLRepository.IsBlocked %s: %w", issueID, err)
	}
	return blocked, blockers, nil
}

func (r *dependencySQLRepositoryImpl) DetectCycles(ctx context.Context) ([][]*types.Issue, error) {
	out, err := issueops.DetectCyclesInTx(ctx, r.runner)
	if err != nil {
		return nil, fmt.Errorf("db: DependencySQLRepository.DetectCycles: %w", err)
	}
	return out, nil
}

func (r *dependencySQLRepositoryImpl) GetTree(ctx context.Context, rootID string, opts domain.DepTreeOpts) ([]*types.TreeNode, error) {
	if rootID == "" {
		return nil, errors.New("db: DependencySQLRepository.GetTree: rootID must not be empty")
	}
	if opts.Direction == domain.DepDirectionBoth {
		return nil, errors.New("db: DependencySQLRepository.GetTree: DepDirectionBoth not supported; callers must invoke once per direction and merge")
	}
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 50
	}
	reverse := opts.Direction == domain.DepDirectionIn
	out, err := issueops.GetDependencyTreeInTx(ctx, r.runner, rootID, maxDepth, opts.ShowAllPaths, reverse)
	if err != nil {
		return nil, fmt.Errorf("db: DependencySQLRepository.GetTree: %w", err)
	}
	return out, nil
}

func (r *dependencySQLRepositoryImpl) CycleThroughEdges(ctx context.Context, edges [][2]string) (string, error) {
	if len(edges) == 0 {
		return "", nil
	}
	graph := make(map[string][]string)
	if err := issueops.AppendBlockingGraphInTx(ctx, r.runner, []string{"dependencies"}, graph); err != nil {
		return "", fmt.Errorf("db: DependencySQLRepository.CycleThroughEdges: %w", err)
	}
	if err := issueops.AppendBlockingGraphInTx(ctx, r.runner, []string{"wisp_dependencies"}, graph); err != nil && !dberrors.IsTableNotExist(err) {
		return "", fmt.Errorf("db: DependencySQLRepository.CycleThroughEdges (wisps): %w", err)
	}
	return issueops.CycleThroughEdgesInGraph(graph, edges), nil
}

func (r *dependencySQLRepositoryImpl) GetDependencyRecordsForIssues(ctx context.Context, issueIDs []string) (map[string][]*types.Dependency, error) {
	if len(issueIDs) == 0 {
		return map[string][]*types.Dependency{}, nil
	}
	out, err := issueops.GetDependencyRecordsForIssuesInTx(ctx, r.runner, issueIDs)
	if err != nil {
		return nil, fmt.Errorf("db: DependencySQLRepository.GetDependencyRecordsForIssues: %w", err)
	}
	return out, nil
}

func (r *dependencySQLRepositoryImpl) GetWispDependencyRecordsForIDs(ctx context.Context, wispIDs []string) (map[string][]*types.Dependency, error) {
	if len(wispIDs) == 0 {
		return map[string][]*types.Dependency{}, nil
	}
	out, err := issueops.GetDependencyRecordsForIssuesFromTableInTx(ctx, r.runner, "wisp_dependencies", wispIDs)
	if err != nil {
		if dberrors.IsTableNotExist(err) {
			return map[string][]*types.Dependency{}, nil
		}
		return nil, fmt.Errorf("db: DependencySQLRepository.GetWispDependencyRecordsForIDs: %w", err)
	}
	return out, nil
}
