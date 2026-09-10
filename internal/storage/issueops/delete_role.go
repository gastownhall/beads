package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// DeleteInTx is the store-backed body behind issueops.Deleter: the whole of
// `bd delete` from the existence probe to the reference rewrite, inside ONE
// transaction.
//
// It lives here rather than in an importable internal/workapi/store<role>
// package for the reason SweepInTx does: the work is several reads and several
// writes that must see one snapshot.
//
// It assumes a request already refused by workapi.ValidateDeleteRequest and
// already normalized by workapi.NormalizeDeleteIDs. The accessors do both
// BEFORE opening a transaction, so a malformed request costs no database work.
//
// THE REWRITE IS INSIDE THE TRANSACTION. A route that deleted the rows in one
// transaction and rewrote the neighbors' text afterwards left, on a failure
// between the two, a workspace whose rows were gone and whose descriptions
// still cited them.
func DeleteInTx(ctx context.Context, tx *sql.Tx, req publicops.DeleteRequest) (publicops.DeleteResult, error) {
	ids := req.IDs
	result := publicops.DeleteResult{DryRun: req.DryRun}

	// The existence probe comes FIRST, so `bd delete typo real` reports the
	// typo rather than whatever the graph says about the id that resolved.
	wispSet, err := WispIDSetInTx(ctx, tx, ids)
	if err != nil {
		return publicops.DeleteResult{}, fmt.Errorf("delete: classify planes: %w", err)
	}
	found, err := GetIssuesByIDsInTx(ctx, tx, ids, wispSet)
	if err != nil {
		return publicops.DeleteResult{}, fmt.Errorf("delete: resolve ids: %w", err)
	}
	present := make(map[string]bool, len(found))
	for _, issue := range found {
		if issue != nil {
			present[issue.ID] = true
		}
	}
	var missing []string
	for _, id := range ids {
		if !present[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return publicops.DeleteResult{}, &publicops.NotFoundError{IDs: missing}
	}

	// The version precondition sits between the existence probe and the
	// dependents guard, where issueops.Deleter.Delete puts it: a version is a
	// fact about a row, so a request naming a typo reports the typo; and a
	// caller holding a stale token is not yet in a position to choose --cascade
	// or --force, so the mismatch outranks that refusal.
	//
	// It reads ids[0] because ValidateDeleteRequest has already refused a
	// multi-id request carrying one and NormalizeDeleteIDs has already
	// collapsed duplicates, so exactly one distinct id is here. The read shares
	// this transaction with the deletion below, which is what makes the pair a
	// compare-and-delete; CheckVersionInTx is the same guard the update and
	// close paths use, over the same row_lock token and with the same
	// plane routing.
	//
	// IT RE-READS A ROW THE PROBE ABOVE ALREADY RETURNED, and that is a real
	// trade rather than an oversight: `found` carries this row's RowVersion, so
	// the comparison could be made here without a second SELECT. What the second
	// SELECT buys is ONE definition of the guard — the same function the update
	// and close paths call, so a change to how a version is read or how a
	// mismatch is worded reaches all three together — at the cost of one indexed
	// single-row read inside a transaction that is about to delete rows and
	// rewrite their neighbors.
	//
	// THE UNIT-OF-WORK LEG ANSWERS THAT TRADE THE OTHER WAY, comparing the row
	// its own probe loaded, and the two are not in disagreement: that leg
	// reaches the domain use cases rather than a transaction, so this function
	// is not available to it and there is no shared guard for it to prefer. The
	// conformance contract is where the two are held equal.
	if req.ExpectedVersion != nil {
		if err := CheckVersionInTx(ctx, tx, ids[0], *req.ExpectedVersion); err != nil {
			return publicops.DeleteResult{}, err
		}
	}

	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}

	// The guard runs only when the request did not already say what to do
	// about dependents. Under Cascade there is nothing outside the set by
	// construction, which is why the expansion below is not asked about it.
	//
	// IT ASKS ABOUT EVERY NAMED ID, IN BOTH PLANES: the leaf says "a NAMED ROW
	// that some row OUTSIDE the request depends on is refused" with no wisp
	// exemption, and the unit-of-work body has always read it that way.
	if !req.Cascade {
		external, err := ExternalDependentsBySourceInTx(ctx, tx, ids, idSet)
		if err != nil {
			return publicops.DeleteResult{}, fmt.Errorf("delete: check dependents: %w", err)
		}
		if !req.Force {
			// Request order, so the id a caller is told about is stable
			// across runs and across backends.
			for _, id := range ids {
				if deps := external[id]; len(deps) > 0 {
					return publicops.DeleteResult{}, &publicops.DependentsOutsideRequestError{
						IssueID:    id,
						Dependents: deps,
					}
				}
			}
		} else {
			orphaned := make(map[string]bool)
			for _, deps := range external {
				for _, id := range deps {
					orphaned[id] = true
				}
			}
			result.Orphaned = workapi.SortedDeleteIDs(orphaned)
		}
	}

	// THE DELETION SET IS RESOLVED ONCE, HERE, and the same value reaches the
	// neighborhood read, the deletion and the citation rewrite. Resolving it
	// twice drifts: a cascade rooted at a wisp landed in the rewrite set and not
	// in the delete, so those rows survived and their neighbors' descriptions
	// were rewritten to call them deleted. See DeletionSet.
	set, err := ResolveDeletionSetInTx(ctx, tx, ids, req.Cascade)
	if err != nil {
		return publicops.DeleteResult{}, fmt.Errorf("delete: %w", err)
	}

	// The neighborhood is read BEFORE the deletion, because after it the
	// edges that identify a neighbor are gone. It is read against the whole
	// deletion set — the cascade closure, not just the named ids — so a row
	// citing a cascade-deleted id is rewritten too.
	neighbors, err := deleteNeighborsInTx(ctx, tx, set.All)
	if err != nil {
		return publicops.DeleteResult{}, err
	}

	// No guard argument to pass: this body has ALREADY answered the guard
	// question above, and DeleteResolvedSetInTx deletes what it is handed.
	deleted, err := DeleteResolvedSetInTx(ctx, tx, set, req.DryRun)
	if err != nil {
		return publicops.DeleteResult{}, err
	}
	result.Deleted = deleted.DeletedCount
	result.Dependencies = deleted.DependenciesCount
	result.Labels = deleted.LabelsCount
	result.Events = deleted.EventsCount

	if req.DryRun {
		return result, nil
	}

	// set.All, not a set recomputed here: a `[deleted:<id>]` marker is a claim
	// that the row is gone, and the only set that can honestly back that claim
	// is the one DeleteResolvedSetInTx was handed.
	rewritten, err := RewriteDeletedReferencesInTx(ctx, tx, set.All, neighbors, req.Actor)
	if err != nil {
		return publicops.DeleteResult{}, err
	}
	result.ReferencesUpdated = rewritten
	return result, nil
}

