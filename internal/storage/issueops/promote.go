package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

//nolint:gosec // G201: table names are hardcoded constants
func PromoteFromEphemeralInTx(ctx context.Context, tx DBTX, id string, actor string) error {
	var destinationRows int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE id = ?`, id).Scan(&destinationRows); err != nil {
		return fmt.Errorf("check promotion destination for %s: %w", id, err)
	}
	if destinationRows != 0 {
		return fmt.Errorf("cannot promote wisp %s: destination already exists in issues", id)
	}

	issue, err := getIssueFromTableInTx(ctx, tx, "wisps", "wisp_labels", id)
	if err != nil {
		return fmt.Errorf("get wisp for promote: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("wisp %s not found", id)
	}

	// Promotion selects durable history, so clear both wisp-plane markers.
	// NoHistory rows are real wisps even though Ephemeral is already false.
	issue.Ephemeral = false
	issue.NoHistory = false

	customStatuses, err := ResolveCustomStatusesDetailedInTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := ResolveCustomTypesInTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("failed to get custom types: %w", err)
	}
	if err := PrepareIssueForInsert(issue, types.CustomStatusNames(customStatuses), customTypes); err != nil {
		return fmt.Errorf("promote wisp to issues: %w", err)
	}
	if err := InsertIssueStrictInTx(ctx, tx, "issues", issue); err != nil {
		return fmt.Errorf("promote wisp to issues: %w", err)
	}

	for _, aux := range promotionAuxiliaryTables {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s (%s)
			SELECT %s FROM %s WHERE %s = ?
		`, aux.destination, aux.columns, aux.columns, aux.source, aux.key), id); err != nil {
			return fmt.Errorf("copy %s for promoted wisp %s: %w", aux.name, id, err)
		}
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE %s = ?`, aux.source, aux.key), id); err != nil {
			return fmt.Errorf("delete copied wisp %s for promoted wisp %s: %w", aux.name, id, err)
		}
	}

	if err := RetargetInboundDependenciesToIssueInTx(ctx, tx, id); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM wisps WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete promoted wisp row %s: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get promoted wisp rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("wisp %s not found", id)
	}

	affectedIssues, affectedWisps, aerr := AffectedByStatusChangeInTx(ctx, tx, id)
	if aerr != nil {
		return fmt.Errorf("affected by promote for %s: %w", id, aerr)
	}
	if err := RecomputeIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
		return fmt.Errorf("recompute is_blocked after promote for %s: %w", id, err)
	}
	return nil
}

var promotionAuxiliaryTables = []struct {
	name        string
	source      string
	destination string
	columns     string
	key         string
}{
	{"labels", "wisp_labels", "labels", "issue_id, label", "issue_id"},
	{"dependencies", "wisp_dependencies", "dependencies", "id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id", "issue_id"},
	{"events", "wisp_events", "events", "id, issue_id, event_type, actor, old_value, new_value, comment, created_at", "issue_id"},
	{"comments", "wisp_comments", "comments", "id, issue_id, author, text, created_at", "issue_id"},
	{"child counters", "wisp_child_counters", "child_counters", "parent_id, last_child", "parent_id"},
}

// PromoteWispIfDurableInTx repairs the physical plane after an update.  A row
// remains a runtime wisp while either marker is set; once both are clear it is
// fully durable and must move to issues before the transaction commits.
func PromoteWispIfDurableInTx(ctx context.Context, tx DBTX, id, actor string) (bool, error) {
	if !IsActiveWispInTx(ctx, tx, id) {
		return false, nil
	}
	issue, err := getIssueFromTableInTx(ctx, tx, "wisps", "wisp_labels", id)
	if err != nil {
		return false, fmt.Errorf("read wisp persistence after update: %w", err)
	}
	if issue == nil {
		return false, fmt.Errorf("read wisp persistence after update: wisp %s not found", id)
	}
	if issue.Ephemeral || issue.NoHistory {
		return false, nil
	}
	if err := PromoteFromEphemeralInTx(ctx, tx, id, actor); err != nil {
		return false, err
	}
	return true, nil
}
