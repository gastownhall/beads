package encode

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi/spec"
	"github.com/steveyegge/beads/issueops"
	"gopkg.in/yaml.v3"
)

// TestListFiltersMembersAreTheTokenedParameters holds three statements of the
// issues.list.filters vocabulary equal: ListFiltersMembers (what the pre-dial
// gate asks about), the parameters listTable maps those members to (what the
// encoder sends), and the listIssues parameters whose document description
// cites the token (what the server announces behind it). A member sent without
// the gate asking would earn an older server's 400; a gated member the server
// does not announce would refuse against a server that serves it.
func TestListFiltersMembersAreTheTokenedParameters(t *testing.T) {
	table, ok := TableFor(OpListIssues, "")
	if !ok {
		t.Fatal("no listIssues table")
	}
	entry := map[string]FieldEntry{}
	for _, e := range table.Fields {
		entry[e.Name] = e
	}
	var sent []string
	for _, member := range ListFiltersMembers {
		if _, ok := reflect.TypeOf(issueops.ListRequest{}).FieldByName(member); !ok {
			t.Errorf("ListFiltersMembers names %s, which issueops.ListRequest does not declare", member)
			continue
		}
		e, ok := entry[member]
		if !ok || e.Disposition != DispParam {
			t.Errorf("listTable does not map %s to a parameter (entry %+v)", member, e)
			continue
		}
		sent = append(sent, e.Param)
	}

	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
			Parameters  []struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
			} `yaml:"parameters"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec.OpenAPIV0(), &doc); err != nil {
		t.Fatalf("parse the wire contract: %v", err)
	}
	var announced []string
	for _, methods := range doc.Paths {
		for _, op := range methods {
			if op.OperationID != string(OpListIssues) {
				continue
			}
			for _, p := range op.Parameters {
				if strings.Contains(p.Description, "`issues.list.filters`") {
					announced = append(announced, p.Name)
				}
			}
		}
	}
	slices.Sort(sent)
	slices.Sort(announced)
	if !slices.Equal(sent, announced) {
		t.Errorf("the gated members encode to %v, the document announces %v behind issues.list.filters", sent, announced)
	}
}

// TestListFiltersSetSeesEachMember: the gate's question is the encoder's own
// notion of populated, member by member.
func TestListFiltersSetSeesEachMember(t *testing.T) {
	if ListFiltersSet(issueops.ListRequest{}) {
		t.Error("an empty request reports a filter member")
	}
	if ListFiltersSet(issueops.ListRequest{ExcludeTypes: []string{}, Status: "open", TitleSearch: "x", Limit: new(int)}) {
		t.Error("a request with only ungated members (and an empty exclusion list) reports a filter member")
	}
	zero := 0
	empty := issueops.MolType("")
	for name, req := range map[string]issueops.ListRequest{
		"Priority=0":    {Priority: &zero},
		"MolType=&\"\"": {MolType: &empty},
		"ExcludeTypes":  {ExcludeTypes: []string{"epic"}},
		"ExcludeStatus": {ExcludeStatus: []string{"closed"}},
		"OverdueFlag":   {OverdueFlag: true},
		"DueBefore":     {DueBefore: &time.Time{}},
	} {
		if !ListFiltersSet(req) {
			t.Errorf("%s: ListFiltersSet = false, want true", name)
		}
	}
}

// TestListParamsSendsTheFilterBoundsAsTheResolvedInstant pins how the bounds
// cross: the instant the CLI resolved, in UTC, with its sub-second part — the
// truncation to whole seconds is the storage layer's on both routes, so the
// wire must not pre-truncate (which would move a bound) or keep an offset.
func TestListParamsSendsTheFilterBoundsAsTheResolvedInstant(t *testing.T) {
	at := time.Date(2026, 3, 4, 7, 6, 7, 900_000_000, time.FixedZone("UTC+2", 2*60*60))
	zero := 0
	empty := issueops.WispType("")
	v, err := ListParams(issueops.ListRequest{
		ClosedAfter: &at, DueBefore: &at, Priority: &zero, WispType: &empty,
		ExcludeTypes: []string{"epic", "chore"}, ExcludeStatus: []string{"closed", "blocked"},
	})
	if err != nil {
		t.Fatalf("ListParams: %v", err)
	}
	for _, param := range []string{"closed_after", "due_before"} {
		if got := v.Get(param); got != "2026-03-04T05:06:07.9Z" {
			t.Errorf("%s = %q, want 2026-03-04T05:06:07.9Z", param, got)
		}
	}
	if got := v.Get("priority"); got != "0" {
		t.Errorf("priority = %q, want 0 (a set pointer to priority 0 is a real filter)", got)
	}
	if got, ok := v["wisp_type"]; !ok || !slices.Equal(got, []string{""}) {
		t.Errorf("wisp_type = %v (present %v), want the empty value sent for the server to refuse by name", got, ok)
	}
	if got := v["exclude_status"]; !slices.Equal(got, []string{"closed", "blocked"}) {
		t.Errorf("exclude_status = %v, want one value per member", got)
	}
	if got := v["exclude_type"]; !slices.Equal(got, []string{"epic", "chore"}) {
		t.Errorf("exclude_type = %v, want one value per member", got)
	}
}
