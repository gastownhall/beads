package sqlkit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// RunInTransaction executes fn inside one mutation transaction. Wisps live in
// the same database, so a single *sql.Tx serves every table; is_blocked
// reprojection runs once at commit via withMutationTx. commitMsg is Dolt
// commit-message residue and intentionally unused.
func (s *Store) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	_ = commitMsg
	return s.withMutationTx(ctx, func(tx *sql.Tx) error {
		// withWriteTx rolls back on a returned error but not on a panic; guard
		// it here so a panicking callback still releases the transaction.
		defer func() {
			if r := recover(); r != nil {
				_ = tx.Rollback()
				panic(r)
			}
		}()
		return fn(&sqlkitTx{tx: tx})
	})
}

// sqlkitTx implements storage.Transaction over a single *sql.Tx.
type sqlkitTx struct{ tx *sql.Tx }

// CreateIssueImport is the import-friendly creation hook. The shared layer does
// not enforce prefix validation, so this delegates to CreateIssue.
func (t *sqlkitTx) CreateIssueImport(ctx context.Context, issue *types.Issue, actor string, skipPrefixValidation bool) error {
	return t.CreateIssue(ctx, issue, actor)
}

// SearchIssueIDs is the narrow-projection variant of SearchIssues over the tx
// (ids only), mirroring the Dolt transaction implementations.
func (t *sqlkitTx) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	return issueops.SearchIssueIDsInTx(ctx, t.tx, query, filter)
}

// CreateIssue creates a single issue; TableRouting inside issueops routes
// wisps, so no store-level split is needed.
func (t *sqlkitTx) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	if issue == nil {
		return fmt.Errorf("issue must not be nil")
	}
	bc, err := issueops.NewBatchContext(ctx, t.tx, storage.BatchCreateOptions{SkipPrefixValidation: true})
	if err != nil {
		return err
	}
	_, err = issueops.CreateIssueInTxWithResult(ctx, t.tx, bc, issue, actor)
	return err
}

// CreateIssues creates a batch; CreateIssuesInTxWithResult routes mixed batches
// per issue and validates mixed-bucket dependencies internally.
func (t *sqlkitTx) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	if len(issues) == 0 {
		return nil
	}
	_, err := issueops.CreateIssuesInTxWithResult(ctx, t.tx, issues, actor, storage.BatchCreateOptions{
		OrphanHandling:       storage.OrphanAllow,
		SkipPrefixValidation: true,
	})
	return err
}

// UpdateIssue applies field updates without recording an event (the WithoutEvent
// variant, matching doltTransaction.UpdateIssue).
func (t *sqlkitTx) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	if rawMeta, ok := updates["metadata"]; ok {
		metadataStr, err := storage.NormalizeMetadataValue(rawMeta)
		if err != nil {
			return fmt.Errorf("invalid metadata: %w", err)
		}
		if err := validateMetadataIfConfigured(json.RawMessage(metadataStr)); err != nil {
			return err
		}
	}
	_, err := issueops.UpdateIssueWithoutEventInTx(ctx, t.tx, id, updates, actor)
	return err
}

// CloseIssue closes an issue and records the close event, matching both Dolt
// reference transactions (dolt/transaction.go:645, embeddeddolt/transaction.go:79),
// which use the with-event CloseIssueInTx. The without-event variant dropped the
// EventClosed row on bd batch / bd mol squash, diverging from the Dolt oracle.
func (t *sqlkitTx) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	_, err := issueops.CloseIssueInTx(ctx, t.tx, id, reason, actor, session)
	return err
}

// DeleteIssue deletes an issue; issueops routes wisps internally.
func (t *sqlkitTx) DeleteIssue(ctx context.Context, id string) error {
	return issueops.DeleteIssueInTx(ctx, t.tx, id)
}

// GetIssue reads an issue for read-your-writes within the transaction.
func (t *sqlkitTx) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return issueops.GetIssueInTx(ctx, t.tx, id)
}

