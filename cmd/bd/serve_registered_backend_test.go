//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// The registered-backend serve path end to end, in-process.
//
// In-process because it has to be: registration is init-time Go wiring and OSS
// registers no alternate backend, so a spawned `bd` — the shape every other
// serve integration test takes — has nothing to register and could not
// reproduce this case at all. bd bootstrap's registered-backend test reaches
// the same conclusion for the same reason.
//
// The store behind the registered NAME is a real embedded Dolt store. Embedded
// Dolt is refused as a WORKSPACE (serveDatabaseSource), and that refusal is
// about its commit protocol in production, not about wiring: behind a
// registered name it is a real store with real SQL, real claim arbitration and
// the real decorator chain, which is exactly the double this path needs.

const serveRegisteredDatabase = "beads"

func TestServeAnswersFromARegisteredBackendStore(t *testing.T) {
	const name = "serve-registered-e2e"

	dir := t.TempDir()
	initGitRepoAt(t, dir)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	seedID := seedRegisteredBackendWorkspace(t, beadsDir)

	// A hook the workspace would fire on a CLI claim. `bd serve` documents that
	// it does not, and this marker is the only evidence that survives the
	// subprocess either way.
	hookMarker := filepath.Join(dir, "on_update.ran")
	plantOnUpdateHook(t, beadsDir, hookMarker)

	// Every store this process opens for the registered name is counted, and
	// the count is an assertion at the end: bd serve creates NOTHING on this
	// arm, so the root command's one open has to be the only one in the
	// process. A serve that opened a handle of its own would leak it — nothing
	// closes it — and double the backend's pools against a workspace some
	// backends hold an exclusive lock on. Counting here catches it wherever it
	// is spelled, because every store factory in cmd/bd dispatches through this
	// same registry (newDoltStoreFromConfig, newReadOnlyStoreFromConfig).
	var opens atomic.Int64
	backends.Register(name, backends.Backend{
		Open: func(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
			opens.Add(1)
			return embeddeddolt.Open(ctx, beadsDir, serveRegisteredDatabase, "main")
		},
		OpenReadOnly: func(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
			opens.Add(1)
			return embeddeddolt.OpenReadOnly(ctx, beadsDir, serveRegisteredDatabase, "main")
		},
		WorkspaceIsBeadsDir: true,
	})
	t.Cleanup(func() { backends.Deregister(name) })
	if err := (&configfile.Config{Backend: name, DoltDatabase: serveRegisteredDatabase}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	addr, done := startServeInProcess(t, dir, beadsDir)

	base := "http://" + addr
	t.Run("the handshake answers", func(t *testing.T) {
		body := getJSON(t, base+"/v0/beads/context")
		if body["schema_version"] == nil {
			t.Errorf("GET /v0/beads/context returned no schema_version: %v", body)
		}
	})

	// GET /v0/beads/context is the one endpoint automation is told to trust for
	// this server's identity, and it was reporting backend="dolt",
	// dolt_mode="embedded", database="beads" here — a full, confident
	// description of the exact topology bd serve REFUSES to serve, while the
	// startup line beside it named the registered backend correctly.
	//
	// database is empty even though this workspace's metadata.json carries
	// dolt_database and the store behind the registered name really does open
	// it: bd does not implement this backend, its Open reads whatever it wants
	// out of the workspace, and a value bd cannot verify is the same lie made
	// quieter. Empty is what bd can assert. Both fields stay required strings —
	// the wire shape is unchanged.
	t.Run("the handshake names the registered backend", func(t *testing.T) {
		body := getJSON(t, base+"/v0/beads/context")
		if body["backend"] != name {
			t.Errorf("backend = %v, want %q: the handshake names a backend this server is not on", body["backend"], name)
		}
		if body["dolt_mode"] != "" {
			t.Errorf("dolt_mode = %v, want empty: a registered backend has no Dolt mode", body["dolt_mode"])
		}
		if body["database"] != "" {
			t.Errorf("database = %v, want empty: bd cannot know which database a registered backend opened", body["database"])
		}
	})

	t.Run("reads come from the registered store", func(t *testing.T) {
		body := getJSON(t, base+"/v0/beads/ready?limit=5")
		if !strings.Contains(string(mustMarshal(t, body)), seedID) {
			t.Errorf("GET /v0/beads/ready does not carry the seeded issue %q: %v", seedID, body)
		}
	})

	t.Run("a claim lands in the registered store", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, base+"/v0/beads/issues/"+seedID+":claim",
			strings.NewReader(`{"actor":"serve-e2e"}`))
		if err != nil {
			t.Fatalf("build claim request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			payload, _ := io.ReadAll(resp.Body)
			t.Fatalf("claim status = %d, want 200: %s", resp.StatusCode, payload)
		}
	})

	// Stop the server the way an operator does. The root command's signal
	// context is what serve rides, so canceling it here exercises the real
	// shutdown path including PersistentPostRunE.
	if rootCancel == nil {
		t.Fatal("the root command published no cancel function; nothing here stopped the server")
	}
	rootCancel()
	if err := <-done; err != nil {
		t.Fatalf("bd serve returned %v, want a clean shutdown", err)
	}

	// One open for the whole process, and it is the root command's. Serving
	// from the store bd already opened is the entire point of this arm; a
	// second handle here would be an unclosed leak with no owner.
	if n := opens.Load(); n != 1 {
		t.Errorf("the registered backend was opened %d times, want 1: bd serve created a store of its own", n)
	}

	// The root command owns the store's whole lifecycle: PersistentPostRunE
	// closed it after the server drained. A store still open would still hold
	// the embedded workspace's exclusive lock, so this reopen is the assertion.
	if store != nil {
		t.Error("PersistentPostRunE left the registered backend's store open")
	}
	reopened, err := embeddeddolt.Open(t.Context(), beadsDir, serveRegisteredDatabase, "main")
	if err != nil {
		t.Fatalf("reopen the workspace after shutdown: %v", err)
	}
	defer reopened.Close()

	claimed, err := reopened.GetIssue(t.Context(), seedID)
	if err != nil {
		t.Fatalf("read the claimed issue back: %v", err)
	}
	if claimed.Status != types.StatusInProgress {
		t.Errorf("status = %q, want %q: the HTTP claim did not land in the registered store", claimed.Status, types.StatusInProgress)
	}
	if claimed.Assignee != "serve-e2e" {
		t.Errorf("assignee = %q, want serve-e2e", claimed.Assignee)
	}

	// The contract this server publishes: a CLI claim runs on_update, an HTTP
	// claim does not. Only the peel beneath the hook decorator makes that true.
	if _, err := os.Stat(hookMarker); err == nil {
		t.Error("an HTTP claim ran this workspace's on_update hook; bd serve documents that hooks do not fire")
	} else if !os.IsNotExist(err) {
		t.Errorf("stat hook marker: %v", err)
	}
}

