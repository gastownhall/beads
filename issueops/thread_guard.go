package issueops

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ThreadGuard is DeleteRequest's precondition for erasing archived mail
// threads: the delete proceeds only if every thread the caller archived is
// still exactly what it saw, and otherwise NOTHING IS DELETED and the request
// fails with a *ThreadGuardMismatchError naming every thread and check that
// failed.
//
// It exists for archive-then-delete: a caller images a thread, ships the
// images somewhere durable, then deletes the thread. Between the archive and
// the delete a reply can land, a member can be edited, or a member can be
// reopened; deleting then would destroy what the archive never saw. The guard is checked INSIDE the deleting transaction, before any row is
// deleted or rewritten, so the archive and the deletion are compared against
// one snapshot.
//
// FOUR CHECKS, all run, every failure reported:
//
//   - membership: the connected component of each thread's Root over Edge
//     dependencies, followed in either direction across both planes and
//     skipping beads labelled ExcludeLabel, is exactly the thread's member
//     id set. A reply from outside the archived set fails it, and so does a
//     member that is no longer connected.
//   - revision: each member's current image revision (Image.Revision) equals
//     the revision the caller archived — delete targets and survivors alike,
//     because a survivor's image is part of the archive too.
//   - eligibility: each member carries MemberLabel, lives in the wisps plane,
//     is closed, and was closed before Cutoff.
//   - coverage: every id the request names that carries MemberLabel is a
//     member of some guarded thread, so a thread member cannot be deleted
//     unarchived by riding along in a guarded request.
//
// Ids the request names that do not carry MemberLabel are not guarded.
//
// ThreadGuard WITH Cascade IS ErrValidation: a cascade deletes a closure the
// caller did not archive or list, which is exactly what the guard is there to
// rule out. DryRun runs the same guard in its read transaction and fails the
// same way, so a clean preview means the real run's guard will pass against
// the same rows.
type ThreadGuard struct {
	// Cutoff is the instant every member must have closed before.
	Cutoff time.Time `json:"cutoff"`
	// MemberLabel marks a bead as thread mail. It is REQUIRED.
	MemberLabel string `json:"member_label"`
	// ExcludeLabel marks beads the membership walk neither includes nor walks
	// through. Empty excludes nothing.
	ExcludeLabel string `json:"exclude_label"`
	// Edge is the dependency type that joins a thread, e.g. "replies-to". It is
	// REQUIRED.
	Edge string `json:"edge"`
	// Threads are the archived threads. At least one is REQUIRED.
	Threads []GuardedThread `json:"threads"`
}

// GuardedThread is one archived thread: its root and every member the caller
// imaged, root included.
type GuardedThread struct {
	Root    string          `json:"root"`
	Members []GuardedMember `json:"members"`
}

// GuardedMember is one archived bead and the revision of its image when the
// caller archived it.
type GuardedMember struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// The checks a ThreadGuardFailure names.
const (
	ThreadGuardMembership  = "membership"
	ThreadGuardRevision    = "revision"
	ThreadGuardEligibility = "eligibility"
	ThreadGuardCoverage    = "coverage"
)

// ThreadGuardFailure is one failed check on one thread. IDs are the beads that
// failed it, sorted: for membership the ids reached but not archived and the
// archived ids not reached; for revision the members whose image changed or is
// gone; for eligibility the members that no longer qualify. A coverage failure
// belongs to no thread, so its Root is empty and its IDs are the requested
// MemberLabel beads no guarded thread lists.
type ThreadGuardFailure struct {
	Root  string   `json:"root"`
	Check string   `json:"check"`
	IDs   []string `json:"ids"`
}

// ErrThreadGuardMismatch is what a *ThreadGuardMismatchError matches, so a
// caller can classify the refusal with errors.Is beside ErrVersionMismatch,
// ErrStatusMismatch and ErrAssigneeMismatch.
var ErrThreadGuardMismatch = errors.New("thread guard mismatch")

// ThreadGuardMismatchError is the refusal of a delete whose ThreadGuard failed.
// Failed lists every failure, threads in guard order and checks in the order
// above, coverage last.
type ThreadGuardMismatchError struct {
	Failed []ThreadGuardFailure `json:"failed"`
}

func (e *ThreadGuardMismatchError) Error() string {
	parts := make([]string, 0, len(e.Failed))
	for _, f := range e.Failed {
		subject := "thread " + f.Root
		if f.Root == "" {
			subject = "request"
		}
		parts = append(parts, fmt.Sprintf("%s: %s (%s)", subject, f.Check, strings.Join(f.IDs, ", ")))
	}
	return ErrThreadGuardMismatch.Error() + ": " + strings.Join(parts, "; ")
}

// Unwrap makes ThreadGuardMismatchError match ErrThreadGuardMismatch.
func (e *ThreadGuardMismatchError) Unwrap() error { return ErrThreadGuardMismatch }
