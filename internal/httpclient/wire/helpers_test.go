// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/wire/helpers_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package wire

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// The whole suite is httptest servers with canned bytes: no Dolt, no bd serve,
// no subprocess. That is deliberate, and it is what lets this layer own the
// entire branch matrix — every problem code, both retry outcomes, both
// polarities of every presence-discriminated extension member — at PR cadence
// (engdocs/TESTING.md §2: lower layers own the branch matrix before anything
// defers to a slower lane).

// recorder captures what the client actually put on the wire.
type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	method   string
	path     string
	rawPath  string
	rawQuery string
	header   http.Header
	body     []byte
}

func (r *recorder) add(req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, recorded{
		method:   req.Method,
		path:     req.URL.Path,
		rawPath:  req.URL.EscapedPath(),
		rawQuery: req.URL.RawQuery,
		header:   req.Header.Clone(),
		body:     body,
	})
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func (r *recorder) at(t *testing.T, i int) recorded {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.reqs) {
		t.Fatalf("wanted request %d, only %d were made", i, len(r.reqs))
	}
	return r.reqs[i]
}

// newTestClient stands up a server around h and returns a client aimed at it.
func newTestClient(t *testing.T, opts Options, creds CredentialProvider, h http.HandlerFunc) (*Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = srv.Client()
	}
	c, err := New(base, creds, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, rec
}

// problemJSON is the shape the server writes for every non-2xx.
func problemJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// staticToken is the smallest CredentialProvider that exercises the interface:
// it authorizes with whatever token it currently holds, and Refresh advances to
// the next one exactly as a rotated token file would.
type staticToken struct {
	tokens   []string
	refreshN int
	retry    bool
	err      error
}

func (s *staticToken) Authorize(_ context.Context, req *http.Request) error {
	if len(s.tokens) == 0 {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+s.tokens[0])
	return nil
}

func (s *staticToken) Refresh(context.Context) (bool, error) {
	s.refreshN++
	if s.err != nil {
		return false, s.err
	}
	if s.retry && len(s.tokens) > 1 {
		s.tokens = s.tokens[1:]
	}
	return s.retry, nil
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return c
}
