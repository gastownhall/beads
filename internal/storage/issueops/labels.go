package issueops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// GetLabelsInTx retrieves all labels for an issue within an existing transaction.
// Automatically routes to wisp_labels if the ID is an active wisp.
// Returns labels sorted alphabetically.
func GetLabelsInTx(ctx context.Context, tx DBTX, table, issueID string) ([]string, error) {
	if table == "" {
		isWisp := IsActiveWispInTx(ctx, tx, issueID)
		_, table, _, _ = WispTableRouting(isWisp)
	}
	//nolint:gosec // G201: table is from WispTableRouting ("labels" or "wisp_labels")
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT label FROM %s WHERE issue_id = ? ORDER BY label`, table), issueID)
	if err != nil {
		return nil, fmt.Errorf("get labels: %w", err)
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, fmt.Errorf("get labels: scan: %w", err)
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// GetLabelsForIssuesInTx fetches labels for multiple issues in a single transaction.
// Routes each ID to labels or wisp_labels based on wisp status.
// Uses a single batched wisp-partition query plus batched IN clauses per label
// table, so the number of round-trips is O(1 + N/queryBatchSize) rather than
// O(N). This matters on remote backends (Dolt) where per-ID round-trips would
// otherwise blow past the context deadline — see GH#3414.
//
// Callers hydrating multiple batches inside one tx may pass a precomputed
// active-wisp set scoped to issueIDs to avoid rebuilding it.
func GetLabelsForIssuesInTx(ctx context.Context, tx DBTX, issueIDs []string, wispSetOpt ...map[string]struct{}) (map[string][]string, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]string), nil
	}

	var wispIDs, permIDs []string
	if len(wispSetOpt) > 0 && wispSetOpt[0] != nil {
		wispIDs, permIDs = partitionByWispSet(issueIDs, wispSetOpt[0])
	} else {
		var err error
		wispIDs, permIDs, err = PartitionWispIDsInTx(ctx, tx, issueIDs)
		if err != nil {
			return nil, err
		}
	}

	result := make(map[string][]string)
	if len(wispIDs) > 0 {
		if err := getLabelsIntoFromTable(ctx, tx, "wisp_labels", wispIDs, result); err != nil {
			return nil, err
		}
	}
	if len(permIDs) > 0 {
		if err := getLabelsIntoFromTable(ctx, tx, "labels", permIDs, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// GetLabelsForIssuesFromTableInTx is a fast path for callers that already know
// which label table applies to every ID in the batch (e.g. searchTableInTx,
// which queries either the issues or wisps table exclusively). It skips the
// wisp-partition round-trip entirely. labelTable must be "labels" or
// "wisp_labels"; callers route via FilterTables.
func GetLabelsForIssuesFromTableInTx(ctx context.Context, tx DBTX, labelTable string, issueIDs []string) (map[string][]string, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]string), nil
	}
	result := make(map[string][]string)
	if err := getLabelsIntoFromTable(ctx, tx, labelTable, issueIDs, result); err != nil {
		return nil, err
	}
	return result, nil
}

// getLabelsIntoFromTable executes the batched SELECT for a single label table
// and accumulates results into the provided map.
//
//nolint:gosec // G201: labelTable is "labels" or "wisp_labels" (hardcoded by callers).
func getLabelsIntoFromTable(ctx context.Context, tx DBTX, labelTable string, ids []string, result map[string][]string) error {
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, label FROM %s WHERE issue_id IN (%s) ORDER BY issue_id, label`,
			labelTable, strings.Join(placeholders, ",")), args...)
		if err != nil {
			return fmt.Errorf("get labels for issues from %s: %w", labelTable, err)
		}
		for rows.Next() {
			var issueID, label string
			if err := rows.Scan(&issueID, &label); err != nil {
				_ = rows.Close()
				return fmt.Errorf("get labels for issues: scan: %w", err)
			}
			result[issueID] = append(result[issueID], label)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("get labels for issues: rows: %w", err)
		}
	}
	return nil
}

// AddLabelInTx adds a label to an issue and records an event within an existing
// transaction. Automatically routes to wisp tables if the ID is an active wisp.
// Uses INSERT IGNORE for idempotency. A label is part of the issue's durable
// state, so a label that was actually inserted mints a version row; an
// idempotent re-add (INSERT IGNORE affecting no row) mints nothing.
func AddLabelInTx(ctx context.Context, tx DBTX, labelTable, eventTable, issueID, label, actor string) error {
	return addLabelInTx(ctx, tx, labelTable, eventTable, issueID, label, actor, true)
}

