//go:build cgo

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/steveyegge/beads/internal/configfile"
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

	backends.Register(name, backends.Backend{
		Open: func(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
			return embeddeddolt.Open(ctx, beadsDir, serveRegisteredDatabase, "main")
		},
		OpenReadOnly: func(ctx context.Context, beadsDir string) (storage.DoltStorage, error) {
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

	lines, stopCapture := captureStdoutLines(t)
	done := make(chan error, 1)
	go func() {
		rootCmd.SetArgs([]string{"serve", "--addr", "127.0.0.1:0"})
		err := rootCmd.Execute()
		stopCapture()
		done <- err
	}()

	deadline := time.After(2 * time.Minute)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("bd serve exited before it bound: %v", <-done)
			}
			const prefix = "bd serve: listening on http://"
			if addr, found := strings.CutPrefix(strings.TrimSpace(line), prefix); found {
				return addr, done
			}
		case <-deadline:
			t.Fatal("bd serve did not print a bound address")
		}
	}
}

// restoreServeGlobals snapshots the package state one in-process serve run can
// touch and puts it back afterwards, so a registered backend and a bound server
// cannot leak into the tests sharing this binary.
//
// The flag set is part of that state and the least obvious part of it: cobra
// merges every inherited persistent flag into serveCmd's own FlagSet the first
// time it parses one, so a run through rootCmd.Execute leaves `bd serve`
// carrying --json, --db and the rest of the root's surface. That is what
// TestServeFlags reads. ResetFlags plus the command's own registration function
// is the un-merge cobra does not offer.
func restoreServeGlobals(t *testing.T) {
	t.Helper()
	origStore, origDBPath := store, dbPath
	origServer, origProxied := serverMode, proxiedServerMode
	origAddr, origNonLoopback := serveAddr, serveAllowNonLoopback
	origCtx, origCancel := rootCtx, rootCancel
	origCmdCtx, origUseGlobals := cmdCtx, testModeUseGlobals
	t.Cleanup(func() {
		if store != nil && store != origStore {
			store.Close()
		}
		serveCmd.ResetFlags()
		registerServeFlags(serveCmd) // rebinds serveAddr/serveAllowNonLoopback to the defaults
		store, dbPath = origStore, origDBPath
		serverMode, proxiedServerMode = origServer, origProxied
		serveAddr, serveAllowNonLoopback = origAddr, origNonLoopback
		rootCtx, rootCancel = origCtx, origCancel
		cmdCtx, testModeUseGlobals = origCmdCtx, origUseGlobals
		rootCmd.SetArgs(nil)
	})
}

// resetRootPersistentFlags puts every root persistent flag back to the default
// it was declared with, and clears its Changed bit, for the duration of one
// test.
//
// A test binary that runs the root command in-process inherits whatever the
// thousands of tests before it left in those flags and their bound globals —
// and `Changed` is what several PersistentPreRunE branches dispatch on, not the
// value. A stale `--db`/`--database` alone makes the pre-run refuse with
// "--database ... is only supported in proxied-server mode" before the command
// under test ever runs. Reset before, restore after, so this test neither reads
// nor writes that shared state.
func resetRootPersistentFlags(t *testing.T) {
	t.Helper()
	type flagState struct {
		value   string
		changed bool
	}
	before := map[string]flagState{}
	rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		before[f.Name] = flagState{value: f.Value.String(), changed: f.Changed}
		if err := f.Value.Set(f.DefValue); err != nil {
			t.Fatalf("reset --%s to its default %q: %v", f.Name, f.DefValue, err)
		}
		f.Changed = false
	})
	t.Cleanup(func() {
		rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			state, ok := before[f.Name]
			if !ok {
				return
			}
			_ = f.Value.Set(state.value)
			f.Changed = state.changed
		})
	})
}

// captureStdoutLines redirects os.Stdout and streams its lines. bd serve prints
// the address it bound — the only way to discover an ephemeral port — to
// stdout, and the server is running by the time it does.
func captureStdoutLines(t *testing.T) (<-chan string, func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		os.Stdout = orig
		_ = w.Close()
	}
	t.Cleanup(func() {
		stop()
		_ = r.Close()
	})
	return lines, stop
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
