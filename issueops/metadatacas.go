package issueops

import (
	"context"
	"encoding/json"
)

// CompareAndSetKeyRequest describes one conditional write of ONE metadata key.
//
// It is a state transition stated as a pair: Expected is the key's value
// BEFORE, Value is its value AFTER, and nil on either side means the key is
// ABSENT there. That symmetry is the whole request shape — a conditional
// create is (nil → value), a conditional swap is (value → value), and a
// conditional delete is (value → nil) — so a caller that can create a key can
// also return it to the state its own create tested for.
//
// Implementations never mutate caller-owned request values: the two raw
// messages are read, never written through.
type CompareAndSetKeyRequest struct {
	// Actor attributes the swap on the history entry it records. It is
	// REQUIRED — an empty Actor is ErrValidation — because a swap is a
	// coordination write between racing callers, and the one question asked of
	// its history entry afterwards is which of them won. It is not an
	// authorization boundary: nothing here checks who the caller is.
	Actor string
	// IssueID is the exact canonical id and must not be empty. The
	// issue-to-wisp fallback happens INSIDE, exactly as it does for Reader.Get,
	// so a caller never has to know which plane the key lives on. There is no
	// fuzzy or prefix resolution here, for the reason GetRequest.ID gives.
	IssueID string
	// Key is the single metadata key this request reads and writes. It is
	// validated against the workspace's metadata-key syntax, so a key the query
	// layer could not later spell is ErrValidation rather than a row only this
	// role can reach.
	//
	// ONE KEY, NOT A PATH. A dotted key like "gc.lease" names a top-level key
	// spelled with a dot, not a nested field of an object under "gc"; the
	// metadata object's nesting is VALUE structure, and this role swaps whole
	// values.
	Key string
	// Expected is the value the key must currently hold for the swap to
	// happen. nil means the key must be ABSENT, which is what makes a
	// first-writer-wins create expressible.
	//
	// EQUALITY IS CANONICAL, NOT BYTE-WISE. Two JSON values are equal when
	// their canonical encodings match: insignificant whitespace is ignored and
	// object keys are compared as a set rather than in the order they were
	// written, so a caller cannot lose a race to its own formatter.
	//
	// NUMBERS ARE COMPARED AS THEIR SOURCE LITERAL: 1 and 1.0 are NOT equal.
	// That is a CONSTRAINT ON CALLERS, not a precision guarantee, and the
	// difference matters. The metadata column decodes JSON numbers through
	// float64 and re-emits them, so a number is not always stored as it was
	// written — measured on today's backends, 1.0 lands as 1, an integer past
	// 2^53 is rounded to the nearest double, -0.0 lands as 0, and 1e300 lands as
	// three hundred and one digits. The role cannot give a number back the
	// fidelity the column took from it.
	//
	// SO COMPOSE AN Expected FROM A PREVIOUS Current, NEVER FROM YOUR OWN
	// SPELLING OF A NUMBER. Current is the value the ROW holds, so a loop that
	// feeds it back converges; a loop that re-sends a literal the column
	// renormalized compares its spelling against the substrate's, is refused
	// every time, and makes no progress. AND PREFER A STRING FOR A COORDINATION
	// TOKEN — an id, an actor, an epoch stamp rendered as digits in quotes —
	// which round-trips byte-for-byte and has none of this to reason about.
	//
	// A key stored holding JSON null is a PRESENT key, distinct from an absent
	// one: nil Expected does not match it, and an Expected of `null` does. The
	// substrate can tell the two apart — an absent key is missing from the
	// metadata object, a null one is in it — so this role reports the
	// distinction instead of conflating it.
	Expected *json.RawMessage
	// Value is the value the key holds after a successful swap. nil REMOVES the
	// key, which is the transition a released lease needs: without it a lease
	// taken with a nil Expected could never be returned to the state a
	// subsequent acquire tests for.
	//
	// It must be a well-formed JSON value when non-nil; anything else is
	// ErrValidation before the substrate is touched.
	Value *json.RawMessage
}

