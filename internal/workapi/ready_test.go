package workapi

import (
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestBuildReadyFilterPropagatesLabelPatternAndRegex moved here from cmd/bd's
// ready_input_test.go with the builder it covers (bd-ehi).
func TestBuildReadyFilterPropagatesLabelPatternAndRegex(t *testing.T) {
	filter, err := BuildReadyFilter(issueops.ReadyRequest{
		LabelPattern: "tech-*",
		LabelRegex:   "^tech-(debt|legacy)$",
		Sort:         "priority",
	})
	if err != nil {
		t.Fatalf("BuildReadyFilter() error = %v", err)
	}
	if got, want := filter.LabelPattern, "tech-*"; got != want {
		t.Errorf("filter.LabelPattern = %q, want %q", got, want)
	}
	if got, want := filter.LabelRegex, "^tech-(debt|legacy)$"; got != want {
		t.Errorf("filter.LabelRegex = %q, want %q", got, want)
	}
}

// TestBuildReadyFilterNormalizes covers the normalization a frontend gets for
// free. The CLI reaches most of it pre-split by pflag, so these are the cases
// only a non-CLI caller hits.
func TestBuildReadyFilterNormalizes(t *testing.T) {
	filter, err := BuildReadyFilter(issueops.ReadyRequest{
		IssueType:     "mr",
		Labels:        []string{" alpha ", "alpha", "  ", "beta"},
		LabelsAny:     []string{"gamma", "gamma"},
		ExcludeLabels: []string{" delta "},
		ExcludeTypes:  []string{"epic, mol", "", "  "},
		Sort:          "priority",
	})
	if err != nil {
		t.Fatalf("BuildReadyFilter() error = %v", err)
	}
	if got, want := filter.Type, "merge-request"; got != want {
		t.Errorf("filter.Type = %q, want %q (alias expansion)", got, want)
	}
	if got, want := filter.Labels, []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
		t.Errorf("filter.Labels = %q, want %q", got, want)
	}
	if got, want := filter.LabelsAny, []string{"gamma"}; !reflect.DeepEqual(got, want) {
		t.Errorf("filter.LabelsAny = %q, want %q", got, want)
	}
	if got, want := filter.ExcludeLabels, []string{"delta"}; !reflect.DeepEqual(got, want) {
		t.Errorf("filter.ExcludeLabels = %q, want %q", got, want)
	}
	want := []types.IssueType{types.IssueType("epic"), types.IssueType("molecule")}
	if !reflect.DeepEqual(filter.ExcludeTypes, want) {
		t.Errorf("filter.ExcludeTypes = %v, want %v", filter.ExcludeTypes, want)
	}
}

// TestBuildReadyFilterRejectsSortPolicy pins the message cmd/bd used to print
// itself: `bd ready --sort bogus` renders this error verbatim behind "Error: ".
func TestBuildReadyFilterRejectsSortPolicy(t *testing.T) {
	_, err := BuildReadyFilter(issueops.ReadyRequest{Sort: "bogus"})
	if err == nil {
		t.Fatal("BuildReadyFilter unexpectedly accepted an invalid sort policy")
	}
	if got, want := err.Error(), "invalid sort policy 'bogus'. Valid values: hybrid, priority, oldest"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	// An unset policy is the library default, not a usage error: storage
	// resolves it the same way it resolves "priority".
	if _, err := BuildReadyFilter(issueops.ReadyRequest{}); err != nil {
		t.Errorf("BuildReadyFilter with no sort policy: %v", err)
	}
}

// TestBuildReadyFilterCtxKeepsNilAndEmptyApart pins the one subtlety the
// declared-context predicate rests on: an undeclared context (nil) reaches the
// filter as nil and switches the predicate off, while a declared-but-empty one
// (`bd ready --ctx ”`) reaches it as a non-nil empty slice and hides every
// requiring issue. Routing the slice through utils.NormalizeLabels would merge
// the two, which is why BuildReadyFilter must not.
func TestBuildReadyFilterCtxKeepsNilAndEmptyApart(t *testing.T) {
	t.Parallel()

	undeclared, err := BuildReadyFilter(issueops.ReadyRequest{})
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	if undeclared.Ctx != nil {
		t.Fatalf("undeclared context must stay nil, got %#v", undeclared.Ctx)
	}

	empty, err := BuildReadyFilter(issueops.ReadyRequest{Ctx: []string{""}})
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	if empty.Ctx == nil || len(empty.Ctx) != 0 {
		t.Fatalf("declared-empty context must be a non-nil empty slice, got %#v", empty.Ctx)
	}

	tokens, err := BuildReadyFilter(issueops.ReadyRequest{Ctx: []string{" host=studio ", "gpu", "gpu"}})
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	if got, want := tokens.Ctx, []string{"host=studio", "gpu"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Ctx = %q, want %q", got, want)
	}

	// `bd list --ready --ctx` reaches the ready query through the IssueFilter
	// bridge, so the field must cross it unchanged — including the nil case.
	if got := ReadyFilterFromIssueFilter(types.IssueFilter{Ctx: []string{}}).Ctx; got == nil || len(got) != 0 {
		t.Fatalf("bridge dropped a declared-empty context: %#v", got)
	}
	if got := ReadyFilterFromIssueFilter(types.IssueFilter{}).Ctx; got != nil {
		t.Fatalf("bridge invented a context: %#v", got)
	}
}
