package httpapi

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/issueops"
)

// The request body's member vocabulary. The schema is
// additionalProperties: false, so anything else is refused BY NAME, and on this
// operation for the sweep's sharper reason: a scope term the server silently
// ignored would widen what is reverted.
const (
	reclaimActorMember         = "actor"
	reclaimOlderThanMember     = "older_than_seconds"
	reclaimIDsMember           = "ids"
	reclaimAssigneesMember     = "assignees"
	reclaimLabelsMember        = "labels"
	reclaimLabelsAnyMember     = "labels_any"
	reclaimExcludeLabelsMember = "exclude_labels"
	reclaimAnyReplicaMember    = "any_replica"
)

// reclaimMembers is the whole vocabulary, in one place, so the unknown-member
// refusal and the decoding below cannot come to disagree about what this
// operation accepts.
var reclaimMembers = []string{
	reclaimActorMember,
	reclaimOlderThanMember,
	reclaimIDsMember,
	reclaimAssigneesMember,
	reclaimLabelsMember,
	reclaimLabelsAnyMember,
	reclaimExcludeLabelsMember,
	reclaimAnyReplicaMember,
}

// maxReclaimOlderThanSeconds is the smallest grace window time.Duration can
// NOT hold: MaxInt64/1e9 as a float64 rounds up, so converting it back yields
// 2^63 nanoseconds, which wraps negative. The check is >=, so every accepted
// value converts in range; a refused one is not wrapped into a negative
// duration the role would refuse with a message about a value never sent.
var maxReclaimOlderThanSeconds = float64(math.MaxInt64) / float64(time.Second)

// handleReclaimIssues answers POST /v0/beads/issues:reclaim.
//
// WHAT THIS HANDLER DOES NOT DO: it does not decide which lease is stale, does
// not apply the scope, does not honor the replica guard and does not mint the
// revisions. All of that is issueops.LeaseReclaimer, the same library surface
// `bd reclaim` calls, so a second front door cannot sweep differently from the
// first. Everything above the role here is argument validation and projection.
//
// THE ACTOR IS REQUIRED and validated by the claim's rules: every reverted row
// records a recovery event attributed to it, and the server's own identity
// would be meaningless to a remote reaper.
func (s *Server) handleReclaimIssues(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.reclaimRequest(w, r)
	if !ok {
		return
	}

	reclaimer, err := s.leaseReclaimer(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := reclaimer.Reclaim(r.Context(), request)
	if err != nil {
		s.failReclaimErr(w, r, err)
		return
	}
	writeJSON(w, reclaimResponse(result))
}

// reclaimRequest decodes the body into the role's request, member by member,
// so every refusal can NAME the member it is about.
func (s *Server) reclaimRequest(w http.ResponseWriter, r *http.Request) (issueops.ReclaimRequest, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.ReclaimRequest{}, false
	}

	var unknown []string
	for name := range members {
		if !slices.Contains(reclaimMembers, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		offender := slices.Min(unknown)
		requestInfo(r.Context()).refuse(offender)
		s.fail(w, r, InvalidArgument(offender, ReasonUnknownParameter,
			"this operation's request body carries "+reclaimMemberList()+" and nothing else"))
		return issueops.ReclaimRequest{}, false
	}

	var request issueops.ReclaimRequest

	raw, ok := members[reclaimActorMember]
	if !ok {
		s.fail(w, r, InvalidArgument(reclaimActorMember, ReasonInvalidValue,
			"`"+reclaimActorMember+"` is required"))
		return issueops.ReclaimRequest{}, false
	}
	var actor *string
	if err := json.Unmarshal(raw, &actor); err != nil || actor == nil {
		s.fail(w, r, InvalidArgument(reclaimActorMember, ReasonInvalidValue,
			"`"+reclaimActorMember+"` must be a string"))
		return issueops.ReclaimRequest{}, false
	}
	trimmed, res := validateActor(*actor)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.ReclaimRequest{}, false
	}
	request.Actor = trimmed

	if raw, ok := members[reclaimOlderThanMember]; ok {
		var seconds *float64
		if err := json.Unmarshal(raw, &seconds); err != nil || seconds == nil {
			s.fail(w, r, InvalidArgument(reclaimOlderThanMember, ReasonInvalidValue,
				"`"+reclaimOlderThanMember+"` must be a number"))
			return issueops.ReclaimRequest{}, false
		}
		if *seconds < 0 || *seconds >= maxReclaimOlderThanSeconds {
			s.fail(w, r, InvalidArgument(reclaimOlderThanMember, ReasonInvalidValue,
				"`"+reclaimOlderThanMember+"` must be a non-negative number of seconds a duration can hold"))
			return issueops.ReclaimRequest{}, false
		}
		request.OlderThan = time.Duration(*seconds * float64(time.Second))
	}

	for _, scope := range []struct {
		member string
		dest   *[]string
	}{
		{reclaimIDsMember, &request.Filter.IDs},
		{reclaimAssigneesMember, &request.Filter.Assignees},
		{reclaimLabelsMember, &request.Filter.Labels},
		{reclaimLabelsAnyMember, &request.Filter.LabelsAny},
		{reclaimExcludeLabelsMember, &request.Filter.ExcludeLabels},
	} {
		raw, ok := members[scope.member]
		if !ok {
			continue
		}
		var values *[]string
		if err := json.Unmarshal(raw, &values); err != nil || values == nil {
			s.fail(w, r, InvalidArgument(scope.member, ReasonInvalidValue,
				"`"+scope.member+"` must be an array of strings"))
			return issueops.ReclaimRequest{}, false
		}
		// A PRESENT, EMPTY scope is refused rather than read as "no scope".
		// The two would sweep the same rows, and that is the hazard: `bd
		// reclaim --label "$LANE"` with LANE unset refuses for exactly this
		// reason, so a caller that meant to narrow and sent nothing gets told
		// rather than getting a workspace-wide sweep.
		if len(*values) == 0 {
			s.fail(w, r, InvalidArgument(scope.member, ReasonInvalidValue,
				"`"+scope.member+"` is empty; an empty scope would reclaim everything, so omit the member to sweep without it"))
			return issueops.ReclaimRequest{}, false
		}
		// The id cap and every blank entry are the ROLE's refusals
		// (ValidateReclaimRequest, the one definition the CLI shares) and
		// reach the wire through failReclaimErr, which names the field the
		// role names.
		*scope.dest = *values
	}

	if raw, ok := members[reclaimAnyReplicaMember]; ok {
		var value *bool
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			s.fail(w, r, InvalidArgument(reclaimAnyReplicaMember, ReasonInvalidValue,
				"`"+reclaimAnyReplicaMember+"` must be a boolean"))
			return issueops.ReclaimRequest{}, false
		}
		request.Filter.AnyReplica = *value
	}

	return request, true
}

