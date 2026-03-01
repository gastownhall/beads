package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// doltTransaction implements storage.Transaction for Dolt
type doltTransaction struct {
	tx    *sql.Tx
	store *DoltStore
}

// isActiveWisp checks if an ID exists in the wisps table within the transaction.
// Unlike the store-level isActiveWisp, this queries within the transaction so it
// sees uncommitted wisps. Handles both -wisp- pattern and explicit-ID ephemerals (GH#2053).
func (t *doltTransaction) isActiveWisp(ctx context.Context, id string) bool {
	var exists int
	err := t.tx.QueryRowContext(ctx, "SELECT 1 FROM wisps WHERE id = ? LIMIT 1", id).Scan(&exists)
	return err == nil
}

// CreateIssueImport is the import-friendly issue creation hook.
// Dolt does not enforce prefix validation at the storage layer, so this delegates to CreateIssue.
func (t *doltTransaction) CreateIssueImport(ctx context.Context, issue *types.Issue, actor string, skipPrefixValidation bool) error {
	return t.CreateIssue(ctx, issue, actor)
}

// RunInTransaction executes a function within a database transaction.
// The commitMsg is used for the DOLT_COMMIT that occurs inside the transaction,
// making the write atomically visible in Dolt's version history.
// Wisp routing is handled within individual transaction methods based on ID/Ephemeral flag.
func (s *DoltStore) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	return s.withRetry(ctx, func() error {
		return s.runDoltTransaction(ctx, commitMsg, fn)
	})
}

func (s *DoltStore) runDoltTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	tx := &doltTransaction{tx: sqlTx, store: s}

	defer func() {
		if r := recover(); r != nil {
			_ = sqlTx.Rollback() // Best effort rollback on error path
			panic(r)
		}
	}()

	if err := fn(tx); err != nil {
		_ = sqlTx.Rollback() // Best effort rollback on error path
		return err
	}

	// Commit the SQL transaction first to persist all working set changes,
	// including writes to dolt-ignored tables (e.g., wisps). (hq-3paz0m)
	//
	// Previously, DOLT_COMMIT was called inside the transaction. When it
	// returned "nothing to commit" (all writes to dolt-ignored tables), the
	// Go sql.Tx was left in a broken state and Commit() failed silently,
	// losing wisp data.
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("sql commit: %w", err)
	}

	// Create a Dolt version commit from the working set.
	// Runs outside the transaction on a pool connection.
	// "Nothing to commit" is benign (all writes to dolt-ignored tables).
	if commitMsg != "" {
		_, err := s.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?, '--author', ?)",
			commitMsg, s.commitAuthorString())
		if err != nil && !isDoltNothingToCommit(err) {
			return fmt.Errorf("dolt commit: %w", err)
		}
	}

	return nil
}

// isDoltNothingToCommit returns true if the error indicates there were no
// staged changes for Dolt to commit — a benign condition.
func isDoltNothingToCommit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "nothing to commit") ||
		(strings.Contains(s, "no changes") && strings.Contains(s, "commit"))
}

