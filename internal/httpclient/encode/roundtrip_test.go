// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/roundtrip_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// GATE 2: the round trip, with the server's own decoder as the oracle.
//
// Every parameter this package emits is fed to internal/httpapi's PRODUCTION
// handlers — the real route table, the real `query` decoder with its
// unknown-parameter rule, the real request construction — and the request the
// server built is read back off a capturing role. There is no second copy of
// the parameter table on this side of the wire, so there is no twin contract
// that can drift: a name the server does not know is a 400 and fails here, and
// the right name carrying the wrong value decodes to a request that disagrees
// and fails here.
//
// WHAT THE EXPECTATION IS BUILT FROM matters as much as the assertion. It is
// derived MECHANICALLY from the encoder table's declared field correspondence —
// each entry's Decoded name — applied to the caller's own source value, plus
// the values the server is declared to weld (the operation's DispServerFixed
// entries). Hand-writing "what the server should have decoded" would recreate
// the twin this whole design exists to avoid: a mapping error would then be
// written twice and agree with itself.
//
// It is a PURE gate: a loopback listener and sixteen fake roles, no database
// and no fixtures on disk, which is what lets it run in the pure-gate step at
// PR cadence rather than behind a conformance tier.

func TestEncodedParametersRoundTripThroughTheServerDecoder(t *testing.T) {
	oracle := startOracle(t)
	exercised := map[string]map[string]bool{}

	cases := roundTripCases()
	if len(cases) == 0 {
		t.Fatal("no round-trip cases; there is nothing for this gate to check")
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table, ok := TableFor(tc.op, tc.shape)
			if !ok {
				t.Fatalf("no encoder table for %s/%s", tc.op, tc.shape)
			}
			source := reflect.ValueOf(tc.source)
			if source.Type() != table.Source {
				t.Fatalf("case source is %s, the table takes %s", source.Type(), table.Source)
			}

			encoded, err := Encode(tc.op, tc.shape, tc.source)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}

			oracle.reset()
			if status := oracle.dial(t, tc.op, encoded); status != http.StatusOK {
				t.Fatalf("the server answered %d for %q.\n"+
					"A 400 here is the server turning down a parameter this client sent: an unknown NAME is version skew the encoder table has wrong, an invalid VALUE is a rendering bug.",
					status, encoded.Params.Encode())
			}

			want := expectedRequest(t, table, source)
			compareRequests(t, table, want, oracle.captured(t, tc.op, tc.shape))
			recordExercised(exercised, table, source)
		})
	}

	assertEveryEncodedFieldWasExercised(t, exercised)
}

// TestTheExactIDsFastPathDialsGetIssue covers the one encoding that is a PATH
// rather than a query string: D11's exact-ids probe, which is what
// ResolvePartialID's fast path becomes over http. It has no single decoded
// request for the sweep above to compare against — n ids are n requests — so it
// is driven here.
func TestTheExactIDsFastPathDialsGetIssue(t *testing.T) {
	oracle := startOracle(t)

	ids := []string{"bd-a3f8e9", "bd-a3f8e9.1", "hacker-news-ko4"}
	plan, err := PlanSearch(types.IssueFilter{IDs: ids}, ZeroVocabulary)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Shape != SearchExactIDs {
		t.Fatalf("shape = %q, want %q", plan.Shape, SearchExactIDs)
	}
	if len(plan.Params) != 0 {
		t.Errorf("the exact-ids shape sent query parameters: %v", plan.Params)
	}
	if !reflect.DeepEqual(plan.IDs, ids) {
		t.Fatalf("plan ids = %v, want %v", plan.IDs, ids)
	}

	for _, id := range plan.IDs {
		oracle.reset()
		if status := oracle.dial(t, OpGetIssue, Encoded{Params: url.Values{}, PathIDs: []string{id}}); status != http.StatusOK {
			t.Fatalf("the server answered %d for id %q", status, id)
		}
		got, ok := oracle.captured(t, OpGetIssue, "").(issueops.GetRequest)
		if !ok {
			t.Fatalf("captured a %T, want an issueops.GetRequest", got)
		}
		if got.ID != id {
			t.Errorf("the server decoded id %q, want %q", got.ID, id)
		}
		if got.IncludeComments || got.IncludeDependents {
			t.Errorf("the fast path asked for the expensive row lists: %+v", got)
		}
	}
}

