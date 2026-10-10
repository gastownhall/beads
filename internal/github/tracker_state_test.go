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

type capturedRequest struct {
	method string
	path   string
	body   map[string]interface{}
}

// newRecordingTracker serves canned JSON responses keyed by "METHOD /path" and
// records every request it receives.
func newRecordingTracker(t *testing.T, responses map[string]string) (*Tracker, *[]capturedRequest) {
	t.Helper()
	var reqs []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := capturedRequest{method: r.Method, path: r.URL.Path}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &req.body)
		}
		reqs = append(reqs, req)
		resp, ok := responses[r.Method+" "+r.URL.Path]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return &Tracker{client: newRateLimitTestClient(srv.URL), config: DefaultMappingConfig()}, &reqs
}

func findRequest(reqs []capturedRequest, method, path string) *capturedRequest {
	for i := range reqs {
		if reqs[i].method == method && reqs[i].path == path {
			return &reqs[i]
		}
	}
	return nil
}

// TestCreateIssue_ClosedCarriesState verifies a closed bead is created and then
// closed with a follow-up PATCH, since GitHub's create endpoint has no state.
// Without it the issue stayed open for good: the push hash recorded after the
// create already said "closed", so later pushes skipped it.
func TestCreateIssue_ClosedCarriesState(t *testing.T) {
	tr, reqs := newRecordingTracker(t, map[string]string{
		"POST /repos/owner/repo/issues":     `{"id":100,"number":42,"state":"open","html_url":"https://github.com/owner/repo/issues/42"}`,
		"PATCH /repos/owner/repo/issues/42": `{"id":100,"number":42,"state":"closed","html_url":"https://github.com/owner/repo/issues/42"}`,
	})

	ti, err := tr.CreateIssue(context.Background(), &types.Issue{
		Title:     "done",
		IssueType: types.TypeBug,
		Status:    types.StatusClosed,
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	closeReq := findRequest(*reqs, http.MethodPatch, "/repos/owner/repo/issues/42")
	if closeReq == nil {
		t.Fatalf("expected follow-up PATCH to close issue, got requests: %+v", *reqs)
	}
	if closeReq.body["state"] != "closed" {
		t.Errorf("close PATCH state = %v, want \"closed\"", closeReq.body["state"])
	}
	if ti.State != "closed" {
		t.Errorf("returned issue state = %q, want closed", ti.State)
	}
	if len(ti.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", ti.Warnings)
	}
}

// TestCreateIssue_NonClosedSkipsClose verifies a non-closed bead is created
// without a follow-up close. deferred is included explicitly: deferred work
// must stay open on GitHub.
func TestCreateIssue_NonClosedSkipsClose(t *testing.T) {
	for _, status := range []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusDeferred} {
		t.Run(string(status), func(t *testing.T) {
			tr, reqs := newRecordingTracker(t, map[string]string{
				"POST /repos/owner/repo/issues": `{"id":101,"number":43,"state":"open","html_url":"https://github.com/owner/repo/issues/43"}`,
			})

			if _, err := tr.CreateIssue(context.Background(), &types.Issue{
				Title:     "todo",
				IssueType: types.TypeTask,
				Status:    status,
			}); err != nil {
				t.Fatalf("CreateIssue: %v", err)
			}

			if r := findRequest(*reqs, http.MethodPatch, "/repos/owner/repo/issues/43"); r != nil {
				t.Errorf("%s issue should not trigger a close PATCH, got %+v", status, r)
			}
		})
	}
}

// TestCreateIssue_FailedCloseSurfacesWarning verifies that when the follow-up
// close fails, CreateIssue still returns the created issue (so the external_ref
// is stored and no duplicate is made) with a warning the engine surfaces.
func TestCreateIssue_FailedCloseSurfacesWarning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":100,"number":42,"state":"open","html_url":"https://github.com/owner/repo/issues/42"}`)
			return
		}
		// A 403 without rate-limit headers is not retried, so the test
		// does not wait on backoff.
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	tr := &Tracker{client: newRateLimitTestClient(srv.URL), config: DefaultMappingConfig()}

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
