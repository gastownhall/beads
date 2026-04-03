package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// GetIssueByExternalRefInTx looks up an issue by its external_ref field.
// Checks both issues and wisps tables so that pushed ephemeral beads are
// found during pull dedup.
// Returns the issue ID if found. Returns storage.ErrNotFound (wrapped) if not found.
func GetIssueByExternalRefInTx(ctx context.Context, tx *sql.Tx, externalRef string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM issues WHERE external_ref = ?", externalRef).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("get issue by external_ref: %w", err)
	}

	// Fall through to wisps table — pushed wisps have external_ref set there.
	err = tx.QueryRowContext(ctx, "SELECT id FROM wisps WHERE external_ref = ?", externalRef).Scan(&id)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("%w: external_ref %s", storage.ErrNotFound, externalRef)
	}
	if err != nil {
		if isTableNotExistError(err) {
			return "", fmt.Errorf("%w: external_ref %s", storage.ErrNotFound, externalRef)
		}
		return "", fmt.Errorf("get wisp by external_ref: %w", err)
	}
	return id, nil
}

// GetIssuesByLabelInTx returns issue IDs matching a label from both issues and wisps tables.
//
//nolint:gosec // G201: tables are hardcoded
func GetIssuesByLabelInTx(ctx context.Context, tx *sql.Tx, label string) ([]string, error) {
	var ids []string

	rows, err := tx.QueryContext(ctx, `
		SELECT i.id FROM issues i
		JOIN labels l ON i.id = l.issue_id
		WHERE l.label = ?
		ORDER BY i.priority ASC, i.created_at DESC
	`, label)
	if err != nil {
		return nil, fmt.Errorf("get issues by label: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan issue id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate issues by label: %w", err)
	}

	wispRows, err := tx.QueryContext(ctx, `SELECT issue_id FROM wisp_labels WHERE label = ?`, label)
	if err != nil {
		return nil, fmt.Errorf("get wisp issues by label: %w", err)
	}
	defer wispRows.Close()

	for wispRows.Next() {
		var id string
		if err := wispRows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan wisp issue id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := wispRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wisp issues by label: %w", err)
	}

	return ids, nil
}

// DeleteConfigInTx removes a configuration value.
func DeleteConfigInTx(ctx context.Context, tx *sql.Tx, key string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM config WHERE `key` = ?", key)
	if err != nil {
		return fmt.Errorf("delete config %s: %w", key, err)
	}
	return nil
}

// GetCommentsForIssuesInTx retrieves comments for multiple issues, partitioning
// between comments and wisp_comments tables.
//
//nolint:gosec // G201: table is hardcoded
func GetCommentsForIssuesInTx(ctx context.Context, tx *sql.Tx, issueIDs []string) (map[string][]*types.Comment, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]*types.Comment), nil
	}

	result := make(map[string][]*types.Comment)

	// Partition IDs by wisp status.
	var wispIDs, permIDs []string
	for _, id := range issueIDs {
		if IsActiveWispInTx(ctx, tx, id) {
			wispIDs = append(wispIDs, id)
		} else {
			permIDs = append(permIDs, id)
		}
	}

	if len(permIDs) > 0 {
		if err := getCommentsForIDsInto(ctx, tx, "comments", permIDs, result); err != nil {
			return nil, err
		}
	}
	if len(wispIDs) > 0 {
		if err := getCommentsForIDsInto(ctx, tx, "wisp_comments", wispIDs, result); err != nil {
			return nil, err
		}
	}

	return result, nil
}

//nolint:gosec // G201: table is hardcoded
func getCommentsForIDsInto(ctx context.Context, tx *sql.Tx, table string, ids []string, result map[string][]*types.Comment) error {
	for start := 0; start < len(ids); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders, args := buildSQLInClause(batch)

		query := fmt.Sprintf(`
			SELECT id, issue_id, author, text, created_at
			FROM %s
			WHERE issue_id IN (%s)
			ORDER BY issue_id, created_at ASC, id ASC
		`, table, placeholders)

		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("get comments from %s: %w", table, err)
		}

		for rows.Next() {
			var c types.Comment
			if err := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan comment: %w", err)
			}
			result[c.IssueID] = append(result[c.IssueID], &c)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
	}
	return nil
}