// CreateIssue creates an issue within the transaction.
// Routes ephemeral issues to the wisps table.
func (t *doltTransaction) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	now := time.Now().UTC()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = now
	}
	if issue.ContentHash == "" {
		issue.ContentHash = issue.ComputeContentHash()
	}

	table := "issues"
	if issue.Ephemeral {
		table = "wisps"
	}

	// Generate ID if not provided
	if issue.ID == "" {
		var configPrefix string
		err := t.tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", "issue_prefix").Scan(&configPrefix)
		if err == sql.ErrNoRows || configPrefix == "" {
			return fmt.Errorf("%w: issue_prefix config is missing", storage.ErrNotInitialized)
		} else if err != nil {
			return fmt.Errorf("failed to get config: %w", err)
		}

		// Normalize prefix: strip trailing hyphen to prevent double-hyphen IDs (bd-6uly)
		configPrefix = strings.TrimSuffix(configPrefix, "-")

		var prefix string
		if issue.Ephemeral {
			prefix = wispPrefix(configPrefix, issue)
		} else {
			prefix = configPrefix
			if issue.PrefixOverride != "" {
				prefix = issue.PrefixOverride
			} else if issue.IDPrefix != "" {
				prefix = configPrefix + "-" + issue.IDPrefix
			}
		}

		generatedID, err := generateIssueIDInTable(ctx, t.tx, table, prefix, issue, actor)
		if err != nil {
			return fmt.Errorf("failed to generate issue ID: %w", err)
		}
		issue.ID = generatedID
	}

	// Validate metadata against schema if configured (GH#1416 Phase 2)
	if err := validateMetadataIfConfigured(issue.Metadata); err != nil {
		return err
	}

	return insertIssueTxIntoTable(ctx, t.tx, table, issue)
}

// CreateIssues creates multiple issues within the transaction
func (t *doltTransaction) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	for _, issue := range issues {
		if err := t.CreateIssue(ctx, issue, actor); err != nil {
			return err
		}
	}
	return nil
}

// GetIssue retrieves an issue within the transaction.
// Checks wisps table for active wisps (including explicit-ID ephemerals).
func (t *doltTransaction) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	table := "issues"
	if t.isActiveWisp(ctx, id) {
		table = "wisps"
	}
	return scanIssueTxFromTable(ctx, t.tx, table, id)
}

