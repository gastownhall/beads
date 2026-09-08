package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The pins for the two include parameters on GET /v0/beads/issues/{id}.
//
// The handler is a decoder and nothing else — the row lists are the ROLE's to
// populate — so the property under test is that each parameter reaches
// issueops.GetRequest and that neither is set when the caller did not ask.
// Both halves are asserted: the request the role received, and the body the
// client got, because a flag forwarded to a role whose answer nobody reads
// would satisfy the first alone.

// includeAwareReader answers a detail view the way the role contract says a
// reader answers one: the two expensive row lists are present when the request
// asked for them and absent when it did not — the contract
// backend/conformance/reader_contract.go holds the real implementations to. It
// models that and nothing else, because that is exactly what these parameters
// select.
//
// Both lists carry TWO rows, and they differ on every member the wire schema
// says a row has. One row cannot show order, and rows that agree on a member
// cannot show that member survived: a handler that re-marshalled comments
// through a struct without Author, or stamped one edge type onto every
// dependent, would answer a single-row fixture indistinguishably from a
// correct one.
func seededComments(issueID string) []*types.Comment {
	return []*types.Comment{
		{
			ID:        "c-1",
			IssueID:   issueID,
			Author:    "alice",
			Text:      "the first comment body",
			CreatedAt: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
		},
		{
			ID:        "c-2",
			IssueID:   issueID,
			Author:    "bob",
			Text:      "the second comment body",
			CreatedAt: time.Date(2026, 7, 31, 12, 0, 5, 0, time.UTC),
		},
	}
}

// seededDependents carries two DIFFERENT edge types because the type is what
// `bd show` groups its dependents by: a surface that flattened every edge to
// one type would still render, into the wrong section.
func seededDependents() []*types.IssueWithDependencyMetadata {
	return []*types.IssueWithDependencyMetadata{
		{
			Issue:          *seededIssue("bd-2", "", types.StatusOpen),
			DependencyType: types.DepBlocks,
		},
		{
			Issue:          *seededIssue("bd-3", "", types.StatusOpen),
			DependencyType: types.DepParentChild,
		},
	}
}

type includeAwareReader struct {
	roleReader
}

func (r *includeAwareReader) Get(ctx context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	if _, err := r.roleReader.Get(ctx, req); err != nil {
		return nil, err
	}
	two := int64(2)
	omitted := true
	details := &issueops.IssueDetails{
		Issue:           *seededIssue(req.ID, "", types.StatusOpen),
		CommentCount:    &two,
		DependentCount:  &two,
		CommentsOmitted: &omitted,
	}
	if req.IncludeComments {
		details.Comments = seededComments(req.ID)
		// Never set alongside a populated list: the flag exists to tell "no
		// comments" from "not asked for", and a caller that asked has neither
		// question.
		details.CommentsOmitted = nil
	}
	if req.IncludeDependents {
		details.Dependents = seededDependents()
	}
	return details, nil
}

func newGetIssueServer(t *testing.T) (*testServer, *includeAwareReader) {
	t.Helper()
	rd := &includeAwareReader{}
	return newTestServer(t, rolesConfig(Config{Reader: rd})), rd
}

