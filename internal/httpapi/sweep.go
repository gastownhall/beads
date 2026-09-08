package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// The request body's member vocabulary. The schema is
// additionalProperties: false, so anything else is refused BY NAME — the same
// posture the unknown-query-parameter rule takes, and on this operation for a
// sharper reason: a narrowing term the server silently ignored would widen
// what is erased.
const (
	sweepTierMember              = "tier"
	sweepActorMember             = "actor"
	sweepClosedBeforeMember      = "closed_before"
	sweepPatternMember           = "pattern"
	sweepProtectReferencedMember = "protect_referenced"
	sweepDryRunMember            = "dry_run"
)

// sweepMembers is the whole vocabulary, in one place, so the unknown-member
// refusal and the decoding below cannot come to disagree about what this
// operation accepts.
var sweepMembers = []string{
	sweepTierMember,
	sweepActorMember,
	sweepClosedBeforeMember,
	sweepPatternMember,
	sweepProtectReferencedMember,
	sweepDryRunMember,
}

// handleSweep answers POST /v0/beads/issues:sweep — one of the two DESTRUCTIVE
// operations on this surface, the other being issues:delete.
//
// WHAT THIS HANDLER DOES NOT DO. It does not decide which beads are closed,
// does not match the glob, does not recheck closed_at, does not protect pinned
// beads, and — the one that matters most — does not implement the
// require-a-filter safety gate. All of that is issueops.Sweeper, the same
// library surface `bd prune` calls, so this endpoint could not erase every
// closed bead in a workspace by omission even if a future edit here forgot the
// rule existed. With the gate in the CLI handler instead, a second front door
// would be one handler away from an unguarded mass delete.
//
// Everything above the role here is argument validation: the media type, the
// body shape, and the six members the document publishes.
//
// NO ACTOR IS INFERRED, for the reason the claim gives: the server's own
// identity is meaningless to a remote caller. Unlike the claim, the actor is
// OPTIONAL here — a deleted bead leaves no row to attribute the deletion on —
// and it is validated by the same rules when present, because it reaches the
// same commit-message interpolation.
func (s *Server) handleSweep(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.sweepRequest(w, r)
	if !ok {
		return
	}

	sweeper, err := s.sweeper(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := sweeper.Sweep(r.Context(), request)
	if err != nil {
		s.failSweepErr(w, r, err)
		return
	}
	writeJSON(w, sweepResponse(result))
}

// sweepRequest decodes the body into the role's request, member by member.
//
// Member by member rather than straight into apigen.SweepRequest so that every
// refusal can NAME the member it is about: unmarshaling the generated struct
// reports a type mismatch only inside an error string, and this endpoint
// exists so clients can stop parsing prose.
func (s *Server) sweepRequest(w http.ResponseWriter, r *http.Request) (issueops.SweepRequest, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.SweepRequest{}, false
	}

	var unknown []string
	for name := range members {
		if !slices.Contains(sweepMembers, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		// One offender, chosen deterministically so a client dispatching on
		// `param` never sees it depend on map order.
		offender := slices.Min(unknown)
		requestInfo(r.Context()).refuse(offender)
		s.fail(w, r, InvalidArgument(offender, ReasonUnknownParameter,
			"this operation's request body carries "+sweepMemberList()+" and nothing else"))
		return issueops.SweepRequest{}, false
	}

	var request issueops.SweepRequest
	// protect_referenced DEFAULTS ON over HTTP, and the default is set here
	// rather than left to the zero value on purpose.
	//
	// This is the only destructive operation on the surface, and a configured
	// bearer is not a per-caller right: one shared token admits a client to
	// everything published here, so being authenticated says nothing about
	// whether this particular deletion was meant. A remote caller that omits
	// the member must not get weaker protection than the operator typing `bd
	// prune`, which protects unless --ignore-references is passed. Leaving the
	// zero value would have inverted exactly that: locally you opt OUT of
	// protection, remotely you had to opt IN.
	//
	// The cost is a full scan of the not-done set and its comments. A caller
	// that wants the cheaper sweep asks for it by sending
	// `protect_referenced: false`.
	request.ProtectReferenced = true

	raw, ok := members[sweepTierMember]
	if !ok {
		s.fail(w, r, InvalidArgument(sweepTierMember, ReasonInvalidValue,
			"`"+sweepTierMember+"` is required and has no default"))
		return issueops.SweepRequest{}, false
	}
	var tier *string
	if err := json.Unmarshal(raw, &tier); err != nil || tier == nil {
		s.fail(w, r, InvalidArgument(sweepTierMember, ReasonInvalidValue,
			"`"+sweepTierMember+"` must be a string"))
		return issueops.SweepRequest{}, false
	}
	// The enum check uses the GENERATED validator, which is derived from the
	// document rather than a second hand-written copy of the vocabulary. The
	// role refuses an unrecognized tier too; this is here so the refusal can
	// name the member.
	if !apigen.SweepRequestTier(*tier).Valid() {
		s.fail(w, r, InvalidArgument(sweepTierMember, ReasonInvalidValue,
			"`"+sweepTierMember+"` must be \"ephemeral\" or \"durable\""))
		return issueops.SweepRequest{}, false
	}
	request.Tier = issueops.SweepTier(*tier)

	if raw, ok := members[sweepActorMember]; ok {
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			s.fail(w, r, InvalidArgument(sweepActorMember, ReasonInvalidValue,
				"`"+sweepActorMember+"` must be a string"))
			return issueops.SweepRequest{}, false
		}
		// The claim's rules, unchanged: trim, then refuse empty, over-long and
		// any control character. `param` reads "actor" there too, so the two
		// operations are one vocabulary for a client.
		trimmed, res := validateActor(*value)
		if res != nil {
			s.fail(w, r, *res)
			return issueops.SweepRequest{}, false
		}
		request.Actor = trimmed
	}

	if raw, ok := members[sweepClosedBeforeMember]; ok {
		var value *time.Time
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			s.fail(w, r, InvalidArgument(sweepClosedBeforeMember, ReasonInvalidValue,
				"`"+sweepClosedBeforeMember+"` must be an RFC 3339 timestamp"))
			return issueops.SweepRequest{}, false
		}
		request.ClosedBefore = value
	}

	if raw, ok := members[sweepPatternMember]; ok {
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			s.fail(w, r, InvalidArgument(sweepPatternMember, ReasonInvalidValue,
				"`"+sweepPatternMember+"` must be a string"))
			return issueops.SweepRequest{}, false
		}
		// A malformed glob is NOT refused here. The role refuses it, and
		// routing it through the role is what keeps one definition of what a
		// pattern is: filepath.Match's, matched in Go on both front doors.
		request.IDPattern = *value
	}

	for _, flag := range []struct {
		member string
		dest   *bool
	}{
		{sweepProtectReferencedMember, &request.ProtectReferenced},
		{sweepDryRunMember, &request.DryRun},
	} {
		raw, ok := members[flag.member]
		if !ok {
			continue
		}
		var value *bool
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			s.fail(w, r, InvalidArgument(flag.member, ReasonInvalidValue,
				"`"+flag.member+"` must be a boolean"))
			return issueops.SweepRequest{}, false
		}
		*flag.dest = *value
	}

	return request, true
}

