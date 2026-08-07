package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// deleteBatchSize controls the maximum number of IDs per IN-clause query
// for delete operations. Kept small to avoid large IN-clause queries.
const deleteBatchSize = 50

// maxRecursiveResults is the safety limit for the total number of issues
// discovered during recursive dependent traversal.
const maxRecursiveResults = 10000

//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func DeleteIssueInTx(ctx context.Context, tx *sql.Tx, id string) error {
	isWisp := IsActiveWispInTx(ctx, tx, id)

	var deletedIssues, deletedWisps []string
	if isWisp {
		deletedWisps = []string{id}
	} else {
		deletedIssues = []string{id}
	}
	affectedIssues, affectedWisps, aerr := AffectedByDeletionInTx(ctx, tx, deletedIssues, deletedWisps)
	if aerr != nil {
		return fmt.Errorf("affected by delete for %s: %w", id, aerr)
	}

	if err := deleteIssueRowInTx(ctx, tx, id, isWisp); err != nil {
		return err
	}

	if err := RecomputeIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
		return fmt.Errorf("recompute is_blocked after delete for %s: %w", id, err)
	}

	return nil
}

//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func deleteIssueRowInTx(ctx context.Context, tx *sql.Tx, id string, isWisp bool) error {
	issueTable, _, _, _ := WispTableRouting(isWisp)
	result, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", issueTable), id)
	if err != nil {
		return fmt.Errorf("delete issue from %s: %w", issueTable, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rows affected: %w", err)
	}
	if rows == 0 {
		// Wrap the sentinel so callers can errors.Is(..., storage.ErrNotFound),
		// matching GetIssue/UpdateIssue. The storage conformance suite asserts
		// this parity across not-found paths.
		return fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if isWisp {
		if err := DeleteWispFromDependenciesInTx(ctx, tx, id); err != nil {
			return err
		}
	} else if err := DeleteLeaseInTx(ctx, tx, id); err != nil {
		// A deleted issue holds no lease.
		return err
	}
	return nil
}

// DeletionSet is the EXACT set of rows one delete removes, split by the plane
// each row lives in.
//
// IT EXISTS BECAUSE THE SET USED TO BE COMPUTED TWICE, from different roots, so
// `bd delete <wisp> --cascade` left a durable row reachable only through that
// wisp alive and then rewrote its neighbors' text to say `[deleted:<id>]` about
// it. The neighborhood read, the deletion and the reference rewrite all take
// THIS value.
type DeletionSet struct {
	// WispIDs and RegularIDs partition All by plane. The two tiers are deleted
	// through different tables and their associated rows counted from
	// different ones, so the split is carried rather than recomputed.
	WispIDs    []string
	RegularIDs []string
	// All is the whole set in one slice — what a caller scopes a neighborhood
	// read or a citation rewrite to, and what nothing else may recompute.
	All []string
}

// ResolveDeletionSetInTx decides WHICH rows a delete removes: the named ids,
// plus — under cascade — the transitive closure of everything that depends on
// them, in BOTH planes.
//
// THE CASCADE IS ROOTED AT EVERY NAMED ID, WISPS INCLUDED. Rooting it at the
// durable half is what made `bd wisp gc` (which hardcodes cascade) silently
// under-delete. It does not read the caller's slice destructively either: the
// non-cascade set is a copy, because DeleteRequest promises IDs is never
// sorted in place.
func ResolveDeletionSetInTx(ctx context.Context, tx DBTX, ids []string, cascade bool) (DeletionSet, error) {
	all := append([]string(nil), ids...)
	if cascade {
		closure, err := FindAllDependentsInTx(ctx, tx, ids)
		if err != nil {
			return DeletionSet{}, fmt.Errorf("expand cascade: %w", err)
		}
		all = workapi.SortedDeleteIDs(closure)
	}
	if len(all) == 0 {
		return DeletionSet{}, nil
	}
	wispIDs, regularIDs, err := PartitionWispIDsInTx(ctx, tx, all)
	if err != nil {
		return DeletionSet{}, fmt.Errorf("partition delete ids: %w", err)
	}
	return DeletionSet{WispIDs: wispIDs, RegularIDs: regularIDs, All: all}, nil
}

func DeleteIssuesInTx(ctx context.Context, tx *sql.Tx, ids []string, cascade bool, force bool, dryRun bool) (*types.DeleteIssuesResult, error) {
	if len(ids) == 0 {
		return &types.DeleteIssuesResult{}, nil
	}

	set, err := ResolveDeletionSetInTx(ctx, tx, ids, cascade)
	if err != nil {
		return nil, err
	}

	var orphaned []string
	if !cascade {
		// The guard here is the STORAGE SEAM's, and it stays durable-only: the
		// server-backed store peels wisps off before it ever reaches this
		// function (dolt/issues.go DeleteIssues), so widening it would make the
		// embedded store refuse where the server-backed one cannot. The ROLE's
		// guard, which does cover both planes, is in DeleteInTx.
		idSet := make(map[string]bool, len(ids))
		for _, id := range ids {
			idSet[id] = true
		}
		// One scan of the dependency planes answers both modes: the guard needs
		// to know WHICH id is blocked, and the forced path needs the union of
		// what it orphans.
		external, err := ExternalDependentsBySourceInTx(ctx, tx, set.RegularIDs, idSet)
		if err != nil {
			return nil, fmt.Errorf("get dependents: %w", err)
		}
		if !force {
			for _, id := range set.RegularIDs {
				if deps := external[id]; len(deps) > 0 {
					return &types.DeleteIssuesResult{OrphanedIssues: deps},
						&publicops.DependentsOutsideRequestError{IssueID: id, Dependents: deps}
				}
			}
		} else {
			orphans := make(map[string]bool)
			for _, deps := range external {
				for _, id := range deps {
					orphans[id] = true
				}
			}
			orphaned = workapi.SortedDeleteIDs(orphans)
		}
	}

	result, err := DeleteResolvedSetInTx(ctx, tx, set, dryRun)
	if err != nil {
		return nil, err
	}
	result.OrphanedIssues = orphaned
	return result, nil
}

// DeleteResolvedSetInTx deletes EXACTLY set — no expansion, no re-partition,
// no guard — and reports the associated rows that went with it.
//
// It is split out of DeleteIssuesInTx so the role body can read the
// neighborhood BEFORE the delete and rewrite it AFTER against the SAME
// DeletionSet the delete was handed.
//
//nolint:gosec // G201: inClause contains only ? placeholders
func DeleteResolvedSetInTx(ctx context.Context, tx *sql.Tx, set DeletionSet, dryRun bool) (*types.DeleteIssuesResult, error) {
	result := &types.DeleteIssuesResult{}
	if len(set.All) == 0 {
		return result, nil
	}

	deletedSet := make(map[string]bool, len(set.All))
	for _, id := range set.All {
		deletedSet[id] = true
	}

	var depsCount, labelsCount, eventsCount int
	var err error
	if depsCount, err = CountRowsForIssueIDsInTx(ctx, tx, "dependencies", set.RegularIDs); err != nil {
		return nil, fmt.Errorf("count dependencies: %w", err)
	}
	wispDepsCount, err := CountRowsForIssueIDsInTx(ctx, tx, "wisp_dependencies", set.WispIDs)
	if err != nil {
		return nil, fmt.Errorf("count wisp dependencies: %w", err)
	}
	depsCount += wispDepsCount

	if labelsCount, err = CountRowsForIssueIDsInTx(ctx, tx, "labels", set.RegularIDs); err != nil {
		return nil, fmt.Errorf("count labels: %w", err)
	}
	wispLabelsCount, err := CountRowsForIssueIDsInTx(ctx, tx, "wisp_labels", set.WispIDs)
	if err != nil {
		return nil, fmt.Errorf("count wisp labels: %w", err)
	}
	labelsCount += wispLabelsCount

	if eventsCount, err = CountRowsForIssueIDsInTx(ctx, tx, "events", set.RegularIDs); err != nil {
		return nil, fmt.Errorf("count events: %w", err)
	}
	wispEventsCount, err := CountRowsForIssueIDsInTx(ctx, tx, "wisp_events", set.WispIDs)
	if err != nil {
		return nil, fmt.Errorf("count wisp events: %w", err)
	}
	eventsCount += wispEventsCount

	for i := 0; i < len(set.All); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(set.All) {
			end = len(set.All)
		}
		batch := set.All[i:end]
		batchInClause, batchArgs := buildSQLInClause(batch)

		for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
			rows, err := tx.QueryContext(ctx,
				fmt.Sprintf(`SELECT issue_id FROM %s WHERE %s`, depTable, depTargetIn("", batchInClause)),
				batchArgs...)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					continue
				}
				return nil, fmt.Errorf("count inbound dependencies from %s: %w", depTable, err)
			}
			for rows.Next() {
				var issID string
				if err := rows.Scan(&issID); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan inbound dependency: %w", err)
				}
				if !deletedSet[issID] {
					depsCount++
				}
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("iterate inbound dependencies from %s: %w", depTable, err)
			}
		}
	}

	result.DependenciesCount = depsCount
	result.LabelsCount = labelsCount
	result.EventsCount = eventsCount
	result.DeletedCount = len(set.RegularIDs) + len(set.WispIDs)

	if dryRun {
		return result, nil
	}

	affectedIssues, affectedWisps, aerr := AffectedByDeletionInTx(ctx, tx, set.RegularIDs, set.WispIDs)
	if aerr != nil {
		return nil, fmt.Errorf("affected by batch delete: %w", aerr)
	}

	for _, id := range set.WispIDs {
		if err := deleteIssueRowInTx(ctx, tx, id, true); err != nil {
			return nil, fmt.Errorf("delete wisp %s: %w", id, err)
		}
	}

	totalRegularsDeleted := 0
	for i := 0; i < len(set.RegularIDs); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(set.RegularIDs) {
			end = len(set.RegularIDs)
		}
		batch := set.RegularIDs[i:end]
		batchInClause, batchArgs := buildSQLInClause(batch)

		deleteResult, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM issues WHERE id IN (%s)`, batchInClause),
			batchArgs...)
		if err != nil {
			return nil, fmt.Errorf("delete issues: %w", err)
		}
		rowsAffected, _ := deleteResult.RowsAffected()
		totalRegularsDeleted += int(rowsAffected)

		// Deleted issues hold no leases.
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM leases WHERE issue_id IN (%s)`, batchInClause),
			batchArgs...); err != nil {
			return nil, fmt.Errorf("delete leases: %w", err)
		}
	}
	result.DeletedCount = totalRegularsDeleted + len(set.WispIDs)

	if err := RecomputeIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
		return nil, fmt.Errorf("recompute is_blocked after batch delete: %w", err)
	}

	return result, nil
}

