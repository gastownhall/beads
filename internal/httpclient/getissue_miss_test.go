package httpclient

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
)

// TestGetIssueMissIsErrNotFound is the unit-tier pin of the raw GetIssue miss,
// over the real wire client against a stub server (no embedded engine): the
// server's 404 not_found, and the empty id that never dials, must both come
// back as storage.ErrNotFound with a nil issue, spelled as the local stores
// spell it. A (nil, nil) miss reads as a hit to every caller written against
// the backend contract: `bd create --graph` refused every explicit-id plan.
func TestGetIssueMissIsErrNotFound(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == wire.PathContext:
			ctx := v0Context("proj-miss")
			ctx.Capabilities = []string{"issues.get"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ctx)
		case strings.HasPrefix(r.URL.Path, wire.PathIssues+"/"):
			gets.Add(1)
			w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"status":404,"code":"not_found","title":"not found"}`)
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
	store := New(Target{BaseURL: base}, client, nil)

	issue, err := store.GetIssue(t.Context(), "bd-absent")
	if issue != nil || !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetIssue(absent) = (%v, %v), want (nil, storage.ErrNotFound)", issue, err)
	}
	if want := "not found: issue bd-absent"; err.Error() != want {
		t.Errorf("GetIssue(absent) error = %q, want the local stores' %q", err, want)
	}
	if got := gets.Load(); got != 1 {
		t.Errorf("getIssue requests = %d, want 1", got)
	}

	issue, err = store.GetIssue(t.Context(), "")
	if issue != nil || !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetIssue(\"\") = (%v, %v), want (nil, storage.ErrNotFound)", issue, err)
	}
	if got := gets.Load(); got != 1 {
		t.Errorf("the empty id dialed: getIssue requests = %d, want still 1", got)
	}
}
