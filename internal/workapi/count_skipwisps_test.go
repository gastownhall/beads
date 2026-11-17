package workapi

import (
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestBuildCountFilter_IncludeEphemeral pins the plane knob on the count side.
//
// The wisps TABLE holds no_history beads (ephemeral = 0) alongside true wisps
// (ephemeral = 1), and the write path routes on STORAGE CLASS, not type — so a
// no_history task lives there while remaining ordinary durable work. A count
// that names a type therefore has to be able to reach that table, and until
// IncludeEphemeral existed the only way was IncludeInfra, which ALSO drops
// template rows of the named type: one silent undercount traded for another.
//
// The default before this change must stay byte-identical: durable plane only.
func TestBuildCountFilter_IncludeEphemeral(t *testing.T) {
	cfg := ListConfig{}

	cases := []struct {
		name          string
		in            issueops.CountRequest
		wantSkipWisps bool
	}{
		{
			name:          "unfiltered count stays durable-only",
			in:            issueops.CountRequest{},
			wantSkipWisps: true,
		},
		{
			name:          "a named type alone stays durable-only (the default before this change)",
			in:            issueops.CountRequest{IssueType: "task"},
			wantSkipWisps: true,
		},
		{
			name:          "--include-ephemeral admits the plane",
			in:            issueops.CountRequest{IncludeEphemeral: true},
			wantSkipWisps: false,
		},
		{
			name:          "--include-ephemeral with a named type admits the plane",
			in:            issueops.CountRequest{IssueType: "task", IncludeEphemeral: true},
			wantSkipWisps: false,
		},
		{
			name:          "--include-infra still admits the plane on its own",
			in:            issueops.CountRequest{IssueType: "task", IncludeInfra: true},
			wantSkipWisps: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			filter, err := BuildCountFilter(tt.in, cfg)
			if err != nil {
				t.Fatalf("BuildCountFilter: %v", err)
			}
			if filter.SkipWisps != tt.wantSkipWisps {
				t.Errorf("SkipWisps = %v, want %v", filter.SkipWisps, tt.wantSkipWisps)
			}
		})
	}
}

// TestCountIncludeEphemeralIsThePlaneBitAndNothingElse is the count side of
// TestIncludeEphemeralIsThePlaneBitAndNothingElse. It pins the half of
// CountRequest.IncludeEphemeral that the SkipWisps table above cannot see: the
// field admits the wisps PLANE and changes nothing else.
//
// That half is why the field exists. IncludeInfra already admitted the plane,
// but it also drops template rows of the named type, drops gates and routes an
// infra type to the ephemeral tier. A builder that sent IncludeEphemeral
// through IncludeInfra's branch would pass every SkipWisps assertion and bring
// back exactly the undercount the flag was added to avoid, so this asserts the
// other three fields against IncludeInfra — the field that does set them.
func TestCountIncludeEphemeralIsThePlaneBitAndNothingElse(t *testing.T) {
	// The zero ListConfig falls back to the default InfraSet, under which
	// "agent" is an infra type: the one IncludeInfra would route to the
	// ephemeral tier.
	cfg := ListConfig{}

	for _, tt := range []struct {
		name      string
		issueType string
	}{
		{"no type", ""},
		{"a named type", "task"},
		{"an infra type", "agent"},
	} {
		t.Run("include-ephemeral with "+tt.name, func(t *testing.T) {
			plane, err := BuildCountFilter(issueops.CountRequest{IssueType: tt.issueType, IncludeEphemeral: true}, cfg)
			if err != nil {
				t.Fatalf("BuildCountFilter(IncludeEphemeral): %v", err)
			}
			if plane.SkipWisps {
				t.Error("IncludeEphemeral left SkipWisps set; the wisps plane is exactly what it admits")
			}
			if plane.IsTemplate != nil {
				t.Errorf("IsTemplate = %v, want nil: a count includes templates, and dropping them is the undercount this flag exists to avoid", *plane.IsTemplate)
			}
			if len(plane.ExcludeTypes) != 0 {
				t.Errorf("ExcludeTypes = %v, want none: the gate exclusion is IncludeInfra's, not the plane's", plane.ExcludeTypes)
			}
			if plane.Ephemeral != nil {
				t.Errorf("Ephemeral = %v, want nil: true would route the count to the wisps tier ALONE, and this admits it in addition", *plane.Ephemeral)
			}
		})
	}

	// And IncludeInfra still makes all four changes, so the contrast above is
	// drawn against the field it has to differ from.
	for _, tt := range []struct {
		name          string
		issueType     string
		wantEphemeral bool
	}{
		{"a named type", "task", false},
		{"an infra type", "agent", true},
	} {
		t.Run("include-infra with "+tt.name, func(t *testing.T) {
			infra, err := BuildCountFilter(issueops.CountRequest{IssueType: tt.issueType, IncludeInfra: true}, cfg)
			if err != nil {
				t.Fatalf("BuildCountFilter(IncludeInfra): %v", err)
			}
			if infra.SkipWisps {
				t.Error("IncludeInfra left SkipWisps set")
			}
			if infra.IsTemplate == nil || *infra.IsTemplate {
				t.Error("IncludeInfra did not set IsTemplate=false; it drops templates to match `bd list --include-infra`")
			}
			if !containsIssueType(infra.ExcludeTypes, "gate") {
				t.Errorf("ExcludeTypes = %v, want gate excluded under IncludeInfra", infra.ExcludeTypes)
			}
			if tt.wantEphemeral {
				if infra.Ephemeral == nil || !*infra.Ephemeral {
					t.Error("IncludeInfra did not set Ephemeral=true for an infra type; it routes that type to the ephemeral tier")
				}
			} else if infra.Ephemeral != nil {
				t.Errorf("Ephemeral = %v, want nil: only an infra type is routed to the ephemeral tier", *infra.Ephemeral)
			}
		})
	}
}

