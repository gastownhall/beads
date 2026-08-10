package issueops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// edgeCountPlanes are the two dependency tables an edge count spans, paired
// with the issue table an edge's SOURCE row lives in. The pairing is what a
// status-narrowed count joins through: a durable dependent's status is in
// `issues`, an ephemeral one's in `wisps`.
//
// The ephemeral pair is optional — older schemas predate that plane — and
// optionalBlockedTable is what says so, the same probe every other read over
// these two tables uses.
var edgeCountPlanes = []struct{ dependencies, sources string }{
	{dependencies: "dependencies", sources: "issues"},
	{dependencies: "wisp_dependencies", sources: "wisps"},
}

// ValidateEdgeCountRequest applies the request rules every GraphCounter
// implementation shares.
//
// THE ORDER IS PART OF THE CONTRACT. The direction is checked FIRST, so an
// empty request is a refusal about the direction rather than an empty answer:
// EdgeCountRequest{} names no anchors, and answering it with no anchors would
// let a caller that forgot the direction get a plausible response forever. The
// per-entry checks that follow tell a caller's mistake from a legitimately
// empty answer, exactly as ValidateEdgeReadRequest's do.
func ValidateEdgeCountRequest(request publicops.EdgeCountRequest) error {
	switch request.Direction {
	case publicops.EdgeDirectionIn, publicops.EdgeDirectionOut:
	case "":
		return fmt.Errorf("%w: count edges requires a direction (%q or %q)",
			storage.ErrValidation, publicops.EdgeDirectionOut, publicops.EdgeDirectionIn)
	default:
		return fmt.Errorf("%w: count edges direction %q is not %q or %q",
			storage.ErrValidation, request.Direction, publicops.EdgeDirectionOut, publicops.EdgeDirectionIn)
	}
	if request.Status != "" && request.Direction != publicops.EdgeDirectionIn {
		return fmt.Errorf("%w: count edges status %q needs direction %q: an outbound edge's far end may be a row this database does not hold",
			storage.ErrValidation, request.Status, publicops.EdgeDirectionIn)
	}
	for i, id := range request.IDs {
		if id == "" {
			return fmt.Errorf("%w: count edges id %d is empty", storage.ErrValidation, i)
		}
	}
	for i, depType := range request.Types {
		if !depType.IsValid() {
			return fmt.Errorf("%w: count edges type %d is not a usable dependency type (non-empty, max %d chars)",
				storage.ErrValidation, i, types.MaxDependencyTypeLen)
		}
	}
	return nil
}

// FinishEdgeCount assembles the per-anchor answer from the two things every
// implementation reads: which anchors exist, and the edge tallies keyed by
// anchor.
//
// It is a pure function beside the body for the reason the checklist gives: the
// parts that decide what the answer MEANS are pinned in milliseconds without a
// database, and the conformance contract is left to assert what only a real
// backend can show. What it decides here is the whole of the missing-anchor
// rule — a missing anchor counts 0 whatever rows are keyed to it, and a present
// anchor with no matching edges counts 0 too, which are the two facts a caller
// tells apart by Missing and by nothing else.
func FinishEdgeCount(anchors []string, present map[string]struct{}, tallies map[string]int64) publicops.EdgeCountResult {
	out := publicops.EdgeCountResult{Anchors: make([]publicops.AnchorEdgeCount, 0, len(anchors))}
	for _, id := range anchors {
		_, exists := present[id]
		entry := publicops.AnchorEdgeCount{ID: id, Missing: !exists}
		// A missing anchor counts nothing even where rows are still keyed to
		// it: a dependency row whose source has been deleted is orphaned data,
		// and counting it would contradict the flag beside it. FinishEdgeRead
		// drops the same rows from a missing anchor's edge list.
		if exists {
			entry.Count = tallies[id]
		}
		out.Anchors = append(out.Anchors, entry)
	}
	return out
}

// ExecuteEdgeCount returns each anchor's edge cardinality in tx. It is the body
// behind the GraphCounter accessor on ALL THREE legs: the two stores wrap it in
// their own read transaction and the unit-of-work provider reaches it through
// the domain repository, whose runner publishes exactly the DBTX method set.
//
// The existence probe and the tally share ONE transaction, which is what lets
// AnchorEdgeCount.Missing mean what it says: a probe in its own transaction
// could report an anchor missing while a second transaction counted that
// anchor's edges, contradicting itself in one response body.
//
// VALIDATION HAPPENS HERE rather than at each accessor, which is where
// ExecuteEdgeRead's sibling leaves it. The difference is that this body is the
// only body: the two stores and the unit-of-work repository all land in this
// function, so a leg that forgot to validate would be a leg answering a
// different contract, and there is no second implementation for the check to
// belong to. It runs before the transaction opens nothing it needs to.
func ExecuteEdgeCount(ctx context.Context, tx DBTX, request publicops.EdgeCountRequest) (publicops.EdgeCountResult, error) {
	if err := ValidateEdgeCountRequest(request); err != nil {
		return publicops.EdgeCountResult{}, err
	}
	anchors := EdgeReadAnchors(request.IDs)
	if len(anchors) == 0 {
		return publicops.EdgeCountResult{Anchors: []publicops.AnchorEdgeCount{}}, nil
	}
	present, err := PresentIssueOrWispIDsInTx(ctx, tx, anchors)
	if err != nil {
		return publicops.EdgeCountResult{}, err
	}
	tallies, err := tallyEdgesInTx(ctx, tx, anchors, request)
	if err != nil {
		return publicops.EdgeCountResult{}, err
	}
	return FinishEdgeCount(anchors, present, tallies), nil
}