// SearchIssues searches for issues within the transaction.
// Supports the same filter fields as DoltStore.SearchIssues (bd-v6v8).
func (t *doltTransaction) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	table := "issues"
	if filter.Ephemeral != nil && *filter.Ephemeral {
		table = "wisps"
	}
	// If searching by IDs that are all ephemeral, use wisps table (bd-w2w)
	if len(filter.IDs) > 0 && allEphemeral(filter.IDs) {
		table = "wisps"
	}

	// Derive related table names from the main table
	depTable := "dependencies"
	labelTable := "labels"
	if table == "wisps" {
		depTable = "wisp_dependencies"
		labelTable = "wisp_labels"
	}

	whereClauses := []string{}
	args := []interface{}{}

	// Text search
	if query != "" {
		whereClauses = append(whereClauses, "(title LIKE ? OR description LIKE ? OR id LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern)
	}

	if filter.TitleSearch != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleSearch+"%")
	}
	if filter.TitleContains != "" {
		whereClauses = append(whereClauses, "title LIKE ?")
		args = append(args, "%"+filter.TitleContains+"%")
	}
	if filter.DescriptionContains != "" {
		whereClauses = append(whereClauses, "description LIKE ?")
		args = append(args, "%"+filter.DescriptionContains+"%")
	}
	if filter.NotesContains != "" {
		whereClauses = append(whereClauses, "notes LIKE ?")
		args = append(args, "%"+filter.NotesContains+"%")
	}

	// Status
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

	// Type filtering — use subquery to prevent Dolt mergeJoinIter panic
	if len(filter.ExcludeTypes) > 0 {
		placeholders := make([]string, len(filter.ExcludeTypes))
		for i, tp := range filter.ExcludeTypes {
			placeholders[i] = "?"
			args = append(args, string(tp))
		}
		//nolint:gosec // G201: table is hardcoded to "issues" or "wisps"
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT id FROM %s WHERE issue_type NOT IN (%s))", table, strings.Join(placeholders, ",")))
	}

	// Priority
	if filter.Priority != nil {
		whereClauses = append(whereClauses, "priority = ?")
		args = append(args, *filter.Priority)
	}
	if filter.PriorityMin != nil {
		whereClauses = append(whereClauses, "priority >= ?")
		args = append(args, *filter.PriorityMin)
	}
	if filter.PriorityMax != nil {
		whereClauses = append(whereClauses, "priority <= ?")
		args = append(args, *filter.PriorityMax)
	}

	// Issue type — use subquery to prevent Dolt mergeJoinIter panic
	if filter.IssueType != nil {
		//nolint:gosec // G201: table is hardcoded to "issues" or "wisps"
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT id FROM %s WHERE issue_type = ?)", table))
		args = append(args, *filter.IssueType)
	}

	// Assignee
	if filter.Assignee != nil {
		whereClauses = append(whereClauses, "assignee = ?")
		args = append(args, *filter.Assignee)
	}

	// Date ranges
	if filter.CreatedAfter != nil {
		whereClauses = append(whereClauses, "created_at > ?")
		args = append(args, filter.CreatedAfter.Format(time.RFC3339))
	}
	if filter.CreatedBefore != nil {
		whereClauses = append(whereClauses, "created_at < ?")
		args = append(args, filter.CreatedBefore.Format(time.RFC3339))
	}
	if filter.UpdatedAfter != nil {
		whereClauses = append(whereClauses, "updated_at > ?")
		args = append(args, filter.UpdatedAfter.Format(time.RFC3339))
	}
	if filter.UpdatedBefore != nil {
		whereClauses = append(whereClauses, "updated_at < ?")
		args = append(args, filter.UpdatedBefore.Format(time.RFC3339))
	}
	if filter.ClosedAfter != nil {
		whereClauses = append(whereClauses, "closed_at > ?")
		args = append(args, filter.ClosedAfter.Format(time.RFC3339))
	}
	if filter.ClosedBefore != nil {
		whereClauses = append(whereClauses, "closed_at < ?")
		args = append(args, filter.ClosedBefore.Format(time.RFC3339))
	}
	if filter.DeferAfter != nil {
		whereClauses = append(whereClauses, "defer_until > ?")
		args = append(args, filter.DeferAfter.Format(time.RFC3339))
	}
	if filter.DeferBefore != nil {
		whereClauses = append(whereClauses, "defer_until < ?")
		args = append(args, filter.DeferBefore.Format(time.RFC3339))
	}
	if filter.DueAfter != nil {
		whereClauses = append(whereClauses, "due_at > ?")
		args = append(args, filter.DueAfter.Format(time.RFC3339))
	}
	if filter.DueBefore != nil {
		whereClauses = append(whereClauses, "due_at < ?")
		args = append(args, filter.DueBefore.Format(time.RFC3339))
	}

	// Empty/null checks
	if filter.EmptyDescription {
		whereClauses = append(whereClauses, "(description IS NULL OR description = '')")
	}
	if filter.NoAssignee {
		whereClauses = append(whereClauses, "(assignee IS NULL OR assignee = '')")
	}
	if filter.NoLabels {
		//nolint:gosec // G201: labelTable is hardcoded to "labels" or "wisp_labels"
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (SELECT DISTINCT issue_id FROM %s)", labelTable))
	}

	// Label filtering (AND)
	if len(filter.Labels) > 0 {
		for _, label := range filter.Labels {
			//nolint:gosec // G201: labelTable is hardcoded to "labels" or "wisp_labels"
			whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label = ?)", labelTable))
			args = append(args, label)
		}
	}

	// Label filtering (OR)
	if len(filter.LabelsAny) > 0 {
		placeholders := make([]string, len(filter.LabelsAny))
		for i, label := range filter.LabelsAny {
			placeholders[i] = "?"
			args = append(args, label)
		}
		//nolint:gosec // G201: labelTable is hardcoded to "labels" or "wisp_labels"
		whereClauses = append(whereClauses, fmt.Sprintf("id IN (SELECT issue_id FROM %s WHERE label IN (%s))", labelTable, strings.Join(placeholders, ", ")))
	}

	// ID filtering
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

	// Source repo
	if filter.SourceRepo != nil {
		whereClauses = append(whereClauses, "source_repo = ?")
		args = append(args, *filter.SourceRepo)
	}

	// Ephemeral filtering (when querying issues table with explicit ephemeral filter)
	if filter.Ephemeral != nil {
		if *filter.Ephemeral {
			whereClauses = append(whereClauses, "ephemeral = 1")
		} else {
			whereClauses = append(whereClauses, "(ephemeral = 0 OR ephemeral IS NULL)")
		}
	}

	// Pinned filtering
	if filter.Pinned != nil {
		if *filter.Pinned {
			whereClauses = append(whereClauses, "pinned = 1")
		} else {
			whereClauses = append(whereClauses, "(pinned = 0 OR pinned IS NULL)")
		}
	}

	// Template filtering
	if filter.IsTemplate != nil {
		if *filter.IsTemplate {
			whereClauses = append(whereClauses, "is_template = 1")
		} else {
			whereClauses = append(whereClauses, "(is_template = 0 OR is_template IS NULL)")
		}
	}

	// Parent filtering
	if filter.ParentID != nil {
		parentID := *filter.ParentID
		//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
		whereClauses = append(whereClauses, fmt.Sprintf("(id IN (SELECT issue_id FROM %s WHERE type = 'parent-child' AND depends_on_id = ?) OR (id LIKE CONCAT(?, '.%%') AND id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child')))", depTable, depTable))
		args = append(args, parentID, parentID)
	}

	// No-parent filtering
	if filter.NoParent {
		//nolint:gosec // G201: depTable is hardcoded to "dependencies" or "wisp_dependencies"
		whereClauses = append(whereClauses, fmt.Sprintf("id NOT IN (SELECT issue_id FROM %s WHERE type = 'parent-child')", depTable))
	}

	// Molecule type filtering
	if filter.MolType != nil {
		whereClauses = append(whereClauses, "mol_type = ?")
		args = append(args, string(*filter.MolType))
	}

	// Wisp type filtering
	if filter.WispType != nil {
		whereClauses = append(whereClauses, "wisp_type = ?")
		args = append(args, string(*filter.WispType))
	}

	// Time-based scheduling filters
	if filter.Deferred {
		whereClauses = append(whereClauses, "defer_until IS NOT NULL")
	}
	if filter.Overdue {
		whereClauses = append(whereClauses, "due_at IS NOT NULL AND due_at < ? AND status != ?")
		args = append(args, time.Now().UTC().Format(time.RFC3339), types.StatusClosed)
	}

	// Metadata existence check
	if filter.HasMetadataKey != "" {
		if err := storage.ValidateMetadataKey(filter.HasMetadataKey); err != nil {
			return nil, err
		}
		whereClauses = append(whereClauses, "JSON_EXTRACT(metadata, ?) IS NOT NULL")
		args = append(args, "$."+filter.HasMetadataKey)
	}

	// Metadata field equality filters
	if len(filter.MetadataFields) > 0 {
		metaKeys := make([]string, 0, len(filter.MetadataFields))
		for k := range filter.MetadataFields {
			metaKeys = append(metaKeys, k)
		}
		sort.Strings(metaKeys)
		for _, k := range metaKeys {
			if err := storage.ValidateMetadataKey(k); err != nil {
				return nil, err
			}
			whereClauses = append(whereClauses, "JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?")
			args = append(args, "$."+k, filter.MetadataFields[k])
		}
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	limitSQL := ""
	if filter.Limit > 0 {
		limitSQL = fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	//nolint:gosec // G201: table is hardcoded, whereSQL is parameterized
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id FROM %s %s ORDER BY priority ASC, created_at DESC %s
	`, table, whereSQL, limitSQL), args...)
	if err != nil {
		return nil, wrapQueryError("search issues in tx", err)
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, wrapScanError("search issues in tx", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, wrapQueryError("search issues in tx: rows iteration", err)
	}
	_ = rows.Close()

	var issues []*types.Issue
	for _, id := range ids {
		issue, err := t.GetIssue(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("search issues in tx: get issue %s: %w", id, err)
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// UpdateIssue updates an issue within the transaction
func (t *doltTransaction) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	table := "issues"
	if t.isActiveWisp(ctx, id) {
		table = "wisps"
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

		// Handle JSON serialization for array fields stored as TEXT
		if key == "waiters" {
			waitersJSON, _ := json.Marshal(value)
			args = append(args, string(waitersJSON))
		} else if key == "metadata" {
			// GH#1417: Normalize metadata to string, accepting string/[]byte/json.RawMessage
			metadataStr, err := storage.NormalizeMetadataValue(value)
			if err != nil {
				return fmt.Errorf("invalid metadata: %w", err)
			}
			// Validate against schema if configured (GH#1416 Phase 2)
			if err := validateMetadataIfConfigured(json.RawMessage(metadataStr)); err != nil {
				return err
			}
			args = append(args, metadataStr)
		} else {
			args = append(args, value)
		}
	}

	args = append(args, id)
	//nolint:gosec // G201: table is hardcoded, setClauses contains only column names
	querySQL := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", table, strings.Join(setClauses, ", "))
	_, err := t.tx.ExecContext(ctx, querySQL, args...)
	return wrapExecError("update issue in tx", err)
}

// CloseIssue closes an issue within the transaction
func (t *doltTransaction) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	table := "issues"
	if t.isActiveWisp(ctx, id) {
		table = "wisps"
	}

	now := time.Now().UTC()
	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET status = ?, closed_at = ?, updated_at = ?, close_reason = ?, closed_by_session = ?
		WHERE id = ?
	`, table), types.StatusClosed, now, now, reason, session, id)
	return wrapExecError("close issue in tx", err)
}

