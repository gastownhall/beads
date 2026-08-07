package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// The request body's member vocabulary. The schema is
// additionalProperties: false, so anything else is refused BY NAME: on this
// operation an ignored member is the difference between orphaning a dependent
// and deleting it.
const (
	deleteIDsMember     = "ids"
	deleteActorMember   = "actor"
	deleteCascadeMember = "cascade"
	deleteForceMember   = "force"
	deleteDryRunMember  = "dry_run"
)

// deleteMembers is the whole vocabulary, in one place, so the unknown-member
// refusal and the decoding below cannot disagree about what this operation
// accepts.
var deleteMembers = []string{
	deleteIDsMember,
	deleteActorMember,
	deleteCascadeMember,
	deleteForceMember,
	deleteDryRunMember,
}

// maxDeleteIDs bounds the `ids` array, matching the document's maxItems. It
// bounds the REQUEST rather than what a cascade expands to: the whole delete is
// one transaction, so the practical ceiling is the backend's write timeout.
const maxDeleteIDs = 1000

// handleDelete answers POST /v0/beads/issues:delete — the second DESTRUCTIVE
// operation on this surface.
//
// WHAT THIS HANDLER DOES NOT DO is the point of it, as for the sweep. It does
// not resolve ids, does not expand a cascade, does not decide which rows are
// dependents, and — the one that matters — does not implement the guard that
// refuses an unforced delete over an outside dependent. All of that is
// issueops.Deleter, the same library surface `bd delete` calls. Everything
// above the role here is argument validation.
//
// NO ACTOR IS INFERRED, for the reason the claim gives. It is OPTIONAL here as
// it is on the sweep — a deleted bead leaves no row to attribute the deletion
// on — and validated by the same rules when present, because it reaches the
// same commit-message interpolation AND the surviving rows this operation
// rewrites.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.deleteRequest(w, r)
	if !ok {
		return
	}

	deleter, err := s.deleter(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := deleter.Delete(r.Context(), request)
	if err != nil {
		s.failDeleteErr(w, r, err)
		return
	}
	writeJSON(w, deleteResponse(result))
}

// deleteRequest decodes the body into the role's request, member by member, so
// that every refusal can NAME the member it is about.
func (s *Server) deleteRequest(w http.ResponseWriter, r *http.Request) (issueops.DeleteRequest, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.DeleteRequest{}, false
	}

	var unknown []string
	for name := range members {
		if !slices.Contains(deleteMembers, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		// One offender, chosen deterministically so a client dispatching on
		// `param` never sees it depend on map order.
		offender := slices.Min(unknown)
		requestInfo(r.Context()).refuse(offender)
		s.fail(w, r, InvalidArgument(offender, ReasonUnknownParameter,
			"this operation's request body carries "+deleteMemberList()+" and nothing else"))
		return issueops.DeleteRequest{}, false
	}

	// cascade, force and dry_run all default to their ZERO values here, unlike
	// the sweep's protect_referenced: false is already the guarded answer for
	// all three, so an omitted member cannot buy weaker protection than the
	// operator typing `bd delete` gets.
	var request issueops.DeleteRequest

	raw, ok := members[deleteIDsMember]
	if !ok {
		s.fail(w, r, InvalidArgument(deleteIDsMember, ReasonInvalidValue,
			"`"+deleteIDsMember+"` is required"))
		return issueops.DeleteRequest{}, false
	}
	var ids *[]string
	if err := json.Unmarshal(raw, &ids); err != nil || ids == nil {
		s.fail(w, r, InvalidArgument(deleteIDsMember, ReasonInvalidValue,
			"`"+deleteIDsMember+"` must be an array of strings"))
		return issueops.DeleteRequest{}, false
	}
	if len(*ids) == 0 {
		// The ROLE refuses this too. It is refused here as well so the client
		// gets the member name.
		s.fail(w, r, InvalidArgument(deleteIDsMember, ReasonInvalidValue,
			"`"+deleteIDsMember+"` must name at least one bead"))
		return issueops.DeleteRequest{}, false
	}
	if len(*ids) > maxDeleteIDs {
		s.fail(w, r, InvalidArgument(deleteIDsMember, ReasonInvalidValue,
			"`"+deleteIDsMember+"` carries more ids than this operation accepts in one request"))
		return issueops.DeleteRequest{}, false
	}
	request.IDs = *ids

	if raw, ok := members[deleteActorMember]; ok {
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			s.fail(w, r, InvalidArgument(deleteActorMember, ReasonInvalidValue,
				"`"+deleteActorMember+"` must be a string"))
			return issueops.DeleteRequest{}, false
		}
		// The claim's rules, unchanged.
		trimmed, res := validateActor(*value)
		if res != nil {
			s.fail(w, r, *res)
			return issueops.DeleteRequest{}, false
		}
		request.Actor = trimmed
	}

	for _, flag := range []struct {
		member string
		dest   *bool
	}{
		{deleteCascadeMember, &request.Cascade},
		{deleteForceMember, &request.Force},
		{deleteDryRunMember, &request.DryRun},
	} {
		raw, ok := members[flag.member]
		if !ok {
			continue
		}
		var value *bool
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			s.fail(w, r, InvalidArgument(flag.member, ReasonInvalidValue,
				"`"+flag.member+"` must be a boolean"))
			return issueops.DeleteRequest{}, false
		}
		*flag.dest = *value
	}

	return request, true
}

func deleteMemberList() string {
	quoted := make([]string, len(deleteMembers))
	for i, name := range deleteMembers {
		quoted[i] = "`" + name + "`"
	}
	return strings.Join(quoted, ", ")
}

// failDeleteErr answers a failed delete.
//
// It draws the same ErrValidation-is-a-400 line the sweep draws, and adds ONE
// more: the role's dependents refusal. That one is a 400 rather than a 409
// because the fix is to change the REQUEST — send `cascade` or `force`.
//
// THE ABSENT-ID REFUSAL NEEDS NO BRANCH AT ALL, and does not get one: the
// role's *NotFoundError wraps issueops.ErrNotFound, which ClassifyError already
// maps to a 404 carrying NotFound()'s FIXED detail. The role's own message
// names every id that did not resolve and the wire deliberately does not repeat
// it — NotFound's doc says why. `bd delete` still names them, because it is
// talking to the person who typed them.
func (s *Server) failDeleteErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, issueops.ErrValidation) || errors.Is(err, issueops.ErrDependentsOutsideRequest) {
		// No `param`: neither refusal is about one member of the request. The
		// dependents one is about the absence of a CHOICE between two of them.
		s.fail(w, r, InvalidArgument("", ReasonInvalidValue, err.Error()))
		return
	}
	s.failErr(w, r, err)
}

// deleteResponse projects the role's result onto the wire type. It is a field
// list rather than an alias for the reason sweepResponse is: DeleteResult is
// deliberately not x-go-type-pinned, and TestDeleteResponseCarriesEveryRoleField
// is what keeps a new result field from being dropped here in silence.
func deleteResponse(result issueops.DeleteResult) apigen.DeleteIssuesResult {
	body := apigen.DeleteIssuesResult{
		DryRun:            result.DryRun,
		Deleted:           result.Deleted,
		Dependencies:      result.Dependencies,
		Labels:            result.Labels,
		Events:            result.Events,
		ReferencesUpdated: result.ReferencesUpdated,
	}
	if len(result.Orphaned) > 0 {
		ids := append([]string(nil), result.Orphaned...)
		body.Orphaned = &ids
	}
	return body
}
