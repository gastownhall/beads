package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The write side of the dependency graph: the wire adapters over
// issueops.DependencyEditor.
//
// They share the claim's posture exactly. The actor is caller-ASSERTED
// provenance and not authenticated identity; hooks do not fire and the
// per-command auto-commit machinery never runs. The only durable effect is the
// single storage commit the role makes inside its own transaction.
//
// Everything above the role here is argument validation: the media type, the
// body shape, the actor rules and the id bounds. The graph itself — the cycle
// gate, the hierarchy rule, the type conflict, the endpoint existence checks,
// the plane routing and the event stream — belongs to the role.

const (
	// maxAddDependencyEdges is the document's cap on `edges`. It bounds how
	// long one request may hold a write transaction — not batch semantics,
	// which have no size in them.
	maxAddDependencyEdges = 100
	// maxAddDependenciesBodyBytes bounds the request body. A hundred edges of
	// three short strings is the shape this has to admit, so it refuses the
	// absurd before any of it is parsed.
	maxAddDependenciesBodyBytes = 1 << 20
)

// The document's member lists at each level. The schemas are
// additionalProperties: false, so anything else is refused BY NAME, which is
// why the bodies are decoded as raw members first.
var (
	removeDependencyMembers = []string{"actor", "depends_on_id", "issue_id"}
	addDependenciesMembers  = []string{"actor", "edges"}
	dependencyEdgeMembers   = []string{"depends_on_id", "issue_id", "type"}
)

// handleAddDependencies asserts every edge in the request body, or none of
// them.
//
// The all-or-nothing transaction, the parent-child-first ordering, the
// whole-graph gate, the per-edge cycle probe, the plane routing and the whole
// refusal vocabulary belong to issueops.DependencyEditor. This function decodes
// a body, refuses the shapes the document refuses, and maps the role's TYPED
// refusals onto the frozen codes.
func (s *Server) handleAddDependencies(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.addDependenciesRequest(w, r)
	if !ok {
		return
	}

	editor, err := s.dependencyEditor(r)
	if err != nil {
		s.failAddDependencies(w, r, request, err)
		return
	}
	result, err := editor.AddDependencies(r.Context(), request)
	if err != nil {
		s.failAddDependencies(w, r, request, err)
		return
	}
	writeJSON(w, apigen.AddDependenciesResponse{Added: wireRequestedEdges(result.Added)})
}

// addDependenciesRequest decodes and validates the body, and reports whether
// the request may proceed. Every refusal here happens before any database work,
// which is what lets these 400s reflect the caller's own input back.
func (s *Server) addDependenciesRequest(w http.ResponseWriter, r *http.Request) (issueops.AddDependenciesRequest, bool) {
	members, res := decodeJSONObject(w, r, maxAddDependenciesBodyBytes)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.AddDependenciesRequest{}, false
	}
	if offender, unknown := unknownMember(members, addDependenciesMembers); unknown {
		s.failUnknownMember(w, r, offender, addDependenciesMembers)
		return issueops.AddDependenciesRequest{}, false
	}
	actor, ok := s.bodyActor(w, r, members)
	if !ok {
		return issueops.AddDependenciesRequest{}, false
	}
	edges, ok := s.addDependencyEdges(w, r, members)
	if !ok {
		return issueops.AddDependenciesRequest{}, false
	}
	// SkipPerEdgeCycleCheck stays UNPUBLISHED and therefore false: it trades
	// validation for speed on a trusted bulk path, and an unauthenticated HTTP
	// surface is where a default must be the guarded one.
	return issueops.AddDependenciesRequest{Actor: actor, Edges: edges}, true
}

