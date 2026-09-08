package workapi

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestIncludeEphemeralIsThePlaneBitAndNothingElse pins the half of
// ListRequest.IncludeEphemeral the golden file records as a shape but does not
// argue: the field admits the wisp PLANE and takes no TYPE exclusion off.
//
// The golden would pass with the two conflated — `include_ephemeral` would
// simply record whatever ExcludeTypes the builder happened to produce. This
// asserts the difference against IncludeInfra, which is the field that does
// both, so a builder that routed IncludeEphemeral through IncludeInfra's branch
// fails here rather than re-recording a golden.
func TestIncludeEphemeralIsThePlaneBitAndNothingElse(t *testing.T) {
	cfg := ListConfig{}

	plane, err := BuildListFilter(issueops.ListRequest{IncludeEphemeral: true}, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter(IncludeEphemeral): %v", err)
	}
	if plane.SkipWisps {
		t.Error("IncludeEphemeral left SkipWisps set; the wisp plane is exactly what it admits")
	}
	if len(plane.ExcludeTypes) == 0 {
		t.Fatal("IncludeEphemeral dropped every type exclusion; it is a plane knob, not a type knob")
	}
	if !containsIssueType(plane.ExcludeTypes, "message") {
		t.Errorf("ExcludeTypes = %v, want the infra types still excluded — IncludeInfra is the field that takes those off", plane.ExcludeTypes)
	}
	if plane.Ephemeral != nil {
		t.Errorf("Ephemeral = %v, want nil: true would route the query to the wisp plane ALONE, and this admits it in addition", *plane.Ephemeral)
	}

	// The default is unmoved: the zero value is the durable-only listing every
	// caller has today.
	durable, err := BuildListFilter(issueops.ListRequest{}, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter(default): %v", err)
	}
	if !durable.SkipWisps {
		t.Error("a request that did not ask for the ephemeral plane got it anyway")
	}

	// And the two knobs compose rather than fight: IncludeInfra still drops the
	// infra exclusions with the flag also set.
	both, err := BuildListFilter(issueops.ListRequest{IncludeEphemeral: true, IncludeInfra: true}, cfg)
	if err != nil {
		t.Fatalf("BuildListFilter(IncludeEphemeral+IncludeInfra): %v", err)
	}
	if both.SkipWisps {
		t.Error("IncludeEphemeral+IncludeInfra left SkipWisps set")
	}
	if containsIssueType(both.ExcludeTypes, "message") {
		t.Errorf("ExcludeTypes = %v, want the infra types admitted by IncludeInfra", both.ExcludeTypes)
	}
}

// TestReadyProjectionCarriesTheEphemeralPlane pins the ReadyFlag arm's half of
// the same promise. The projection onto WorkFilter is where a list field is
// silently dropped, and a dropped IncludeEphemeral would answer `--ready
// --include-ephemeral` with the durable set while the request read correctly —
// the defect class ValidateReadyFlagScope exists to make impossible.
func TestReadyProjectionCarriesTheEphemeralPlane(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  issueops.ListRequest
		want bool
	}{
		{"a plain ready listing keeps the durable set", issueops.ListRequest{ReadyFlag: true}, false},
		{"IncludeEphemeral crosses", issueops.ListRequest{ReadyFlag: true, IncludeEphemeral: true}, true},
		{"IncludeInfra's plane half crosses with it", issueops.ListRequest{ReadyFlag: true, IncludeInfra: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter, err := BuildListFilter(tc.req, ListConfig{})
			if err != nil {
				t.Fatalf("BuildListFilter: %v", err)
			}
			if got := ReadyFilterFromIssueFilter(filter).IncludeEphemeral; got != tc.want {
				t.Errorf("WorkFilter.IncludeEphemeral = %v, want %v", got, tc.want)
			}
		})
	}
}

func containsIssueType(types_ []types.IssueType, want types.IssueType) bool {
	for _, t := range types_ {
		if t == want {
			return true
		}
	}
	return false
}
