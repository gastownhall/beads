package main

import (
	"reflect"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/workapi"
)

// TestReadyRoleRequestDropsThePage pins what both role-taking questions of
// `bd ready` share: the claim and the count are asked over the LISTING's
// request with the page removed.
//
// Both roles refuse a Limit and an Offset (ErrValidation). For the count,
// published as "showing 2 of N", a limit that survived would make N the size of
// the page instead of the size of the set.
func TestReadyRoleRequestDropsThePage(t *testing.T) {
	got := runGatherReadyInput(t, newReadyFlagsCommand(t, "--limit", "5", "--label", "api"), nil)
	if got.err != nil {
		t.Fatalf("gatherReadyInput: %v", got.err)
	}
	// The listing itself is bounded, which is what makes the drop observable.
	if got.in.Limit == nil || *got.in.Limit != 5 {
		t.Fatalf("the listing request carries limit %v, want 5: the drop below would prove nothing", got.in.Limit)
	}

	req := readyRoleRequest(got.in)
	if req.Limit != nil {
		t.Errorf("role request carries limit %d; both roles refuse one", *req.Limit)
	}
	if req.Offset != 0 {
		t.Errorf("role request carries offset %d; both roles refuse one", req.Offset)
	}
	if !slices.Equal(req.Labels, []string{"api"}) {
		t.Errorf("role request Labels = %q, want the listing's own %q", req.Labels, []string{"api"})
	}

	// The claim asks the same question, plus the claimant: one builder, so the
	// count and the claim cannot drift apart from each other either.
	claim := claimNextRequest(got.in)
	if !reflect.DeepEqual(claim.Filter, req) {
		t.Errorf("the claim's filter and the shared role request differ:\nclaim: %+v\nrole:  %+v", claim.Filter, req)
	}

	// And the predicate the count actually runs is the listing's, unbounded —
	// the identity issueops.ReadyCounter promises, checked at the front door
	// rather than only at the three backends.
	counted, err := workapi.BuildReadyCountFilter(req)
	if err != nil {
		t.Fatalf("BuildReadyCountFilter: %v", err)
	}
	unlimited := 0
	listing := got.in.ReadyRequest
	listing.Limit = &unlimited
	want, err := workapi.BuildReadyFilter(listing)
	if err != nil {
		t.Fatalf("BuildReadyFilter: %v", err)
	}
	if !reflect.DeepEqual(counted, want) {
		t.Errorf("the count's filter and the unbounded listing filter differ:\ncount:   %+v\nlisting: %+v", counted, want)
	}
}

// TestReadyRoleRequestCarriesTheDirectoryLabelDefault pins the one thing the
// role request adds that the raw flags do not: GH#541's configured label,
// applied under the same gate the listing applies it under. A count or a claim
// that missed the default would answer for a wider set than the listing beside
// it shows.
func TestReadyRoleRequestCarriesTheDirectoryLabelDefault(t *testing.T) {
	const configured = "scope:web"
	configureDirectoryLabel(t, configured)

	got := runGatherReadyInput(t, newReadyFlagsCommand(t), nil)
	if got.err != nil {
		t.Fatalf("gatherReadyInput: %v", got.err)
	}
	if req := readyRoleRequest(got.in); !slices.Equal(req.LabelsAny, []string{configured}) {
		t.Errorf("role request LabelsAny = %q, want the configured %q", req.LabelsAny, []string{configured})
	}

	// An explicit label suppresses it, the same way it suppresses the
	// listing's.
	explicit := runGatherReadyInput(t, newReadyFlagsCommand(t, "--label", "chosen"), nil)
	if explicit.err != nil {
		t.Fatalf("gatherReadyInput: %v", explicit.err)
	}
	if req := readyRoleRequest(explicit.in); len(req.LabelsAny) != 0 {
		t.Errorf("role request LabelsAny = %q with an explicit --label, want none", req.LabelsAny)
	}
}