type roundTripCase struct {
	name   string
	op     Op
	shape  string
	source any
}

func roundTripCases() []roundTripCase {
	// Deliberately sub-second: the wire format is RFC 3339 and a bare
	// time.RFC3339 layout DROPS the fraction. On a created bound a dropped
	// fraction is a silently widened result set, which is precisely the class
	// of bug this package exists to make impossible.
	subSecond := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)
	// Deliberately NOT UTC: the encoder normalizes, and an instant that
	// survives the crossing only when the caller already held it in UTC is an
	// encoder that works by luck.
	zoned := time.Date(2025, 12, 31, 23, 59, 59, 0, time.FixedZone("KST", 9*3600))

	zero, five := 0, 5
	assignee, parent := "agent-7", "bd-epic1"

	readyFilters := issueops.ReadyRequest{
		IssueType:  "bug",
		Assignee:   "agent-7",
		Unassigned: true,

		Labels:        []string{"alpha", "beta"},
		LabelsAny:     []string{"gamma"},
		ExcludeLabels: []string{"delta", "epsilon"},
		LabelPattern:  "tech-*",
		LabelRegex:    "tech-(debt|legacy)",

		// Zero is a real priority, and a value-plus-flag pair has already lost
		// P0 once. The pointer has to survive the crossing carrying zero.
		Priority: &zero,
		ParentID: "bd-epic1",

		IncludeDeferred:  true,
		IncludeEphemeral: true,
		ExcludeTypes:     []string{"gate", "molecule"},

		MetadataFields: map[string]string{"team": "core", "wave": "3"},
		HasMetadataKey: "team",
	}

	// The listing's fixture gains the text projection; readyFilters itself must
	// NOT, because the countReadyWork case below shares it and that operation
	// publishes no `brief` to round-trip.
	readyEverything := readyFilters
	readyEverything.Sort, readyEverything.Limit = "oldest", &five
	readyEverything.Brief = true

	listEverything := issueops.ListRequest{
		Status:    "open,in_progress",
		IssueType: "bug",
		Assignee:  "agent-7",

		Labels:        []string{"alpha", "beta"},
		LabelsAny:     []string{"gamma"},
		ExcludeLabels: []string{"delta", "epsilon"},

		ParentID: "bd-epic1",

		AllFlag:          true,
		IncludeTemplates: true,
		IncludeGates:     true,
		IncludeInfra:     true,
		IncludeEphemeral: true,

		CreatedBefore: &subSecond,
		CreatedAfter:  &zoned,

		MetadataFields: map[string]string{"team": "core", "wave": "3"},
		HasMetadataKey: "team",

		Brief: true,
		Limit: &five,
	}

	workEverything := types.WorkFilter{
		// The derived default every real ready filter carries; the encoder drops
		// it and the server re-derives it, so it round-trips as the absence of a
		// `status` parameter.
		Status:     types.StatusOpen,
		Type:       "bug",
		Priority:   &zero,
		Assignee:   &assignee,
		Unassigned: true,

		Labels:        []string{"alpha", "beta"},
		LabelsAny:     []string{"gamma"},
		ExcludeLabels: []string{"delta", "epsilon"},
		LabelPattern:  "tech-*",
		LabelRegex:    "tech-(debt|legacy)",

		Limit:      5,
		SortPolicy: types.SortPolicyOldest,
		ParentID:   &parent,

		IncludeDeferred:  true,
		IncludeEphemeral: true,
		ExcludeTypes:     []types.IssueType{"gate", "molecule"},

		MetadataFields: map[string]string{"team": "core", "wave": "3"},
		HasMetadataKey: "team",

		// The bridge maps Lite onto the same `brief` the ready request sends.
		Lite: true,
	}

	// The count's whole vocabulary at once. Every member is here because the
	// operation publishes every member, so this case IS the partition: a
	// parameter the server does not know is a 400 and a value it decodes onto
	// the wrong field is a mismatch, and one number coming back would show
	// neither.
	countEverything := issueops.CountRequest{
		Status:    "closed",
		IssueType: "bug",
		Assignee:  "agent-7",

		// Zero is a real priority on this request too, and all three bounds are
		// pointers for that reason.
		Priority:    &zero,
		PriorityMin: &zero,
		PriorityMax: &five,

		Labels:    []string{"alpha", "beta"},
		LabelsAny: []string{"gamma"},

		TitleSearch: "flaky",
		// The role splits this string itself, so it has to arrive UNSPLIT —
		// spaces and all, which is what the server's own handler forwards.
		IDFilter: "bd-a3f8e9, bd-a3f8e9.1,bd-b1",

		TitleContains: "retry",
		DescContains:  "timeout",
		NotesContains: "flake",

		CreatedAfter:  &zoned,
		CreatedBefore: &subSecond,
		UpdatedAfter:  &zoned,
		UpdatedBefore: &subSecond,
		ClosedAfter:   &zoned,
		ClosedBefore:  &subSecond,

		EmptyDesc:  true,
		NoAssignee: true,
		NoLabels:   true,

		IncludeInfra:     true,
		IncludeEphemeral: true,

		MetadataFields: map[string]string{"team": "core", "wave": "3"},
		HasMetadataKey: "team",

		// The issues.count.scope members (#7199). NoParent is the one left out:
		// the role refuses it beside ParentID, so it rides its own case below.
		ParentID:      "bd-epic1",
		ExcludeTypes:  []string{"gate", "molecule"},
		ExcludeStatus: []string{"closed", "deferred"},
	}

	// Values that have to survive percent-encoding, a `=` inside a metadata
	// VALUE (the server splits on the first one), and a repeated parameter.
	awkward := issueops.ReadyRequest{
		Assignee:       "a b&c=d+e",
		LabelPattern:   "a/b?c#d*",
		LabelRegex:     `^(tech|ops)-[a-z]{2,}$`,
		Labels:         []string{"needs review", "p0/urgent"},
		MetadataFields: map[string]string{"spec key": "v=1&x", "z": ""},
		HasMetadataKey: "spec key",
	}

	cases := []roundTripCase{
		{"listReadyWork/a request naming nothing", OpListReadyWork, "", issueops.ReadyRequest{}},
		{"listReadyWork/every filter, an explicit order and a page", OpListReadyWork, "", readyEverything},
		{"listReadyWork/an explicitly unlimited page", OpListReadyWork, "", issueops.ReadyRequest{Sort: "hybrid", Limit: &zero}},
		{"listReadyWork/the priority policy", OpListReadyWork, "", issueops.ReadyRequest{Sort: "priority"}},
		{"listReadyWork/values that have to be escaped", OpListReadyWork, "", awkward},

		{"countReadyWork/a count naming nothing", OpCountReadyWork, "", issueops.ReadyRequest{}},
		{"countReadyWork/every filter the listing takes", OpCountReadyWork, "", readyFilters},

		{"listIssues/a listing naming nothing", OpListIssues, "", issueops.ListRequest{}},
		{"listIssues/every parameter", OpListIssues, "", listEverything},
		{"listIssues/an explicitly unlimited page", OpListIssues, "", issueops.ListRequest{Limit: &zero}},
		{"listIssues/one status, one label", OpListIssues, "", issueops.ListRequest{Status: "closed", Labels: []string{"alpha"}}},

		{"queryIssues/a query naming nothing", OpQueryIssues, "", issueops.QueryRequest{}},
		{"queryIssues/an expression with operators and parentheses", OpQueryIssues, "", issueops.QueryRequest{
			Expression: "(type=bug OR label=urgent) AND NOT priority<2",
			SortBy:     "title", Reverse: true, IncludeClosed: true, Limit: &five,
		}},

		{"countIssues/a count naming nothing", OpCountIssues, "", issueops.CountRequest{}},
		{"countIssues/every filter the operation publishes", OpCountIssues, "", countEverything},
		// `all` is a status the count takes literally and the server forwards
		// verbatim; it is not the listing's boolean of the same spelling.
		{"countIssues/the literal all status", OpCountIssues, "", issueops.CountRequest{Status: "all"}},
		// The issues.count.scope members (#7199). ParentID and NoParent are
		// mutually exclusive at the ROLE, so they ride separate cases; the two
		// exclusion lists carry more than one entry each so a decoder that kept
		// only the first value would be a mismatch rather than a coincidence.
		{"countIssues/scope under a parent", OpCountIssues, "", issueops.CountRequest{
			ParentID:      "bd-epic1",
			ExcludeTypes:  []string{"gate", "molecule"},
			ExcludeStatus: []string{"closed", "deferred"},
		}},
		{"countIssues/scope at the top level", OpCountIssues, "", issueops.CountRequest{
			NoParent:      true,
			ExcludeTypes:  []string{"epic"},
			ExcludeStatus: []string{"blocked"},
		}},
		{"countIssues/byGroup/a dimension over an empty predicate", OpCountIssues, "byGroup", issueops.CountByGroupRequest{GroupBy: issueops.CountGroupStatus}},
		{"countIssues/byGroup/a dimension over the whole predicate", OpCountIssues, "byGroup", issueops.CountByGroupRequest{Filter: countEverything, GroupBy: issueops.CountGroupLabel}},

		{"getIssue/an id alone", OpGetIssue, "", issueops.GetRequest{ID: "bd-a3f8e9"}},
		{"getIssue/both row lists", OpGetIssue, "", issueops.GetRequest{ID: "bd-a3f8e9.1", IncludeComments: true, IncludeDependents: true}},
		{"getIssue/the brief dependency projection", OpGetIssue, "", issueops.GetRequest{ID: "bd-a3f8e9.2", IncludeDependents: true, BriefDeps: true}},

		{"listReadyWork/workFilterBridge/the derived-default status alone drops", OpListReadyWork, "workFilterBridge", types.WorkFilter{Status: types.StatusOpen}},
		{"listReadyWork/workFilterBridge/every expressible field", OpListReadyWork, "workFilterBridge", workEverything},
		{"listReadyWork/workFilterBridge/an unbounded page keeps the wire's default", OpListReadyWork, "workFilterBridge", types.WorkFilter{Status: types.StatusOpen, Limit: 0, SortPolicy: types.SortPolicyPriority}},
	}
	_ = parent

	// The whole published sort vocabulary, one case each: an order accepted and
	// then ignored is indistinguishable from one the caller does not understand,
	// so the server refuses a value outside the set — and this proves every
	// value the client can send is inside it.
	for _, policy := range []string{"priority", "created", "updated", "closed", "status", "id", "title", "type", "assignee"} {
		cases = append(cases, roundTripCase{
			name: "queryIssues/sort=" + policy, op: OpQueryIssues,
			source: issueops.QueryRequest{Expression: "status=open", SortBy: policy},
		})
	}
	for _, policy := range []string{"hybrid", "priority", "oldest"} {
		cases = append(cases, roundTripCase{
			name: "listReadyWork/sort=" + policy, op: OpListReadyWork,
			source: issueops.ReadyRequest{Sort: policy},
		})
	}
	return cases
}

