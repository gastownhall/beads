package issueops

import (
	"context"
	"time"
)

// SweepTier names which of the two planes a sweep clears. It is REQUIRED on
// every request and has no default, for the reason RelatedRequest.Direction
// has none: the two tiers are disjoint sets of rows reached by one operation,
// and a caller handed the wrong one has nothing to notice until the rows are
// gone.
//
// The two are DISJOINT by construction — a sweep of one tier can never touch a
// row of the other — which is what lets `bd purge` and `bd prune` be one
// capability with a parameter rather than two capabilities that resemble each
// other.
type SweepTier string

const (
	// SweepEphemeral clears CLOSED rows of the wisp tier: `bd purge`.
	// Ephemeral rows accumulate fast, carry no long-term value once closed,
	// and live in tables the version-control plane ignores.
	SweepEphemeral SweepTier = "ephemeral"
	// SweepDurable clears CLOSED rows of the issue tier: `bd prune`. This is
	// the tier a workspace's real history lives in, which is why it — and only
	// it — carries the require-a-filter refusal SweepRequest.ClosedBefore
	// describes.
	SweepDurable SweepTier = "durable"
)

// SweepRequest describes one bulk clearance of closed rows: which tier, which
// of its closed rows, and whether to do it or only report it.
//
// IT IS NOT A FILTER-SHAPED READ REQUEST with a delete attached. The predicate
// is FIXED at "closed rows of one tier" and the two fields below only narrow
// it; there is no status, no assignee, no label and no free-text query,
// because every one of those would be a way to spell a destructive selection
// that no front door asks for and that a caller could get subtly wrong. What
// this request can express is exactly what `bd purge` and `bd prune` expose,
// and widening it is a decision rather than an omission to be filled in.
//
// Implementations never mutate caller-owned request values: ClosedBefore is
// read, never written through.
type SweepRequest struct {
	// Actor attributes the sweep wherever the backend records attribution. It
	// is not an authorization boundary — nothing here checks who the caller
	// is — and an empty Actor is accepted, because the deletion of a row
	// leaves no row to attribute it on.
	Actor string
	// Tier selects the plane. It is REQUIRED: the zero value is ErrValidation
	// rather than a default, and an unrecognized value is ErrValidation rather
	// than a sweep that matches nothing.
	Tier SweepTier
	// ClosedBefore keeps only rows closed STRICTLY BEFORE this instant. A row
	// whose stored closed_at is exactly this instant is kept, which is the
	// half-open interval every other time bound in this package uses.
	//
	// It is one half of the DURABLE TIER'S SAFETY GATE: a SweepDurable request
	// with neither ClosedBefore nor IDPattern set is ErrValidation, never an
	// unfiltered mass delete. That refusal lives HERE, below every front door,
	// rather than in the CLI handler that historically owned it. The gate is a
	// safety invariant and not presentation: a second front door — an HTTP
	// operation, a library embedder — that inherited the capability without
	// the guard would be one handler away from erasing every closed issue in a
	// workspace, and a guard that each caller must remember is a guard that
	// one caller will not. A caller that really does mean "everything closed"
	// says so with IDPattern "*", which is a deliberate keystroke rather than
	// an omitted one.
	//
	// The EPHEMERAL tier carries no such gate: an unfiltered sweep of closed
	// wisps is the ordinary use of `bd purge`, and the tier is transient by
	// definition.
	ClosedBefore *time.Time
	// IDPattern keeps only rows whose ID matches this shell glob, in the
	// syntax of path/filepath.Match (`*`, `?`, `[...]`, `\` escapes; `*` also
	// crosses `-` and `.`, since an id is not a path). Empty matches every row
	// in the tier.
	//
	// A MALFORMED PATTERN IS ErrValidation, not a pattern that matches
	// nothing. Both front doors previously discarded filepath.Match's error,
	// so `--pattern '['` reported "no beads to prune" — indistinguishable from
	// a correct pattern over an empty set, on a command whose whole job is to
	// delete what it matched.
	//
	// It is the other half of the durable tier's gate: see ClosedBefore.
	IDPattern string
	// ProtectReferenced skips candidates whose ID is CITED by a row that is
	// not done — in its description, its notes, or any of its comments — so a
	// decision trail a live bead still points at is not deleted out from under
	// it.
	//
	// WHAT COUNTS AS A CITATION is a literal occurrence of the id at ASCII
	// word boundaries: `be-1` matches in "see (be-1)." and does not match
	// inside `xbe-1` or `be-12`. It is a text scan, not a link: an id spelled
	// in prose protects, and a stored dependency edge does not (a sweep
	// removes edges pointing at what it deletes — see SweepResult).
	//
	// WHICH ROWS ARE ASKED is every row whose status is not a done one: the
	// built-in active statuses plus every configured custom status whose
	// category is not "done". Enumerating the workspace's custom statuses is
	// REQUIRED rather than best-effort, because a failure to read them would
	// under-scan and delete a bead a live custom-status bead still cites; an
	// implementation that cannot read them fails the sweep instead.
	//
	// It costs a full scan of the not-done set and of that set's comments, so
	// it is opt-in per request rather than always-on. `bd prune` asks for it;
	// `bd purge` does not, because a wisp's citations are as transient as the
	// wisp.
	ProtectReferenced bool
	// DryRun reports what the sweep WOULD do and deletes nothing. The result
	// is otherwise the same result — the same counts, the same skips, the same
	// refusals — computed against the same snapshot the real sweep would have
	// used, which is what makes a preview worth reading.
	DryRun bool
}

