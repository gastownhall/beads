// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/encode.go@49d1df2f6)
// to OSS beads under the MIT license.
// Package encode turns the read requests bd's storage roles take into v0 query
// parameters, and refuses — never drops — everything the wire cannot carry.
//
// It is the client half of internal/httpapi's parameter decoding, and it is a
// separate package from the wire transport for one reason: the decision it
// makes is a POLICY decision, not a transport one. Dropping a filter widens a
// result set invisibly, and the server can only reject parameters it receives,
// so a filter the encoder silently forgets is a wrong answer no gate on either
// side can observe. Everything in here exists to make that impossible:
//
//   - the encoder table (table.go) classifies every field of every request
//     shape, and the bijection gate checks it against reflection over the
//     structs, against the OpenAPI document, and against these encoders;
//   - divergence ledger v1 (ledger.go, ledger_fields.go) carries the reason and
//     the citation for every field that cannot make the crossing, and a refusal
//     is raised BY ledger id so a refusal with no row cannot be written;
//   - the refusal checks themselves are TABLE-DRIVEN rather than hand-rolled,
//     so an encoder and its table cannot disagree about what refuses. What
//     stays hand-written is the value rendering, which is exactly what the
//     round-trip gate drives through the server's own decoder.
//
// See engdocs/design/http-client-backend.md — D7 (the refusal taxonomy), D8
// (the role dispositions and the refuse-not-drop enumeration), D9 (the
// divergence ledger), D11 (id resolution) and D12 (MaxRows).
package encode

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// MaxExactIDs bounds the exact-ids fan-out of the SearchIssues bridge: an
// IssueFilter naming more ids than this refuses rather than issuing an
// unbounded burst of getIssue calls at one server (D11; the number itself is
// open question 6, to be measured against real call-site fan-out before GA).
const MaxExactIDs = 100

// ErrRefused matches every refusal this package raises, so a caller that only
// needs to know "the wire cannot express this" does not have to reach for the
// concrete type.
var ErrRefused = errors.New("not expressible on the v0 wire")

// RefusedError is the typed refusal: the operation being encoded for, and the
// divergence-ledger row that says what was refused, why, which decision it
// comes from and what pins it.
//
// It deliberately carries no server URL and no flag rendering. D7's three
// refusal texts are the CLI's to write — they speak the user's vocabulary and
// need the target and the server's version — and the row here is what supplies
// the flag name and the reason they read back.
type RefusedError struct {
	Op    Op
	Shape string
	Row   Row
}

func (e *RefusedError) Error() string {
	subject := e.Row.Flag
	if subject == "" && e.Row.Type != nil {
		subject = e.Row.Type.Name() + "." + e.Row.Field
	}
	if subject == "" {
		// A row about a COMBINATION of fields has no single subject; its What
		// is already the sentence.
		return fmt.Sprintf("%s: %s (%s; %s)", e.Op, e.Row.What, e.Row.Why, e.Row.SpecRow)
	}
	return fmt.Sprintf("%s: %s is %s (%s; %s)", e.Op, subject, ErrRefused, e.Row.Why, e.Row.SpecRow)
}

func (e *RefusedError) Is(target error) bool { return target == ErrRefused }

// Encoded is what one request becomes.
type Encoded struct {
	// Params is the query string, empty rather than nil for a request that
	// names no parameter.
	Params url.Values
	// PathIDs are the ids a path-shaped encoding dials, one request each: the
	// getIssue id, or the exact-ids fan-out of the SearchIssues bridge.
	PathIDs []string
}

// Encode routes one source value to the encoder for an operation and shape.
//
// It exists so the gates can drive every table uniformly. Ordinary callers know
// which request they hold and use the typed functions below.
func Encode(op Op, shape string, source any) (Encoded, error) {
	switch (opShape{op, shape}) {
	case opShape{OpListReadyWork, ""}:
		return encodeAs(op, shape, source, ReadyParams)
	case opShape{OpCountReadyWork, ""}:
		return encodeAs(op, shape, source, ReadyCountParams)
	case opShape{OpListIssues, ""}:
		return encodeAs(op, shape, source, ListParams)
	case opShape{OpQueryIssues, ""}:
		return encodeAs(op, shape, source, QueryParams)
	case opShape{OpCountIssues, ""}:
		return encodeAs(op, shape, source, CountParams)
	case opShape{OpCountIssues, "byGroup"}:
		return encodeAs(op, shape, source, CountByGroupParams)
	case opShape{OpListReadyWork, "workFilterBridge"}:
		return encodeAs(op, shape, source, ReadyBridgeParams)
	case opShape{OpGetIssue, ""}:
		return encodeGetIssue(op, shape, source)
	case opShape{OpGetIssue, "searchExactIDs"}, opShape{OpListIssues, "searchParentWalk"}:
		return encodeSearch(op, shape, source)
	}
	return Encoded{}, fmt.Errorf("encode: no encoder for operation %q shape %q", op, shape)
}