// CompareAndSetKeyResult reports the outcome of one conditional write.
type CompareAndSetKeyResult struct {
	// Swapped reports whether the precondition held and the transition was
	// applied. It is the verdict, and it is false for a LOST RACE rather than
	// for a failure: see MetadataCAS.CompareAndSetKey.
	Swapped bool
	// Current is the key's value as the deciding transaction saw it — the
	// value that refused the swap when Swapped is false, and the value the
	// swap landed when it is true. nil means the key was (or is now) absent.
	//
	// IT IS WHAT MAKES A RETRY LOOP A LOOP. A caller that lost the race
	// recomputes from this value and calls again with it as Expected, without a
	// second read that could itself go stale. Reporting only the verdict would
	// force exactly the read-then-act shape this role removes.
	//
	// IT IS ALWAYS READ FROM THE ROW, on every path — a refusal, a swap that
	// landed, and a precondition that held over an already-equal value — inside
	// the transaction that decided. It is therefore what a subsequent read sees,
	// NOT an echo of what the caller sent: where the substrate renormalized the
	// value on the way in, Current reports the substrate's form. That is what
	// makes feeding it back as the next Expected a converging loop, and it is
	// the only way a caller can SEE that a number it wrote was not the number
	// that was stored.
	Current *json.RawMessage
}

