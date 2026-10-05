// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/sweeper.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/issueops"
)

// httpSweeper serves issueops.Sweeper from the sweepIssues custom method
// (design D8 row 12) — the capability behind `bd purge` and `bd prune`.
//
// S4 closed three gaps this client had against the local Sweeper role: the
// wire now carries `tier: "wisps-plane"`, `protect_live_dependents` and
// `limit` on the
// request, and `skipped.live_dependent`/`remaining` on the response — each
// behind its own behavior-capability token (CapSweepWispsPlane,
// CapSweepLiveDependents, CapSweepLimit), since each is a parameter added to
// an operation that already shipped. refuseUnservedSweep checks all three
// before the dial, exactly as counter.go's refuseUnservedScope does for
// Count's scope fields: a caller asking for something an older server
// predates learns that LOCALLY, never after a round trip that would 400
// anyway, and never by way of a silently unprotected or unbounded sweep.
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
func (s *httpSweeper) Sweep(ctx context.Context, req issueops.SweepRequest) (result issueops.SweepResult, err error) {
	// Write-side parity with reads: decorates a bare *encode.RefusedError into
	// *InexpressibleError so errors.As(err, &unsupported) reaches
	// *storage.ErrUnsupported, same as inexpressible does for a read role. No
	// Sweep path raises one today — refuseUnservedSweep's capability errors
	// pass through unchanged — so this is the write-role convention's single
	// defer, kept so the next refuse() here is decorated without a new one.
	defer func() { err = s.store.inexpressible("Sweeper.Sweep", err) }()
	tier := apigen.SweepRequestTier(req.Tier)
	if !tier.Valid() {
		return issueops.SweepResult{}, invalid("sweep tier %q is not %q, %q, or %q",
			string(req.Tier), apigen.Ephemeral, apigen.Durable, apigen.WispsPlane)
	}

	// refuseUnservedSweep is the pre-dial half of the three Sweep behavior
	// tokens (routes.go, beside CapIssuesSweepWispsPlane): a request naming
	// the wisps-plane tier, or setting ProtectLiveDependents or Limit, asks
	// for something only a server advertising the matching token answers, and
	// an older server predating it answers with a guaranteed 400
	// invalid_value/unknown_parameter. Checked BEFORE the dial so a caller
	// never pays for a round trip that 400s anyway, and never silently gets an
	// unprotected or unbounded sweep from a server too old to honor the
	// request as asked.
	if err := s.refuseUnservedSweep(ctx, tier, req); err != nil {
		return issueops.SweepResult{}, err
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
	if req.ProtectLiveDependents {
		body.ProtectLiveDependents = &req.ProtectLiveDependents
	}
	if req.Limit != 0 {
		limit := int64(req.Limit)
		body.Limit = &limit
	}

	res, err := s.wire.SweepIssues(ctx, body)
	if err != nil {
		return issueops.SweepResult{}, err
	}
	return sweepResult(res), nil
}

// refuseUnservedSweep checks each of the three S4 additions against the
// handshake snapshot, independently — a caller may ask for any subset of
// them, and an older server may serve none, some, or (not yet, but
// structurally possible) only some of the three. A request using none of
// them never consults the snapshot at all, the same short-circuit
// refuseUnservedScope uses for Count.
func (s *httpSweeper) refuseUnservedSweep(ctx context.Context, tier apigen.SweepRequestTier, req issueops.SweepRequest) error {
	if tier != apigen.WispsPlane && !req.ProtectLiveDependents && req.Limit == 0 {
		return nil
	}
	snap, err := s.store.snapshot(ctx)
	if err != nil {
		return err
	}
	// snap is nil, nil whenever Store.snapshot has no transport AND no cached
	// handshake (a Store built with a nil wire): nothing was ever advertised,
	// so every check below reads a nil capability list rather than
	// dereferencing a nil *apigen.ContextResponse.
	var capabilities []string
	if snap != nil {
		capabilities = snap.Capabilities
	}
	if tier == apigen.WispsPlane && !slices.Contains(capabilities, wire.CapSweepWispsPlane) {
		return s.store.unsupportedCapability("Sweeper.Sweep", wire.CapSweepWispsPlane)
	}
	if req.ProtectLiveDependents && !slices.Contains(capabilities, wire.CapSweepLiveDependents) {
		return s.store.unsupportedCapability("Sweeper.Sweep", wire.CapSweepLiveDependents)
	}
	if req.Limit != 0 && !slices.Contains(capabilities, wire.CapSweepLimit) {
		return s.store.unsupportedCapability("Sweeper.Sweep", wire.CapSweepLimit)
	}
	return nil
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
	if res.Skipped.LiveDependent != nil {
		out.Skipped.LiveDependent = *res.Skipped.LiveDependent
	}
	if res.Remaining != nil {
		out.Remaining = int(*res.Remaining)
	}
	if res.ReferencedIds != nil {
		out.ReferencedIDs = append([]string(nil), *res.ReferencedIds...)
	}
	return out
}