// addDependencyEdges validates `edges` and projects it onto the role's edges.
func (s *Server) addDependencyEdges(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage) ([]issueops.DependencyEdge, bool) {
	raw, ok := members["edges"]
	if !ok {
		s.fail(w, r, InvalidArgument("edges", ReasonInvalidValue, "`edges` is required"))
		return nil, false
	}
	var rawEdges []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawEdges); err != nil || rawEdges == nil {
		s.fail(w, r, InvalidArgument("edges", ReasonInvalidValue, "`edges` must be an array of objects"))
		return nil, false
	}
	switch {
	case len(rawEdges) == 0:
		s.fail(w, r, InvalidArgument("edges", ReasonInvalidValue,
			"`edges` must carry at least one edge; a write that writes nothing is refused rather than answered"))
		return nil, false
	case len(rawEdges) > maxAddDependencyEdges:
		s.fail(w, r, InvalidArgument("edges", ReasonInvalidValue,
			fmt.Sprintf("`edges` carries %d edges; the limit is %d per request", len(rawEdges), maxAddDependencyEdges)))
		return nil, false
	}

	edges := make([]issueops.DependencyEdge, 0, len(rawEdges))
	for i, rawEdge := range rawEdges {
		if rawEdge == nil {
			s.fail(w, r, InvalidArgument(addEdgeParam(i, ""), ReasonInvalidValue, "an edge must be a JSON object"))
			return nil, false
		}
		if offender, unknown := unknownMember(rawEdge, dependencyEdgeMembers); unknown {
			s.failUnknownMember(w, r, addEdgeParam(i, offender), dependencyEdgeMembers)
			return nil, false
		}
		edge, res := addDependencyEdge(i, rawEdge)
		if res != nil {
			s.fail(w, r, *res)
			return nil, false
		}
		edges = append(edges, edge)
	}
	return edges, true
}

// addDependencyEdge projects one decoded edge onto the role's edge, or reports
// the refusal it earned.
func addDependencyEdge(index int, raw map[string]json.RawMessage) (issueops.DependencyEdge, *Result) {
	issueID, res := requiredEndpointMember(raw, "issue_id")
	if res != nil {
		return issueops.DependencyEdge{}, indexParam(res, index)
	}
	dependsOnID, res := requiredEndpointMember(raw, "depends_on_id")
	if res != nil {
		return issueops.DependencyEdge{}, indexParam(res, index)
	}
	edgeType, res := requiredEdgeType(raw)
	if res != nil {
		return issueops.DependencyEdge{}, indexParam(res, index)
	}
	// The SELF-DEPENDENCY refusal is answered here rather than left to the
	// role, and it is a 400 rather than a 409 because it is request-INTRINSIC:
	// an edge pointing an issue at itself is invalid whatever the graph holds,
	// so it is a refusal of a value. Answering it here is also what lets the
	// refusal name the offending edge, which the role's request-level sentinel
	// cannot.
	if issueID == dependsOnID {
		res := InvalidArgument(addEdgeParam(index, "depends_on_id"), ReasonInvalidValue,
			"an issue cannot depend on itself")
		return issueops.DependencyEdge{}, &res
	}
	return issueops.DependencyEdge{
		IssueID:     issueID,
		DependsOnID: dependsOnID,
		Type:        edgeType,
	}, nil
}

// requiredEdgeType reads `type` and checks that it IS a value the column can
// hold — never that it is a member of a known-types list. The edge vocabulary
// is OPEN, so a workspace's own type passes and only an unstorable one is
// refused.
func requiredEdgeType(raw map[string]json.RawMessage) (types.DependencyType, *Result) {
	refuse := func(detail string) *Result {
		res := InvalidArgument("type", ReasonInvalidValue, detail)
		return &res
	}
	member, ok := raw["type"]
	if !ok {
		return "", refuse("`type` is required")
	}
	var value *string
	if err := json.Unmarshal(member, &value); err != nil || value == nil {
		return "", refuse("`type` must be a string")
	}
	edgeType := types.DependencyType(*value)
	if !edgeType.IsValid() {
		return "", refuse(fmt.Sprintf("`type` must be 1 to %d characters", types.MaxDependencyTypeLen))
	}
	return edgeType, nil
}

// indexParam rewrites a per-member refusal's `param` into the `edges[i].member`
// spelling, so a client dispatching on it learns WHICH edge and WHICH member.
// The member checks are shared with the removal, which has no index to add.
func indexParam(res *Result, index int) *Result {
	if res.Problem.Param != nil {
		indexed := addEdgeParam(index, *res.Problem.Param)
		res.Problem.Param = &indexed
	}
	return res
}

// addEdgeParam spells the `param` member for a refusal inside `edges`.
func addEdgeParam(index int, member string) string {
	param := fmt.Sprintf("edges[%d]", index)
	if member == "" {
		return param
	}
	return param + "." + member
}