// DeleteIssue deletes an issue within the transaction
func (t *doltTransaction) DeleteIssue(ctx context.Context, id string) error {
	table := "issues"
	if t.isActiveWisp(ctx, id) {
		table = "wisps"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", table), id)
	return wrapExecError("delete issue in tx", err)
}

// AddDependency adds a dependency within the transaction.
// Checks for existing pairs to prevent silent type overwrites.
func (t *doltTransaction) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	table := "dependencies"
	if t.isActiveWisp(ctx, dep.IssueID) {
		table = "wisp_dependencies"
	}

	// Check for existing dependency to prevent silent type overwrites.
	var existingType string
	//nolint:gosec // G201: table is hardcoded
	err := t.tx.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT type FROM %s WHERE issue_id = ? AND depends_on_id = ?
	`, table), dep.IssueID, dep.DependsOnID).Scan(&existingType)
	if err == nil {
		if existingType == string(dep.Type) {
			return nil // idempotent
		}
		return fmt.Errorf("dependency %s -> %s already exists with type %q (requested %q); remove it first with 'bd dep remove' then re-add",
			dep.IssueID, dep.DependsOnID, existingType, dep.Type)
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("failed to check existing dependency: %w", err)
	}

	//nolint:gosec // G201: table is hardcoded
	_, err = t.tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, depends_on_id, type, created_at, created_by, thread_id)
		VALUES (?, ?, ?, NOW(), ?, ?)
	`, table), dep.IssueID, dep.DependsOnID, dep.Type, actor, dep.ThreadID)
	return wrapExecError("add dependency in tx", err)
}

