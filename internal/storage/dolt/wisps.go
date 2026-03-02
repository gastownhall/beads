package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// Wisp table routing helpers.
// Wisps are stored in dolt_ignored tables (wisps, wisp_labels, wisp_dependencies,
// wisp_events, wisp_comments) to avoid Dolt history bloat. All operations use the
// same Dolt SQL connection — no separate store or transaction routing needed.


// insertIssueIntoTable inserts an issue into the specified table,
// using ON DUPLICATE KEY UPDATE to handle pre-existing records gracefully (GH#2061).
// The table must be either "issues" or "wisps" (same schema).
//
//nolint:gosec // G201: table is a hardcoded constant ("issues" or "wisps")
func insertIssueIntoTable(ctx context.Context, tx *sql.Tx, table string, issue *types.Issue) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, closed_at, external_ref, spec_id,
			compaction_level, compacted_at, compacted_at_commit, original_size,
			sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
			mol_type, work_type, quality_score, source_system, source_repo, close_reason,
			event_kind, actor, target, payload,
			await_type, await_id, timeout_ns, waiters,
			hook_bead, role_bead, agent_state, last_activity, role_type, rig,
			due_at, defer_until, metadata
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?
		)
		ON DUPLICATE KEY UPDATE
			content_hash = VALUES(content_hash),
			title = VALUES(title),
			description = VALUES(description),
			design = VALUES(design),
			acceptance_criteria = VALUES(acceptance_criteria),
			notes = VALUES(notes),
			status = VALUES(status),
			priority = VALUES(priority),
			issue_type = VALUES(issue_type),
			assignee = VALUES(assignee),
			estimated_minutes = VALUES(estimated_minutes),
			updated_at = VALUES(updated_at),
			closed_at = VALUES(closed_at),
			external_ref = VALUES(external_ref),
			source_repo = VALUES(source_repo),
			close_reason = VALUES(close_reason),
			metadata = VALUES(metadata)
	`, table),
		issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes,
		issue.Status, issue.Priority, issue.IssueType, nullString(issue.Assignee), nullInt(issue.EstimatedMinutes),
		issue.CreatedAt, issue.CreatedBy, issue.Owner, issue.UpdatedAt, issue.ClosedAt, nullStringPtr(issue.ExternalRef), issue.SpecID,
		issue.CompactionLevel, issue.CompactedAt, nullStringPtr(issue.CompactedAtCommit), nullIntVal(issue.OriginalSize),
		issue.Sender, issue.Ephemeral, issue.WispType, issue.Pinned, issue.IsTemplate, issue.Crystallizes,
		issue.MolType, issue.WorkType, issue.QualityScore, issue.SourceSystem, issue.SourceRepo, issue.CloseReason,
		issue.EventKind, issue.Actor, issue.Target, issue.Payload,
		issue.AwaitType, issue.AwaitID, issue.Timeout.Nanoseconds(), formatJSONStringArray(issue.Waiters),
		issue.HookBead, issue.RoleBead, issue.AgentState, issue.LastActivity, issue.RoleType, issue.Rig,
		issue.DueAt, issue.DeferUntil, jsonMetadata(issue.Metadata),
	)
	return wrapExecError("insert issue into table", err)
}

// scanIssueFromTable scans a single issue from the specified table.
//
//nolint:gosec // G201: table is a hardcoded constant ("issues" or "wisps")
func scanIssueFromTable(ctx context.Context, db *sql.DB, table, id string) (*types.Issue, error) {
	row := db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE id = ?
	`, issueSelectColumns, table), id)

	issue, err := scanIssueFrom(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue from %s: %w", table, err)
	}
	return issue, nil
}

// recordEventInTable records an event in the specified events table.
//
//nolint:gosec // G201: table is a hardcoded constant ("events" or "wisp_events")
func recordEventInTable(ctx context.Context, tx *sql.Tx, table, issueID string, eventType types.EventType, actor, newValue string) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, table), issueID, eventType, actor, "", newValue)
	return wrapExecError("record event in table", err)
}