// findAllDependentsRecursiveInTx finds all issues that depend on the given
// issues, recursively. Uses batched IN-clause queries. Traversal is capped
// at maxRecursiveResults total discovered IDs.
//
//nolint:gosec // G201: inClause contains only ? placeholders
func FindAllDependentsInTx(ctx context.Context, tx DBTX, ids []string) (map[string]bool, error) {
	result := make(map[string]bool)
	for _, id := range ids {
		result[id] = true
	}

	toProcess := make([]string, len(ids))
	copy(toProcess, ids)

	for len(toProcess) > 0 {
		if len(result) > maxRecursiveResults {
			return nil, fmt.Errorf("cascade traversal discovered over %d issues; aborting to prevent runaway deletion", maxRecursiveResults)
		}
		batchEnd := deleteBatchSize
		if batchEnd > len(toProcess) {
			batchEnd = len(toProcess)
		}
		batch := toProcess[:batchEnd]
		toProcess = toProcess[batchEnd:]

		inClause, args := buildSQLInClause(batch)
		for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
			rows, err := tx.QueryContext(ctx,
				fmt.Sprintf(`SELECT issue_id FROM %s WHERE %s`, depTable, depTargetIn("", inClause)),
				args...)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					continue
				}
				return nil, fmt.Errorf("query dependents for batch from %s: %w", depTable, err)
			}

			for rows.Next() {
				var depID string
				if err := rows.Scan(&depID); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan dependent: %w", err)
				}
				if !result[depID] {
					result[depID] = true
					toProcess = append(toProcess, depID)
				}
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("iterate dependents for batch from %s: %w", depTable, err)
			}
		}
	}

	return result, nil
}

//nolint:gosec // G201: table is selected by callers from fixed issue/wisp auxiliary tables.
func CountRowsForIssueIDsInTx(ctx context.Context, tx DBTX, table string, ids []string) (int, error) {
	total := 0
	for i := 0; i < len(ids); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		inClause, args := buildSQLInClause(ids[i:end])
		var count int
		if err := tx.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE issue_id IN (%s)`, table, inClause),
			args...).Scan(&count); err != nil {
			if optionalBlockedTable(table) && isTableNotExistError(err) {
				continue
			}
			return 0, err
		}
		total += count
	}
	return total, nil
}
