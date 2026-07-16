package issueops

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/labelns"
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
// Uses INSERT IGNORE for idempotency.
func AddLabelInTx(ctx context.Context, tx DBTX, labelTable, eventTable, issueID, label, actor string) error {
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
	if err := checkExclusiveLabelInTx(ctx, tx, labelTable, issueID, label); err != nil {
		return err
	}
	//nolint:gosec // G201: labelTable is from WispTableRouting ("labels" or "wisp_labels")
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT IGNORE INTO %s (issue_id, label) VALUES (?, ?)`, labelTable), issueID, label); err != nil {
		return fmt.Errorf("add label: %w", err)
	}
	comment := "Added label: " + label
	//nolint:gosec // G201: eventTable is from WispTableRouting ("events" or "wisp_events")
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (id, issue_id, event_type, actor, comment) VALUES (?, ?, ?, ?, ?)`, eventTable),
		NewEventID(), issueID, types.EventLabelAdded, actor, comment); err != nil {
		return fmt.Errorf("add label: record event: %w", err)
	}
	return nil
}

// checkExclusiveLabelInTx rejects the add when the label falls in a
// configured exclusive namespace (labels.exclusive-prefixes, bd-7u5ki) the
// issue already carries a different label in. It lives at the shared insert
// choke point so every interactive mutation path — bd label add, bd update
// --add-label, bd label propagate — gets the same guard. With no prefixes
// configured (the default) only the config lookup runs.
func checkExclusiveLabelInTx(ctx context.Context, tx DBTX, labelTable, issueID, label string) error {
	raw, err := GetConfigInTx(ctx, tx, labelns.ConfigKey)
	if err != nil {
		return fmt.Errorf("add label: %w", err)
	}
	prefixes := labelns.ParsePrefixes(raw)
	if len(prefixes) == 0 {
		return nil
	}
	prefix := labelns.Match(prefixes, label)
	if prefix == "" {
		return nil
	}
	existing, err := GetLabelsInTx(ctx, tx, labelTable, issueID)
	if err != nil {
		return fmt.Errorf("add label: %w", err)
	}
	for _, have := range existing {
		if have != label && labelns.Match(prefixes, have) == prefix {
			return fmt.Errorf("cannot add label %q to %s: namespace %q is exclusive (%s) and the issue already has %q — remove it first, or swap in one step with 'bd label add --replace'",
				label, issueID, prefix, labelns.ConfigKey, have)
		}
	}
	return nil
}

// ExclusiveLabelViolation reports an issue carrying more than one label in a
// configured exclusive namespace.
type ExclusiveLabelViolation struct {
	IssueID string
	Prefix  string
	Labels  []string
}

// FindExclusiveLabelViolations scans the permanent labels table for
// non-closed issues violating the given exclusive prefixes. bd doctor uses it
// so adopters can find pre-existing violations before (or after) enabling
// labels.exclusive-prefixes — write-path enforcement only guards new label
// additions. Closed issues are skipped (no routing consumer reads them), as
// are wisp labels (wisps are ephemeral and expire via TTL compaction).
func FindExclusiveLabelViolations(ctx context.Context, db DBTX, prefixes []string) ([]ExclusiveLabelViolation, error) {
	if len(prefixes) == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT l.issue_id, l.label
		FROM labels l JOIN issues i ON i.id = l.issue_id
		WHERE i.status != 'closed'
		ORDER BY l.issue_id, l.label`)
	if err != nil {
		return nil, fmt.Errorf("scan exclusive labels: %w", err)
	}
	defer rows.Close()

	labelsByIssue := make(map[string][]string)
	var issueOrder []string
	for rows.Next() {
		var issueID, label string
		if err := rows.Scan(&issueID, &label); err != nil {
			return nil, fmt.Errorf("scan exclusive labels: %w", err)
		}
		if _, ok := labelsByIssue[issueID]; !ok {
			issueOrder = append(issueOrder, issueID)
		}
		labelsByIssue[issueID] = append(labelsByIssue[issueID], label)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan exclusive labels: %w", err)
	}

	var violations []ExclusiveLabelViolation
	for _, issueID := range issueOrder {
		for _, c := range labelns.Conflicts(prefixes, labelsByIssue[issueID]) {
			violations = append(violations, ExclusiveLabelViolation{IssueID: issueID, Prefix: c.Prefix, Labels: c.Labels})
		}
	}
	return violations, nil
}

// RemoveLabelInTx removes a label from an issue and records an event within
// an existing transaction. Automatically routes to wisp tables if the ID is
// an active wisp.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func RemoveLabelInTx(ctx context.Context, tx DBTX, labelTable, eventTable, issueID, label, actor string) error {
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
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE issue_id = ? AND label = ?`, labelTable), issueID, label); err != nil {
		return fmt.Errorf("remove label: %w", err)
	}
	comment := "Removed label: " + label
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (id, issue_id, event_type, actor, comment) VALUES (?, ?, ?, ?, ?)`, eventTable),
		NewEventID(), issueID, types.EventLabelRemoved, actor, comment); err != nil {
		return fmt.Errorf("remove label: record event: %w", err)
	}
	return nil
}
