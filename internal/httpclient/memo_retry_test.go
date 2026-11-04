package httpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// TestDispatchRetryOfABusyMemoizedReadAsksTheServer (mc-5dkzp): dispatch comes
// back to a busy read through wire.Client.Do again (dispatch.go's retry loop).
// With the per-command read memo on, the retry must reach the server rather
// than be answered with the 503 the first attempt got, and the 200 it gets is
// what the command's later identical reads are answered from.
func TestDispatchRetryOfABusyMemoizedReadAsksTheServer(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == wire.PathContext:
			ctx := v0Context("proj-busy")
			ctx.Capabilities = []string{"issues.get"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ctx)
		case strings.HasPrefix(r.URL.Path, wire.PathIssues+"/"):
			if gets.Add(1) == 1 {
				w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"status":503,"code":"busy","title":"busy"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"bd-1","title":"answered after a 503","revision":"7"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := wire.New(base, nil, wire.Options{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	client.EnableReadMemo()
	store := New(Target{BaseURL: base}, client, nil)

	issue, err := store.GetIssue(t.Context(), "bd-1")
	if err != nil {
		t.Fatalf("GetIssue after one 503: %v (getIssue requests = %d); the retry must reach the server", err, gets.Load())
	}
	if issue.Title != "answered after a 503" || gets.Load() != 2 {
		t.Fatalf("GetIssue after one 503 answered %q in %d getIssue requests; want the retry's answer in 2", issue.Title, gets.Load())
	}
	again, err := store.GetIssue(t.Context(), "bd-1")
	if err != nil || again.Title != issue.Title || gets.Load() != 2 {
		t.Fatalf("the repeat read answered (%v, %v) in %d getIssue requests; want the retry's 200 from the memo (2 requests)", again, err, gets.Load())
	}
}