// expectedRequest builds the server-side request the caller's source value
// MEANS, from the table's declared correspondence.
//
// Three populations, in order: the fields the table maps onto parameters, the
// fields the server welds for itself, and the one normalization the encoder
// performs. Nothing here re-derives a parameter name or a value rendering —
// those are exactly what the server is being asked to confirm.
func expectedRequest(t *testing.T, table Table, source reflect.Value) reflect.Value {
	t.Helper()
	want := reflect.New(table.Target).Elem()

	driven := map[string]bool{}
	for _, entry := range table.Fields {
		// DispNested rides the same correspondence: the delegated shape is
		// encoded by another table, but it lands on a member of THIS target and
		// the caller's value is what the server must have decoded there.
		if entry.Disposition != DispParam && entry.Disposition != DispPath && entry.Disposition != DispNested {
			continue
		}
		dst := want.FieldByName(entry.Decoded)
		if !dst.IsValid() {
			t.Fatalf("%s has no field %s", table.Target, entry.Decoded)
		}
		assignConverted(t, dst, source.FieldByName(entry.Name))
		driven[entry.Decoded] = true
	}

	// The values the SERVER pins, taken from the operation's primary table so a
	// bridge inherits them rather than restating them: listIssues welds SortBy
	// to created order because the cursor is a position in it, and
	// countReadyWork pins the order it has no parameter for.
	for _, other := range Tables() {
		if !other.Primary || other.Op != table.Op || other.Target != table.Target {
			continue
		}
		for _, entry := range other.Fields {
			if entry.Disposition != DispServerFixed || driven[entry.Name] {
				continue
			}
			field := want.FieldByName(entry.Name)
			if !field.IsValid() || field.Kind() != reflect.String {
				t.Fatalf("%s.%s is server-fixed but is not a string field", table.Target, entry.Name)
			}
			field.SetString(entry.Fixed)
		}
	}

	// The encoder's own normalization: an absent ready policy is sent as the
	// concrete `hybrid` it means, because omitting it would adopt the server's
	// `priority` default instead — a different item SET once a limit truncates.
	if table.Op == OpListReadyWork {
		if policy := want.FieldByName("Sort"); policy.IsValid() && policy.String() == "" {
			policy.SetString("hybrid")
		}
	}
	return want
}