// SearchIssues runs a filtered search within the transaction.
func (t *sqlkitTx) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	return issueops.SearchIssuesInTx(ctx, t.tx, query, filter)
}

// AddDependency adds a dependency with default options.
func (t *sqlkitTx) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	return t.AddDependencyWithOptions(ctx, dep, actor, storage.DependencyAddOptions{})
}

// AddDependencyWithOptions resolves the source/target tables and edge kind, then
// delegates to issueops. Cross-prefix and external targets are recorded as
// external edges; same-DB wisp targets route to the wisps table.
func (t *sqlkitTx) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts storage.DependencyAddOptions) error {
	writeTable, sourceTable := "dependencies", "issues"
	if issueops.IsActiveWispInTx(ctx, t.tx, dep.IssueID) {
		writeTable, sourceTable = "wisp_dependencies", "wisps"
	}

	isCrossPrefix := types.ExtractPrefix(dep.IssueID) != types.ExtractPrefix(dep.DependsOnID)
	targetTable := "issues"
	kind := issueops.DepTargetIssue
	switch {
	case isCrossPrefix, strings.HasPrefix(dep.DependsOnID, "external:"):
		kind = issueops.DepTargetExternal
	default:
		if issueops.IsActiveWispInTx(ctx, t.tx, dep.DependsOnID) {
			targetTable = "wisps"
			kind = issueops.DepTargetWisp
		}
	}

	return issueops.AddDependencyInTx(ctx, t.tx, dep, actor, issueops.AddDependencyOpts{
		SourceTable:    sourceTable,
		TargetTable:    targetTable,
		WriteTable:     writeTable,
		IsCrossPrefix:  isCrossPrefix,
		SkipCycleCheck: opts.SkipCycleCheck,
		TargetKind:     &kind,
	})
}

// RemoveDependency removes an edge; actor is unused. issueops handles routing.
func (t *sqlkitTx) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	return issueops.RemoveDependencyInTx(ctx, t.tx, issueID, dependsOnID)
}

