package issueops

import "context"

// ClaimRequest describes one atomic compare-and-set claim.
type ClaimRequest struct {
	// Actor is the claimant. It becomes the issue's assignee and is recorded in
	// the operation's audit trail.
	Actor string
	// IssueID names the issue to claim.
	IssueID string
}

// ClaimResult reports the post-claim issue as a detached post-state snapshot.
//
// The snapshot is the issue ROW: labels, dependency records and comments are
// all omitted. That is deliberate, and it is the one place this family differs
// from UpdateResult, CloseResult and ReopenResult, which all carry labels and
// dependency records. The reason is that this role answers an already-published
// surface — the v0 claim response and `bd update --claim --json` both report
// the bare row, verified against an issue that does carry labels — so
// hydrating here would change what a shipped body returns. Enriching it is a
// decision for the next revision window, not a side effect of moving the
// operation onto a role.
type ClaimResult struct {
	// Issue is a detached post-state snapshot of the issue row, without
	// labels, dependency records, or comments.
	Issue *Issue
	// Changed reports whether the claim persisted a semantic mutation. It is
	// false exactly for the idempotent re-claim: Actor already held the issue
	// in StatusInProgress, so nothing was written and nothing was committed.
	Changed bool
}

// Claimer describes the guarded atomic claim. It is its own role beside
// Lifecycle and Reader, reached by its own accessor; a new capability gets a
// new role interface and its own accessor, so never append a method here.
//
// Implementations never mutate caller-owned request values. Result values are
// unspecified when error is non-nil.
type Claimer interface {
	// Claim validates and commits the complete request as one atomic
	// compare-and-set mutation: it sets Assignee to Actor and Status to
	// StatusInProgress only while the issue's status is built-in StatusOpen or
	// a configured active status and the issue is unassigned, assigned to
	// Actor, or assigned to a configured claim pool. StatusInProgress held by
	// the same Actor is an idempotent success with Changed false and no
	// persisted mutation.
	//
	// A foreign holder refuses with a wrapped ErrAlreadyClaimed and an
	// ineligible status with a wrapped ErrNotClaimable; when the refusing state
	// was readable in the same transaction, the refusal is a
	// *ClaimConflictError carrying it. Any actor may claim — the actor is
	// caller-asserted provenance, not authenticated identity, and eligibility
	// is decided by the issue's state alone.
	//
	// Refusals and deterministic validation failures leave persistent state
	// unchanged. Implementations own transaction retry: a claim that loses a
	// commit-time merge is retried, never surfaced. Other operational or
	// commit-finalization errors can have an indeterminate durable outcome;
	// callers must reread state before retrying.
	//
	// A wisp id is ErrNotFound: the wisp plane is not claimable through this
	// role.
	Claim(context.Context, ClaimRequest) (ClaimResult, error)
}
