package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
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

// wispIssueTable returns the table name for issue storage based on ID.
func wispIssueTable(id string) string {
	if IsEphemeralID(id) {
		return "wisps"
	}
	return "issues"
}

// wispLabelTable returns the label table name based on issue ID.
func wispLabelTable(issueID string) string {
	if IsEphemeralID(issueID) {
		return "wisp_labels"
	}
	return "labels"
}

// wispDepTable returns the dependency table name based on issue ID.
func wispDepTable(issueID string) string {
	if IsEphemeralID(issueID) {
		return "wisp_dependencies"
	}
	return "dependencies"
}

// wispEventTable returns the event table name based on issue ID.
func wispEventTable(issueID string) string {
	if IsEphemeralID(issueID) {
		return "wisp_events"
	}
	return "events"
}

// wispCommentTable returns the comment table name based on issue ID.
func wispCommentTable(issueID string) string {
	if IsEphemeralID(issueID) {
		return "wisp_comments"
	}
	return "comments"
}

// insertIssueIntoTable inserts an issue into the specified table.
// The table must be either "issues" or "wisps" (same schema).
//
//nolint:gosec // G201: table is a hardcoded constant from wispIssueTable
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
	return err
}

// scanIssueFromTable scans a single issue from the specified table.
//
//nolint:gosec // G201: table is a hardcoded constant from wispIssueTable
func scanIssueFromTable(ctx context.Context, db *sql.DB, table, id string) (*types.Issue, error) {
	row := db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE id = ?
	`, issueSelectColumns, table), id)

	issue, err := scanIssueFrom(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue from %s: %w", table, err)
	}
	return issue, nil
}

// recordEventInTable records an event in the specified events table.
//
//nolint:gosec // G201: table is a hardcoded constant from wispEventTable
func recordEventInTable(ctx context.Context, tx *sql.Tx, table, issueID string, eventType types.EventType, actor, oldValue, newValue string) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?)
	`, table), issueID, eventType, actor, oldValue, newValue)
	return err
}

// generateIssueIDInTable generates a unique hash-based ID, checking for collisions
// in the specified table.
//
//nolint:gosec // G201: table is a hardcoded constant
func generateIssueIDInTable(ctx context.Context, tx *sql.Tx, table, prefix string, issue *types.Issue, actor string) (string, error) {
	baseLength, err := getAdaptiveIDLengthFromTable(ctx, tx, table, prefix)
	if err != nil {
		baseLength = 6
	}

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
func getAdaptiveIDLengthFromTable(ctx context.Context, tx *sql.Tx, table, prefix string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE id LIKE ?`, table), prefix+"%").Scan(&count)
	if err != nil {
		return 4, nil // Default for wisps (small tables)
	}

	switch {
	case count < 100:
		return 4, nil
	case count < 1000:
		return 5, nil
	case count < 10000:
		return 6, nil
	default:
		return 7, nil
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
//nolint:gosec // G201: table is a hardcoded constant from wispIssueTable
func scanIssueTxFromTable(ctx context.Context, tx *sql.Tx, table, id string) (*types.Issue, error) {
	row := tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT %s FROM %s WHERE id = ?
	`, issueSelectColumns, table), id)

	issue, err := scanIssueFrom(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return issue, nil
}

// wispPrefix returns the ID prefix for wisp ID generation.
// Appends "-wisp" to the config prefix (e.g., "bd" -> "bd-wisp").
func wispPrefix(configPrefix string, issue *types.Issue) string {
	prefix := configPrefix
	if issue.PrefixOverride != "" {
		prefix = issue.PrefixOverride
	} else if issue.IDPrefix != "" {
		prefix = configPrefix + "-" + issue.IDPrefix
	}
	return prefix + "-wisp"
}

// createWisp creates an issue in the wisps table.
func (s *DoltStore) createWisp(ctx context.Context, issue *types.Issue, actor string) error {
	issue.Ephemeral = true

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

	if err := recordEventInTable(ctx, tx, "wisp_events", issue.ID, types.EventCreated, actor, "", ""); err != nil {
		return fmt.Errorf("failed to record creation event: %w", err)
	}

	return tx.Commit()
}