// addLabelInTx is the body of AddLabelInTx. mintVersion controls whether an
// inserted label mints its own version row: the exported entry point always
// does, while a label patch (applyLabelPatch) writes several rows for one
// caller-visible mutation and mints once after the last of them.
func addLabelInTx(ctx context.Context, tx DBTX, labelTable, eventTable, issueID, label, actor string, mintVersion bool) error {
	// Reject an over-length label up front. The INSERT IGNORE below would
	// otherwise silently truncate it to the VARCHAR(255) column, storing a label
	// the caller never sent; a typed ErrFieldTooLong is the clean rejection.
	if err := types.CheckFieldLen("label", label); err != nil {
		return err
	}
	if labelTable == "" || eventTable == "" {
		isWisp := IsActiveWispInTx(ctx, tx, issueID)
		_, lt, et, _ := WispTableRouting(isWisp)
		if labelTable == "" {
			labelTable = lt
		}
		if eventTable == "" {
			eventTable = et
		}
	}
	//nolint:gosec // G201: labelTable is from WispTableRouting ("labels" or "wisp_labels")
	res, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT IGNORE INTO %s (issue_id, label) VALUES (?, ?)`, labelTable), issueID, label)
	if err != nil {
		return fmt.Errorf("add label: %w", err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("add label: rows affected: %w", err)
	}
	comment := "Added label: " + label
	if err := InsertDerivedEvent(ctx, tx, eventTable, AuxEvent{
		IssueID:   issueID,
		EventType: types.EventLabelAdded,
		Actor:     actor,
		Comment:   str(comment),
	}); err != nil {
		return fmt.Errorf("add label: record event: %w", err)
	}
	// A label is part of the bead snapshot, so a label write journals as an
	// update carrying the complete post-mutation set.
	if err := RecordEventInTx(ctx, tx, EventUpdate, issueID, actor); err != nil {
		return err
	}
	// Version only when a row landed: INSERT IGNORE on an existing label is
	// the no-op case, and a no-op mints nothing (the same gate the dependency
	// helpers draw on their inserted/deleted row).
	if !mintVersion || inserted == 0 {
		return nil
	}
	return RecordVersionInTx(ctx, tx, issueID, actor)
}

// RemoveLabelInTx removes a label from an issue and records an event within
// an existing transaction. Automatically routes to wisp tables if the ID is
// an active wisp. A deleted label row mints a version row; a DELETE that
// matched no row is a no-op and mints nothing.
func RemoveLabelInTx(ctx context.Context, tx DBTX, labelTable, eventTable, issueID, label, actor string) error {
	return removeLabelInTx(ctx, tx, labelTable, eventTable, issueID, label, actor, true)
}

// removeLabelInTx is the body of RemoveLabelInTx; mintVersion is
// addLabelInTx's, for the same reason.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func removeLabelInTx(ctx context.Context, tx DBTX, labelTable, eventTable, issueID, label, actor string, mintVersion bool) error {
	if labelTable == "" || eventTable == "" {
		isWisp := IsActiveWispInTx(ctx, tx, issueID)
		_, lt, et, _ := WispTableRouting(isWisp)
		if labelTable == "" {
			labelTable = lt
		}
		if eventTable == "" {
			eventTable = et
		}
	}
	res, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE issue_id = ? AND label = ?`, labelTable), issueID, label)
	if err != nil {
		return fmt.Errorf("remove label: %w", err)
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove label: rows affected: %w", err)
	}
	comment := "Removed label: " + label
	if err := InsertDerivedEvent(ctx, tx, eventTable, AuxEvent{
		IssueID:   issueID,
		EventType: types.EventLabelRemoved,
		Actor:     actor,
		Comment:   str(comment),
	}); err != nil {
		return fmt.Errorf("remove label: record event: %w", err)
	}
	if err := RecordEventInTx(ctx, tx, EventUpdate, issueID, actor); err != nil {
		return err
	}
	if !mintVersion || deleted == 0 {
		return nil
	}
	return RecordVersionInTx(ctx, tx, issueID, actor)
}
