package gqlapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	graphql "github.com/graph-gophers/graphql-go"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// fakeReader answers Get from a fixed map; err, when set, is returned for
// every Get instead.
type fakeReader struct {
	issues    map[string]*types.IssueDetails
	err       error
	gets      []issueops.GetRequest
	readyReqs []issueops.ReadyRequest
	readyPage issueops.IssuePage
	readyErr  error
}

func (f *fakeReader) Ready(_ context.Context, req issueops.ReadyRequest) (issueops.IssuePage, error) {
	f.readyReqs = append(f.readyReqs, req)
	return f.readyPage, f.readyErr
}

func (f *fakeReader) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, errors.New("fakeReader: List not expected")
}

func (f *fakeReader) Get(_ context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	f.gets = append(f.gets, req)
	if f.err != nil {
		return nil, f.err
	}
	if d, ok := f.issues[req.ID]; ok {
		return d, nil
	}
	return nil, issueops.ErrNotFound
}

// run executes document and decodes the response's JSON form, the form bd
// prints.
func run(t *testing.T, rd issueops.Reader, document string) (map[string]any, []any) {
	t.Helper()
	resp := Execute(context.Background(), Roles{Reader: rd}, document, nil)
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var out struct {
		Data   map[string]any `json:"data"`
		Errors []any          `json:"errors"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	return out.Data, out.Errors
}

func TestGQLIssueTracerFields(t *testing.T) {
	created := time.Date(2026, 9, 25, 17, 24, 18, 0, time.UTC)
	rd := &fakeReader{issues: map[string]*types.IssueDetails{
		"bd-1": {Issue: types.Issue{
			ID: "bd-1", Title: "One", Status: types.StatusOpen, Priority: 2, CreatedAt: created,
			Metadata: json.RawMessage(`{"k":[1,"two"]}`), Timeout: 2 * time.Hour,
		}},
	}}
	data, errs := run(t, rd, `{ issue(id: "bd-1") { id title status priority created_at metadata timeout } }`)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	got, _ := json.Marshal(data["issue"])
	want := `{"created_at":"2026-09-25T17:24:18Z","id":"bd-1","metadata":{"k":[1,"two"]},"priority":2,"status":"open","timeout":7200000000000,"title":"One"}`
	if string(got) != want {
		t.Fatalf("issue = %s\nwant    %s", got, want)
	}
	if len(rd.gets) != 1 || !rd.gets[0].BriefDeps {
		t.Fatalf("Get requests = %+v, want one with BriefDeps", rd.gets)
	}
}

func TestGQLIssueEmptyMetadataIsNull(t *testing.T) {
	rd := &fakeReader{issues: map[string]*types.IssueDetails{"bd-1": {Issue: types.Issue{ID: "bd-1"}}}}
	data, errs := run(t, rd, `{ issue(id: "bd-1") { metadata } }`)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if m := data["issue"].(map[string]any); m["metadata"] != nil {
		t.Fatalf("metadata = %v, want null", m["metadata"])
	}
}

func TestGQLIssueNotFoundIsNull(t *testing.T) {
	data, errs := run(t, &fakeReader{}, `{ issue(id: "missing-1") { id } }`)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if v, ok := data["issue"]; !ok || v != nil {
		t.Fatalf("data = %v, want issue: null", data)
	}
}

func TestGQLIssueRoleErrorIsReported(t *testing.T) {
	rd := &fakeReader{err: errors.New("backend unavailable")}
	data, errs := run(t, rd, `{ issue(id: "bd-1") { id } }`)
	if len(errs) != 1 || !strings.Contains(errs[0].(map[string]any)["message"].(string), "backend unavailable") {
		t.Fatalf("errors = %v, want the role message", errs)
	}
	if data["issue"] != nil {
		t.Fatalf("data = %v, want issue: null", data)
	}
}

func TestGQLQueryLengthLimit(t *testing.T) {
	rd := &fakeReader{}
	ok := `{ issue(id: "x") { id } }`
	pad := strings.Repeat(" ", maxQueryLength-len(ok))
	if _, errs := run(t, rd, ok+pad); len(errs) != 0 {
		t.Fatalf("document of exactly %d bytes: errors %v", maxQueryLength, errs)
	}
	if _, errs := run(t, rd, ok+pad+" "); len(errs) == 0 {
		t.Fatalf("document of %d bytes: want a length error", maxQueryLength+1)
	}
}

func TestGQLHasNoMutationType(t *testing.T) {
	_, errs := run(t, &fakeReader{}, `mutation { issue(id: "x") { id } }`)
	if len(errs) == 0 {
		t.Fatal("mutation document: want an error")
	}
}

type fakeQuerier struct {
	reqs []issueops.QueryRequest
	page issueops.IssuePage
	err  error
}

func (f *fakeQuerier) Query(_ context.Context, req issueops.QueryRequest) (issueops.IssuePage, error) {
	f.reqs = append(f.reqs, req)
	return f.page, f.err
}
func runRoles(t *testing.T, roles Roles, document string) (map[string]any, []any) {
	t.Helper()
	raw, e := json.Marshal(Execute(context.Background(), roles, document, nil))
	if e != nil {
		t.Fatal(e)
	}
	var out struct {
		Data   map[string]any `json:"data"`
		Errors []any          `json:"errors"`
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		t.Fatalf("decode %s: %v", raw, e)
	}
	return out.Data, out.Errors
}
func assertNoErrors(t *testing.T, errs []any) {
	t.Helper()
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
}
func assertError(t *testing.T, errs []any, text string) {
	t.Helper()
	if len(errs) == 0 || !strings.Contains(fmt.Sprint(errs), text) {
		t.Fatalf("errors %v do not name %q", errs, text)
	}
}
func row(id string) *types.IssueWithCounts {
	return &types.IssueWithCounts{Issue: &types.Issue{ID: id, Title: id, Status: types.StatusOpen}}
}
func detail(id string) *types.IssueDetails {
	return &types.IssueDetails{Issue: types.Issue{ID: id, Title: id, Status: types.StatusOpen}, Revision: "0"}
}

func TestGQLSchemaTracksPublicJSONFields(t *testing.T) {
	data, errs := run(t, &fakeReader{}, `{ __type(name:"Issue") { fields { name } } }`)
	assertNoErrors(t, errs)
	names := map[string]bool{}
	for _, f := range data["__type"].(map[string]any)["fields"].([]any) {
		names[f.(map[string]any)["name"].(string)] = true
	}
	expected := map[string]bool{}
	for _, shape := range []any{types.Issue{}, types.IssueDetails{}, types.IssueWithCounts{}} {
		typ := reflect.TypeOf(shape)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag != "" && tag != "-" && tag != "comments_omitted" {
				expected[tag] = true
			}
		}
	}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("Issue schema drift: fields %v vs public JSON tags %v", sortedKeys(names), sortedKeys(expected))
	}
	data, errs = run(t, &fakeReader{}, `{ __type(name:"Comment") { fields { name } } }`)
	assertNoErrors(t, errs)
	names = map[string]bool{}
	for _, f := range data["__type"].(map[string]any)["fields"].([]any) {
		names[f.(map[string]any)["name"].(string)] = true
	}
	expected = map[string]bool{}
	typ := reflect.TypeOf(types.Comment{})
	for i := 0; i < typ.NumField(); i++ {
		expected[strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("Comment schema drift: %v vs %v", sortedKeys(names), sortedKeys(expected))
	}
}
func sortedKeys(m map[string]bool) []string {
	s := make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	sort.Strings(s)
	return s
}

func TestGQLRelationDetailAndShallow(t *testing.T) {
	source := detail("a")
	source.Dependencies = []*types.IssueWithDependencyMetadata{{Issue: types.Issue{ID: "b", Title: "B", Status: types.StatusOpen}, DependencyType: types.DepBlocks}}
	target := detail("b")
	target.Description = "full description"
	rd := &fakeReader{issues: map[string]*types.IssueDetails{"a": source, "b": target}}
	data, errs := run(t, rd, `{ issue(id:"a") { dependencies { id dependency_type issue { title description } } } }`)
	assertNoErrors(t, errs)
	deps := data["issue"].(map[string]any)["dependencies"].([]any)
	if len(deps) != 1 || deps[0].(map[string]any)["issue"].(map[string]any)["description"] != "full description" {
		t.Fatalf("dependency detail = %v", deps)
	}
	if len(rd.gets) != 2 || !rd.gets[0].BriefDeps || !rd.gets[1].BriefDeps {
		t.Fatalf("Get requests = %+v", rd.gets)
	}
	rd.gets = nil
	data, errs = run(t, rd, `{ issue(id:"a") { dependencies { issue { id title status } } } }`)
	assertNoErrors(t, errs)
	if len(rd.gets) != 1 || data["issue"].(map[string]any)["dependencies"].([]any)[0].(map[string]any)["issue"].(map[string]any)["title"] != "B" {
		t.Fatalf("shallow = %v, Gets=%+v", data, rd.gets)
	}
}

// A list row can be deleted between the list read and the detail read; the
// detail-only field must then fail as an error naming the issue, not a panic.
func TestGQLListRowGoneBeforeDetail(t *testing.T) {
	row := &issueops.IssueWithCounts{Issue: &types.Issue{ID: "a", Title: "A"}}
	roles := Roles{Reader: &fakeReader{}, Querier: &fakeQuerier{page: issueops.IssuePage{Items: []*issueops.IssueWithCounts{row}}}}
	_, errs := runRoles(t, roles, `{ issues { items { id revision } } }`)
	if len(errs) != 1 {
		t.Fatalf("errors = %v, want one", errs)
	}
	msg := errs[0].(map[string]any)["message"].(string)
	if strings.Contains(msg, "panic") || !strings.Contains(msg, "a") || !strings.Contains(msg, "not found") {
		t.Fatalf("message = %q, want a not-found error naming the issue", msg)
	}
}
func TestGQLSelectionOptionsAndCache(t *testing.T) {
	source := detail("a")
	source.Comments = []*types.Comment{{ID: "c", IssueID: "a", Author: "x", Text: "hello"}}
	source.Dependents = []*types.IssueWithDependencyMetadata{}
	rd := &fakeReader{issues: map[string]*types.IssueDetails{"a": source}}
	_, errs := run(t, rd, `{ first:issue(id:"a") { id } second:issue(id:"a") { id } }`)
	assertNoErrors(t, errs)
	if len(rd.gets) != 1 || rd.gets[0].IncludeDependents || rd.gets[0].IncludeComments {
		t.Fatalf("cache / minimal options = %+v", rd.gets)
	}
	rd.gets = nil
	_, errs = run(t, rd, `{ issue(id:"a") { dependents { id } comments { id } epic_total_children } }`)
	assertNoErrors(t, errs)
	if len(rd.gets) != 1 || !rd.gets[0].IncludeDependents || !rd.gets[0].IncludeComments {
		t.Fatalf("selection options = %+v", rd.gets)
	}
	rd.gets = nil
	_, errs = run(t, rd, `{ issue(id:"a") { epic_closeable } }`)
	assertNoErrors(t, errs)
	if len(rd.gets) != 1 || !rd.gets[0].IncludeDependents || rd.gets[0].IncludeComments {
		t.Fatalf("epic options = %+v", rd.gets)
	}
	rd.gets = nil
	unresolved := int64(2)
	source.UnresolvableDependents = &unresolved
	data, errs := run(t, rd, `{ issue(id:"a") { unresolvable_dependents unresolvable_dependencies } }`)
	assertNoErrors(t, errs)
	if len(rd.gets) != 1 || !rd.gets[0].IncludeDependents || rd.gets[0].IncludeComments {
		t.Fatalf("unresolvable options = %+v", rd.gets)
	}
	got := data["issue"].(map[string]any)
	if got["unresolvable_dependents"] != float64(2) || got["unresolvable_dependencies"] != nil {
		t.Fatalf("unresolvable fields = %v", got)
	}
}
func TestGQLPerSelectionCacheKeys(t *testing.T) {
	rd := &fakeReader{issues: map[string]*types.IssueDetails{"a": detail("a")}}
	_, errs := run(t, rd, `{ plain: issue(id:"a") { id } withComments: issue(id:"a") { comments { id } } repeated: issue(id:"a") { comments { id } } }`)
	assertNoErrors(t, errs)
	if len(rd.gets) != 2 || rd.gets[0].IncludeComments == rd.gets[1].IncludeComments {
		t.Fatalf("cache keys %+v", rd.gets)
	}
}

func TestGQLListSelectionGetsOnlyMissingDetails(t *testing.T) {
	rd := &fakeReader{issues: map[string]*types.IssueDetails{"a": detail("a")}}
	rd.issues["a"].Comments = []*types.Comment{{ID: "c", IssueID: "a", Author: "x", Text: "hello"}}
	qr := &fakeQuerier{page: issueops.IssuePage{Items: []*issueops.IssueWithCounts{row("a")}}}
	data, errs := runRoles(t, Roles{Reader: rd, Querier: qr}, `{ issues { items { id labels comments { text } } } }`)
	assertNoErrors(t, errs)
	if len(rd.gets) != 1 || !rd.gets[0].BriefDeps || rd.gets[0].IncludeDependents || !rd.gets[0].IncludeComments {
		t.Fatalf("list detail options %+v", rd.gets)
	}
	items := data["issues"].(map[string]any)["items"].([]any)
	if items[0].(map[string]any)["comments"].([]any)[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("list details %v", items)
	}
}

func TestGQLListRoleMappingAndRows(t *testing.T) {
	rd := &fakeReader{readyPage: issueops.IssuePage{Items: []*issueops.IssueWithCounts{row("r")}, HasMore: true}}
	rd.readyPage.Items[0].DependencyCount = 2
	qr := &fakeQuerier{page: issueops.IssuePage{Items: []*issueops.IssueWithCounts{row("q")}, HasMore: true}}
	roles := Roles{Reader: rd, Querier: qr}
	data, errs := runRoles(t, roles, `{ issues(all:true,limit:10,offset:3) { items { id } has_more } }`)
	assertNoErrors(t, errs)
	req := qr.reqs[0]
	if req.Expression != `id="*"` || !req.IncludeClosed || *req.Limit != 10 || req.Offset != 3 || !data["issues"].(map[string]any)["has_more"].(bool) {
		t.Fatalf("query request %+v, data %v", req, data)
	}
	_, errs = runRoles(t, roles, `{ issues(query:"type=bug",sort:"priority",reverse:true,limit:7) { items { id } } }`)
	assertNoErrors(t, errs)
	queryReq := qr.reqs[1]
	if queryReq.Expression != "type=bug" || queryReq.SortBy != "priority" || !queryReq.Reverse || *queryReq.Limit != 7 || queryReq.Offset != 0 {
		t.Fatalf("explicit query request %+v", queryReq)
	}
	plan, e := workapi.BuildQueryPlan(req)
	if e != nil || len(plan.Filter.IDs) != 0 || plan.Filter.IDPrefix != "" {
		t.Fatalf("unfiltered query plan: %+v, %v", plan, e)
	}
	data, errs = runRoles(t, roles, `{ ready(offset:2,limit:10,assignee:"x",type:"bug",labels:["one"],parent:"p") { items { id dependency_count } has_more } }`)
	assertNoErrors(t, errs)
	got := rd.readyReqs[0]
	if got.Sort != "priority" || got.Offset != 2 || *got.Limit != 10 || got.Assignee != "x" || got.IssueType != "bug" || !reflect.DeepEqual(got.Labels, []string{"one"}) || got.ParentID != "p" || data["ready"].(map[string]any)["items"].([]any)[0].(map[string]any)["dependency_count"] != float64(2) {
		t.Fatalf("ready request %+v, data %v", got, data)
	}
	if len(rd.gets) != 0 {
		t.Fatalf("list rows fetched details: %+v", rd.gets)
	}
}
func TestGQLReadyDefaultsAndRoleErrors(t *testing.T) {
	rd := &fakeReader{}
	data, errs := runRoles(t, Roles{Reader: rd}, `{ ready { items { id } has_more } }`)
	assertNoErrors(t, errs)
	if len(rd.readyReqs) != 1 || *rd.readyReqs[0].Limit != 100 || rd.readyReqs[0].Sort != "priority" || rd.readyReqs[0].Offset != 0 || len(data["ready"].(map[string]any)["items"].([]any)) != 0 {
		t.Fatalf("default ready %+v, %v", rd.readyReqs, data)
	}
	rd.readyErr = errors.New("ready backend failed")
	_, errs = runRoles(t, Roles{Reader: rd}, `{ ready { has_more } }`)
	assertError(t, errs, "ready backend failed")
}

func TestGQLNullArgumentsTakeDefaults(t *testing.T) {
	rd := &fakeReader{}
	qr := &fakeQuerier{}
	roles := Roles{Reader: rd, Querier: qr}
	withVars := `query($a: Boolean, $l: Int, $o: Int, $s: String) { issues(all:$a, reverse:$a, limit:$l, offset:$o) { has_more } ready(limit:$l, offset:$o, sort:$s) { has_more } }`
	for _, doc := range []string{
		`{ issues(all:null, reverse:null, limit:null, offset:null) { has_more } ready(limit:null, offset:null, sort:null) { has_more } }`,
		withVars,
	} {
		_, errs := runRoles(t, roles, doc)
		assertNoErrors(t, errs)
	}
	for _, got := range qr.reqs {
		if got.IncludeClosed || got.Reverse || *got.Limit != 50 || got.Offset != 0 {
			t.Fatalf("issues request %+v", got)
		}
	}
	for _, got := range rd.readyReqs {
		if *got.Limit != 100 || got.Offset != 0 || got.Sort != "priority" {
			t.Fatalf("ready request %+v", got)
		}
	}
	if len(qr.reqs) != 2 || len(rd.readyReqs) != 2 {
		t.Fatalf("requests: %d issues, %d ready", len(qr.reqs), len(rd.readyReqs))
	}
	// Values decoded from --vars JSON still reach the roles.
	vars := map[string]any{"a": true, "l": float64(5), "o": float64(2), "s": "created"}
	if errs := Execute(context.Background(), roles, withVars, vars).Errors; len(errs) != 0 {
		t.Fatalf("errors with variables: %v", errs)
	}
	q, r := qr.reqs[2], rd.readyReqs[2]
	if !q.IncludeClosed || !q.Reverse || *q.Limit != 5 || q.Offset != 2 || *r.Limit != 5 || r.Offset != 2 || r.Sort != "created" {
		t.Fatalf("issues %+v, ready %+v", q, r)
	}
}

func TestGQLInputRejections(t *testing.T) {
	cases := []struct{ name, doc, part string }{
		{"syntax", `{ issue(`, "syntax"},
		{"unknown", `{ issue(id:"x") { unknown_field } }`, "unknown_field"},
		{"depth", `{ issue(id:"x") { dependencies { issue { dependencies { issue { dependencies { issue { dependencies { issue { id } } } } } } } } } }`, "depth"},
		{"limit zero", `{ issues(limit:0) { has_more } }`, "limit"},
		{"limit 201", `{ ready(limit:201) { has_more } }`, "limit"},
		{"mutation", `mutation { x }`, "mutation"},
		{"subscription", `subscription { x }`, "subscription"},
		{"negative ready offset", `{ ready(offset:-1) { has_more } }`, "offset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rd := &fakeReader{}
			qr := &fakeQuerier{}
			_, errs := runRoles(t, Roles{Reader: rd, Querier: qr}, tc.doc)
			assertError(t, errs, tc.part)
			if len(rd.gets)+len(rd.readyReqs)+len(qr.reqs) != 0 {
				t.Fatalf("role called: %+v %+v %+v", rd.gets, rd.readyReqs, qr.reqs)
			}
		})
	}
	qr := &fakeQuerier{err: errors.New("invalid query expression: unexpected @")}
	_, errs := runRoles(t, Roles{Querier: qr}, `{ issues(query:"@") { has_more } }`)
	assertError(t, errs, "invalid query expression")
	qr.err = errors.New("offset cannot be combined with a display order")
	_, errs = runRoles(t, Roles{Querier: qr}, `{ issues(query:"type=bug",offset:1,sort:"priority") { has_more } }`)
	assertError(t, errs, "offset")
}
func TestGQLIssuesByIdOrderRepeatsAndLimit(t *testing.T) {
	rd := &fakeReader{issues: map[string]*types.IssueDetails{"a": detail("a"), "b": detail("b")}}
	data, errs := run(t, rd, `{ issuesById(ids:["b","missing-1","a","b"]) { id title status assignee } }`)
	assertNoErrors(t, errs)
	items := data["issuesById"].([]any)
	if len(items) != 4 || items[0].(map[string]any)["id"] != "b" || items[1] != nil || items[2].(map[string]any)["id"] != "a" || items[3].(map[string]any)["id"] != "b" || len(rd.gets) != 3 {
		t.Fatalf("items=%v, Gets=%+v", items, rd.gets)
	}
	ids := make([]string, 201)
	for i := range ids {
		ids[i] = strconv.Quote(fmt.Sprintf("i%d", i))
	}
	_, errs = run(t, rd, `{ issuesById(ids:[`+strings.Join(ids, ",")+`]) { id } }`)
	assertError(t, errs, "200 IDs")
	if len(rd.gets) != 3 {
		t.Fatalf("201 ID refusal called Get: %+v", rd.gets)
	}
}
func TestGQLGetBudget(t *testing.T) {
	rd := &fakeReader{issues: map[string]*types.IssueDetails{}}
	ids := make([]string, 201)
	for i := range ids {
		id := fmt.Sprintf("i%d", i)
		rd.issues[id] = detail(id)
		ids[i] = `a` + strconv.Itoa(i) + `:issue(id:"` + id + `") { id }`
	}
	_, errs := run(t, rd, `{ `+strings.Join(ids[:200], " ")+` }`)
	assertNoErrors(t, errs)
	if len(rd.gets) != 200 {
		t.Fatalf("200 calls: %d", len(rd.gets))
	}
	rd.gets = nil
	_, errs = run(t, rd, `{ `+strings.Join(ids, " ")+` }`)
	assertError(t, errs, "budget")
	if len(rd.gets) != 200 {
		t.Fatalf("201st call reached Reader: %d", len(rd.gets))
	}
	rd.gets = nil
	repeats := make([]string, 201)
	for i := range repeats {
		repeats[i] = fmt.Sprintf(`a%d:issue(id:"i0") { id }`, i)
	}
	_, errs = run(t, rd, `{ `+strings.Join(repeats, " ")+` }`)
	assertNoErrors(t, errs)
	if len(rd.gets) != 1 {
		t.Fatalf("cache hits counted: %d", len(rd.gets))
	}
}

// execWith runs document against q, so a test can read or preset its budgets.
func execWith(q *queryResolver, document string) *graphql.Response {
	return graphql.MustParseSchema(schemaSDL, q, graphql.MaxParallelism(1)).Exec(context.Background(), document, "", nil)
}
func newQ(rd *fakeReader, qr *fakeQuerier) *queryResolver {
	return &queryResolver{roles: Roles{Reader: rd, Querier: qr}, cache: map[getKey]*issueops.IssueDetails{}}
}
func TestGQLBudgetCountSites(t *testing.T) {
	a := detail("a")
	a.Description, a.Design, a.AcceptanceCriteria, a.Notes, a.Payload = "1", "22", "333", "4444", "55555"
	a.Metadata = json.RawMessage(`{"k":1}`)
	a.Comments = []*types.Comment{{ID: "c1", Text: "x"}, {ID: "c2", Text: "yy"}}
	a.Dependencies = []*types.IssueWithDependencyMetadata{{Issue: types.Issue{ID: "b"}, DependencyType: types.DepBlocks}}
	a.Dependents = []*types.IssueWithDependencyMetadata{{Issue: types.Issue{ID: "b"}, DependencyType: types.DepBlocks}}
	b := detail("b")
	b.Description = "bbb"
	rd := &fakeReader{issues: map[string]*types.IssueDetails{"a": a, "b": b}, readyPage: issueops.IssuePage{Items: []*types.IssueWithCounts{row("r1")}}}
	qr := &fakeQuerier{page: issueops.IssuePage{Items: []*types.IssueWithCounts{row("q1"), row("q2")}}}
	q := newQ(rd, qr)
	resp := execWith(q, `{
		issue(id:"a") { description design acceptance_criteria notes payload metadata comments { text }
			dependencies { id issue { id } } dependents { issue { description } } }
		issuesById(ids:["a","a","missing"]) { id }
		issues { items { id } }
		ready { items { id } }
	}`)
	if len(resp.Errors) != 0 {
		t.Fatalf("errors: %v", resp.Errors)
	}
	// Objects: issue 1, comments 2, dependency relation and its shallow issue 2,
	// dependent relation and its full issue 2, issuesById 2 (the missing ID is
	// null), issues items 2, ready items 1.
	// Reads: Get a twice (two selection keys), Get missing, Get b, Query, Ready.
	// Text: 1+2+3+4+5 bytes of issue text, 7 of metadata, 3 of comments, 3 of b.
	if q.objects != 12 || q.readCalls != 6 || q.textBytes != 28 {
		t.Fatalf("objects %d, reads %d, text bytes %d", q.objects, q.readCalls, q.textBytes)
	}
}
func TestGQLBudgetBoundaries(t *testing.T) {
	a := detail("a")
	a.Description = "abc"
	a.Comments = []*types.Comment{{ID: "c1"}, {ID: "c2"}}
	rd := &fakeReader{issues: map[string]*types.IssueDetails{"a": a}}
	cases := []struct {
		name, document, err string
		preset              func(*queryResolver)
	}{
		{"objects", `{ issue(id:"a") { comments { id } } }`, "response object budget exceeded", func(q *queryResolver) { q.objects = maxResponseObjects - 3 }},
		{"text", `{ issue(id:"a") { description } }`, "long-text budget exceeded", func(q *queryResolver) { q.textBytes = maxLongTextBytes - 3 }},
		{"reads", `{ issue(id:"a") { id } }`, "read budget exceeded", func(q *queryResolver) { q.readCalls = maxReadCalls - 1 }},
	}
	for _, tc := range cases {
		q := newQ(rd, nil)
		tc.preset(q)
		if resp := execWith(q, tc.document); len(resp.Errors) != 0 {
			t.Fatalf("%s: at the limit: %v", tc.name, resp.Errors)
		}
		q = newQ(rd, nil)
		tc.preset(q)
		switch tc.name {
		case "objects":
			q.objects++
		case "text":
			q.textBytes++
		case "reads":
			q.readCalls++
		}
		resp := execWith(q, tc.document)
		if len(resp.Errors) != 1 || !strings.Contains(resp.Errors[0].Message, tc.err) || string(resp.Data) != `{"issue":null}` {
			t.Fatalf("%s: one over the limit: data %s, errors %v", tc.name, resp.Data, resp.Errors)
		}
	}
}
func TestGQLResponseBudgetsStopAmplification(t *testing.T) {
	hub := detail("h")
	issues := map[string]*types.IssueDetails{"h": hub}
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("d%d", i)
		d := detail(id)
		d.Dependencies = []*types.IssueWithDependencyMetadata{{Issue: types.Issue{ID: "h"}, DependencyType: types.DepBlocks}}
		issues[id] = d
		hub.Dependents = append(hub.Dependents, &types.IssueWithDependencyMetadata{Issue: types.Issue{ID: id}, DependencyType: types.DepBlocks})
	}
	rd := &fakeReader{issues: issues}
	// 200 copies of the hub, each with 100 dependents that point back to it:
	// 101 reads, but 80,200 objects.
	ids := strings.TrimSuffix(strings.Repeat(`"h",`, 200), ",")
	_, errs := run(t, rd, `{ issuesById(ids:[`+ids+`]) { dependents { issue { dependencies { issue { id } } } } } }`)
	assertError(t, errs, "response object budget exceeded: maximum 10000 objects")
	if len(rd.gets) != 101 {
		t.Fatalf("gets: %d", len(rd.gets))
	}
	// List aliases share the read budget with Get.
	rd = &fakeReader{issues: issues}
	qr := &fakeQuerier{}
	aliases := make([]string, 201)
	for i := range aliases {
		aliases[i] = fmt.Sprintf(`a%d:issues { has_more }`, i)
	}
	aliases[0] = `a0:issue(id:"h") { id }`
	aliases[1] = `a1:ready { has_more }`
	roles := Roles{Reader: rd, Querier: qr}
	_, errs = runRoles(t, roles, `{ `+strings.Join(aliases[:200], " ")+` }`)
	assertNoErrors(t, errs)
	if len(rd.gets)+len(rd.readyReqs)+len(qr.reqs) != 200 {
		t.Fatalf("200 reads: %d gets, %d ready, %d queries", len(rd.gets), len(rd.readyReqs), len(qr.reqs))
	}
	rd.gets, rd.readyReqs, qr.reqs = nil, nil, nil
	_, errs = runRoles(t, roles, `{ `+strings.Join(aliases, " ")+` }`)
	assertError(t, errs, "read budget exceeded: maximum 200 role calls")
	if len(rd.gets)+len(rd.readyReqs)+len(qr.reqs) != 200 {
		t.Fatalf("201st read reached a role: %d gets, %d ready, %d queries", len(rd.gets), len(rd.readyReqs), len(qr.reqs))
	}
}
func TestGQLCountOverflowAndUnevenEdges(t *testing.T) {
	d := detail("a")
	n := int64(math.MaxInt32) + 1
	d.DependencyCount = &n
	d.Dependencies = []*types.IssueWithDependencyMetadata{{Issue: types.Issue{ID: "b"}}}
	rd := &fakeReader{issues: map[string]*types.IssueDetails{"a": d}}
	_, errs := run(t, rd, `{ issue(id:"a") { dependency_count } }`)
	assertError(t, errs, "dependency_count")
	n = 2
	data, errs := run(t, rd, `{ issue(id:"a") { dependency_count dependencies { id } } }`)
	assertNoErrors(t, errs)
	item := data["issue"].(map[string]any)
	if item["dependency_count"] != float64(2) || len(item["dependencies"].([]any)) != 1 {
		t.Fatalf("uneven edges = %v", item)
	}
	qr := &fakeQuerier{page: issueops.IssuePage{Items: []*issueops.IssueWithCounts{row("a")}}}
	qr.page.Items[0].DependentCount = math.MaxInt32 + 1
	_, errs = runRoles(t, Roles{Querier: qr}, `{ issues { items { dependent_count } } }`)
	assertError(t, errs, "dependent_count")
}