// getWisp retrieves an issue from the wisps table.
func (s *DoltStore) getWisp(ctx context.Context, id string) (*types.Issue, error) {
	s.mu.RLock()
	issue, err := scanIssueFromTable(ctx, s.db, "wisps", id)
	if err != nil {
		s.mu.RUnlock()
		return nil, err
	}
	if issue != nil {
		labels, err := s.getWispLabels(ctx, id)
		s.mu.RUnlock()
		if err != nil {
			return nil, fmt.Errorf("failed to get wisp labels: %w", err)
		}
		issue.Labels = labels
		return issue, nil
	}
	s.mu.RUnlock()
	return nil, nil
}

// getWispLabels retrieves labels from the wisp_labels table.
func (s *DoltStore) getWispLabels(ctx context.Context, issueID string) ([]string, error) {
	rows, err := s.queryContext(ctx, `SELECT label FROM wisp_labels WHERE issue_id = ? ORDER BY label`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var labels []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

// updateWisp updates fields on a wisp in the wisps table.
func (s *DoltStore) updateWisp(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
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
			waitersJSON, _ := json.Marshal(value)
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

	args = append(args, id)

	// nolint:gosec // G201: setClauses contains only column names
	query := fmt.Sprintf("UPDATE wisps SET %s WHERE id = ?", strings.Join(setClauses, ", "))
	_, err := s.execContext(ctx, query, args...)
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

	if err := recordEventInTable(ctx, tx, "wisp_events", id, types.EventClosed, actor, "", reason); err != nil {
		return fmt.Errorf("failed to record event: %w", err)
	}

	return tx.Commit()
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

	return tx.Commit()
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
		err := s.db.QueryRowContext(ctx, `SELECT assignee FROM wisps WHERE id = ?`, id).Scan(&currentAssignee)
		if err != nil {
			return fmt.Errorf("failed to get current assignee: %w", err)
		}
		return fmt.Errorf("%w by %s", storage.ErrAlreadyClaimed, currentAssignee)
	}

	if err := recordEventInTable(ctx, tx, "wisp_events", id, "claimed", actor, "", ""); err != nil {
		return fmt.Errorf("failed to record claim event: %w", err)
	}

	return tx.Commit()
}

// searchWisps searches for issues in the wisps table.
func (s *DoltStore) searchWisps(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	whereClauses := []string{}
	args := []interface{}{}

	if query != "" {
		whereClauses = append(whereClauses, "(title LIKE ? OR description LIKE ? OR id LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern)
	}

	if filter.Status != nil {
		whereClauses = append(whereClauses, "status = ?")
		args = append(args, *filter.Status)
	}

	if len(filter.ExcludeStatus) > 0 {
		placeholders := make([]string, len(filter.ExcludeStatus))
		for i, s := range filter.ExcludeStatus {
			placeholders[i] = "?"
			args = append(args, string(s))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("status NOT IN (%s)", strings.Join(placeholders, ",")))
	}

	if filter.IssueType != nil {
		whereClauses = append(whereClauses, "issue_type = ?")
		args = append(args, *filter.IssueType)
	}

	if len(filter.ExcludeTypes) > 0 {
		placeholders := make([]string, len(filter.ExcludeTypes))
		for i, t := range filter.ExcludeTypes {
			placeholders[i] = "?"
			args = append(args, string(t))
		}
		whereClauses = append(whereClauses, fmt.Sprintf("issue_type NOT IN (%s)", strings.Join(placeholders, ",")))
	}

	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}

	if len(filter.IDs) > 0 {
		placeholders := make([]string, len(filter.IDs))
		for i, id := range filter.IDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ", ")))
	}

	if filter.IDPrefix != "" {
		whereClauses = append(whereClauses, "id LIKE ?")
		args = append(args, filter.IDPrefix+"%")
	}

	if filter.SpecIDPrefix != "" {
		whereClauses = append(whereClauses, "spec_id LIKE ?")
		args = append(args, filter.SpecIDPrefix+"%")
	}

	if filter.ParentID != nil {
		parentID := *filter.ParentID
		whereClauses = append(whereClauses, "(id IN (SELECT issue_id FROM wisp_dependencies WHERE type = 'parent-child' AND depends_on_id = ?) OR id LIKE CONCAT(?, '.%'))")
		args = append(args, parentID, parentID)
	}

	if filter.MolType != nil {
		whereClauses = append(whereClauses, "mol_type = ?")
		args = append(args, string(*filter.MolType))
	}

	if filter.WispType != nil {
		whereClauses = append(whereClauses, "wisp_type = ?")
		args = append(args, string(*filter.WispType))
	}

	if filter.TitleSearch != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleSearch+"%")
	}

	if filter.TitleContains != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleContains+"%")
	}

	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			whereClauses = append(whereClauses, "id IN (SELECT issue_id FROM wisp_labels WHERE label = ?)")
			args = append(args, label)
		}
	}

	if len(filter.LabelsAny) > 0 {
		placeholders := make([]string, len(filter.LabelsAny))
		for i, label := range filter.LabelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM wisp_labels WHERE label IN (%s))", strings.Join(placeholders, ", ")))
	}

	if filter.Pinned != nil {
		if *filter.Pinned {
			whereClauses = append(whereClauses, "pinned = 1")
		} else {
			whereClauses = append(whereClauses, "(pinned = 0 OR pinned IS NULL)")
		}
	}

	if filter.SourceRepo != nil {
		whereClauses = append(whereClauses, "source_repo = ?")
		args = append(args, *filter.SourceRepo)
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
		return nil, err
	}
	_ = rows.Close()

	if len(ids) == 0 {
		return nil, nil
	}

	return s.getWispsByIDs(ctx, ids)
}

