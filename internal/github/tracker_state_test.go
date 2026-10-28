package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// capturedRequest records a single API call the tracker made.
type capturedRequest struct {
	method string
	path   string
	body   map[string]interface{}
}

// newRecordingTracker returns a Tracker whose client points at a test server
// that records every request and replies with the JSON bodies keyed by
// "METHOD path".
func newRecordingTracker(t *testing.T, responses map[string]string) (*Tracker, *[]capturedRequest) {
	t.Helper()
	var reqs []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		reqs = append(reqs, capturedRequest{method: r.Method, path: r.URL.Path, body: body})
		key := r.Method + " " + r.URL.Path
		resp, ok := responses[key]
		if !ok {
			t.Errorf("unexpected request: %s", key)
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)

	tr := &Tracker{
		client: NewClient("token", "o", "r").WithBaseURL(srv.URL),
		config: DefaultMappingConfig(),
	}
	return tr, &reqs
}

// findRequest returns the first captured request matching method and path.
func findRequest(reqs []capturedRequest, method, path string) *capturedRequest {
	for i := range reqs {
		if reqs[i].method == method && reqs[i].path == path {
			return &reqs[i]
		}
	}
	return nil
}

// TestCreateIssue_ClosedLocalIssueLandsClosed reproduces gastownhall/beads#5486:
// pushing a locally closed issue that has never been synced creates it on
// GitHub, but the create POST cannot carry a state, so the issue lands OPEN.
// The engine then records the push hash of the closed local issue, so later
// --push-only runs skip it as already in sync and the drift is never repaired.
// Creating a closed issue must leave the GitHub issue closed.
func TestCreateIssue_ClosedLocalIssueLandsClosed(t *testing.T) {
	tr, reqs := newRecordingTracker(t, map[string]string{
		"POST /repos/o/r/issues":     `{"id":7007,"number":42,"title":"Done already","state":"open","html_url":"https://github.com/o/r/issues/42"}`,
		"PATCH /repos/o/r/issues/42": `{"id":7007,"number":42,"title":"Done already","state":"closed","closed_at":"2026-10-01T00:00:00Z","html_url":"https://github.com/o/r/issues/42"}`,
	})
	issue := &types.Issue{
		ID: "bd-1", Title: "Done already", Description: "body",
		IssueType: types.TypeTask, Priority: 2,
		Status: types.StatusClosed, CloseReason: "done",
	}

	ti, err := tr.CreateIssue(context.Background(), issue)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	closeReq := findRequest(*reqs, http.MethodPatch, "/repos/o/r/issues/42")
	if closeReq == nil {
		t.Fatalf("expected follow-up PATCH to close issue, got requests: %+v", *reqs)
	}
	if closeReq.body["state"] != "closed" {
		t.Errorf("close PATCH state = %v, want %q", closeReq.body["state"], "closed")
	}
	if ti.State != "closed" {
		t.Errorf("returned TrackerIssue.State = %q, want %q", ti.State, "closed")
	}
	if len(ti.Warnings) != 0 {
		t.Errorf("expected no warnings on a clean create+close, got %v", ti.Warnings)
	}
	if !strings.Contains(ti.URL, "/issues/42") {
		t.Errorf("returned URL = %q, want the created issue's URL", ti.URL)
	}
}