// assignConverted copies one source field onto the server-side field the table
// says it lands on, crossing the pointer and named-type differences the legacy
// filters carry.
//
// The pointer rules are the interesting half. A nil source pointer leaves the
// target at its zero value, because "unset" is what a nil means; a plain zero
// int leaves a target POINTER nil, because the legacy filters spell "no bound"
// as zero and the wire spells it as an absent parameter.
func assignConverted(t *testing.T, dst, src reflect.Value) {
	t.Helper()
	switch {
	case src.Kind() == reflect.Pointer && dst.Kind() != reflect.Pointer:
		if src.IsNil() {
			return
		}
		assignConverted(t, dst, src.Elem())
	case dst.Kind() == reflect.Pointer && src.Kind() != reflect.Pointer:
		if !populated(src) {
			return
		}
		dst.Set(reflect.New(dst.Type().Elem()))
		assignConverted(t, dst.Elem(), src)
	case dst.Kind() == reflect.Pointer && src.Kind() == reflect.Pointer:
		if src.IsNil() {
			return
		}
		dst.Set(reflect.New(dst.Type().Elem()))
		assignConverted(t, dst.Elem(), src.Elem())
	case dst.Kind() == reflect.Slice && src.Kind() == reflect.Slice:
		if src.Len() == 0 {
			return
		}
		out := reflect.MakeSlice(dst.Type(), src.Len(), src.Len())
		for i := range src.Len() {
			assignConverted(t, out.Index(i), src.Index(i))
		}
		dst.Set(out)
	default:
		if !src.Type().ConvertibleTo(dst.Type()) {
			t.Fatalf("cannot carry a %s onto a %s", src.Type(), dst.Type())
		}
		dst.Set(src.Convert(dst.Type()))
	}
}