// GetDependencyRecords returns the raw dependency rows for an issue. Targets are
// resolved via DepTargetExpr (the split physical columns), never the generated
// depends_on_id column.
func (t *sqlkitTx) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	table := "dependencies"
	if issueops.IsActiveWispInTx(ctx, t.tx, issueID) {
		table = "wisp_dependencies"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT issue_id, %s AS depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM %s
		WHERE issue_id = ?
	`, issueops.DepTargetExpr, table), issueID)
	if err != nil {
		return nil, fmt.Errorf("get dependency records in tx: %w", err)
	}
	defer rows.Close()

	var deps []*types.Dependency
	for rows.Next() {
		var d types.Dependency
		var metadata sql.NullString
		var threadID sql.NullString
		if err := rows.Scan(&d.IssueID, &d.DependsOnID, &d.Type, &d.CreatedAt, &d.CreatedBy, &metadata, &threadID); err != nil {
			return nil, fmt.Errorf("get dependency records in tx: %w", err)
		}
		if metadata.Valid {
			d.Metadata = metadata.String
		}
		if threadID.Valid {
			d.ThreadID = threadID.String
		}
		deps = append(deps, &d)
	}
	return deps, rows.Err()
}

// CycleThroughEdges reports a scheduling cycle through one of the new edges. The
// graph merges both dependency tables so uncommitted wisp and permanent edges
// are gated together.
func (t *sqlkitTx) CycleThroughEdges(ctx context.Context, edges [][2]string) (string, error) {
	graph := make(map[string][]string)
	if err := issueops.AppendSchedulingGraphInTx(ctx, t.tx, []string{"dependencies"}, graph); err != nil {
		return "", err
	}
	if err := issueops.AppendSchedulingGraphInTx(ctx, t.tx, []string{"wisp_dependencies"}, graph); err != nil {
		return "", err
	}
	return issueops.CycleThroughEdgesInGraph(graph, edges), nil
}

// AddLabel adds a label and records the label-added event, matching both Dolt
// reference transactions (dolt/transaction.go:784, embeddeddolt/transaction.go:148),
// which delegate to issueops.AddLabelInTx. Empty table args let issueops route the
// issue vs wisp label/event tables. The prior bare INSERT dropped the
// EventLabelAdded row on every SQL-backend label mutation.
func (t *sqlkitTx) AddLabel(ctx context.Context, issueID, label, actor string) error {
	return issueops.AddLabelInTx(ctx, t.tx, "", "", issueID, label, actor)
}

// RemoveLabel removes a label and records the label-removed event, matching both
// Dolt reference transactions (dolt/transaction.go:824, embeddeddolt/transaction.go:153),
// which delegate to issueops.RemoveLabelInTx.
func (t *sqlkitTx) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	return issueops.RemoveLabelInTx(ctx, t.tx, "", "", issueID, label, actor)
}

// GetLabels returns an issue's labels; empty table arg auto-routes wisps.
func (t *sqlkitTx) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	return issueops.GetLabelsInTx(ctx, t.tx, "", issueID)
}

// SetConfig sets a config value within the transaction and syncs the normalized
// custom_statuses/custom_types tables for the relevant keys, matching the
// embedded-Dolt reference (embeddeddolt/transaction.go) and Store.SetConfig.
// Without this, tx-path writes of status.custom/types.custom leave those tables
// stale, so GetCustomStatuses/GetCustomTypes (which read them) diverge.
func (t *sqlkitTx) SetConfig(ctx context.Context, key, value string) error {
	if err := issueops.SetConfigInTx(ctx, t.tx, key, value); err != nil {
		return err
	}
	switch key {
	case "status.custom":
		if err := issueops.SyncCustomStatusesTable(ctx, t.tx, value); err != nil {
			return fmt.Errorf("syncing custom_statuses table: %w", err)
		}
	case "types.custom":
		if err := issueops.SyncCustomTypesTable(ctx, t.tx, value); err != nil {
			return fmt.Errorf("syncing custom_types table: %w", err)
		}
	}
	return nil
}

// GetConfig gets a config value within the transaction ("" when absent).
func (t *sqlkitTx) GetConfig(ctx context.Context, key string) (string, error) {
	return issueops.GetConfigInTx(ctx, t.tx, key)
}

// SetMetadata sets a metadata value within the transaction.
func (t *sqlkitTx) SetMetadata(ctx context.Context, key, value string) error {
	return issueops.SetMetadataInTx(ctx, t.tx, key, value)
}

// GetMetadata gets a metadata value within the transaction ("" when absent).
func (t *sqlkitTx) GetMetadata(ctx context.Context, key string) (string, error) {
	return issueops.GetMetadataInTx(ctx, t.tx, key)
}

// SetLocalMetadata writes a clone-local value; same DB, no ignored-tx split.
func (t *sqlkitTx) SetLocalMetadata(ctx context.Context, key, value string) error {
	return issueops.SetLocalMetadataInTx(ctx, t.tx, key, value)
}

// GetLocalMetadata reads a clone-local value ("" when absent).
func (t *sqlkitTx) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	return issueops.GetLocalMetadataInTx(ctx, t.tx, key)
}

// AddComment records a comment-as-event within the transaction.
func (t *sqlkitTx) AddComment(ctx context.Context, issueID, actor, comment string) error {
	return issueops.AddCommentEventInTx(ctx, t.tx, issueID, actor, comment)
}

// ImportIssueComment adds a structured comment preserving createdAt. issueops
// verifies the issue exists before inserting.
func (t *sqlkitTx) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	return issueops.ImportIssueCommentInTx(ctx, t.tx, issueID, author, text, createdAt)
}

// GetIssueComments returns an issue's comments within the transaction.
func (t *sqlkitTx) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	return issueops.GetIssueCommentsInTx(ctx, t.tx, issueID)
}
