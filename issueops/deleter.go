package issueops

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// DeleteRequest names the rows to erase and says what to do about the rows
// that point at them.
//
// IT IS NOT A SELECTION. Sweeper describes a SET and lets the implementation
// resolve it; this one is handed ids and deletes those. That difference is why
// this request carries no predicate at all — no status, no cutoff, no glob —
// and why it needs no require-a-filter gate: a caller cannot spell "everything"
// here without typing every id.
//
// Implementations never mutate caller-owned request values: IDs is read, never
// written through, and never sorted in place.
type DeleteRequest struct {
	// Actor attributes the deletion wherever the backend records attribution.
	// It is not an authorization boundary, and an empty Actor is accepted for
	// the reason SweepRequest.Actor is: the deletion of a row leaves no row to
	// attribute it on. It DOES reach the rewritten neighbors, which survive,
	// so a workspace that cares who rewrote a description passes one.
	Actor string
	// IDs names the rows to delete, in either plane. It is REQUIRED: an empty
	// or all-blank slice is ErrValidation rather than a no-op, because "delete
	// nothing" is never what a caller meant to ask and a silent zero is how a
	// broken id list looks from the outside.
	//
	// DUPLICATES COLLAPSE and the surviving order is the caller's first
	// mention of each id. An id repeated in a `--from-file` list is one row,
	// not a row deleted twice.
	//
	// EVERY ID MUST NAME A STORED ROW. An id that names none is ErrNotFound
	// and NOTHING IS DELETED — not even the ids beside it that did resolve.
	// That is the all-or-nothing this operation needs most: a batch that
	// deleted eight of ten and then reported a typo has already done the part
	// that cannot be undone.
	//
	// IDs ARE EXACT. Prefix resolution, cross-repository routing and `bd`'s
	// other id conveniences happen at the front door, above this role, because
	// resolving an ambiguous prefix to a row and then deleting it is the one
	// place a convenience is not one.
	IDs []string
	// Cascade also deletes the TRANSITIVE CLOSURE of everything that depends
	// on the named rows, in both planes. It is what `bd delete --cascade`
	// asks for.
	//
	// WITH CASCADE THERE IS NOTHING LEFT TO ORPHAN, by construction: the
	// closure contains every dependent of every row it deletes. So Cascade
	// makes Force moot rather than conflicting with it — a request carrying
	// both is legal and behaves as Cascade, and DeleteResult.Orphaned comes
	// back empty.
	//
	// The traversal is BOUNDED. A closure that grows past the backend's
	// runaway limit aborts the whole request and deletes nothing rather than
	// deleting as far as it got.
	Cascade bool
	// Force deletes the named rows and leaves rows that depended on them
	// ORPHANED — their edges into the deleted rows go, they themselves stay,
	// and DeleteResult.Orphaned names them.
	//
	// WITHOUT Cascade AND WITHOUT Force, a named row that some row OUTSIDE the
	// request depends on is refused: the request fails with a
	// DependentsOutsideRequestError and nothing is deleted. That guard is the
	// role's, not a front door's, for the reason Sweeper's require-a-filter
	// gate is: a second front door that inherited the capability without the
	// guard would orphan a workspace's graph by omission.
	//
	// THE GUARD READS BOTH PLANES ON BOTH ENDS OF THE EDGE, and there is no
	// wisp exemption at either end. A named WISP with a durable dependent is
	// refused exactly as a named issue is, and a durable row's wisp dependent
	// counts too. Half of that was an implementation gap for one release: the
	// store-backed body asked the guard about the durable half of the request
	// only, so `bd delete <wisp>` orphaned a durable dependent without saying
	// so and without being refused.
	//
	// A dependent INSIDE the request is not a dependent for this purpose — a
	// batch that names both ends of an edge is deleting the edge too, and
	// refusing it would make `bd delete a b` fail on exactly the pair a caller
	// took care to list together.
	Force bool
	// DryRun reports what the deletion WOULD do and changes nothing, including
	// history.
	//
	// IT REFUSES WHERE THE REAL RUN WOULD REFUSE, and that is the whole value
	// of it on this operation: an unforced preview over a row with outside
	// dependents fails with the same DependentsOutsideRequestError, and a
	// preview naming an absent id fails with the same ErrNotFound, so a caller
	// that got a clean preview has learned the real run will not stop
	// half-explained. It is computed against the same snapshot the real
	// deletion would have used.
	DryRun bool
}