// TestGetIssueWithoutTheIncludeParametersIsUnchanged is the no-regression half
// of adding them: a caller that sends neither gets the request the role saw
// before this operation had parameters at all, so nothing about the response
// can have moved.
//
// The second half asserts the DEFAULT rather than the decode. Spelling both
// parameters `false` must answer the same body as omitting them; a default
// that drifted to true would still satisfy the explicit-spelling cases below.
func TestGetIssueWithoutTheIncludeParametersIsUnchanged(t *testing.T) {
	ts, rd := newGetIssueServer(t)

	resp := ts.get(t, "/v0/beads/issues/bd-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	omitted := decodeBody(t, resp)

	reqs := rd.getRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d detail reads, want 1", len(reqs))
	}
	if want := (issueops.GetRequest{ID: "bd-1"}); reqs[0] != want {
		t.Errorf("GetRequest = %+v, want %+v — a caller that asks for neither row list must not pay for either", reqs[0], want)
	}
	if _, ok := omitted["comments"]; ok {
		t.Error("`comments` is populated without include_comments")
	}
	if _, ok := omitted["dependents"]; ok {
		t.Error("`dependents` is populated without include_dependents")
	}

	explicit := ts.get(t, "/v0/beads/issues/bd-1?include_comments=false&include_dependents=false")
	if explicit.StatusCode != http.StatusOK {
		t.Fatalf("explicit false: status = %d, want 200: %s", explicit.StatusCode, readAll(t, explicit))
	}
	if got := decodeBody(t, explicit); !reflect.DeepEqual(got, omitted) {
		t.Errorf("spelling both parameters false answered %v, want the body an omitted parameter answers: %v", got, omitted)
	}
}

// TestGetIssueIncludeParametersReachTheRole drives each parameter alone and
// both together. Alone matters: the two row lists are independent reads, and a
// handler that decoded one into the other's field would answer every
// single-parameter request with the wrong list and still look correct on the
// both-parameters case.
//
// The values exercise strconv.ParseBool's vocabulary rather than "true" three
// times, because that vocabulary is what the shared decoder publishes.
func TestGetIssueIncludeParametersReachTheRole(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  issueops.GetRequest
	}{
		{
			name:  "comments alone",
			query: "?include_comments=true",
			want:  issueops.GetRequest{ID: "bd-1", IncludeComments: true},
		},
		{
			name:  "dependents alone",
			query: "?include_dependents=1",
			want:  issueops.GetRequest{ID: "bd-1", IncludeDependents: true},
		},
		{
			name:  "both",
			query: "?include_comments=TRUE&include_dependents=t",
			want:  issueops.GetRequest{ID: "bd-1", IncludeComments: true, IncludeDependents: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, rd := newGetIssueServer(t)

			resp := ts.get(t, "/v0/beads/issues/bd-1"+tc.query)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}

			reqs := rd.getRequests()
			if len(reqs) != 1 {
				t.Fatalf("%d detail reads, want 1", len(reqs))
			}
			if reqs[0] != tc.want {
				t.Errorf("GetRequest = %+v, want %+v", reqs[0], tc.want)
			}

			body := decodeBody(t, resp)
			if _, ok := body["comments"]; ok != tc.want.IncludeComments {
				t.Errorf("`comments` present = %v, want %v", ok, tc.want.IncludeComments)
			}
			if _, ok := body["dependents"]; ok != tc.want.IncludeDependents {
				t.Errorf("`dependents` present = %v, want %v", ok, tc.want.IncludeDependents)
			}
			if tc.want.IncludeComments {
				if _, ok := body["comments_omitted"]; ok {
					t.Error("`comments_omitted` is set on a response that carries the comment bodies")
				}
			}
		})
	}
}

