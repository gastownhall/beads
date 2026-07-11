package gitlab

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
// "METHOD path". store is left nil so parent-epic lookups are skipped.
func newRecordingTracker(t *testing.T, responses map[string]string) (*Tracker, *[]capturedRequest) {
	t.Helper()
	var reqs []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		// Strip the /api/v4 prefix so keys read like "/projects/123/issues".
		path := strings.TrimPrefix(r.URL.Path, "/api/v4")
		reqs = append(reqs, capturedRequest{method: r.Method, path: path, body: body})
		key := r.Method + " " + path
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
		client: NewClient("token", srv.URL, "123"),
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

// TestCreateIssue_ClosedCarriesState verifies a closed bead is created and then
// closed with a follow-up PUT carrying state_event=close.
func TestCreateIssue_ClosedCarriesState(t *testing.T) {
	tr, reqs := newRecordingTracker(t, map[string]string{
		"POST /projects/123/issues":   `{"id":100,"iid":42,"state":"opened","web_url":"https://gl/x/-/issues/42"}`,
		"PUT /projects/123/issues/42": `{"id":100,"iid":42,"state":"closed","web_url":"https://gl/x/-/issues/42"}`,
	})

	ti, err := tr.CreateIssue(context.Background(), &types.Issue{
		Title:     "done",
		IssueType: types.TypeBug,
		Status:    types.StatusClosed,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	closeReq := findRequest(*reqs, http.MethodPut, "/projects/123/issues/42")
	if closeReq == nil {
		t.Fatalf("expected follow-up PUT to close issue, got requests: %+v", *reqs)
	}
	if closeReq.body["state_event"] != "close" {
		t.Errorf("close PUT state_event = %v, want \"close\"", closeReq.body["state_event"])
	}
	if raw, ok := ti.Raw.(*Issue); !ok || raw.State != "closed" {
		t.Errorf("returned issue state = %v, want closed", ti.Raw)
	}
}

// TestCreateIssue_NonClosedSkipsClose verifies a non-closed bead is created
// without a follow-up close request. deferred is included explicitly: deferred
// work must stay open in GitLab.
func TestCreateIssue_NonClosedSkipsClose(t *testing.T) {
	for _, status := range []types.Status{types.StatusOpen, types.StatusDeferred} {
		t.Run(string(status), func(t *testing.T) {
			tr, reqs := newRecordingTracker(t, map[string]string{
				"POST /projects/123/issues": `{"id":101,"iid":43,"state":"opened","web_url":"https://gl/x/-/issues/43"}`,
			})

			if _, err := tr.CreateIssue(context.Background(), &types.Issue{
				Title:     "todo",
				IssueType: types.TypeTask,
				Status:    status,
			}); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}

			if r := findRequest(*reqs, http.MethodPut, "/projects/123/issues/43"); r != nil {
				t.Errorf("%s issue should not trigger a close PUT, got %+v", status, r)
			}
		})
	}
}

// TestCreateIssue_FailedCloseSurfacesWarning verifies that when the follow-up
// close fails, CreateIssue still returns the created issue (so the external_ref
// is stored and no duplicate is made) but records a warning on the returned
// TrackerIssue so the sync engine can surface it instead of swallowing it.
func TestCreateIssue_FailedCloseSurfacesWarning(t *testing.T) {
	var reqs []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v4")
		reqs = append(reqs, capturedRequest{method: r.Method, path: path})
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":100,"iid":42,"state":"opened","web_url":"https://gl/x/-/issues/42"}`)
			return
		}
		// The follow-up close (PUT) fails with a non-retryable status (403) so
		// the test doesn't wait on the client's 5xx/429 backoff retries.
		http.Error(w, `{"message":"403 Forbidden"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	tr := &Tracker{client: NewClient("token", srv.URL, "123"), config: DefaultMappingConfig()}

	ti, err := tr.CreateIssue(context.Background(), &types.Issue{
		Title: "done", IssueType: types.TypeBug, Status: types.StatusClosed,
	})
	if err != nil {
		t.Fatalf("CreateIssue should not error on a failed best-effort close: %v", err)
	}
	if ti == nil || ti.Identifier != "42" {
		t.Fatalf("expected the created issue to be returned so external_ref is stored, got %+v", ti)
	}
	if len(ti.Warnings) == 0 {
		t.Fatal("expected a warning on the returned TrackerIssue when the close fails")
	}
	if !strings.Contains(ti.Warnings[0], "failed to close") {
		t.Errorf("warning = %q, want it to mention the failed close", ti.Warnings[0])
	}
}

// TestUpdateIssue_StateCarries verifies the update path flips GitLab state to
// match the bead: a closed bead closes, a reopened bead reopens.
func TestUpdateIssue_StateCarries(t *testing.T) {
	tests := []struct {
		name   string
		status types.Status
		want   string
	}{
		{"close", types.StatusClosed, "close"},
		{"reopen", types.StatusOpen, "reopen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, reqs := newRecordingTracker(t, map[string]string{
				"PUT /projects/123/issues/42": `{"id":100,"iid":42,"state":"opened"}`,
			})

			if _, err := tr.UpdateIssue(context.Background(), "42", &types.Issue{
				Title:     "t",
				IssueType: types.TypeTask,
				Status:    tt.status,
			}); err != nil {
				t.Fatalf("UpdateIssue: %v", err)
			}

			req := findRequest(*reqs, http.MethodPut, "/projects/123/issues/42")
			if req == nil {
				t.Fatalf("expected a PUT, got requests: %+v", *reqs)
			}
			if req.body["state_event"] != tt.want {
				t.Errorf("update state_event = %v, want %q", req.body["state_event"], tt.want)
			}
		})
	}
}

// TestCreateMilestone_ClosedCarriesState verifies a closed epic is created as a
// milestone and then closed with state_event=close.
func TestCreateMilestone_ClosedCarriesState(t *testing.T) {
	tr, reqs := newRecordingTracker(t, map[string]string{
		"POST /projects/123/milestones":  `{"id":7,"iid":3,"state":"active","title":"Epic"}`,
		"PUT /projects/123/milestones/7": `{"id":7,"iid":3,"state":"closed","title":"Epic"}`,
	})

	if _, err := tr.CreateIssue(context.Background(), &types.Issue{
		Title:     "Epic",
		IssueType: types.TypeEpic,
		Status:    types.StatusClosed,
	}); err != nil {
		t.Fatalf("CreateIssue(epic): %v", err)
	}

	closeReq := findRequest(*reqs, http.MethodPut, "/projects/123/milestones/7")
	if closeReq == nil {
		t.Fatalf("expected follow-up PUT to close milestone, got requests: %+v", *reqs)
	}
	if closeReq.body["state_event"] != "close" {
		t.Errorf("milestone close state_event = %v, want \"close\"", closeReq.body["state_event"])
	}
}
