package issueops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// IsAllowedUpdateField checks if a field name is valid for issue updates.
func IsAllowedUpdateField(key string) bool {
	allowed := map[string]bool{
		"status": true, "priority": true, "title": true, "assignee": true,
		"description": true, "design": true, "acceptance_criteria": true, "notes": true,
		"issue_type": true, "estimated_minutes": true, "external_ref": true, "spec_id": true,
		"started_at": true,
		"closed_at":  true, "close_reason": true, "closed_by_session": true,
		"source_repo": true,
		"sender":      true, "wisp": true, "wisp_type": true, "no_history": true, "pinned": true,
		"mol_type":       true,
		"event_category": true, "event_actor": true, "event_target": true, "event_payload": true,
		"due_at": true, "defer_until": true, "await_id": true, "waiters": true,
		"metadata": true,
	}
	return allowed[key]
}

// ManageClosedAt auto-sets closed_at when closing or clears it when reopening.
func ManageClosedAt(oldIssue *types.Issue, updates map[string]interface{}, setClauses []string, args []interface{}) ([]string, []interface{}) {
	statusVal, hasStatus := updates["status"]
	_, hasExplicitClosedAt := updates["closed_at"]
	if hasExplicitClosedAt || !hasStatus {
		return setClauses, args
	}

	var newStatus string
	switch v := statusVal.(type) {
	case string:
		newStatus = v
	case types.Status:
		newStatus = string(v)
	default:
		return setClauses, args
	}

	if newStatus == string(types.StatusClosed) {
		now := time.Now().UTC()
		setClauses = append(setClauses, "closed_at = ?")
		args = append(args, now)
	} else if oldIssue.Status == types.StatusClosed {
		setClauses = append(setClauses, "closed_at = ?", "close_reason = ?")
		args = append(args, nil, "")
	}

	return setClauses, args
}

// ManageStartedAt auto-sets started_at when transitioning to in_progress.
// If the issue already has a started_at, it is preserved (not overwritten).
func ManageStartedAt(oldIssue *types.Issue, updates map[string]interface{}, setClauses []string, args []interface{}) ([]string, []interface{}) {
	statusVal, hasStatus := updates["status"]
	_, hasExplicitStartedAt := updates["started_at"]
	if hasExplicitStartedAt || !hasStatus {
		return setClauses, args
	}

	var newStatus string
	switch v := statusVal.(type) {
	case string:
		newStatus = v
	case types.Status:
		newStatus = string(v)
	default:
		return setClauses, args
	}

	if newStatus == string(types.StatusInProgress) && oldIssue.StartedAt == nil {
		now := time.Now().UTC()
		setClauses = append(setClauses, "started_at = ?")
		args = append(args, now)
	}

	return setClauses, args
}

// ManageLeaseOnUpdate keeps lease ownership coherent when generic updates alter
// status or assignee. Leases are armed ONLY by the lease-aware verbs — claim
// (ClaimIssueInTx, bd update --claim, bd ready --claim) and heartbeat — never by
// a generic update. A bare `bd update -s in_progress -a <who>` is an interactive
// hand-dole claim: nobody is heartbeating it, so arming a lease here just turns
// the claim into reclaim-bait that reverts to open after the TTL (bd-9hpgf,
// GH#4716). This helper therefore only ever CLEARS lease columns:
//
//   - the update moves the row out of the claimed state (not in_progress, or
//     unassigned): any lease is stale — clear it.
//   - the update changes who holds the claim (assignee transfer, or a fresh
//     transition into in_progress): the previous owner's lease must not count
//     down against the new holder — clear it. The new holder gets a lease only
//     via the claim verb; a real worker's next heartbeat re-arms one.
//   - the update leaves the same claim in place (already in_progress, same
//     assignee): leave the lease untouched, so a worker's live lease survives
//     unrelated edits to its issue.
//
// Returns true when the update ends/transfers the claim and the issue's lease
// row must be deleted (DeleteLeaseInTx) after the row update.
func ManageLeaseOnUpdate(oldIssue *types.Issue, updates map[string]interface{}) bool {
	rawStatus, hasStatus := updates["status"]
	rawAssignee, hasAssignee := updates["assignee"]
	if !hasStatus && !hasAssignee {
		return false
	}

	newStatus := string(oldIssue.Status)
	if hasStatus {
		switch v := rawStatus.(type) {
		case string:
			newStatus = v
		case types.Status:
			newStatus = string(v)
		default:
			return false
		}
	}

	newAssignee := oldIssue.Assignee
	if hasAssignee {
		switch v := rawAssignee.(type) {
		case nil:
			newAssignee = ""
		case string:
			newAssignee = v
		default:
			newAssignee = fmt.Sprint(v)
		}
	}

	sameClaim := newStatus == string(types.StatusInProgress) && newAssignee != "" &&
		oldIssue.Status == types.StatusInProgress && newAssignee == oldIssue.Assignee
	return !sameClaim
}

