package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

const (
	// maxBatchCreateItems is the document's cap on `items`. It bounds how long
	// one request may hold a write transaction — not batch semantics, which
	// have no size in them.
	maxBatchCreateItems = 100
	// maxBatchCreateBodyBytes bounds the request body. A hundred items each
	// carrying a description, a design and acceptance criteria is the shape this
	// has to admit, so it refuses the absurd before any of it is parsed.
	maxBatchCreateBodyBytes = 4 << 20
)

// batchCreateMembers is the document's member list at each level. The schemas
// are additionalProperties: false, so anything else is refused BY NAME, which is
// why these are decoded as raw members first: encoding/json's
// DisallowUnknownFields reports the offender only inside an error string.
var (
	batchCreateRequestMembers    = []string{"actor", "items"}
	batchCreateItemMembers       = []string{"title", "description", "design", "acceptance_criteria", "priority", "issue_type", "assignee", "labels", "dependencies"}
	batchCreateDependencyMembers = []string{"target_id", "type"}
)

// handleBatchCreate creates every issue in the request body, or none of them.
//
// The create-only guard, the vocabulary, the id assignment, the wisp routing,
// the retry and the history entry all belong to issueops.BatchCreator. This
// function decodes a body, refuses the shapes the document refuses, and
// marshals what came back.
//
// It shares the claim's posture exactly: the actor is caller-ASSERTED
// provenance and not authenticated identity, hooks do not fire, and the
// per-command auto-commit machinery never runs. The only durable effect is the
// single storage commit the role makes inside its own transaction.
func (s *Server) handleBatchCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.batchCreateRequest(w, r)
	if !ok {
		return
	}

	creator, err := s.batchCreator(r)
	if err != nil {
		s.failBatchCreate(w, r, err)
		return
	}
	result, err := creator.CreateBatch(r.Context(), request)
	if err != nil {
		s.failBatchCreate(w, r, err)
		return
	}
	items := make([]types.Issue, len(result.Issues))
	for i, issue := range result.Issues {
		items[i] = *issue
	}
	writeJSON(w, apigen.BatchCreateResponse{Items: items})
}

// batchCreateRequest decodes and validates the body, and reports whether the
// request may proceed.
//
// Every refusal here happens BEFORE any database work, which is what lets the
// 400s reflect the caller's own input back. The role's own refusals cannot be
// that specific without quoting a storage error into a response, so
// failBatchCreate answers those in the server's own words.
func (s *Server) batchCreateRequest(w http.ResponseWriter, r *http.Request) (issueops.CreateBatchRequest, bool) {
	members, res := decodeJSONObject(w, r, maxBatchCreateBodyBytes)
	if res != nil {
		s.fail(w, r, *res)
		return issueops.CreateBatchRequest{}, false
	}
	if offender, unknown := unknownMember(members, batchCreateRequestMembers); unknown {
		s.failUnknownMember(w, r, offender, batchCreateRequestMembers)
		return issueops.CreateBatchRequest{}, false
	}

	actor, ok := s.batchCreateActor(w, r, members)
	if !ok {
		return issueops.CreateBatchRequest{}, false
	}
	items, ok := s.batchCreateItems(w, r, members)
	if !ok {
		return issueops.CreateBatchRequest{}, false
	}
	return issueops.CreateBatchRequest{Actor: actor, Items: items}, true
}

// batchCreateActor validates `actor` under the claim's rules, shared rather
// than restated: the value lands in the same columns and the same storage
// commit message, so a newline forges the same audit-trail lines.
func (s *Server) batchCreateActor(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage) (string, bool) {
	raw, ok := members[claimActorMember]
	if !ok {
		s.fail(w, r, InvalidArgument(claimActorMember, ReasonInvalidValue, "`"+claimActorMember+"` is required"))
		return "", false
	}
	// Through a POINTER so that `null` reaches the type-mismatch branch, for
	// the reason claimActor gives.
	var actor *string
	if err := json.Unmarshal(raw, &actor); err != nil || actor == nil {
		s.fail(w, r, InvalidArgument(claimActorMember, ReasonInvalidValue, "`"+claimActorMember+"` must be a string"))
		return "", false
	}
	trimmed, res := validateActor(*actor)
	if res != nil {
		s.fail(w, r, *res)
		return "", false
	}
	return trimmed, true
}

