package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

type readyWorkPredicates struct {
	whereSQL string
	// whereArgs binds only the WHERE placeholders (no ORDER BY params); it is
	// what a bare COUNT(*) over the ready predicate needs. args carries the
	// WHERE params followed by the ORDER BY params for the full page query.
	whereArgs        []interface{}
	orderBySQL       string
	limitSQL         string
	args             []interface{}
	deferredChildIDs []string
}

func readyWorkPageSize(limit int) int {
	if limit <= 0 {
		return 0
	}
	const minPageSize = 100
	if limit < minPageSize {
		return minPageSize
	}
	return limit
}

func buildReadyWorkOrder(policy types.SortPolicy) sqlbuild.ReadyWorkOrder {
	return sqlbuild.BuildReadyWorkOrder(policy, "created_at", "priority")
}

// buildReadyWorkPredicates computes the ID sets the ready-work WHERE clause
// needs (children of deferred parents, parent descendants), then delegates
// the clause text to sqlbuild so both stacks share ready semantics.
func buildReadyWorkPredicates(ctx context.Context, tx DBTX, filter types.WorkFilter, tables FilterTables) (*readyWorkPredicates, error) {
	var inputs sqlbuild.ReadyWorkWhereInputs
	if !filter.IncludeDeferred {
		deferredChildIDs, dcErr := getChildrenOfDeferredParentsInTx(ctx, tx)
		if dcErr != nil {
			return nil, fmt.Errorf("get ready work: compute deferred parent children: %w", dcErr)
		}
		inputs.DeferredChildIDs = deferredChildIDs
	}
	// Parent filtering: return all transitive descendants of parentID.
	// GH#3396: previously was a one-hop subquery against dependencies, so
	// grandchildren were silently dropped despite the help text and
	// WorkFilter.ParentID godoc both promising "descendants (recursive)".
	if filter.ParentID != nil {
		descendantIDs, descErr := GetDescendantIDsInTx(ctx, tx, *filter.ParentID, 0)
		if descErr != nil {
			return nil, fmt.Errorf("get parent descendants: %w", descErr)
		}
		inputs.ParentDescendantIDs = descendantIDs
	}

	whereSQL, whereArgs, err := sqlbuild.BuildReadyWorkWhere(filter, tables, inputs)
	if err != nil {
		return nil, err
	}

	orderBy := buildReadyWorkOrder(filter.SortPolicy)
	args := make([]interface{}, 0, len(whereArgs)+len(orderBy.Args))
	args = append(args, whereArgs...)
	args = append(args, orderBy.Args...)

	var limitSQL string
	if eff := EffectiveSearchLimit(filter.Limit, filter.MaxRows); eff > 0 {
		limitSQL = fmt.Sprintf("LIMIT %d", eff)
	}

	return &readyWorkPredicates{
		whereSQL:         whereSQL,
		whereArgs:        whereArgs,
		orderBySQL:       orderBy.SQL,
		limitSQL:         limitSQL,
		args:             args,
		deferredChildIDs: inputs.DeferredChildIDs,
	}, nil
}

//nolint:gosec // G201: whereSQL/orderBySQL built from hardcoded strings and ? placeholders
func GetReadyWorkInTx(
	ctx context.Context,
	tx DBTX,
	filter types.WorkFilter,
) ([]*types.Issue, error) {
	preds, err := buildReadyWorkPredicates(ctx, tx, filter, IssuesFilterTables)
	if err != nil {
		return nil, err
	}

	//nolint:gosec // G201: fragments are hardcoded; only ? placeholders carry user input.
	query := fmt.Sprintf(`
		SELECT id FROM issues
		%s
		%s
		%s
	`, preds.whereSQL, preds.orderBySQL, preds.limitSQL)

	issueIDs, err := queryReadyIssueIDPage(ctx, tx, query, preds.args)
	if err != nil {
		return nil, err
	}

	issues, err := GetIssuesByIDsInTx(ctx, tx, issueIDs, nil)
	if err != nil {
		return nil, fmt.Errorf("get ready work: fetch issues: %w", err)
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}
	ordered := make([]*types.Issue, 0, len(issueIDs))
	for _, id := range issueIDs {
		if iss, ok := issueMap[id]; ok {
			ordered = append(ordered, iss)
		}
	}

	wisps, wErr := getReadyWispsInTx(ctx, tx, filter, preds.deferredChildIDs)
	if wErr != nil {
		return nil, wErr
	}
	if len(wisps) > 0 {
		ordered = mergeReadyWisps(ordered, wisps, filter)
	}

	// Apply the defensive cap on the row count returned to the caller.
	// LIMIT cap+1 was issued above so a count of cap+1 indicates overage.
	if err := EnforceMaxRowsCap(len(ordered), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, err
	}

	return ordered, nil
}