// getWispsByIDs retrieves multiple wisps by ID in a single query.
func (s *DoltStore) getWispsByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	//nolint:gosec // G201: placeholders contains only ? markers
	querySQL := fmt.Sprintf(`
		SELECT %s
		FROM wisps
		WHERE id IN (%s)
	`, issueSelectColumns, strings.Join(placeholders, ","))

	queryRows, err := s.queryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get wisps by IDs: %w", err)
	}
	defer queryRows.Close()

	var issues []*types.Issue
	for queryRows.Next() {
		issue, err := scanIssueFrom(queryRows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	if err := queryRows.Err(); err != nil {
		return nil, err
	}

	// Hydrate labels for each wisp (batch query)
	if len(issues) > 0 {
		labelPlaceholders := make([]string, len(issues))
		labelArgs := make([]interface{}, len(issues))
		issueMap := make(map[string]*types.Issue, len(issues))
		for i, issue := range issues {
			labelPlaceholders[i] = "?"
			labelArgs[i] = issue.ID
			issueMap[issue.ID] = issue
		}

		//nolint:gosec // G201: placeholders contains only ? markers
		labelSQL := fmt.Sprintf(`
			SELECT issue_id, label FROM wisp_labels
			WHERE issue_id IN (%s)
			ORDER BY issue_id, label
		`, strings.Join(labelPlaceholders, ","))

		labelRows, err := s.queryContext(ctx, labelSQL, labelArgs...)
		if err != nil {
			return nil, fmt.Errorf("failed to get wisp labels: %w", err)
		}
		defer labelRows.Close()

		for labelRows.Next() {
			var issueID, label string
			if err := labelRows.Scan(&issueID, &label); err != nil {
				return nil, err
			}
			if issue, ok := issueMap[issueID]; ok {
				issue.Labels = append(issue.Labels, label)
			}
		}
		if err := labelRows.Err(); err != nil {
			return nil, err
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

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wisp_dependencies (issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?, ?)
		ON DUPLICATE KEY UPDATE type = VALUES(type), metadata = VALUES(metadata)
	`, dep.IssueID, dep.DependsOnID, dep.Type, actor, metadata, dep.ThreadID); err != nil {
		return fmt.Errorf("failed to add wisp dependency: %w", err)
	}

	return tx.Commit()
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

	return tx.Commit()
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
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
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
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
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
			return nil, err
		}
		deps = append(deps, depMeta{depID: depID, depType: depType})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
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
			return nil, err
		}
		deps = append(deps, depMeta{depID: depID, depType: depType})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
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
	_, err := s.execContext(ctx, `
		INSERT IGNORE INTO wisp_labels (issue_id, label) VALUES (?, ?)
	`, issueID, label)
	if err != nil {
		return fmt.Errorf("failed to add wisp label: %w", err)
	}
	return nil
}

// removeWispLabel removes a label from a wisp.
func (s *DoltStore) removeWispLabel(ctx context.Context, issueID, label string) error {
	_, err := s.execContext(ctx, `
		DELETE FROM wisp_labels WHERE issue_id = ? AND label = ?
	`, issueID, label)
	if err != nil {
		return fmt.Errorf("failed to remove wisp label: %w", err)
	}
	return nil
}
