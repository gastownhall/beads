package dolt

import (
	"database/sql"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// issueSelectColumns is the canonical column list for full issue hydration.
// Every query that reads a complete types.Issue from the issues table should
// use this constant to avoid column-list drift between scan sites.
const issueSelectColumns = `id, content_hash, title, description, design, acceptance_criteria, notes,
	       status, priority, issue_type, assignee, estimated_minutes,
	       created_at, created_by, owner, updated_at, closed_at, external_ref, spec_id,
	       compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
	       sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
	       await_type, await_id, timeout_ns, waiters,
	       hook_bead, role_bead, agent_state, last_activity, role_type, rig, mol_type,
	       event_kind, actor, target, payload,
	       due_at, defer_until,
	       quality_score, work_type, source_system, metadata`

// issueScanner is the common interface between *sql.Row and *sql.Rows,
// allowing a single scan function to work with both single-row and
// multi-row query results.
type issueScanner interface {
	Scan(dest ...any) error
}

// scanIssueFrom scans a full issue from any source implementing issueScanner.
// The caller must ensure the query selected exactly issueSelectColumns in order.
func scanIssueFrom(s issueScanner) (*types.Issue, error) {
	var issue types.Issue
	var createdAtStr, updatedAtStr sql.NullString // TEXT columns - must parse manually
	var closedAt, compactedAt, lastActivity, dueAt, deferUntil sql.NullTime
	var estimatedMinutes, originalSize, timeoutNs sql.NullInt64
	var createdBy sql.NullString
	var assignee, externalRef, specID, compactedAtCommit, owner sql.NullString
	var contentHash, sourceRepo, closeReason sql.NullString
	var workType, sourceSystem sql.NullString
	var sender, wispType, molType, eventKind, actor, target, payload sql.NullString
	var awaitType, awaitID, waiters sql.NullString
	var hookBead, roleBead, agentState, roleType, rig sql.NullString
	var ephemeral, pinned, isTemplate, crystallizes sql.NullInt64
	var qualityScore sql.NullFloat64
	var metadata sql.NullString

	if err := s.Scan(
		&issue.ID, &contentHash, &issue.Title, &issue.Description, &issue.Design,
		&issue.AcceptanceCriteria, &issue.Notes, &issue.Status,
		&issue.Priority, &issue.IssueType, &assignee, &estimatedMinutes,
		&createdAtStr, &createdBy, &owner, &updatedAtStr, &closedAt, &externalRef, &specID,
		&issue.CompactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
		&sender, &ephemeral, &wispType, &pinned, &isTemplate, &crystallizes,
		&awaitType, &awaitID, &timeoutNs, &waiters,
		&hookBead, &roleBead, &agentState, &lastActivity, &roleType, &rig, &molType,
		&eventKind, &actor, &target, &payload,
		&dueAt, &deferUntil,
		&qualityScore, &workType, &sourceSystem, &metadata,
	); err != nil {
		return nil, err
	}

	// Parse timestamp strings (TEXT columns require manual parsing)
	if createdAtStr.Valid {
		issue.CreatedAt = parseTimeString(createdAtStr.String)
	}
	if updatedAtStr.Valid {
		issue.UpdatedAt = parseTimeString(updatedAtStr.String)
	}

	// Map nullable fields
	if contentHash.Valid {
		issue.ContentHash = contentHash.String
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
	if wispType.Valid {
		issue.WispType = types.WispType(wispType.String)
	}
	if pinned.Valid && pinned.Int64 != 0 {
		issue.Pinned = true
	}
	if isTemplate.Valid && isTemplate.Int64 != 0 {
		issue.IsTemplate = true
	}
	if crystallizes.Valid && crystallizes.Int64 != 0 {
		issue.Crystallizes = true
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
		issue.Waiters = parseJSONStringArray(waiters.String)
	}
	if hookBead.Valid {
		issue.HookBead = hookBead.String
	}
	if roleBead.Valid {
		issue.RoleBead = roleBead.String
	}
	if agentState.Valid {
		issue.AgentState = types.AgentState(agentState.String)
	}
	if lastActivity.Valid {
		issue.LastActivity = &lastActivity.Time
	}
	if roleType.Valid {
		issue.RoleType = roleType.String
	}
	if rig.Valid {
		issue.Rig = rig.String
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
	if qualityScore.Valid {
		qs := float32(qualityScore.Float64)
		issue.QualityScore = &qs
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

	return &issue, nil
}