// wireRequestedEdges projects the role's echo onto the wire, in request order.
func wireRequestedEdges(edges []issueops.DependencyEdge) []apigen.DependencyEdge {
	out := make([]apigen.DependencyEdge, 0, len(edges))
	for _, edge := range edges {
		out = append(out, apigen.DependencyEdge{
			IssueId:     edge.IssueID,
			DependsOnId: edge.DependsOnID,
			Type:        string(edge.Type),
		})
	}
	return out
}

// failAddDependencies answers a refused assertion, adding the extension members
// each typed 409 carries.
//
// EVERY BRANCH READS THE ROLE'S TYPED FIELDS, never its prose. The two conflict
// codes exist so a client can stop substring-matching messages, and it can only
// stop if the server never does it either — the ClaimConflictError rule,
// applied to the graph. The hierarchy members in particular have no other
// source: the conflicting hierarchy may exist only inside the batch that was
// rolled back, so the refusing transaction is the only place they can come
// from.
//
// The existence refusals are 400s rather than 404s (the request BODY is what is
// wrong; there is no id in the path to have missed) and they name the offending
// edge by finding it in the request — which is exactly why
// DependencyEndpointNotFoundError carries the whole edge and not only the
// missing id.
//
// Nothing here quotes a role message: 4xx details on this surface reflect the
// caller's own input back, and the endpoint ids they name came from the request.
func (s *Server) failAddDependencies(w http.ResponseWriter, r *http.Request, request issueops.AddDependenciesRequest, err error) {
	var typeConflict *issueops.DependencyTypeConflictError
	var hierarchy *issueops.DependencyHierarchyConflictError
	var endpoint *issueops.DependencyEndpointNotFoundError

	switch {
	case errors.As(err, &typeConflict):
		s.fail(w, r, newResult(CodeDependencyExists,
			"this pair already carries an edge of a different type; remove it before re-adding").
			WithDependencyTypeConflict(typeConflict.ExistingType, typeConflict.RequestedType))

	case errors.As(err, &hierarchy):
		s.fail(w, r, newResult(CodeDependencyCycle,
			"a blocking edge against the issue's own ancestor or descendant would never clear").
			WithHierarchyConflict(hierarchy.IssueID, hierarchy.BlockerID, hierarchy.BlockerIsAncestor))

	case errors.Is(err, issueops.ErrDependencyCycle):
		// No extension members: this is the plain scheduling cycle, and their
		// ABSENCE is what tells a client which of the two refusals it got.
		s.fail(w, r, newResult(CodeDependencyCycle,
			"the requested edges would create a dependency cycle; nothing was written"))

	case errors.As(err, &endpoint):
		s.refusedAddDependencies(r, err)
		member, detail := "issue_id", "an edge's source names no issue in this workspace; nothing was written"
		if errors.Is(err, issueops.ErrDependencyTargetNotFound) {
			member, detail = "depends_on_id", "an edge's target names no issue this workspace can see; nothing was written"
		}
		s.fail(w, r, InvalidArgument(addEdgeParam(edgeIndex(request, endpoint), member), ReasonInvalidValue, detail))

	case errors.Is(err, issueops.ErrSelfDependency):
		// Unreachable while the wire edge refuses a self-edge first, and kept
		// so the role's own sentinel cannot become a 500 if the two checks ever
		// disagree about what "the same id" means.
		s.refusedAddDependencies(r, err)
		s.fail(w, r, InvalidArgument("edges", ReasonInvalidValue, "an issue cannot depend on itself"))

	case errors.Is(err, storage.ErrValidation):
		s.refusedAddDependencies(r, err)
		s.fail(w, r, InvalidArgument("edges", ReasonInvalidValue,
			"an edge was refused by this workspace's own validation; nothing was written"))

	default:
		s.failErr(w, r, err)
	}
}

// refusedAddDependencies records the real refusal for the operator. The 4xx
// path does not log by default, and the role's message is the only place the
// underlying reason survives once the response carries the server's own words.
func (s *Server) refusedAddDependencies(r *http.Request, err error) {
	s.event("request_refused", "request_id", requestInfo(r.Context()).id, "error", err.Error())
}