// opShape is the (operation, shape) pair Encode dispatches on.
type opShape struct {
	op    Op
	shape string
}

// encodeGetIssue is Encode's arm for getIssue: GetTarget's id is the one path
// id the request dials.
func encodeGetIssue(op Op, shape string, source any) (Encoded, error) {
	req, err := sourceAs[issueops.GetRequest](op, shape, source)
	if err != nil {
		return Encoded{}, err
	}
	id, v := GetTarget(req)
	return Encoded{Params: v, PathIDs: []string{id}}, nil
}

// encodeSearch is Encode's arm for the two SearchIssues bridge shapes: it plans
// the filter and refuses one that plans to the other shape.
func encodeSearch(op Op, shape string, source any) (Encoded, error) {
	filter, err := sourceAs[types.IssueFilter](op, shape, source)
	if err != nil {
		return Encoded{}, err
	}
	// The zero vocabulary, which is what a gate driving the table has: it
	// falls back to the built-in statuses and the default infra types, the
	// same reading BuildListFilter gives an unconfigured workspace.
	plan, err := PlanSearch(filter, ZeroVocabulary)
	if err != nil {
		return Encoded{}, err
	}
	if string(plan.Shape) != shape {
		return Encoded{}, fmt.Errorf("encode: the filter is the %q shape, not %q", plan.Shape, shape)
	}
	return Encoded{Params: plan.Params, PathIDs: plan.IDs}, nil
}

func encodeAs[T any](op Op, shape string, source any, encoder func(T) (url.Values, error)) (Encoded, error) {
	req, err := sourceAs[T](op, shape, source)
	if err != nil {
		return Encoded{}, err
	}
	v, err := encoder(req)
	if err != nil {
		return Encoded{}, err
	}
	return Encoded{Params: v}, nil
}

func sourceAs[T any](op Op, shape string, source any) (T, error) {
	req, ok := source.(T)
	if !ok {
		return req, fmt.Errorf("encode: operation %q shape %q takes a %T, got %T", op, shape, req, source)
	}
	return req, nil
}

// ReadyParams encodes a ready-work listing onto GET /v0/beads/ready.
func ReadyParams(req issueops.ReadyRequest) (url.Values, error) {
	b := newBuilder(OpListReadyWork, "")
	b.refuseInexpressible(req)
	b.readyFilters(req)
	b.boolean("brief", req.Brief)
	b.str("sort", readySort(req.Sort))
	b.intPtr("limit", req.Limit)
	return b.done()
}

// ClaimNextParams encodes a ClaimNext ready request onto the QUERY STRING of
// POST /v0/beads/issues:claimNext.
//
// IT BUILDS AGAINST listReadyWork's TABLE, which is not laziness about a missing
// one: the claim's filter IS that listing's filter, decoded server-side by the
// same function, and giving this operation a table of its own would be a second
// declaration of one vocabulary — the exact drift the table gate exists to
// prevent. So the refusal walk, the fifteen filter parameters and the sort
// policy all come from readyTable, and what differs is what is NOT emitted.
//
// TWO PARAMETERS ARE WITHHELD and neither absence is one the server would
// forgive. `limit` is refused BY VALUE on this operation, with any value at all
// — the scan has to stay unbounded or a window of rows a racing agent already
// took would report nothing to claim while other ready work waits. `brief` has
// no spelling here, because the claim refetches its winner whole rather than
// reading it through the page's query, so the projection would have nothing to
// apply to and would answer a MUTATING request with a row saying it got
// everything. The ROLE refuses both before this encoder is reached
// (ValidateClaimNextRequest); this is the second line, and it is the one that
// holds if a caller ever reaches the encoder another way.
//
// The SORT is sent explicitly, always, exactly as the listing sends it — an
// absent `sort` is `priority` to the handler and `hybrid` to the storage layer,
// and here that difference decides WHICH ROW IS WRITTEN rather than what order
// rows are printed in.
func ClaimNextParams(req issueops.ReadyRequest) (url.Values, error) {
	b := newBuilder(OpListReadyWork, "")
	b.refuseInexpressible(req)
	b.readyFilters(req)
	b.str("sort", readySort(req.Sort))
	return b.done()
}