func mergeReadyWisps(ordered []*types.Issue, wisps []*types.Issue, filter types.WorkFilter) []*types.Issue {
	// Prefer the canonical wisp record when an ID exists in both tables (be-iabdi).
	wispByID := make(map[string]*types.Issue, len(wisps))
	for _, w := range wisps {
		wispByID[w.ID] = w
	}
	var kept []*types.Issue
	for _, issue := range ordered {
		if wispByID[issue.ID] == nil {
			kept = append(kept, issue)
		}
	}
	kept = append(kept, wisps...)
	sortReadyIssues(kept, filter.SortPolicy)
	if filter.Limit > 0 && len(kept) > filter.Limit {
		kept = kept[:filter.Limit]
	}
	return kept
}

func getReadyWispsInTx(ctx context.Context, tx DBTX, filter types.WorkFilter, deferredChildIDs []string) ([]*types.Issue, error) {
	empty, err := wispsTableEmptyOrMissingInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("search wisps (ready work): probe: %w", err)
	}
	if empty {
		return nil, nil
	}

	wispFilter := readyWorkWispIssueFilter(filter)
	if filter.Limit <= 0 {
		wispFilter.Limit = 0
		wisps, err := searchTableInTxT(ctx, tx, "", wispFilter, WispsFilterTables, issueProjection)
		if err != nil {
			if isTableNotExistError(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("search wisps (ready work): %w", err)
		}
		return filterReadyWispsInTx(ctx, tx, filter, wisps, deferredChildIDs)
	}

	pageSize := readyWorkPageSize(filter.Limit)
	orderBy := buildReadyWorkOrder(filter.SortPolicy)
	ready := make([]*types.Issue, 0, filter.Limit)
	for offset := 0; len(ready) < filter.Limit; offset += pageSize {
		pageIDs, err := queryReadyWispIssueIDPage(ctx, tx, wispFilter, !filter.IncludeDeferred, orderBy, pageSize, offset)
		if err != nil {
			if isTableNotExistError(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("search wisps (ready work): %w", err)
		}
		if len(pageIDs) == 0 {
			break
		}

		pageWisps, err := getWispIssuesByIDsInOrderInTx(ctx, tx, pageIDs)
		if err != nil {
			return nil, fmt.Errorf("search wisps (ready work): %w", err)
		}
		pageReady, err := filterReadyWispsInTx(ctx, tx, filter, pageWisps, deferredChildIDs)
		if err != nil {
			return nil, err
		}
		for _, wisp := range pageReady {
			ready = append(ready, wisp)
			if len(ready) >= filter.Limit {
				break
			}
		}
		if len(pageIDs) < pageSize {
			break
		}
	}
	return ready, nil
}

func queryReadyWispIssueIDPage(ctx context.Context, tx DBTX, filter types.IssueFilter, excludeDeferred bool, orderBy sqlbuild.ReadyWorkOrder, limit, offset int) ([]string, error) {
	plan := sqlbuild.BuildLabelDrivenSearch(filter, WispsFilterTables)
	whereClauses, args, err := BuildIssueFilterClauses("", plan.Filter, WispsFilterTables)
	if err != nil {
		return nil, err
	}
	whereClauses, args = plan.MergeInto(whereClauses, args)
	if excludeDeferred {
		whereClauses = append(whereClauses, "(defer_until IS NULL OR defer_until <= UTC_TIMESTAMP())")
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	selectSQL := "SELECT "
	if plan.Distinct {
		selectSQL = "SELECT DISTINCT "
	}
	args = append(args, orderBy.Args...)
	//nolint:gosec // G201: SQL fragments are fixed table/column names and parameterized filters; limit/offset are ints.
	query := fmt.Sprintf(`%sid FROM %s %s %s LIMIT %d OFFSET %d`,
		selectSQL, plan.FromSQL, whereSQL, orderBy.SQL, limit, offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search wisps: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("search wisps: scan id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search wisps: rows: %w", err)
	}
	return ids, nil
}

func getWispIssuesByIDsInOrderInTx(ctx context.Context, tx DBTX, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	wispSet := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wispSet[id] = struct{}{}
	}
	issues, err := GetIssuesByIDsInTx(ctx, tx, ids, wispSet)
	if err != nil {
		return nil, err
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, issue := range issues {
		issueMap[issue.ID] = issue
	}
	ordered := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		if issue, ok := issueMap[id]; ok {
			ordered = append(ordered, issue)
		}
	}
	return ordered, nil
}

func readyWorkExcludeTypes(extra []types.IssueType) []types.IssueType {
	return sqlbuild.ReadyWorkExcludeTypes(extra)
}

func readyWorkWispIssueFilter(filter types.WorkFilter) types.IssueFilter {
	pinnedFalse := false
	wispFilter := types.IssueFilter{
		Priority:       filter.Priority,
		Labels:         filter.Labels,
		LabelsAny:      filter.LabelsAny,
		ExcludeLabels:  filter.ExcludeLabels,
		Limit:          filter.Limit,
		MolType:        filter.MolType,
		WispType:       filter.WispType,
		Pinned:         &pinnedFalse,
		MetadataFields: filter.MetadataFields,
		HasMetadataKey: filter.HasMetadataKey,
		// be-x42v.4 follow-up (review SHOULD-FIX 8): without this,
		// getReadyWispsInTx's unbounded (Limit<=0) branch called
		// searchTableInTxT with MaxRows=0, so EffectiveSearchLimit emitted
		// no SQL LIMIT at all — the entire wisps table matching the
		// predicate was scanned and hydrated before GetReadyWorkInTx's
		// post-merge EnforceMaxRowsCap ever ran. Propagating the cap here
		// lets EffectiveSearchLimit bound that query to cap+1 up front.
		MaxRows:       filter.MaxRows,
		MaxRowsSource: filter.MaxRowsSource,
	}
	if filter.Status != "" {
		s := filter.Status
		wispFilter.Status = &s
	} else {
		wispFilter.Statuses = []types.Status{types.StatusOpen, types.StatusInProgress}
	}
	if filter.Type != "" {
		t := types.IssueType(filter.Type)
		wispFilter.IssueType = &t
	} else {
		wispFilter.ExcludeTypes = readyWorkExcludeTypes(filter.ExcludeTypes)
	}
	if filter.Unassigned {
		wispFilter.NoAssignee = true
	} else if filter.Assignee != nil {
		wispFilter.Assignee = filter.Assignee
	}
	if filter.MoleculeID != "" {
		moleculeID := filter.MoleculeID
		wispFilter.ParentID = &moleculeID
	}
	if !filter.IncludeEphemeral {
		ephFalse := false
		wispFilter.Ephemeral = &ephFalse
	}
	return wispFilter
}

func filterReadyWispsInTx(ctx context.Context, tx DBTX, filter types.WorkFilter, wisps []*types.Issue, deferredChildIDs []string) ([]*types.Issue, error) {
	if len(wisps) == 0 {
		return wisps, nil
	}

	wispIDs := make([]string, 0, len(wisps))
	for _, wisp := range wisps {
		wispIDs = append(wispIDs, wisp.ID)
	}

	excluded := make(map[string]struct{})
	if filter.ParentID != nil {
		parentID := *filter.ParentID
		descendantIDs, err := GetDescendantIDsInTx(ctx, tx, parentID, 0)
		if err != nil {
			return nil, fmt.Errorf("get wisp parent descendants: %w", err)
		}
		descendantSet := make(map[string]struct{}, len(descendantIDs))
		for _, id := range descendantIDs {
			descendantSet[id] = struct{}{}
		}
		parentedSet, err := getParentedIDSetInTx(ctx, tx, wispIDs)
		if err != nil {
			return nil, err
		}
		for _, wisp := range wisps {
			if _, ok := descendantSet[wisp.ID]; ok {
				continue
			}
			if strings.HasPrefix(wisp.ID, parentID+".") {
				if _, hasParent := parentedSet[wisp.ID]; !hasParent {
					continue
				}
			}
			excluded[wisp.ID] = struct{}{}
		}
	}

	if !filter.IncludeDeferred {
		now := time.Now().UTC()
		for _, wisp := range wisps {
			if wisp.DeferUntil != nil && wisp.DeferUntil.After(now) {
				excluded[wisp.ID] = struct{}{}
			}
		}
		for _, id := range deferredChildIDs {
			excluded[id] = struct{}{}
		}
	}

	for start := 0; start < len(wispIDs); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(wispIDs) {
			end = len(wispIDs)
		}
		placeholders, args := buildSQLInClause(wispIDs[start:end])
		//nolint:gosec // G201: only IN-clause placeholders are formatted in.
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
			SELECT id FROM wisps WHERE id IN (%s) AND is_blocked = 1
		`, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("get ready work: filter blocked wisps: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan blocked wisp: %w", err)
			}
			excluded[id] = struct{}{}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("blocked wisp rows: %w", err)
		}
	}

	ready := wisps[:0]
	for _, wisp := range wisps {
		if wisp.Pinned {
			continue
		}
		if _, skip := excluded[wisp.ID]; skip {
			continue
		}
		ready = append(ready, wisp)
	}
	return ready, nil
}

func sortReadyIssues(issues []*types.Issue, policy types.SortPolicy) {
	recentCutoff := time.Now().UTC().Add(-48 * time.Hour)
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		switch policy {
		case types.SortPolicyOldest:
			return issueCreatedBefore(a, b)
		case types.SortPolicyPriority:
			return issuePriorityBefore(a, b)
		case types.SortPolicyHybrid, "":
			aRecent := !a.CreatedAt.Before(recentCutoff)
			bRecent := !b.CreatedAt.Before(recentCutoff)
			if aRecent != bRecent {
				return aRecent
			}
			if aRecent && a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			return issueCreatedBefore(a, b)
		default:
			return issuePriorityBefore(a, b)
		}
	})
}

func issuePriorityBefore(a, b *types.Issue) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

func issueCreatedBefore(a, b *types.Issue) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

func queryReadyIssueIDPage(ctx context.Context, tx DBTX, query string, args []interface{}) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get ready work: %w", err)
	}

	var issueIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("get ready work: scan id: %w", err)
		}
		issueIDs = append(issueIDs, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get ready work: rows: %w", err)
	}
	return issueIDs, nil
}

// getChildrenOfDeferredParentsInTx returns IDs of issues whose parent has a
// future defer_until. Works within an existing transaction.
//
//nolint:gosec // G201: depTable is selected from a hardcoded list below.
func getChildrenOfDeferredParentsInTx(ctx context.Context, tx DBTX) ([]string, error) {
	hasDeferredParent := false
	for _, issueTable := range []string{"issues", "wisps"} {
		//nolint:gosec // G201: issueTable is hardcoded to "issues" or "wisps"
		var exists int
		err := tx.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT 1 FROM %s
			WHERE defer_until IS NOT NULL
			  AND defer_until > UTC_TIMESTAMP()
			LIMIT 1
		`, issueTable)).Scan(&exists)
		if err == nil {
			hasDeferredParent = true
			break
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if issueTable == "wisps" && isTableNotExistError(err) {
			continue
		}
		return nil, fmt.Errorf("deferred parents: check future-deferred parents from %s: %w", issueTable, err)
	}
	if !hasDeferredParent {
		return nil, nil
	}

	var childIDs []string
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		for _, issueTable := range []string{"issues", "wisps"} {
			targetCol := "depends_on_issue_id"
			if issueTable == "wisps" {
				targetCol = "depends_on_wisp_id"
			}
			rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
				SELECT dep.issue_id
				FROM %s dep
				JOIN %s parent ON parent.id = dep.%s
				WHERE dep.type = 'parent-child'
				  AND parent.defer_until IS NOT NULL
				  AND parent.defer_until > UTC_TIMESTAMP()
			`, depTable, issueTable, targetCol))
			if err != nil {
				if depTable == "wisp_dependencies" && isTableNotExistError(err) {
					break
				}
				if issueTable == "wisps" && isTableNotExistError(err) {
					continue
				}
				return nil, fmt.Errorf("deferred parents: get deferred children from %s/%s: %w", depTable, issueTable, err)
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("deferred parents: scan deferred child from %s/%s: %w", depTable, issueTable, err)
				}
				childIDs = append(childIDs, id)
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("deferred parents: child rows from %s/%s: %w", depTable, issueTable, err)
			}
		}
	}
	return childIDs, nil
}

//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
func getParentedIDSetInTx(ctx context.Context, tx DBTX, issueIDs []string) (map[string]struct{}, error) {
	parented := make(map[string]struct{})
	if len(issueIDs) == 0 {
		return parented, nil
	}
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		for start := 0; start < len(issueIDs); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(issueIDs) {
				end = len(issueIDs)
			}
			placeholders, args := buildSQLInClause(issueIDs[start:end])
			query := fmt.Sprintf(`
				SELECT issue_id FROM %s
				WHERE type = 'parent-child' AND issue_id IN (%s)
			`, depTable, placeholders)
			rows, err := tx.QueryContext(ctx, query, args...)
			if err != nil {
				if depTable == "wisp_dependencies" && isTableNotExistError(err) {
					break
				}
				return nil, fmt.Errorf("get parented IDs from %s: %w", depTable, err)
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("get parented IDs: scan: %w", err)
				}
				parented[id] = struct{}{}
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("get parented IDs: rows from %s: %w", depTable, err)
			}
		}
	}
	return parented, nil
}

// buildSQLInClause builds a parameterized IN clause from a slice of IDs.
func buildSQLInClause(ids []string) (string, []interface{}) {
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}
