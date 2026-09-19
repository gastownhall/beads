package httpapi

import (
	"net/http"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestStatsReportsTheSkippedScanFromTheAnswer pins the one derivation this
// handler performs: `blocked_count_skipped` comes from the summary's nil
// pointers, never from the request that asked for the fast path.
//
// The role here TAKES the hint, which is the half the roles-path route test
// cannot show. Both halves matter because the two shipped backends differ.
func TestStatsReportsTheSkippedScanFromTheAnswer(t *testing.T) {
	reporter := &roleStats{summary: types.Statistics{TotalIssues: 3, OpenIssues: 2}}
	ts := newTestServer(t, rolesConfig(Config{Stats: reporter}))

	resp := ts.get(t, "/v0/beads/stats?skip_blocked=true")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["blocked_count_skipped"] != true {
		t.Errorf("blocked_count_skipped = %v, want true beside a null blocked_issues", body["blocked_count_skipped"])
	}
	summary, _ := body["summary"].(map[string]any)
	if got, ok := summary["blocked_issues"]; !ok || got != nil {
		t.Errorf("blocked_issues = %v (present=%t), want an explicit null", got, ok)
	}
	if got, ok := summary["ready_issues"]; !ok || got != nil {
		t.Errorf("ready_issues = %v (present=%t), want an explicit null — the two are nil together", got, ok)
	}
}

// TestStatsRefusesAnEmptyAssignee pins the refusal the document states: the
// workspace-wide question is asked by OMITTING the parameter, so an empty one
// is a 400 rather than a summary of the rows nobody is assigned.
//
// It also pins that the refusal happens BEFORE the role is asked for anything.
// A handler that passed the empty string down would get the role's
// ErrValidation and answer 500, which tells a client nothing it can act on.
func TestStatsRefusesAnEmptyAssignee(t *testing.T) {
	for _, raw := range []string{"", "%20", "%20%20", "%09"} {
		t.Run("assignee="+raw, func(t *testing.T) {
			statsAssertEmptyAssigneeRefused(t, raw)
		})
	}
}

// statsAssertEmptyAssigneeRefused drives one spelling of "supplied but empty".
//
// The whitespace spellings are the ones that regressed: the guard compared the
// RAW string, so `?assignee=%20` reached the role, came back ErrValidation,
// and left as a 500 with an operator alert — for a trailing space in a script.
func statsAssertEmptyAssigneeRefused(t *testing.T, rawAssignee string) {
	t.Helper()
	reporter := &roleStats{}
	ts := newTestServer(t, rolesConfig(Config{Stats: reporter}))

	resp := ts.get(t, "/v0/beads/stats?assignee="+rawAssignee)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeInvalidArgument) {
		t.Errorf("code = %v, want %q", body["code"], CodeInvalidArgument)
	}
	if body["param"] != "assignee" {
		t.Errorf("param = %v, want the parameter a client can fix", body["param"])
	}
	if len(reporter.statsRequests()) != 0 || len(reporter.assigneeRequests()) != 0 {
		t.Errorf("the role was asked %d workspace and %d assignee questions, want none: the refusal is before the database slot",
			len(reporter.statsRequests()), len(reporter.assigneeRequests()))
	}
}

// TestStatsRefusesAMalformedSkipBlocked pins the other 400 this operation can
// produce on its own.
func TestStatsRefusesAMalformedSkipBlocked(t *testing.T) {
	ts := newTestServer(t, rolesConfig(Config{Stats: &roleStats{}}))

	resp := ts.get(t, "/v0/beads/stats?skip_blocked=maybe")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "skip_blocked" {
		t.Errorf("param = %v, want skip_blocked", body["param"])
	}
}

// TestStatsRefusesAnUnknownParameter pins that this operation is inside the
// document's unknown-parameter rule rather than outside it. Silently ignoring a
// parameter is how a client one version ahead of the server acts on an answer
// it believes was narrowed.
func TestStatsRefusesAnUnknownParameter(t *testing.T) {
	ts := newTestServer(t, rolesConfig(Config{Stats: &roleStats{}}))

	resp := ts.get(t, "/v0/beads/stats?status=open")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
	}
	if body := decodeBody(t, resp); body["param"] != "status" {
		t.Errorf("param = %v, want the parameter this server does not know", body["param"])
	}
}
