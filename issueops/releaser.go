package issueops

import (
	"context"
	"errors"
)

// ReleaseRequest describes one release of a claim — the inverse of
// ClaimRequest, and the shape behind `bd unclaim`.
//
// Implementations never mutate caller-owned request values: ExpectedAssignee is
// read, never written through.
type ReleaseRequest struct {
	// Actor attributes the release on the row's event history. It is REQUIRED —
	// an empty Actor is ErrValidation — because a release is the moment work
	// stops being owned, and the one question asked of its history entry
	// afterwards is who let it go.
	//
	// IT IS ALSO THE OWNERSHIP FENCE'S SUBJECT, on the unconditional path: a
	// release with neither ExpectedAssignee nor Force succeeds only while Actor
	// is the current holder. That is not authentication — Actor is
	// caller-asserted provenance here as it is everywhere else in this package
	// — it is the same anti-yank guard ClaimRequest gets from refusing a
	// foreign holder, pointed the other way. "Is the current holder" is the
	// same separator-insensitive comparison ExpectedAssignee documents below,
	// not exact equality.
	Actor string
	// IssueID is the exact canonical id and must not be empty.
	//
	// IT RESOLVES ACROSS BOTH PLANES, unlike Claimer.Claim, which answers
	// ErrNotFound for a wisp id. The asymmetry is deliberate and it is about
	// which direction strands work: a wisp can hold a claim — the ephemeral
	// plane has its own claim path — so a role that refused to release one
	// would leave an ephemeral row owned by an agent that is gone, with no verb
	// able to free it. Refusing to MINT a claim on that plane through this
	// family strands nothing.
	//
	// There is no fuzzy or prefix resolution: that is a front-door convenience,
	// for the reason GetRequest.ID gives.
	IssueID string
	// ExpectedAssignee is a compare-and-set precondition on the holder: the
	// release proceeds only while the issue is still assigned to
	// *ExpectedAssignee, and otherwise refuses with ErrAssigneeMismatch naming
	// the actual holder, leaving the row untouched.
	//
	// A MATCH REPLACES THE OWNERSHIP FENCE, exactly as
	// UpdateRequest.ExpectedAssignee replaces the anti-steal fence for a
	// transfer: a caller that can name the current holder has demonstrated the
	// view the fence exists to protect, so Actor need not be that holder. This
	// is the shape a supervisor releasing a specific agent's abandoned claim
	// wants, and it is safer than Force for the purpose because it cannot
	// release a claim that has since moved.
	//
	// THE COMPARISON IS SEPARATOR-INSENSITIVE AND NOTHING ELSE. A run of ".",
	// "_" or "-" matches any other such run, so "agent-a", "agent_a" and
	// "agent.a" are one holder — that is deliberate, so a caller naming the
	// holder under a different layer's spelling is a match rather than a
	// mismatch (ga-5ksp5). THE ONE EXCEPTION IS AN EXACT "--" RUN: that is
	// gascity's session-name encoding of a rig-qualified agent's "/", so it
	// decodes to "/" rather than collapsing. "a--b" matches "a/b" and no
	// longer matches "a__b" or "a-b" — "a--b" is the agent "b" on rig "a"
	// while "a__b" is the dotted alias "a.b", so treating them as one holder
	// was a widening, and removing it is the point of the exception
	// (ga-2vy9p2). Longer or mixed runs, "__" included, still collapse.
	// NOTHING ELSE IS FORGIVEN: the value is not trimmed
	// and not case-folded, so " agent-a" and "Agent-a" are both refusals. The
	// validation below trims only far enough to tell a blank expectation from a
	// real one, and never writes the trimmed form back — the no-mutation
	// promise above makes that permanent, so a caller that pads its expectation
	// loses every time rather than intermittently. Compose it from a holder a
	// read gave you.
	//
	// nil DISABLES THE CHECK and selects the unconditional path. It is a
	// pointer so that "do not check" is distinct from a value — but unlike
	// UpdateRequest.ExpectedAssignee, a non-nil pointer to "" is NOT a guard
	// meaning "expected unassigned": it is ErrValidation. "Release a row nobody
	// holds" describes no release at all, and the raw seam beneath this role
	// refuses the empty expectation in as many words. A caller that wants to
	// assert a row is unheld is asking a READER a question, not asking this
	// role to do nothing.
	//
	// FORCE MUST BE FALSE WHEN IT IS NON-NIL — a request setting both is
	// ErrValidation. The two are answers to the same question, and they
	// disagree: this one says "only if X still holds it", Force says "whoever
	// holds it". That is the same rule UpdateRequest states for
	// ForceAssigneeTransfer beside its own ExpectedAssignee.
	ExpectedAssignee *string
	// Force bypasses the ownership fence, so an actor that is not the holder
	// may release the claim. It is the escape hatch `bd unclaim --force` spells,
	// for an abandoned claim whose holder crashed.
	//
	// IT BYPASSES THE FENCE AND NOTHING ELSE. It does not make an unheld row
	// releasable (that is ErrNotClaimed with or without it), it does not make a
	// closed one releasable (ErrNotReleasable), and it never bypasses a
	// precondition — see ExpectedAssignee, which it may not accompany. Force
	// answers "may I release someone else's claim"; every other refusal below
	// answers "is there a claim here to release", which force has no opinion
	// about.
	Force bool
}