// generateIssueIDInTable generates a unique ID, checking for collisions
// in the specified table. Supports counter mode for non-ephemeral issues.
//
//nolint:gosec // G201: table is a hardcoded constant
func generateIssueIDInTable(ctx context.Context, tx *sql.Tx, table, prefix string, issue *types.Issue, actor string) (string, error) {
	// Counter mode only applies to the issues table (not wisps).
	if table == "issues" {
		counterMode, err := isCounterModeTx(ctx, tx)
		if err != nil {
			return "", err
		}
		if counterMode {
			return nextCounterIDTx(ctx, tx, prefix)
		}
	}

	baseLength := getAdaptiveIDLengthFromTable(ctx, tx, table, prefix)

	var err error
	maxLength := 8
	if baseLength > maxLength {
		baseLength = maxLength
	}

	for length := baseLength; length <= maxLength; length++ {
		for nonce := 0; nonce < 10; nonce++ {
			candidate := generateHashID(prefix, issue.Title, issue.Description, actor, issue.CreatedAt, length, nonce)

			var count int
			err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id = ?`, table), candidate).Scan(&count) //nolint:gosec // G201
			if err != nil {
				return "", fmt.Errorf("failed to check for ID collision: %w", err)
			}

			if count == 0 {
				return candidate, nil
			}
		}
	}

	return "", fmt.Errorf("failed to generate unique ID after trying lengths %d-%d with 10 nonces each", baseLength, maxLength)
}

// getAdaptiveIDLengthFromTable returns the adaptive ID length based on table size.
//
//nolint:gosec // G201: table is a hardcoded constant
func getAdaptiveIDLengthFromTable(ctx context.Context, tx *sql.Tx, table, prefix string) int {
	var count int
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id LIKE ?`, table), prefix+"%").Scan(&count); err != nil {
		return 4 // Default for wisps (small tables)
	}

	switch {
	case count < 100:
		return 4
	case count < 1000:
		return 5
	case count < 10000:
		return 6
	default:
		return 7
	}
}

// insertIssueTxIntoTable is the transaction-context version for inserting into a named table.
// Delegates to insertIssueIntoTable to ensure all columns are written.
func insertIssueTxIntoTable(ctx context.Context, tx *sql.Tx, table string, issue *types.Issue) error {
	return insertIssueIntoTable(ctx, tx, table, issue)
}

