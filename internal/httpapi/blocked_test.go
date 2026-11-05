package httpapi

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These cover the transport half of GET /v0/beads/blocked: the parameter
// mapping, the refusals the handler raises itself and the projection. The set,
// its narrowing and its order are issueops.BlockedLister's, pinned by
// backend/conformance/blocked_lister_contract.go on every leg, the served one
// included.

func newBlockedServer(t *testing.T, lister *roleBlockedLister) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{BlockedLister: lister}))
}

// TestListBlockedMapsEveryParameter: each `bd blocked` flag's parameter
// reaches the role as sent, labels repeated, and nothing else is set.
func TestListBlockedMapsEveryParameter(t *testing.T) {
	lister := &roleBlockedLister{}
	ts := newBlockedServer(t, lister)

	resp := ts.get(t, "/v0/beads/blocked?parent=bd-p&label=a&label=b&label_any=c&exclude_label=d&exclude_label=e")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	calls := lister.blockedRequests()
	if len(calls) != 1 {
		t.Fatalf("role calls = %d, want 1", len(calls))
	}
	want := issueops.BlockedRequest{
		ParentID:      "bd-p",
		Labels:        []string{"a", "b"},
		LabelsAny:     []string{"c"},
		ExcludeLabels: []string{"d", "e"},
	}
	if !reflect.DeepEqual(calls[0], want) {
		t.Fatalf("role request = %+v, want %+v", calls[0], want)
	}
}

// TestListBlockedWithNoParametersAsksTheWholeSet: an empty query is the
// unnarrowed listing.
func TestListBlockedWithNoParametersAsksTheWholeSet(t *testing.T) {
	lister := &roleBlockedLister{}
	ts := newBlockedServer(t, lister)

	resp := ts.get(t, "/v0/beads/blocked")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	calls := lister.blockedRequests()
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], issueops.BlockedRequest{}) {
		t.Fatalf("role requests = %+v, want one empty request", calls)
	}
}

// TestListBlockedRefusesAnUnknownParameter: a parameter the operation does
// not publish is a 400 naming it, and the role is never asked.
func TestListBlockedRefusesAnUnknownParameter(t *testing.T) {
	lister := &roleBlockedLister{}
	ts := newBlockedServer(t, lister)

	resp := ts.get(t, "/v0/beads/blocked?limit=5")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) || body["reason"] != string(ReasonUnknownParameter) || body["param"] != "limit" {
		t.Errorf("problem = %v, want invalid_argument/unknown_parameter naming limit", body)
	}
	if n := len(lister.blockedRequests()); n != 0 {
		t.Errorf("role calls = %d, want 0", n)
	}
}

// TestListBlockedRefusesARepeatedParent: parent is single-valued.
func TestListBlockedRefusesARepeatedParent(t *testing.T) {
	lister := &roleBlockedLister{}
	ts := newBlockedServer(t, lister)

	resp := ts.get(t, "/v0/beads/blocked?parent=a&parent=b")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if n := len(lister.blockedRequests()); n != 0 {
		t.Errorf("role calls = %d, want 0", n)
	}
}

// TestListBlockedProjectsTheRows: each row reaches the wire with its
// blockers and count, in the role's order; a nil row is dropped, a nil
// blocker list is [] and has_more is false.
func TestListBlockedProjectsTheRows(t *testing.T) {
	lister := &roleBlockedLister{result: issueops.BlockedResult{Items: []*types.BlockedIssue{
		{Issue: types.Issue{ID: "bd-2", Title: "two", Priority: 0}, BlockedByCount: 1, BlockedBy: []string{"bd-9"}},
		nil,
		{Issue: types.Issue{ID: "bd-1", Title: "one", Priority: 1}},
	}}}
	ts := newBlockedServer(t, lister)

	resp := ts.get(t, "/v0/beads/blocked")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	raw := readAll(t, resp)
	if !strings.Contains(raw, `"has_more":false`) {
		t.Errorf("body %s does not carry has_more false", raw)
	}
	if !strings.Contains(raw, `"blocked_by":[]`) {
		t.Errorf("body %s does not encode a nil blocker list as []", raw)
	}
	first := strings.Index(raw, `"id":"bd-2"`)
	second := strings.Index(raw, `"id":"bd-1"`)
	if first < 0 || second < 0 || first > second {
		t.Errorf("body %s does not carry bd-2 then bd-1", raw)
	}
	if !strings.Contains(raw, `"blocked_by":["bd-9"]`) || !strings.Contains(raw, `"blocked_by_count":1`) {
		t.Errorf("body %s lost bd-2's blockers", raw)
	}
}

// TestListBlockedEmptyIsAnEmptyArray: nothing blocked is "items":[].
func TestListBlockedEmptyIsAnEmptyArray(t *testing.T) {
	ts := newBlockedServer(t, &roleBlockedLister{})
	resp := ts.get(t, "/v0/beads/blocked")
	if raw := readAll(t, resp); !strings.Contains(raw, `"items":[]`) {
		t.Errorf("body %s, want items []", raw)
	}
}

// TestListBlockedIsAdvertised: the route row carries blocked.list.
func TestListBlockedIsAdvertised(t *testing.T) {
	found := false
	for _, c := range Capabilities() {
		if c == "blocked.list" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Capabilities() = %v, want blocked.list", Capabilities())
	}
}