// ReleaseResult reports one release as a detached post-state snapshot.
//
// It is shaped after ClaimResult rather than after UpdateResult, and for the
// same reason: this family answers with the issue ROW, without labels,
// dependency records or comments.
type ReleaseResult struct {
	// Issue is a detached post-release snapshot of the issue row and its
	// labels, without dependency records or comments. It is read from the row
	// INSIDE the releasing transaction, so it is what a subsequent read sees
	// rather than a value composed from the request and the pre-state.
	//
	// THE LABELS ARE THE ONE PLACE THIS DIFFERS FROM ClaimResult.Issue, which
	// is the bare row. That difference is not a preference: Claimer answers an
	// already-published wire shape and cannot enrich its snapshot without
	// changing what a shipped body returns, and this role has no published
	// shape to preserve. The read the release needs anyway hydrates labels, so
	// stripping them would be inventing a difference rather than keeping one.
	//
	// THE POST-RELEASE RowVersion RIDES ON Issue.RowVersion and is deliberately
	// not copied onto a second result field. It is the token a caller feeds to
	// a following ExpectedVersion, and two spellings of one token is how they
	// come to disagree.
	Issue *Issue
	// Changed reports whether the release WROTE the row.
	//
	// IT IS TRUE ON EVERY ANSWER THIS ROLE RETURNS WITHOUT AN ERROR, because
	// every shape that would not write is refused above it: an unheld row is
	// ErrNotClaimed rather than an idempotent no-op.
	//
	// THE ASYMMETRY WITH ClaimResult.Changed IS NOT AN INCONSISTENCY, and the
	// reason is a fact about the two POST-STATES rather than a preference.
	// Claimer can afford an idempotent answer because the state a claim leaves
	// behind IDENTIFIES THE CLAIMANT: a re-claim finds assignee == Actor and
	// StatusInProgress, which is the claim's own signature surviving on the row,
	// so the role can tell "you already did this" from "somebody else holds it"
	// and reports Changed false for exactly the first. A release leaves an
	// ANONYMOUS post-state — assignee cleared, status open, started_at gone —
	// which is the same row no matter who emptied it, or whether it was ever
	// full. An idempotent release would therefore answer Changed false
	// identically for "I already released this", "a reaper beat me to it" and
	// "nothing ever claimed it", with NOTHING LEFT ON THE ROW to tell them
	// apart. Those three want different things from a caller, so the role
	// refuses instead and lets the caller decide which of them it can live
	// with — see Releaser.Release's non-idempotence paragraph.
	//
	// So the field is here because ClaimResult.Changed is, and a caller holding
	// both roles should not have to read the pair two ways. Nothing returns it
	// false today, and the conformance contract says so in its coverage
	// paragraph rather than carrying a case that could not fail.
	Changed bool
}

// ErrNotClaimed classifies the refusal of a release over a row that holds no
// claim: there is nothing to let go of.
//
// It lives beside the role rather than in errors.go because it is meaningless
// without ReleaseRequest to explain it, and errors.go is the file every
// parallel role slice touches. Its neighbor ErrNotOwner is in errors.go
// instead, because that one is the shared ownership vocabulary the claim family
// already speaks.
//
// IT IS A REFUSAL AND NOT A NO-OP, which is the raw seam's rule. A caller whose
// release found nothing to release did not do what it believed it was doing —
// most often because a reaper got there first — and the two facts are worth
// telling apart. Callers for which "already released" is the ordinary path
// errors.Is this and carry on; that is one line, and it is a line the caller
// writes knowingly rather than one the role writes on its behalf.
var ErrNotClaimed = errors.New("issue holds no claim")