// DeleteResult reports one deletion.
//
// EVERY NUMBER DESCRIBES THE SAME SNAPSHOT, because the guard, the cascade
// expansion, the deletion and the reference rewrite all run in ONE
// transaction. See Deleter.Delete.
type DeleteResult struct {
	// DryRun echoes the request, so a result value carries whether its numbers
	// describe rows that are gone or rows that would go without the caller
	// having to keep the request beside it.
	DryRun bool
	// Deleted is how many rows were deleted, or — under DryRun — would be.
	// Under Cascade it counts the whole closure, so it is normally larger than
	// len(IDs) and is the number a caller should show rather than the request
	// length.
	Deleted int
	// Dependencies, Labels and Events count the ROWS OF ASSOCIATED DATA that
	// went with them: edges touching a deleted row in either direction, its
	// labels, and its recorded events. A deletion's visible effect is much
	// larger than its row count, and these are the numbers that explain it.
	Dependencies int
	Labels       int
	Events       int
	// ReferencesUpdated counts the SURVIVING rows whose text was rewritten —
	// rows, not occurrences: a neighbor citing two deleted ids in three
	// fields counts once. It is 0 under DryRun, because a preview rewrites
	// nothing and reporting a number there would describe an edit that did not
	// happen.
	ReferencesUpdated int
	// Orphaned names the surviving rows that depended on something this
	// request deleted, in ascending id order.
	//
	// It is populated exactly when the request carried Force without Cascade —
	// the only mode in which orphaning is possible — and is empty otherwise.
	// It is also carried on the DependentsOutsideRequestError the unforced
	// mode returns, so the same fact reaches a caller whichever answer it got.
	//
	// It is DIRECT dependents only. A row two edges away loses no edge and is
	// not orphaned by this deletion; it is merely blocked by something that is
	// now blocked by nothing.
	Orphaned []string
}

// ErrDependentsOutsideRequest classifies the unforced refusal: a named row has
// a dependent the request did not name, and neither Cascade nor Force said
// what to do about it.
//
// It lives beside the role rather than in errors.go because it is meaningless
// without DeleteRequest.Cascade and DeleteRequest.Force to explain it, and
// errors.go is the file every parallel role slice touches.
var ErrDependentsOutsideRequest = errors.New("dependents outside deletion set")

// DependentsOutsideRequestError reports WHICH row was blocked and by WHAT, so
// a caller can name them without parsing the message. It wraps the sentinel
// rather than replacing it.
//
// It names ONE blocked row — the first the implementation reached — rather
// than every blocked row in the request. That is deliberate and matches the
// only answer a caller can act on: the request is refused whole, so the second
// blocked row changes nothing about what to do next, and enumerating all of
// them would cost a full scan on the path that deletes nothing.
type DependentsOutsideRequestError struct {
	// IssueID is the named row that has dependents outside the request.
	IssueID string
	// Dependents are that row's direct dependents that the request did not
	// name, in ascending id order.
	Dependents []string
}

func (e *DependentsOutsideRequestError) Error() string {
	return fmt.Sprintf("issue %s has dependents not in deletion set; use --cascade to delete them or --force to orphan them", e.IssueID)
}

// Unwrap makes DependentsOutsideRequestError match ErrDependentsOutsideRequest.
func (e *DependentsOutsideRequestError) Unwrap() error { return ErrDependentsOutsideRequest }

// NotFoundError reports which of a request's ids named no stored row. It wraps
// ErrNotFound, so a caller that only wants to classify the failure still can.
//
// It names EVERY missing id rather than the first, unlike the dependents
// refusal above, because this one is a typo report: a caller handed a list and
// wants the whole list of what did not resolve, and the ids are already in
// hand from the probe that found them missing.
type NotFoundError struct {
	// IDs are the requested ids that named no row in either plane, in the
	// order the request spelled them.
	IDs []string
}

func (e *NotFoundError) Error() string {
	return "issues not found: " + strings.Join(e.IDs, ", ")
}