// TestGetIssueIncludeLegsCarryTheWireShape is the half the presence checks
// above cannot reach: what is IN the two lists once a caller has paid for them.
//
// Presence is the cheap property. The row CONTENT is the expensive one and the
// one clients read — `bd show` prints each comment's author and time in the
// order it received them, and GROUPS dependents by edge type — so a surface
// that answered the rows in a different order, dropped Author, or flattened
// every dependent onto one edge type would satisfy every other test in this
// file and still render wrong. Both lists are asserted row by row, by index,
// because the index IS the order promise: the handler is a projection of the
// role's answer and may not sort, filter or reshape it.
//
// The required-member sweep is deliberately DERIVED from the document rather
// than listed here. A member added to `Comment` or
// `IssueWithDependencyMetadata` tomorrow is covered the moment the schema
// declares it, without an edit to this test — which is the only version of this
// check that cannot go stale.
func TestGetIssueIncludeLegsCarryTheWireShape(t *testing.T) {
	ts, _ := newGetIssueServer(t)

	resp := ts.get(t, "/v0/beads/issues/bd-1?include_comments=true&include_dependents=true")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)

	comments := objectRows(t, body, "comments")
	wantComments := seededComments("bd-1")
	if len(comments) != len(wantComments) {
		t.Fatalf("%d comments, want %d — the handler answers the role's rows, all of them", len(comments), len(wantComments))
	}
	for i, got := range comments {
		want := wantComments[i]
		if got["id"] != want.ID {
			t.Errorf("comments[%d].id = %v, want %q — the rows are answered in the role's order", i, got["id"], want.ID)
		}
		if got["issue_id"] != want.IssueID {
			t.Errorf("comments[%d].issue_id = %v, want %q", i, got["issue_id"], want.IssueID)
		}
		if got["author"] != want.Author {
			t.Errorf("comments[%d].author = %v, want %q", i, got["author"], want.Author)
		}
		if got["text"] != want.Text {
			t.Errorf("comments[%d].text = %v, want %q", i, got["text"], want.Text)
		}
		if stamp := want.CreatedAt.Format(time.RFC3339); got["created_at"] != stamp {
			t.Errorf("comments[%d].created_at = %v, want %q", i, got["created_at"], stamp)
		}
	}
	requireDocumentedMembers(t, "Comment", comments)

	dependents := objectRows(t, body, "dependents")
	wantDependents := seededDependents()
	if len(dependents) != len(wantDependents) {
		t.Fatalf("%d dependents, want %d", len(dependents), len(wantDependents))
	}
	for i, got := range dependents {
		want := wantDependents[i]
		if got["id"] != want.ID {
			t.Errorf("dependents[%d].id = %v, want %q — the rows are answered in the role's order", i, got["id"], want.ID)
		}
		if got["dependency_type"] != string(want.DependencyType) {
			t.Errorf("dependents[%d].dependency_type = %v, want %q — the edge type is what `bd show` groups this row by",
				i, got["dependency_type"], want.DependencyType)
		}
	}
	requireDocumentedMembers(t, "IssueWithDependencyMetadata", dependents)
}

// objectRows reads one array-of-objects member out of a decoded body.
func objectRows(t *testing.T, body map[string]any, member string) []map[string]any {
	t.Helper()
	raw, ok := body[member].([]any)
	if !ok {
		t.Fatalf("`%s` is %T, want an array of objects: %v", member, body[member], body[member])
	}
	rows := make([]map[string]any, 0, len(raw))
	for i, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("`%s[%d]` is %T, want an object", member, i, item)
		}
		rows = append(rows, row)
	}
	return rows
}

// requireDocumentedMembers asserts that every row carries every member the
// document marks REQUIRED on its schema. Read from the spec, so a schema that
// grows a required member the serve path cannot fill fails here rather than at
// a client.
func requireDocumentedMembers(t *testing.T, schema string, rows []map[string]any) {
	t.Helper()
	doc := loadSpec(t)
	node := mapAt(t, mapAt(t, mapAt(t, doc, "components"), "schemas"), schema)
	required := toStrings(t, node["required"])
	if len(required) == 0 {
		t.Fatalf("the %s schema declares no required members; re-point this guard at the document's row shape", schema)
	}
	if len(rows) == 0 {
		t.Fatalf("no %s rows to check", schema)
	}
	for i, row := range rows {
		for _, member := range required {
			if value, ok := row[member]; !ok || value == nil {
				t.Errorf("%s[%d] omits `%s`, which the document marks required: %v", schema, i, member, row)
			}
		}
	}
}