// TestServeRefusesAnEmbeddedWorkspaceEndToEnd drives the permanent refusal
// through runServe rather than through serveDatabaseSource alone.
//
// The unit test proves the classification; this proves the wiring honors it. An
// edit that dropped the switch and always handed Listen the roles would pass
// the unit test and fail here, which is the regression the source-level pin and
// this test bracket from opposite sides.
func TestServeRefusesAnEmbeddedWorkspaceEndToEnd(t *testing.T) {
	useStorageModeGlobals(t)
	if !isEmbeddedMode() {
		t.Skip("this build cannot open an embedded workspace")
	}

	dir := t.TempDir()
	initGitRepoAt(t, dir)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	if err := (&configfile.Config{Backend: configfile.BackendDolt}).Save(beadsDir); err != nil {
		t.Fatalf("save metadata.json: %v", err)
	}

	restoreServeGlobals(t)
	// No store, deliberately: the refusal must come from the classification and
	// not from a role extraction that found nothing to take roles off.
	store = nil
	serveAddr = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	setRootContext(ctx, cancel)
	t.Chdir(dir)
	t.Setenv("BEADS_DIR", beadsDir)
	// Same cached-workspace hazard as startServeInProcess: runServe resolves
	// the workspace through GetRepoContext, which caches a prior test's
	// verdict — including its error — for the whole process.
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	// runServe surfaces the refusal through HandleError, which writes the
	// message to stderr and returns an opaque exit error, so the message is
	// only observable here.
	var err error
	stderr := captureBootstrapStderr(t, func() { err = runServe() })
	if err == nil {
		t.Fatalf("bd serve bound a server over an embedded Dolt workspace\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "embedded Dolt") {
		t.Errorf("runServe refused with %q; want the embedded refusal", stderr)
	}
	// Not "no store is open": a refusal from the role extraction would mean the
	// classification had already been bypassed.
	if strings.Contains(stderr, "no store is open") {
		t.Error("runServe reached the role extraction on an embedded workspace; the classification was bypassed")
	}
}

// seedRegisteredBackendWorkspace creates the embedded Dolt database the
// registered backend will open and puts one claimable issue in it. The store is
// closed before returning: it holds the workspace's exclusive lock, and the
// server is about to want it.
func seedRegisteredBackendWorkspace(t *testing.T, beadsDir string) string {
	t.Helper()
	seed, err := embeddeddolt.Open(t.Context(), beadsDir, serveRegisteredDatabase, "main")
	if err != nil {
		t.Fatalf("open the embedded store behind the registered backend: %v", err)
	}
	defer func() {
		if err := seed.Close(); err != nil {
			t.Fatalf("close the seed store: %v", err)
		}
	}()

	if err := seed.SetConfig(t.Context(), "issue_prefix", "srv"); err != nil {
		t.Fatalf("set issue prefix: %v", err)
	}
	const id = "srv-1"
	now := time.Now().UTC()
	issue := &types.Issue{
		ID:        id,
		Title:     "Claimable over HTTP",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		Priority:  2,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := seed.CreateIssue(t.Context(), issue, "seed"); err != nil {
		t.Fatalf("seed an issue: %v", err)
	}
	return id
}

// plantOnUpdateHook writes an on_update hook that touches marker. A CLI claim
// in this workspace runs it; an HTTP claim must not.
func plantOnUpdateHook(t *testing.T, beadsDir, marker string) {
	t.Helper()
	hooksDir := filepath.Join(beadsDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	script := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "on_update"), []byte(script), 0o755); err != nil {
		t.Fatalf("write on_update hook: %v", err)
	}
}

// startServeInProcess runs `bd serve` on the shared cobra command in a
// goroutine and returns the address it bound plus a channel carrying the run's
// result. The whole root command runs, so PersistentPreRunE is what opens the
// registered backend's store — the point being that serve consumes the store bd
// already creates rather than creating one of its own.
func startServeInProcess(t *testing.T, dir, beadsDir string) (string, <-chan error) {
	t.Helper()
	restoreServeGlobals(t)
	resetRootPersistentFlags(t)
	store = nil
	serverMode, proxiedServerMode = false, false

	t.Chdir(dir)
	t.Setenv("HOME", dir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_NO_DAEMON", "1")
	t.Setenv("BEADS_SKIP_IDENTITY_CHECK", "1")
	t.Setenv("BD_NON_INTERACTIVE", "1")
	t.Setenv("BD_DISABLE_METRICS", "1")
	t.Setenv("BD_DISABLE_EVENT_FLUSH", "1")

	// GetRepoContext caches per process — the context AND the error. A prior
	// in-process test that resolved (or failed to resolve) a workspace leaves
	// that verdict cached, and this command would serve it instead of the
	// tempdir above ("no .beads directory found", deterministic in Main's
	// cmd/bd shard 8 ordering). Reset on the way out too, so this test's
	// soon-to-be-deleted tempdir isn't the next test's cached workspace.
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	lines, stopCapture := captureStdoutLines(t)
	done := make(chan error, 1)
	go func() {
		rootCmd.SetArgs([]string{"serve", "--addr", "127.0.0.1:0"})
		err := rootCmd.Execute()
		stopCapture()
		done <- err
	}()

	return waitForBoundAddress(t, lines, done), done
}

// getJSON issues a GET and decodes a JSON object body, failing on anything else.
func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s status = %d, want 200: %s", url, resp.StatusCode, payload)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return body
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

// TestServeRefusesStrictReadonlyOnARegisteredBackend drives the strict-readonly
// refusal in the workspace where it was measured, and where the alternative was
// worst.
//
// Under `--readonly` the root command opens this workspace through
// backend.OpenReadOnly, and serve takes its claimer off that store. The server
// bound anyway, kept advertising `issues.claim` on GET /v0/beads/context — the
// capability set comes off the route table and cannot see a CLI flag — and
// answered every claim with an opaque 500, leaving the issue open and
// unassigned. That is a server lying about what it can do, which is worse than
// no server.
//
// The workspace here would serve: the same registration and the same store
// answer reads over HTTP when the flag is absent
// (TestServeAnswersFromTheStoreTheRootCommandOpened). What stops it is the
// refusal, not a workspace that could not have answered anyway.
// serveRefusalBudget is how long a refusal is given to be a refusal. Only a
// failing run ever waits it out.
const serveRefusalBudget = 30 * time.Second

func TestServeRefusesStrictReadonlyOnARegisteredBackend(t *testing.T) {
	const name = "serve-readonly-registered"
	backends.Register(name, backends.Backend{
		// Open is required by the registry and never reached: strict readonly
		// is what sends the root command down OpenReadOnly, and that is the
		// posture under test.
		Open: func(context.Context, string) (storage.DoltStorage, error) {
			return &serveIdentityStore{id: "read-write"}, nil
		},
		OpenReadOnly: func(context.Context, string) (storage.DoltStorage, error) {
			return &serveIdentityStore{id: "read-only"}, nil
		},
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
	beads.ResetCaches()
	t.Cleanup(beads.ResetCaches)

	origReadonly := readonlyMode
	readonlyMode = true
	t.Cleanup(func() { readonlyMode = origReadonly })

	// What PersistentPreRunE leaves behind under --readonly: the read-only open
	// of this workspace, which is the store serve would have taken a claimer
	// off.
	backend, ok := backends.Lookup(name)
	if !ok {
		t.Fatalf("backend %q is not registered", name)
	}
	opened, err := backend.OpenReadOnly(t.Context(), beadsDir)
	if err != nil {
		t.Fatalf("read-only open: %v", err)
	}
	store = opened

	serveAddr, serveAllowNonLoopback = "127.0.0.1:0", false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	setRootContext(ctx, cancel)

	// runServe is run on its own goroutine and bounded, because the failure
	// this test is here to catch does not return: a serve that binds blocks
	// until the root context is canceled, and an unbounded wait would turn the
	// regression into a package-wide timeout instead of one named failure.
	lines, stopCapture := captureStdoutLines(t)
	var (
		runErr error
		bound  bool
	)
	done := make(chan error, 1)
	stderr := captureBootstrapStderr(t, func() {
		go func() { done <- runServe() }()
		select {
		case runErr = <-done:
		case <-time.After(serveRefusalBudget):
			bound = true
			cancel()
			runErr = <-done
		}
	})
	stopCapture()

	if bound {
		t.Fatalf("bd --readonly serve bound a server over a read-only store and had to be stopped\nstderr:\n%s", stderr)
	}
	if runErr == nil {
		t.Fatalf("bd --readonly serve returned no error\nstderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--readonly") {
		t.Errorf("the refusal does not name the flag that caused it: %q", stderr)
	}
	for line := range lines {
		if strings.Contains(line, "listening on") {
			t.Errorf("bd serve bound before refusing: %q", line)
		}
	}
}