// batchCreateItems validates `items` and projects it onto the role's items.
func (s *Server) batchCreateItems(w http.ResponseWriter, r *http.Request, members map[string]json.RawMessage) ([]issueops.BatchCreateItem, bool) {
	raw, ok := members["items"]
	if !ok {
		s.fail(w, r, InvalidArgument("items", ReasonInvalidValue, "`items` is required"))
		return nil, false
	}
	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil || rawItems == nil {
		s.fail(w, r, InvalidArgument("items", ReasonInvalidValue, "`items` must be an array of objects"))
		return nil, false
	}
	switch {
	case len(rawItems) == 0:
		s.fail(w, r, InvalidArgument("items", ReasonInvalidValue,
			"`items` must carry at least one issue; a create that creates nothing is refused rather than answered"))
		return nil, false
	case len(rawItems) > maxBatchCreateItems:
		s.fail(w, r, InvalidArgument("items", ReasonInvalidValue,
			fmt.Sprintf("`items` carries %d issues; the limit is %d per request", len(rawItems), maxBatchCreateItems)))
		return nil, false
	}

	items := make([]issueops.BatchCreateItem, 0, len(rawItems))
	for i, rawItem := range rawItems {
		if rawItem == nil {
			s.fail(w, r, InvalidArgument(batchCreateItemParam(i, ""), ReasonInvalidValue, "an item must be a JSON object"))
			return nil, false
		}
		if offender, unknown := unknownMember(rawItem, batchCreateItemMembers); unknown {
			s.failUnknownMember(w, r, batchCreateItemParam(i, offender), batchCreateItemMembers)
			return nil, false
		}
		item, res := batchCreateItem(i, rawItem)
		if res != nil {
			s.fail(w, r, *res)
			return nil, false
		}
		items = append(items, item)
	}
	return items, true
}

// batchCreateItem projects one decoded item onto the role's item, or reports the
// refusal it earned. It decodes into the GENERATED struct, which is what makes a
// member's type the document's type: `priority: "high"` is refused here rather
// than reaching a role that would have to guess what the caller meant.
func batchCreateItem(index int, raw map[string]json.RawMessage) (issueops.BatchCreateItem, *Result) {
	refuse := func(member, detail string) *Result {
		res := InvalidArgument(batchCreateItemParam(index, member), ReasonInvalidValue, detail)
		return &res
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return issueops.BatchCreateItem{}, refuse("", "an item must be a JSON object")
	}
	var wire apigen.BatchCreateItem
	if err := json.Unmarshal(encoded, &wire); err != nil {
		return issueops.BatchCreateItem{}, refuse("", "an item member carries the wrong JSON type")
	}
	if strings.TrimSpace(wire.Title) == "" {
		return issueops.BatchCreateItem{}, refuse("title", "`title` is required and must not be blank")
	}
	if types.CheckFieldLen("title", wire.Title) != nil {
		return issueops.BatchCreateItem{}, refuse("title",
			fmt.Sprintf("`title` is longer than the %d characters storage holds", types.MaxFieldLen))
	}
	// The role validates the type against the workspace's configured vocabulary,
	// which this server cannot read without a transaction; what is checked here
	// is only what the schema declares. A SLICE and not a map, so that an item
	// breaking both rules always names the same offender: `param` is what a
	// client dispatches on, and it must not depend on map order.
	for _, bounded := range []struct {
		member string
		value  *string
	}{{"assignee", wire.Assignee}, {"issue_type", wire.IssueType}} {
		if bounded.value != nil && types.CheckFieldLen(bounded.member, *bounded.value) != nil {
			return issueops.BatchCreateItem{}, refuse(bounded.member,
				fmt.Sprintf("`%s` is longer than the %d characters storage holds", bounded.member, types.MaxFieldLen))
		}
	}
	issue := &types.Issue{
		Title:              wire.Title,
		Description:        derefString(wire.Description),
		Design:             derefString(wire.Design),
		AcceptanceCriteria: derefString(wire.AcceptanceCriteria),
		Status:             types.StatusOpen,
		Assignee:           derefString(wire.Assignee),
		IssueType:          types.IssueType(derefString(wire.IssueType)),
	}
	if wire.Priority != nil {
		if *wire.Priority < 0 || *wire.Priority > 4 {
			return issueops.BatchCreateItem{}, refuse("priority",
				fmt.Sprintf("`priority` is %d; the range is 0 to 4", *wire.Priority))
		}
		issue.Priority = *wire.Priority
	}
	if wire.Labels != nil {
		for _, label := range *wire.Labels {
			if types.CheckFieldLen("label", label) != nil {
				return issueops.BatchCreateItem{}, refuse("labels",
					fmt.Sprintf("a label is longer than the %d characters storage holds", types.MaxFieldLen))
			}
		}
		issue.Labels = *wire.Labels
	}
	item := issueops.BatchCreateItem{Issue: issue}
	if wire.Dependencies == nil {
		return item, nil
	}
	rawEdges := rawDependencyMembers(raw)
	for j, dependency := range *wire.Dependencies {
		if offender, unknown := unknownMember(rawEdgeAt(rawEdges, j), batchCreateDependencyMembers); unknown {
			res := InvalidArgument(batchCreateItemParam(index, "dependencies."+offender), ReasonUnknownParameter,
				"an edge carries "+strings.Join(batchCreateDependencyMembers, ", ")+" and nothing else")
			return issueops.BatchCreateItem{}, &res
		}
		if dependency.TargetId == "" || types.CheckFieldLen("target_id", dependency.TargetId) != nil {
			return issueops.BatchCreateItem{}, refuse("dependencies.target_id",
				"`target_id` is required and must be at most "+fmt.Sprint(types.MaxFieldLen)+" characters")
		}
		// A value at all, and one the column holds — never membership of a
		// known-types list. The edge vocabulary is OPEN, as EdgeReadRequest.Types
		// says, so a workspace's own type passes and only an unstorable one is
		// refused.
		if !types.DependencyType(dependency.Type).IsValid() {
			return issueops.BatchCreateItem{}, refuse("dependencies.type",
				fmt.Sprintf("`type` must be 1 to %d characters", types.MaxDependencyTypeLen))
		}
		item.Dependencies = append(item.Dependencies, issueops.CreateDependency{
			TargetID: dependency.TargetId,
			Type:     types.DependencyType(dependency.Type),
		})
	}
	return item, nil
}

