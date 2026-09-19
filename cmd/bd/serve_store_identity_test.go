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
	"github.com/steveyegge/beads/memoryops"
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
		return &serveIdentityDoltStore{
			serveIdentityStore: &serveIdentityStore{id: fmt.Sprintf("store-%d", opens.Add(1))},
		}, nil
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
func openRegisteredStoreForTest(t *testing.T, name, beadsDir string) (*serveIdentityDoltStore, error) {
	t.Helper()
	backend, ok := backends.Lookup(name)
	if !ok {
		t.Fatalf("backend %q is not registered", name)
	}
	opened, err := backend.Open(t.Context(), beadsDir)
	if err != nil {
		return nil, err
	}
	identified, ok := opened.(*serveIdentityDoltStore)
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
// IT EMBEDS NOTHING, and the assertion below is why. This stub used to embed a
// nil storage.DoltStorage, so a role it had not declared arrived as a promoted
// method on a nil interface — GraphCounter did, and it surfaced as a segfault on
// a full-package CI shard rather than as a compile error, because no -run
// pattern anyone reached for names this test. Declaring the whole of
// serveRoleSource makes the next role a build failure here.
type serveIdentityStore struct {
	id string
}

var _ serveRoleSource = (*serveIdentityStore)(nil)

// serveIdentityDoltStore is what the registry hands back, because
// backends.Backend.Open returns a whole storage.DoltStorage. The roles above sit
// one embed shallower than the nil store in serveStubRest and so shadow it:
// nothing on this path calls anything else, and a nil panic naming the method
// would be a truer failure than a stub that answered.
type serveIdentityDoltStore struct {
	*serveIdentityStore
	serveStubRest
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

// The rest of the roles httpapi.Config requires. This test drives one route,
// but Listen refuses a partial role set, so the server does not bind at all
// without them.
//
// They hand back serveIdentityRole, which satisfies each interface by
// EMBEDDING it rather than implementing it: non-nil, so the set is complete,
// while an actual call panics naming the method it reached.
func (*serveIdentityStore) BatchCloser() (issueops.BatchCloser, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) ReadyClaimer() (issueops.ReadyClaimer, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) Releaser() (issueops.Releaser, error) { return serveIdentityRole{}, nil }
func (*serveIdentityStore) IssueLifecycle() (issueops.Lifecycle, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) StatsReporter() (issueops.StatsReporter, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) CycleDetector() (issueops.CycleDetector, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) EdgeReader() (issueops.EdgeReader, error) { return serveIdentityRole{}, nil }

// GraphCounter is declared for the reason every accessor here is, and it is the
// one this stub was CAUGHT missing rather than written with: the edge-count
// role is a read, so its hook decorator recurses, and the recursion lands on a
// type whose embedded storage.DoltStorage is nil. That is a promoted method on
// a nil interface — a segfault inside `bd serve`'s role extraction, not a
// compile error — and it surfaced only on the full-suite macOS shard, because
// no test name here matches the ones a role slice thinks to run.
func (*serveIdentityStore) GraphCounter() (issueops.GraphCounter, error) {
	return serveIdentityRole{}, nil
}

// IssueRelations is the first role added to serveIssueRoles since this stub
// stopped embedding a nil store, so the comment above it is now a record of the
// old regime rather than a warning about this one: GraphCounter had to be found
// by a CI shard, and this had to be declared before the package would build,
// with `missing method IssueRelations` naming it.
//
// It is declared HERE, at depth 1, and that placement is the load-bearing half.
// serveIdentityDoltStore embeds this type shallower than serveStubRest's nil
// store, so a role declared here shadows the promotion; one declared on the
// wrapper instead would satisfy the compiler and leave the assertion below
// unable to see it.
func (*serveIdentityStore) IssueRelations() (issueops.Relations, error) {
	return serveIdentityRole{}, nil
}

// Commenter is declared at depth 1 for IssueRelations' reason, and it is the
// second role to arrive as a build error here rather than as a runtime one.
func (*serveIdentityStore) Commenter() (issueops.Commenter, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) BlockingAnnotator() (issueops.BlockingAnnotator, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) TreeWalker() (issueops.TreeWalker, error) { return serveIdentityRole{}, nil }
func (*serveIdentityStore) ReadyCounter() (issueops.ReadyCounter, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) Counter() (issueops.Counter, error) { return serveIdentityRole{}, nil }
func (*serveIdentityStore) Querier() (issueops.Querier, error) { return serveIdentityRole{}, nil }
func (*serveIdentityStore) Sweeper() (issueops.Sweeper, error) { return serveIdentityRole{}, nil }
func (*serveIdentityStore) Deleter() (issueops.Deleter, error) { return serveIdentityRole{}, nil }
func (*serveIdentityStore) BatchCreator() (issueops.BatchCreator, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) DependencyEditor() (issueops.DependencyEditor, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) BatchApplier() (issueops.BatchApplier, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) Memories() (memoryops.Memories, error) {
	return serveIdentityRole{}, nil
}
func (*serveIdentityStore) MetadataCAS() (issueops.MetadataCAS, error) {
	return serveIdentityRole{}, nil
}

// serveIdentityRole satisfies every one of them at once, which it can because no
// two of those interfaces declare a method of the same name. If a future role
// collides, split this into one type per role — the embedded method would stop
// being promoted and the build would say so.
type serveIdentityRole struct {
	issueops.Lifecycle
	issueops.Releaser
	issueops.ReadyClaimer
	issueops.BatchCloser
	issueops.MetadataCAS
	issueops.WorkspaceConfig
	issueops.StatsReporter
	issueops.CycleDetector
	issueops.EdgeReader
	issueops.GraphCounter
	issueops.Relations
	issueops.Commenter
	issueops.BlockingAnnotator
	issueops.TreeWalker
	issueops.ReadyCounter
	issueops.Counter
	issueops.Querier
	issueops.Sweeper
	issueops.Deleter
	issueops.BatchCreator
	issueops.DependencyEditor
	issueops.BatchApplier
	memoryops.Memories
}

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
