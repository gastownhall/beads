package issueops

import "context"

// ClaimNextRequest describes one atomic take of ready work.
type ClaimNextRequest struct {
	// Actor is the claimant and must not be empty.
	Actor string
	// Filter selects the candidate set in Reader.Ready's EXACT vocabulary —
	// the same type, not a parallel one shaped like it. A claim that answered
	// a different question than `bd ready` shows would hand an agent work the
	// listing never offered it, and two predicates that are allowed to differ
	// eventually do.
	//
	// Limit and Offset must be unset (ErrValidation). A claim delivers exactly
	// the one row it wins no matter how large the pool it scanned, and the
	// scan itself must stay unbounded: the implementation walks the ready
	// order and continues past rows a racing agent already took, so a bounded
	// window would report "nothing to claim" whenever that window happened to
	// be unclaimable while plenty of other ready work remained. Rejecting the
	// two fields says that out loud rather than accepting them and quietly
	// dropping them.
	Filter ReadyRequest
}

// ClaimNextResult reports the claim.
type ClaimNextResult struct {
	// Claimed is the claimed row with its relationship cardinalities, hydrated
	// inside the transaction that committed the claim, so the counts describe
	// the state the claim actually produced rather than a later one.
	//
	// It is nil when nothing was eligible. That is a NORMAL outcome, not an
	// error: an empty ready front is the steady state of a drained queue, and
	// a polling agent that had to classify an error to discover it would be
	// pattern-matching prose. Nothing is written and no history entry is
	// recorded for it.
	Claimed *IssueWithCounts
}

// ReadyClaimer describes the atomic take of ready work: the operation `bd
// ready --claim` performs, and — like Lifecycle and Reader — a role with its
// own accessor. A new capability gets a new role interface and its own
// accessor; never append a method here.
//
// It is a role of its own rather than a fifth Lifecycle verb because it is not
// a mutation of an issue the caller names. The caller names a QUESTION and the
// implementation picks the answer, which makes selection part of the
// operation's contract: the ready predicate, the compare-and-set that wins the
// row, and the hydration of the row that was won all belong to the same
// transaction, and none of them is expressible as a patch.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. Deterministic request-validation failures match
// ErrValidation. Result values are unspecified when error is non-nil; a
// validation failure leaves persistent state unchanged.
type ReadyClaimer interface {
	// ClaimNext claims the first ready issue matching the request's filter and
	// returns it hydrated. Selection, the compare-and-set and the hydration
	// share ONE transaction, so the row cannot move between being chosen and
	// being reported.
	//
	// The ready set is whatever the request's filter admits — the same set
	// Reader.Ready lists for that filter, which is the whole point of the two
	// surfaces sharing a request type. Ephemeral rows are outside it by
	// default, and a filter that sets IncludeEphemeral pulls them in for the
	// claim exactly as it does for the listing: such a row IS claimable, and
	// claiming it moves the ephemeral row itself rather than promoting it to
	// a durable one or refusing it.
	//
	// At most one durable history entry is recorded, and none at all when
	// nothing was eligible. A claim that wins an EPHEMERAL row records none
	// either: ephemeral rows are not versioned, so the row moves to
	// in-progress under the actor while the durable history stands still. A
	// caller reconstructing who took what from history alone will not see
	// those claims — read the row.
	//
	// A DURABLE win grants EXACTLY ONE LEASE on the row it won. The lease is
	// what makes the claim recoverable — it is the handle heartbeats extend,
	// and the row lease-expiry recovery walks once nothing extends it — so a
	// win that wrote no lease would hand out work nothing could ever take
	// back.
	//
	// An ephemeral win carries NO LEASE, which is the sharper of the two
	// consequences because it has no expiry to wait out. Heartbeats refuse an
	// ephemeral row and lease-expiry recovery only walks leased durable ones,
	// so nothing reclaims the row if its claimant dies: it stays in-progress
	// under a gone actor until something releases or finishes it. A caller
	// that hands ephemeral work to agents it does not supervise owns that
	// recovery itself.
	ClaimNext(ctx context.Context, req ClaimNextRequest) (ClaimNextResult, error)
}
