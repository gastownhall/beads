package httpapi

import (
	"net/http"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The issue-collection reads. Each one decodes its parameters, hands the whole
// request to a role, and shapes the answer onto the wire. Three are on
// issueops.Reader (ready, list, detail); the count and the query are on
// ReadyCounter and Querier, siblings reached the same way through the same
// provider accessors. What follows is about the Reader three, and holds for
// the other two in every respect but which role they name.
//
// WHAT IS NOT HERE IS THE POINT. No filter is built, no ConfigSource is wired,
// no default limit is applied, no status exclusion is chosen, no wisp fallback
// is arranged: all of that is inside issueops.Reader's implementation, which
// `bd show --json` reaches through the same accessor. A handler that skipped a
// step of that construction — which is exactly how a hosted viewer once shipped
// a bare IssueFilter and silently served in_progress rows as "ready" — is not
// writable from here.
//
// That is a MACHINE claim, not a convention, and it takes two rules because
// there are two ways to build a filter. The depguard rule
// httpapi-transport-boundary denies internal/workapi from this package's
// non-test files, so the builders are not importable here at all. That alone
// would leave the door the hosted viewer actually walked through: it did not
// misuse a builder, it hand-rolled `IssueFilter{}` — and internal/types is
// reachable from here, for CheckFieldLen and IssueWithCounts below. So the
// forbidigo rule in .golangci.yml denies NAMING types.IssueFilter or
// types.WorkFilter in this package at all — every file of it, so a file added
// here tomorrow is covered the moment it exists. Both rules run in
// `make ci-pr-lint`, which the PR workflow runs on every pull request and
// aggregates into its ci-gate job; what that gate is and is not worth as
// enforcement is stated once, in doc.go.
//
// `bd ready` and `bd list` still call the builders directly, for the reasons
// issueops.Reader's doc comment sets out. They are not unguarded for it: they
// build from the same request types through the same builders, and they run
// the same workapi.FinishPage epilogue the reader below runs — `bd list` in
// every mode but the hierarchical --parent tree, `bd ready` on its proxied
// route only. Where both hold, what a CLI listing and one of these bodies can
// differ by is presentation, not the query and not the page. `bd show --json`
// is on the role outright, on both its routes.
//
// What DOES stay here is transport: parameter decoding, the opaque cursor
// codec, the bind-mode refusal of an unlimited read, and the wire envelopes.

// handleReady answers GET /v0/beads/ready.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())

	req := readyFilters(q)

	// EXPLICIT, always. The storage layer maps an EMPTY sort policy to hybrid,
	// and forwarding an absent `sort` as "" would silently adopt that fallback
	// while the document still read `default: priority`. It is not a cosmetic
	// difference: hybrid demotes older high-priority work, so the item SET
	// changes as soon as `limit` truncates — and only for the clients this API
	// exists to migrate off `bd ready`, whose flag registers a concrete default
	// and never sends empty.
	req.Sort = q.oneOf("sort", readySortDefault, "hybrid", "priority", "oldest")
	req.Limit = q.limit()

	if !s.acceptQuery(w, r, q) {
		return
	}
	if !s.allowUnlimited(w, r, req.Limit) {
		return
	}

	rd, err := s.reader(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	page, err := rd.Ready(r.Context(), req)
	if err != nil {
		s.failReadErr(w, r, err)
		return
	}
	writeJSON(w, apigen.ReadyPage{
		Items:   wireItems(page.Items),
		HasMore: page.HasMore,
	})
}

// readyFilters decodes the ready-work filter vocabulary the two ready
// operations share — everything except the PAGE, which only the listing has,
// and the ORDER, which only the listing needs.
//
// It is one function because the two operations must admit exactly the same
// filters: countReadyWork answers with the size of the set listReadyWork
// returns, and a parameter one of them decoded and the other did not would
// make that identity false for any client that sent it.
func readyFilters(q *query) issueops.ReadyRequest {
	return issueops.ReadyRequest{
		Assignee:     q.str("assignee"),
		Unassigned:   q.boolean("unassigned"),
		IssueType:    q.str("type"),
		ExcludeTypes: q.list("exclude_type"),

		Labels:        q.list("label"),
		LabelsAny:     q.list("label_any"),
		ExcludeLabels: q.list("exclude_label"),
		LabelPattern:  q.str("label_pattern"),
		LabelRegex:    q.str("label_regex"),

		Priority: q.integer("priority"),
		ParentID: q.str("parent"),

		MetadataFields: q.metadataFields("metadata_field"),
		HasMetadataKey: q.str("has_metadata_key"),

		IncludeEphemeral: q.boolean("include_ephemeral"),
		IncludeDeferred:  q.boolean("include_deferred"),
	}
}

// handleCountReady answers GET /v0/beads/ready:count.
//
// THE REQUEST IS THE LISTING'S REQUEST with the page taken off, which is what
// makes the answer the size of the page the listing would return. It is not
// assembled here beyond that: the role refuses a Limit and an Offset itself
// (issueops.ReadyCounter.CountReady), so this handler could not smuggle a
// bounded count past it even if a parameter for one existed.
//
// THE SORT IS SENT ANYWAY, and the operation publishes no parameter for it. A
// count has no order, but the request still has to be one the builder accepts,
// and sending "" would adopt the storage layer's hybrid fallback that no front
// door relies on.
func (s *Server) handleCountReady(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())

	req := readyFilters(q)
	req.Sort = readySortDefault

	if !s.acceptQuery(w, r, q) {
		return
	}

	counter, err := s.readyCounter(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := counter.CountReady(r.Context(), req)
	if err != nil {
		s.failReadErr(w, r, err)
		return
	}
	writeJSON(w, apigen.ReadyCount{Total: result.Total})
}

// readySortDefault is the ordering this operation applies when `sort` is
// absent. It is the same value `bd ready --sort` registers.
//
// Three things have to agree: the frozen document, the CLI flag, and this.
// TestDefaultsMatchCLIFlags compares all three against each other and
// TestReadyForwardsAnExplicitSortPolicy asserts the LITERAL "priority" on the
// filter the handler built — deliberately not this constant, because an
// assertion against the value the handler itself read would hold for every
// value it could take.
const readySortDefault = "priority"

// handleListIssues answers GET /v0/beads/issues.
func (s *Server) handleListIssues(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())

	req := issueops.ListRequest{
		Status:    q.csv("status"),
		IssueType: q.str("type"),
		Assignee:  q.str("assignee"),

		Labels:        q.list("label"),
		LabelsAny:     q.list("label_any"),
		ExcludeLabels: q.list("exclude_label"),

		ParentID: q.str("parent"),

		AllFlag:          q.boolean("all"),
		IncludeTemplates: q.boolean("include_templates"),
		IncludeGates:     q.boolean("include_gates"),
		IncludeInfra:     q.boolean("include_infra"),

		CreatedBefore: q.timestamp("created_before"),
		CreatedAfter:  q.timestamp("created_after"),

		MetadataFields: q.metadataFields("metadata_field"),
		HasMetadataKey: q.str("has_metadata_key"),

		// ORDERING IS FIXED AND DIVERGES FROM `bd list` DELIBERATELY, which is
		// why there is no `sort` parameter to decode. The cursor is a keyset
		// position in the created order, so a first page under `bd list`'s
		// priority-first default would make the second page skip and duplicate
		// rows. The order is welded to the cursor contract.
		SortBy: "created",

		Limit: q.limit(),
	}

	token := q.str("cursor")

	if !s.acceptQuery(w, r, q) {
		return
	}
	if token != "" {
		pos, ok := decodeCursor(token)
		if !ok {
			requestInfo(r.Context()).refuse(token)
			s.fail(w, r, InvalidCursor())
			return
		}
		req.AfterCreatedAt = &pos.CreatedAt
		req.AfterID = pos.ID
	}
	if !s.allowUnlimited(w, r, req.Limit) {
		return
	}

	rd, err := s.reader(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	page, err := rd.List(r.Context(), req)
	if err != nil {
		s.failReadErr(w, r, err)
		return
	}

	body := apigen.IssuesPage{
		Items:   wireItems(page.Items),
		HasMore: page.HasMore,
	}
	if page.HasMore {
		// Present if and only if has_more, which the document states as a
		// biconditional: a client that sees one and not the other has no way
		// to know whether paging is finished.
		if next := cursorFor(page.Items); next != "" {
			body.NextCursor = &next
		}
	}
	writeJSON(w, body)
}

// handleQueryIssues answers GET /v0/beads/issues:query.
//
// The EXPRESSION IS NOT PARSED HERE. Parsing, evaluation, the predicate
// decision and the scan bound all live inside the role, so this handler cannot
// make the truncating one: it reads five parameters and hands the sentence
// over.
func (s *Server) handleQueryIssues(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())

	req := issueops.QueryRequest{
		Expression:    q.str("q"),
		IncludeClosed: q.boolean("all"),
		SortBy:        q.oneOf("sort", "", querySorts...),
		Reverse:       q.boolean("reverse"),
		Limit:         q.limit(),
		// No Offset. The document publishes no parameter for one, because the
		// two database sources this server can be built on disagree about
		// whether they can honor it — see issueops.QueryRequest.Offset.
	}

	if !s.acceptQuery(w, r, q) {
		return
	}
	if !s.allowUnlimited(w, r, req.Limit) {
		return
	}

	qr, err := s.querier(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	page, err := qr.Query(r.Context(), req)
	if err != nil {
		s.failReadErr(w, r, err)
		return
	}
	writeJSON(w, apigen.QueryPage{
		Items:   wireItems(page.Items),
		HasMore: page.HasMore,
	})
}

// querySorts is the display-order vocabulary this operation publishes, in the
// document's order. It is the set workapi.CompareIssuesBy can order by; a value
// outside it is refused here rather than accepted and ignored, because a page
// returned unordered under a sort the caller named is indistinguishable from
// one whose order the caller does not understand.
var querySorts = []string{"priority", "created", "updated", "closed", "status", "id", "title", "type", "assignee"}

// handleGetIssue answers GET /v0/beads/issues/{id}.
func (s *Server) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())

	// Both default off, so a request that names neither builds the request this
	// handler built when the operation had no parameters at all.
	req := issueops.GetRequest{
		IncludeComments:   q.boolean("include_comments"),
		IncludeDependents: q.boolean("include_dependents"),
	}

	// Before the id bound, which is the order this operation had when
	// requireNoQuery ran first: a refused query string is a 400 that names what
	// to fix, and deciding the id first would answer it with a 404 instead.
	if !s.acceptQuery(w, r, q) {
		return
	}
	id := r.PathValue("id")

	// The id is bounded HERE, before the request buys a concurrency slot and a
	// database round trip, exactly as the claim route bounds its own. The
	// column is VARCHAR(255) and the document calls this an exact canonical
	// id, so a longer one — or one carrying a control character, which a
	// percent-escape in the path decodes to — names no row that can exist.
	// The refusal is the SAME 404 a real miss gets: a distinct answer would
	// let a caller map this server's notion of a well-formed id, and there is
	// nothing to learn from it.
	if id == "" || types.CheckFieldLen("id", id) != nil || strings.ContainsFunc(id, isControlChar) {
		s.fail(w, r, NotFound())
		return
	}

	req.ID = id

	rd, err := s.reader(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	details, err := rd.Get(r.Context(), req)
	if err != nil {
		s.failReadErr(w, r, err)
		return
	}
	writeJSON(w, *details)
}