// SweepSkips reports the candidates a sweep declined to delete, bucketed by
// WHY. Every bucket is a row the tier's predicate admitted and the sweep then
// held back, so the buckets and SweepResult.Swept describe one candidate set.
//
// They are separate counters rather than one number because they mean
// different things to the person reading them: Pinned and Referenced are
// PROTECTIONS a user can override or re-express, and the other three are the
// sweep declining to trust its own input.
type SweepSkips struct {
	// Pinned counts candidates protected by the pinned flag. Pinning is the
	// workspace's own "never sweep this", and no request field overrides it —
	// a caller that wants a pinned row gone unpins it first.
	Pinned int
	// Referenced counts candidates skipped by ProtectReferenced. It is always
	// 0 when that field is false; a caller that reads a 0 without having asked
	// for the protection has learned nothing about whether rows are cited.
	Referenced int
	// NotClosed, UnknownClosedAt and ClosedAtOrAfterCutoff count candidates
	// the tier's own query returned and the sweep rechecked and rejected: a
	// status that is not closed, a closed row with no closed_at stamp at all,
	// and a closed_at that does not satisfy ClosedBefore.
	//
	// A NON-ZERO VALUE IN ANY OF THESE THREE IS A DEFENSE FIRING, not a normal
	// outcome, because the query was asked for exactly the rows they exclude.
	// They are published rather than silently dropped so an operator can see
	// that a delete-everything-closed query and a delete-everything-closed
	// recheck disagreed, which is the state in which nothing should be
	// deleted on trust.
	NotClosed             int
	UnknownClosedAt       int
	ClosedAtOrAfterCutoff int
	// Unreadable counts rows the tier's query returned as nothing at all. It
	// is a defense of the same kind, on a shape rather than a value.
	Unreadable int
}

// SweepResult reports one sweep.
//
// EVERY NUMBER DESCRIBES THE SAME SNAPSHOT, because the whole sweep — the
// candidate query, the recheck, the reference scan and the deletion — runs in
// ONE transaction. See Sweeper.Sweep.
type SweepResult struct {
	// DryRun echoes the request. It is here so a result value carries whether
	// its numbers describe rows that are gone or rows that would go, without
	// the caller having to keep the request beside it.
	DryRun bool
	// Swept is how many rows were deleted, or — under DryRun — would be.
	Swept int
	// Dependencies, Labels and Events count the ROWS OF ASSOCIATED DATA the
	// sweep removed with them: edges pointing at or out of a swept row, its
	// labels, and its recorded events. They are reported because a sweep's
	// visible effect is much larger than its row count, and because they are
	// the number that explains a large storage reclaim from a small Swept.
	Dependencies int
	Labels       int
	Events       int
	// Skipped reports the candidates the sweep held back and why.
	Skipped SweepSkips
	// ReferencedIDs is a BOUNDED SAMPLE of the ids Skipped.Referenced counts —
	// at most SweepReferencedSampleLimit of them, in the order the candidate
	// query returned them. It is a sample and not the set: a caller that needs
	// every protected id re-runs with ProtectReferenced false and a DryRun to
	// see the difference, or narrows the request.
	//
	// It is empty when nothing was protected, and never longer than the count
	// beside it.
	ReferencedIDs []string
}

// SweepReferencedSampleLimit bounds SweepResult.ReferencedIDs. It is published
// so a caller can tell a truncated sample from a complete one by comparing the
// slice's length against it, rather than against a number copied out of an
// implementation.
const SweepReferencedSampleLimit = 100

