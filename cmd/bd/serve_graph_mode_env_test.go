//go:build cgo

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// `bd serve` in an ordinary workspace is the same server whether or not
// BD_GRAPH_MODE=dependency is exported.
//
// bd reads no BD_GRAPH_MODE today. "dependency" is the value that names the
// format every existing workspace already has, so it is the one value a later
// reader of that variable (a graph-mode serve dispatch, say) must keep treating
// as "serve this workspace the way bd always has". These tests pin that before
// any such reader exists: with the variable exported, the run still has to
// reach runServe and build the same server.
//
// "The same server" is measured where an operator and a client measure it: the
// event=startup and event=limits lines, byte for byte apart from the ephemeral
// port, and the whole GET /v0/beads/context body. Each run is also checked on
// its own against what runServe builds (its mode label, its database source,
// the advertised capabilities), so two runs that were both routed somewhere
// else cannot pass by agreeing with each other.

const serveGraphModeEnv = "BD_GRAPH_MODE"

// TestServeOrdinaryWorkspaceIgnoresGraphModeDependency drives the whole root
// command in-process, PersistentPreRunE included, on a workspace served from
// the store source. In-process for the reason the registered-backend serve
// tests are: registration is Go wiring, so a spawned bd could not reach this
// arm. The server-mode twin below covers the provider source with a real bd.
func TestServeOrdinaryWorkspaceIgnoresGraphModeDependency(t *testing.T) {
	const name = "serve-graph-mode-env"

	dir := t.TempDir()
	initGitRepoAt(t, dir)
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	seedRegisteredBackendWorkspace(t, beadsDir)
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

	// Unset rather than empty, so the baseline is a shell that never exported
	// it. t.Setenv first so the original value comes back afterwards.
	t.Setenv(serveGraphModeEnv, "")
	if err := os.Unsetenv(serveGraphModeEnv); err != nil {
		t.Fatalf("unset %s: %v", serveGraphModeEnv, err)
	}
	ordinary := serveInProcessSnapshot(t, dir, beadsDir)

	t.Setenv(serveGraphModeEnv, "dependency")
	asserted := serveInProcessSnapshot(t, dir, beadsDir)

	mode := name + " (registered backend)"
	assertRunServeBuiltIt(t, serveGraphModeEnv+" unset", ordinary, "roles", mode)
	assertRunServeBuiltIt(t, serveGraphModeEnv+"=dependency", asserted, "roles", mode)
	if asserted.context["backend"] != name {
		t.Errorf("context backend = %v, want %q", asserted.context["backend"], name)
	}
	assertSameServer(t, ordinary, asserted)
}

// TestServerModeServeIgnoresGraphModeDependency is the same pin for a
// `bd init --server` workspace, the topology an ordinary deployment serves
// from: the provider source, which serve builds itself. It runs a real bd,
// because that arm fronts the server through a proxy child, and a proxy child
// cannot be spawned from inside the test binary.
func TestServerModeServeIgnoresGraphModeDependency(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newServerModeProject(t, bd, "srvgm")

	ordinaryEnv := envWithout(p.env, serveGraphModeEnv)
	assertedEnv := append(slices.Clone(ordinaryEnv), serveGraphModeEnv+"=dependency")

	ordinary := serveProcessSnapshot(t, startServe(t, bd, p.dir, ordinaryEnv))
	asserted := serveProcessSnapshot(t, startServe(t, bd, p.dir, assertedEnv))

	const mode = "server (external dolt)"
	assertRunServeBuiltIt(t, serveGraphModeEnv+" unset", ordinary, "provider", mode)
	assertRunServeBuiltIt(t, serveGraphModeEnv+"=dependency", asserted, "provider", mode)
	if asserted.context["dolt_mode"] != "server" || asserted.context["database"] != p.database {
		t.Errorf("context dolt_mode/database = %v/%v, want server/%s",
			asserted.context["dolt_mode"], asserted.context["database"], p.database)
	}
	assertSameServer(t, ordinary, asserted)
}

// serveSnapshot is what one bd serve run shows before any operation: its two
// startup events (from "event=" on, without the log prefix) and its handshake.
type serveSnapshot struct {
	addr    string
	startup string
	limits  string
	context map[string]any
}

