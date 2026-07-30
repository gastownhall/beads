package issueops

import (
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// fenceBumpExpr is the single SET fragment that advances a row's ownership
// fence. claim_fence is a monotonic counter bumped ONLY on ownership
// transitions. The canonical list — every other enumeration of it (migration
// 0062's header, types.Issue.ClaimFence, the CHANGELOG entry) must match this
// one:
//
//   - claim (the CAS in claim.go and its proxied dual in domain/db)
//   - unclaim/release, including --force and the --if-assignee CAS form
//   - lease reclaim
//   - assignee change, through the generic update path or an import/upsert
//   - reopen (closed→open) through the reopen verb or the generic update path
//
// Content mutations never move it, and neither does close. A caller holding a
// stale read of the ownership state can therefore be told so, instead of
// silently stomping newer ownership. See migration 0062 (issues) and
// ignored/0019 (wisps).
//
// The claim CAS bumps UNCONDITIONALLY, and that is deliberate: a successful
// claim of an open row already assigned to the same actor still bumps. Two
// sessions of one user are two ownership holders, and the arriving session
// must be able to fence out whatever the earlier one snapshotted; keying the
// bump on "did the assignee string change" would leave that case silently
// shared. The idempotent same-actor re-claim of an already in_progress row is
// a different path — it never matches the CAS (in_progress is not a claimable
// source status), returns success without writing, and so does not bump.
//
// IMPORT-REOPEN EXCEPTION. UpsertFenceAssignments keys the import/upsert bump
// on an assignee change ALONE, so an import that flips a stored closed row
// back to open while keeping the same assignee does NOT bump — where the
// reopen verb (and the generic update path) does. That asymmetry is
// deliberate, not an oversight: import is convergence toward a peer's
// snapshot, not an ownership verb exercised by a live holder, and the fence is
// a live-coordination token. Bumping on every imported status flip would let
// a routine sync invalidate outstanding --if-fence guards held by workers on
// this replica, for a "transition" no one on this replica performed. The
// upsert path's one bump condition stays the one thing an import can say
// about ownership: the owner is now someone else.
//
// INVARIANT: every SQL statement that bumps claim_fence MUST also rewrite
// row_lock in the same statement. A monotonic cell is exactly the write
// pattern Dolt cell-merges silently (two concurrent N→N+1 bumps write
// identical values, no conflict); the random row_lock rewrite is what forces
// racing ownership transitions to serialize. The fragment itself carries no
// row_lock — statements that compose it own the pairing (claim pairs via
// RowLockClause, updateIssueInTx via its unconditional row_lock append, the
// upsert paths via their existing row_lock assignment). Literal-form bumps
// are checked by TestFenceBumpAlwaysPairsRowLock; fragment-composed
// statements are covered by the behavioral fence tests in
// internal/storage/dolt.
const fenceBumpExpr = "claim_fence = claim_fence + 1"

// FenceBumpExpr exposes the bump fragment to sibling storage layers that
// hand-roll their ownership SQL (the proxied-server domain/db repository),
// so the fence discipline cannot drift between dispatch layers.
const FenceBumpExpr = fenceBumpExpr

// upsertAssigneeChanged renders the ON DUPLICATE KEY UPDATE predicate for
// "this upsert changes the row's owner", with the stored side qualified by
// the target table (main's convention for existing-row references in an
// upsert). Both sides are COALESCEd to "" because the system has two
// representations of unassigned — unclaim writes "", while reclaim and
// NullString-bound inserts write NULL — and the claim WHERE clauses already
// treat them as equivalent. Without the normalization, a content-only
// re-import of a previously-unclaimed row ("" stored, NULL incoming) would
// spuriously bump the fence and conflict legitimate outstanding guards.
func upsertAssigneeChanged(table string) string {
	return fmt.Sprintf("COALESCE(%s.assignee, '') <> COALESCE(VALUES(assignee), '')", table)
}

// UpsertFenceAssignments returns the leading ON DUPLICATE KEY UPDATE
// assignment that applies the fence discipline to import/upsert paths: an
// upsert that changes the stored assignee is an ownership transition, so it
// bumps claim_fence. Callers MUST place the fragment FIRST in the assignment
// list — ON DUPLICATE KEY UPDATE evaluates assignments in order and column
// references see pre-assignment values only until the column is reassigned,
// so the assignee/updated_at comparisons must run before those columns are
// rewritten.
//
// Assignee change is the ONLY condition here — see the import-reopen
// exception on fenceBumpExpr for why an imported closed→open flip that keeps
// the owner deliberately does not bump.
//
// The bump⇒row_lock pairing is satisfied by the caller's own row_lock
// assignment, which both upsert sites already carry (row_lock = VALUES(...),
// bound to a fresh freshRowLock() on the INSERT side).
//
// With rejectStaleUpdate, the bump mirrors the stale guard on the column
// assignments: the assignee only actually changes when the incoming row is
// strictly newer, so the fence only moves then too — and row_lock, guarded by
// the same comparison, is rewritten in exactly those cases.
func UpsertFenceAssignments(table string, rejectStaleUpdate bool) string {
	if rejectStaleUpdate {
		return fmt.Sprintf("claim_fence = %s.claim_fence + IF(VALUES(updated_at) > %s.updated_at AND %s, 1, 0)",
			table, table, upsertAssigneeChanged(table))
	}
	return fmt.Sprintf("claim_fence = %s.claim_fence + IF(%s, 1, 0)", table, upsertAssigneeChanged(table))
}

// IsOwnershipTransition reports whether a generic update changes the row's
// ownership context: an assignee change, or a reopen (any transition out of
// closed — after which a fresh claim starts a new ownership generation).
// Shared by updateIssueInTx and the proxied domain/db Update so the
// transition predicate cannot drift between dispatch layers.
//
// Deliberate exclusions: close is not a transition (guarded verbs reject
// closed rows through their status predicates, and bumping on close would
// invalidate legitimate orchestrator ownership snapshots for no protective
// gain); an in_progress→open status change that keeps the assignee is not a
// transition either — the row remains claimable only by the same assignee,
// and the eventual re-claim or release bumps the fence at the actual
// ownership boundary.
func IsOwnershipTransition(oldStatus types.Status, oldAssignee string, updates map[string]interface{}) bool {
	if raw, ok := updates["assignee"]; ok {
		switch v := raw.(type) {
		case string:
			if v != oldAssignee {
				return true
			}
		case nil:
			// A nil assignee writes SQL NULL — the unassigned state
			// (ManageLeaseOnUpdate maps it to "" the same way).
			if oldAssignee != "" {
				return true
			}
		default:
			// Unknown value shape: treat conservatively as a transition so
			// guards fail safe (bump when unsure).
			return true
		}
	}
	if raw, ok := updates["status"]; ok && oldStatus == types.StatusClosed {
		var statusStr string
		switch v := raw.(type) {
		case string:
			statusStr = v
		case types.Status:
			statusStr = string(v)
		}
		if statusStr != "" && types.Status(statusStr) != types.StatusClosed {
			return true
		}
	}
	return false
}
