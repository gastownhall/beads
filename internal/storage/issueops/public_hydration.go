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

// HydrateIssueOperationResultForPlane returns the complete operation result
// from exactly the caller-selected aggregate. Lifecycle mutations resolve an
// ID's plane before applying guards and writes; carrying that decision through
// hydration prevents a dual-resident ID from returning the untouched twin with
// labels, dependencies, or comments from the row that actually changed.
func HydrateIssueOperationResultForPlane(ctx context.Context, tx DBTX, id string, includeComments, isWisp bool) (*types.Issue, error) {
	if tx == nil {
		return nil, fmt.Errorf("hydrate issue operation result: transaction is nil")
	}
	issue, err := GetIssueForPlaneInTx(ctx, tx, id, isWisp)
	if err != nil {
		return nil, err
	}
	_, _, _, dependencyTable := WispTableRouting(isWisp)
	dependencies, err := GetDependencyRecordsForIssuesFromTableInTx(ctx, tx, dependencyTable, []string{id})
	if err != nil {
		return nil, fmt.Errorf("hydrate issue dependencies: %w", err)
	}
	issue.Dependencies = dependencies[id]
	if includeComments {
		comments, err := GetIssueCommentsForPlaneInTx(ctx, tx, id, isWisp)
		if err != nil {
			return nil, fmt.Errorf("hydrate issue comments: %w", err)
		}
		issue.Comments = comments
	}
	return issue, nil
}
