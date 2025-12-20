// Package sqlite implements multi-repo hydration for the SQLite storage backend.
package sqlite

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// HydrateFromMultiRepo loads issues from all configured repositories into the database.
// Uses mtime caching to skip unchanged JSONL files for performance.
// Returns the number of issues imported from each repo.
func (s *SQLiteStorage) HydrateFromMultiRepo(ctx context.Context) (map[string]int, error) {
	// Get multi-repo config
	multiRepo := config.GetMultiRepoConfig()
	if multiRepo == nil {
		// Single-repo mode - nothing to hydrate
		return nil, nil
	}

	results := make(map[string]int)

	// Process primary repo first (if set)
	if multiRepo.Primary != "" {
		count, err := s.hydrateFromRepo(ctx, multiRepo.Primary, ".")
		if err != nil {
			return nil, fmt.Errorf("failed to hydrate primary repo %s: %w", multiRepo.Primary, err)
		}
		results["."] = count
	}

	// Process additional repos
	for _, repoPath := range multiRepo.Additional {
		// Expand tilde in path
		expandedPath, err := expandTilde(repoPath)
		if err != nil {
			return nil, fmt.Errorf("failed to expand path %s: %w", repoPath, err)
		}

		// Use relative path as source_repo identifier
		relPath := repoPath // Keep original for source_repo field
		count, err := s.hydrateFromRepo(ctx, expandedPath, relPath)
		if err != nil {
			return nil, fmt.Errorf("failed to hydrate repo %s: %w", repoPath, err)
		}
		results[relPath] = count
	}

	return results, nil
}

// hydrateFromRepo loads issues from a single repository's JSONL file.
// Uses mtime caching to skip unchanged files.
func (s *SQLiteStorage) hydrateFromRepo(ctx context.Context, repoPath, sourceRepo string) (int, error) {
	// Get absolute path to repo
	absRepoPath, err := filepath.Abs(repoPath)
	if err != nil {
		return 0, fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Construct path to JSONL file
	jsonlPath := filepath.Join(absRepoPath, ".beads", "issues.jsonl")

	// Check if file exists
	// Use Lstat to get the symlink's own mtime, not the target's (NixOS fix).
	fileInfo, err := os.Lstat(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No JSONL file - skip this repo
			return 0, nil
		}
		return 0, fmt.Errorf("failed to stat JSONL file: %w", err)
	}

	// Get current mtime
	currentMtime := fileInfo.ModTime().UnixNano()

	// Check cached mtime
	var cachedMtime int64
	err = s.db.QueryRowContext(ctx, `
		SELECT mtime_ns FROM repo_mtimes WHERE repo_path = ?
	`, absRepoPath).Scan(&cachedMtime)

	if err == nil && cachedMtime == currentMtime {
		// File hasn't changed - skip import
		return 0, nil
	}

	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("failed to query mtime cache: %w", err)
	}

	// Import issues from JSONL
	count, err := s.importJSONLFile(ctx, jsonlPath, sourceRepo)
	if err != nil {
		return 0, fmt.Errorf("failed to import JSONL: %w", err)
	}

	// Update mtime cache
	_, err = s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO repo_mtimes (repo_path, jsonl_path, mtime_ns, last_checked)
		VALUES (?, ?, ?, ?)
	`, absRepoPath, jsonlPath, currentMtime, time.Now())
	if err != nil {
		return 0, fmt.Errorf("failed to update mtime cache: %w", err)
	}

	return count, nil
}

// importJSONLFile imports issues from a JSONL file, setting the source_repo field.
// Disables FK checks during import to handle out-of-order dependencies.
func (s *SQLiteStorage) importJSONLFile(ctx context.Context, jsonlPath, sourceRepo string) (int, error) {
	file, err := os.Open(jsonlPath) // #nosec G304 -- jsonlPath is from trusted source
	if err != nil {
		return 0, fmt.Errorf("failed to open JSONL file: %w", err)
	}
	defer file.Close()

	// Fetch custom statuses for validation (bd-1pj6)
	customStatuses, err := s.GetCustomStatuses(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get custom statuses: %w", err)
	}

	scanner := bufio.NewScanner(file)
	// Increase buffer size for large issues
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // 10MB max line size

	count := 0
	lineNum := 0

	// Get exclusive connection to ensure PRAGMA applies
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Disable foreign keys on this connection to handle out-of-order deps
	// (issue A may depend on issue B that appears later in the file)
	_, err = conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`)
	if err != nil {
		return 0, fmt.Errorf("failed to disable foreign keys: %w", err)
	}

	// Begin transaction for bulk import
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return 0, fmt.Errorf("failed to parse JSON at line %d: %w", lineNum, err)
		}

		// Set source_repo field
		issue.SourceRepo = sourceRepo

		// Compute content hash if missing
		if issue.ContentHash == "" {
			issue.ContentHash = issue.ComputeContentHash()
		}

		// Insert or update issue (with custom status support)
		if err := s.upsertIssueInTx(ctx, tx, &issue, customStatuses); err != nil {
			return 0, fmt.Errorf("failed to import issue %s at line %d: %w", issue.ID, lineNum, err)
		}

		count++
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("failed to read JSONL file: %w", err)
	}

	// Re-enable foreign keys before commit to validate data integrity
	_, err = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	if err != nil {
		return 0, fmt.Errorf("failed to re-enable foreign keys: %w", err)
	}

	// Validate FK constraints on imported data
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return 0, fmt.Errorf("failed to check foreign keys: %w", err)
	}
	defer rows.Close()

	if rows.Next() {
		var table, rowid, parent, fkid string
		_ = rows.Scan(&table, &rowid, &parent, &fkid)
		return 0, fmt.Errorf(
			"foreign key violation in imported data: table=%s rowid=%s parent=%s",
			table, rowid, parent,
		)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return count, nil
}