// DeleteIssuesBySourceRepoInTx removes all issues from a source repo and their related data.
//
//nolint:gosec // G201: table is validated by hardcoded list
func DeleteIssuesBySourceRepoInTx(ctx context.Context, tx *sql.Tx, sourceRepo string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM issues WHERE source_repo = ?`, sourceRepo)
	if err != nil {
		return 0, fmt.Errorf("query issues: %w", err)
	}
	var issueIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan issue ID: %w", err)
		}
		issueIDs = append(issueIDs, id)
	}
	_ = rows.Close()

	if len(issueIDs) == 0 {
		return 0, nil
	}

	for _, table := range []string{"dependencies", "events", "comments", "labels"} {
		for _, id := range issueIDs {
			if table == "dependencies" {
				_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ? OR depends_on_id = ?", table), id, id)
			} else {
				_, err = tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE issue_id = ?", table), id)
			}
			if err != nil {
				return 0, fmt.Errorf("delete from %s for %s: %w", table, id, err)
			}
		}
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM issues WHERE source_repo = ?`, sourceRepo)
	if err != nil {
		return 0, fmt.Errorf("delete issues: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return int(rowsAffected), nil
}

// UpdateIssueIDInTx renames an issue and updates all references across tables.
//
//nolint:gosec // G201: table names are hardcoded
func UpdateIssueIDInTx(ctx context.Context, tx *sql.Tx, oldID, newID string, issue *types.Issue, actor string) error {
	isWisp := IsActiveWispInTx(ctx, tx, oldID)

	if _, err := tx.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 0`); err != nil {
		return fmt.Errorf("disable FK checks: %w", err)
	}
	defer func() { _, _ = tx.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 1`) }()

	if isWisp {
		return updateWispIDInTx(ctx, tx, oldID, newID, issue, actor)
	}
	return updateIssueIDInTx(ctx, tx, oldID, newID, issue, actor)
}

func updateIssueIDInTx(ctx context.Context, tx *sql.Tx, oldID, newID string, issue *types.Issue, actor string) error {
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE issues
		SET id = ?, title = ?, description = ?, design = ?, acceptance_criteria = ?, notes = ?, updated_at = ?
		WHERE id = ?
	`, newID, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes, now, oldID)
	if err != nil {
		return fmt.Errorf("update issue ID: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("issue not found: %s", oldID)
	}

	refs := []struct{ table, col string }{
		{"dependencies", "issue_id"},
		{"dependencies", "depends_on_id"},
		{"events", "issue_id"},
		{"labels", "issue_id"},
		{"comments", "issue_id"},
		{"issue_snapshots", "issue_id"},
		{"compaction_snapshots", "issue_id"},
		{"child_counters", "parent_id"},
		{"wisp_dependencies", "issue_id"},
		{"wisp_dependencies", "depends_on_id"},
		{"wisp_events", "issue_id"},
		{"wisp_labels", "issue_id"},
		{"wisp_comments", "issue_id"},
	}
	for _, r := range refs {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", r.table, r.col, r.col), newID, oldID); err != nil {
			return fmt.Errorf("update %s.%s: %w", r.table, r.col, err)
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, 'renamed', ?, ?, ?)
	`, newID, actor, oldID, newID)
	return err
}

func updateWispIDInTx(ctx context.Context, tx *sql.Tx, oldID, newID string, issue *types.Issue, actor string) error {
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		UPDATE wisps
		SET id = ?, title = ?, description = ?, design = ?, acceptance_criteria = ?, notes = ?, updated_at = ?
		WHERE id = ?
	`, newID, issue.Title, issue.Description, issue.Design, issue.AcceptanceCriteria, issue.Notes, now, oldID)
	if err != nil {
		return fmt.Errorf("update wisp ID: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("wisp not found: %s", oldID)
	}

	refs := []struct{ table, col string }{
		{"wisp_dependencies", "issue_id"},
		{"wisp_dependencies", "depends_on_id"},
		{"wisp_events", "issue_id"},
		{"wisp_labels", "issue_id"},
		{"wisp_comments", "issue_id"},
	}
	for _, r := range refs {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", r.table, r.col, r.col), newID, oldID); err != nil {
			return fmt.Errorf("update %s.%s: %w", r.table, r.col, err)
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO wisp_events (issue_id, event_type, actor, old_value, new_value)
		VALUES (?, 'renamed', ?, ?, ?)
	`, newID, actor, oldID, newID)
	return err
}

