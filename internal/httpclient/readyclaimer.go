// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/readyclaimer.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	storageops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// readyClaimPage is how many ready rows one DOWN-LEVEL attempt looks at.
//
// The local role does this in ONE transaction with an unbounded scan; a server
// too old to advertise issues.claimNext leaves the composition below no choice
// but a window, and the choice is between wasted rows and wasted round trips.
// Twenty-five is a page a contended front survives — losing all of it takes
// twenty-five simultaneous winners — while staying an order of magnitude under
// the server's own default ready limit, so a claim never drags a full listing
// across the wire.
const readyClaimPage = 25

// httpReadyClaimer serves issueops.ReadyClaimer, on the wire's claimNext
// operation where the server advertises it and by composing a listing and a
// claim where it does not (design D8 row 4).
//
// WHERE THE WIRE CARRIES issues.claimNext, the whole role is one call: the
// filter travels as the query string GET /v0/beads/ready's own decode reads, the
// actor travels as the body, and the server's role does the selection, the
// compare-and-set and the hydration inside ONE transaction. That is the
// operation this role was always waiting for — every clause of the contract that
// the composition could only approximate is simply true of it.
//
// WHERE IT DOES NOT — a server older than upstream #5510 — the composition
// survives unchanged, because `bd ready --claim` worked against those servers
// before this port and refusing it now would be a regression dressed as
// progress. It is the same posture BatchCloser takes toward issues.batchClose,
// and it keeps L14 describing shipped behavior rather than history: the three
// residues are the DOWN-LEVEL leg's, and none of them survives on the served
// one.
//
//   - the ready-at-fetch / claim-at-dial window: a row that gained a blocker
//     between the listing and the claim can still be claimed, which the local
//     one-transaction role can never do;
//   - a false empty under contention: after the bounded refetch below, a front
//     that was never actually empty is reported as lost races — an ERROR
//     distinct from the nil-Claimed nil-error answer an empty front earns, so a
//     polling agent is never told "nothing to do" about work it merely lost;
//   - the claimed row's cardinalities come from the READY PAGE rather than from
//     the claiming transaction. See the hydration note on composedClaim below.
type httpReadyClaimer struct {
	store *Store
	wire  WriteWire
}

var _ issueops.ReadyClaimer = (*httpReadyClaimer)(nil)

// ErrClaimRacesLost reports that every candidate this claim looked at was taken
// by someone else before it could take one. It is deliberately NOT the empty
// front: the queue had work, and re-running is the recovery.
var ErrClaimRacesLost = errors.New("every ready candidate was claimed by someone else")

// ClaimRacesLostError names how many races were lost and where, because "try
// again" without a count cannot be told from a broken claim path.
type ClaimRacesLostError struct {
	Actor     string
	Attempts  int
	ServerURL string
}

func (e *ClaimRacesLostError) Error() string {
	return fmt.Sprintf("lost %d races for ready work on bd serve at %s; re-run to take the next one",
		e.Attempts, e.ServerURL)
}

func (e *ClaimRacesLostError) Unwrap() error { return ErrClaimRacesLost }

// ClaimNext takes the next ready row the filter admits.
//
// The filter is the reader's own vocabulary and is encoded by the same table the
// listing uses on BOTH legs, so the claim asks exactly the question `bd ready`
// shows — an inexpressible member refuses there rather than widening the
// candidate set here.
func (r *httpReadyClaimer) ClaimNext(ctx context.Context, req issueops.ClaimNextRequest) (result issueops.ClaimNextResult, err error) {
	// Write-side parity with reads: decorates a bare *encode.RefusedError —
	// whichever leg raised it, serveClaimNext's encode.ClaimNextParams or
	// composeClaimNext's encode.ReadyParams — into *InexpressibleError so
	// errors.As(err, &unsupported) reaches *storage.ErrUnsupported, same as
	// inexpressible does for a read role. Both legs return directly from this
	// method's own return statements, so one defer here catches both.
	defer func() { err = r.store.inexpressible("ReadyClaimer.ClaimNext", err) }()
	// The role's own request rules, taken from the one place every ReadyClaimer
	// shares them rather than restated here, and run BEFORE the leg is chosen so
	// both legs refuse the same set. Restating them is how this leg drifted:
	// upstream added the Brief refusal to the shared validator and this copy
	// kept accepting a projection on a MUTATING call — the field whose whole job
	// is to tell a caller it got less than it asked for, answering that it got
	// everything.
	if err := storageops.ValidateClaimNextRequest(req); err != nil {
		return issueops.ClaimNextResult{}, err
	}

	served, err := r.servesClaimNext(ctx)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	if served {
		return r.serveClaimNext(ctx, req)
	}
	return r.composeClaimNext(ctx, req)
}

