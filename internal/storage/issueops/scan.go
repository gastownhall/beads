package issueops

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// IssueSelectColumns is the canonical column list for full issue hydration.
// Every query that reads a complete types.Issue from the issues table should
// use this constant to avoid column-list drift between scan sites. The list
// itself lives in internal/storage/sqlbuild, shared with the domain/db stack;
// ScanIssueFrom below scans it positionally and must stay in agreement.
const IssueSelectColumns = sqlbuild.IssueSelectColumns

// IssueScanner is the common interface between *sql.Row and *sql.Rows,
// allowing a single scan function to work with both single-row and
// multi-row query results.
type IssueScanner interface {
	Scan(dest ...any) error
}

// ScanIssueFrom scans a full issue from any source implementing IssueScanner.
// The caller must ensure the query selected exactly IssueSelectColumns in
// order; any extra dests are appended for trailing columns beyond that list.
func ScanIssueFrom(s IssueScanner, extra ...any) (*types.Issue, error) {
	var issue types.Issue
	var createdAtStr, updatedAtStr sql.NullString // scanned as strings, parsed with format fallbacks
	var startedAt, closedAt, compactedAt, dueAt, deferUntil sql.NullTime
	var leaseExpiresAt, heartbeatAt sql.NullTime // lease columns (migration 0054); NULL when no active lease
	var estimatedMinutes, originalSize, timeoutNs sql.NullInt64
	var createdBy sql.NullString
	var assignee, externalRef, specID, compactedAtCommit, owner sql.NullString
	var contentHash, sourceRepo, closeReason sql.NullString
	var workType, sourceSystem sql.NullString
	var sender, wispType, molType, eventKind, actor, target, payload sql.NullString
	var awaitType, awaitID, waiters sql.NullString
	var ephemeral, noHistory, pinned, isTemplate sql.NullInt64
	var metadata sql.NullString

	dests := []any{
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &createdBy, &owner, &updatedAtStr, &startedAt, &closedAt, &externalRef, &specID,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&sender, &ephemeral, &noHistory, &wispType, &pinned, &isTemplate,
		&awaitType, &awaitID, &timeoutNs, &waiters,
		&molType,
		&eventKind, &actor, &target, &payload,
		&dueAt, &deferUntil,
		&workType, &sourceSystem, &metadata,
		&leaseExpiresAt, &heartbeatAt,
	}
	dests = append(dests, extra...)
	if err := s.Scan(dests...); err != nil {
		return nil, err
	}

	// Parse timestamp strings (TEXT columns require manual parsing)
	if createdAtStr.Valid {
		issue.CreatedAt = ParseTimeString(createdAtStr.String)
	}
	if updatedAtStr.Valid {
		issue.UpdatedAt = ParseTimeString(updatedAtStr.String)
	}

	// Map nullable fields
	if contentHash.Valid {
		issue.ContentHash = contentHash.String
	}
	if startedAt.Valid {
		issue.StartedAt = &startedAt.Time
	}
	if closedAt.Valid {
		issue.ClosedAt = &closedAt.Time
	}
	if estimatedMinutes.Valid {
		mins := int(estimatedMinutes.Int64)
		issue.EstimatedMinutes = &mins
	}
	if assignee.Valid {
		issue.Assignee = assignee.String
	}
	if createdBy.Valid {
		issue.CreatedBy = createdBy.String
	}
	if owner.Valid {
		issue.Owner = owner.String
	}
	if externalRef.Valid {
		issue.ExternalRef = &externalRef.String
	}
	if specID.Valid {
		issue.SpecID = specID.String
	}
	if compactedAt.Valid {
		issue.CompactedAt = &compactedAt.Time
	}
	if compactedAtCommit.Valid {
		issue.CompactedAtCommit = &compactedAtCommit.String
	}
	if originalSize.Valid {
		issue.OriginalSize = int(originalSize.Int64)
	}
	if sourceRepo.Valid {
		issue.SourceRepo = sourceRepo.String
	}
	if closeReason.Valid {
		issue.CloseReason = closeReason.String
	}
	if sender.Valid {
		issue.Sender = sender.String
	}
	if ephemeral.Valid && ephemeral.Int64 != 0 {
		issue.Ephemeral = true
	}
	if noHistory.Valid && noHistory.Int64 != 0 {
		issue.NoHistory = true
	}
	if wispType.Valid {
		issue.WispType = types.WispType(wispType.String)
	}
	if pinned.Valid && pinned.Int64 != 0 {
		issue.Pinned = true
	}
	if isTemplate.Valid && isTemplate.Int64 != 0 {
		issue.IsTemplate = true
	}
	if awaitType.Valid {
		issue.AwaitType = awaitType.String
	}
	if awaitID.Valid {
		issue.AwaitID = awaitID.String
	}
	if timeoutNs.Valid {
		issue.Timeout = time.Duration(timeoutNs.Int64)
	}
	if waiters.Valid && waiters.String != "" {
		issue.Waiters = ParseJSONStringArray(waiters.String)
	}
	if molType.Valid {
		issue.MolType = types.MolType(molType.String)
	}
	if eventKind.Valid {
		issue.EventKind = eventKind.String
	}
	if actor.Valid {
		issue.Actor = actor.String
	}
	if target.Valid {
		issue.Target = target.String
	}
	if payload.Valid {
		issue.Payload = payload.String
	}
	if dueAt.Valid {
		issue.DueAt = &dueAt.Time
	}
	if deferUntil.Valid {
		issue.DeferUntil = &deferUntil.Time
	}
	if workType.Valid {
		issue.WorkType = types.WorkType(workType.String)
	}
	if sourceSystem.Valid {
		issue.SourceSystem = sourceSystem.String
	}
	// Custom metadata field (GH#1406)
	if metadata.Valid && metadata.String != "" && metadata.String != "{}" {
		issue.Metadata = []byte(metadata.String)
	}
	// Lease columns (migration 0054); NULL when no active lease.
	if leaseExpiresAt.Valid {
		issue.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	if heartbeatAt.Valid {
		issue.HeartbeatAt = &heartbeatAt.Time
	}

	return &issue, nil
}

// ParseTimeString parses a time string from database TEXT columns (non-nullable).
// Supports RFC3339Nano, RFC3339, and MySQL DATETIME format.
func ParseTimeString(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Try RFC3339Nano first (more precise), then RFC3339, then DATETIME format
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{} // Unparseable - shouldn't happen with valid data
}

// ParseJSONStringArray unmarshals a JSON string array. Returns nil on error or empty input.
func ParseJSONStringArray(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil
	}
	return result
}