// Sweeper describes bulk clearance of CLOSED rows — the capability behind
// `bd purge` (the ephemeral tier) and `bd prune` (the durable one) — and, like
// every other capability here, a role with its own accessor. A new capability
// gets a new role interface and its own accessor; never append a method here.
//
// ONE CAPABILITY, NOT TWO. purge and prune name one operation over two
// disjoint tiers: the same candidate query, the same pinned protection, the
// same closed_at recheck, the same deletion. The tier is a REQUEST FIELD
// because that is what it is — a selection — and splitting it into two roles
// would have produced two interfaces over one body, which is the accretion the
// governing rule exists to prevent in the other direction. What genuinely
// differs between them is expressed as request fields too: the durable tier's
// require-a-filter refusal keys on Tier, and the reference protection is
// asked for rather than implied.
//
// IT IS A DIFFERENT QUESTION FROM ITS NEIGHBORS.
//
//   - Reader answers with rows. This answers with an EFFECT, and its
//     result is a tally of what it did — there is no page of it, no cursor
//     into it, and asking twice does not answer twice.
//   - Counter answers "how many match". A sweep matches and then ACTS, and
//     the two cannot be composed by a caller without reintroducing exactly the
//     window this role closes: count, then delete what you counted, is two
//     transactions.
//   - Lifecycle patches ONE issue the caller names. A sweep names none: it
//     describes a set and the implementation resolves it. That is the whole
//     reason the safety gate can live here at all — there is a selection to
//     refuse.
//
// WRITES, AND THE ONLY DESTRUCTIVE ROLE IN THIS PACKAGE. A sweep deletes rows
// and everything that hangs off them, and nothing it deletes comes back. Its
// completion fires the hook the front doors already fire for a bulk change;
// its refusals change nothing at all.
//
// Deterministic request-validation failures match ErrValidation. Result values
// are unspecified when error is non-nil, with one deliberate exception stated
// on Sweep.
type Sweeper interface {
	// Sweep deletes the closed rows of req.Tier that req's two narrowing
	// fields admit, and reports what it did.
	//
	// IT IS ONE TRANSACTION, and that is the central promise of this role
	// rather than an implementation note. The candidate query, the pinned and
	// closed_at rechecks, the reference scan and the deletion all see one
	// snapshot, so:
	//
	//	the set this result describes IS the set it deleted
	//
	// A row created, closed, unpinned or cited between the selection and the
	// deletion cannot change which rows go. The direct CLI route did not have
	// that property before this role existed — it selected in one transaction
	// and deleted in another, so a bead closed in the window was reported and
	// not deleted, and a bead pinned in the window was deleted after being
	// judged unpinned.
	//
	// WHAT THAT COSTS, said out loud because it is a real trade and not a free
	// win: a sweep is ALL OR NOTHING. A sweep large enough to exceed a
	// backend's write timeout fails whole and deletes nothing, where a
	// batching implementation would have deleted some of it. That is the
	// better of the two failures — the partial one previously reported a
	// deleted count of zero while having deleted rows — but a caller sweeping
	// a very large set narrows the request rather than expecting progress.
	//
	// THE ORDER THE NARROWING HAPPENS IN IS PART OF THE ANSWER, because the
	// skip counters are counted along the way: the tier's closed rows, then
	// IDPattern, then the pinned and closed_at rechecks, then the reference
	// protection. A pinned row excluded by the pattern is therefore NOT
	// counted in Skipped.Pinned — it was never a candidate — and the counters
	// describe the set the request actually reached.
	//
	// REFUSALS, all ErrValidation and all before anything is read:
	//
	//   - an unset or unrecognized Tier;
	//   - a SweepDurable request with neither ClosedBefore nor IDPattern (see
	//     SweepRequest.ClosedBefore for why this lives here);
	//   - an IDPattern that is not a well-formed glob.
	//
	// A REQUEST THAT MATCHES NOTHING IS A ZERO RESULT AND A NIL ERROR, not a
	// not-found: an empty set of closed rows is the steady state of a swept
	// workspace, and it is the answer a scheduled sweep gets every time it
	// runs after the first.
	//
	// A DRY RUN CHANGES NOTHING, including history: an implementation that
	// records a version-control entry for a sweep records none for a dry run
	// and none for a sweep that deleted nothing. Where an implementation
	// versions at all, one sweep records AT MOST ONE entry — the deletion is
	// one act, not one per row.
	Sweep(ctx context.Context, req SweepRequest) (SweepResult, error)
}