// edgeIndex finds the refused edge in the request by BOTH endpoints, which is
// the only way to name it: the refusal is the request's, and a pair is what
// identifies an edge. An edge the request does not carry cannot happen, and
// answers 0 rather than inventing an index.
func edgeIndex(request issueops.AddDependenciesRequest, refused *issueops.DependencyEndpointNotFoundError) int {
	for i, edge := range request.Edges {
		if edge.IssueID == refused.IssueID && edge.DependsOnID == refused.DependsOnID {
			return i
		}
	}
	return 0
}

// handleRemoveDependency removes exactly the edge the body names.
//
// It is idempotent at the role: an edge that is not there is `removed: false`
// with a 200, not a refusal, so a replayed teardown does not have to classify
// an error to discover it already ran. Nothing on this path probes whether
// either endpoint exists, which is why the operation has no 404 to give.
func (s *Server) handleRemoveDependency(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.removeDependencyRequest(w, r)
	if !ok {
		return
	}

	editor, err := s.dependencyEditor(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := editor.RemoveDependency(r.Context(), request)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	writeJSON(w, apigen.RemoveDependencyResponse{Removed: result.Removed})
}

// removeDependencyRequest decodes and validates the body, and reports whether
// the request may proceed. Every refusal here happens before any database work.
func (s *Server) removeDependencyRequest(w http.ResponseWriter, r *http.Request) (issueops.RemoveDependencyRequest, bool) {
	members, res := decodeJSONObject(w, r, maxJSONBodyBytes)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.RemoveDependencyRequest{}, false
	}
	if offender, unknown := unknownMember(members, removeDependencyMembers); unknown {
		s.failUnknownMember(w, r, offender, removeDependencyMembers)
		return issueops.RemoveDependencyRequest{}, false
	}
	actor, ok := s.bodyActor(w, r, members)
	if !ok {
		return issueops.RemoveDependencyRequest{}, false
	}
	issueID, res := requiredEndpointMember(members, "issue_id")
	if res != nil {
		s.fail(w, r, *res)
		return issueops.RemoveDependencyRequest{}, false
	}
	dependsOnID, res := requiredEndpointMember(members, "depends_on_id")
	if res != nil {
		s.fail(w, r, *res)
		return issueops.RemoveDependencyRequest{}, false
	}
	return issueops.RemoveDependencyRequest{
		Actor:       actor,
		IssueID:     issueID,
		DependsOnID: dependsOnID,
	}, true
}

// requiredEndpointMember reads one end of an edge out of a decoded body.
//
// The id is bounded HERE for the reason the claim's path id is: the ids are
// EXACT canonical ids, `issues.id` is VARCHAR(255), and a longer value — or one
// carrying a control character — names no row that can exist. Answering it from
// the edge costs the server nothing. Unlike the claim's, this refusal is a 400
// rather than a 404: the id is in the BODY, so there is no resource this
// request failed to address and nothing a caller could learn from the answer
// that its own request does not already say.
func requiredEndpointMember(members map[string]json.RawMessage, member string) (string, *Result) {
	refuse := func(detail string) *Result {
		res := InvalidArgument(member, ReasonInvalidValue, detail)
		return &res
	}
	raw, ok := members[member]
	if !ok {
		return "", refuse("`" + member + "` is required")
	}
	// Through a POINTER so that `null` reaches the type-mismatch branch, for
	// the reason bodyActor gives.
	var id *string
	if err := json.Unmarshal(raw, &id); err != nil || id == nil {
		return "", refuse("`" + member + "` must be a string")
	}
	if res := checkEndpointID(member, *id); res != nil {
		return "", res
	}
	return *id, nil
}

// checkEndpointID applies the id bounds an edge endpoint carries wherever it is
// spelled — a member of the removal's body, or a member of one of the add's
// edges — so the two operations refuse the same values.
//
// It does NOT trim. An id is an exact canonical id, and trimming one would
// silently accept a value the caller did not send; the actor is trimmed because
// the document says so for that member alone.
func checkEndpointID(param, id string) *Result {
	refuse := func(detail string) *Result {
		res := InvalidArgument(param, ReasonInvalidValue, detail)
		return &res
	}
	switch {
	case id == "":
		return refuse("`" + param + "` must not be empty")
	case types.CheckFieldLen(param, id) != nil:
		return refuse(fmt.Sprintf("`%s` is longer than the %d characters storage holds", param, types.MaxFieldLen))
	case strings.ContainsFunc(id, isControlChar):
		return refuse("`" + param + "` must not contain control characters")
	}
	return nil
}