// upsertIssueInTx inserts or updates an issue within a transaction.
// Uses INSERT OR REPLACE to handle both new and existing issues.
func (s *SQLiteStorage) upsertIssueInTx(ctx context.Context, tx *sql.Tx, issue *types.Issue, customStatuses []string) error {
	// Defensive fix for closed_at invariant (GH#523): older versions of bd could
	// close issues without setting closed_at. Fix by using max(created_at, updated_at) + 1s.
	if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		closedAt := maxTime.Add(time.Second)
		issue.ClosedAt = &closedAt
	}

	// Defensive fix for deleted_at invariant: tombstones must have deleted_at
	if issue.Status == types.StatusTombstone && issue.DeletedAt == nil {
		maxTime := issue.CreatedAt
		if issue.UpdatedAt.After(maxTime) {
			maxTime = issue.UpdatedAt
		}
		deletedAt := maxTime.Add(time.Second)
		issue.DeletedAt = &deletedAt
	}

	// Validate issue (with custom status support, bd-1pj6)
	if err := issue.ValidateWithCustomStatuses(customStatuses); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Check if issue exists
	var existingID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM issues WHERE id = ?`, issue.ID).Scan(&existingID)

	ephemeral := 0
	if issue.Ephemeral {
		ephemeral = 1
	}
	pinned := 0
	if issue.Pinned {
		pinned = 1
	}
	isTemplate := 0
	if issue.IsTemplate {
		isTemplate = 1
	}

	if err == sql.ErrNoRows {
		// Issue doesn't exist - insert it
		_, err = tx.ExecContext(ctx, `
			INSERT INTO issues (
				id, content_hash, title, description, design, acceptance_criteria, notes,
				status, priority, issue_type, assignee, estimated_minutes,
				created_at, updated_at, closed_at, external_ref, source_repo, close_reason,
				deleted_at, deleted_by, delete_reason, original_type,
				sender, ephemeral, pinned, is_template
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			issue.ID, issue.ContentHash, issue.Title, issue.Description, issue.Design,
			issue.AcceptanceCriteria, issue.Notes, issue.Status,
			issue.Priority, issue.IssueType, issue.Assignee,
			issue.EstimatedMinutes, issue.CreatedAt, issue.UpdatedAt,
			issue.ClosedAt, issue.ExternalRef, issue.SourceRepo, issue.CloseReason,
			issue.DeletedAt, issue.DeletedBy, issue.DeleteReason, issue.OriginalType,
			issue.Sender, ephemeral, pinned, isTemplate,
		)
		if err != nil {
			return fmt.Errorf("failed to insert issue: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to check existing issue: %w", err)
	} else {
		// Issue exists - update it
		// Only update if content_hash is different (avoid unnecessary writes)
		var existingHash string
		err = tx.QueryRowContext(ctx, `SELECT content_hash FROM issues WHERE id = ?`, issue.ID).Scan(&existingHash)
		if err != nil {
			return fmt.Errorf("failed to get existing hash: %w", err)
		}

		if existingHash != issue.ContentHash {
			_, err = tx.ExecContext(ctx, `
				UPDATE issues SET
					content_hash = ?, title = ?, description = ?, design = ?,
					acceptance_criteria = ?, notes = ?, status = ?, priority = ?,
					issue_type = ?, assignee = ?, estimated_minutes = ?,
					updated_at = ?, closed_at = ?, external_ref = ?, source_repo = ?,
					deleted_at = ?, deleted_by = ?, delete_reason = ?, original_type = ?,
					sender = ?, ephemeral = ?, pinned = ?, is_template = ?
				WHERE id = ?
			`,
				issue.ContentHash, issue.Title, issue.Description, issue.Design,
				issue.AcceptanceCriteria, issue.Notes, issue.Status, issue.Priority,
				issue.IssueType, issue.Assignee, issue.EstimatedMinutes,
				issue.UpdatedAt, issue.ClosedAt, issue.ExternalRef, issue.SourceRepo,
				issue.DeletedAt, issue.DeletedBy, issue.DeleteReason, issue.OriginalType,
				issue.Sender, ephemeral, pinned, isTemplate,
				issue.ID,
			)
			if err != nil {
				return fmt.Errorf("failed to update issue: %w", err)
			}
		}
	}

	// Import dependencies if present
	for _, dep := range issue.Dependencies {
		_, err = tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO dependencies (issue_id, depends_on_id, type, created_at, created_by)
			VALUES (?, ?, ?, ?, ?)
		`, dep.IssueID, dep.DependsOnID, dep.Type, dep.CreatedAt, dep.CreatedBy)
		if err != nil {
			return fmt.Errorf("failed to import dependency: %w", err)
		}
	}

	// Import labels if present
	for _, label := range issue.Labels {
		_, err = tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO labels (issue_id, label)
			VALUES (?, ?)
		`, issue.ID, label)
		if err != nil {
			return fmt.Errorf("failed to import label: %w", err)
		}
	}

	// Import comments if present
	for _, comment := range issue.Comments {
		_, err = tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO comments (id, issue_id, author, text, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, comment.ID, comment.IssueID, comment.Author, comment.Text, comment.CreatedAt)
		if err != nil {
			return fmt.Errorf("failed to import comment: %w", err)
		}
	}

	return nil
}

// expandTilde expands ~ in a file path to the user's home directory.
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	if path == "~" {
		return homeDir, nil
	}

	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir, path[2:]), nil
	}

	// ~user not supported
	return path, nil
}