// ErrNotReleasable classifies the refusal of a release over a row whose STATUS
// will not accept one: a closed issue, or an issue in any status other than the
// two the release transition is defined over.
//
// IT IS WIDER THAN "CLOSED", and that is worth knowing before it surprises
// someone. The release is an UPDATE pinned to status open or in_progress, so an
// issue parked in a workspace's own configured status — an active or wip status
// a `bd update` put it in — is refused here even though it plainly holds a
// claim. That is a limitation of the transition rather than a policy, and it is
// named rather than papered over: before this role, the same request failed
// with an untyped "no matching row" that no caller could classify and no
// message explained.
var ErrNotReleasable = errors.New("issue status does not accept a release")

// Releaser describes the release of a claim — the capability behind
// `bd unclaim` — and, like every other capability here, a role with its own
// accessor. A new capability gets a new role interface and its own accessor;
// never append a method here.
//
// IT IS A DIFFERENT QUESTION FROM ITS NEIGHBORS.
//
//   - Claimer takes ownership. This gives it back, and the two are not one
//     role with a flag: a caller entitled to release its own work is very
//     often not entitled to take new work, and a surface carrying both hands
//     it a capability it should not be able to reach. That is the same test
//     Bootstrapper and InitVerifier were split by.
//   - Lifecycle.Update can clear an assignee as one field among many, and
//     that is exactly why this is separate. An update spells the release as a
//     patch — assignee to empty, status to open, started_at to null — which
//     puts the transition's definition in the CALLER, three fields at a time,
//     and leaves the lease row behind. A release is one act with a known
//     post-state, and the lease it drops is the part a patch cannot express at
//     all.
//   - Lifecycle.Close ends the work. This ends the OWNERSHIP and leaves the
//     work open for the next taker, which is why its post-state is status open
//     rather than anything in the done category.
//
// WHAT A RELEASE DOES TO THE ROW, stated once so no caller has to reconstruct
// it: assignee is cleared, status becomes the literal StatusOpen, started_at is
// cleared, the row's lease is deleted, and RowVersion is reminted so a
// concurrent reclaim or close on the same row conflicts rather than silently
// merging. An "unclaimed" event is recorded against Actor, and the row's
// version-control history sees the release as an update.
//
// WRITES, AND ITS HOOK IS on_update. internal/hooks publishes on_create,
// on_update and on_close; a release changes assignee and status, which is an
// update and is already how the journal classifies it. There is no on_unclaim
// to fire and inventing one is not this role's job.
//
// Deterministic request-validation failures match ErrValidation. Result values
// are unspecified when error is non-nil, and every refusal below leaves
// persistent state unchanged.
type Releaser interface {
	// Release gives up the claim on one issue and reports the row it left
	// behind.
	//
	// IT IS ONE TRANSACTION. The read that classifies the refusals, the release
	// itself and the post-state snapshot all see one snapshot, so the Issue it
	// answers with is the row the release produced rather than a row some other
	// writer produced afterwards.
	//
	// THE ORDER THE REFUSALS HAPPEN IN IS PART OF THE ANSWER, because a request
	// can fail several ways at once. Request validation runs before anything is
	// read; then existence; then the row's STATUS, because a closed issue is
	// not a claim question at all; then whether a claim exists; then the
	// precondition or the fence, whichever the request selected.
	//
	// REFUSALS:
	//
	//   - an empty Actor or IssueID, an ExpectedAssignee that is non-nil and
	//     blank, or Force beside a non-nil ExpectedAssignee: ErrValidation,
	//     before anything is read;
	//   - an id naming no row in either plane: ErrNotFound;
	//   - a closed issue, or one whose status is neither StatusOpen nor
	//     StatusInProgress: ErrNotReleasable;
	//   - a row that holds no claim, on the unconditional path with or without
	//     Force: ErrNotClaimed;
	//   - a non-nil ExpectedAssignee that does not match the current holder —
	//     INCLUDING a row that holds no claim, which is a mismatch rather than
	//     ErrNotClaimed, because the caller asked about a specific holder and
	//     the answer is that it is not the holder: ErrAssigneeMismatch;
	//   - an unforced, unconditional release by an actor that is not the
	//     holder: ErrNotOwner.
	//
	// A RELEASE IS NOT IDEMPOTENT, and the ErrNotClaimed entry above is where
	// that shows. A caller that retries after a crash and cannot tell whether
	// its first release landed matches that sentinel and treats it as success;
	// the role will not make that decision on its behalf, because "somebody
	// else released this" and "I released this twice" are the same answer from
	// here and only the caller knows which one it can live with.
	//
	// Implementations own transaction retry: a release that loses a
	// commit-time merge is retried, never surfaced. Other operational or
	// commit-finalization errors can have an indeterminate durable outcome;
	// callers must reread state before retrying.
	Release(context.Context, ReleaseRequest) (ReleaseResult, error)
}
