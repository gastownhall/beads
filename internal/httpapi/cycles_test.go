package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

const cyclesPath = "/v0/beads/dependencies/cycles"

// cyclesServer binds a roles-backed server whose cycle detector answers with
// report. The rest of the role set is inert: this operation touches none of it,
// but Listen requires a COMPLETE source, so rolesConfig fills them in.
func cyclesServer(t *testing.T, detector *roleCycleDetector) *testServer {
	t.Helper()
	return newTestServer(t, rolesConfig(Config{CycleDetector: detector}))
}

// TestCyclesCarriesTheRolesAnswerToTheWire is the whole point of the operation:
// the body is the role's report projected onto the envelope, with no shaping in
// between.
//
// It asserts the JSON rather than the Go value because the alias is what makes
// them the same thing — apigen.Cycle IS issueops.Cycle — so the only way for
// this to fail is a handler that reordered, renamed or dropped something.
func TestCyclesCarriesTheRolesAnswerToTheWire(t *testing.T) {
	ts := cyclesServer(t, &roleCycleDetector{report: issueops.CycleReport{
		Cycles: []issueops.Cycle{{
			Members: []issueops.CycleMember{
				{ID: "bd-a", Issue: &types.Issue{ID: "bd-a", Title: "A"}},
				{ID: "bd-b", Issue: &types.Issue{ID: "bd-b", Title: "B"}},
			},
		}},
	}})

	resp := ts.get(t, cyclesPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := decodeBody(t, resp)

	if hasMore, _ := body["has_more"].(bool); hasMore {
		t.Error("has_more = true; this operation takes no limit, so nothing can truncate the report")
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %#v, want one cycle", body["items"])
	}
	cycle, _ := items[0].(map[string]any)
	if partial, present := cycle["partial"]; !present || partial != false {
		t.Errorf("partial = %#v, want an explicit false: a consumer reads completeness from the answer, not from an absent key", cycle["partial"])
	}
	members, _ := cycle["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members = %#v, want two", cycle["members"])
	}
	for i, want := range []string{"bd-a", "bd-b"} {
		member, _ := members[i].(map[string]any)
		if member["id"] != want {
			t.Errorf("member %d id = %v, want %q: members run in edge order", i, member["id"], want)
		}
		if member["issue"] == nil {
			t.Errorf("member %d carries no issue, want the row the role hydrated", i)
		}
	}
}

// TestCyclesOmitsAnUndescribableMemberIssueAndMarksThePath is the wire half of
// the honest partial: the member keeps its place and its id, `issue` is ABSENT
// rather than null, and `partial` says the descriptions are incomplete.
func TestCyclesOmitsAnUndescribableMemberIssueAndMarksThePath(t *testing.T) {
	ts := cyclesServer(t, &roleCycleDetector{report: issueops.CycleReport{
		Cycles: []issueops.Cycle{{
			Partial: true,
			Members: []issueops.CycleMember{
				{ID: "bd-a", Issue: &types.Issue{ID: "bd-a", Title: "A"}},
				{ID: "bd-ghost"},
			},
		}},
	}})

	items, _ := decodeBody(t, ts.get(t, cyclesPath))["items"].([]any)
	cycle, _ := items[0].(map[string]any)
	if cycle["partial"] != true {
		t.Errorf("partial = %#v, want true", cycle["partial"])
	}
	members, _ := cycle["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members = %#v, want two: an undescribable member is carried, not dropped", cycle["members"])
	}
	ghost, _ := members[1].(map[string]any)
	if ghost["id"] != "bd-ghost" {
		t.Errorf("member 1 id = %v, want bd-ghost", ghost["id"])
	}
	if _, present := ghost["issue"]; present {
		t.Errorf("member 1 carries an `issue` key (%#v); the document says absent, never null", ghost["issue"])
	}
}

// TestCyclesAnswersAnEmptyArrayForACleanWorkspace pins the `never null` half of
// the envelope.
func TestCyclesAnswersAnEmptyArrayForACleanWorkspace(t *testing.T) {
	ts := cyclesServer(t, &roleCycleDetector{})

	var body struct {
		Items   *[]json.RawMessage `json:"items"`
		HasMore bool               `json:"has_more"`
	}
	resp := ts.get(t, cyclesPath)
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Items == nil {
		t.Fatal("items is null, want an empty array")
	}
	if len(*body.Items) != 0 {
		t.Fatalf("items = %v, want empty", *body.Items)
	}
}

// TestCyclesRejectsEveryQueryParameter pins the document's uniform
// unknown-parameter rule for an operation that takes none at all.
func TestCyclesRejectsEveryQueryParameter(t *testing.T) {
	ts := cyclesServer(t, &roleCycleDetector{})

	resp := ts.get(t, cyclesPath+"?limit=1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) || body["reason"] != string(ReasonUnknownParameter) {
		t.Errorf("code/reason = %v/%v, want invalid_argument/unknown_parameter", body["code"], body["reason"])
	}
	if body["param"] != "limit" {
		t.Errorf("param = %v, want the offending key", body["param"])
	}
}

// TestCyclesMapsARoleFailureThroughTheOneErrorMapping keeps the sweep on the
// same classification every other route uses, rather than inventing a status
// for a graph read.
func TestCyclesMapsARoleFailureThroughTheOneErrorMapping(t *testing.T) {
	ts := cyclesServer(t, &roleCycleDetector{err: errors.New("backend is unreachable")})

	resp := ts.get(t, cyclesPath)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if code := decodeBody(t, resp)["code"]; code != string(CodeInternal) {
		t.Errorf("code = %v, want internal", code)
	}
}

// TestCyclesReachesTheProviderAccessor covers the OTHER database source: the
// handler must reach the role through uow.CycleDetectorSource on the timed
// provider, not through a constructor and not through the roles fields, which
// are nil on this server.
func TestCyclesReachesTheProviderAccessor(t *testing.T) {
	ts := newTestServer(t, Config{})

	resp := ts.get(t, cyclesPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the provider-backed source", resp.StatusCode)
	}
	// A handler wired to the roles field, which this source leaves nil, would
	// panic into a recovered 500 rather than reaching the provider's accessor.
	if strings.Contains(ts.stderr.String(), "event=panic") {
		t.Fatalf("the handler panicked; it read a role this source does not set:\n%s", ts.stderr.String())
	}
	if items, _ := decodeBody(t, resp)["items"].([]any); len(items) != 0 {
		t.Errorf("items = %v, want the empty report this provider answers with", items)
	}
}