// TestGetIssueRefusesAMalformedIncludeParameter: a bad boolean is now this
// operation's OWN 400, not the document-level unknown-parameter rule, and the
// two carry opposite client recoveries — `invalid_value` says "send something
// else", `unknown_parameter` says "this server is older than you think".
func TestGetIssueRefusesAMalformedIncludeParameter(t *testing.T) {
	for _, param := range []string{"include_comments", "include_dependents"} {
		t.Run(param, func(t *testing.T) {
			ts, rd := newGetIssueServer(t)

			resp := ts.get(t, "/v0/beads/issues/bd-1?"+param+"=maybe")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != param || body["reason"] != string(ReasonInvalidValue) {
				t.Errorf("body = %v, want invalid_argument on param %s with reason invalid_value", body, param)
			}
			if n := len(rd.getRequests()); n != 0 {
				t.Errorf("%d detail reads ran; a refused request must not reach the database", n)
			}
		})
	}
}

// TestGetIssueStaysStrictAboutUnknownParameters is the guard on what having a
// parameter table could have cost. This operation used to reject every query
// key outright; now the table is the whole allowlist, and a key outside it must
// still be refused by name — silently ignoring one would hand a client the
// count-only body it believed it had asked to have filled in.
//
// The last case is the interaction: a request carrying BOTH a malformed known
// parameter and an unknown one is reported as the malformed value, because
// reporting it as version skew would send the client to degrade a parameter
// this server does in fact have.
func TestGetIssueStaysStrictAboutUnknownParameters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		param  string
		reason Reason
	}{
		{"an unknown key alone", "?bogus=1", "bogus", ReasonUnknownParameter},
		{"an unknown key beside a known one", "?include_comments=true&bogus=1", "bogus", ReasonUnknownParameter},
		{"a near miss on a real parameter", "?include_comment=true", "include_comment", ReasonUnknownParameter},
		{"a malformed known key beside an unknown one", "?include_comments=maybe&bogus=1", "include_comments", ReasonInvalidValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, rd := newGetIssueServer(t)

			resp := ts.get(t, "/v0/beads/issues/bd-1"+tc.query)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != tc.param || body["reason"] != string(tc.reason) {
				t.Errorf("body = %v, want invalid_argument on param %s with reason %s", body, tc.param, tc.reason)
			}
			if n := len(rd.getRequests()); n != 0 {
				t.Errorf("%d detail reads ran; a refused request must not reach the database", n)
			}
		})
	}
}

// TestGetIssueAnswersAQueryRefusalBeforeTheIDBound pins the order the operation
// already had. When it took no parameters, the query string was refused first,
// so an unknown key on an id no row could hold was a 400 — and a client that
// sends one gets the answer that tells it what to fix. Deciding the id first
// would turn that request into a 404 and lose the refusal.
func TestGetIssueAnswersAQueryRefusalBeforeTheIDBound(t *testing.T) {
	ts, rd := newGetIssueServer(t)

	resp := ts.get(t, "/v0/beads/issues/bd-%01?bogus=1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["param"] != "bogus" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("body = %v, want param=bogus reason=unknown_parameter", body)
	}
	if n := len(rd.getRequests()); n != 0 {
		t.Errorf("%d detail reads ran for a refused request", n)
	}
}

// revisionReader answers a detail view built the way the seam builds one — the
// token PROJECTED off the row — so the handler is exercised against the shape
// production hands it rather than against a literal that sets the member by
// hand.
type revisionReader struct {
	roleReader
	token int64
}

func (r *revisionReader) Get(ctx context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	if _, err := r.roleReader.Get(ctx, req); err != nil {
		return nil, err
	}
	issue := seededIssue(req.ID, "", types.StatusOpen)
	issue.RowVersion = r.token
	return types.NewIssueDetails(*issue), nil
}