// rawDependencyMembers decodes an item's edges as raw members, so an unknown
// member INSIDE an edge is refused by name like every other one. It answers nil
// — carrying no unknown member — when the shape is not the one the generated
// decode accepted, because a disagreement between the two decodes is not a
// client-attributable refusal.
func rawDependencyMembers(item map[string]json.RawMessage) []map[string]json.RawMessage {
	raw, ok := item["dependencies"]
	if !ok {
		return nil
	}
	var edges []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &edges); err != nil {
		return nil
	}
	return edges
}

// rawEdgeAt is the bounds-checked read of one decoded edge.
func rawEdgeAt(edges []map[string]json.RawMessage, j int) map[string]json.RawMessage {
	if j >= len(edges) {
		return nil
	}
	return edges[j]
}

// batchCreateItemParam spells the `param` member for a refusal inside `items`,
// so a client dispatching on it learns WHICH item and WHICH member.
func batchCreateItemParam(index int, member string) string {
	param := fmt.Sprintf("items[%d]", index)
	if member == "" {
		return param
	}
	return param + "." + member
}

// failUnknownMember answers an unknown body member the way an unknown query
// parameter is answered: one offender, chosen deterministically, and the
// allowed set in the detail.
func (s *Server) failUnknownMember(w http.ResponseWriter, r *http.Request, offender string, allowed []string) {
	requestInfo(r.Context()).refuse(offender)
	s.fail(w, r, InvalidArgument(offender, ReasonUnknownParameter,
		"this operation's request body carries "+strings.Join(allowed, ", ")+" and nothing else"))
}

// failBatchCreate answers a failed batch.
//
// The role's ErrValidation is answered HERE rather than in ClassifyError: the
// role wraps its dangling-edge refusal in BOTH ErrValidation and ErrNotFound,
// so the shared classifier — which reaches ErrNotFound first — would answer 404
// for a request that addressed no resource at all.
//
// NEITHER BRANCH QUOTES THE ROLE'S MESSAGE. A refused edge arrives as a driver
// error naming tables and constraints, and 4xx details on this surface reflect
// the caller's own input back rather than server internals. The real error goes
// to the log with the request id.
func (s *Server) failBatchCreate(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, storage.ErrValidation) {
		s.failErr(w, r, err)
		return
	}
	// The 4xx path does not log by default, so this is the one place the real
	// refusal is recorded for the operator.
	s.event("request_refused", "request_id", requestInfo(r.Context()).id, "error", err.Error())
	if errors.Is(err, storage.ErrNotFound) {
		s.fail(w, r, InvalidArgument("items", ReasonInvalidValue,
			"a dependency target names no issue in this workspace; nothing was created"))
		return
	}
	s.fail(w, r, InvalidArgument("items", ReasonInvalidValue,
		"an item was refused by this workspace's own validation; nothing was created"))
}

// unknownMember reports the smallest member name outside allowed, chosen
// deterministically so a client dispatching on `param` never sees it depend on
// map order.
func unknownMember(members map[string]json.RawMessage, allowed []string) (string, bool) {
	var unknown []string
	for name := range members {
		if !slices.Contains(allowed, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return "", false
	}
	return slices.Min(unknown), true
}

// decodeJSONObject reads the body as a JSON object of raw members, bounded by
// limit. It is decodeClaimBody generalized over the bound, and it makes the
// same three refusals for the same reasons.
func decodeJSONObject(w http.ResponseWriter, r *http.Request, limit int64) (map[string]json.RawMessage, *Result) {
	// A body with no nameable part: `param` is documented absent on exactly
	// this case and present on every other 400.
	unparseable := func(detail string) *Result {
		res := InvalidArgument("", ReasonInvalidValue, detail)
		return &res
	}
	var members map[string]json.RawMessage
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(&members); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, unparseable(fmt.Sprintf("request body is larger than %d bytes", limit))
		}
		return nil, unparseable("request body must be a JSON object")
	}
	if members == nil {
		// Valid JSON, but `null`: no members to read anything out of.
		return nil, unparseable("request body must be a JSON object")
	}
	if dec.More() {
		return nil, unparseable("request body must be a single JSON object")
	}
	return members, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