// metadataFieldList renders the metadata equality filter as the wire's repeated
// `key=value` member, in key order — b.metadata's rule, so the object and the
// query produce the same set.
func metadataFieldList(fields map[string]string) []string {
	if len(fields) == 0 {
		return nil
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(fields))
	for _, key := range keys {
		out = append(out, key+"="+fields[key])
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

// strSlicePtr returns a pointer to a copy of values, or nil for an empty slice.
// An empty-but-non-nil slice constrains nothing, so it is sent as absent — the
// same reading populated() gives it on the refusal walk.
func strSlicePtr(values []string) *[]string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	return &out
}

// ReadyCountParams encodes a ready-work COUNT onto GET /v0/beads/ready:count.
//
// It sends the listing's filters and nothing else: the operation publishes no
// order and no page, and the role refuses both itself.
func ReadyCountParams(req issueops.ReadyRequest) (url.Values, error) {
	b := newBuilder(OpCountReadyWork, "")
	b.refuseInexpressible(req)
	b.readyFilters(req)
	return b.done()
}

// readySort resolves the policy actually sent.
//
// An EMPTY policy is sent as the concrete `hybrid` it means, never omitted.
// Omitting it would adopt the server's own `priority` default while the caller
// meant the storage layer's hybrid fallback, and those two answer with
// different item SETS as soon as a limit truncates — the silent kind of
// divergence this package exists to make impossible.
func readySort(policy string) string {
	if policy == "" {
		return "hybrid"
	}
	return policy
}

// ListParams encodes an issue listing onto GET /v0/beads/issues.
//
// It emits no `cursor` and no `sort`: the cursor is the server's to mint and
// the pager's to echo, and the order is welded to created order on the wire —
// see the listIssues table for both.
func ListParams(req issueops.ListRequest) (url.Values, error) {
	b := newBuilder(OpListIssues, "")
	b.refuseInexpressible(req)

	b.str("status", req.Status)
	b.str("type", req.IssueType)
	b.str("assignee", req.Assignee)

	b.list("label", req.Labels)
	b.list("label_any", req.LabelsAny)
	b.list("exclude_label", req.ExcludeLabels)

	b.str("parent", req.ParentID)

	b.boolean("all", req.AllFlag)
	b.boolean("include_templates", req.IncludeTemplates)
	b.boolean("include_gates", req.IncludeGates)
	b.boolean("include_infra", req.IncludeInfra)
	b.boolean("include_ephemeral", req.IncludeEphemeral)

	b.timestamp("created_before", req.CreatedBefore)
	b.timestamp("created_after", req.CreatedAfter)

	b.metadata(req.MetadataFields)
	b.str("has_metadata_key", req.HasMetadataKey)

	b.boolean("brief", req.Brief)
	b.intPtr("limit", req.Limit)
	return b.done()
}

// QueryParams encodes a boolean-expression query onto
// GET /v0/beads/issues:query.
func QueryParams(req issueops.QueryRequest) (url.Values, error) {
	b := newBuilder(OpQueryIssues, "")
	b.refuseInexpressible(req)

	b.str("q", req.Expression)
	b.boolean("all", req.IncludeClosed)
	b.str("sort", req.SortBy)
	b.boolean("reverse", req.Reverse)
	b.intPtr("limit", req.Limit)
	return b.done()
}

// CountParams encodes an issue count onto GET /v0/beads/issues:count.
//
// It sends the predicate and nothing else — there is no page, no order and no
// projection on this operation, because a cardinality has none — and it refuses
// nothing, because the operation publishes every member of the request. The
// refusal walk still runs: it is what turns a member added upstream tomorrow
// into a failure here rather than into a silently narrower predicate behind an
// unchanged-looking number.
func CountParams(req issueops.CountRequest) (url.Values, error) {
	b := newBuilder(OpCountIssues, "")
	b.refuseInexpressible(req)
	b.countFilters(req)
	return b.done()
}

// CountByGroupParams encodes a bucketed count onto the same operation.
//
// The predicate is the SCALAR encoder's, called rather than copied: the two
// methods must ask the same question of the same set, and two builders would be
// two chances to disagree about that.
//
// A dimension that is there travels VERBATIM, unrecognized ones included: the
// closed vocabulary is the ROLE's rule at every backend, refused by the role
// client before it ever reaches here — and the server refuses it too, naming
// the parameter. What this layer must not do is drop one: a group_by that went
// missing would earn a scalar answer with no `groups` member at all, which the
// caller reads as "no buckets" rather than as an error.
//
// An EMPTY dimension is the one value that does not travel, and it is not a
// drop — it is the absence itself, which no parameter can spell. Nothing
// reaches this function with one: the role client refuses an empty GroupBy with
// the unrecognized ones, for exactly the reason above. The emptiness check is
// what keeps `group_by=` off the query of a caller who came through some other
// door.
func CountByGroupParams(req issueops.CountByGroupRequest) (url.Values, error) {
	v, err := CountParams(req.Filter)
	if err != nil {
		return nil, err
	}
	if req.GroupBy != "" {
		v.Set("group_by", string(req.GroupBy))
	}
	return v, nil
}

// countFilters emits the count's whole vocabulary, which is every member of the
// request.
//
// It is spelled beside readyFilters and in the count table's order, so the
// three statements about this operation — the table, this emitter and the
// server's own countFilters decode — read as one list three times rather than
// as three lists.
func (b *builder) countFilters(req issueops.CountRequest) {
	b.str("status", req.Status)
	b.str("type", req.IssueType)
	b.str("assignee", req.Assignee)

	b.intPtr("priority", req.Priority)
	b.intPtr("priority_min", req.PriorityMin)
	b.intPtr("priority_max", req.PriorityMax)

	b.list("label", req.Labels)
	b.list("label_any", req.LabelsAny)

	b.str("title", req.TitleSearch)
	b.str("id", req.IDFilter)

	b.str("title_contains", req.TitleContains)
	b.str("desc_contains", req.DescContains)
	b.str("notes_contains", req.NotesContains)

	b.timestamp("created_after", req.CreatedAfter)
	b.timestamp("created_before", req.CreatedBefore)
	b.timestamp("updated_after", req.UpdatedAfter)
	b.timestamp("updated_before", req.UpdatedBefore)
	b.timestamp("closed_after", req.ClosedAfter)
	b.timestamp("closed_before", req.ClosedBefore)

	b.boolean("empty_description", req.EmptyDesc)
	b.boolean("no_assignee", req.NoAssignee)
	b.boolean("no_labels", req.NoLabels)

	b.boolean("include_infra", req.IncludeInfra)
	// Added alongside the table.go IncludeEphemeral/HasMetadataKey entries: the
	// table declared these params but nothing here wrote them, so a populated
	// field silently vanished before the request left the process.
	b.boolean("include_ephemeral", req.IncludeEphemeral)

	b.metadata(req.MetadataFields)
	b.str("has_metadata_key", req.HasMetadataKey)

	// The issues.count.scope members (upstream #7199), in the server's own
	// countFilters order. Each is emitted only when populated, so a request that
	// sets none of them stays byte-identical to what a pre-scope server accepts.
	b.str("parent", req.ParentID)
	b.boolean("no_parent", req.NoParent)
	b.list("exclude_type", req.ExcludeTypes)
	b.list("exclude_status", req.ExcludeStatus)
}

// GetTarget encodes a detail lookup onto GET /v0/beads/issues/{id}, returning
// the path id and the query.
//
// It cannot refuse: every member of a GetRequest is on the wire, which is what
// retired ledger row L4.
func GetTarget(req issueops.GetRequest) (string, url.Values) {
	b := newBuilder(OpGetIssue, "")
	b.boolean("include_dependents", req.IncludeDependents)
	b.boolean("include_comments", req.IncludeComments)
	b.boolean("brief_deps", req.BriefDeps)
	return req.ID, b.v
}

// ReadyBridgeParams encodes the legacy types.WorkFilter onto the ready
// operation's parameters.
//
// It is the reverse mapper D8 calls deliberately throwaway: it lives until
// `bd ready`'s listing moves onto issueops.Reader upstream, and until then it
// refuses every field it cannot express rather than answering a wider question
// than was asked (L12).
func ReadyBridgeParams(f types.WorkFilter) (url.Values, error) {
	b := newBuilder(OpListReadyWork, "workFilterBridge")
	// The one derived default is recognized BEFORE the residual refusal sweep,
	// matching the table's declaration order: Status is declared ahead of the
	// plain refusals, so a filter that diverges in both refuses on the derived
	// member (D4's parent-walk inversion, applied to the ready bridge's single
	// derived default).
	b.recognizeReadyStatus(f)
	b.refuseInexpressible(f)

	b.str("type", f.Type)
	b.strPtr("assignee", f.Assignee)
	b.boolean("unassigned", f.Unassigned)

	b.list("label", f.Labels)
	b.list("label_any", f.LabelsAny)
	b.list("exclude_label", f.ExcludeLabels)
	b.str("label_pattern", f.LabelPattern)
	b.str("label_regex", f.LabelRegex)

	b.intPtr("priority", f.Priority)
	b.strPtr("parent", f.ParentID)

	b.list("exclude_type", issueTypeNames(f.ExcludeTypes))
	b.metadata(f.MetadataFields)
	b.str("has_metadata_key", f.HasMetadataKey)

	b.boolean("include_ephemeral", f.IncludeEphemeral)
	b.boolean("include_deferred", f.IncludeDeferred)

	b.boolean("brief", f.Lite)
	b.str("sort", readySort(string(f.SortPolicy)))
	b.intPositive("limit", f.Limit)
	return b.done()
}

// ReadyBridgeCountParams encodes the same legacy types.WorkFilter onto
// countReadyWork's parameters: the size of the set ReadyBridgeParams lists a
// page of.
//
// It is ReadyBridgeParams with the page taken off, not a second mapper. The
// filter half goes through the bridge's own encoder — so every refusal it
// makes (ExcludeIDs, Offset, a non-default Status, ...) refuses here too, and a
// field the listing could not express can never be counted over a wider set —
// and then the three page-only parameters are removed: `limit` (countReadyWork
// refuses any value, and a cardinality has no page), `sort` (the count's order
// is server-fixed and its total order-independent) and `brief` (a count
// hydrates nothing). What remains is exactly readyFilterEntries, the parameter
// list the two operations share by construction.
func ReadyBridgeCountParams(f types.WorkFilter) (url.Values, error) {
	f.Limit = 0
	f.Lite = false
	v, err := ReadyBridgeParams(f)
	if err != nil {
		return nil, err
	}
	v.Del("limit")
	v.Del("sort")
	v.Del("brief")
	return v, nil
}

// recognizeReadyStatus inverts ready work's one derived default.
//
// listReadyWork publishes no status parameter: handleReady decodes a status-LESS
// request and the server's role re-derives the status through
// workapi.BuildReadyFilter, which is unconditionally StatusOpen — open work
// only, the set `bd list --ready` shows. So a WorkFilter carrying exactly that
// derived default is the server's own baseline: the encoder sends no `status`
// and the server reproduces the identical row set. This is what lets every real
// `bd ready` and `bd ready --json` cross the wire, because BuildReadyFilter is
// the one shape the CLI ever builds and it always stamps StatusOpen.
//
// Any OTHER status refuses (L12), which is what makes the drop match the real
// filter EXACTLY. A named status the wire cannot carry — in_progress, closed, a
// custom category — would answer over the query's own open-only default rather
// than the set named; and an EMPTY status is the storage layer's own
// open+in_progress default (types.WorkFilter.Status), which the ready operation
// cannot express either. A populated Statuses OR-set is left to the residual
// sweep, which refuses it as E-WorkFilter.Statuses.
func (b *builder) recognizeReadyStatus(f types.WorkFilter) {
	if b.err != nil {
		return
	}
	if f.Status != types.StatusOpen {
		b.err = &RefusedError{Op: b.op, Shape: b.shape, Row: RowByID("E-WorkFilter.Status")}
	}
}

// SearchShape names one of the two shapes the SearchIssues bridge serves.
type SearchShape string

const (
	// SearchExactIDs is D11's fast path: one getIssue per named id.
	SearchExactIDs SearchShape = "searchExactIDs"
	// SearchParentWalk is D4's hierarchical descendant walk: paged
	// listIssues?parent=<id> calls, one level at a time.
	SearchParentWalk SearchShape = "searchParentWalk"
)

// SearchPlan is what a raw SearchIssues filter becomes.
type SearchPlan struct {
	Shape SearchShape
	// IDs is the exact-ids fan-out, in the caller's order.
	IDs []string
	// Params is the listing query for the descendant walk. The pager owns the
	// per-page `limit` and the cursor.
	Params url.Values
}

// PlanSearch classifies a raw types.IssueFilter into one of the bridge's two
// shapes, or refuses.
//
// SHAPE FIRST, THEN FIELDS. Which shape a filter is in decides which of its
// fields are readable at all — a ParentID means nothing to getIssue and an id
// set means nothing to a descendant walk — so the classification happens before
// the refusal sweep rather than after it.
//
// vocabulary is the status and type set the descendant walk's derived-default
// inversion recognizes with (parentwalk.go). It must yield the SAME ListConfig
// the filter was built from, which is what makes the inversion exact rather than
// approximate.
//
// It is a FUNCTION rather than a value because only one of the two shapes reads
// it, and the other one is the resolver's hot path: ResolvePartialID runs on
// every id-taking command, and loading a vocabulary the exact-ids arm never
// consults would put three round trips in front of every `bd show`. Deferring it
// into the arm that needs it is also what keeps a REFUSED plan from dialing at
// all — the shape is decided, and rejected, before the wire is touched.
func PlanSearch(f types.IssueFilter, vocabulary func() ListConfig) (SearchPlan, error) {
	hasIDs, hasParent := len(f.IDs) > 0, f.ParentID != nil && *f.ParentID != ""
	switch {
	case hasIDs && hasParent:
		// Refused BEFORE a shape is chosen. Letting precedence pick one would
		// make the refusal cite whichever field the loser happened to be,
		// which is a worse answer than naming the conflict itself.
		return SearchPlan{}, &RefusedError{Op: OpListIssues, Row: RowByID("E-IssueFilter.shapeConflict")}

	case hasIDs:
		if len(f.IDs) > MaxExactIDs {
			return SearchPlan{}, &RefusedError{Op: OpGetIssue, Shape: string(SearchExactIDs), Row: RowByID("E-IssueFilter.IDs@bound")}
		}
		b := newBuilder(OpGetIssue, string(SearchExactIDs))
		b.refuseInexpressible(f)
		if _, err := b.done(); err != nil {
			return SearchPlan{}, err
		}
		return SearchPlan{Shape: SearchExactIDs, IDs: append([]string(nil), f.IDs...)}, nil

	case hasParent:
		v, err := planParentWalk(f, vocabulary())
		if err != nil {
			return SearchPlan{}, err
		}
		return SearchPlan{Shape: SearchParentWalk, Params: v}, nil
	}
	return SearchPlan{}, &RefusedError{Op: OpListIssues, Row: RowByID("E-IssueFilter.noShape")}
}

// issueTypeNames widens a typed exclusion list onto the wire's plain strings.
func issueTypeNames(in []types.IssueType) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, t := range in {
		out = append(out, string(t))
	}
	return out
}

