package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
)

const hydrationTestIssue = `{"id":1,"number":9,"title":"t","body":"b","state":"open","comments":1}`
const hydrationTestThread = `[{"id":9001,"user":{"login":"alice"},"body":"hello","created_at":"2026-01-02T03:04:05Z"}]`

func hydrationTestServer(requests *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(requests, 1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/9/comments"):
			_, _ = w.Write([]byte(hydrationTestThread))
		case strings.HasSuffix(r.URL.Path, "/issues/9"):
			_, _ = w.Write([]byte(hydrationTestIssue))
		case strings.HasSuffix(r.URL.Path, "/issues"):
			_, _ = w.Write([]byte(`[` + hydrationTestIssue + `]`))
		default:
			t := r.URL.Path
			http.Error(w, "unexpected path "+t, http.StatusNotFound)
		}
	}))
}

func hydrationTestTracker(srv *httptest.Server) *Tracker {
	return &Tracker{client: newRateLimitTestClient(srv.URL), config: DefaultMappingConfig()}
}

func TestFetchIssues_HydratesOnlyWhenIncluded(t *testing.T) {
	ctx := context.Background()

	t.Run("without flag issues no thread fetch", func(t *testing.T) {
		var requests int32
		srv := hydrationTestServer(&requests)
		defer srv.Close()

		tis, err := hydrationTestTracker(srv).FetchIssues(ctx, tracker.FetchOptions{State: "open"})
		if err != nil {
			t.Fatalf("FetchIssues() error: %v", err)
		}
		if len(tis) != 1 {
			t.Fatalf("issues = %d, want 1", len(tis))
		}
		if got := atomic.LoadInt32(&requests); got != 1 {
			t.Errorf("HTTP requests = %d, want 1 (list only, no thread fetch)", got)
		}
	})

	t.Run("with flag hydrates the thread", func(t *testing.T) {
		var requests int32
		srv := hydrationTestServer(&requests)
		defer srv.Close()

		tr := hydrationTestTracker(srv)
		tis, err := tr.FetchIssues(ctx, tracker.FetchOptions{State: "open", IncludeComments: true})
		if err != nil {
			t.Fatalf("FetchIssues() error: %v", err)
		}
		if got := atomic.LoadInt32(&requests); got != 2 {
			t.Fatalf("HTTP requests = %d, want 2 (list + thread)", got)
		}
		conv := tr.FieldMapper().IssueToBeads(&tis[0])
		if conv == nil || len(conv.Issue.Comments) != 1 || conv.Issue.Comments[0].Text != "hello" {
			t.Errorf("mapped comments = %+v, want the hydrated thread", conv.Issue)
		}
	})
}

func TestFetchIssue_SkipsThreadFetch(t *testing.T) {
	ctx := context.Background()
	var requests int32
	srv := hydrationTestServer(&requests)
	defer srv.Close()

	ti, err := hydrationTestTracker(srv).FetchIssue(ctx, "9")
	if err != nil {
		t.Fatalf("FetchIssue() error: %v", err)
	}
	if ti == nil {
		t.Fatal("FetchIssue() returned nil issue")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("HTTP requests = %d, want 1 (push and dry-run paths must not pay for threads)", got)
	}
}

func TestFetchIssueWithComments_Hydrates(t *testing.T) {
	ctx := context.Background()
	var requests int32
	srv := hydrationTestServer(&requests)
	defer srv.Close()

	tr := hydrationTestTracker(srv)
	ti, err := tr.FetchIssueWithComments(ctx, "9")
	if err != nil {
		t.Fatalf("FetchIssueWithComments() error: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("HTTP requests = %d, want 2 (issue + thread)", got)
	}
	conv := tr.FieldMapper().IssueToBeads(ti)
	if conv == nil || len(conv.Issue.Comments) != 1 || conv.Issue.Comments[0].Author != "alice" {
		t.Errorf("mapped comments = %+v, want the hydrated thread", conv.Issue)
	}
}