func sweepMemberList() string {
	quoted := make([]string, len(sweepMembers))
	for i, name := range sweepMembers {
		quoted[i] = "`" + name + "`"
	}
	return strings.Join(quoted, ", ")
}

// failSweepErr answers a failed sweep.
//
// issueops.ErrValidation is mapped to a 400 HERE rather than in ClassifyError,
// because this operation's ROLE performs request validation the handler does
// not duplicate — the require-a-filter gate, the tier vocabulary and the glob
// are all refused below the wire. Delete, tree, edges, blocking and batch-create
// each draw the same line in their own handler, deliberately in the same shape.
// Widening ClassifyError instead would change what every other operation
// returns for an error it has never produced.
func (s *Server) failSweepErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, issueops.ErrValidation) {
		// No `param`: the refusal is about the REQUEST rather than one member
		// of it — an unfiltered durable sweep is two absent members at once —
		// and the document's `param` is documented absent on exactly that
		// case. The detail carries the role's own sentence, which names what
		// to send instead.
		s.fail(w, r, InvalidArgument("", ReasonInvalidValue, err.Error()))
		return
	}
	s.failErr(w, r, err)
}

// sweepResponse projects the role's result onto the wire type. It is a field
// list rather than an alias because SweepResult is deliberately not
// x-go-type-pinned: there is no canonical Go struct whose JSON encoding is
// this body (see the schema's own description), so the projection is where the
// two shapes are held together and TestSweepResponseCarriesEveryRoleField is
// what keeps a new result field from being dropped here in silence.
func sweepResponse(result issueops.SweepResult) apigen.SweepResult {
	body := apigen.SweepResult{
		DryRun:       result.DryRun,
		Swept:        result.Swept,
		Dependencies: result.Dependencies,
		Labels:       result.Labels,
		Events:       result.Events,
		Skipped: apigen.SweepSkips{
			Pinned:                result.Skipped.Pinned,
			Referenced:            result.Skipped.Referenced,
			NotClosed:             result.Skipped.NotClosed,
			UnknownClosedAt:       result.Skipped.UnknownClosedAt,
			ClosedAtOrAfterCutoff: result.Skipped.ClosedAtOrAfterCutoff,
			Unreadable:            result.Skipped.Unreadable,
		},
	}
	if len(result.ReferencedIDs) > 0 {
		ids := append([]string(nil), result.ReferencedIDs...)
		body.ReferencedIds = &ids
	}
	return body
}