// servesClaimNext reports whether the server advertises issues.claimNext. It
// forces the one lazy handshake the store already owns and reads the cached
// capability list, so the check costs at most one round trip and none once the
// handshake has run — servesBatchClose's shape exactly, for the same question.
func (r *httpReadyClaimer) servesClaimNext(ctx context.Context) (bool, error) {
	snap, err := r.store.snapshot(ctx)
	if err != nil {
		return false, err
	}
	if snap == nil {
		return false, nil
	}
	token, _ := wire.CapabilityFor(wire.OpClaimNextIssue)
	return slices.Contains(snap.Capabilities, token), nil
}

// serveClaimNext sends the whole request on one issues:claimNext call.
//
// THE ANSWER IS THE ROLE'S RESULT ALREADY. `claimed` is pinned to
// types.IssueWithCounts by the document, hydrated inside the transaction that
// committed the claim, so it passes through unrewritten — no counts to stitch,
// no second read to make, and nothing for this client to recompute. A client
// that rebuilt the row here would be inventing a hydration the wire had already
// done correctly.
//
// AN ABSENT `claimed` IS A NORMAL OUTCOME and is the whole signal: there is no
// boolean beside it, so a drained front is a 200 whose body is `{}`. That
// decodes to a nil pointer, which is exactly what ClaimNextResult means by "nil
// Claimed, nil error, nothing written".
func (r *httpReadyClaimer) serveClaimNext(ctx context.Context, req issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	params, err := encode.ClaimNextParams(req.Filter)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	res, err := r.wire.ClaimNextIssue(ctx, params, apigen.ClaimNextRequest{Actor: req.Actor})
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}
	if err := checkServedClaimNext(res.Claimed); err != nil {
		return issueops.ClaimNextResult{}, err
	}
	return issueops.ClaimNextResult{Claimed: res.Claimed}, nil
}

// checkServedClaimNext holds a served `claimed` to the role contract, and it is
// ONE rule where batchClose's checkServedClaim is three.
//
// A CLAIM CARRYING NO ROW is the rule both share, and it is here for the harder
// of the two reasons that function gives. types.IssueWithCounts holds the row as
// an EMBEDDED POINTER, so an object carrying the cardinalities and none of the
// issue's own members decodes to a non-nil claim whose Issue is nil — a shape
// nothing on the wire tells apart from a claim that landed, and one nothing
// downstream is written to expect. Passing it through is not a wrong answer but
// a PANIC in the caller: `bd ready --claim` dereferences the claimed row's ID
// with no nil check, and the hook decorator above this store fires the
// workspace's on_update hook on the member's PRESENCE alone.
//
// THE OTHER TWO RULES DO NOT APPLY HERE, and the difference is the operation's
// shape rather than a laxer standard. batchClose refuses a claim the request
// never ASKED for and a claim for a batch that closed NOTHING, because there the
// claim is an optional rider on a different act: both preconditions are facts
// about the request and its outcomes, and both are checkable. This operation IS
// the claim — asking is what calling it means, and there is no other outcome for
// it to be earned by — so restating either would be asserting a tautology, and a
// rule that cannot fail is a rule that teaches a later reader the wrong thing
// about what is being checked.
//
// THE CLAIMED ID IS CHECKED AGAINST NOTHING, for exactly the reason it is not
// checked there: the answer names no id the caller enumerated. It names the next
// READY row, chosen by a predicate this client cannot evaluate without running
// the selection the operation exists to keep inside one transaction.
func checkServedClaimNext(claimed *types.IssueWithCounts) error {
	if claimed == nil {
		return nil
	}
	if claimed.Issue == nil {
		return fmt.Errorf("bd serve returned a claim carrying no issue")
	}
	return nil
}

