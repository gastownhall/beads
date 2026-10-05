// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/sweeper.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// httpSweeper serves issueops.Sweeper from the sweepIssues custom method
// (design D8 row 12) — the capability behind `bd purge` and `bd prune`.
//
// The mapping is TOTAL: every one of SweepRequest's six members has a wire
// member, and every one of SweepResult's fields is published, so nothing here
// refuses on shape and the ledger carries no W- row for this operation.
//
// WHAT IS NOT DECIDED HERE, deliberately: the require-a-filter gate, the glob's
// well-formedness and the tier's own predicate. All three are the ROLE's, they
// live below the server's handler, and the server routes this request through
// the same issueops.Sweeper a local workspace uses — so re-deciding any of them
// client-side would be a second definition of the rule that keeps a workspace's
// history from being erased by an omission. They come back as
// issueops.ErrValidation, which is what the role's contract promises.
//
// The ONE thing decided here is the tier VOCABULARY, because the tier is an
// enum on the wire and this client has to map it. An unrecognized tier refuses
// before the dial rather than as a 400, which is the rule every other write on
// this surface follows for a request its own contract calls invalid.
type httpSweeper struct {
	store *Store
	wire  WriteWire
}

var _ issueops.Sweeper = (*httpSweeper)(nil)

// Sweep dials POST /v0/beads/issues:sweep.
//
// BOTH BOOLEANS ARE SENT EXPLICITLY, and protect_referenced is the one that
// matters: the wire DEFAULTS it ON when the member is absent (an unauthenticated
// surface is where a default must be the guarded one), while the role's zero
// value is off. A client that omitted it would turn `bd prune
// --ignore-references` into a protected sweep and report the protection as
// skips the caller never asked for — a narrower answer than the request, which
// is the same failure class refuse-not-drop exists to stop, in the other
// direction.
func (s *httpSweeper) Sweep(ctx context.Context, req issueops.SweepRequest) (issueops.SweepResult, error) {
	tier := apigen.SweepRequestTier(req.Tier)
	if !tier.Valid() {
		return issueops.SweepResult{}, invalid("sweep tier %q is not %q or %q",
			string(req.Tier), apigen.Ephemeral, apigen.Durable)
	}

	body := apigen.SweepRequest{
		Tier:              tier,
		ProtectReferenced: &req.ProtectReferenced,
		DryRun:            &req.DryRun,
	}
	if strings.TrimSpace(req.Actor) != "" {
		// Omitted rather than sent blank: the role accepts an empty Actor — a
		// deleted row leaves nothing to attribute the deletion on — and the
		// server refuses an actor that is empty AFTER TRIMMING. The trim is the
		// whole test, not `!= ""`: a whitespace-only Actor is an accepted
		// request locally and would come back a 400 over http.
		body.Actor = &req.Actor
	}
	if req.IDPattern != "" {
		body.Pattern = &req.IDPattern
	}
	if req.ClosedBefore != nil {
		// Copied, never aliased: SweepRequest promises implementations never
		// write through a caller's pointer, and handing this one to a marshaler
		// is the kind of borrow that becomes a write when a helper is added.
		cutoff := *req.ClosedBefore
		body.ClosedBefore = &cutoff
	}

	res, err := s.wire.SweepIssues(ctx, body)
	if err != nil {
		return issueops.SweepResult{}, err
	}
	return sweepResult(res), nil
}

// sweepResult projects the wire's answer onto the role's.
//
// It is a field list rather than a cast because SweepResult is deliberately not
// x-go-type-pinned on the wire: there is no canonical Go struct whose JSON
// encoding is that body, so this is the one place the two shapes are held
// together. TestSweepResultCarriesEveryWireMember is what keeps a member the
// server grows from being dropped here in silence.
func sweepResult(res *apigen.SweepResult) issueops.SweepResult {
	out := issueops.SweepResult{
		DryRun:       res.DryRun,
		Swept:        res.Swept,
		Dependencies: res.Dependencies,
		Labels:       res.Labels,
		Events:       res.Events,
		Skipped: issueops.SweepSkips{
			Pinned:                res.Skipped.Pinned,
			Referenced:            res.Skipped.Referenced,
			NotClosed:             res.Skipped.NotClosed,
			UnknownClosedAt:       res.Skipped.UnknownClosedAt,
			ClosedAtOrAfterCutoff: res.Skipped.ClosedAtOrAfterCutoff,
			Unreadable:            res.Skipped.Unreadable,
		},
	}
	if res.ReferencedIds != nil {
		out.ReferencedIDs = append([]string(nil), *res.ReferencedIds...)
	}
	return out
}