// builder accumulates one request's parameters and its first refusal.
//
// FIRST refusal rather than last, mirroring internal/httpapi's own query
// decoder: the answer must not depend on the order a caller happens to populate
// fields in, and the table's declaration order is the order the sweep walks.
type builder struct {
	op    Op
	shape string
	v     url.Values
	err   error
}

func newBuilder(op Op, shape string) *builder {
	return &builder{op: op, shape: shape, v: url.Values{}}
}

// refuseInexpressible walks this encoding's table and refuses the first
// populated field the wire cannot carry.
//
// It is table-driven on purpose. A hand-rolled check per field is a check that
// can be forgotten for the field added next week, and the forgetting is silent:
// the encoder would emit nothing for it and the server would have nothing to
// reject. Reading the table means the encoder and the classification the gates
// verify are the SAME statement rather than two that agree today.
func (b *builder) refuseInexpressible(source any) {
	if b.err != nil {
		return
	}
	table, ok := TableFor(b.op, b.shape)
	if !ok {
		b.err = fmt.Errorf("encode: no encoder table for operation %q shape %q", b.op, b.shape)
		return
	}
	rv := reflect.ValueOf(source)
	if rv.Type() != table.Source {
		b.err = fmt.Errorf("encode: %s expects %s, got %s", table.Op, table.Source, rv.Type())
		return
	}
	for _, entry := range table.Fields {
		if entry.Disposition != DispRefused {
			continue
		}
		field := rv.FieldByName(entry.Name)
		if !field.IsValid() {
			b.err = fmt.Errorf("encode: %s has no field %s", table.Source, entry.Name)
			return
		}
		if populated(field) {
			b.err = &RefusedError{Op: b.op, Shape: b.shape, Row: RowByID(entry.Ledger)}
			return
		}
	}
}

