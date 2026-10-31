package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
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
			if err := statusRows.Err(); err != nil {
				return nil, fmt.Errorf("get molecule progress: iterate statuses: %w", err)
			}
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

// MoleculeAutoClose is what one molecule auto-close did.
type MoleculeAutoClose struct {
	// Root is the post-close snapshot of the root it closed, or nil.
	Root *types.Issue
	// Refusal is why a completed root stayed open (its close policy or a
	// close guard refused it), or "": the refusal's own sentence
	// (RefusedMoleculeAutoClose).
	Refusal string
}

// RefusedMoleculeAutoClose is the MoleculeAutoClose for a root whose close
// failed with err, a refusal (issueops.IsMoleculeAutoCloseRefusal). Its
// Refusal is the sentence the refusing guard or close policy wrote, the same
// on every backend. ExecuteClose returns a refusal as it was minted, while the
// unit of work's use case and repository wrap it on the way out ("close <id>:
// db: IssueSQLRepository.CloseChecked <id>: …"); the sentence already names
// the root, so the wrap would only leak a repository type into an
// operator-facing field (bd close --json's molecule_auto_close_refusal).
func RefusedMoleculeAutoClose(err error) MoleculeAutoClose {
	var (
		blocked      *publicops.BlockedError
		openChildren *publicops.CloseOpenChildrenError
		template     *publicops.TemplateReadOnlyError
		pinned       *publicops.PinnedError
		notAssignee  *publicops.CloseNotAssigneeError
	)
	switch {
	case errors.As(err, &blocked):
		err = blocked
	case errors.As(err, &openChildren):
		err = openChildren
	case errors.As(err, &template):
		err = template
	case errors.As(err, &pinned):
		err = pinned
	case errors.As(err, &notAssignee):
		err = notAssignee
	}
	return MoleculeAutoClose{Refusal: err.Error()}
}

// moleculeCoordinationKeyPrefix names the local_metadata cells an auto-close
// rewrites (TouchMoleculeCoordinationInTx).
const moleculeCoordinationKeyPrefix = "molecule-coordination/v1/"

// MoleculeCoordinationKey is the local_metadata cell a molecule auto-close
// rewrites before it decides rootID. It shares the dependency coordination
// cells' sharding (4096 rows): the same root always resolves to the same
// cell, and a hash collision between two roots only adds a safe
// serialization conflict.
func MoleculeCoordinationKey(rootID string) string {
	return fmt.Sprintf("%s%03x", moleculeCoordinationKeyPrefix, dependencyCoordinationShard(rootID))
}

// TouchMoleculeCoordinationInTx rewrites rootID's molecule coordination cell
// with a fresh value, the dependency coordination idiom
// (TouchDependencyCoordinationTableInTx) applied to a molecule's completion.
//
// Two closes that finish a molecule's last two steps in concurrent
// transactions each read the other's step as still open, so neither closes the
// root, and their writes touch disjoint rows: both commit and the complete
// molecule's root is stranded open. Every auto-close that decides a root
// rewrites this one cell first, so two transactions deciding the same root
// conflict at commit instead; the loser's retry reads the winner's close and
// closes the root.
//
// The cost is that sibling closes serialize. The cell is rewritten before the
// steps are read, by every close under the open root and not only the one that
// completes it, so any two concurrent closes of steps under the same root
// conflict and one of them retries. Under heavy fan-out a close can spend its
// whole retry budget conflicting and then fails with the serialization error.
func TouchMoleculeCoordinationInTx(ctx context.Context, tx DBTX, rootID string) error {
	if rootID == "" {
		return fmt.Errorf("touch molecule coordination: root ID must not be empty")
	}
	if _, err := tx.ExecContext(ctx,
		"REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)", MoleculeCoordinationKey(rootID), strconv.FormatInt(FreshRowLock(), 10)); err != nil {
		return fmt.Errorf("touch molecule coordination for %s: %w", rootID, err)
	}
	return nil
}

