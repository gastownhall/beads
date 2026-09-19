package issueops

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ValidateRelatedRequest applies the request rules every Relations
// implementation shares.
//
// The direction check is the load-bearing one: the zero value is refused
// rather than defaulted, because "out" and "in" answer inverse questions with
// identical shapes and a caller handed the wrong one has nothing to notice.
//
// A Types entry is checked for being a value at all — non-empty, within the
// column's length — and NEVER for membership of a known-types list. The
// vocabulary is an open, workspace-configurable set (see the Dep* constants),
// so a workspace's own type has to be able to filter; what is refused is an
// entry no edge could ever carry, which would otherwise be a filter that
// silently matches nothing.
func ValidateRelatedRequest(request publicops.RelatedRequest) error {
	if request.ID == "" {
		return fmt.Errorf("%w: related requires an issue ID", storage.ErrValidation)
	}
	switch request.Direction {
	case publicops.RelationOut, publicops.RelationIn:
	default:
		return fmt.Errorf("%w: related requires a direction (%q or %q), and has no default",
			storage.ErrValidation, publicops.RelationOut, publicops.RelationIn)
	}
	for i, depType := range request.Types {
		if !depType.IsValid() {
			return fmt.Errorf("%w: related type %d is not a usable dependency type (non-empty, max %d chars)",
				storage.ErrValidation, i, types.MaxDependencyTypeLen)
		}
	}
	return nil
}

// ExecuteRelated returns the anchor's neighbors in tx. It is the store-backed
// body behind the Relations accessor; the unit-of-work provider has its own,
// which reaches the same two queries through its dependency use case.
//
// The anchor's EXISTENCE is checked first, on both planes, because an empty
// neighbor list is otherwise indistinguishable from a typo — and the empty
// list is the common case, so the typo would never surface.
func ExecuteRelated(ctx context.Context, tx DBTX, request publicops.RelatedRequest) ([]*types.IssueWithDependencyMetadata, error) {
	if err := RequireIssueOrWispInTx(ctx, tx, request.ID); err != nil {
		return nil, err
	}
	var (
		items []*types.IssueWithDependencyMetadata
		err   error
	)
	if request.Direction == publicops.RelationIn {
		items, err = GetDependentsWithMetadataInTx(ctx, tx, request.ID)
	} else {
		items, err = GetDependenciesWithMetadataInTx(ctx, tx, request.ID)
	}
	if err != nil {
		return nil, err
	}
	return FinishRelatedPage(items, request.Types), nil
}

// RequireIssueOrWispInTx refuses an id that names neither an issue nor a wisp,
// with the typed ErrNotFound a caller classifies on. It probes the two planes
// the way IsActiveWispInTx routes them, so an anchor cannot be reported
// missing on one plane while existing on the other.
func RequireIssueOrWispInTx(ctx context.Context, tx DBTX, id string) error {
	for _, table := range []string{"issues", "wisps"} {
		var exists bool
		//nolint:gosec // G201: table is one of two hardcoded literals
		err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?)`, table), id).Scan(&exists)
		if err != nil {
			if isTableNotExistError(err) {
				continue
			}
			return fmt.Errorf("check issue existence in %s: %w", table, err)
		}
		if exists {
			return nil
		}
	}
	return fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
}

// FinishRelatedPage applies the type filter and the pinned order both
// Relations implementations answer in. It is one function rather than two
// copies for the reason workapi.FinishPage is: an ordering that each
// implementation applies for itself is an ordering the two will eventually
// disagree about.
//
// THE ORDER is ascending by the neighbor's id, with the edge type breaking a
// tie. The underlying reads walk the durable and wisp dependency tables in
// sequence, so their natural order tracks which plane a neighbor happens to
// live on — stable enough to look deliberate and not stable enough to rely on.
//
// The result is never nil, so a caller that marshals it emits an empty array
// rather than null.
func FinishRelatedPage(items []*types.IssueWithDependencyMetadata, depTypes []types.DependencyType) []*types.IssueWithDependencyMetadata {
	out := make([]*types.IssueWithDependencyMetadata, 0, len(items))
	allowed := make(map[types.DependencyType]struct{}, len(depTypes))
	for _, depType := range depTypes {
		allowed[depType] = struct{}{}
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[item.DependencyType]; !ok {
				continue
			}
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].DependencyType < out[j].DependencyType
	})
	return out
}
