package workapi

import (
	"fmt"
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
// but it also drops template rows of the named type and drops gates, and it
// keeps an infra type's narrowing to its ephemeral rows, which since GH#6258
// a typed count applies with or without IncludeInfra and only IncludeEphemeral
// lifts. A builder that sent IncludeEphemeral through IncludeInfra's branch
// would pass every SkipWisps assertion and bring back exactly the undercount
// the flag was added to avoid, so this asserts the other three fields against
// IncludeInfra — the field that does set them.
func TestCountIncludeEphemeralIsThePlaneBitAndNothingElse(t *testing.T) {
	// The zero ListConfig falls back to the default InfraSet, under which
	// "agent" is an infra type: the one whose count narrows to its ephemeral
	// rows unless IncludeEphemeral lifts that.
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

	// And IncludeInfra still sets all three — its template and gate exclusions,
	// and an infra type's narrowing, which it keeps — so the contrast above is
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

// TestCountAndListPlaneAgreement pins that count and list decide the PLANE
// identically for the same request: whether the wisps TABLE is read at all
// (SkipWisps), and whether a named infra type narrows the read to its
// ephemeral rows (Ephemeral). Both decisions live in applyPlane, which both
// builders call, so a change to either builder alone cannot land here — which
// is the drift this test exists to catch: two count-local copies of the rule
// had already disagreed with list's on exactly the named-infra-type cell
// (GH#6258, the row this test used to pin as "infra type diverges").
//
// The table runs under the zero config's default infra set (agent, role,
// message) and again under a workspace infra set of {robot}, where "agent" is
// NOT infra and "robot" is: the two configs disagree about both named types,
// so the table proves the plane follows the workspace's vocabulary rather
// than the built-in one.
//
// It asserts the ABSOLUTE expected value as well as the agreement. Agreement
// alone is satisfied by a regression that turns a flag into a no-op on BOTH
// builders, which is exactly the drift this exists to catch.
//
// It deliberately says nothing about the two answers being equal in ROWS: a
// count includes templates and a listing does not, whatever the flags.
// See issueops.CountRequest.IncludeEphemeral.
func TestCountAndListPlaneAgreement(t *testing.T) {
	// CustomTypes admits every named type into the workspace vocabulary
	// (BuildListFilter rejects a type it does not know); without the
	// vocabulary the list arm returns an error and the infra cells prove
	// nothing.
	defaultSet := ListConfig{CustomTypes: []string{"agent", "role", "message", "robot"}}
	workspaceSet := ListConfig{
		CustomTypes: []string{"agent", "role", "message", "robot"},
		InfraSet:    map[string]bool{"robot": true},
	}

	for _, cfgCase := range []struct {
		name string
		cfg  ListConfig
	}{
		{"default_infra_set", defaultSet},
		{"workspace_infra_set", workspaceSet},
	} {
		for _, issueType := range []string{"", "task", "agent", "robot"} {
			for _, includeEphemeral := range []bool{false, true} {
				for _, includeInfra := range []bool{false, true} {
					name := fmt.Sprintf("%s/type=%s/ephemeral=%v/infra=%v",
						cfgCase.name, issueType, includeEphemeral, includeInfra)
					t.Run(name, func(t *testing.T) {
						listFilter, err := BuildListFilter(issueops.ListRequest{
							IssueType: issueType, IncludeEphemeral: includeEphemeral, IncludeInfra: includeInfra,
						}, cfgCase.cfg)
						if err != nil {
							t.Fatalf("BuildListFilter: %v", err)
						}
						countFilter, err := BuildCountFilter(issueops.CountRequest{
							IssueType: issueType, IncludeEphemeral: includeEphemeral, IncludeInfra: includeInfra,
						}, cfgCase.cfg)
						if err != nil {
							t.Fatalf("BuildCountFilter: %v", err)
						}

						// The absolute expectation, spelled from the request and the
						// config rather than from either builder.
						wantSkip := !includeEphemeral && !includeInfra && (issueType == "" || !cfgCase.cfg.IsInfra(issueType))
						if countFilter.SkipWisps != wantSkip {
							t.Errorf("count SkipWisps = %v, want %v", countFilter.SkipWisps, wantSkip)
						}
						if listFilter.SkipWisps != wantSkip {
							t.Errorf("list SkipWisps = %v, want %v", listFilter.SkipWisps, wantSkip)
						}
						wantEphemeral := cfgCase.cfg.IsInfra(issueType) && !includeEphemeral
						countEph := countFilter.Ephemeral != nil && *countFilter.Ephemeral
						listEph := listFilter.Ephemeral != nil && *listFilter.Ephemeral
						if countEph != wantEphemeral {
							t.Errorf("count Ephemeral = %v, want %v", countEph, wantEphemeral)
						}
						if listEph != wantEphemeral {
							t.Errorf("list Ephemeral = %v, want %v", listEph, wantEphemeral)
						}

						// The agreement itself, cell by cell.
						if countFilter.SkipWisps != listFilter.SkipWisps {
							t.Errorf("count SkipWisps = %v, list SkipWisps = %v, want equal — "+
								"a change to either builder alone lands here",
								countFilter.SkipWisps, listFilter.SkipWisps)
						}
						if countEph != listEph {
							t.Errorf("count Ephemeral = %v, list Ephemeral = %v, want equal", countEph, listEph)
						}
					})
				}
			}
		}
	}

	// The AC's anchor cells, asserted literally so the table above cannot pass
	// merely by agreeing with itself: under the default infra set, naming
	// "agent" with no flags reads the wisps tier narrowed to ephemeral rows —
	// the cell that used to diverge — while a request naming no infra type
	// stays durable-only.
	anchor, err := BuildCountFilter(issueops.CountRequest{IssueType: "agent"}, defaultSet)
	if err != nil {
		t.Fatalf("BuildCountFilter(agent, default set): %v", err)
	}
	if anchor.SkipWisps {
		t.Error("count --type agent (default set) SkipWisps = true, want false: an infra type reads the wisps tier")
	}
	if anchor.Ephemeral == nil || !*anchor.Ephemeral {
		t.Error("count --type agent (default set) Ephemeral not &true: an infra type narrows to its ephemeral rows")
	}
	for _, issueType := range []string{"", "task"} {
		durable, err := BuildCountFilter(issueops.CountRequest{IssueType: issueType}, defaultSet)
		if err != nil {
			t.Fatalf("BuildCountFilter(%q, default set): %v", issueType, err)
		}
		if !durable.SkipWisps {
			t.Errorf("count --type %q with no flags SkipWisps = false, want true: only an infra type admits the plane", issueType)
		}
	}
}
