package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/issueops"
)

// The pins for GET /v0/beads/issues:count. These are pure: the count path runs
// end to end over a real listener against a fake ROLE, so what is asserted here
// is the WIRE EDGE — that every documented parameter reaches the role's request
// unchanged, that `group_by` chooses between the role's two methods, and that
// the response shape distinguishes "you did not ask for buckets" from "nothing
// matched".
//
// What a fake cannot prove is what the numbers MEAN: that a bare count really
// includes closed rows where a listing hides them, and that `include_infra`
// really moves the set in four directions at once. Those live in cmd/bd's
// TestProxiedServerServeCount against real Dolt, over seeded rows.

const countPath = "/v0/beads/issues:count"

func newCountServer(t *testing.T, counter *roleCounter) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{Counter: counter}))
}

// countBody decodes the answer into the generated type, which is what a client
// holds. `groups` is a POINTER there, so its absence is observable — the whole
// reason the schema publishes it optional.
func countBody(t *testing.T, resp *http.Response) (int64, *map[string]int) {
	t.Helper()
	raw := readAll(t, resp)
	var body struct {
		Total  int64           `json:"total"`
		Groups *map[string]int `json:"groups"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode count body %q: %v", raw, err)
	}
	return body.Total, body.Groups
}

// TestCountPathReachesItsHandler is the sweep and delete rows' twin: a LITERAL
// segment registered beside the claim's wildcard `/v0/beads/issues/{idop}`,
// where ServeMux precedence is by specificity rather than registration order.
// It is also registered beside the plain collection GET, which matches the
// whole path and so cannot claim this one.
func TestCountPathReachesItsHandler(t *testing.T) {
	counter := &roleCounter{total: 7}
	ts := newCountServer(t, counter)

	resp := ts.get(t, countPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if len(counter.countRequests()) != 1 {
		t.Fatalf("the count role was called %d times, want 1 — the path reached another handler",
			len(counter.countRequests()))
	}
	total, groups := countBody(t, resp)
	if total != 7 {
		t.Errorf("total = %d, want the role's 7", total)
	}
	if groups != nil {
		t.Errorf("groups = %v; a request that asked for no buckets must not carry the member", *groups)
	}
}

// TestCountForwardsEveryDocumentedParameter is the operation's central pin:
// every filter the role publishes reaches its request, decoded as the type the
// role models.
//
// It is asserted on the REQUEST the role received rather than on the number
// that came back, for the reason the delete's forwarding case gives: an answer
// carrying a plausible number says nothing about which set was counted, and a
// dropped filter is exactly the failure that produces one.
//
// The three POINTER priorities are here for their own reason. The role models
// them as pointers because 0 is a real priority, so a handler that sent 0 for
// an absent parameter would silently count only P0 work.
func TestCountForwardsEveryDocumentedParameter(t *testing.T) {
	counter := &roleCounter{}
	ts := newCountServer(t, counter)

	resp := ts.get(t, countPath+"?"+url.Values{
		"status":            {"in_progress"},
		"type":              {"bug"},
		"assignee":          {"alice"},
		"priority":          {"0"},
		"priority_min":      {"1"},
		"priority_max":      {"3"},
		"label":             {"backend", "urgent"},
		"label_any":         {"triage"},
		"title":             {"flake"},
		"id":                {"bd-1,bd-2"},
		"title_contains":    {"retry"},
		"desc_contains":     {"timeout"},
		"notes_contains":    {"rollback"},
		"created_after":     {"2026-01-01T00:00:00Z"},
		"created_before":    {"2026-02-01T00:00:00Z"},
		"updated_after":     {"2026-03-01T00:00:00Z"},
		"updated_before":    {"2026-04-01T00:00:00Z"},
		"closed_after":      {"2026-05-01T00:00:00Z"},
		"closed_before":     {"2026-06-01T00:00:00Z"},
		"empty_description": {"true"},
		"no_assignee":       {"true"},
		"no_labels":         {"true"},
		"include_infra":     {"true"},
	}.Encode())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	got := counter.countRequests()
	if len(got) != 1 {
		t.Fatalf("%d counts, want 1", len(got))
	}
	at := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return &v
	}
	pri := func(v int) *int { return &v }
	want := issueops.CountRequest{
		Status:    "in_progress",
		IssueType: "bug",
		Assignee:  "alice",

		Priority:    pri(0),
		PriorityMin: pri(1),
		PriorityMax: pri(3),

		Labels:    []string{"backend", "urgent"},
		LabelsAny: []string{"triage"},

		TitleSearch: "flake",
		// AS WRITTEN, not pre-split: the role owns what an id set means.
		IDFilter: "bd-1,bd-2",

		TitleContains: "retry",
		DescContains:  "timeout",
		NotesContains: "rollback",

		CreatedAfter:  at("2026-01-01T00:00:00Z"),
		CreatedBefore: at("2026-02-01T00:00:00Z"),
		UpdatedAfter:  at("2026-03-01T00:00:00Z"),
		UpdatedBefore: at("2026-04-01T00:00:00Z"),
		ClosedAfter:   at("2026-05-01T00:00:00Z"),
		ClosedBefore:  at("2026-06-01T00:00:00Z"),

		EmptyDesc:  true,
		NoAssignee: true,
		NoLabels:   true,

		IncludeInfra: true,
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("request = %+v\nwant     %+v", got[0], want)
	}
}

// TestCountDefaultsToTheDurablePlaneAndNoBucketing: an empty request is the
// role's default answer and nothing else.
//
// The zero value of IncludeInfra is the whole plane story on this operation and
// it is worth an assertion of its own: false means DURABLE ONLY — no wisps, no
// `no_history` beads stored in that tier — and a handler that defaulted it on
// would silently start counting ephemeral rows a scripted caller has never
// counted.
func TestCountDefaultsToTheDurablePlaneAndNoBucketing(t *testing.T) {
	counter := &roleCounter{}
	ts := newCountServer(t, counter)

	if resp := ts.get(t, countPath); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := counter.countRequests()
	if len(got) != 1 {
		t.Fatalf("%d counts, want 1", len(got))
	}
	if !reflect.DeepEqual(got[0], issueops.CountRequest{}) {
		t.Errorf("request = %+v, want the zero request: an empty query must not invent a predicate", got[0])
	}
	if len(counter.groupRequests()) != 0 {
		t.Errorf("%d grouped counts; an absent `group_by` must not reach the bucketing method", len(counter.groupRequests()))
	}
}

// TestCountGroupBySelectsTheRolesOtherMethod is the discriminator, asserted in
// both directions on the ROLE rather than on the body.
//
// A handler that always called CountByGroup and dropped the buckets would look
// identical on the wire for an ungrouped request — same `total`, no `groups` —
// while asking the store for a bucketed scan on every count.
func TestCountGroupBySelectsTheRolesOtherMethod(t *testing.T) {
	for _, group := range countGroups {
		t.Run(string(group), func(t *testing.T) {
			counter := &roleCounter{groupTo: 12, groups: map[string]int{"x": 12}}
			ts := newCountServer(t, counter)

			resp := ts.get(t, countPath+"?group_by="+string(group)+"&assignee=alice")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			if calls := counter.countRequests(); len(calls) != 0 {
				t.Errorf("%d scalar counts; a grouped request must not reach Count", len(calls))
			}
			grouped := counter.groupRequests()
			if len(grouped) != 1 {
				t.Fatalf("%d grouped counts, want 1", len(grouped))
			}
			if grouped[0].GroupBy != group {
				t.Errorf("GroupBy = %q, want %q", grouped[0].GroupBy, group)
			}
			// THE SAME PREDICATE, which is the identity the role promises: a
			// grouped count is a scalar count plus a dimension, so the filter
			// must survive the switch between methods intact.
			if grouped[0].Filter.Assignee != "alice" {
				t.Errorf("Filter = %+v, want the request's predicate carried onto the grouped call", grouped[0].Filter)
			}
		})
	}
}

// TestCountByGroupAnswersTheRolesTotalRatherThanTheSumOfBuckets is the pin the
// role's own doc asks for, and it is the one number a client is most likely to
// get wrong by deriving it.
//
// LABEL BUCKETS OVERLAP: an issue carrying three labels is one row in `total`
// and one row in each of three buckets. The fixture makes the sum deliberately
// larger than the total, so a handler that computed the total from the buckets
// would report a workspace three times its size and this case would say so.
func TestCountByGroupAnswersTheRolesTotalRatherThanTheSumOfBuckets(t *testing.T) {
	counter := &roleCounter{
		groupTo: 4,
		groups:  map[string]int{"backend": 3, "urgent": 3, "(no labels)": 1},
	}
	ts := newCountServer(t, counter)

	resp := ts.get(t, countPath+"?group_by=label")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	total, groups := countBody(t, resp)
	if total != 4 {
		t.Errorf("total = %d, want the role's 4 — never the sum of the buckets", total)
	}
	if groups == nil {
		t.Fatal("groups is absent on a grouped request")
	}
	if !reflect.DeepEqual(*groups, map[string]int{"backend": 3, "urgent": 3, "(no labels)": 1}) {
		t.Errorf("groups = %v, want the role's buckets verbatim", *groups)
	}
	sum := 0
	for _, n := range *groups {
		sum += n
	}
	if sum == int(total) {
		t.Fatal("the fixture's buckets sum to its total, so this case could not tell a derived total from the role's")
	}
}

// TestCountByGroupAnswersAnEmptyObjectRatherThanNull, and rather than nothing
// at all.
//
// Three states have to stay distinguishable: `groups` ABSENT means "you did not
// ask for buckets"; `{}` means "you asked and nothing matched"; and `null` must
// never appear, because a client would have to guess which of the two it meant.
// The second case drives the role returning a NIL map, which is the shape an
// implementation can produce even though the contract says it will not.
func TestCountByGroupAnswersAnEmptyObjectRatherThanNull(t *testing.T) {
	for _, tc := range []struct {
		name     string
		nilGroup bool
	}{
		{"the role answered with an empty map", false},
		{"the role answered with a nil map", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counter := &roleCounter{nilGroup: tc.nilGroup}
			ts := newCountServer(t, counter)

			resp := ts.get(t, countPath+"?group_by=status")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			// ASSERTED ON THE BYTES, deliberately: `{}` and `null` decode
			// identically into a *map, which is exactly the confusion this case
			// exists to prevent, so a decoded assertion could not see it.
			raw := readAll(t, resp)
			if !strings.Contains(raw, `"groups":{}`) {
				t.Errorf("body = %s, want `groups` present as an empty object", raw)
			}
			if strings.Contains(raw, `"groups":null`) {
				t.Errorf("body = %s; a null `groups` is indistinguishable from an absent one to a client that decodes into a pointer", raw)
			}
		})
	}
}

// TestCountRefusesAGroupByOutsideTheClosedSet: the dimension vocabulary is
// closed, and an unknown value is a 400 naming the parameter with nothing
// reaching the role.
//
// The role refuses it too — ValidateCountGroup answers ErrValidation — and the
// reason it is refused HERE as well is the one the role's own doc gives for
// refusing it at all: a caller that misspelled a dimension and got zero buckets
// back has no way to tell that from a workspace with nothing in it. Refusing at
// the edge adds the parameter name, which is also a client's only per-parameter
// capability probe.
func TestCountRefusesAGroupByOutsideTheClosedSet(t *testing.T) {
	for _, value := range []string{"nosuch", "Status", "priority,status", "assignee "} {
		counter := &roleCounter{}
		ts := newCountServer(t, counter)

		resp := ts.get(t, countPath+"?group_by="+url.QueryEscape(value))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("group_by=%q: status = %d, want 400: %s", value, resp.StatusCode, readAll(t, resp))
			continue
		}
		body := decodeBody(t, resp)
		if body["param"] != "group_by" {
			t.Errorf("group_by=%q: param = %v, want group_by", value, body["param"])
		}
		if body["reason"] != string(ReasonInvalidValue) {
			t.Errorf("group_by=%q: reason = %v, want %s — the parameter is known, its value is not",
				value, body["reason"], ReasonInvalidValue)
		}
		if calls := len(counter.countRequests()) + len(counter.groupRequests()); calls != 0 {
			t.Errorf("group_by=%q: %d calls reached the role", value, calls)
		}
	}
}

// TestCountRefusesTheValuesTheDocumentRefuses: every typed parameter is
// refused at the edge before any database work, naming itself.
func TestCountRefusesTheValuesTheDocumentRefuses(t *testing.T) {
	for _, tc := range []struct {
		query string
		param string
	}{
		{"priority=high", "priority"},
		{"priority_min=x", "priority_min"},
		{"empty_description=yes-please", "empty_description"},
		{"no_assignee=1.5", "no_assignee"},
		{"include_infra=maybe", "include_infra"},
		{"created_after=yesterday", "created_after"},
		{"closed_before=2026-06-01", "closed_before"},
		{"status=open&status=closed", "status"},
		{"nosuchparam=1", "nosuchparam"},
	} {
		counter := &roleCounter{}
		ts := newCountServer(t, counter)

		resp := ts.get(t, countPath+"?"+tc.query)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", tc.query, resp.StatusCode, readAll(t, resp))
			continue
		}
		if body := decodeBody(t, resp); body["param"] != tc.param {
			t.Errorf("%s: param = %v, want %s", tc.query, body["param"], tc.param)
		}
		if calls := len(counter.countRequests()) + len(counter.groupRequests()); calls != 0 {
			t.Errorf("%s: %d calls reached the role", tc.query, calls)
		}
	}
}

// TestCountAcceptsAnEmptyValuedNumberAsAbsent pins the shared decoder's rule
// where this operation inherits it: `priority_max=` with no value is ABSENT,
// not zero.
//
// It is the same reading every integer parameter on this surface gets, and it
// matters more here than on a listing: the role models the three priorities as
// pointers because 0 is a real priority, so reading an empty value as 0 would
// turn "no upper bound" into "P0 only" and answer a plausible smaller number.
func TestCountAcceptsAnEmptyValuedNumberAsAbsent(t *testing.T) {
	counter := &roleCounter{}
	ts := newCountServer(t, counter)

	if resp := ts.get(t, countPath+"?priority_max=&priority="); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := counter.countRequests()
	if len(got) != 1 {
		t.Fatalf("%d counts, want 1", len(got))
	}
	if got[0].Priority != nil || got[0].PriorityMax != nil {
		t.Errorf("Priority = %v PriorityMax = %v, want both nil: an empty value is an absent parameter",
			got[0].Priority, got[0].PriorityMax)
	}
}

// TestCountPublishesNoConflictAndNoMiss is the absence, asserted.
//
// A predicate matching nothing is `0` and a 200, never a 404: the role has no
// ErrNotFound at all, because a question about a set has an answer even when
// the set is empty, and a client polling for work would otherwise have to
// classify an error to read a zero. And nothing about a READ can conflict.
func TestCountPublishesNoConflictAndNoMiss(t *testing.T) {
	codes, ok := operationCodes[OpCountIssues]
	if !ok {
		t.Fatalf("no %s row in the handler table", OpCountIssues)
	}
	for _, c := range codes {
		switch c.Status() {
		case http.StatusNotFound:
			t.Errorf("count publishes %q; a predicate matching nothing is 0, not a miss", c)
		case http.StatusConflict:
			t.Errorf("count publishes the conflict code %q; it is a READ", c)
		}
	}

	counter := &roleCounter{total: 0}
	ts := newCountServer(t, counter)
	resp := ts.get(t, countPath+"?assignee=nobody-by-that-name")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an empty set: %s", resp.StatusCode, readAll(t, resp))
	}
	if total, _ := countBody(t, resp); total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
}

// TestCountTakesADatabaseSlot: the operation runs a real query, so it queues
// behind the request-wide database semaphore like every other read. Only the
// two snapshot endpoints bypass it.
func TestCountTakesADatabaseSlot(t *testing.T) {
	row, ok := routeRow(OpCountIssues)
	if !ok {
		t.Fatalf("no %s row in the route table", OpCountIssues)
	}
	if row.bypassSemaphore {
		t.Error("the count bypasses the database slot; it runs a real query and must queue like every other read")
	}
	if row.streaming {
		t.Error("the count is marked streaming; it writes one body and finishes")
	}
}

// routeRow finds a route by operation id.
func routeRow(op string) (route, bool) {
	for _, rt := range routeTable {
		if rt.op == op {
			return rt, true
		}
	}
	return route{}, false
}

// specParams lists the query-parameter names an operation documents. It is
// specParam's plural, and it exists because the parity check below is about the
// SET rather than about one entry.
func specParams(t *testing.T, doc map[string]any, id string) []string {
	t.Helper()
	so, ok := specOps(t, doc)[id]
	if !ok {
		t.Fatalf("the document has no operation %q", id)
	}
	raw, ok := so.op["parameters"].([]any)
	if !ok {
		t.Fatalf("%s documents no parameters", id)
	}
	var names []string
	for _, p := range raw {
		param, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("%s: parameter is %T, want a mapping", id, p)
		}
		name, _ := param["name"].(string)
		if name == "" {
			t.Fatalf("%s: a parameter has no name: %#v", id, param)
		}
		names = append(names, name)
	}
	return names
}

// TestCountParametersMatchTheHandler is the query-parameter analog of the
// member-parity gates in spec_parity_test.go, and it is MECHANICAL in both
// directions rather than a hand-rolled list beside the document's.
//
// query.read records every name the handler asks for, so driving countFilters
// and countGroupOf over an empty query yields the server's real vocabulary. A
// parameter documented and not read is accepted by the document and refused by
// the server as `unknown_parameter`; a parameter read and not documented is
// undisclosed filtering surface, and on THIS operation an undisclosed filter
// silently changes which set a number describes.
//
// THE DERIVATION IS BY EXECUTION, WHICH BOUNDS WHAT IT CAN SEE. It observes the
// names read on the path this call actually takes, and that is the whole
// vocabulary only because countFilters is straight-line: every accessor runs on
// every request, so an empty query exercises all of them. A conditional there —
// a parameter read only when another is present — would leave its name out of
// this set and out of this check. If one is ever added, drive this over the
// query that reaches it too, or the gate quietly narrows to the default path.
func TestCountParametersMatchTheHandler(t *testing.T) {
	q := newQuery(url.Values{})
	_ = countFilters(q)
	_, _ = countGroupOf(q)

	read := map[string]bool{}
	for name := range q.read {
		read[name] = true
	}

	doc := loadSpec(t)
	documented := map[string]bool{}
	for _, p := range specParams(t, doc, "countIssues") {
		documented[p] = true
	}

	if extra := diff(documented, read); len(extra) > 0 {
		t.Errorf("the document publishes parameters this handler never reads, so the server refuses them as unknown_parameter: %v", extra)
	}
	if missing := diff(read, documented); len(missing) > 0 {
		t.Errorf("this handler reads parameters the document does not publish: %v", missing)
	}
}

// TestCountGroupEnumMatchesTheRolesVocabulary keeps the schema's enum, the
// server's accepted set and the ROLE's constants as one list.
//
// The three are the same strings today. countGroups is spelled with the role's
// constants so the server and the role cannot drift; this compares that list
// against the DOCUMENT, which is the third party to the agreement.
//
// IT IS ALSO WHAT MAKES THE ROLE'S OWN ErrValidation UNREACHABLE FROM THIS
// OPERATION, which is a fact worth pinning rather than discovering. The count
// role has exactly one validation refusal — ValidateCountGroup's unknown
// dimension; BuildCountFilter cannot fail at all — and this handler refuses
// that dimension at the edge. So every `invalid_argument` this operation emits
// is the transport's, and the shared read failure path never sees a role
// refusal. If the enum here and the role's constants ever diverged, a value
// this server accepted and the role refused would arrive as an unclassified
// 500, which is the regression this comparison prevents.
func TestCountGroupEnumMatchesTheRolesVocabulary(t *testing.T) {
	doc := loadSpec(t)
	so := specOps(t, doc)["countIssues"]
	param := specParam(t, so, "group_by")

	schema, ok := param["schema"].(map[string]any)
	if !ok {
		t.Fatalf("group_by has no schema: %#v", param)
	}
	var documented []string
	for _, v := range toStrings(t, schema["enum"]) {
		documented = append(documented, v)
	}
	if !slices.Equal(documented, countGroupNames()) {
		t.Errorf("the document's group_by enum is %v, the server accepts %v", documented, countGroupNames())
	}
}

// countFieldForParameter maps every documented parameter to the
// issueops.CountRequest field it fills, and it is the third leg of this
// operation's parity triangle.
//
// The other two are already mechanical: TestCountParametersMatchTheHandler ties
// the parameter names to the DOCUMENT, and TestCountForwardsEveryDocumentedParameter
// ties each parameter's VALUE to the field it lands in. Neither can see a role
// field that no parameter reaches — a 24th filter added to CountRequest and left
// unpublished turns nothing red, and the wire silently stops being able to ask
// a question the role can answer. That is the failure this map closes, and it is
// the one that matters for an HTTP-backed store: it is how the wire becomes
// narrower than the role without anyone deciding to narrow it.
//
// `group_by` is absent because it is not a filter: it selects the role's other
// METHOD and lives on CountByGroupRequest.
var countFieldForParameter = map[string]string{
	"status":            "Status",
	"type":              "IssueType",
	"assignee":          "Assignee",
	"priority":          "Priority",
	"priority_min":      "PriorityMin",
	"priority_max":      "PriorityMax",
	"label":             "Labels",
	"label_any":         "LabelsAny",
	"title":             "TitleSearch",
	"id":                "IDFilter",
	"title_contains":    "TitleContains",
	"desc_contains":     "DescContains",
	"notes_contains":    "NotesContains",
	"created_after":     "CreatedAfter",
	"created_before":    "CreatedBefore",
	"updated_after":     "UpdatedAfter",
	"updated_before":    "UpdatedBefore",
	"closed_after":      "ClosedAfter",
	"closed_before":     "ClosedBefore",
	"empty_description": "EmptyDesc",
	"no_assignee":       "NoAssignee",
	"no_labels":         "NoLabels",
	"include_infra":     "IncludeInfra",
}

// TestEveryCountRequestFieldIsPublished: the role publishes 23 filters and the
// wire publishes all 23. A field added to issueops.CountRequest fails here and
// NAMES itself, so the choice is made deliberately — publish it, or record why
// it is withheld — rather than by nobody noticing.
//
// The map above is pinned at both ends and is not a third hand-rolled list: its
// KEYS are checked against the document by TestCountParametersMatchTheHandler,
// and its VALUES are checked against the struct here.
func TestEveryCountRequestFieldIsPublished(t *testing.T) {
	published := map[string]bool{}
	for param, field := range countFieldForParameter {
		if published[field] {
			t.Errorf("two parameters claim to fill %s; the map is meant to be one parameter per field", field)
		}
		published[field] = true
		if _, ok := reflect.TypeOf(issueops.CountRequest{}).FieldByName(field); !ok {
			t.Errorf("parameter %q names field %s, which issueops.CountRequest does not declare", param, field)
		}
	}

	declared := map[string]bool{}
	typ := reflect.TypeOf(issueops.CountRequest{})
	for i := range typ.NumField() {
		declared[typ.Field(i).Name] = true
	}
	if missing := diff(declared, published); len(missing) > 0 {
		t.Errorf("issueops.CountRequest declares filters this operation publishes no parameter for: %v\n"+
			"add a parameter and a line to countFilters, or record here why the wire withholds it — "+
			"a role filter with no way to reach it makes an HTTP-backed store narrower than the native one", missing)
	}
}