// acceptQuery answers a refused query string and reports whether the request
// may proceed.
func (s *Server) acceptQuery(w http.ResponseWriter, r *http.Request, q *query) bool {
	res := q.result()
	if res == nil {
		return true
	}
	requestInfo(r.Context()).refuse(res.offender())
	s.fail(w, r, *res)
	return false
}

// allowUnlimited enforces the one mode-dependent refusal on this surface: an
// unlimited read buffers the whole active set and its JSON encoding inside one
// shared process, which must not be reachable by arbitrary network peers.
//
// The bind mode is deliberately NOT advertised in ContextResponse. A client
// that wants an unlimited read asks for one and, on this 400, re-issues with
// an explicit limit; it is a client-side fix, never a retry.
func (s *Server) allowUnlimited(w http.ResponseWriter, r *http.Request, limit *int) bool {
	if !s.cfg.AllowNonLoopback || limit == nil || *limit != 0 {
		return true
	}
	requestInfo(r.Context()).refuse("0")
	s.fail(w, r, InvalidArgument("limit", ReasonInvalidValue,
		"unlimited reads are loopback-only; pass an explicit limit"))
	return false
}

// failReadErr answers a failed read. It exists so that a filter-construction
// refusal — an unknown status, an issue type outside the workspace vocabulary,
// an invalid metadata key — reaches the client as the 400 it is rather than as
// a 500, while every storage failure keeps going through the one mapping in
// problem.go.
//
// The builders are the authority on what those messages say (the CLI prints
// them verbatim), so the detail is the builder's own text: it reflects the
// caller's own input back, which is what a 4xx detail is for.
func (s *Server) failReadErr(w http.ResponseWriter, r *http.Request, err error) {
	if param, ok := invalidFilterParam(err); ok {
		requestInfo(r.Context()).refuse(param)
		s.fail(w, r, InvalidArgument(param, ReasonInvalidValue, err.Error()))
		return
	}
	s.failErr(w, r, err)
}