// compareRequests checks the whole server-side request, field by field.
//
// The WHOLE request rather than the mapped fields: a parameter that landed
// somewhere it was not meant to is exactly the bug an encoder makes, and
// checking only the fields the table claims to drive would leave it invisible.
func compareRequests(t *testing.T, table Table, want reflect.Value, got any) {
	t.Helper()
	gotValue := reflect.ValueOf(got)
	if gotValue.Type() != table.Target {
		t.Fatalf("the server built a %s, the table targets %s", gotValue.Type(), table.Target)
	}
	for i := range table.Target.NumField() {
		name := table.Target.Field(i).Name
		wantField, gotField := canonical(want.Field(i)), canonical(gotValue.Field(i))
		if !reflect.DeepEqual(wantField, gotField) {
			t.Errorf("%s.%s: the server decoded %#v, the caller meant %#v", table.Target.Name(), name, gotField, wantField)
		}
	}
}

// canonical reduces a request field to a comparable shape.
//
// Instants compare in UTC at nanosecond precision, so a zone the caller
// happened to hold does not read as a difference; an empty slice or map is the
// same as an absent one, because the wire spells both as no parameter at all. A
// nil POINTER stays distinct from a pointed-to zero, which is the distinction
// `limit=0` (unlimited) and an absent limit (the server's default) turn on.
func canonical(v reflect.Value) any {
	if v.Type() == reflect.TypeOf(time.Time{}) {
		return v.Interface().(time.Time).UTC().Format(time.RFC3339Nano)
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return canonical(v.Elem())
	case reflect.Slice:
		if v.Len() == 0 {
			return nil
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = canonical(v.Index(i))
		}
		return out
	case reflect.Map:
		if v.Len() == 0 {
			return nil
		}
		out := map[string]any{}
		for iter := v.MapRange(); iter.Next(); {
			out[fmt.Sprint(iter.Key().Interface())] = canonical(iter.Value())
		}
		return out
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Struct:
		// A nested request shape. It is reduced member by member rather than
		// compared whole, so the rules above still hold INSIDE it: an instant
		// compares in UTC, and an empty slice is the same as an absent one.
		// reflect.DeepEqual on the raw struct would read a zone the caller
		// happened to hold as a difference the wire never carried.
		out := map[string]any{}
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			out[field.Name] = canonical(v.Field(i))
		}
		return out
	default:
		return v.Interface()
	}
}