// RenameDependencyPrefixInTx updates the prefix in all dependency records.
func RenameDependencyPrefixInTx(ctx context.Context, tx *sql.Tx, oldPrefix, newPrefix string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE dependencies
		SET issue_id = CONCAT(?, SUBSTRING(issue_id, LENGTH(?) + 1))
		WHERE issue_id LIKE CONCAT(?, '%')
	`, newPrefix, oldPrefix, oldPrefix)
	if err != nil {
		return fmt.Errorf("update issue_id prefix in dependencies: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE dependencies
		SET depends_on_id = CONCAT(?, SUBSTRING(depends_on_id, LENGTH(?) + 1))
		WHERE depends_on_id LIKE CONCAT(?, '%')
	`, newPrefix, oldPrefix, oldPrefix)
	if err != nil {
		return fmt.Errorf("update depends_on_id prefix in dependencies: %w", err)
	}
	return nil
}

// FindWispDependentsRecursiveInTx walks wisp_dependencies to find all transitive
// dependents of the given IDs.
func FindWispDependentsRecursiveInTx(ctx context.Context, tx *sql.Tx, ids []string) (map[string]bool, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	const maxResults = 10000
	const batchSize = 50

	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}

	toProcess := make([]string, len(ids))
	copy(toProcess, ids)
	discovered := make(map[string]bool)

	for len(toProcess) > 0 {
		if len(seen) > maxResults {
			return discovered, fmt.Errorf("wisp cascade traversal discovered over %d issues; aborting", maxResults)
		}

		end := batchSize
		if end > len(toProcess) {
			end = len(toProcess)
		}
		batch := toProcess[:end]
		toProcess = toProcess[end:]

		placeholders, args := buildSQLInClause(batch)
		rows, err := tx.QueryContext(ctx,
			fmt.Sprintf(`SELECT issue_id FROM wisp_dependencies WHERE depends_on_id IN (%s)`, placeholders),
			args...)
		if err != nil {
			return discovered, fmt.Errorf("query wisp dependents: %w", err)
		}

		for rows.Next() {
			var depID string
			if err := rows.Scan(&depID); err != nil {
				_ = rows.Close()
				return discovered, fmt.Errorf("scan wisp dependent: %w", err)
			}
			if !seen[depID] {
				seen[depID] = true
				discovered[depID] = true
				toProcess = append(toProcess, depID)
			}
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return discovered, fmt.Errorf("iterate wisp dependents: %w", err)
		}
	}

	return discovered, nil
}

// GetRepoMtimeInTx returns the cached mtime (nanoseconds) for a repo path.
// Returns 0 if no cache entry exists.
func GetRepoMtimeInTx(ctx context.Context, tx *sql.Tx, repoPath string) (int64, error) {
	var mtimeNs int64
	err := tx.QueryRowContext(ctx,
		`SELECT mtime_ns FROM repo_mtimes WHERE repo_path = ?`, repoPath).Scan(&mtimeNs)
	if err != nil {
		return 0, nil
	}
	return mtimeNs, nil
}

// SetRepoMtimeInTx upserts the mtime cache for a repo path.
func SetRepoMtimeInTx(ctx context.Context, tx *sql.Tx, repoPath, jsonlPath string, mtimeNs int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO repo_mtimes (repo_path, jsonl_path, mtime_ns, last_checked)
		VALUES (?, ?, ?, NOW())
		ON DUPLICATE KEY UPDATE
			jsonl_path = VALUES(jsonl_path),
			mtime_ns = VALUES(mtime_ns),
			last_checked = NOW()
	`, repoPath, jsonlPath, mtimeNs)
	if err != nil {
		return fmt.Errorf("set repo mtime: %w", err)
	}
	return nil
}

// ClearRepoMtimeInTx removes the mtime cache entry for a repo path.
func ClearRepoMtimeInTx(ctx context.Context, tx *sql.Tx, repoPath string) error {
	// Expand ~ and resolve to absolute path to match stored format.
	absPath := expandAndAbsPath(repoPath)
	_, err := tx.ExecContext(ctx, `DELETE FROM repo_mtimes WHERE repo_path = ?`, absPath)
	if err != nil {
		return fmt.Errorf("clear repo mtime: %w", err)
	}
	return nil
}

func expandAndAbsPath(p string) string {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			if p == "~" {
				p = home
			} else {
				p = filepath.Join(home, p[2:])
			}
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