// CloseCompletedMoleculeInTx is CloseRequest.AutoCloseMolecule's body for the
// store-backed backends: CloseCompletedMoleculesInTx for one step.
func CloseCompletedMoleculeInTx(ctx context.Context, tx *sql.Tx, stepID, actor, session string) (MoleculeAutoClose, ChangedTables, error) {
	autos, tables, err := CloseCompletedMoleculesInTx(ctx, tx, []string{stepID}, actor, session)
	if err != nil {
		return MoleculeAutoClose{}, nil, err
	}
	return autos[0], tables, nil
}

// CloseCompletedMoleculesInTx is CloseBatchRequest.AutoCloseMolecule's body
// for the store-backed backends, and through CloseCompletedMoleculeInTx
// CloseRequest.AutoCloseMolecule's. For every auto-closing molecule root a
// close of stepIDs could have completed (issueops.AutoClosingMoleculeRoots,
// read in tx), it rewrites the root's coordination cell
// (TouchMoleculeCoordinationInTx), and when every step under the root is now
// closed (issueops.MoleculeStepsClosed) it closes the root in tx through
// ExecuteClose — issueops.MoleculeRootClose: unforced, guarded on the root
// revision read in the same transaction, recording
// issueops.MoleculeAutoCloseReason and session.
//
// It answers one MoleculeAutoClose per step id, in order. Each root is decided
// once, and what happened to it is reported on the first step under it; every
// other step under that root answers the zero value.
//
// A refusal of the root (issueops.IsMoleculeAutoCloseRefusal) is answered as
// MoleculeAutoClose.Refusal and writes nothing; the caller's own closes stand.
// Any other failure is returned and fails the caller's transaction.
func CloseCompletedMoleculesInTx(ctx context.Context, tx *sql.Tx, stepIDs []string, actor, session string) ([]MoleculeAutoClose, ChangedTables, error) {
	reader := publicops.MoleculeReaderOver(TxMoleculeReader{Tx: tx})
	roots, err := publicops.AutoClosingMoleculeRoots(ctx, reader, stepIDs)
	if err != nil {
		return nil, nil, err
	}
	autos := make([]MoleculeAutoClose, len(stepIDs))
	tables := ChangedTables{}
	decided := make(map[string]bool, len(roots))
	for i, stepID := range stepIDs {
		root := roots[stepID]
		if root == nil || decided[root.ID] {
			continue
		}
		decided[root.ID] = true
		auto, rootTables, err := closeCompletedMoleculeRootInTx(ctx, tx, reader, root, actor, session)
		if err != nil {
			return nil, nil, err
		}
		tables.Merge(rootTables)
		autos[i] = auto
	}
	return autos, tables, nil
}

func closeCompletedMoleculeRootInTx(ctx context.Context, tx *sql.Tx, reader publicops.MoleculeReader, root *types.Issue, actor, session string) (MoleculeAutoClose, ChangedTables, error) {
	if err := TouchMoleculeCoordinationInTx(ctx, tx, root.ID); err != nil {
		return MoleculeAutoClose{}, nil, err
	}
	complete, err := publicops.MoleculeStepsClosed(ctx, reader, root.ID)
	if err != nil || !complete {
		return MoleculeAutoClose{}, nil, err
	}
	closed, tables, err := ExecuteClose(ctx, tx, publicops.MoleculeRootClose(root, actor, session))
	if publicops.IsMoleculeAutoCloseRefusal(err) {
		return RefusedMoleculeAutoClose(err), nil, nil
	}
	if err != nil {
		// Not a refusal, such as a storage error. Returned, it fails the
		// caller's transaction, so the step's own close rolls back with it
		// (CloseRequest.AutoCloseMolecule).
		return MoleculeAutoClose{}, nil, fmt.Errorf("auto-closing molecule %s: %w", root.ID, err)
	}
	if !closed.Changed {
		return MoleculeAutoClose{}, tables, nil
	}
	return MoleculeAutoClose{Root: closed.Issue}, tables, nil
}