// MetadataCAS describes a compare-and-set over ONE metadata key of one issue —
// the conditional write every coordination protocol over the metadata plane is
// built from — and, like every other capability here, a role with its own
// accessor. A new capability gets a new role interface and its own accessor;
// never append a method here.
//
// IT IS A DIFFERENT QUESTION FROM ITS NEIGHBORS.
//
//   - Lifecycle.Update patches an issue with the fields a caller names,
//     including its metadata, and answers with the issue. It has no way to
//     express "only if", so composing it with a read is the check-then-act
//     shape that loses to a racing writer in the gap. That gap is not narrowed
//     by re-reading; nothing but a guard inside the write closes it.
//   - Lifecycle's ExpectedVersion, ExpectedAssignee and ExpectedStatus guards
//     are compare-and-set over the row's LIFECYCLE — a version token, a
//     holder, a status. They gate an ordinary edit on a fact about the row,
//     and they cannot express a guard on a key a caller invented, which is
//     where coordination state that is not a claim actually lives.
//   - Claimer answers "may this actor hold this issue" with the workspace's
//     claim policy applied. This asks nothing of policy: the key, its meaning
//     and the protocol over it belong entirely to the caller, and the role
//     supplies only atomicity.
//
// A CALLER LOOPS ON IT, and the role is shaped for that. The designed use is
// read the current value, decide, and swap conditionally; on a refusal, decide
// again from the value the refusal carried. A protocol built this way converges
// without a lock, and neither the key's meaning nor the retry policy is
// anything this package knows about.
//
// WHAT IT DOES NOT PROMISE is a fence. The guard is the KEY'S OWN VALUE, so two
// callers that write the same value are indistinguishable to it, and a caller
// that needs "nobody else has written since I read" needs a token that changes
// on every write — which is Lifecycle's ExpectedVersion, over a different set
// of columns. Say which one the protocol needs before choosing.
//
// Implementations never mutate caller-owned request values, snapshot the
// request at method entry, and apply validation only to attempt-local clones.
// Deterministic request-validation failures match ErrValidation and leave
// persistent state unchanged.
type MetadataCAS interface {
	// CompareAndSetKey applies req's transition if and only if the key
	// currently holds req.Expected, and reports what it found either way.
	//
	// IT IS ONE TRANSACTION, and that is the central promise of this role
	// rather than an implementation note. The read of the key, the comparison
	// and the write of the whole metadata object all see one snapshot, so:
	//
	//	the value that decided the verdict IS the value that was replaced
	//
	// A write that lands between a caller's read and its swap cannot be
	// overwritten by it. Every caller before this role existed composed a
	// transaction, a read and an update by hand to get that property, and each
	// one had to get the composition right.
	//
	// SIBLING KEYS SURVIVE. The metadata object is a single stored blob, so the
	// write re-serializes the object READ INSIDE THIS TRANSACTION rather than
	// patching one field. A concurrent swap of a DIFFERENT key is therefore
	// preserved: it either committed before this transaction read, and is in
	// what this one writes, or it conflicts with it and is retried against the
	// winner.
	//
	// A MISMATCH IS AN ANSWER, NOT A FAILURE: Swapped false, Current carrying
	// the value that refused it, and a nil error. That is deliberate and it is
	// where this role diverges from Lifecycle's ExpectedAssignee and
	// ExpectedStatus guards, which raise ErrAssigneeMismatch and
	// ErrStatusMismatch. Those guards exist to make a shell script's swallowed
	// refusal impossible, and a non-zero exit is how a front door enforces
	// that; a role's caller here is a retry loop, for which a lost race is the
	// ordinary path and an error is the wrong shape for it — and an error
	// cannot hand back Current without a bespoke type every caller must
	// errors.As before it can read the one field it needs.
	//
	// A FRONT DOOR OWES THE CALLER AN UNMISTAKABLE ANSWER, WHICH IS NOT THE
	// SAME AS AN ERROR, and the shipped HTTP spelling is the worked example:
	// POST /v0/beads/issues/{id}:casMetadata answers a refused swap with 200
	// and `swapped: false` beside `current`, and publishes NO 409 at all,
	// because a conflict code would put a retry loop's ordinary iteration into
	// the error channel and force the value that loop needs next to travel as
	// a problem extension member. What a front door must never do is let a
	// false Swapped read as a landed write: `swapped` is the member a client
	// dispatches on, and a surface that reported only the status would be
	// telling a caller its swap succeeded. A front door whose contract is an
	// EXIT CODE — a shell script's, where a swallowed refusal is the hazard
	// this role's sibling guards raise ErrAssigneeMismatch for — owes a
	// non-zero exit instead, for the same reason spelled in its own vocabulary.
	//
	// REFUSALS, all before anything is written:
	//
	//   - an empty Actor, an empty IssueID, or a Key outside the metadata-key
	//     syntax: ErrValidation;
	//   - an Expected or Value that is not well-formed JSON: ErrValidation;
	//   - an IssueID naming neither an issue nor a wisp: ErrNotFound.
	//
	// AN ISSUE WHOSE STORED METADATA IS NOT A JSON OBJECT is an error rather
	// than an empty map: the role would otherwise report every key absent and
	// let a create win against a blob it is about to destroy.
	//
	// That error is deliberately UNTYPED — it matches neither ErrValidation nor
	// ErrNotFound, and a front door reports it as an internal failure. Both
	// typed refusals would be lies a caller could act on: the request was
	// well-formed and the issue exists. The row is corrupt, no retry converges,
	// and an operator is the only one who can do anything about it.
	//
	// A SWAP THAT CHANGES NOTHING WRITES NOTHING. When the precondition holds
	// and Value already equals what is stored — including the absent-to-absent
	// case a nil Expected and a nil Value spell — Swapped is true, Current is
	// that value, and no row and no history entry is touched. The verdict
	// answers the precondition; it does not claim a write happened.
	//
	// A SWAP THAT CHANGES SOMETHING RECORDS EXACTLY ONE HISTORY ENTRY,
	// attributed to req.Actor, the same entry any other metadata write on this
	// issue records. A swap on an EPHEMERAL row records NO durable history
	// entry — none, not "at most one" — for the reason Commenter.AddComment
	// gives: ephemeral rows are not versioned and their tables are ignored by
	// the version-control plane, so an entry naming one would be the sync
	// artifact that ignoring them exists to prevent.
	CompareAndSetKey(ctx context.Context, req CompareAndSetKeyRequest) (CompareAndSetKeyResult, error)
}