func (t *doltTransaction) GetDependencyRecords(ctx context.Context, issueID string) ([]*types.Dependency, error) {
	table := "dependencies"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_dependencies"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		FROM %s
		WHERE issue_id = ?
	`, table), issueID)
	if err != nil {
		return nil, wrapQueryError("get dependency records in tx", err)
	}
	defer rows.Close()

	var deps []*types.Dependency
	for rows.Next() {
		var d types.Dependency
		var metadata sql.NullString
		var threadID sql.NullString
		if err := rows.Scan(&d.IssueID, &d.DependsOnID, &d.Type, &d.CreatedAt, &d.CreatedBy, &metadata, &threadID); err != nil {
			return nil, wrapScanError("get dependency records in tx", err)
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

// RemoveDependency removes a dependency within the transaction
func (t *doltTransaction) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	table := "dependencies"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_dependencies"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		DELETE FROM %s WHERE issue_id = ? AND depends_on_id = ?
	`, table), issueID, dependsOnID)
	return wrapExecError("remove dependency in tx", err)
}

// AddLabel adds a label within the transaction
func (t *doltTransaction) AddLabel(ctx context.Context, issueID, label, actor string) error {
	table := "labels"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_labels"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT IGNORE INTO %s (issue_id, label) VALUES (?, ?)
	`, table), issueID, label)
	return wrapExecError("add label in tx", err)
}

func (t *doltTransaction) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	table := "labels"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_labels"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`SELECT label FROM %s WHERE issue_id = ? ORDER BY label`, table), issueID)
	if err != nil {
		return nil, wrapQueryError("get labels in tx", err)
	}
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, wrapScanError("get labels in tx", err)
		}
		labels = append(labels, l)
	}
	return labels, rows.Err()
}

// RemoveLabel removes a label within the transaction
func (t *doltTransaction) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	table := "labels"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_labels"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		DELETE FROM %s WHERE issue_id = ? AND label = ?
	`, table), issueID, label)
	return wrapExecError("remove label in tx", err)
}

