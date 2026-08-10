package workapi

import (
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestBuildCountFilterIncludeInfraMirrorsListFilter pins `bd count
// --include-infra` to the exact cardinality semantics of `bd list
// --include-infra --all` (GH#4387): for any filter set, the count must equal
// the number of rows the equivalent list invocation returns. The trap
// dimensions are the wisps merge (SkipWisps), template exclusion (IsTemplate),
// the default gate exclusion (ExcludeTypes), and infra-type routing to the
// ephemeral tier (Ephemeral).
func TestBuildCountFilterIncludeInfraMirrorsListFilter(t *testing.T) {
	cfg := ListConfig{}
	for _, issueType := range []string{"", "task", "gate", "message"} {
		name := issueType
		if name == "" {
			name = "none"
		}
		t.Run("type_"+name, func(t *testing.T) {
			want, err := BuildListFilter(issueops.ListRequest{
				AllFlag: true, IncludeInfra: true, IssueType: issueType,
			}, cfg)
			if err != nil {
				t.Fatalf("BuildListFilter(%q): %v", issueType, err)
			}
			got, err := BuildCountFilter(issueops.CountRequest{
				IncludeInfra: true, IssueType: issueType,
			}, cfg)
			if err != nil {
				t.Fatalf("BuildCountFilter(%q): %v", issueType, err)
			}

			if got.SkipWisps != want.SkipWisps {
				t.Errorf("SkipWisps = %v, list --include-infra --all uses %v", got.SkipWisps, want.SkipWisps)
			}
			if !reflect.DeepEqual(got.IsTemplate, want.IsTemplate) {
				t.Errorf("IsTemplate = %v, list --include-infra --all uses %v", countPtrStr(got.IsTemplate), countPtrStr(want.IsTemplate))
			}
			if !reflect.DeepEqual(got.ExcludeTypes, want.ExcludeTypes) {
				t.Errorf("ExcludeTypes = %v, list --include-infra --all uses %v", got.ExcludeTypes, want.ExcludeTypes)
			}
			if !reflect.DeepEqual(got.Ephemeral, want.Ephemeral) {
				t.Errorf("Ephemeral = %v, list --include-infra --all uses %v", countPtrStr(got.Ephemeral), countPtrStr(want.Ephemeral))
			}
			if !reflect.DeepEqual(got.IssueType, want.IssueType) {
				t.Errorf("IssueType = %v, list --include-infra --all uses %v", got.IssueType, want.IssueType)
			}
			// A count defaults to all statuses and all pinned states, which is
			// exactly what list's --all flag selects.
			if !reflect.DeepEqual(got.Status, want.Status) {
				t.Errorf("Status = %v, list --include-infra --all uses %v", got.Status, want.Status)
			}
			if !reflect.DeepEqual(got.Statuses, want.Statuses) {
				t.Errorf("Statuses = %v, list --include-infra --all uses %v", got.Statuses, want.Statuses)
			}
			if !reflect.DeepEqual(got.ExcludeStatus, want.ExcludeStatus) {
				t.Errorf("ExcludeStatus = %v, list --include-infra --all uses %v", got.ExcludeStatus, want.ExcludeStatus)
			}
			if !reflect.DeepEqual(got.Pinned, want.Pinned) {
				t.Errorf("Pinned = %v, list --include-infra --all uses %v", countPtrStr(got.Pinned), countPtrStr(want.Pinned))
			}
		})
	}
}

// TestBuildCountFilterHonorsTheWorkspaceInfraSet verifies that the infra-type
// routing honors a store-configured infra set, exactly like bd list does.
func TestBuildCountFilterHonorsTheWorkspaceInfraSet(t *testing.T) {
	cfg := ListConfig{InfraSet: map[string]bool{"robot": true}}

	robot, err := BuildCountFilter(issueops.CountRequest{IncludeInfra: true, IssueType: "robot"}, cfg)
	if err != nil {
		t.Fatalf("BuildCountFilter(robot): %v", err)
	}
	if robot.Ephemeral == nil || !*robot.Ephemeral {
		t.Errorf("custom infra type %q must route to the ephemeral tier (Ephemeral=true), got %v", "robot", countPtrStr(robot.Ephemeral))
	}

	// "message" is a default infra type but NOT part of the custom set, so it
	// must not route to the ephemeral tier (mirrors ListConfig.IsInfra).
	msg, err := BuildCountFilter(issueops.CountRequest{IncludeInfra: true, IssueType: "message"}, cfg)
	if err != nil {
		t.Fatalf("BuildCountFilter(message): %v", err)
	}
	if msg.Ephemeral != nil {
		t.Errorf("non-infra type under custom set must keep Ephemeral=nil, got %v", countPtrStr(msg.Ephemeral))
	}
}

// TestBuildCountFilterDefaultsToTheDurablePlane pins the no-flag path, which
// is the answer every scripted `bd count` has always read: durable rows only
// (SkipWisps), and none of the listing's template, gate, status or pinned
// exclusions.
func TestBuildCountFilterDefaultsToTheDurablePlane(t *testing.T) {
	got, err := BuildCountFilter(issueops.CountRequest{}, ListConfig{})
	if err != nil {
		t.Fatalf("BuildCountFilter: %v", err)
	}
	if !got.SkipWisps {
		t.Error("SkipWisps = false without IncludeInfra, want the durable plane only")
	}
	if got.IsTemplate != nil {
		t.Errorf("IsTemplate = %v without IncludeInfra, want no template predicate: a count includes templates", countPtrStr(got.IsTemplate))
	}
	if len(got.ExcludeTypes) != 0 {
		t.Errorf("ExcludeTypes = %v without IncludeInfra, want none: a count excludes no type by default", got.ExcludeTypes)
	}
	if got.Ephemeral != nil {
		t.Errorf("Ephemeral = %v without IncludeInfra, want nil", countPtrStr(got.Ephemeral))
	}
	if got.Status != nil || len(got.ExcludeStatus) != 0 || got.Pinned != nil {
		t.Errorf("a bare count filter carries status/pinned predicates (Status=%v ExcludeStatus=%v Pinned=%v): a count answers for closed and pinned rows too",
			countPtrStr(got.Status), got.ExcludeStatus, countPtrStr(got.Pinned))
	}
	// Limit and Offset are the two fields the count seam ignores, and
	// issueops.CountRequest has no knob for either: pinning them at zero says
	// the builder does not invent one.
	if got.Limit != 0 || got.Offset != 0 {
		t.Errorf("Limit=%d Offset=%d, want a count filter to bound nothing", got.Limit, got.Offset)
	}
}

// TestBuildCountFilterNormalizesLabelsAndIDs pins the normalization
// issueops.CountRequest promises happens INSIDE: entries are trimmed and
// de-duplicated, an all-blank slice is the same as an unset one, and the
// caller's own slices come back untouched.
func TestBuildCountFilterNormalizesLabelsAndIDs(t *testing.T) {
	labels := []string{" alpha ", "alpha", "", "beta"}
	labelsAny := []string{"  ", ""}
	snapshot := append([]string(nil), labels...)

	got, err := BuildCountFilter(issueops.CountRequest{
		Labels:    labels,
		LabelsAny: labelsAny,
		IDFilter:  " bd-1 , bd-2 ,, bd-1 ",
	}, ListConfig{})
	if err != nil {
		t.Fatalf("BuildCountFilter: %v", err)
	}
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(got.Labels, want) {
		t.Errorf("Labels = %v, want %v", got.Labels, want)
	}
	if got.LabelsAny != nil {
		t.Errorf("LabelsAny = %v, want nil: a slice of blanks is the same as an unset one", got.LabelsAny)
	}
	if want := []string{"bd-1", "bd-2"}; !reflect.DeepEqual(got.IDs, want) {
		t.Errorf("IDs = %v, want %v", got.IDs, want)
	}
	if !reflect.DeepEqual(labels, snapshot) {
		t.Errorf("the caller's Labels became %v, want them left as %v", labels, snapshot)
	}
}

// TestBuildCountFilterTakesStatusAndTypeAsWritten pins the deliberate absence
// of validation: an unrecognized status or type reaches the filter as written
// and matches nothing, rather than failing. A scripted caller counting a
// retired status reads 0.
func TestBuildCountFilterTakesStatusAndTypeAsWritten(t *testing.T) {
	got, err := BuildCountFilter(issueops.CountRequest{Status: "no-such-status", IssueType: "no-such-type"}, ListConfig{})
	if err != nil {
		t.Fatalf("BuildCountFilter with unknown vocabulary: %v, want the filter as written", err)
	}
	if got.Status == nil || *got.Status != types.Status("no-such-status") {
		t.Errorf("Status = %v, want the literal the caller wrote", countPtrStr(got.Status))
	}
	if got.IssueType == nil || *got.IssueType != types.IssueType("no-such-type") {
		t.Errorf("IssueType = %v, want the literal the caller wrote", countPtrStr(got.IssueType))
	}

	// "all" and the empty string are the two spellings of "every status".
	for _, status := range []string{"", "all"} {
		filter, err := BuildCountFilter(issueops.CountRequest{Status: status}, ListConfig{})
		if err != nil {
			t.Fatalf("BuildCountFilter(status=%q): %v", status, err)
		}
		if filter.Status != nil {
			t.Errorf("status %q produced Status = %v, want no status predicate", status, countPtrStr(filter.Status))
		}
	}
}

// TestValidateCountGroupClosesTheDimensionSet pins the five published
// dimensions and the typed refusal for everything else. The refusal has to be
// ErrValidation rather than a bare string because both front doors classify it,
// and because an empty GroupBy reaching storage comes back as an "unsupported
// groupBy" that no caller can match on.
func TestValidateCountGroupClosesTheDimensionSet(t *testing.T) {
	for group, want := range map[issueops.CountGroup]string{
		issueops.CountGroupStatus:   "status",
		issueops.CountGroupPriority: "priority",
		issueops.CountGroupType:     "type",
		issueops.CountGroupAssignee: "assignee",
		issueops.CountGroupLabel:    "label",
	} {
		got, err := ValidateCountGroup(group)
		if err != nil {
			t.Errorf("ValidateCountGroup(%q): %v", group, err)
			continue
		}
		if got != want {
			t.Errorf("ValidateCountGroup(%q) = %q, want %q", group, got, want)
		}
	}
	for _, group := range []issueops.CountGroup{"", "Status", "owner", "label "} {
		if _, err := ValidateCountGroup(group); !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("ValidateCountGroup(%q) error = %v, want ErrValidation", group, err)
		}
	}
}

func countPtrStr[T any](p *T) string {
	if p == nil {
		return "<nil>"
	}
	return "&" + reflect.ValueOf(*p).String()
}