// invalidFilterParam maps a filter-construction failure back to the query
// parameter that caused it.
//
// It matches on the builder's message prefixes, and that is a deliberate,
// bounded exception to this package's "never classify by prose" rule: these
// are THIS repository's own error strings, not a foreign library's. The
// alternative — typed errors for each — is the right end state and is a change
// to internal/workapi, not to the wire. A message that stops matching degrades
// to a 500, which is loud, rather than to a wrong 400.
//
// Every row here is driven end to end by a case in
// TestABuilderRefusalIsTheDocumentedBadRequest, so a reworded builder message
// fails a test rather than silently demoting its parameter to a 500. That
// test IS the pin: the builders' golden files record successful filters only,
// and cannot see a message at all.
func invalidFilterParam(err error) (string, bool) {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "invalid status "):
		return "status", true
	case strings.HasPrefix(msg, "invalid issue type "):
		return "type", true
	case strings.HasPrefix(msg, "invalid sort policy "):
		return "sort", true
	case strings.HasPrefix(msg, "invalid metadata field key"):
		return "metadata_field", true
	case strings.HasPrefix(msg, "invalid metadata key filter"):
		return "has_metadata_key", true
	// The query role's refusal. It is the caller's SENTENCE being wrong, which
	// is a 400 on `q` rather than the 500 an unclassified error would give.
	case strings.HasPrefix(msg, "invalid query expression"):
		return "q", true
	}
	return "", false
}

// wireItems projects the reader's page onto the generated envelope's element
// type.
//
// The element type is an ALIAS of types.IssueWithCounts — the same struct the
// CLI's --json marshals — so this copies a header and shares everything under
// it. There is no second wire struct here and there must never be one: that is
// what keeps `bd list --json` and this body one compatibility domain.
func wireItems(items []*types.IssueWithCounts) []apigen.IssueWithCounts {
	out := make([]apigen.IssueWithCounts, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, *item)
	}
	return out
}
