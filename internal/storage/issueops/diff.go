package issueops

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// DiffInTx returns changes between two commits or branches by querying
// Dolt's dolt_diff() table function.
//
// nolint:gosec // G201: refs are validated by ValidateRef() - dolt_diff requires literal refs
func DiffInTx(ctx context.Context, tx *sql.Tx, fromRef, toRef string) ([]*storage.DiffEntry, error) {
	if err := ValidateRef(fromRef); err != nil {
		return nil, fmt.Errorf("invalid fromRef: %w", err)
	}
	if err := ValidateRef(toRef); err != nil {
		return nil, fmt.Errorf("invalid toRef: %w", err)
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(from_id, '') as from_id,
			COALESCE(to_id, '') as to_id,
			diff_type,
			from_title, to_title,
			from_description, to_description,
			from_status, to_status,
			from_priority, to_priority
		FROM dolt_diff('%s', '%s', 'issues')
	`, fromRef, toRef)

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get diff: %w", err)
	}
	defer rows.Close()

	var entries []*storage.DiffEntry
	for rows.Next() {
		var fromID, toID, diffType string
		var fromTitle, toTitle, fromDesc, toDesc, fromStatus, toStatus *string
		var fromPriority, toPriority *int

		if err := rows.Scan(&fromID, &toID, &diffType,
			&fromTitle, &toTitle,
			&fromDesc, &toDesc,
			&fromStatus, &toStatus,
			&fromPriority, &toPriority); err != nil {
			return nil, fmt.Errorf("failed to scan diff: %w", err)
		}

		entry := &storage.DiffEntry{
			DiffType: diffType,
		}

		if toID != "" {
			entry.IssueID = toID
		} else {
			entry.IssueID = fromID
		}

		// Build old value for modified/removed
		if diffType != "added" && fromID != "" {
			entry.OldValue = &types.Issue{
				ID: fromID,
			}
			if fromTitle != nil {
				entry.OldValue.Title = *fromTitle
			}
			if fromDesc != nil {
				entry.OldValue.Description = *fromDesc
			}
			if fromStatus != nil {
				entry.OldValue.Status = types.Status(*fromStatus)
			}
			if fromPriority != nil {
				entry.OldValue.Priority = *fromPriority
			}
		}

		// Build new value for modified/added
		if diffType != "removed" && toID != "" {
			entry.NewValue = &types.Issue{
				ID: toID,
			}
			if toTitle != nil {
				entry.NewValue.Title = *toTitle
			}
			if toDesc != nil {
				entry.NewValue.Description = *toDesc
			}
			if toStatus != nil {
				entry.NewValue.Status = types.Status(*toStatus)
			}
			if toPriority != nil {
				entry.NewValue.Priority = *toPriority
			}
		}

		entries = append(entries, entry)
	}

	return entries, rows.Err()
}
