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

// IssueSelectColumnsLite is the column list for lite issue hydration. It mirrors
// IssueSelectColumns in order, minus the heavy TEXT columns enumerated in
// HeavyDropList. Used when a caller opts in via types.IssueFilter.Lite=true to
// skip materializing large text bodies on listing paths.
//
// metadata is intentionally retained — it is small and read by routing.
//
// row_lock and the leases.* overlay (lease_expires_at, heartbeat_at,
// granted_node; lease_expires_at/heartbeat_at added by migration 0054 after
// this list was first written, see #4150; granted_node added by migration
// 0016/wy-jpd3.7 for replica-aware leases) are also retained: all three are
// small, non-TEXT columns that routing/claim code reads (optimistic
// concurrency token, active-lease state, granting replica), not the
// multi-KB bodies this split exists to skip. Any query selecting
// IssueSelectColumnsLite must include sqlbuild.LeaseJoin(table) in its FROM
// clause, exactly as full hydration does (see issueLiteProjection in
// search.go, joinLeases: true).
// The list itself lives in internal/storage/sqlbuild beside IssueSelectColumns,
// shared with the domain/db stack and with the counts mega-query, which renders
// a qualified variant of it.
const IssueSelectColumnsLite = sqlbuild.IssueSelectColumnsLite