// populated reports whether a field carries a value the caller set.
//
// An empty-but-non-nil slice or map is NOT populated: it constrains nothing, so
// refusing it would turn a no-op into an error. A non-nil pointer IS, even to a
// zero value — priority 0 is a real priority and `Ephemeral: &false` is a real
// restriction, and both have been lost by code that read a zero as absent.
func populated(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Slice, reflect.Map:
		return v.Len() > 0
	default:
		return !v.IsZero()
	}
}

func (b *builder) str(param, value string) {
	if value != "" {
		b.v.Set(param, value)
	}
}

func (b *builder) strPtr(param string, value *string) {
	if value != nil {
		b.str(param, *value)
	}
}

// boolean emits only the TRUE spelling. Every flag on this surface documents
// false as its default, so an explicit `false` would be a parameter that says
// nothing — and one more value for the server to parse.
func (b *builder) boolean(param string, value bool) {
	if value {
		b.v.Set(param, "true")
	}
}

// intPtr emits a pointer-valued number, INCLUDING zero. The pointer is what
// distinguishes unset from an explicit value, which is the whole reason these
// fields are pointers.
func (b *builder) intPtr(param string, value *int) {
	if value != nil {
		b.v.Set(param, strconv.Itoa(*value))
	}
}

// intPositive emits a plain int only when it bounds something. The legacy
// filters spell "no bound" as 0, so a zero here is an absent parameter rather
// than the wire's `limit=0`, which means unlimited and is refused off loopback.
func (b *builder) intPositive(param string, value int) {
	if value > 0 {
		b.v.Set(param, strconv.Itoa(value))
	}
}