// DetermineEventType returns the appropriate event type for an update.
func DetermineEventType(oldIssue *types.Issue, updates map[string]interface{}) types.EventType {
	statusVal, hasStatus := updates["status"]
	if !hasStatus {
		return types.EventUpdated
	}

	var newStatus string
	switch v := statusVal.(type) {
	case string:
		newStatus = v
	case types.Status:
		newStatus = string(v)
	default:
		return types.EventUpdated
	}

	if newStatus == string(types.StatusClosed) {
		return types.EventClosed
	}
	if oldIssue.Status == types.StatusClosed {
		return types.EventReopened
	}
	return types.EventStatusChanged
}

// UpdateResult holds the result of an UpdateIssueInTx call.
type UpdateResult struct {
	OldIssue *types.Issue
	IsWisp   bool
}

// UpdateIssueInTx performs the full update SQL logic within a transaction.
// It routes to the correct table (issues/wisps) automatically.
// The caller is responsible for Dolt versioning (DOLT_ADD/COMMIT) if needed.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func UpdateIssueInTx(ctx context.Context, tx DBTX, id string, updates map[string]interface{}, actor string) (*UpdateResult, error) {
	return updateIssueInTx(ctx, tx, id, updates, actor, true)
}

// UpdateIssueWithoutEventInTx applies normal update semantics without recording
// an intermediate event. Demotion uses this to preserve the historical event
// stream: create/update history is copied, then a single demotion event is added.
func UpdateIssueWithoutEventInTx(ctx context.Context, tx DBTX, id string, updates map[string]interface{}, actor string) (*UpdateResult, error) {
	return updateIssueInTx(ctx, tx, id, updates, actor, false)
}

