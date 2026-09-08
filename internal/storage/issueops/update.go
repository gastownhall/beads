package issueops

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// IsAllowedUpdateField checks if a field name is valid for issue updates.
func IsAllowedUpdateField(key string) bool {
	allowed := map[string]bool{
		"status": true, "priority": true, "title": true, "assignee": true, "owner": true,
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

// ManageClosedAt brings the close-lifecycle columns along with a status
// transition, so a generic update that crosses into closed lands the same row
// `bd close` would and a reopen leaves nothing of the old close behind.
//
// closeIssueInTx writes closed_at, close_reason and closed_by_session on EVERY
// close, including the empty reason and session a caller that supplied neither
// produces. The update funnels write a column only when the caller names its
// key, so without these defaults a generic close inherits whatever the previous
// close left: a re-close after a generic reopen keeps the old session and
// `bd show` reports the new close as the old session's work (ga-kjkv1). Each
// default is suppressed by its own explicit key, so a caller that names the
// column still wins — the CLI's own closed_by_session pass-through included.
//
// The reopen branch clears all three, mirroring ReopenIssueInTx. Both branches
// stay keyed on the LITERAL closed status; category-aware reopen keying is
// ga-ktn9pe.4.15.
func ManageClosedAt(oldIssue *types.Issue, updates map[string]interface{}, setClauses []string, args []interface{}) ([]string, []interface{}) {
	statusVal, hasStatus := updates["status"]
	if !hasStatus {
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

	// Defaults are SET-clause appends, never inserts into updates: the map
	// drives no-op detection, the event payload, lease management and the
	// blocked recompute, none of which should see a column the caller did not
	// ask for.
	appendDefault := func(column string, value interface{}) {
		if _, explicit := updates[column]; explicit {
			return
		}
		setClauses = append(setClauses, column+" = ?")
		args = append(args, value)
	}

	if newStatus == string(types.StatusClosed) {
		appendDefault("closed_at", time.Now().UTC())
		appendDefault("close_reason", "")
		appendDefault("closed_by_session", "")
	} else if oldIssue.Status == types.StatusClosed {
		appendDefault("closed_at", nil)
		appendDefault("close_reason", "")
		appendDefault("closed_by_session", "")
	}

	return setClauses, args
}

// ValidateClosedAtCoherence refuses an explicit closed_at write that would
// leave the row violating the closed-iff-closed_at invariant
// types.Issue.Validate enforces: a closed issue has a closed_at, a non-closed
// issue has none. It reads the status the update actually lands — the update's
// own status when it carries one, otherwise the row's current status, which
// both funnels read inside the mutation transaction.
//
// The key stays allowlisted, so every coherent standalone write still works:
// stamping closed_at on a row that is already closed is the repair path for
// rows a pre-invariant close left blank, an external-sync or backfill caller
// can land status and closed_at together, and clearing closed_at on a row that
// is or becomes non-closed is the reverse repair. What is refused is the half
// that mints a row no reader can trust — stamping closed_at on a row that stays
// open, or clearing it from a row that stays closed.
//
// This is a coherence invariant rather than close policy, so the force override
// that waives the open-children and blocker refusals does not waive it, exactly
// as force never waives the version CAS.
//
// Both funnels must call this BEFORE DiscardNoopIssueUpdates. The guard judges
// the caller's intent, and a closed_at equal to the stored value is still a
// request to keep the column; the no-op filter drops it as an unchanged value,
// which would hide exactly the incoherent halves this refuses and let
// ManageClosedAt's reopen branch clear the column instead.
func ValidateClosedAtCoherence(oldIssue *types.Issue, updates map[string]interface{}) error {
	rawClosedAt, hasClosedAt := updates["closed_at"]
	if !hasClosedAt {
		return nil
	}

	landedStatus := oldIssue.Status
	if rawStatus, hasStatus := updates["status"]; hasStatus {
		switch value := rawStatus.(type) {
		case string:
			landedStatus = types.Status(value)
		case types.Status:
			landedStatus = value
		default:
			// A status Go type nobody can read is already CrossesIntoDoneCategoryInTx's
			// refusal in both funnels; leave that the single message for it.
			return nil
		}
	}

	clearing := clearsClosedAt(rawClosedAt)
	switch {
	case landedStatus == types.StatusClosed && clearing:
		return fmt.Errorf("%w: refusing to clear closed_at on %s: its status stays %q, and a closed issue must keep a closed_at; reopen it with a status update instead",
			storage.ErrValidation, oldIssue.ID, landedStatus)
	case landedStatus != types.StatusClosed && !clearing:
		return fmt.Errorf("%w: refusing to set closed_at on %s: its status stays %q, and only a closed issue may carry a closed_at; set status=closed in the same update to close it",
			storage.ErrValidation, oldIssue.ID, landedStatus)
	}
	return nil
}

// clearsClosedAt reports whether an allowlisted closed_at value blanks the
// column. It accepts the same nil shapes matchesTimePointer treats as empty, so
// the guard and the no-op filter agree on what "no closed_at" means.
func clearsClosedAt(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case *time.Time:
		return typed == nil
	default:
		return false
	}
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

	// newAssignee == oldIssue.Assignee is deliberately verbatim, not
	// actorMatches (ga-v2k49): a generic update that hand-doles the same
	// identity back under a different layer's spelling genuinely rewrites the
	// assignee column's bytes, so clearing the lease here (as any other
	// transfer would) is defensible rather than a false positive — it costs
	// the holder's live lease, but HeartbeatIssueInTx's fallback re-arms one
	// under the caller's current spelling on the next beat, so it self-heals.
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

// CrossesIntoDoneCategoryInTx reports whether updates move oldStatus from
// outside the done category into it. The categories are resolved from the same
// transaction that will perform the write, so a custom status configured as
// done counts exactly as the built-in closed status does.
//
// It is false without a status update, and for a move that starts in the done
// category — a done-to-done restatement is not a crossing, and reopening is the
// operation that leaves.
//
// A status value whose Go type cannot carry a status is a validation error, not
// a false. Both write funnels ask this question to decide whether close policy
// applies, so answering "no crossing" for a value nobody can read would let an
// in-process caller that got the transport wrong land status='closed' with the
// policy gate skipped — on an issue with open children, no less. Refusing here
// gives a mis-typed status the same fail-loud handling the mis-typed override
// key already gets (see PopForceClosePolicy).
func CrossesIntoDoneCategoryInTx(ctx context.Context, tx DBTX, oldStatus types.Status, updates map[string]interface{}) (bool, error) {
	rawStatus, hasStatus := updates["status"]
	if !hasStatus {
		return false, nil
	}
	var newStatus types.Status
	switch value := rawStatus.(type) {
	case string:
		newStatus = types.Status(value)
	case types.Status:
		newStatus = value
	default:
		return false, fmt.Errorf("%w: status value of type %T is neither a string nor a types.Status", storage.ErrValidation, rawStatus)
	}

	newCategory, err := ReopenCategoryInTx(ctx, tx, newStatus)
	if err != nil {
		return false, err
	}
	if newCategory != types.CategoryDone {
		return false, nil
	}
	oldCategory, err := ReopenCategoryInTx(ctx, tx, oldStatus)
	if err != nil {
		return false, err
	}
	return oldCategory != types.CategoryDone, nil
}

// UpdateResult holds the result of an UpdateIssueInTx call.
type UpdateResult struct {
	OldIssue         *types.Issue
	IsWisp           bool
	Changed          bool
	IssueRowsChanged bool
	WispRowsChanged  bool
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
	updates = cloneUpdateFields(updates)
	// Pop the override before anything reads the map as a set of columns. It
	// has to come out ahead of the no-op filter too: the filter keeps every key
	// it does not recognize, so a surviving override would reach the field
	// allowlist and be refused by name.
	forceClosePolicy := PopForceClosePolicy(updates)

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

	// An explicit closed_at must agree with the status the update lands. The
	// guard reads the caller's INTENT, so it runs on the merge-resolved map
	// BEFORE the no-op filter: a closed_at that happens to equal the stored
	// value is still a request to keep the column, and letting the filter drop
	// it first would send that request down ManageClosedAt's reopen branch —
	// clearing the column instead of refusing the write, and answering two
	// identical intents differently over a nanosecond of stamp. Merge-op
	// resolution only ever writes metadata and notes, so the key the guard sees
	// is always one the caller supplied. It also runs ahead of the close-policy
	// gate so a refused write never even touches the dependency coordination
	// cell, and it reads the row the same transaction just read, so no
	// concurrent status change can slip between.
	if err := ValidateClosedAtCoherence(oldIssue, updates); err != nil {
		return nil, err
	}

	updates, err = DiscardNoopIssueUpdates(oldIssue, updates)
	if err != nil {
		return nil, err
	}
	if len(updates) == 0 {
		return &UpdateResult{OldIssue: oldIssue, IsWisp: isWisp, Changed: false}, nil
	}

	// A status update that crosses into the done category is a close by another
	// name, so it answers to close policy. Running after the no-op filter keeps
	// a done-to-done restatement policy-free, and running after the callers'
	// version and assignee preconditions keeps force away from those: it
	// bypasses the two close refusals and nothing else, exactly as it does for
	// `bd close`. A refusal returns here, before any write, and aborts the
	// caller's transaction.
	crossing, err := CrossesIntoDoneCategoryInTx(ctx, tx, oldIssue.Status, updates)
	if err != nil {
		return nil, err
	}
	if crossing {
		if _, err := EnforceClosePolicyInTx(ctx, tx, id, forceClosePolicy); err != nil {
			return nil, err
		}
	}

	if err := ValidateScalarUpdates(ctx, tx, updates); err != nil {
		return nil, err
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

	updateResult := &UpdateResult{OldIssue: oldIssue, IsWisp: isWisp, Changed: true, IssueRowsChanged: !isWisp, WispRowsChanged: isWisp}
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
			recompute, err := RecomputeIsBlockedInTxWithResult(ctx, tx, affectedIssues, affectedWisps)
			if err != nil {
				return nil, fmt.Errorf("recompute is_blocked after status change for %s: %w", id, err)
			}
			updateResult.IssueRowsChanged = !isWisp || recompute.IssueRowsChanged
			updateResult.WispRowsChanged = isWisp || recompute.WispRowsChanged
		}
	}

	// Snapshot only after all derived blocked-state maintenance has completed,
	// so the journal row carries the settled bead. recordEvent controls the
	// human-facing audit event only: the journal is a machine replay feed and
	// must never have a hole punched in it by an audit-suppressing caller.
	if err := RecordEventInTx(ctx, tx, EventUpdate, id, actor); err != nil {
		return nil, err
	}
	return updateResult, nil
}

func cloneUpdateFields(updates map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(updates))
	for key, value := range updates {
		cloned[key] = value
	}
	return cloned
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
	// OpSetMetadata sets individual metadata entries. CLI callers use []string
	// key=value values; public issue operations use map[string]json.RawMessage.
	OpSetMetadata = "_set_metadata"
	// OpUnsetMetadata removes metadata keys (bd update --unset-metadata).
	// Value: []string.
	OpUnsetMetadata = "_unset_metadata"
	// OpAppendNotes appends a line to the issue's notes
	// (bd update --append-notes). Value: string.
	OpAppendNotes = "append_notes"
)

// OpForceClosePolicy carries a close-policy override into a generic update
// (bd update --force). Value: bool. It is not a merge operation and not a
// column — the write funnels pop it before validating fields, so it never
// reaches SQL.
//
// It rides the update map, like the merge operations, so adding the override
// breaks no interface. That choice has a deliberate failure mode: the field
// allowlists do not name this key, so an occurrence that reaches field
// validation unpopped is refused by name. A caller that misspells the override,
// or a write path that forgets to pop it, fails loudly instead of quietly
// running the update with force read as off.
const OpForceClosePolicy = "_force_close_policy"

// PopForceClosePolicy removes the close-policy override from updates and
// reports whether it asked for force. A value that is not a bool is left in
// place on purpose: it cannot be read as an intent, and leaving it lets the
// field allowlist refuse it by name.
func PopForceClosePolicy(updates map[string]interface{}) bool {
	raw, present := updates[OpForceClosePolicy]
	if !present {
		return false
	}
	force, isBool := raw.(bool)
	if !isBool {
		return false
	}
	delete(updates, OpForceClosePolicy)
	return force
}

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

// DiscardNoopIssueUpdates removes concrete updates whose value already matches
// the row read by the caller. This keeps idempotent updates from advancing the
// row version or recording an event.
func DiscardNoopIssueUpdates(oldIssue *types.Issue, updates map[string]interface{}) (map[string]interface{}, error) {
	filtered := make(map[string]interface{}, len(updates))
	for key, value := range updates {
		unchanged, err := issueFieldMatches(oldIssue, key, value)
		if err != nil {
			return nil, err
		}
		if !unchanged {
			filtered[key] = value
		}
	}
	return filtered, nil
}

func issueFieldMatches(issue *types.Issue, key string, value interface{}) (bool, error) {
	switch key {
	case "title":
		return matchesString(issue.Title, value), nil
	case "description":
		return matchesString(issue.Description, value), nil
	case "design":
		return matchesString(issue.Design, value), nil
	case "acceptance_criteria":
		return matchesString(issue.AcceptanceCriteria, value), nil
	case "notes":
		return matchesString(issue.Notes, value), nil
	case "spec_id":
		return matchesString(issue.SpecID, value), nil
	case "await_id":
		return matchesString(issue.AwaitID, value), nil
	case "status":
		return matchesStatus(issue.Status, value), nil
	case "priority":
		return matchesInt(issue.Priority, value), nil
	case "issue_type":
		return matchesIssueType(issue.IssueType, value), nil
	case "assignee":
		return matchesString(issue.Assignee, value), nil
	case "owner":
		return matchesString(issue.Owner, value), nil
	case "estimated_minutes":
		return matchesIntPointer(issue.EstimatedMinutes, value), nil
	case "external_ref":
		return matchesStringPointer(issue.ExternalRef, value), nil
	case "started_at":
		return matchesTimePointer(issue.StartedAt, value), nil
	case "closed_at":
		return matchesTimePointer(issue.ClosedAt, value), nil
	case "due_at":
		return matchesTimePointer(issue.DueAt, value), nil
	case "defer_until":
		return matchesTimePointer(issue.DeferUntil, value), nil
	case "close_reason":
		return matchesString(issue.CloseReason, value), nil
	case "closed_by_session":
		return matchesString(issue.ClosedBySession, value), nil
	case "source_repo":
		return matchesString(issue.SourceRepo, value), nil
	case "sender":
		return matchesString(issue.Sender, value), nil
	case "wisp":
		return matchesBool(issue.Ephemeral, value), nil
	case "wisp_type":
		return matchesWispType(issue.WispType, value), nil
	case "no_history":
		return matchesBool(issue.NoHistory, value), nil
	case "pinned":
		return matchesBool(issue.Pinned, value), nil
	case "mol_type":
		return matchesMolType(issue.MolType, value), nil
	case "event_category", "event_kind":
		return matchesString(issue.EventKind, value), nil
	case "event_actor", "actor":
		return matchesString(issue.Actor, value), nil
	case "event_target", "target":
		return matchesString(issue.Target, value), nil
	case "event_payload", "payload":
		return matchesString(issue.Payload, value), nil
	case "waiters":
		waiters, ok := value.([]string)
		return ok && slices.Equal(issue.Waiters, waiters), nil
	case "metadata":
		current, err := normalizedMetadata(issue.Metadata)
		if err != nil {
			return false, err
		}
		candidate, err := storage.NormalizeMetadataValue(value)
		if err != nil {
			return false, err
		}
		return current == candidate, nil
	default:
		return false, nil
	}
}

func matchesString(current string, candidate interface{}) bool {
	switch value := candidate.(type) {
	case nil:
		return current == ""
	case string:
		return current == value
	default:
		return false
	}
}

func matchesInt(current int, candidate interface{}) bool {
	value, ok := candidate.(int)
	return ok && current == value
}

func matchesBool(current bool, candidate interface{}) bool {
	value, ok := candidate.(bool)
	return ok && current == value
}

func matchesStatus(current types.Status, candidate interface{}) bool {
	switch value := candidate.(type) {
	case string:
		return current == types.Status(value)
	case types.Status:
		return current == value
	default:
		return false
	}
}

func matchesIssueType(current types.IssueType, candidate interface{}) bool {
	switch value := candidate.(type) {
	case string:
		return current == types.IssueType(value)
	case types.IssueType:
		return current == value
	default:
		return false
	}
}

func matchesWispType(current types.WispType, candidate interface{}) bool {
	switch value := candidate.(type) {
	case string:
		return current == types.WispType(value)
	case types.WispType:
		return current == value
	default:
		return false
	}
}

func matchesMolType(current types.MolType, candidate interface{}) bool {
	switch value := candidate.(type) {
	case string:
		return current == types.MolType(value)
	case types.MolType:
		return current == value
	default:
		return false
	}
}

func matchesIntPointer(current *int, candidate interface{}) bool {
	switch value := candidate.(type) {
	case nil:
		return current == nil
	case *int:
		return (current == nil && value == nil) || (current != nil && value != nil && *current == *value)
	case int:
		return current != nil && *current == value
	default:
		return false
	}
}

func matchesStringPointer(current *string, candidate interface{}) bool {
	switch value := candidate.(type) {
	case nil:
		return current == nil
	case *string:
		return (current == nil && value == nil) || (current != nil && value != nil && *current == *value)
	case string:
		return current != nil && *current == value
	default:
		return false
	}
}

func matchesTimePointer(current *time.Time, candidate interface{}) bool {
	switch value := candidate.(type) {
	case nil:
		return current == nil
	case *time.Time:
		return (current == nil && value == nil) || (current != nil && value != nil && current.Equal(*value))
	case time.Time:
		return current != nil && current.Equal(value)
	default:
		return false
	}
}

func normalizedMetadata(value json.RawMessage) (string, error) {
	if len(value) == 0 {
		return "{}", nil
	}
	return storage.NormalizeMetadataValue(value)
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
		unset, err := mergeOpStrings(OpUnsetMetadata, updates[OpUnsetMetadata], hasUnset)
		if err != nil {
			return err
		}
		var merged json.RawMessage
		if set, typed := updates[OpSetMetadata].(map[string]json.RawMessage); typed {
			merged, err = applyTypedMetadataEdits(current, set, unset)
		} else {
			var set []string
			set, err = mergeOpStrings(OpSetMetadata, updates[OpSetMetadata], hasSet)
			if err != nil {
				return err
			}
			merged, err = storage.ApplyMetadataEdits(current, set, unset)
		}
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

func applyTypedMetadataEdits(existing json.RawMessage, set map[string]json.RawMessage, unset []string) (json.RawMessage, error) {
	data := make(map[string]json.RawMessage)
	if len(existing) > 0 {
		trimmed := strings.TrimSpace(string(existing))
		if trimmed != "" && trimmed != "null" {
			if err := json.Unmarshal(existing, &data); err != nil {
				return nil, fmt.Errorf("existing metadata is not a JSON object: %w", err)
			}
		}
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := storage.ValidateMetadataKey(key); err != nil {
			return nil, err
		}
		if !json.Valid(set[key]) {
			return nil, fmt.Errorf("metadata value for key %q is not valid JSON", key)
		}
		data[key] = set[key]
	}
	for _, key := range unset {
		if err := storage.ValidateMetadataKey(key); err != nil {
			return nil, err
		}
		delete(data, key)
	}
	result, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}
	return json.RawMessage(result), nil
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
		return fmt.Errorf("%w: cannot combine a notes replacement with %s", storage.ErrValidation, OpAppendNotes)
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
func RecordFullEventInTable(ctx context.Context, tx DBTX, table, issueID string, eventType types.EventType, actor, oldValue, newValue string) error {
	return InsertDerivedEvent(ctx, tx, table, AuxEvent{
		IssueID:   issueID,
		EventType: eventType,
		Actor:     actor,
		OldValue:  str(oldValue),
		NewValue:  str(newValue),
	})
}