func recordExercised(exercised map[string]map[string]bool, table Table, source reflect.Value) {
	key := tableName(table)
	if exercised[key] == nil {
		exercised[key] = map[string]bool{}
	}
	for _, entry := range table.Fields {
		if entry.Disposition != DispParam && entry.Disposition != DispPath {
			continue
		}
		if populated(source.FieldByName(entry.Name)) {
			exercised[key][entry.Name] = true
		}
	}
}

// assertEveryEncodedFieldWasExercised is what makes "cover every encoded field"
// a property rather than an intention. A parameter no case ever populates is a
// parameter this gate does not check, and the sweep passing would say otherwise.
func assertEveryEncodedFieldWasExercised(t *testing.T, exercised map[string]map[string]bool) {
	t.Helper()

	// Entries whose encoding has no single decoded request for the sweep to
	// compare against, each named with the test that does cover it. A bare table
	// name redirects the whole table.
	coveredElsewhere := map[string]string{
		"getIssue/searchExactIDs.IDs": "TestTheExactIDsFastPathDialsGetIssue",
		// The descendant walk is not a field correspondence and cannot be
		// checked as one: two of its source members share the `status`
		// parameter, and four of the parameters it sends are INVERSIONS of
		// members that carry no parameter at all, so the mechanical expectation
		// this sweep builds has nothing to build from. Its round trip is a
		// stronger assertion made in its own gate — the server's decoded request
		// is fed back through the same builder and the filter that comes out is
		// compared with the one that went in.
		"listIssues/searchParentWalk": "TestTheParentWalkRoundTripsThroughTheServerDecoder",
	}

	for _, table := range Tables() {
		key := tableName(table)
		if coveredElsewhere[key] != "" {
			continue
		}
		var missing []string
		for _, entry := range table.Fields {
			if entry.Disposition != DispParam && entry.Disposition != DispPath {
				continue
			}
			if exercised[key][entry.Name] || coveredElsewhere[key+"."+entry.Name] != "" {
				continue
			}
			missing = append(missing, entry.Name)
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s: no round-trip case populates %v, so the server never confirmed those parameters", key, missing)
		}
	}
}