func updateIssueInTx(ctx context.Context, tx DBTX, id string, updates map[string]interface{}, actor string, recordEvent bool) (*UpdateResult, error) {
	// Route to correct table.
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	// Read the pre-update row inside the transaction and fold any
	// read-merge-write operations (metadata edits, note appends) into concrete
	// column values against THAT row. On Dolt — the only supported backend — a
	// stale snapshot is made safe not by row locks (FOR UPDATE is a parse-only
	// no-op) but by commit-time conflict detection plus the store's
	// whole-attempt retry (withRetryTx), which re-runs this resolution against
	// the winning writer's committed row. Merging outside the mutation
	// transaction — the CLI's old behavior — silently erased concurrent
	// committed writes to sibling keys (GH audit: 7 of 200 exit-0
	// --set-metadata writes lost).
	oldIssue, updates, err := readIssueAndResolveMergeOps(ctx, tx, id, updates)
	if err != nil {
		return nil, err
	}

	// Validate issue_type against built-in + custom types (GH#3030).
	// This mirrors the create path (PrepareIssueForInsert → ValidateWithCustom)
	// and reads custom types from the same transaction, so it works reliably
	// even in subprocess contexts where the CLI-level store may be unavailable.
	if rawType, ok := updates["issue_type"]; ok {
		if issueType, ok := rawType.(string); ok {
			customTypes, err := ResolveCustomTypesInTx(ctx, tx)
			if err != nil {
				return nil, fmt.Errorf("failed to get custom types for validation: %w", err)
			}
			if !types.IssueType(issueType).IsValidWithCustom(customTypes) {
				return nil, fmt.Errorf("invalid issue type: %s", issueType)
			}
		}
	}

	// Build SET clauses.
	setClauses := []string{"updated_at = ?"}
	args := []interface{}{time.Now().UTC()}

	for key, value := range updates {
		if !IsAllowedUpdateField(key) {
			return nil, fmt.Errorf("invalid field for update: %s", key)
		}

		columnName := key
		if key == "wisp" {
			columnName = "ephemeral"
		}
		setClauses = append(setClauses, fmt.Sprintf("`%s` = ?", columnName))

		// Handle JSON serialization for array fields stored as TEXT.
		if key == "waiters" {
			waitersJSON, _ := json.Marshal(value)
			args = append(args, string(waitersJSON))
		} else if key == "metadata" {
			metadataStr, err := storage.NormalizeMetadataValue(value)
			if err != nil {
				return nil, fmt.Errorf("invalid metadata: %w", err)
			}
			args = append(args, metadataStr)
		} else {
			args = append(args, value)
		}
	}

	// Auto-clear pinned column when status transitions away from "pinned".
	if rawStatus, ok := updates["status"]; ok {
		var statusStr string
		switch v := rawStatus.(type) {
		case string:
			statusStr = v
		case types.Status:
			statusStr = string(v)
		}
		if oldIssue.Pinned && statusStr != string(types.StatusPinned) {
			if _, alreadySet := updates["pinned"]; !alreadySet {
				setClauses = append(setClauses, "`pinned` = ?")
				args = append(args, false)
			}
		}
	}

	// Auto-manage closed_at (set on close, clear on reopen).
	setClauses, args = ManageClosedAt(oldIssue, updates, setClauses, args)

	// Auto-manage started_at (set on transition to in_progress). (GH#2796)
	setClauses, args = ManageStartedAt(oldIssue, updates, setClauses, args)

	// Auto-manage leases when direct updates change status or assignee.
	// Clears stale leases only; arming is reserved for claim/heartbeat.
	clearLease := ManageLeaseOnUpdate(oldIssue, updates)

	// Rewrite row_lock on every update so a concurrent status/ownership
	// mutation (reclaim/close) collides on this shared cell and is forced to
	// conflict-and-retry rather than silently cell-merging two writes to
	// different columns of the same row (see lease.go). This is the "every
	// mutating path writes row_lock" invariant the lease scheme depends on.
	setClauses = append(setClauses, "row_lock = ?")
	args = append(args, freshRowLock())

	args = append(args, id)

	//nolint:gosec // G201: issueTable comes from WispTableRouting (hardcoded constants)
	query := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", issueTable, strings.Join(setClauses, ", "))
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return nil, fmt.Errorf("failed to update issue: %w", err)
	}

	if clearLease {
		if err := DeleteLeaseInTx(ctx, tx, id); err != nil {
			return nil, err
		}
	}

	if recordEvent {
		oldData, _ := json.Marshal(oldIssue)
		newData, _ := json.Marshal(updates)
		eventType := DetermineEventType(oldIssue, updates)

		if err := RecordFullEventInTable(ctx, tx, eventTable, id, eventType, actor, string(oldData), string(newData)); err != nil {
			return nil, fmt.Errorf("failed to record event: %w", err)
		}
	}

	if rawStatus, hasStatus := updates["status"]; hasStatus {
		var newStatus string
		switch v := rawStatus.(type) {
		case string:
			newStatus = v
		case types.Status:
			newStatus = string(v)
		}
		oldActive := oldIssue.Status != types.StatusClosed && oldIssue.Status != types.StatusPinned
		newActive := newStatus != string(types.StatusClosed) && newStatus != string(types.StatusPinned)
		if oldActive != newActive {
			var affectedIssues, affectedWisps []string
			var aerr error
			if isWisp {
				affectedIssues, affectedWisps, aerr = AffectedByStatusChangeForWispInTx(ctx, tx, id)
			} else {
				affectedIssues, affectedWisps, aerr = AffectedByStatusChangeInTx(ctx, tx, id)
			}
			if aerr != nil {
				return nil, fmt.Errorf("affected by status change for %s: %w", id, aerr)
			}
			if err := RecomputeIsBlockedInTx(ctx, tx, affectedIssues, affectedWisps); err != nil {
				return nil, fmt.Errorf("recompute is_blocked after status change for %s: %w", id, err)
			}
		}
	}

	return &UpdateResult{OldIssue: oldIssue, IsWisp: isWisp}, nil
}

