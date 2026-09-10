package issueops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ValidateEdgeReadRequest applies the request rules every EdgeReader
// implementation shares. Both tell a caller's mistake from a legitimately empty
// answer: an empty ID entry names nothing, and an unusable dependency type would
// become a filter that silently matches nothing. An empty ID SLICE is neither —
// it asks about no anchors and gets none back.
func ValidateEdgeReadRequest(request publicops.EdgeReadRequest) error {
	for i, id := range request.IDs {
		if id == "" {
			return fmt.Errorf("%w: read edges id %d is empty", storage.ErrValidation, i)
		}
	}
	for i, depType := range request.Types {
		if !depType.IsValid() {
			return fmt.Errorf("%w: read edges type %d is not a usable dependency type (non-empty, max %d chars)",
				storage.ErrValidation, i, types.MaxDependencyTypeLen)
		}
	}
	return nil
}

// EdgeReadAnchors is the de-duplicated anchor list a read runs against: the
// request's ids with repeats collapsed onto their first mention.
//
// It is shared rather than a loop in each implementation because the
// de-duplication decides the SHAPE of the answer — one entry per distinct id, in
// first-mention order. BlockingAnnotator makes the same promise over the same
// shape of request (blocking_annotation.go) and reaches it here too.
func EdgeReadAnchors(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// FinishEdgeRead assembles the per-anchor answer from the two things every
// implementation reads: which anchors exist, and the edges keyed by source.
//
// It is one function rather than two copies because the type filter, the pinned
// order and the empty-versus-absent distinction are the whole observable
// contract of this role.
//
// THE ORDER is ascending by target id, with the edge type breaking a tie. The
// row's own surrogate key is deliberately not a third term — the source-keyed
// read does not select it, so it is empty on every row here.
func FinishEdgeRead(anchors []string, present map[string]struct{}, edges map[string][]*types.Dependency, depTypes []types.DependencyType) publicops.EdgeReadResult {
	allowed := make(map[types.DependencyType]struct{}, len(depTypes))
	for _, depType := range depTypes {
		allowed[depType] = struct{}{}
	}

	out := publicops.EdgeReadResult{Anchors: make([]publicops.AnchorEdges, 0, len(anchors))}
	for _, id := range anchors {
		_, exists := present[id]
		entry := publicops.AnchorEdges{ID: id, Missing: !exists, Edges: []*types.Dependency{}}
		// A missing anchor carries no edges even if rows are keyed to it: a
		// dependency row whose source has been deleted is orphaned data, and
		// reporting it would contradict the flag beside it.
		if exists {
			for _, edge := range edges[id] {
				if edge == nil {
					continue
				}
				if len(allowed) > 0 {
					if _, ok := allowed[edge.Type]; !ok {
						continue
					}
				}
				entry.Edges = append(entry.Edges, edge)
			}
			sort.SliceStable(entry.Edges, func(i, j int) bool {
				if entry.Edges[i].DependsOnID != entry.Edges[j].DependsOnID {
					return entry.Edges[i].DependsOnID < entry.Edges[j].DependsOnID
				}
				return entry.Edges[i].Type < entry.Edges[j].Type
			})
		}
		out.Anchors = append(out.Anchors, entry)
	}
	return out
}

// ExecuteEdgeRead returns each anchor's stored outgoing edges in tx. It is the
// store-backed body behind the EdgeReader accessor; the unit-of-work provider
// reaches the same two reads through its use cases.
//
// The existence probe and the edge read share ONE transaction, which is what
// lets AnchorEdges.Missing mean what it says: a probe in its own transaction
// could report an anchor missing while a second transaction returned that
// anchor's edges, contradicting itself in one response body.
func ExecuteEdgeRead(ctx context.Context, tx DBTX, request publicops.EdgeReadRequest) (publicops.EdgeReadResult, error) {
	anchors := EdgeReadAnchors(request.IDs)
	if len(anchors) == 0 {
		return publicops.EdgeReadResult{Anchors: []publicops.AnchorEdges{}}, nil
	}
	present, err := PresentIssueOrWispIDsInTx(ctx, tx, anchors)
	if err != nil {
		return publicops.EdgeReadResult{}, err
	}
	edges, err := GetDependencyRecordsForIssuesInTx(ctx, tx, anchors)
	if err != nil {
		return publicops.EdgeReadResult{}, err
	}
	return FinishEdgeRead(anchors, present, edges, request.Types), nil
}

// PresentIssueOrWispIDsInTx reports which of ids exist, on either plane. It is
// the batched form of RequireIssueOrWispInTx, and it answers with a set rather
// than an error because a batch that failed on the first absent id would throw
// away the answers for the ids that were found.
//
// A missing wisps table is "no wisps", exactly as the single-id probe treats
// it: older schemas predate that plane and an anchor there is simply not one.
//
//nolint:gosec // G201: table is one of two hardcoded literals; only ? placeholders in the IN clause.
func PresentIssueOrWispIDsInTx(ctx context.Context, tx DBTX, ids []string) (map[string]struct{}, error) {
	present := make(map[string]struct{}, len(ids))
	if len(ids) == 0 {
		return present, nil
	}
	for _, table := range []string{"issues", "wisps"} {
		for start := 0; start < len(ids); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(ids) {
				end = len(ids)
			}
			batch := ids[start:end]
			placeholders := make([]string, len(batch))
			args := make([]any, len(batch))
			for i, id := range batch {
				placeholders[i] = "?"
				args[i] = id
			}
			rows, err := tx.QueryContext(ctx, fmt.Sprintf(
				"SELECT id FROM %s WHERE id IN (%s)", table, strings.Join(placeholders, ",")), args...)
			if err != nil {
				if isTableNotExistError(err) {
					break
				}
				return nil, fmt.Errorf("check issue existence in %s: %w", table, err)
			}
			for rows.Next() {
				var id string
				if scanErr := rows.Scan(&id); scanErr != nil {
					_ = rows.Close()
					return nil, fmt.Errorf("check issue existence in %s: scan: %w", table, scanErr)
				}
				present[id] = struct{}{}
			}
			_ = rows.Close()
			if err := rows.Err(); err != nil {
				return nil, fmt.Errorf("check issue existence in %s: rows: %w", table, err)
			}
		}
	}
	return present, nil
}
