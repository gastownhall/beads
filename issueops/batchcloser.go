package issueops

import "context"

// BatchCloseItem is one issue a batch close asks for, with the reason recorded
// against that issue alone. Reasons are per item rather than per request
// because `bd close a b c --reason x --reason y --reason z` has always mapped
// them positionally, and a single request-wide reason could not express it.
type BatchCloseItem struct {
	IssueID string
	Reason  string
}

// CloseBatchRequest describes one closure of many issues.
//
// THE REQUEST IS THE TRANSACTION BOUNDARY. That is the whole reason this is a
// request rather than a loop over Lifecycle.Close: closing N issues one call
// at a time is N transactions and N history entries, and the caller that wants
// them to land together has no way to say so. Nothing here exposes begin or
// commit and there is no handle to hold open, so the boundary cannot be got
// wrong — it is wherever the request ends.
type CloseBatchRequest struct {
	// Actor is the closer and must not be empty.
	Actor string
	// Items are the issues to close, in the order the caller asked for them.
	// It must not be empty. Every item appears in Outcomes at the same index.
	//
	// A DUPLICATED id is not a request error — `bd close a b a` is a plausible
	// typo, not a failure. The items are closed in the order given, so the
	// second occurrence finds what the first one did and reports an idempotent
	// re-close at its OWN index: a per-item success with Changed false. The
	// reason recorded on the row is the first occurrence's, because the second
	// mutated nothing and so wrote nothing.
	//
	// A WISP ID IS AN ADMISSIBLE ITEM. An id is resolved across both planes,
	// exactly as Lifecycle.Close resolves one, and closing a wisp is real work
	// landing: it counts toward "at least one item closed" for ClaimNext below.
	// What it does NOT do is reach the durable history entry — see the
	// ephemeral paragraph on BatchCloser itself.
	Items []BatchCloseItem
	// Session records the closing session against every item.
	Session string
	// Force bypasses only blocker and open-child close policy, for every item.
	// It never bypasses validation, and it never bypasses existence: an id that
	// names nothing refuses whether or not force is set. It is request-wide
	// because the flag that spells it is.
	//
	// It would not bypass a per-item LIFECYCLE PRECONDITION either, but no
	// batch item carries one: there is no counterpart here to the
	// ExpectedVersion a Lifecycle close takes, so the category is empty and
	// nothing in a batch can exercise it today. The rule is stated for the day
	// an item grows a precondition, which is the day force stops being a
	// question with only two answers.
	Force bool
	// ClaimNext claims the next ready issue after the closes land, in the same
	// transaction, and only when at least one item closed — a claim handed out
	// by a request that closed nothing would be a claim the caller never
	// earned. Nil skips it entirely.
	//
	// CHANGED IS THE TEST for "at least one item closed", not a nil per-item
	// Err: a batch whose items were ALL already closed mutated nothing, so
	// nothing landed. It earns no claim and records no history entry, for the
	// same reason a batch of typos records none. A per-item success that
	// changed nothing is not work the caller did.
	//
	// Its semantics are ReadyClaimer.ClaimNext's, by reference and not by
	// restatement: the same filter vocabulary, the same unset-Limit-and-Offset
	// rule, the same nil-means-nothing-eligible outcome. Restating them here
	// is how the two would come apart.
	ClaimNext *ReadyRequest
}

// CloseOutcome is what happened to ONE requested item.
type CloseOutcome struct {
	// IssueID is the id the caller asked for, echoed so an outcome can be read
	// without indexing back into the request.
	IssueID string
	// Issue is the post-close snapshot with labels and dependency records, or
	// nil when Err is set. Comments are omitted, as they are for CloseResult.
	Issue *Issue
	// Changed reports whether this item persisted a semantic mutation. It is
	// false for an idempotent re-close.
	Changed bool
	// OpenChildren is the number of open children observed by a forced close
	// of this item. It is reported even for an idempotent re-close.
	OpenChildren int
	// Err is this item's refusal, and NEVER aborts the rest of the batch. It
	// carries the same typed close vocabulary Lifecycle.Close returns —
	// ErrNotFound, ErrCloseBlocked, *CloseOpenChildrenError — so a caller
	// classifies it with errors.Is and errors.As rather than by reading prose.
	Err error
}

// CloseBatchResult reports every requested item.
type CloseBatchResult struct {
	// Outcomes has exactly one entry per requested item, in REQUEST ORDER,
	// including for items that refused. A caller reporting per-id status walks
	// it against its own argument list without matching ids back up.
	Outcomes []CloseOutcome
	// ClaimedNext is the row a requested ClaimNext won, hydrated with its
	// cardinalities in the same transaction. Nil means the request asked for
	// no claim, nothing closed, or nothing was eligible.
	ClaimedNext *IssueWithCounts
}

// BatchCloser describes closing many issues as one durable act: the write side
// of `bd close a b c`, and — like Lifecycle, Reader and ReadyClaimer — a role
// with its own accessor. A new capability gets a new role interface and its
// own accessor; never append a method here.
//
// IT IS NOT ALL-OR-NOTHING, and that is deliberate rather than a concession.
// An id the batch refuses is SKIPPED and the survivors commit together, which
// is what closing a list of issues has always done: an agent that finishes
// four of five steps and mistypes the fifth keeps the four. Making the batch
// atomic in the other sense — all or none — would turn a typo into a rollback
// of finished work, and the per-id refusal a caller prints is the report that
// tells it which one to fix.
//
// WHAT IS ATOMIC is everything that LANDS: one transaction, at most one
// history entry, and none at all when nothing landed. LANDED means CHANGED, in
// the CloseOutcome sense — an idempotent re-close is a per-item success that
// persisted nothing, so a batch of them lands nothing. The method's own error
// is reserved for request validation, cancellation and infrastructure — the
// failures that mean the batch never ran — so a non-nil error and a populated
// Outcomes are mutually exclusive. A per-item refusal is not an error of the
// batch; it is a result of the batch.
//
// A DURABLE HISTORY ENTRY NEVER NAMES A WISP ID. The entry a mixed batch
// records is composed from its DURABLE landings alone, so a batch of one issue
// and one wisp records an entry naming the issue only. The wisp tables are
// dolt-ignored precisely so ephemeral work never ships, so an entry naming a
// wisp would be the sync artifact ignoring those tables exists to prevent — the
// same rule Commenter states for an ephemeral comment. Only the durable trace
// is restricted: the close itself lands on the ephemeral row, reads back from
// it, and counts toward ClaimNext's coupling like any other landing.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation and normalization only to
// attempt-local clones. Deterministic request-validation failures match
// ErrValidation and leave persistent state unchanged.
type BatchCloser interface {
	// CloseBatch closes every item it can and commits them together. Outcomes
	// mirrors Items index for index. ClaimNext, when requested and when at
	// least one item closed, runs after the closes inside the same
	// transaction.
	CloseBatch(ctx context.Context, req CloseBatchRequest) (CloseBatchResult, error)
}
