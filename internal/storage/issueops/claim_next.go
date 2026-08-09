package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ClaimNextCommitMessage names the claim in the Dolt commit message, matching
// what the CLI's own per-command commit wrote before the claim moved onto the
// role. It is the only spelling: the id it names is the one the claim WON, so
// no caller could have composed it before the call.
func ClaimNextCommitMessage(issueID string) string {
	return "bd: claim ready " + issueID
}

// ValidateClaimNextRequest applies the request rules every ReadyClaimer
// implementation shares. It lives here rather than in each of them because a
// rule enforced on one backend and not the other is not a contract.
func ValidateClaimNextRequest(request publicops.ClaimNextRequest) error {
	if request.Actor == "" {
		return fmt.Errorf("%w: claim next requires an actor", storage.ErrValidation)
	}
	if request.Filter.Limit != nil {
		return fmt.Errorf("%w: claim next does not take a limit", storage.ErrValidation)
	}
	if request.Filter.Offset != 0 {
		return fmt.Errorf("%w: claim next does not take an offset", storage.ErrValidation)
	}
	return nil
}

// ExecuteClaimNext claims the first ready issue matching filter in tx, hydrates
// it in that same transaction, and reports the durable tables changed.
//
// It takes the filter rather than the public request because this package
// speaks WorkFilter: the request-to-filter translation is internal/workapi's,
// and the same builder that answers Reader.Ready is what the accessor
// implementations run before calling in here. That is what keeps the claim's
// predicate and the listing's predicate the same predicate.
//
// A nil claim with a nil error is the ordinary empty-front outcome. It reports
// no changed tables, so the caller's transaction commits nothing and no
// history entry is recorded.
func ExecuteClaimNext(ctx context.Context, tx *sql.Tx, actor string, filter types.WorkFilter) (publicops.ClaimNextResult, ChangedTables, error) {
	claimed, err := ClaimReadyIssueInTx(ctx, tx, filter, actor)
	if err != nil {
		return publicops.ClaimNextResult{}, nil, err
	}
	if claimed == nil {
		return publicops.ClaimNextResult{}, nil, nil
	}
	hydrated, err := HydrateReadyRowInTx(ctx, tx, claimed)
	if err != nil {
		return publicops.ClaimNextResult{}, nil, err
	}
	tables := ChangedTables{}
	tables.Add("issues", "events")
	return publicops.ClaimNextResult{Claimed: hydrated}, tables, nil
}

// HydrateReadyRowInTx fills in the relationship cardinalities a ready row
// carries, reading them in the caller's transaction so the counts describe the
// state that transaction is about to commit.
//
// A failed count read is an error rather than a zero. The CLI's pre-role
// hydration swallowed all three, which meant a broken database reported a
// claimed issue with no dependencies rather than saying the read failed; here
// the whole claim rolls back instead, because a result nobody can hydrate is
// not a result.
func HydrateReadyRowInTx(ctx context.Context, tx *sql.Tx, issue *types.Issue) (*types.IssueWithCounts, error) {
	if issue == nil {
		return nil, nil
	}
	ids := []string{issue.ID}
	depCounts, err := GetDependencyCountsInTx(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate ready row %s: dependency counts: %w", issue.ID, err)
	}
	records, err := GetDependencyRecordsForIssuesInTx(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate ready row %s: dependency records: %w", issue.ID, err)
	}
	commentCounts, err := GetCommentCountsInTx(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("hydrate ready row %s: comment counts: %w", issue.ID, err)
	}

	issue.Dependencies = records[issue.ID]
	counts := depCounts[issue.ID]
	if counts == nil {
		counts = &types.DependencyCounts{}
	}
	var parent *string
	for _, dep := range records[issue.ID] {
		if dep.Type == types.DepParentChild {
			parent = &dep.DependsOnID
			break
		}
	}
	return &types.IssueWithCounts{
		Issue:           issue,
		DependencyCount: counts.DependencyCount,
		DependentCount:  counts.DependentCount,
		CommentCount:    commentCounts[issue.ID],
		Parent:          parent,
	}, nil
}