// TestGetIssuePublishesTheRevisionToken is the wire half of the read-side
// token: the value the role's row carries arrives on the response under
// `revision`, and it arrives for a legacy-zero row too.
//
// The token is decoded as a 64-BIT INTEGER rather than through `any`, and the
// large case is chosen past 2^53 on purpose. A live row_lock runs there, where
// a float64's ulp is already 64, so a body read through the default JSON
// decoding yields a number NEAR the token and not the token — and every guard
// composed from it is refused against a row nothing else touched. A test that
// compared float64s would pass on a server that had silently narrowed the
// member to a double.
func TestGetIssuePublishesTheRevisionToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token int64
	}{
		{"a live token past 2^53", 9007199254740993},
		// 0 is the migration-0054 backfill value: a real, comparable token a
		// guarded client must be able to read and send back. It is emitted
		// rather than omitted, or an absent member would be ambiguous between
		// a legacy row and a server with no token to give.
		{"a legacy un-mutated row", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rd := &revisionReader{token: tc.token}
			ts := newTestServer(t, rolesConfig(Config{Reader: rd}))

			resp := ts.get(t, "/v0/beads/issues/bd-1")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			var body struct {
				Revision *int64 `json:"revision"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decode %q: %v", raw, err)
			}
			if body.Revision == nil {
				t.Fatalf("the detail response carries no `revision`: %s", raw)
			}
			if *body.Revision != tc.token {
				t.Errorf("revision = %d, want the row's token %d", *body.Revision, tc.token)
			}
			// The storage spelling never rides along: row_lock is json:"-" on
			// the issue for a reason that still holds, and `revision` is the
			// one name this token has on the wire.
			for _, forbidden := range []string{"row_lock", "row_version", "RowVersion"} {
				if strings.Contains(string(raw), forbidden) {
					t.Errorf("the detail response leaked the storage spelling %q: %s", forbidden, raw)
				}
			}
		})
	}
}

// TestListIssuesRowsCarryNoRevision is the negative space beside the member
// above, and it is a real assertion rather than a restatement of the schema.
//
// types.IssueWithCounts is also the record `bd export` writes to JSONL, so a
// token on that element would put a per-write-random value into a git-tracked
// file — the loss types.Issue.RowVersion's json:"-" exists to prevent.
//
// Neither existing gate covers it, and both were measured against the
// mutation: adding `Revision int64 json:"revision"` to types.IssueWithCounts
// leaves types.TestRowVersionNeverSerialized GREEN, because the new field is a
// SEPARATE one that nothing populates and its zero carries none of the
// forbidden spellings. TestWireTagBijection goes red only while the document
// has not caught up — a slice that adds the field AND the schema property has
// both surfaces agreeing on the wrong answer. This case is what stays red.
func TestListIssuesRowsCarryNoRevision(t *testing.T) {
	issue := seededIssue("bd-1", "", types.StatusOpen)
	issue.RowVersion = 9007199254740993
	rd := &roleReader{page: issueops.IssuePage{
		Items: []*types.IssueWithCounts{{Issue: issue}},
	}}
	ts := newTestServer(t, rolesConfig(Config{Reader: rd}))

	resp := ts.get(t, "/v0/beads/issues?limit=10")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	for _, forbidden := range []string{"revision", "row_lock", "9007199254740993"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("a list row carries %q; the token stops at the detail read, "+
				"because this element is also the JSONL interchange record: %s", forbidden, raw)
		}
	}
}

// TestGetIssueBriefDepsReachesTheRequest is the HTTP half of #5546. The CLI and
// this handler build GetRequest separately, so wiring one leaves the field
// unreachable from the other.
func TestGetIssueBriefDepsReachesTheRequest(t *testing.T) {
	ts, rd := newGetIssueServer(t)

	if resp := ts.get(t, "/v0/beads/issues/bd-1?brief_deps=true"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	reqs := rd.getRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d detail reads, want 1", len(reqs))
	}
	if want := (issueops.GetRequest{ID: "bd-1", BriefDeps: true}); reqs[0] != want {
		t.Errorf("GetRequest = %+v, want %+v", reqs[0], want)
	}
}