// HeavyDropList enumerates the columns omitted from IssueSelectColumnsLite.
// Test-only: the schema-parity test asserts
//
//	cols(IssueSelectColumnsLite) ∪ HeavyDropList == cols(IssueSelectColumns)
//
// so every future column added to IssueSelectColumns must be classified
// explicitly — either into IssueSelectColumnsLite (small, routing/listing
// reads it) or into HeavyDropList (large body, fetch via GetIssue when
// needed). Production code paths reference IssueSelectColumns or
// IssueSelectColumnsLite directly; do not consume this list at runtime.
var HeavyDropList = []string{
	"description",
	"design",
	"acceptance_criteria",
	"notes",
	"waiters",
	"payload",
}

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
	var leaseGrantedNode sql.NullString          // granting replica (ignored migration 0016); NULL when no active lease
	var estimatedMinutes, originalSize, timeoutNs sql.NullInt64
	var createdBy sql.NullString
	var assignee, externalRef, specID, compactedAtCommit, owner sql.NullString
	var contentHash, sourceRepo, closeReason, closedBySession sql.NullString
	var workType, sourceSystem sql.NullString
	var sender, wispType, molType, eventKind, actor, target, payload sql.NullString
	var awaitType, awaitID, waiters sql.NullString
	var ephemeral, noHistory, pinned, isTemplate sql.NullInt64
	var metadata sql.NullString
	var rowLock sql.NullInt64       // row_lock column (NOT NULL DEFAULT 0); scanned defensively so NULL maps to 0
	var storageClass sql.NullString // storage_class column (migration 0060); NULL = unset, resolves per EffectiveStorageClass

	dests := []any{
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &createdBy, &owner, &updatedAtStr, &startedAt, &closedAt, &externalRef, &specID,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason, &closedBySession,
		&sender, &ephemeral, &noHistory, &wispType, &pinned, &isTemplate,
		&awaitType, &awaitID, &timeoutNs, &waiters,
		&molType,
		&eventKind, &actor, &target, &payload,
		&dueAt, &deferUntil,
		&workType, &sourceSystem, &metadata, &rowLock, &storageClass,
		&leaseExpiresAt, &heartbeatAt, &leaseGrantedNode,
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
	if closedBySession.Valid {
		issue.ClosedBySession = closedBySession.String
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
	// row_lock surfaced as the opaque RowVersion token. NOT NULL DEFAULT 0, so
	// this is normally valid; a NULL (defensive) maps to 0.
	issue.RowVersion = rowLock.Int64
	// storage_class (migration 0060); NULL = unset (EffectiveStorageClass
	// resolves the default per Protocol v0.1 C1.2).
	if storageClass.Valid {
		issue.StorageClass = types.StorageClass(storageClass.String)
	}
	// Lease columns (migration 0054); NULL when no active lease.
	if leaseExpiresAt.Valid {
		issue.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	if heartbeatAt.Valid {
		issue.HeartbeatAt = &heartbeatAt.Time
	}
	// Granting replica (ignored migration 0016); "" when the lease predates
	// the column or the deployment cannot name its replicas.
	issue.LeaseGrantedNode = leaseGrantedNode.String

	return &issue, nil
}

// ScanIssueLiteFrom scans a lite issue from any source implementing IssueScanner.
// The caller must ensure the query selected exactly IssueSelectColumnsLite in
// order. Heavy text fields (Description, Design, AcceptanceCriteria, Notes,
// Payload, Waiters) are NOT read from the row and remain zero-valued on the
// returned issue. The returned issue has IsLitePartial=true so downstream code
// can detect the partial hydration.
func ScanIssueLiteFrom(s IssueScanner, extra ...any) (*types.Issue, error) {
	var issue types.Issue
	var createdAtStr, updatedAtStr sql.NullString // TEXT columns - must parse manually
	var startedAt, closedAt, compactedAt, dueAt, deferUntil sql.NullTime
	var leaseExpiresAt, heartbeatAt sql.NullTime // lease columns (migration 0054); NULL when no active lease
	var leaseGrantedNode sql.NullString          // granting replica (migration 0016); NULL when no active lease
	var estimatedMinutes, originalSize, timeoutNs sql.NullInt64
	var createdBy sql.NullString
	var assignee, externalRef, specID, compactedAtCommit, owner sql.NullString
	var contentHash, sourceRepo, closeReason, closedBySession sql.NullString
	var workType, sourceSystem sql.NullString
	var sender, wispType, molType, eventKind, actor, target sql.NullString
	var awaitType, awaitID sql.NullString
	var ephemeral, noHistory, pinned, isTemplate sql.NullInt64
	var metadata sql.NullString
	var rowLock sql.NullInt64       // row_lock column (NOT NULL DEFAULT 0); scanned defensively so NULL maps to 0
	var storageClass sql.NullString // storage_class column (migration 0060); NULL = unset, resolves per EffectiveStorageClass

	dests := []any{
		&issue.ID, &contentHash, &issue.Title,
		&issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &createdBy, &owner, &updatedAtStr, &startedAt, &closedAt, &externalRef, &specID,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason, &closedBySession,
		&sender, &ephemeral, &noHistory, &wispType, &pinned, &isTemplate,
		&awaitType, &awaitID, &timeoutNs,
		&molType,
		&eventKind, &actor, &target,
		&dueAt, &deferUntil,
		&workType, &sourceSystem, &metadata, &rowLock, &storageClass,
		&leaseExpiresAt, &heartbeatAt, &leaseGrantedNode,
	}
	dests = append(dests, extra...)
	if err := s.Scan(dests...); err != nil {
		return nil, err
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
	if closedBySession.Valid {
		issue.ClosedBySession = closedBySession.String
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
	if metadata.Valid && metadata.String != "" && metadata.String != "{}" {
		issue.Metadata = []byte(metadata.String)
	}
	// row_lock surfaced as the opaque RowVersion token. NOT NULL DEFAULT 0, so
	// this is normally valid; a NULL (defensive) maps to 0.
	issue.RowVersion = rowLock.Int64
	// storage_class (migration 0060); NULL = unset (EffectiveStorageClass
	// resolves the default per Protocol v0.1 C1.2).
	if storageClass.Valid {
		issue.StorageClass = types.StorageClass(storageClass.String)
	}
	// Lease columns (migration 0054); NULL when no active lease.
	if leaseExpiresAt.Valid {
		issue.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	if heartbeatAt.Valid {
		issue.HeartbeatAt = &heartbeatAt.Time
	}
	// Granting replica (migration 0016); "" when the lease predates the
	// column or the deployment cannot name its replicas.
	issue.LeaseGrantedNode = leaseGrantedNode.String

	issue.IsLitePartial = true
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