// scanIssueTxFromTable scans a full issue from a named table within a transaction.
// Delegates to the unified scanIssueFrom to ensure all columns are hydrated.
//
//nolint:gosec // G201: table is a hardcoded constant ("issues" or "wisps")
func scanIssueTxFromTable(ctx context.Context, tx *sql.Tx, table, id string) (*types.Issue, error) {
	row := tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT %s FROM %s WHERE id = ?
	`, issueSelectColumns, table), id)

	issue, err := scanIssueFrom(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: issue %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, wrapScanError("scan issue from "+table, err)
	}
	return issue, nil
}

// wispPrefix returns the ID prefix for wisp ID generation.
// Uses IDPrefix if set (e.g., IDPrefix="wisp" → "bd-wisp"), otherwise
// appends "-wisp" to the config prefix (e.g., "bd" → "bd-wisp").
func wispPrefix(configPrefix string, issue *types.Issue) string {
	if issue.PrefixOverride != "" {
		return issue.PrefixOverride
	}
	if issue.IDPrefix != "" {
		return configPrefix + "-" + issue.IDPrefix
	}
	return configPrefix + "-wisp"
}

// createWisp creates an issue in the wisps table.
func (s *DoltStore) createWisp(ctx context.Context, issue *types.Issue, actor string) error {
	issue.Ephemeral = true

	// Fetch custom statuses and types for validation (parity with CreateIssue)
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err := s.GetCustomTypes(ctx)
	if err != nil {
		return fmt.Errorf("failed to get custom types: %w", err)
	}

	now := time.Now().UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	} else {
		issue.CreatedAt = issue.CreatedAt.UTC()
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	} else {
		issue.UpdatedAt = issue.UpdatedAt.UTC()
	}

	if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		closedAt := maxTime.Add(time.Second)
		issue.ClosedAt = &closedAt
	}

	// Validate issue fields (parity with CreateIssue)
	if err := issue.ValidateWithCustom(customStatuses, customTypes); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Get prefix from config
	var configPrefix string
	err = tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&configPrefix)
	if err == sql.ErrNoRows || configPrefix == "" {
		return fmt.Errorf("database not initialized: issue_prefix config is missing (run 'bd init --prefix <prefix>' first)")
	} else if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	// Normalize prefix: strip trailing hyphen to prevent double-hyphen IDs (bd-6uly)
	configPrefix = strings.TrimSuffix(configPrefix, "-")

	// Generate wisp ID if not provided
	if issue.ID == "" {
		prefix := wispPrefix(configPrefix, issue)
		generatedID, err := generateIssueIDInTable(ctx, tx, "wisps", prefix, issue, actor)
		if err != nil {
			return fmt.Errorf("failed to generate wisp ID: %w", err)
		}
		issue.ID = generatedID
	}

	if err := insertIssueIntoTable(ctx, tx, "wisps", issue); err != nil {
		return fmt.Errorf("failed to insert wisp: %w", err)
	}

	if err := recordEventInTable(ctx, tx, "wisp_events", issue.ID, types.EventCreated, actor, ""); err != nil {
		return fmt.Errorf("failed to record creation event: %w", err)
	}

	return wrapTransactionError("commit create wisp", tx.Commit())
}

// getWisp retrieves an issue from the wisps table.
func (s *DoltStore) getWisp(ctx context.Context, id string) (*types.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	issue, err := scanIssueFromTable(ctx, s.db, "wisps", id)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, nil
	}
	labels, err := s.getWispLabels(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp labels: %w", err)
	}
	issue.Labels = labels
	return issue, nil
}

// getWispLabels retrieves labels from the wisp_labels table.
func (s *DoltStore) getWispLabels(ctx context.Context, issueID string) ([]string, error) {
	rows, err := s.queryContext(ctx, `SELECT label FROM wisp_labels WHERE issue_id = ? ORDER BY label`, issueID)
	if err != nil {
		return nil, wrapQueryError("get wisp labels", err)
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, wrapScanError("scan wisp label", err)
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// updateWisp updates fields on a wisp in the wisps table.
func (s *DoltStore) updateWisp(ctx context.Context, id string, updates map[string]interface{}, _ string) error {
	// Get old wisp for closed_at auto-management
	oldWisp, err := s.getWisp(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get wisp for update: %w", err)
	}

	setClauses := []string{"updated_at = ?"}
	args := []interface{}{time.Now().UTC()}

	for key, value := range updates {
		if !isAllowedUpdateField(key) {
			return fmt.Errorf("invalid field for update: %s", key)
		}
		columnName := key
		if key == "wisp" {
			columnName = "ephemeral"
		}
		setClauses = append(setClauses, fmt.Sprintf("`%s` = ?", columnName))
		if key == "waiters" {
			waitersJSON, err := json.Marshal(value)
			if err != nil {
				return fmt.Errorf("invalid waiters: %w", err)
			}
			args = append(args, string(waitersJSON))
		} else if key == "metadata" {
			metadataStr, err := storage.NormalizeMetadataValue(value)
			if err != nil {
				return fmt.Errorf("invalid metadata: %w", err)
			}
			args = append(args, metadataStr)
		} else {
			args = append(args, value)
		}
	}

	// Auto-manage closed_at (set on close, clear on reopen)
	setClauses, args = manageClosedAt(oldWisp, updates, setClauses, args)

	args = append(args, id)

	// nolint:gosec // G201: setClauses contains only column names
	query := fmt.Sprintf("UPDATE wisps SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	_, err = s.execContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to update wisp: %w", err)
	}
	return nil
}

// closeWisp closes a wisp in the wisps table.
func (s *DoltStore) closeWisp(ctx context.Context, id string, reason string, actor string, session string) error {
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE wisps SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?, closed_by_session = ?
		WHERE id = ?
	`, types.StatusClosed, now, now, reason, session, id)
	if err != nil {
		return fmt.Errorf("failed to close wisp: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("wisp not found: %s", id)
	}

	if err := recordEventInTable(ctx, tx, "wisp_events", id, types.EventClosed, actor, reason); err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	return wrapTransactionError("commit close wisp", tx.Commit())
}

