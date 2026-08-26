package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// GetIssueInTx retrieves a single issue by ID within an existing transaction,
// including its labels. Automatically routes to the wisps/wisp_labels tables
// if the ID is an active wisp. Returns storage.ErrNotFound (wrapped) if the
// issue does not exist in either table.
func GetIssueInTx(ctx context.Context, tx DBTX, id string) (*types.Issue, error) {
	issue, err := getIssueFromTableInTx(ctx, tx, "issues", "labels", id)
	if err == nil {
		return issue, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}

	issue, err = getIssueFromTableInTx(ctx, tx, "wisps", "wisp_labels", id)
	if err == nil {
		return issue, nil
	}
	if errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	return nil, err
}

// GetIssueForPlaneInTx retrieves id from exactly the caller-selected issue
// plane, including that plane's labels. Mutation facades use it after they
// have resolved a dual-resident ID so their preimage, CAS, audit event, and
// journal snapshot cannot silently switch to the sibling aggregate.
func GetIssueForPlaneInTx(ctx context.Context, tx DBTX, id string, isWisp bool) (*types.Issue, error) {
	issueTable, labelTable, _, _ := WispTableRouting(isWisp)
	issue, err := getIssueFromTableInTx(ctx, tx, issueTable, labelTable, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	return issue, err
}

// missingOptionalIssueTable reports whether err is the absence of the optional
// issue plane the hydration query just read. The hydration FROM clause also
// carries sqlbuild.LeaseJoin, so a blanket table-not-exist check here folds a
// missing leases table into "row absent" — a wrong answer, not an empty one.
func missingOptionalIssueTable(err error, issueTable string) bool {
	return optionalBlockedTable(issueTable) && dberrors.IsMissingTable(err, issueTable)
}

func getIssueFromTableInTx(ctx context.Context, tx DBTX, issueTable, labelTable, id string) (*types.Issue, error) {
	//nolint:gosec // G201: issueTable is a hardcoded literal supplied by GetIssueInTx ("issues" or "wisps")
	row := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM %s %s WHERE id = ?`,
		IssueSelectColumns, issueTable, sqlbuild.LeaseJoin(issueTable)), id)
	issue, err := ScanIssueFrom(row)
	if err == sql.ErrNoRows || missingOptionalIssueTable(err, issueTable) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get issue: %w", err)
	}

	// Fetch labels in the same transaction to avoid MaxOpenConns=1 deadlock.
	labels, err := GetLabelsInTx(ctx, tx, labelTable, id)
	if err != nil {
		return nil, fmt.Errorf("get issue labels: %w", err)
	}
	issue.Labels = labels

	return issue, nil
}