func (b *builder) list(param string, values []string) {
	for _, value := range values {
		b.v.Add(param, value)
	}
}

// timestamp emits an instant in UTC with whatever sub-second precision it
// carries. RFC3339Nano round-trips through the server's time.Parse without
// losing a fractional second, which a bare RFC3339 format would silently drop —
// and a dropped fraction on a created bound is a widened result set.
func (b *builder) timestamp(param string, value *time.Time) {
	if value != nil {
		b.v.Set(param, value.UTC().Format(time.RFC3339Nano))
	}
}

// metadata emits the repeatable key=value equality filter, in key order so one
// filter always produces one query string.
//
// The parameter name is not a parameter of this method: every operation that
// publishes the filter at all spells it `metadata_field`, so taking it from the
// caller would be one more thing a call site could get wrong for no expressive
// gain.
func (b *builder) metadata(fields map[string]string) {
	const param = "metadata_field"
	if len(fields) == 0 {
		return
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.v.Add(param, key+"="+fields[key])
	}
}

// readyFilters emits the filter vocabulary listReadyWork and countReadyWork
// share, which is every parameter but the order and the page.
func (b *builder) readyFilters(req issueops.ReadyRequest) {
	b.str("type", req.IssueType)
	b.str("assignee", req.Assignee)
	b.boolean("unassigned", req.Unassigned)

	b.list("label", req.Labels)
	b.list("label_any", req.LabelsAny)
	b.list("exclude_label", req.ExcludeLabels)
	b.str("label_pattern", req.LabelPattern)
	b.str("label_regex", req.LabelRegex)

	b.intPtr("priority", req.Priority)
	b.str("parent", req.ParentID)

	b.list("exclude_type", req.ExcludeTypes)
	b.metadata(req.MetadataFields)
	b.str("has_metadata_key", req.HasMetadataKey)

	b.boolean("include_ephemeral", req.IncludeEphemeral)
	b.boolean("include_deferred", req.IncludeDeferred)
}

func (b *builder) done() (url.Values, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.v, nil
}