// serveInProcessSnapshot runs `bd serve` through rootCmd, reads the handshake,
// and shuts it down the way an operator does.
func serveInProcessSnapshot(t *testing.T, dir, beadsDir string) serveSnapshot {
	t.Helper()
	stopStderr := captureStderrText(t)
	addr, done := startServeInProcess(t, dir, beadsDir)
	// Stops a server that a failure below left running. Keyed on whether done
	// has been read, not on rootCancel: a run that returns an error skips
	// PersistentPostRunE and leaves rootCancel set, and waiting on done again
	// after reading its only result would hang the test binary.
	drained := false
	t.Cleanup(func() {
		if !drained && rootCancel != nil {
			rootCancel()
			<-done
		}
	})
	body := getJSON(t, "http://"+addr+"/v0/beads/context")
	if rootCancel == nil {
		t.Fatal("the root command published no cancel function; nothing here can stop the server")
	}
	rootCancel()
	err := <-done
	drained = true
	if err != nil {
		t.Fatalf("bd serve returned %v, want a clean shutdown", err)
	}
	log := stopStderr()
	return serveSnapshot{
		addr:    addr,
		startup: serveEventLine(t, log, "startup"),
		limits:  serveEventLine(t, log, "limits"),
		context: body,
	}
}

// serveProcessSnapshot reads a spawned serve's startup and handshake, then
// stops it with SIGTERM.
func serveProcessSnapshot(t *testing.T, sp *serveProcess) serveSnapshot {
	t.Helper()
	startup := sp.awaitLogLine(t, "event=startup")
	limits := sp.awaitLogLine(t, "event=limits")
	status, body, _ := sp.get(t, "/v0/beads/context")
	if status != http.StatusOK {
		t.Fatalf("GET /v0/beads/context status = %d, want 200: %v", status, body)
	}
	sp.shutdown(t)
	return serveSnapshot{
		addr:    sp.addr,
		startup: serveEventLine(t, startup, "startup"),
		limits:  serveEventLine(t, limits, "limits"),
		context: body,
	}
}

// serveEventLine returns the named event's line from "event=" on.
func serveEventLine(t *testing.T, log, name string) string {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		if i := strings.Index(line, "event="+name+" "); i >= 0 {
			return line[i:]
		}
	}
	t.Fatalf("no event=%s line in:\n%s", name, log)
	return ""
}

// assertRunServeBuiltIt checks one run against the server runServe builds:
// serveResolvedMode's label, the Issue API's database source, and a
// capabilities field equal to the handshake's.
func assertRunServeBuiltIt(t *testing.T, run string, s serveSnapshot, source, mode string) {
	t.Helper()
	assertServedCapabilities(t, s.context)
	if s.context["api_version"] != "v0" {
		t.Errorf("%s: context api_version = %v, want v0", run, s.context["api_version"])
	}
	caps, _ := s.context["capabilities"].([]any)
	tokens := make([]string, 0, len(caps))
	for _, c := range caps {
		token, _ := c.(string)
		tokens = append(tokens, token)
	}
	for _, field := range []string{
		"addr=" + s.addr,
		"mode=" + strconv.Quote(mode),
		"db=" + source,
		"capabilities=" + strings.Join(tokens, ","),
	} {
		if !strings.Contains(s.startup+" ", " "+field+" ") {
			t.Errorf("%s: startup line is missing %s:\n%s", run, field, s.startup)
		}
	}
}

// assertSameServer compares two runs on everything but the ephemeral port.
func assertSameServer(t *testing.T, ordinary, asserted serveSnapshot) {
	t.Helper()
	normalize := func(s serveSnapshot) string {
		return strings.Replace(s.startup, " addr="+s.addr+" ", " addr=<bound> ", 1)
	}
	if got, want := normalize(asserted), normalize(ordinary); got != want {
		t.Errorf("%s=dependency changed the startup line:\n got: %s\nwant: %s", serveGraphModeEnv, got, want)
	}
	if asserted.limits != ordinary.limits {
		t.Errorf("%s=dependency changed the limits line:\n got: %s\nwant: %s", serveGraphModeEnv, asserted.limits, ordinary.limits)
	}
	if !reflect.DeepEqual(asserted.context, ordinary.context) {
		t.Errorf("%s=dependency changed GET /v0/beads/context:\n got: %v\nwant: %v", serveGraphModeEnv, asserted.context, ordinary.context)
	}
}

// captureStderrText redirects os.Stderr until the returned function is called,
// and returns everything written meanwhile. The restore also runs at cleanup,
// so a t.Fatal partway through cannot leak the redirect into later tests; a
// failed test logs what was captured.
func captureStderrText(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w

	var buf bytes.Buffer
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, _ = io.Copy(&buf, r)
	}()

	var once sync.Once
	var out string
	stop := func() string {
		once.Do(func() {
			os.Stderr = orig
			_ = w.Close()
			<-copied
			_ = r.Close()
			out = buf.String()
		})
		return out
	}
	t.Cleanup(func() {
		if text := stop(); t.Failed() {
			t.Logf("captured stderr:\n%s", text)
		}
	})
	return stop
}