// Merge-operation update keys. Unlike plain column updates, these are resolved
// against the current row INSIDE the mutation transaction: re-read, merge,
// write, commit. Callers pass the operation (keys to set/unset, text to
// append) instead of a pre-merged value, so concurrent writers touching
// different keys of the same issue cannot erase each other.
const (
	// OpMergeMetadata merges a JSON object's top-level keys into the issue's
	// metadata (bd update --metadata). Value: string, []byte, or json.RawMessage.
	OpMergeMetadata = "_merge_metadata"
	// OpSetMetadata sets individual key=value metadata entries
	// (bd update --set-metadata). Value: []string.
	OpSetMetadata = "_set_metadata"
	// OpUnsetMetadata removes metadata keys (bd update --unset-metadata).
	// Value: []string.
	OpUnsetMetadata = "_unset_metadata"
	// OpAppendNotes appends a line to the issue's notes
	// (bd update --append-notes). Value: string.
	OpAppendNotes = "append_notes"
)

// HasMergeOps reports whether the update map carries any read-merge-write
// operation key. Updates with merge ops must resolve those ops against the row
// read inside the same mutation transaction (see ResolveMergeOps); on Dolt the
// store's whole-attempt retry then re-runs that resolution against the winning
// writer's committed row.
func HasMergeOps(updates map[string]interface{}) bool {
	for _, op := range []string{OpMergeMetadata, OpSetMetadata, OpUnsetMetadata, OpAppendNotes} {
		if _, ok := updates[op]; ok {
			return true
		}
	}
	return false
}