// deleteWisp permanently removes a wisp and its related data.
func (s *DoltStore) deleteWisp(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete from auxiliary tables
	for _, table := range []string{"wisp_dependencies", "wisp_events", "wisp_comments", "wisp_labels"} {
		if table == "wisp_dependencies" {
			_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ? OR depends_on_id = ?", table), id, id) //nolint:gosec // G201: table is hardcoded
		} else {
			_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ?", table), id) //nolint:gosec // G201: table is hardcoded
		}
		if err != nil {
			return fmt.Errorf("failed to delete from %s: %w", table, err)
		}
	}

	result, err := tx.ExecContext(ctx, "DELETE FROM wisps WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete wisp: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("wisp not found: %s", id)
	}

	return wrapTransactionError("commit delete wisp", tx.Commit())
}

// claimWisp atomically claims a wisp.
func (s *DoltStore) claimWisp(ctx context.Context, id string, actor string) error {
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE wisps
		SET assignee = ?, status = 'in_progress', updated_at = ?
		WHERE id = ? AND (assignee = '' OR assignee IS NULL)
	`, actor, now, id)
	if err != nil {
		return fmt.Errorf("failed to claim wisp: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		var currentAssignee string
		err := tx.QueryRowContext(ctx, `SELECT assignee FROM wisps WHERE id = ?`, id).Scan(&currentAssignee)
		if err != nil {
			return fmt.Errorf("failed to get current assignee: %w", err)
		}
		return fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, currentAssignee)
	}

	if err := recordEventInTable(ctx, tx, "wisp_events", id, "claimed", actor, ""); err != nil {
		return fmt.Errorf("failed to record claim event: %w", err)
	}

	return wrapTransactionError("commit claim wisp", tx.Commit())
}

// searchWisps searches for issues in the wisps table.
func (s *DoltStore) searchWisps(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	whereClauses, args, err := buildIssueFilterClauses(query, filter, wispsFilterTables)
	if err != nil {
		return nil, err
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	//nolint:gosec // G201: whereSQL contains column comparisons with ?, limitSQL is a safe integer
	querySQL := fmt.Sprintf(`
		SELECT id FROM wisps
		%s
		ORDER BY priority ASC, created_at DESC
		%s
	`, whereSQL, limitSQL)

	rows, err := s.queryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search wisps: %w", err)
	}
	defer rows.Close()

	return s.scanWispIDs(ctx, rows)
}

// scanWispIDs collects IDs from rows and fetches full issues from the wisps table.
func (s *DoltStore) scanWispIDs(ctx context.Context, rows *sql.Rows) ([]*types.Issue, error) {
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan wisp id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapQueryError("iterate wisp IDs", err)
	}
	_ = rows.Close()

	if len(ids) == 0 {
		return nil, nil
	}

	return s.getWispsByIDs(ctx, ids)
}

// getWispsByIDs retrieves multiple wisps by ID, batching queries to avoid
// oversized IN-clauses that cause slow queries on large databases.
func (s *DoltStore) getWispsByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Fetch wisps in batches to keep IN-clause size bounded.
	var issues []*types.Issue
	issueMap := make(map[string]*types.Issue, len(ids))
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]

		placeholders, args := doltBuildSQLInClause(batch)

		//nolint:gosec // G201: placeholders contains only ? markers
		querySQL := fmt.Sprintf(`
			SELECT %s
			FROM wisps
			WHERE id IN (%s)
		`, issueSelectColumns, placeholders)

		queryRows, err := s.queryContext(ctx, querySQL, args...)
		if err != nil {
			return nil, fmt.Errorf("failed to get wisps by IDs: %w", err)
		}

		for queryRows.Next() {
			issue, err := scanIssueFrom(queryRows)
			if err != nil {
				_ = queryRows.Close()
				return nil, wrapScanError("scan wisp", err)
			}
			issues = append(issues, issue)
			issueMap[issue.ID] = issue
		}
		if err := queryRows.Err(); err != nil {
			_ = queryRows.Close()
			return nil, wrapQueryError("iterate wisps", err)
		}
		_ = queryRows.Close()
	}

	// Hydrate labels in batches.
	if len(issues) > 0 {
		allIDs := make([]string, len(issues))
		for i, issue := range issues {
			allIDs[i] = issue.ID
		}

		for start := 0; start < len(allIDs); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(allIDs) {
				end = len(allIDs)
			}
			batch := allIDs[start:end]
			placeholders, args := doltBuildSQLInClause(batch)

			//nolint:gosec // G201: placeholders contains only ? markers
			labelSQL := fmt.Sprintf(`
				SELECT issue_id, label FROM wisp_labels
				WHERE issue_id IN (%s)
				ORDER BY issue_id, label
			`, placeholders)

			labelRows, err := s.queryContext(ctx, labelSQL, args...)
			if err != nil {
				return nil, fmt.Errorf("failed to get wisp labels: %w", err)
			}

			for labelRows.Next() {
				var issueID, label string
				if err := labelRows.Scan(&issueID, &label); err != nil {
					_ = labelRows.Close()
					return nil, wrapScanError("scan wisp label", err)
				}
				if issue, ok := issueMap[issueID]; ok {
					issue.Labels = append(issue.Labels, label)
				}
			}
			if err := labelRows.Err(); err != nil {
				_ = labelRows.Close()
				return nil, wrapQueryError("iterate wisp labels", err)
			}
			_ = labelRows.Close()
		}
	}

	return issues, nil
}

// addWispDependency adds a dependency to the wisp_dependencies table.
func (s *DoltStore) addWispDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	metadata := dep.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Cycle detection for blocking dependency types: check if adding this edge
	// would create a cycle. UNIONs both tables to detect cross-table cycles
	// (e.g., wisp A -> permanent B -> wisp A). (bd-xe27)
	if dep.Type == types.DepBlocks {
		var reachable int
		if err := tx.QueryRowContext(ctx, `
			WITH RECURSIVE reachable AS (
				SELECT ? AS node, 0 AS depth
				UNION ALL
				SELECT d.depends_on_id, r.depth + 1
				FROM reachable r
				JOIN (
					SELECT issue_id, depends_on_id FROM dependencies WHERE type = 'blocks'
					UNION ALL
					SELECT issue_id, depends_on_id FROM wisp_dependencies WHERE type = 'blocks'
				) d ON d.issue_id = r.node
				WHERE r.depth < 100
			)
			SELECT COUNT(*) FROM reachable WHERE node = ?
		`, dep.DependsOnID, dep.IssueID).Scan(&reachable); err != nil {
			return fmt.Errorf("failed to check for dependency cycle: %w", err)
		}
		if reachable > 0 {
			return fmt.Errorf("adding dependency would create a cycle")
		}
	}

	// Check for existing dependency to prevent silent type overwrites.
	var existingType string
	err = tx.QueryRowContext(ctx, `
		SELECT type FROM wisp_dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, dep.IssueID, dep.DependsOnID).Scan(&existingType)
	if err == nil {
		if existingType == string(dep.Type) {
			// Same type — idempotent; update metadata in case it changed
			if _, err := tx.ExecContext(ctx, `
				UPDATE wisp_dependencies SET metadata = ? WHERE issue_id = ? AND depends_on_id = ?
			`, metadata, dep.IssueID, dep.DependsOnID); err != nil {
				return fmt.Errorf("failed to update wisp dependency metadata: %w", err)
			}
			return wrapTransactionError("commit add wisp dependency", tx.Commit())
		}
		return fmt.Errorf("dependency %s -> %s already exists with type %q (requested %q); remove it first with 'bd dep remove' then re-add",
			dep.IssueID, dep.DependsOnID, existingType, dep.Type)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to check existing wisp dependency: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wisp_dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?, ?)
	`, dep.IssueID, dep.DependsOnID, dep.Type, actor, metadata, dep.ThreadID); err != nil {
		return fmt.Errorf("failed to add wisp dependency: %w", err)
	}

	return wrapTransactionError("commit add wisp dependency", tx.Commit())
}

// removeWispDependency removes a dependency from wisp_dependencies.
func (s *DoltStore) removeWispDependency(ctx context.Context, issueID, dependsOnID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM wisp_dependencies WHERE issue_id = ? AND depends_on_id = ?
	`, issueID, dependsOnID); err != nil {
		return fmt.Errorf("failed to remove wisp dependency: %w", err)
	}

	return wrapTransactionError("commit remove wisp dependency", tx.Commit())
}

