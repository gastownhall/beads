package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// GetMoleculeProgressInTx returns progress stats for a molecule within an
// existing transaction. Routes to the correct table (issues/wisps) automatically.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func GetMoleculeProgressInTx(ctx context.Context, tx *sql.Tx, moleculeID string) (*types.MoleculeProgressStats, error) {
	stats := &types.MoleculeProgressStats{
		MoleculeID: moleculeID,
	}

	isWisp := IsActiveWispInTx(ctx, tx, moleculeID)
	issueTable, _, _, depTable := WispTableRouting(isWisp)
	parentCol := "depends_on_issue_id"
	if isWisp {
		parentCol = "depends_on_wisp_id"
	}

	// Get molecule title.
	var title sql.NullString
	err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT title FROM %s WHERE id = ?", issueTable), moleculeID).Scan(&title)
	if err == nil && title.Valid {
		stats.MoleculeTitle = title.String
	}

	// Step 1: Get child issue IDs from dependencies table.
	depRows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT issue_id FROM %s
		WHERE %s = ? AND type = 'parent-child'
	`, depTable, parentCol), moleculeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get molecule children: %w", err)
	}
	var childIDs []string
	for depRows.Next() {
		var id string
		if err := depRows.Scan(&id); err != nil {
			_ = depRows.Close()
			return nil, fmt.Errorf("get molecule progress: scan child: %w", err)
		}
		childIDs = append(childIDs, id)
	}
	_ = depRows.Close()
	if err := depRows.Err(); err != nil {
		return nil, fmt.Errorf("get molecule progress: child rows: %w", err)
	}

	// Step 2: Batch-fetch status for all children.
	// Children of a wisp molecule are also wisps, so use the same table.
	if len(childIDs) > 0 {
		type childInfo struct {
			status string
		}
		childMap := make(map[string]childInfo)
		for start := 0; start < len(childIDs); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(childIDs) {
				end = len(childIDs)
			}
			batch := childIDs[start:end]
			placeholders := make([]string, len(batch))
			args := make([]any, len(batch))
			for i, id := range batch {
				placeholders[i] = "?"
				args[i] = id
			}
			inClause := strings.Join(placeholders, ",")

			query := fmt.Sprintf("SELECT id, status FROM %s WHERE id IN (%s)", issueTable, inClause)
			statusRows, err := tx.QueryContext(ctx, query, args...)
			if err != nil {
				return nil, fmt.Errorf("failed to batch-fetch child statuses: %w", err)
			}
			for statusRows.Next() {
				var id, status string
				if err := statusRows.Scan(&id, &status); err != nil {
					_ = statusRows.Close()
					return nil, fmt.Errorf("get molecule progress: scan status: %w", err)
				}
				childMap[id] = childInfo{status: status}
			}
			_ = statusRows.Close()
		}

		for _, childID := range childIDs {
			info, ok := childMap[childID]
			if !ok {
				continue
			}
			stats.Total++
			switch types.Status(info.status) {
			case types.StatusClosed:
				stats.Completed++
			case types.StatusInProgress:
				stats.InProgress++
				if stats.CurrentStepID == "" {
					stats.CurrentStepID = childID
				}
			}
		}
	}

	return stats, nil
}

// TxMoleculeReader binds the molecule rules (issueops.MoleculeReader) to a
// caller's transaction: it runs the SAME BatchGetter, Relations and EdgeReader
// bodies the store-backed roles run, against tx, so a close that decides
// whether it completed a molecule reads the state its own writes produced.
type TxMoleculeReader struct{ Tx DBTX }

var _ = publicops.MoleculeReaderOver(TxMoleculeReader{})

// GetMany is the BatchGetter body (ExecuteGetMany) in the bound transaction.
func (r TxMoleculeReader) GetMany(ctx context.Context, request publicops.GetManyRequest) (publicops.GetManyResult, error) {
	return ExecuteGetMany(ctx, r.Tx, request)
}

// Related is the Relations body (ExecuteRelated) in the bound transaction.
func (r TxMoleculeReader) Related(ctx context.Context, request publicops.RelatedRequest) ([]*publicops.RelatedIssue, error) {
	if err := ValidateRelatedRequest(request); err != nil {
		return nil, err
	}
	return ExecuteRelated(ctx, r.Tx, request)
}

// ReadEdges is the EdgeReader body (ExecuteEdgeRead) in the bound transaction.
func (r TxMoleculeReader) ReadEdges(ctx context.Context, request publicops.EdgeReadRequest) (publicops.EdgeReadResult, error) {
	if err := ValidateEdgeReadRequest(request); err != nil {
		return publicops.EdgeReadResult{}, err
	}
	return ExecuteEdgeRead(ctx, r.Tx, request)
}

// MoleculeAutoClose is what one CloseCompletedMoleculeInTx did.
type MoleculeAutoClose struct {
	// Root is the post-close snapshot of the root it closed, or nil.
	Root *types.Issue
	// Refusal is why a completed root stayed open (its close policy refused
	// it), or "".
	Refusal string
}

// CloseCompletedMoleculeInTx is CloseRequest.AutoCloseMolecule's body for the
// store-backed backends: when closing stepID completed an auto-closing
// molecule (issueops.CompletedMolecule, read in tx), it closes the root in tx
// through ExecuteClose — unforced, guarded on the root revision read in the
// same transaction, recording issueops.MoleculeAutoCloseReason and session.
//
// A close-policy refusal of the root is answered as MoleculeAutoClose.Refusal
// and writes nothing (the checked close rolls back to its savepoint); the
// caller's own close stands. Any other failure is returned and fails the
// caller's transaction.
func CloseCompletedMoleculeInTx(ctx context.Context, tx *sql.Tx, stepID, actor, session string) (MoleculeAutoClose, ChangedTables, error) {
	root, err := publicops.CompletedMolecule(ctx, publicops.MoleculeReaderOver(TxMoleculeReader{Tx: tx}), stepID)
	if err != nil || root == nil {
		return MoleculeAutoClose{}, nil, err
	}
	version := root.RowVersion
	closed, tables, err := ExecuteClose(ctx, tx, publicops.CloseRequest{
		Actor:           actor,
		IssueID:         root.ID,
		Reason:          publicops.MoleculeAutoCloseReason,
		Session:         session,
		ExpectedVersion: &version,
	})
	if publicops.IsMoleculeAutoCloseRefusal(err) {
		return MoleculeAutoClose{Refusal: err.Error()}, nil, nil
	}
	if err != nil {
		return MoleculeAutoClose{}, nil, fmt.Errorf("auto-closing molecule %s: %w", root.ID, err)
	}
	if !closed.Changed {
		return MoleculeAutoClose{}, tables, nil
	}
	return MoleculeAutoClose{Root: closed.Issue}, tables, nil
}
