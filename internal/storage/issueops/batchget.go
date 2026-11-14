package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

// ValidateGetManyRequest applies the request rules every BatchGetter
// implementation shares.
//
// THE CAP IS CHECKED FIRST, against the request AS SENT, before
// deduplication: the obligation it bounds is reading the request apart (and,
// below the role, holding one snapshot open long enough to resolve it), not
// reading the rows the ids resolve to, so a caller cannot dodge it by
// repeating one id 1,500 times. The per-entry blank check that follows tells a
// caller's mistake from a legitimately empty request, exactly as
// ValidateEdgeCountRequest's does for EdgeCountRequest.IDs.
func ValidateGetManyRequest(request publicops.GetManyRequest) error {
	if len(request.IDs) > publicops.MaxGetManyIDs {
		return &publicops.TooManyIDsError{Requested: len(request.IDs), Cap: publicops.MaxGetManyIDs}
	}
	for i, id := range request.IDs {
		if id == "" {
			return fmt.Errorf("%w: get many id at position %d is empty", storage.ErrValidation, i)
		}
	}
	return nil
}

// DedupeGetManyIDs collapses duplicates, keeping the caller's FIRST mention of
// each id. Unlike NormalizeDeleteIDs's sibling, it does NOT trim whitespace:
// GetManyRequest.IDs are exact ids, and a caller that padded one with
// whitespace gets that id back in Missing rather than a silently corrected
// lookup, the same exactness RunGraphCounterResolvesIDsExactly pins for
// EdgeCountRequest.IDs.
func DedupeGetManyIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// FinishGetMany assembles the ordered answer from the ids a caller asked for
// (already deduplicated) and the issues a read actually found, keyed by id.
//
// It is a pure function beside the body for ExecuteEdgeCount's reason: the
// part that decides what the answer MEANS — an id not in found is Missing, and
// Issues/Missing both preserve the caller's order — is pinned without a
// database, and the conformance contract is left to assert what only a real
// backend can show.
func FinishGetMany(ids []string, found map[string]*publicops.Issue) publicops.GetManyResult {
	result := publicops.GetManyResult{
		Issues:  make([]*publicops.Issue, 0, len(ids)),
		Missing: make([]string, 0),
	}
	for _, id := range ids {
		if issue, ok := found[id]; ok {
			result.Issues = append(result.Issues, issue)
			continue
		}
		result.Missing = append(result.Missing, id)
	}
	return result
}

// ExecuteGetMany returns the issues named by request.IDs from tx, in ONE
// snapshot. It is the body behind the BatchGetter accessor on ALL THREE legs:
// the two stores wrap it in their own read transaction, and the unit-of-work
// provider reaches it through the domain repository, whose runner publishes
// exactly the DBTX method set GetIssuesByIDsInTx takes.
//
// VALIDATION HAPPENS HERE, for ExecuteEdgeCount's reason: this body is the
// only body, so a leg that forgot to validate would be answering a different
// contract, and there is no second implementation for the check to belong to.
// It runs before the transaction's one query, so a request over MaxGetManyIDs
// never reaches storage.
//
// HYDRATION IS LABELS ONLY, because that is all GetIssuesByIDsInTx fills: no
// dependencies, dependents or comments. A caller that needs those reads
// Reader.Get instead, one id at a time.
func ExecuteGetMany(ctx context.Context, tx DBTX, request publicops.GetManyRequest) (publicops.GetManyResult, error) {
	if err := ValidateGetManyRequest(request); err != nil {
		return publicops.GetManyResult{}, err
	}
	ids := DedupeGetManyIDs(request.IDs)
	if len(ids) == 0 {
		return publicops.GetManyResult{Issues: []*publicops.Issue{}, Missing: []string{}}, nil
	}
	issues, err := GetIssuesByIDsInTx(ctx, tx, ids, nil)
	if err != nil {
		return publicops.GetManyResult{}, fmt.Errorf("get many: %w", err)
	}
	found := make(map[string]*publicops.Issue, len(issues))
	for _, issue := range issues {
		found[issue.ID] = issue
	}
	return FinishGetMany(ids, found), nil
}