func reclaimMemberList() string {
	quoted := make([]string, len(reclaimMembers))
	for i, name := range reclaimMembers {
		quoted[i] = "`" + name + "`"
	}
	return strings.Join(quoted, ", ")
}

// reclaimFieldMembers spells each field the role's *ReclaimFieldError can
// name as the wire member it arrived in.
var reclaimFieldMembers = map[string]string{
	issueops.ReclaimFieldActor:         reclaimActorMember,
	issueops.ReclaimFieldOlderThan:     reclaimOlderThanMember,
	issueops.ReclaimFieldIDs:           reclaimIDsMember,
	issueops.ReclaimFieldAssignees:     reclaimAssigneesMember,
	issueops.ReclaimFieldLabels:        reclaimLabelsMember,
	issueops.ReclaimFieldLabelsAny:     reclaimLabelsAnyMember,
	issueops.ReclaimFieldExcludeLabels: reclaimExcludeLabelsMember,
}

// failReclaimErr answers a failed reclaim. issueops.ErrValidation is mapped to
// a 400 HERE, in the sweep's shape, because the role validates what the
// handler does not duplicate. The refusal names the member the role's error
// is about: `ids` for the cap, the *ReclaimFieldError's own field otherwise,
// and no member at all for a validation that names none.
func (s *Server) failReclaimErr(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, issueops.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	member := ""
	var capErr *issueops.TooManyReclaimIDsError
	var fieldErr *issueops.ReclaimFieldError
	switch {
	case errors.As(err, &capErr):
		member = reclaimIDsMember
	case errors.As(err, &fieldErr):
		member = reclaimFieldMembers[fieldErr.Field]
	}
	if member != "" {
		requestInfo(r.Context()).refuse(member)
	}
	s.fail(w, r, InvalidArgument(member, ReasonInvalidValue, err.Error()))
}

// reclaimResponse projects the role's result onto the wire envelope.
// apigen.ReclaimedLease IS types.ReclaimedLease (pinning.go), so this is a
// copy rather than a field list; `reclaimed` is never null.
func reclaimResponse(result issueops.ReclaimResult) apigen.ReclaimIssuesResult {
	body := apigen.ReclaimIssuesResult{Reclaimed: []apigen.ReclaimedLease{}}
	body.Reclaimed = append(body.Reclaimed, result.Reclaimed...)
	return body
}