// ExternalDependentsBySourceInTx reports, for each of ids, the DIRECT
// dependents that idSet does not contain — the rows a forced delete orphans
// and an unforced one refuses over.
//
// The per-source shape is what lets the unforced refusal name ONE blocked id
// instead of a flat union that answers "something is blocked".
//
//nolint:gosec // G201: inClause contains only ? placeholders
func ExternalDependentsBySourceInTx(ctx context.Context, tx DBTX, ids []string, idSet map[string]bool) (map[string][]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	bySource := make(map[string]map[string]bool)
	for i := 0; i < len(ids); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		inClause, args := buildSQLInClause(ids[i:end])

		for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
			rows, err := tx.QueryContext(ctx,
				fmt.Sprintf(`SELECT %s AS depends_on_id, issue_id FROM %s WHERE %s`,
					DepTargetExpr, depTable, depTargetIn("", inClause)),
				args...)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					continue
				}
				return nil, fmt.Errorf("query dependents from %s: %w", depTable, err)
			}
			for rows.Next() {
				var target, dependent string
				if err := rows.Scan(&target, &dependent); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan dependent: %w", err)
				}
				if idSet[dependent] {
					continue
				}
				if bySource[target] == nil {
					bySource[target] = make(map[string]bool)
				}
				bySource[target][dependent] = true
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("iterate dependents from %s: %w", depTable, err)
			}
		}
	}

	out := make(map[string][]string, len(bySource))
	for target, dependents := range bySource {
		out[target] = workapi.SortedDeleteIDs(dependents)
	}
	return out, nil
}