// SetConfig sets a config value within the transaction
func (t *doltTransaction) SetConfig(ctx context.Context, key, value string) error {
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO config (`+"`key`"+`, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, key, value)
	return wrapExecError("set config in tx", err)
}

// GetConfig gets a config value within the transaction
func (t *doltTransaction) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, wrapQueryError("get config in tx", err)
}

// SetMetadata sets a metadata value within the transaction
func (t *doltTransaction) SetMetadata(ctx context.Context, key, value string) error {
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO metadata (`+"`key`"+`, value) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE value = VALUES(value)
	`, key, value)
	return wrapExecError("set metadata in tx", err)
}

// GetMetadata gets a metadata value within the transaction
func (t *doltTransaction) GetMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := t.tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, wrapQueryError("get metadata in tx", err)
}

func (t *doltTransaction) ImportIssueComment(ctx context.Context, issueID, author, text string, createdAt time.Time) (*types.Comment, error) {
	_, err := t.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}

	table := "comments"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_comments"
	}

	createdAt = createdAt.UTC()
	//nolint:gosec // G201: table is hardcoded
	res, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?)
	`, table), issueID, author, text, createdAt)
	if err != nil {
		return nil, fmt.Errorf("failed to add comment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("failed to get comment id: %w", err)
	}

	return &types.Comment{ID: id, IssueID: issueID, Author: author, Text: text, CreatedAt: createdAt}, nil
}

func (t *doltTransaction) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	table := "comments"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_comments"
	}

	//nolint:gosec // G201: table is hardcoded
	rows, err := t.tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, issue_id, author, text, created_at
		FROM %s
		WHERE issue_id = ?
		ORDER BY created_at ASC
	`, table), issueID)
	if err != nil {
		return nil, wrapQueryError("get comments in tx", err)
	}
	defer rows.Close()
	var comments []*types.Comment
	for rows.Next() {
		var c types.Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
			return nil, wrapScanError("get comments in tx", err)
		}
		comments = append(comments, &c)
	}
	return comments, rows.Err()
}

// AddComment adds a comment within the transaction
func (t *doltTransaction) AddComment(ctx context.Context, issueID, actor, comment string) error {
	table := "events"
	if t.isActiveWisp(ctx, issueID) {
		table = "wisp_events"
	}

	//nolint:gosec // G201: table is hardcoded
	_, err := t.tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (issue_id, event_type, actor, comment)
		VALUES (?, ?, ?, ?)
	`, table), issueID, types.EventCommented, actor, comment)
	return wrapExecError("add comment in tx", err)
}
