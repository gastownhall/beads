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

// TestCreateIssue_NonClosedSkipsClose verifies a non-closed bead is created
// without a follow-up close request: only the create POST may hit the wire.
// Statuses other than closed must stay open on GitHub.
func TestCreateIssue_NonClosedSkipsClose(t *testing.T) {
	for _, status := range []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusBlocked, types.StatusDeferred} {
		t.Run(string(status), func(t *testing.T) {
			tr, reqs := newRecordingTracker(t, map[string]string{
				"POST /repos/o/r/issues": `{"id":7008,"number":43,"title":"todo","state":"open","html_url":"https://github.com/o/r/issues/43"}`,
			})

			ti, err := tr.CreateIssue(context.Background(), &types.Issue{
				ID: "bd-2", Title: "todo", Description: "b",
				IssueType: types.TypeTask, Priority: 2,
				Status: status,
			})
			if err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}

			for _, r := range *reqs {
				if r.method == http.MethodPatch {
					t.Errorf("%s issue triggered a follow-up PATCH (%s %s), want create only", status, r.method, r.path)
				}
			}
			if ti.State != "open" {
				t.Errorf("%s issue: returned TrackerIssue.State = %q, want %q", status, ti.State, "open")
			}
		})
	}
}

// TestCreateIssue_FailedCloseSurfacesWarning verifies that when the follow-up
// close fails, CreateIssue still returns the created issue (so the external_ref
// is stored and the next push updates it instead of creating a duplicate) with
// a nil error, and records a warning on the returned TrackerIssue so the sync
// engine surfaces the degraded push instead of swallowing it.
func TestCreateIssue_FailedCloseSurfacesWarning(t *testing.T) {
	var reqs []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, capturedRequest{method: r.Method, path: r.URL.Path})
		if r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":7009,"number":44,"title":"done","state":"open","html_url":"https://github.com/o/r/issues/44"}`)
			return
		}
		// The follow-up close (PATCH) fails with a non-retryable status (422)
		// so the test doesn't wait on the client's 5xx/429 backoff retries.
		http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
	}))
	t.Cleanup(srv.Close)
	tr := &Tracker{
		client: NewClient("token", "o", "r").WithBaseURL(srv.URL),
		config: DefaultMappingConfig(),
	}

	ti, err := tr.CreateIssue(context.Background(), &types.Issue{
		ID: "bd-3", Title: "done", Description: "b",
		IssueType: types.TypeTask, Priority: 2,
		Status: types.StatusClosed,
	})
	if err != nil {
		t.Fatalf("CreateIssue should not error on a failed best-effort close: %v", err)
	}
	if ti == nil || ti.Identifier != "44" {
		t.Fatalf("expected the created issue to be returned so external_ref is stored, got %+v", ti)
	}
	if !strings.Contains(ti.URL, "/issues/44") {
		t.Errorf("returned URL = %q, want the created issue's URL", ti.URL)
	}
	if ti.State != "open" {
		t.Errorf("returned TrackerIssue.State = %q, want %q (the close failed)", ti.State, "open")
	}
	if len(ti.Warnings) == 0 {
		t.Fatal("expected a warning on the returned TrackerIssue when the close fails")
	}
	if !strings.Contains(ti.Warnings[0], "44") || !strings.Contains(ti.Warnings[0], "failed to close") {
		t.Errorf("warning = %q, want it to name the issue number and the failed close", ti.Warnings[0])
	}
	if findRequest(reqs, http.MethodPost, "/repos/o/r/issues") == nil {
		t.Fatalf("expected the create POST, got requests: %+v", reqs)
	}
	if findRequest(reqs, http.MethodPatch, "/repos/o/r/issues/44") == nil {
		t.Fatalf("expected the follow-up close PATCH, got requests: %+v", reqs)
	}
}
