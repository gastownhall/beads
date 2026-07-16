package issueops

import (
	"github.com/steveyegge/beads/internal/types"
)

// fenceBumpExpr is the single SET fragment that advances a row's ownership
// fence. claim_fence is a monotonic counter bumped ONLY on ownership
// transitions — claim, unclaim/release, lease reclaim, assignee change,
// reopen (closed→open), transfer — never by content mutations (notes,
// metadata, close). Guarded verbs compare it with IfFence so an actor holding
// a stale read of the ownership state gets a typed conflict instead of
// silently stomping newer ownership; see migration 0055 and
// engdocs/plans/ownership-fencing/DESIGN.md (gascity).
//
// INVARIANT: every SQL statement that bumps claim_fence MUST also rewrite
// row_lock in the same statement. A monotonic cell is exactly the write
// pattern Dolt cell-merges silently (two concurrent N→N+1 bumps write
// identical values, no conflict); the random row_lock rewrite is what forces
// racing ownership transitions to serialize. The fragment itself carries no
// row_lock — statements that compose it own the pairing (claim pairs via the
// lease clause, updateIssueInTx via its unconditional row_lock append, the
// proxied Update via an explicit "row_lock = ?"). Literal-form bumps are
// checked by TestFenceBumpAlwaysPairsRowLock; fragment-composed statements
// are covered by the behavioral fence tests in internal/storage/dolt.
const fenceBumpExpr = "claim_fence = claim_fence + 1"

// FenceBumpExpr exposes the bump fragment to sibling storage layers that
// hand-roll their ownership SQL (the proxied-server domain/db repository),
// so the fence discipline cannot drift between dispatch layers.
const FenceBumpExpr = fenceBumpExpr

// FreshRowLock exposes the row_lock generator to sibling storage layers so a
// hand-rolled fence bump can satisfy the bump⇒row_lock pairing invariant.
func FreshRowLock() int64 { return freshRowLock() }

// upsertAssigneeChanged is the ON DUPLICATE KEY UPDATE predicate for "this
// upsert changes the row's owner". Both sides are COALESCEd to ” because the
// system has two representations of unassigned — unclaim writes ”, while
// reclaim and NullString-bound inserts write NULL — and the claim WHERE
// clauses already treat them as equivalent. Without the normalization, a
// content-only re-import of a previously-unclaimed row (” stored, NULL
// incoming) would spuriously bump the fence and conflict legitimate
// outstanding guards.
const upsertAssigneeChanged = "COALESCE(assignee, '') <> COALESCE(VALUES(assignee), '')"

// UpsertFenceAssignments returns the leading ON DUPLICATE KEY UPDATE
// assignments (plus their bound args) that apply the fence discipline to
// import/upsert paths: an upsert that changes the stored assignee is an
// ownership transition, so it bumps claim_fence and rewrites row_lock in the
// same statement. Callers MUST place the fragment FIRST in the assignment
// list — ON DUPLICATE KEY UPDATE evaluates assignments in order and column
// references see pre-assignment values only until the column is reassigned,
// so the assignee/updated_at comparisons must run before those columns are
// rewritten. The row_lock value is bound as a parameter so all row_lock
// entropy stays on the single freshRowLock (crypto/rand) path.
//
// With rejectStaleUpdate, the bump mirrors the stale guard on the column
// assignments: the assignee only actually changes when the incoming row is
// strictly newer, so the fence only moves then too.
func UpsertFenceAssignments(rejectStaleUpdate bool) (string, []any) {
	// An import that changes the owner also clears holder_token: a foreign
	// import cannot carry a valid incarnation token for the new owner, and a
	// leftover token would lock the new owner out under enforcement. Same
	// change-condition as the fence bump.
	if rejectStaleUpdate {
		cond := "VALUES(updated_at) > updated_at AND " + upsertAssigneeChanged
		return "claim_fence = claim_fence + IF(" + cond + ", 1, 0),\n\t\t\t" +
			"holder_token = IF(" + cond + ", '', holder_token),\n\t\t\t" +
			"row_lock = IF(" + cond + ", ?, row_lock)", []any{freshRowLock()}
	}
	return "claim_fence = claim_fence + IF(" + upsertAssigneeChanged + ", 1, 0),\n\t\t\t" +
		"holder_token = IF(" + upsertAssigneeChanged + ", '', holder_token),\n\t\t\t" +
		"row_lock = IF(" + upsertAssigneeChanged + ", ?, row_lock)", []any{freshRowLock()}
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
