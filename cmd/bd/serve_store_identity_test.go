//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestServeAnswersFromTheStoreTheRootCommandOpened pins the one property this
// whole arm exists for, and pins it as a property rather than as a shape.
//
// "Serve answers from the store bd already opened" and "serve opens a second
// store of its own" produce identical output on every other assertion available:
// the same reads, the same claims, the same handshake, the same clean shutdown.
// The difference is a leaked handle with no owner, doubled pools, and — on a
// backend that takes an exclusive workspace lock — a second open that fights the
// first. Replacing `store` in runServe with a store opened here, which compiles,
// used to pass everything.
//
// So WHICH store answered has to be readable off the wire. Every open through
// this registry hands back a store whose reader answers with one issue named
// after that open: the first is "store-1", a second would be "store-2". The
// response body says which one the server is on, and no amount of wiring can
// forge it.
func TestServeAnswersFromTheStoreTheRootCommandOpened(t *testing.T) {
	const name = "serve-store-identity"

	var opens atomic.Int64
	open := func(context.Context, string) (storage.DoltStorage, error) {
		return &serveIdentityStore{id: fmt.Sprintf("store-%d", opens.Add(1))}, nil
	}
	backends.Register(name, backends.Backend{
		Open:                open,
		OpenReadOnly:        open,
		WorkspaceIsBeadsDir: true,
	})
	t.Cleanup(func() { backends.Deregister(name) })

	dir := t.TempDir()
	initGitRepoAt(t, dir)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := (&configfile.Config{Backend: name}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	useStorageModeGlobals(t)
	restoreServeGlobals(t)
	t.Chdir(dir)
	t.Setenv("BEADS_DIR", beadsDir)
	// The workspace snapshot is resolved once per process and cached, so a test
	// that chdirs into its own workspace has to clear it going in and coming
	// out.
	beads.ResetCaches()
	t.Cleanup(beads.ResetCaches)

	// Stand in for PersistentPreRunE, which opens the workspace through exactly
	// this dispatch and leaves it in `store`. This is the store bd already
	// opened, and it is store-1.
	opened, err := openRegisteredStoreForTest(t, name, beadsDir)
	if err != nil {
		t.Fatalf("open the registered backend: %v", err)
	}
	store = opened

	serveAddr, serveAllowNonLoopback = "127.0.0.1:0", false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	setRootContext(ctx, cancel)

	lines, stopCapture := captureStdoutLines(t)
	done := make(chan error, 1)
	go func() {
		err := runServe()
		stopCapture()
		done <- err
	}()
	addr := waitForBoundAddress(t, lines, done)

	body := getBody(t, "http://"+addr+"/v0/beads/ready?limit=5")
	if !strings.Contains(body, opened.id) {
		t.Errorf("GET /v0/beads/ready answered from a store other than the one bd opened (%s):\n%s", opened.id, body)
	}
	// Named separately, because the two failures are different bugs: the body
	// above says the server is on the wrong store, and this says a store was
	// created that nobody will ever close.
	if n := opens.Load(); n != 1 {
		t.Errorf("the registered backend was opened %d times, want 1: bd serve created a store of its own", n)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("runServe returned %v, want a clean shutdown", err)
	}
}

// openRegisteredStoreForTest opens the workspace the way PersistentPreRunE
// does: through the registry, by name, with no knowledge of what is behind it.
func openRegisteredStoreForTest(t *testing.T, name, beadsDir string) (*serveIdentityStore, error) {
	t.Helper()
	backend, ok := backends.Lookup(name)
	if !ok {
		t.Fatalf("backend %q is not registered", name)
	}
	opened, err := backend.Open(t.Context(), beadsDir)
	if err != nil {
		return nil, err
	}
	identified, ok := opened.(*serveIdentityStore)
	if !ok {
		t.Fatalf("the registry returned %T, not the identified store this test reads by name", opened)
	}
	return identified, nil
}

// getBody issues a GET and returns the raw body, failing on anything but a 200.
func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200: %s", url, resp.StatusCode, payload)
	}
	return string(payload)
}

// serveIdentityStore is a store that says which store it is. Its reader answers
// every ready query with a single issue whose ID is the store's own, so the
// identity of the value the server was wired to is a substring of the response
// rather than something a test has to reach inside the server to inspect.
//
// The embedded DoltStorage is nil: nothing on this path calls anything else, and
// a nil-panic naming the method would be a truer failure than a stub that
// answered.
type serveIdentityStore struct {
	storage.DoltStorage
	id string
}

func (s *serveIdentityStore) IssueReader() (issueops.Reader, error) {
	return serveIdentityReader{id: s.id}, nil
}

func (s *serveIdentityStore) IssueClaimer() (issueops.Claimer, error) {
	return serveIdentityClaimer{}, nil
}

// Close is the one lifecycle method that is reached: the test harness closes a
// store it finds left behind. There is nothing to release.
func (s *serveIdentityStore) Close() error { return nil }

type serveIdentityReader struct{ id string }

func (r serveIdentityReader) Ready(context.Context, issueops.ReadyRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{
		Items: []*issueops.IssueWithCounts{{Issue: &types.Issue{
			ID:     r.id,
			Title:  "answered by " + r.id,
			Status: types.StatusOpen,
		}}},
	}, nil
}

func (serveIdentityReader) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, errors.ErrUnsupported
}

func (serveIdentityReader) Get(context.Context, issueops.GetRequest) (*issueops.IssueDetails, error) {
	return nil, errors.ErrUnsupported
}

type serveIdentityClaimer struct{}

func (serveIdentityClaimer) Claim(context.Context, issueops.ClaimRequest) (issueops.ClaimResult, error) {
	return issueops.ClaimResult{}, errors.ErrUnsupported
}