// Unwrap makes NotFoundError match ErrNotFound.
func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// Deleter describes erasure of NAMED rows — the capability behind
// `bd delete` — and, like every other capability here, a role with its own
// accessor. A new capability gets a new role interface and its own accessor;
// never append a method here.
//
// IT IS A DIFFERENT QUESTION FROM ITS NEIGHBORS.
//
//   - Sweeper erases a SET the caller described: closed rows of one tier,
//     narrowed by a cutoff and a glob. This erases rows the caller NAMED. The
//     difference decides where the safety lives — a description can be
//     accidentally too wide, so Sweeper carries a require-a-filter gate; a
//     list of ids cannot, so this one's guard is about the graph instead.
//   - Lifecycle closes ONE issue and leaves the row. A closed row is still
//     readable, still cited, still in a report. This one leaves nothing, which
//     is why its unforced mode refuses rather than warns.
//   - DependencyEditor removes EDGES. This removes edges as a consequence of
//     removing their endpoints, and the two must not be composed by a caller
//     to imitate it: unlink-then-delete is two transactions, and the window
//     between them is a graph that has already lost its edges and still has
//     the row.
//
// WRITES, AND THE SECOND DESTRUCTIVE ROLE IN THIS PACKAGE. Nothing it deletes
// comes back. Its completion fires no hook, for the reason Sweeper's does not:
// internal/hooks publishes on_create, on_update and on_close and none of them
// names a deletion.
//
// Deterministic request-validation failures match ErrValidation. Result values
// are unspecified when error is non-nil.
type Deleter interface {
	// Delete erases req.IDs and, under Cascade, everything that depends on
	// them, and reports what it did.
	//
	// IT IS ONE TRANSACTION, and on this role that covers one thing it did not
	// cover before: THE TEXT-REFERENCE REWRITE. The direct CLI route deleted
	// the rows in a transaction and then rewrote the neighbors' text in
	// separate statements afterwards, so a failure between the two left a
	// workspace whose rows were gone and whose descriptions still pointed at
	// them by id. Here the existence probe, the guard, the cascade expansion,
	// the deletion and the rewrite all see one snapshot and land or fail
	// together.
	//
	// WHAT THAT COSTS, said out loud because it is a real trade: the
	// transaction is now as large as the deletion PLUS its neighborhood. A
	// request whose neighbors are numerous enough to exceed a backend's write
	// timeout fails whole and deletes nothing, where the split version would
	// have deleted the rows and left the text stale. That is the better of the
	// two failures, but it is a ceiling: a caller deleting a very large set
	// splits the request rather than expecting progress.
	//
	// WHICH ROWS GET REWRITTEN is the deletion set's GRAPH NEIGHBORS — the
	// surviving rows joined to a deleted row by a dependency edge in either
	// direction — and not the workspace. Each occurrence of a deleted id in a
	// neighbor's description, notes, design or acceptance criteria becomes
	// `[deleted:<id>]`, matched at ASCII word boundaries the way
	// SweepRequest.ProtectReferenced matches: `be-1` rewrites in "see (be-1)."
	// and not inside `xbe-1` or `be-12`. A row that cites a deleted id in
	// prose WITHOUT an edge to it is left alone, because a workspace-wide text
	// scan on every delete is a cost this operation does not take.
	//
	// THE ORDER THE REFUSALS HAPPEN IN IS PART OF THE ANSWER, because a
	// request can fail two ways at once: request validation, then the
	// existence probe, then the dependents guard. `bd delete typo real-with-
	// dependents` reports the TYPO, which is the one the caller can fix
	// without deciding anything.
	//
	// REFUSALS:
	//
	//   - an empty or all-blank IDs, and an IDs containing a blank entry:
	//     ErrValidation, before anything is read;
	//   - an id naming no row in either plane: *NotFoundError, matching
	//     ErrNotFound;
	//   - without Cascade and without Force, a named row with a dependent the
	//     request did not name: *DependentsOutsideRequestError, matching
	//     ErrDependentsOutsideRequest.
	//
	// EVERY ONE OF THEM DELETES NOTHING, under DryRun and otherwise.
	//
	// A DRY RUN CHANGES NOTHING, including history: an implementation that
	// records a version-control entry for a deletion records none for a dry
	// run. Where an implementation versions at all, one call records AT MOST
	// ONE entry — a deletion is one act, not one per row.
	Delete(ctx context.Context, req DeleteRequest) (DeleteResult, error)
}