// getWispDependencies retrieves issues that a wisp depends on.
func (s *DoltStore) getWispDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.queryContext(ctx, `
		SELECT depends_on_id FROM wisp_dependencies WHERE issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependencies: %w", err)
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, wrapScanError("scan wisp dependency", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrapQueryError("iterate wisp dependencies", err)
	}

	if len(ids) == 0 {
		return nil, nil
	}

	return s.GetIssuesByIDs(ctx, ids)
}

// getWispDependents retrieves issues that depend on a wisp.
func (s *DoltStore) getWispDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	rows, err := s.queryContext(ctx, `
		SELECT issue_id FROM wisp_dependencies WHERE depends_on_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependents: %w", err)
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, wrapScanError("scan wisp dependent", err)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrapQueryError("iterate wisp dependents", err)
	}

	if len(ids) == 0 {
		return nil, nil
	}

	return s.GetIssuesByIDs(ctx, ids)
}

// getWispDependenciesWithMetadata returns wisp dependencies with metadata.
func (s *DoltStore) getWispDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	rows, err := s.queryContext(ctx, `
		SELECT depends_on_id, type FROM wisp_dependencies WHERE issue_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependencies with metadata: %w", err)
	}

	type depMeta struct {
		depID, depType string
	}
	var deps []depMeta
	for rows.Next() {
		var depID, depType string
		if err := rows.Scan(&depID, &depType); err != nil {
			_ = rows.Close()
			return nil, wrapScanError("scan wisp dependency metadata", err)
		}
		deps = append(deps, depMeta{depID: depID, depType: depType})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrapQueryError("iterate wisp dependencies", err)
	}

	if len(deps) == 0 {
		return nil, nil
	}

	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := s.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	var results []*types.IssueWithDependencyMetadata
	for _, d := range deps {
		issue, ok := issueMap[d.depID]
		if !ok {
			continue
		}
		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(d.depType),
		})
	}
	return results, nil
}

// getWispDependentsWithMetadata returns wisp dependents with metadata.
func (s *DoltStore) getWispDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	rows, err := s.queryContext(ctx, `
		SELECT issue_id, type FROM wisp_dependencies WHERE depends_on_id = ?
	`, issueID)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisp dependents with metadata: %w", err)
	}

	type depMeta struct {
		depID, depType string
	}
	var deps []depMeta
	for rows.Next() {
		var depID, depType string
		if err := rows.Scan(&depID, &depType); err != nil {
			_ = rows.Close()
			return nil, wrapScanError("scan wisp dependent metadata", err)
		}
		deps = append(deps, depMeta{depID: depID, depType: depType})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrapQueryError("iterate wisp dependents", err)
	}

	if len(deps) == 0 {
		return nil, nil
	}

	ids := make([]string, len(deps))
	for i, d := range deps {
		ids[i] = d.depID
	}
	issues, err := s.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	issueMap := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		issueMap[iss.ID] = iss
	}

	var results []*types.IssueWithDependencyMetadata
	for _, d := range deps {
		issue, ok := issueMap[d.depID]
		if !ok {
			continue
		}
		results = append(results, &types.IssueWithDependencyMetadata{
			Issue:          *issue,
			DependencyType: types.DependencyType(d.depType),
		})
	}
	return results, nil
}

// addWispLabel adds a label to a wisp in the wisp_labels table.
func (s *DoltStore) addWispLabel(ctx context.Context, issueID, label, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		INSERT IGNORE INTO wisp_labels (issue_id, label) VALUES (?, ?)
	`, issueID, label)
	if err != nil {
		return fmt.Errorf("failed to add wisp label: %w", err)
	}

	if err := recordEventInTable(ctx, tx, "wisp_events", issueID, types.EventLabelAdded, actor, "Added label: "+label); err != nil {
		return fmt.Errorf("failed to record wisp label event: %w", err)
	}

	return tx.Commit()
}

// removeWispLabel removes a label from a wisp.
func (s *DoltStore) removeWispLabel(ctx context.Context, issueID, label, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		DELETE FROM wisp_labels WHERE issue_id = ? AND label = ?
	`, issueID, label)
	if err != nil {
		return fmt.Errorf("failed to remove wisp label: %w", err)
	}

	if err := recordEventInTable(ctx, tx, "wisp_events", issueID, types.EventLabelRemoved, actor, "Removed label: "+label); err != nil {
		return fmt.Errorf("failed to record wisp label event: %w", err)
	}

	return tx.Commit()
}
