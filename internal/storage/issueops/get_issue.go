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
	issue, _, err := getIssueInTxWithPlane(ctx, tx, id)
	return issue, err
}

// getIssueInTxWithPlane is GetIssueInTx that also reports which table returned
// the row: answeredFromWisps is true only when the issues table had no row and
// the wisps table did. It is the table the read succeeded on, not a guess from
// the row's Ephemeral or NoHistory flags. The labels on the returned issue come
// from that same table's label table.
func getIssueInTxWithPlane(ctx context.Context, tx DBTX, id string) (issue *types.Issue, answeredFromWisps bool, err error) {
	issue, err = getIssueFromTableInTx(ctx, tx, "issues", "labels", id)
	if err == nil {
		return issue, false, nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return nil, false, err
	}

	issue, err = getIssueFromTableInTx(ctx, tx, "wisps", "wisp_labels", id)
	if err == nil {
		return issue, true, nil
	}
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	return nil, false, err
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
