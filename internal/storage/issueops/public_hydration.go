package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// HydrateIssueOperationResult returns a detached issue with requested relations.
func HydrateIssueOperationResult(ctx context.Context, tx DBTX, id string, includeComments bool) (*types.Issue, error) {
	if tx == nil {
		return nil, fmt.Errorf("hydrate issue operation result: transaction is nil")
	}
	issue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	labels, err := GetLabelsInTx(ctx, tx, "", id)
	if err != nil {
		return nil, fmt.Errorf("hydrate issue labels: %w", err)
	}
	issue.Labels = labels
	dependencies, err := GetDependencyRecordsForIssuesInTx(ctx, tx, []string{id})
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