// composeClaimNext is the DOWN-LEVEL leg: one listReadyWork page, then claimIssue
// down it until one lands. It is reached only when issues.claimNext is absent,
// and L14 is the ledger row for everything it cannot promise.
func (r *httpReadyClaimer) composeClaimNext(ctx context.Context, req issueops.ClaimNextRequest) (issueops.ClaimNextResult, error) {
	// A copy, because the request is the caller's and the page size is ours.
	filter := req.Filter
	page := readyClaimPage
	filter.Limit = &page
	params, err := encode.ReadyParams(filter)
	if err != nil {
		return issueops.ClaimNextResult{}, err
	}

	// At most one refetch, and only after a FULL page was lost end to end: a
	// short page means the front really is that short, so refetching it would
	// only re-read the rows that just refused. Two passes bound the work a
	// single ClaimNext can do against a server under contention.
	var lost int
	for attempt := 0; attempt < 2; attempt++ {
		ready, err := r.wire.ListReadyWork(ctx, params)
		if err != nil {
			return issueops.ClaimNextResult{}, err
		}
		if len(ready.Items) == 0 {
			// The steady state of a drained queue, and a normal outcome: nil
			// Claimed, nil error, nothing written.
			return issueops.ClaimNextResult{}, nil
		}

		for i := range ready.Items {
			row := ready.Items[i]
			if row.Issue == nil {
				continue
			}
			claimed, err := r.composedClaim(ctx, req.Actor, row)
			switch {
			case err == nil:
				return issueops.ClaimNextResult{Claimed: claimed}, nil
			case isLostRace(err):
				lost++
			default:
				return issueops.ClaimNextResult{}, err
			}
		}
		if len(ready.Items) < page {
			break
		}
	}
	return issueops.ClaimNextResult{}, &ClaimRacesLostError{
		Actor: req.Actor, Attempts: lost, ServerURL: r.store.target.String(),
	}
}

// composedClaim takes one candidate and assembles the hydrated row the role
// promises, for the down-level leg alone.
//
// HYDRATION. ClaimNextResult.Claimed is an issue plus its cardinalities, and the
// wire's ClaimResponse carries the bare row. The counts here therefore come from
// the ready page's own IssueWithCounts rather than from a second read: the issue
// is the POST-claim row the claim itself answered, and the counts are as of the
// listing. A claim changes no dependency, dependent or comment count, so the
// only staleness is the listing window L14's first residue already owns —
// whereas a follow-up getIssue would add a read that can fail AFTER the claim is
// durable, which is a worse answer to the same question.
func (r *httpReadyClaimer) composedClaim(ctx context.Context, actor string, row apigen.IssueWithCounts) (*types.IssueWithCounts, error) {
	res, err := r.wire.ClaimIssue(ctx, row.Issue.ID, apigen.ClaimRequest{Actor: actor})
	if err != nil {
		return nil, err
	}
	issue := res.Issue
	return &types.IssueWithCounts{
		Issue:           &issue,
		DependencyCount: row.DependencyCount,
		DependentCount:  row.DependentCount,
		CommentCount:    row.CommentCount,
		Parent:          row.Parent,
	}, nil
}

// isLostRace classifies the refusals that mean "someone else got there first",
// which the loop walks past rather than surfacing.
//
// ErrNotFound joins the two claim refusals deliberately: a row listed as ready
// and gone by the time it was dialed was deleted or promoted mid-loop, which is
// the same situation for a caller that just wants the next piece of work.
func isLostRace(err error) bool {
	return errors.Is(err, issueops.ErrAlreadyClaimed) ||
		errors.Is(err, issueops.ErrNotClaimable) ||
		errors.Is(err, issueops.ErrNotFound)
}
