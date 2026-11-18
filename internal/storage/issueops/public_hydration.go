package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// HydrateIssueOperationResult returns a detached issue with requested relations.
//
// The labels and outgoing dependency records come from the table that answered
// for the body (issues or wisps), read in tx, the same transaction as the body,
// after every write the caller made in it. For an ID with a row in both tables
// that is the durable row, as the body is.
func HydrateIssueOperationResult(ctx context.Context, tx DBTX, id string, includeComments bool) (*types.Issue, error) {
	if tx == nil {
		return nil, fmt.Errorf("hydrate issue operation result: transaction is nil")
	}
	issue, answeredFromWisps, err := getIssueInTxWithPlane(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	_, _, _, dependencyTable := WispTableRouting(answeredFromWisps)
	dependencies, err := GetDependencyRecordsForIssuesFromTableInTx(ctx, tx, dependencyTable, []string{id})
	if err != nil {
		return nil, fmt.Errorf("hydrate issue dependencies: %w", err)
	}
	issue.Dependencies = dependencies[id]
	if includeComments {
		comments, err := GetIssueCommentsInTx(ctx, tx, id)
		if err != nil {
			return nil, fmt.Errorf("hydrate issue comments: %w", err)
		}
		issue.Comments = comments
	}
	return issue, nil
}

// OverlayCreateTimestamps restores onto a hydrated create result the
// full-precision timestamps the INSERT actually sent.
//
// created_at/updated_at are DATETIME(0) columns, so the re-read a create does
// to build its result snapshot comes back rounded to whole seconds. The create
// echo has carried sub-second RFC3339Nano stamps since before the lifecycle
// facade existed (v1.2.2 returned the caller's own in-memory struct), and
// agents ordering same-second creates by that echo depend on it. Everything
// else on the snapshot stays DB-truth, which is the facade's contract; only
// these three fields are restored, and only for create.
func OverlayCreateTimestamps(hydrated, written *types.Issue) {
	if hydrated == nil || written == nil {
		return
	}
	hydrated.CreatedAt = written.CreatedAt
	hydrated.UpdatedAt = written.UpdatedAt
	if written.ClosedAt != nil {
		hydrated.ClosedAt = written.ClosedAt
	}
}
