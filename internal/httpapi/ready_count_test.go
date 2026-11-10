package httpapi

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The provider-path pins for GET /v0/beads/ready:count. The roles-path twin
// lives in roles_test.go, beside the other store-shaped-source cases.

// TestCountReadyRunsTheListingsQueryUnbounded asserts the property the
// operation exists to deliver: the count runs the SAME ready query the listing
// runs, with the page taken off.
//
// A body carrying the right total says nothing about which set was counted —
// every wrong predicate also produces a number — so the assertions are on the
// filter: the label the wire named, the listing's own sort policy, and Limit 0.
// Limit is the one that would silently break the operation's promise, because
// the shared builder defaults an UNSET limit to workapi.DefaultReadyLimit: a
// count assembled without the role would answer "how many of the first 100" and
// be published as a total.
func TestCountReadyRunsTheListingsQueryUnbounded(t *testing.T) {
	rec := &recordingIssues{items: countedPage()}
	ts := newTestServer(t, Config{Provider: &fakeProvider{
		issues:     &fakeIssues{},
		readIssues: rec,
		readConfig: emptyConfig{},
	}})

	resp := ts.get(t, "/v0/beads/ready:count?label=api")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["total"] != float64(len(countedPage())) {
		t.Errorf("total = %v, want %d — the size of the set the reader answered with", body["total"], len(countedPage()))
	}

	filters := rec.readyFilters()
	if len(filters) != 1 {
		t.Fatalf("%d ready queries, want 1", len(filters))
	}
	if got := filters[0].Limit; got != 0 {
		t.Errorf("Limit = %d, want 0: a bounded count answers \"how many of the first N\" and would be read as a total", got)
	}
	if got := filters[0].Offset; got != 0 {
		t.Errorf("Offset = %d, want 0", got)
	}
	if got := filters[0].SortPolicy; got != types.SortPolicy("priority") {
		t.Errorf("SortPolicy = %q, want the listing's own default: an empty policy is the storage layer's hybrid fallback", got)
	}
	if got := filters[0].Labels; len(got) != 1 || got[0] != "api" {
		t.Errorf("Labels = %v, want the label the wire named", got)
	}
}

// TestCountReadyPublishesNoPageParameters: `limit`, `offset` and `sort` are the
// listing's parameters and are absent from this operation's table, so sending
// one is version skew rather than a bad value and `unknown_parameter` is the
// answer. Silently accepting `limit` would be the failure the role itself
// refuses; silently accepting `sort` would advertise a knob that changes nothing
// about a set with no order.
func TestCountReadyPublishesNoPageParameters(t *testing.T) {
	for _, param := range []string{"limit=5", "limit=0", "offset=1", "sort=oldest"} {
		ts, _ := newReadServer(t, Config{})
		resp := ts.get(t, "/v0/beads/ready:count?"+param)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET ready:count?%s: status = %d, want 400", param, resp.StatusCode)
			continue
		}
		body := decodeBody(t, resp)
		if body["reason"] != string(ReasonUnknownParameter) {
			t.Errorf("GET ready:count?%s: body = %v, want reason=unknown_parameter", param, body)
		}
	}
}

// TestCountReadyAndListReadyAdmitTheSameFilters is the parity the identity
// rests on: every filter the listing accepts, the count accepts too, and both
// build the same predicate from it. A parameter one of them knew and the other
// did not would make "the size of the page you would get" false for exactly the
// clients that sent it, and would fail as a 400 on one path only.
func TestCountReadyAndListReadyAdmitTheSameFilters(t *testing.T) {
	shared := "assignee=alice&unassigned=false&type=bug&exclude_type=epic&label=api&label_any=x" +
		"&exclude_label=wip&label_pattern=tech-*&label_regex=^tech-&priority=1&parent=bd-1" +
		"&metadata_field=team%3Dcore&has_metadata_key=team&include_ephemeral=true&include_deferred=true"

	listing, listRec := newReadServer(t, Config{})
	if resp := listing.get(t, "/v0/beads/ready?"+shared); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ready: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	counting, countRec := newReadServer(t, Config{})
	if resp := counting.get(t, "/v0/beads/ready:count?"+shared); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ready:count: status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	listed, counted := listRec.readyFilters(), countRec.readyFilters()
	if len(listed) != 1 || len(counted) != 1 {
		t.Fatalf("%d listing queries and %d counting queries, want 1 each", len(listed), len(counted))
	}
	// The page is the only thing that may differ.
	want := listed[0]
	want.Limit = 0
	if !reflect.DeepEqual(counted[0], want) {
		t.Errorf("the count and the listing built different predicates from one query string:\ncount:   %+v\nlisting: %+v", counted[0], want)
	}
}