// TestCountAndListPlaneAgreement pins how count and list decide the PLANE for
// the same request — including the ONE case where they disagree.
//
// The name is not "AgreeOnThePlane" because they do not always agree, and a
// test that claimed they did would be asserting a property the code lacks. For
// an INFRA type they diverge: list reads the plane (applyTypeSuppressions
// exempts infra types) while count does not, so `bd count --type agent` answers
// 0 where `bd list --type agent` returns rows.
//
// That divergence predates the flag — the count arm is a bare `else {
// SkipWisps = true }`, so it answers 0 for an infra type with or without this
// change. It is pinned here, not fixed: the fix is a separate decision about
// what naming an infra type should admit, and this change deliberately alters
// nothing about the default arm.
//
// It asserts the ABSOLUTE expected value as well as the agreement. Agreement
// alone is satisfied by a regression that turns the flag into a no-op on BOTH
// builders, which is exactly the drift this exists to catch.
//
// It deliberately says nothing about the two answers being equal in ROWS: a
// count includes templates and a listing does not, with or without this flag.
// See issueops.CountRequest.IncludeEphemeral.
func TestCountAndListPlaneAgreement(t *testing.T) {
	// CustomTypes admits "agent" into the workspace vocabulary (BuildListFilter
	// rejects a type it does not know), and the default InfraSet already treats
	// it as infra. Without the vocabulary the builders return an error and the
	// infra cases below prove nothing.
	cfg := ListConfig{CustomTypes: []string{"agent", "role", "message"}}

	for _, tt := range []struct {
		name             string
		issueType        string
		includeEphemeral bool
		wantCountSkip    bool
		wantAgree        bool
	}{
		{"default", "", false, true, true},
		{"named type", "task", false, true, true},
		{"include-ephemeral", "", true, false, true},
		{"named type + include-ephemeral", "task", true, false, true},
		// The known divergence. list reads the plane for an infra type, count
		// does not; --include-ephemeral recovers count.
		{"infra type diverges", "agent", false, true, false},
		{"infra type + include-ephemeral", "agent", true, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			listFilter, err := BuildListFilter(issueops.ListRequest{
				IssueType: tt.issueType, IncludeEphemeral: tt.includeEphemeral,
			}, cfg)
			if err != nil {
				t.Fatalf("BuildListFilter: %v", err)
			}
			countFilter, err := BuildCountFilter(issueops.CountRequest{
				IssueType: tt.issueType, IncludeEphemeral: tt.includeEphemeral,
			}, cfg)
			if err != nil {
				t.Fatalf("BuildCountFilter: %v", err)
			}
			if countFilter.SkipWisps != tt.wantCountSkip {
				t.Errorf("count SkipWisps = %v, want %v", countFilter.SkipWisps, tt.wantCountSkip)
			}
			agree := listFilter.SkipWisps == countFilter.SkipWisps
			if agree != tt.wantAgree {
				t.Errorf("list SkipWisps=%v count SkipWisps=%v (agree=%v), want agree=%v — "+
					"a change to either builder alone lands here",
					listFilter.SkipWisps, countFilter.SkipWisps, agree, tt.wantAgree)
			}
		})
	}
}