// tallyEdgesInTx counts the matching edges per anchor across both dependency
// planes, batched at queryBatchSize.
//
// It SUMS the two planes rather than counting distinct row ids, which is the
// shipped answer of every raw count this role covers (dolt/counts.go's
// CountDependents, CountDependencies and CountDependentsByStatus) and of
// CountDependencyEdgesInTx, the domain body behind `bd show`'s counts on the
// unit-of-work leg. CountDependentRecordsInTx de-duplicates instead, because it
// must agree with a keyset PAGE of the same rows; a cardinality has no page and
// has never de-duplicated.
func tallyEdgesInTx(ctx context.Context, tx DBTX, anchors []string, request publicops.EdgeCountRequest) (map[string]int64, error) {
	tallies := make(map[string]int64, len(anchors))
	for _, plane := range edgeCountPlanes {
		for start := 0; start < len(anchors); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(anchors) {
				end = len(anchors)
			}
			batch := anchors[start:end]
			query := buildEdgeCountQuery(plane.dependencies, plane.sources, len(batch), request)
			rows, err := tx.QueryContext(ctx, query, edgeCountArgs(batch, request)...)
			if err != nil {
				if optionalBlockedTable(plane.dependencies) && isTableNotExistError(err) {
					break
				}
				return nil, fmt.Errorf("count edges in %s: %w", plane.dependencies, err)
			}
			for rows.Next() {
				var id string
				var n int64
				if scanErr := rows.Scan(&id, &n); scanErr != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("count edges in %s: scan: %w", plane.dependencies, scanErr)
				}
				tallies[id] += n
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("count edges in %s: rows: %w", plane.dependencies, err)
			}
		}
	}
	return tallies, nil
}

// buildEdgeCountQuery returns the grouped count for one dependency plane, keyed
// by the anchor end the request's direction names.
//
// THE TARGET END IS ALWAYS THE COALESCE EXPRESSION, never the STORED generated
// `depends_on_id` column. Both dependency tables define that column as
// GENERATED ALWAYS AS the same COALESCE, and inside an aggregate the pure-Go
// GMS analyzer can prune the base columns it derives from and then fail with
// "column depends_on_id could not be found in any table in scope"
// (dolt/counts.go says so at depTargetExpr). Every other aggregate over these
// tables in this package resolves the target the same way.
func buildEdgeCountQuery(depTable, sourceTable string, batchSize int, request publicops.EdgeCountRequest) string {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", batchSize), ",")

	typeClause, _ := buildDepTypeClause(request.Types)
	if typeClause != "" {
		typeClause = " AND d." + typeClause
	}

	if request.Direction == publicops.EdgeDirectionOut {
		//nolint:gosec // G201: table names are hardcoded literals and the IN clause holds only ? placeholders.
		return fmt.Sprintf(
			"SELECT d.issue_id AS anchor, COUNT(*) AS n FROM %s d WHERE d.issue_id IN (%s)%s GROUP BY d.issue_id",
			depTable, placeholders, typeClause)
	}

	anchorExpr := depTargetExpr("d")
	if request.Status == "" {
		//nolint:gosec // G201: table names are hardcoded literals and the IN clause holds only ? placeholders.
		return fmt.Sprintf(
			"SELECT %s AS anchor, COUNT(*) AS n FROM %s d WHERE %s%s GROUP BY %s",
			anchorExpr, depTable, depTargetIn("d", placeholders), typeClause, anchorExpr)
	}
	// A status narrows by the DEPENDENT's row, which lives on the same plane as
	// the edge: `dependencies` sources are durable issues, `wisp_dependencies`
	// sources are wisps. An edge whose source row is gone joins to nothing and
	// drops out, which the leaf doc states.
	//nolint:gosec // G201: table names are hardcoded literals and the IN clause holds only ? placeholders.
	return fmt.Sprintf(
		"SELECT %s AS anchor, COUNT(*) AS n FROM %s d JOIN %s s ON s.id = d.issue_id WHERE %s%s AND s.status = ? GROUP BY %s",
		anchorExpr, depTable, sourceTable, depTargetIn("d", placeholders), typeClause, anchorExpr)
}

// edgeCountArgs binds the batch's anchor ids, then the type filter, then the
// status — the order buildEdgeCountQuery emits its placeholders in.
func edgeCountArgs(batch []string, request publicops.EdgeCountRequest) []any {
	args := make([]any, 0, len(batch)+len(request.Types)+1)
	for _, id := range batch {
		args = append(args, id)
	}
	_, typeArgs := buildDepTypeClause(request.Types)
	args = append(args, typeArgs...)
	if request.Direction == publicops.EdgeDirectionIn && request.Status != "" {
		args = append(args, request.Status)
	}
	return args
}
