package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ReleaseIssueInTx is the store-backed body behind issueops.Releaser: the whole
// of `bd unclaim` from the classifying read to the post-state snapshot, inside
// ONE transaction.
//
// It lives here rather than in an importable internal/workapi/store<role>
// package for the reason CompareAndSetMetadataKeyInTx does: the work is a read,
// a guarded write and a second read that must all see one snapshot, and a
// transaction is not reachable through storage.DoltStorage. ALL THREE LEGS
// reach this function — the two stores wrap it in their own transaction and the
// unit of work reaches it through the domain issue repository — so the three
// wirings are one reading plus two wrapper checks.
//
// It assumes a request already refused by workapi.ValidateReleaseRequest. The
// accessors validate BEFORE opening a transaction, so a malformed request costs
// no database work.
//
// IT CLASSIFIES EXACTLY THE TWO REFUSALS THE RAW SEAM LEAVES UNTYPED, and no
// others. The raw entry points below refuse the same requests this role does,
// but two of those refusals are bare strings — "issue %s is not assigned" and,
// for a status the release transition is not defined over, "no matching row",
// which no caller can classify and the second of which explains nothing. The
// row is read here anyway for the post-state snapshot, so naming them costs a
// comparison each.
//
// THE ONES THE RAW SEAM ALREADY TYPES ARE NOT REPEATED. ErrNotOwner and
// ErrAssigneeMismatch come back from the calls below already wrapped and in
// this same order, so a pre-check for either is a branch nothing can make fail.
// Both were written, and both were deleted after a mutation removing them left
// the whole eleven-case contract green on the server-backed leg.
//
// THAT ONE LEG WAS SUFFICIENT EVIDENCE, and the reason is the paragraph above
// rather than a shortcut: all three legs run THIS function and it reaches the
// SAME two raw entry points, so there is no second implementation in which a
// deleted pre-check could still have been load-bearing. The engdocs' standing
// warning — "a mutation verdict is only true of the body you mutated" — is
// answered here by there being one body to mutate. Where the three legs DO
// differ is in what they version, which is why ReleaseWrite carries two facts
// and why the wisp case runs on all three.
//
// WHICH REQUESTS ARE REFUSED IS UNCHANGED BY EITHER DECISION. The checks here
// are the raw seam's own, in the raw seam's order, and the raw calls still run
// them — this body narrows nothing and widens nothing.
func ReleaseIssueInTx(ctx context.Context, tx DBTX, req publicops.ReleaseRequest) (publicops.ReleaseResult, ReleaseWrite, error) {
	before, err := GetIssueInTx(ctx, tx, req.IssueID)
	if err != nil {
		// GetIssueInTx already answers ErrNotFound for an id on neither plane,
		// which is the role's own vocabulary; wrapping it would put this
		// function's name in front of a sentinel every front door classifies.
		return publicops.ReleaseResult{}, ReleaseWrite{}, err
	}

	// STATUS FIRST, because a closed issue is not a claim question: telling a
	// caller "nothing holds this" about a row that was closed while still
	// assigned would send it looking for a reaper that never ran.
	if !releasableStatus(before.Status) {
		return publicops.ReleaseResult{}, ReleaseWrite{}, fmt.Errorf(
			"%w: %s has status %q, which is neither %q nor %q",
			publicops.ErrNotReleasable, req.IssueID, before.Status, types.StatusOpen, types.StatusInProgress)
	}

	// AN UNHELD ROW ON THE UNCONDITIONAL PATH is the second refusal this body
	// owns, and for the same reason as the first: the raw release answers it
	// with a bare string. It is only asked when the request named no expected
	// holder — a caller that DID name one gets ErrAssigneeMismatch from the raw
	// conditional release below, because it asked about a specific holder and
	// the answer is that it is not the holder.
	//
	// Force does not reach it. Force bypasses the ownership fence, and "is
	// there a claim here at all" is not a question about ownership.
	if req.ExpectedAssignee == nil && before.Assignee == "" {
		return publicops.ReleaseResult{}, ReleaseWrite{}, fmt.Errorf(
			"%w: %s has no assignee to release", publicops.ErrNotClaimed, req.IssueID)
	}

	// EVERY OTHER REFUSAL IS THE RAW SEAM'S, and is deliberately not repeated
	// here. The ownership fence and the assignee compare-and-set already answer
	// with ErrNotOwner and ErrAssigneeMismatch — typed, wrapped, and in this
	// same order — so a copy above would be a branch nothing could make fail:
	// the call below refuses the identical requests whether or not it is there.
	// Both copies WERE written, and both were deleted after a mutation removing
	// them left the whole contract green.
	if req.ExpectedAssignee != nil {
		err = UnclaimIssueIfAssigneeInTx(ctx, tx, req.IssueID, req.Actor, *req.ExpectedAssignee)
	} else {
		err = UnclaimIssueInTx(ctx, tx, req.IssueID, req.Actor, req.Force)
	}
	if err != nil {
		return publicops.ReleaseResult{}, ReleaseWrite{}, err
	}

	// The snapshot is READ FROM THE ROW inside this transaction rather than
	// composed from the request and the pre-state. A caller feeds
	// Issue.RowVersion straight back as the next operation's ExpectedVersion,
	// and a token this body invented would be a token no writer minted.
	after, err := GetIssueInTx(ctx, tx, req.IssueID)
	if err != nil {
		return publicops.ReleaseResult{}, ReleaseWrite{}, fmt.Errorf(
			"release %s: read back the released row: %w", req.IssueID, err)
	}

	// The release rewrites the issue row and records an event, in whichever
	// plane the row lives — asked the same way the raw seam routes its own
	// UPDATE, so the two cannot disagree about which tables a release touched.
	// ChangedTables drops the wisp tables on purpose, so an EPHEMERAL release
	// reports Wrote with an EMPTY table set: the distinction the
	// version-control legs need, and the one a single bool would lose.
	issueTable, _, eventTable, _ := WispTableRouting(IsActiveWispInTx(ctx, tx, req.IssueID))
	write := ReleaseWrite{Wrote: true, Tables: ChangedTables{}}
	write.Tables.Add(issueTable, eventTable)

	return publicops.ReleaseResult{Issue: after, Changed: true}, write, nil
}

// ReleaseWrite reports what one release did, in the two facts that are not the
// same question.
//
// A release of an EPHEMERAL row writes a row and changes no durable table,
// because the version-control plane ignores the wisp tables. A caller that read
// an empty table set as "nothing happened" would roll the write back — which is
// exactly what the unit-of-work leg did to a compare-and-set before
// MetadataCASWrite drew this same line.
type ReleaseWrite struct {
	// Wrote is true when the row was released. It is false only on the paths
	// that returned an error, so it exists for the table-set distinction above
	// rather than as a second verdict.
	Wrote bool
	// Tables are the DURABLE tables the release changed, for a caller that
	// stages them. It is empty for an ephemeral release.
	Tables ChangedTables
}

// releasableStatus mirrors the status predicate the raw release UPDATE is
// pinned to. It is a function rather than an inline comparison so the role's
// ErrNotReleasable refusal and the SQL that would otherwise refuse it silently
// cannot drift apart.
func releasableStatus(status types.Status) bool {
	return status == types.StatusOpen || status == types.StatusInProgress
}