// ResolveMergeOps rewrites merge-operation keys into concrete column values
// using oldIssue, which the caller read in the same mutation transaction.
// Returns the input map unchanged when no operation keys are present;
// otherwise returns a copy so the caller's map is not mutated.
func ResolveMergeOps(oldIssue *types.Issue, updates map[string]interface{}) (map[string]interface{}, error) {
	if !HasMergeOps(updates) {
		return updates, nil
	}

	resolved := make(map[string]interface{}, len(updates))
	for k, v := range updates {
		if !isMergeOpKey(k) {
			resolved[k] = v
		}
	}

	if err := resolveMetadataMergeOps(oldIssue, updates, resolved); err != nil {
		return nil, err
	}
	if err := resolveNotesAppendOp(oldIssue, updates, resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

// isMergeOpKey reports whether k is a read-merge-write operation key consumed by
// ResolveMergeOps rather than a concrete column value to pass through unchanged.
func isMergeOpKey(k string) bool {
	switch k {
	case OpMergeMetadata, OpSetMetadata, OpUnsetMetadata, OpAppendNotes:
		return true
	default:
		return false
	}
}

// resolveMetadataMergeOps folds OpMergeMetadata/OpSetMetadata/OpUnsetMetadata
// into a concrete "metadata" value on resolved, using oldIssue.Metadata (read in
// the same mutation transaction) as the base. It is a no-op when no metadata
// operation keys are present.
func resolveMetadataMergeOps(oldIssue *types.Issue, updates, resolved map[string]interface{}) error {
	_, hasMerge := updates[OpMergeMetadata]
	_, hasSet := updates[OpSetMetadata]
	_, hasUnset := updates[OpUnsetMetadata]
	if !hasMerge && !hasSet && !hasUnset {
		return nil
	}
	if _, direct := resolved["metadata"]; direct {
		return fmt.Errorf("cannot combine a metadata replacement with incremental metadata edits")
	}

	current := oldIssue.Metadata
	if hasMerge {
		normalized, err := storage.NormalizeMetadataValue(updates[OpMergeMetadata])
		if err != nil {
			return fmt.Errorf("invalid %s: %w", OpMergeMetadata, err)
		}
		merged, err := storage.MergeMetadataJSON(current, json.RawMessage(normalized))
		if err != nil {
			return fmt.Errorf("metadata merge failed: %w", err)
		}
		current = merged
	}
	if hasSet || hasUnset {
		set, err := mergeOpStrings(OpSetMetadata, updates[OpSetMetadata], hasSet)
		if err != nil {
			return err
		}
		unset, err := mergeOpStrings(OpUnsetMetadata, updates[OpUnsetMetadata], hasUnset)
		if err != nil {
			return err
		}
		merged, err := storage.ApplyMetadataEdits(current, set, unset)
		if err != nil {
			return fmt.Errorf("metadata edit failed: %w", err)
		}
		current = merged
	}
	// Validate the merged result, matching the schema check stores apply to
	// direct metadata replacements (GH#1416 Phase 2).
	if err := ValidateMetadataIfConfigured(current); err != nil {
		return err
	}
	resolved["metadata"] = current
	return nil
}

// resolveNotesAppendOp folds OpAppendNotes into a concrete "notes" value on
// resolved, appending to oldIssue.Notes (read in the same mutation transaction).
// It is a no-op when the append op is absent.
func resolveNotesAppendOp(oldIssue *types.Issue, updates, resolved map[string]interface{}) error {
	raw, ok := updates[OpAppendNotes]
	if !ok {
		return nil
	}
	if _, direct := resolved["notes"]; direct {
		return fmt.Errorf("cannot combine a notes replacement with %s", OpAppendNotes)
	}
	text, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%s must be a string, got %T", OpAppendNotes, raw)
	}
	combined := oldIssue.Notes
	if combined != "" {
		combined += "\n"
	}
	combined += text
	resolved["notes"] = combined
	return nil
}

// mergeOpStrings coerces a merge-operation value to []string. Accepts
// []interface{} of strings as well, so operation maps survive a JSON
// round-trip (e.g. daemon transports).
func mergeOpStrings(op string, value interface{}, present bool) ([]string, error) {
	if !present {
		return nil, nil
	}
	switch v := value.(type) {
	case []string:
		return v, nil
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a list of strings, got element %T", op, item)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be a list of strings, got %T", op, value)
	}
}

// readIssueAndResolveMergeOps reads the pre-update row in-transaction and folds
// any merge-operation keys (metadata edits, note appends) into concrete column
// values against that row, returning the row and the rewritten update map. It
// keeps the read-merge-write plumbing off updateIssueInTx's already-large body.
func readIssueAndResolveMergeOps(ctx context.Context, tx DBTX, id string, updates map[string]interface{}) (*types.Issue, map[string]interface{}, error) {
	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get issue for update: %w", err)
	}
	resolved, err := ResolveMergeOps(oldIssue, updates)
	if err != nil {
		return nil, nil, err
	}
	return oldIssue, resolved, nil
}

// RecordFullEventInTable records an event with both old and new values.
//
//nolint:gosec // G201: table is from WispTableRouting ("events" or "wisp_events")
func RecordFullEventInTable(ctx context.Context, tx DBTX, table, issueID string, eventType types.EventType, actor, oldValue, newValue string) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, issue_id, event_type, actor, old_value, new_value)
		VALUES (?, ?, ?, ?, ?, ?)
	`, table), NewEventID(), issueID, eventType, actor, oldValue, newValue)
	if err != nil {
		return fmt.Errorf("record event in %s: %w", table, err)
	}
	return nil
}