// deleteNeighborsInTx hydrates the SURVIVING rows joined to the deletion set
// by a dependency edge in either direction — the rows whose text the deletion
// rewrites.
//
// One query per plane over the whole set, so a `--from-file` batch costs two
// queries rather than two per deleted id.
//
//nolint:gosec // G201: inClause contains only ? placeholders
func deleteNeighborsInTx(ctx context.Context, tx DBTX, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	deleting := make(map[string]bool, len(ids))
	for _, id := range ids {
		deleting[id] = true
	}

	neighborIDs := make(map[string]bool)
	for i := 0; i < len(ids); i += deleteBatchSize {
		end := i + deleteBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		inClause, args := buildSQLInClause(ids[i:end])
		doubled := append(append([]interface{}{}, args...), args...)

		for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
			rows, err := tx.QueryContext(ctx,
				fmt.Sprintf(`SELECT issue_id, %s AS depends_on_id FROM %s WHERE issue_id IN (%s) OR %s`,
					DepTargetExpr, depTable, inClause, depTargetIn("", inClause)),
				doubled...)
			if err != nil {
				if optionalBlockedTable(depTable) && isTableNotExistError(err) {
					continue
				}
				return nil, fmt.Errorf("query neighbors from %s: %w", depTable, err)
			}
			for rows.Next() {
				var source, target string
				if err := rows.Scan(&source, &target); err != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("scan neighbor: %w", err)
				}
				for _, candidate := range [2]string{source, target} {
					if candidate == "" || deleting[candidate] {
						continue
					}
					neighborIDs[candidate] = true
				}
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("iterate neighbors from %s: %w", depTable, err)
			}
		}
	}
	if len(neighborIDs) == 0 {
		return nil, nil
	}

	// Sorted so the rewrite touches rows in a stable order, which is what
	// makes a partially-applied failure reproducible.
	hydrate := workapi.SortedDeleteIDs(neighborIDs)
	// An `external:` target and a target belonging to another repository name
	// no row here; GetIssuesByIDsInTx simply does not return them.
	issues, err := GetIssuesByIDsInTx(ctx, tx, hydrate, nil)
	if err != nil {
		return nil, fmt.Errorf("hydrate neighbors: %w", err)
	}
	return issues, nil
}

// RewriteDeletedReferencesInTx replaces every word-boundary occurrence of a
// deleted id with `[deleted:<id>]` in each neighbor's description, notes,
// design and acceptance criteria, and reports how many ROWS it changed.
//
// Exported because the unit-of-work body needs the same rule and neither
// implementation may own it: a route that spelled the pattern differently
// would rewrite a different set of citations for the same deletion.
func RewriteDeletedReferencesInTx(ctx context.Context, tx DBTX, deletedIDs []string, neighbors []*types.Issue, actor string) (int, error) {
	if len(neighbors) == 0 {
		return 0, nil
	}
	touched := make(map[string]bool)
	for _, id := range deletedIDs {
		re := DeletedReferencePattern(id)
		replacement := `$1[deleted:` + id + `]$3`
		for _, neighbor := range neighbors {
			if neighbor == nil {
				continue
			}
			updates := make(map[string]interface{})
			for _, field := range []struct {
				column string
				value  *string
			}{
				{"description", &neighbor.Description},
				{"notes", &neighbor.Notes},
				{"design", &neighbor.Design},
				{"acceptance_criteria", &neighbor.AcceptanceCriteria},
			} {
				if *field.value == "" || !re.MatchString(*field.value) {
					continue
				}
				rewritten := re.ReplaceAllString(*field.value, replacement)
				updates[field.column] = rewritten
				// Write the rewrite back onto the in-memory row so a second
				// deleted id in the same field sees the first one's result
				// rather than re-reading the original.
				*field.value = rewritten
			}
			if len(updates) == 0 {
				continue
			}
			if _, err := UpdateIssueInTx(ctx, tx, neighbor.ID, updates, actor); err != nil {
				return 0, fmt.Errorf("rewrite references in %s: %w", neighbor.ID, err)
			}
			touched[neighbor.ID] = true
		}
	}
	return len(touched), nil
}

// DeletedReferencePattern is the citation rule, in one place: a literal id at
// ASCII word boundaries, where a word character includes the hyphen an id is
// full of. It matches `be-1` in "see (be-1)." and not inside `xbe-1` or
// `be-12`.
func DeletedReferencePattern(id string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_-])(` + regexp.QuoteMeta(id) + `)($|[^A-Za-z0-9_-])`)
}
