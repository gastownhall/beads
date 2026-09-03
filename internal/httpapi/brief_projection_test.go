package httpapi

import (
	"net/http"
	"testing"
)

// TestListBriefReachesTheFilter is the HTTP half of the `bd list --brief`
// wiring. The CLI and this handler build issueops.ListRequest separately, so a
// field wired into one is unreachable from the other.
//
// Asserted on the storage FILTER rather than on the request, because the
// filter is what the query is built from: an assignment that reached
// ListRequest and stopped there would satisfy a request-level assertion while
// the rows still came back whole.
func TestListBriefReachesTheFilter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		wantLite bool
	}{
		{"absent leaves the payload whole", "/v0/beads/issues", false},
		{"brief projects", "/v0/beads/issues?brief=true", true},
		{"brief=false is explicit and whole", "/v0/beads/issues?brief=false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, rec := newReadServer(t, Config{})
			if resp := ts.get(t, tc.path); resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200", tc.path, resp.StatusCode)
			}
			filters := rec.searchFilters()
			if len(filters) != 1 {
				t.Fatalf("%d list queries, want 1", len(filters))
			}
			if got := filters[0].Lite; got != tc.wantLite {
				t.Errorf("IssueFilter.Lite = %v, want %v", got, tc.wantLite)
			}
		})
	}
}

// TestReadyBriefReachesTheFilter is the same hop for the ready listing.
func TestReadyBriefReachesTheFilter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		wantLite bool
	}{
		{"absent leaves the payload whole", "/v0/beads/ready", false},
		{"brief projects", "/v0/beads/ready?brief=true", true},
		{"brief=false is explicit and whole", "/v0/beads/ready?brief=false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, rec := newReadServer(t, Config{})
			if resp := ts.get(t, tc.path); resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200", tc.path, resp.StatusCode)
			}
			filters := rec.readyFilters()
			if len(filters) != 1 {
				t.Fatalf("%d ready queries, want 1", len(filters))
			}
			if got := filters[0].Lite; got != tc.wantLite {
				t.Errorf("WorkFilter.Lite = %v, want %v", got, tc.wantLite)
			}
		})
	}
}

// TestReadyCountDoesNotTakeTheProjection pins the decision to decode `brief` in
// handleListReadyWork rather than in readyFilters, the vocabulary the count
// shares with the listing.
//
// readyFilters is documented as the filters both operations must admit, since a
// parameter one decoded and the other did not would break the identity between
// the count and the set it sizes. This projection is not one of those: it
// selects FIELDS, and a count returns no rows to project. Decoding it there
// would advertise a parameter on `:count` that cannot change its answer.
//
// The 400 is the shared decoder's strict unknown-parameter behavior, so what
// this pins is that the parameter was not quietly added to the shared function.
// The reason is asserted, not just the status: unknown_parameter tells a client
// this server predates the parameter, and invalid_value would tell it to send
// something else, so a bare status check would accept the wrong instruction.
func TestReadyCountDoesNotTakeTheProjection(t *testing.T) {
	ts, _ := newReadServer(t, Config{})

	resp := ts.get(t, "/v0/beads/ready:count?brief=true")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a count has no rows to project, so the parameter is not in its vocabulary", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["param"] != "brief" || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("body = %v, want param=brief reason=unknown_parameter", body)
	}
}
