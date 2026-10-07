package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The request body's whole member vocabulary. The schema is
// additionalProperties: false, so anything else is refused BY NAME, the same
// discipline deleteMembers documents.
const batchGetIDsMember = "ids"

// batchGetMembers is the whole vocabulary, in one place, for batchGetMemberList
// below.
var batchGetMembers = []string{batchGetIDsMember}

// handleBatchGetIssues answers POST /v0/beads/issues:batchGet.
//
// WHAT THIS HANDLER DOES NOT DO is the point of it, as for delete and sweep.
// It does not decide which id is missing, does not pick a snapshot, and does
// not hydrate a row. All of that is issueops.BatchGetter, the same library
// surface gc's ready veto and the CLI's own id-resolution passes are meant to
// move onto (see the role's own doc for the migration this slice did not make
// — the HTTP client leg waits on the generator). Everything above the role
// here is argument validation and projection.
//
// NO ACTOR, NO VERSION GUARD, NO FLAGS: this operation reads and changes
// nothing, so it carries none of delete's write vocabulary. Its only member is
// `ids`.
func (s *Server) handleBatchGetIssues(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.batchGetRequest(w, r)
	if !ok {
		return
	}

	getter, err := s.batchGetter(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := getter.GetMany(r.Context(), request)
	if err != nil {
		s.failBatchGetErr(w, r, err)
		return
	}
	writeJSON(w, batchGetResponse(result))
}

// batchGetRequest decodes the body into the role's request.
//
// UNLIKE delete's handler, it enforces NEITHER the cap NOR the per-entry blank
// check itself. Both reach the wire through the role's own ErrValidation and
// failBatchGetErr below, and that is deliberate rather than an omission: `ids`
// is the ONLY member this operation has, so there is no second member a
// pre-check could need to disambiguate from, and checking here first would
// risk disagreeing with ValidateGetManyRequest's own order — the cap is
// checked BEFORE the blank-entry scan, against the request as sent — for a
// request that trips both. Letting the role answer keeps the wire's order
// identical to the library's, by construction.
func (s *Server) batchGetRequest(w http.ResponseWriter, r *http.Request) (issueops.GetManyRequest, bool) {
	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.GetManyRequest{}, false
	}

	var unknown []string
	for name := range members {
		if !slices.Contains(batchGetMembers, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		offender := slices.Min(unknown)
		requestInfo(r.Context()).refuse(offender)
		s.fail(w, r, InvalidArgument(offender, ReasonUnknownParameter,
			"this operation's request body carries "+batchGetMemberList()+" and nothing else"))
		return issueops.GetManyRequest{}, false
	}

	raw, ok := members[batchGetIDsMember]
	if !ok {
		s.fail(w, r, InvalidArgument(batchGetIDsMember, ReasonInvalidValue,
			"`"+batchGetIDsMember+"` is required"))
		return issueops.GetManyRequest{}, false
	}
	var ids *[]string
	if err := json.Unmarshal(raw, &ids); err != nil || ids == nil {
		s.fail(w, r, InvalidArgument(batchGetIDsMember, ReasonInvalidValue,
			"`"+batchGetIDsMember+"` must be an array of strings"))
		return issueops.GetManyRequest{}, false
	}

	return issueops.GetManyRequest{IDs: *ids}, true
}

func batchGetMemberList() string {
	quoted := make([]string, len(batchGetMembers))
	for i, name := range batchGetMembers {
		quoted[i] = "`" + name + "`"
	}
	return strings.Join(quoted, ", ")
}

// failBatchGetErr answers a failed batch get. The role's only refusal over the
// wire is ErrValidation — an oversized `ids` (*issueops.TooManyIDsError,
// counted before deduplication); there is no handler-side blank-entry
// pre-check to catch anything first, so EVERY validation failure, blank
// entries included, reaches the wire through this function, which is why
// batchGetRequest above enforces neither the cap nor the per-entry blank
// check itself. Nothing here is a 404 or a 409: a batch read reports a miss
// in the response body, it does not refuse the request over one, and this
// operation changes nothing to guard.
//
// UNLIKE failDeleteErr, it takes no request value: this operation has no
// version-mismatch case (it writes nothing) and only one member to blame, so
// there is nothing in the request this function would need to read.
func (s *Server) failBatchGetErr(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, issueops.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	requestInfo(r.Context()).refuse(batchGetIDsMember)
	s.fail(w, r, InvalidArgument(batchGetIDsMember, ReasonInvalidValue, err.Error()))
}

// batchGetResponse projects the role's result onto the wire envelope.
//
// Both members are non-nil so the body carries `[]` rather than `null` on an
// empty answer, the same promise wireEdges and deleteResponse's `orphaned`
// make. `issues` holds `apigen.BatchGetIssue`, each built by
// types.NewBatchGetIssue rather than a bare `apigen.Issue`: Issue.RowVersion
// is `json:"-"`, so the Issue body alone cannot carry the row's revision, and
// NewBatchGetIssue is the one projection that puts it on the wire the same
// way NewIssueDetails does for `GET /v0/beads/issues/{id}`.
// GetManyResult.Issues never carries a nil entry for a successful call, and a
// wire array of pointers would let json encode a `null` element the role's
// own contract forbids, so each entry is dereferenced before projection.
func batchGetResponse(result issueops.GetManyResult) apigen.BatchGetIssuesResult {
	body := apigen.BatchGetIssuesResult{
		Issues:  []apigen.BatchGetIssue{},
		Missing: []string{},
	}
	for _, issue := range result.Issues {
		if issue == nil {
			continue
		}
		body.Issues = append(body.Issues, types.NewBatchGetIssue(*issue))
	}
	body.Missing = append(body.Missing, result.Missing...)
	return body
}
