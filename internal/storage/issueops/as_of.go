package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// validRefPattern matches valid Dolt commit hashes (32 hex chars) or branch names.
// Allows dots and slashes for branch names like "release/v2.0" or "feature/auth.flow".
var validRefPattern = regexp.MustCompile(`^[a-zA-Z0-9_./-]+$`)

// truncatedHashPattern matches strings that could plausibly be a truncated
// Dolt commit hash: 16-31 characters, entirely within Dolt's base32 hash
// alphabet (see doltCommitHashRE in blocked_merge.go for the full 32-char
// form). This is a heuristic, not a certainty, so the lower bound matters:
// an earlier version of this pattern started at 1 character and caught real
// short branch names spelled entirely with digits and the letters a-v (e.g.
// "main", "dev", "cafe") as false positives. Raising the floor to 16 trades
// away the specific error for very short hand-typed truncations (e.g. the
// 8-char prefix `bd history` used to print) in exchange for never
// false-positiving on a real short branch name again -- an accepted
// tradeoff, since the 8-char case was already fixed at its source (bd
// history now prints the full hash) and no longer needs catching here.
var truncatedHashPattern = regexp.MustCompile(`^[0-9a-v]{16,31}$`)

// ValidateRef checks if a ref string is safe to use in AS OF queries.
// Refs must be non-empty, <= 128 chars, and match [a-zA-Z0-9_./-]+.
func ValidateRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("ref cannot be empty")
	}
	if len(ref) > 128 {
		return fmt.Errorf("ref too long")
	}
	if !validRefPattern.MatchString(ref) {
		return fmt.Errorf("invalid ref format: %s", ref)
	}
	if truncatedHashPattern.MatchString(ref) {
		return fmt.Errorf("%q looks like a truncated commit hash -- Dolt's AS OF does not support hash-prefix resolution; use the full 32-character hash (bd history --json) or an exact branch name", ref)
	}
	return nil
}

// AsOfInTx returns the state of an issue at a specific commit hash or branch ref.
// Uses Dolt's AS OF syntax which works in both server and embedded modes.
//
// nolint:gosec // G201: ref is validated by ValidateRef() above - AS OF requires literal
func AsOfInTx(ctx context.Context, tx DBTX, issueID string, ref string) (*types.Issue, error) {
	if err := ValidateRef(ref); err != nil {
		return nil, fmt.Errorf("invalid ref: %w", err)
	}

	var issue types.Issue
	var createdAtStr, updatedAtStr sql.NullString
	var closedAt sql.NullTime
	var assignee, owner, contentHash sql.NullString
	var estimatedMinutes sql.NullInt64

	query := fmt.Sprintf(`
		SELECT id, content_hash, title, description, status, priority, issue_type, assignee, estimated_minutes,
		       created_at, created_by, owner, updated_at, closed_at
		FROM issues AS OF '%s'
		WHERE id = ?
	`, ref)

	err := tx.QueryRowContext(ctx, query, issueID).Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Status, &issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &issue.CreatedBy, &owner, &updatedAtStr, &closedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: issue %s as of %s", storage.ErrNotFound, issueID, ref)
	}
	if err != nil {
		return nil, fmt.Errorf("get issue as of %s: %w", ref, err)
	}

	if createdAtStr.Valid {
		issue.CreatedAt = ParseTimeString(createdAtStr.String)
	}
	if updatedAtStr.Valid {
		issue.UpdatedAt = ParseTimeString(updatedAtStr.String)
	}
	if contentHash.Valid {
		issue.ContentHash = contentHash.String
	}
	if closedAt.Valid {
		issue.ClosedAt = &closedAt.Time
	}
	if assignee.Valid {
		issue.Assignee = assignee.String
	}
	if owner.Valid {
		issue.Owner = owner.String
	}
	if estimatedMinutes.Valid {
		mins := int(estimatedMinutes.Int64)
		issue.EstimatedMinutes = &mins
	}

	return &issue, nil
}
