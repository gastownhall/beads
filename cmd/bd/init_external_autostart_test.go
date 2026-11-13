//go:build cgo

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// `bd init --external` declares that the Dolt server is managed outside bd, so
// init must never start one. These tests own one boundary risk that no lower
// seam can show: init builds its dolt config by hand, so the flag reaches the
// auto-start decision only if init carries it there. They run the real bd
// binary against a dolt shim that records every launch, which makes the
// outcome observable without a real server and leaves nothing running.

// externalInitFixture is one fresh project plus a dolt shim for `bd init`
// attempts. The shim sits first on PATH, records every invocation and fails.
type externalInitFixture struct {
	t        *testing.T
	repoDir  string
	home     string
	shimDir  string
	launches string
	env      []string // extra environment for every run, after the isolation above
}

type externalInitResult struct {
	output   string // combined stdout and stderr
	failed   bool   // bd exited non-zero
	launches string // dolt invocations the shim saw, one per line
}

func newExternalInitFixture(t *testing.T) *externalInitFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the dolt shim is a POSIX shell script")
	}
	f := &externalInitFixture{t: t, repoDir: t.TempDir(), home: t.TempDir(), shimDir: t.TempDir()}
	f.launches = filepath.Join(f.shimDir, "invocations")
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$EXTERNAL_INIT_SHIM_LOG\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(f.shimDir, "dolt"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, f.repoDir)
	return f
}

// run executes bd in the project. autoStart is the value of
// BEADS_DOLT_AUTO_START, or "" to leave it unset: an unpinned environment is
// what most callers run in, so it is the one that matters here.
func (f *externalInitFixture) run(autoStart string, args ...string) externalInitResult {
	f.t.Helper()
	env := append(envWithoutBeadsStorageSettings(),
		"HOME="+f.home,
		"XDG_CONFIG_HOME="+filepath.Join(f.home, ".config"),
		"BEADS_TEST_IGNORE_REPO_CONFIG=1",
		"BD_DISABLE_METRICS=1",
		"PATH="+f.shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"EXTERNAL_INIT_SHIM_LOG="+f.launches,
	)
	if autoStart != "" {
		env = append(env, "BEADS_DOLT_AUTO_START="+autoStart)
	}
	env = append(env, f.env...)
	out, err := runExternalServerBD(f.t, f.repoDir, env, args...)
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		f.t.Fatalf("bd %s did not run: %v\n%s", strings.Join(args, " "), err, out)
	}
	launches, _ := os.ReadFile(f.launches)
	return externalInitResult{output: out, failed: err != nil, launches: strings.TrimSpace(string(launches))}
}

// externalEndpoint is the server an --external init is pointed at.
type externalEndpoint struct {
	flags   []string // --server-host/--server-port, or --server-socket
	network string   // "tcp" or "unix"
	addr    string   // host:port or the socket path, as bd reports it
}

func tcpEndpoint(port int) externalEndpoint {
	return externalEndpoint{
		flags:   []string{"--server-host", "127.0.0.1", "--server-port", strconv.Itoa(port)},
		network: "tcp",
		addr:    net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
	}
}

func unixEndpoint(path string) externalEndpoint {
	return externalEndpoint{flags: []string{"--server-socket", path}, network: "unix", addr: path}
}

// externalInitSocketPath returns an unused unix socket path. Socket paths are
// capped near 104 bytes and t.TempDir() nests too deeply under some runners.
func externalInitSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bdx")
	if err != nil {
		t.Fatal(err)
	}
	if len(filepath.Join(dir, "dolt.sock")) > 100 {
		_ = os.RemoveAll(dir)
		if dir, err = os.MkdirTemp("/tmp", "bdx"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "dolt.sock")
}

// serveUnixProxy listens on a fresh unix socket and forwards each connection
// to target, so bd sees a reachable server at a socket path. The returned
// func reports how many connections bd made through it.
func serveUnixProxy(t *testing.T, target string) (string, func() int) {
	t.Helper()
	path := externalInitSocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on %s: %v", path, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	accepted := 0
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted++
			mu.Unlock()
			go func() {
				defer client.Close()
				upstream, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer upstream.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
				go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
				<-done // either side closing ends the session; the deferred closes release the other copy
			}()
		}
	}()
	return path, func() int { mu.Lock(); defer mu.Unlock(); return accepted }
}

// requireNoLaunch fails when bd tried to run dolt at all: an externally
// managed server is never started by bd, and the shim is how a start shows.
func requireNoLaunch(t *testing.T, res externalInitResult) {
	t.Helper()
	if res.launches != "" {
		t.Errorf("bd launched dolt although the server is externally managed (dolt %s):\n%s",
			strings.ReplaceAll(res.launches, "\n", "; dolt "), res.output)
	}
}

// requireExternalUnreachableMessage checks the one wording an --external init
// gives for an endpoint it cannot reach: it names the endpoint, says the server
// is declared external and that bd did not start one, and never advises
// `bd dolt start`, which would start the server --external says bd must not.
func requireExternalUnreachableMessage(t *testing.T, output, endpoint string) {
	t.Helper()
	for _, want := range []string{endpoint, "externally managed", "did not start"} {
		if !strings.Contains(output, want) {
			t.Errorf("output does not contain %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "bd dolt start") {
		t.Errorf("output advises `bd dolt start`:\n%s", output)
	}
}

func requireNothingListening(t *testing.T, network, addr string) {
	t.Helper()
	conn, err := net.DialTimeout(network, addr, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Errorf("something accepts connections on %s %s after init", network, addr)
	}
}

// requireDoltRootEmpty fails when .beads/dolt holds anything: init leaves at
// most an empty marker directory for an external server, so any entry is a
// server root a local dolt created.
func requireDoltRootEmpty(t *testing.T, repoDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repoDir, ".beads", "dolt"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf(".beads/dolt holds %v, which init did not put there", names)
	}
}

// TestInitServerExternalDeadPortStartsNoServer is the end-to-end boundary for
// --external: a dead endpoint must fail loudly instead of making bd launch a
// dolt of its own on the port the caller named.
func TestInitServerExternalDeadPortStartsNoServer(t *testing.T) {
	f := newExternalInitFixture(t)
	ep := tcpEndpoint(freeLoopbackPort(t))

	res := f.run("", append(append([]string{"init", "--quiet", "--server", "--external"}, ep.flags...),
		"--prefix", "x", "--skip-hooks", "--skip-agents")...)

	if !res.failed {
		t.Errorf("bd init --server --external succeeded against a dead endpoint %s:\n%s", ep.addr, res.output)
	}
	requireNoLaunch(t, res)
	requireExternalUnreachableMessage(t, res.output, ep.addr)
	requireNothingListening(t, ep.network, ep.addr)
	requireDoltRootEmpty(t, f.repoDir)
}

// TestInitServerExternalNoPortStartsNoServer is the --external boundary that
// rule 2c (#5934) cannot see: the argv and the environment name no port, so
// nothing but the flag declares the server external. A server init in that
// shape launches a repo-local dolt by design (see
// TestInitServerWithoutNamedPortStillStartsServer), so with --external it must
// fail instead, and say that bd did not start one. There is no endpoint to
// name, so the wording is checked without one: the child inherits
// BEADS_TEST_MODE, under which the store dials port 1 rather than leave an
// unnamed port at 0, so the port-0 wording itself is pinned by
// TestExternalServerUnreachableMessage.
func TestInitServerExternalNoPortStartsNoServer(t *testing.T) {
	f := newExternalInitFixture(t)

	res := f.run("", "init", "--quiet", "--server", "--external", "--prefix", "x", "--skip-hooks", "--skip-agents")

	if !res.failed {
		t.Errorf("bd init --server --external succeeded with no server named:\n%s", res.output)
	}
	requireNoLaunch(t, res)
	requireDoltRootEmpty(t, f.repoDir)
	for _, want := range []string{"externally managed", "did not start"} {
		if !strings.Contains(res.output, want) {
			t.Errorf("output does not contain %q:\n%s", want, res.output)
		}
	}
	if strings.Contains(res.output, "bd dolt start") {
		t.Errorf("output advises `bd dolt start`:\n%s", res.output)
	}
}

// TestInitServerNamedPortDeadEndpointStartsNoServer is the same boundary
// without --external: a port named with --server-port or
// BEADS_DOLT_SERVER_PORT declares the server external on its own (rule 2c,
// #5934), so init starts none, and its failure must say so instead of
// advising the `bd dolt start` that would start one. Init leaves auto-start on
// here, so the store reaches EnsureRunning, which refuses rather than starts.
func TestInitServerNamedPortDeadEndpointStartsNoServer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		viaEnv     bool
		declaredBy string
	}{
		{"--server-port", false, "--server-port declares"},
		{"BEADS_DOLT_SERVER_PORT", true, "BEADS_DOLT_SERVER_PORT declares"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExternalInitFixture(t)
			port := freeLoopbackPort(t)
			ep := tcpEndpoint(port)
			args := []string{"init", "--quiet", "--server", "--prefix", "x", "--skip-hooks", "--skip-agents"}
			if tc.viaEnv {
				f.env = append(f.env, "BEADS_DOLT_SERVER_PORT="+strconv.Itoa(port))
			} else {
				args = append(args, ep.flags...)
			}

			res := f.run("", args...)

			if !res.failed {
				t.Errorf("bd init succeeded against a dead endpoint %s:\n%s", ep.addr, res.output)
			}
			requireNoLaunch(t, res)
			requireExternalUnreachableMessage(t, res.output, ep.addr)
			if !strings.Contains(res.output, tc.declaredBy) {
				t.Errorf("output does not contain %q:\n%s", tc.declaredBy, res.output)
			}
			requireNothingListening(t, ep.network, ep.addr)
		})
	}
}

// TestInitServerDialsCentralServerConfig pins GH#3895 for bd init: with no
// --server-* flag and no BEADS_DOLT_SERVER_* variable, a server-mode init
// dials the endpoint ~/.config/beads/server.json names, not 127.0.0.1, and,
// as for --external, starts no local dolt server for it.
func TestInitServerDialsCentralServerConfig(t *testing.T) {
	f := newExternalInitFixture(t)
	port := freeLoopbackPort(t)
	central := filepath.Join(f.home, "server.json")
	body := fmt.Sprintf(`{"dolt_server_host":"db.invalid","dolt_server_port":%d,"dolt_server_user":"central_user","dolt_server_tls":true}`, port)
	if err := os.WriteFile(central, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "BEADS_CENTRAL_CONFIG="+central)

	res := f.run("", "init", "--quiet", "--server", "--prefix", "x", "--skip-hooks", "--skip-agents")

	if !res.failed {
		t.Errorf("bd init --server succeeded with no server at db.invalid:\n%s", res.output)
	}
	requireNoLaunch(t, res)
	if want := net.JoinHostPort("db.invalid", strconv.Itoa(port)); !strings.Contains(res.output, want) {
		t.Errorf("bd init output does not name the central server config's endpoint %q:\n%s", want, res.output)
	}
	local := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if strings.Contains(res.output, local) {
		t.Errorf("bd init dialed %s, not the central server config's host:\n%s", local, res.output)
	}
	requireNothingListening(t, "tcp", local)
	requireDoltRootEmpty(t, f.repoDir)
}

// TestInitServerRecordsNoCentralServerConfigEndpoint pins that a server-mode
// init that reaches the server ~/.config/beads/server.json names records
// dolt_mode server in metadata.json but none of server.json's endpoint
// (GH#3895): a recorded copy would outrank server.json for every later
// command, so editing server.json would no longer move the workspace.
func TestInitServerRecordsNoCentralServerConfigEndpoint(t *testing.T) {
	skipIfNoDolt(t)
	f := newExternalInitFixture(t)
	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
	central := filepath.Join(f.home, "server.json")
	body := fmt.Sprintf(`{"dolt_server_host":"127.0.0.1","dolt_server_port":%d,"dolt_server_user":"root"}`, testDoltServerPort)
	if err := os.WriteFile(central, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "BEADS_CENTRAL_CONFIG="+central)

	res := f.run("", "init", "--quiet", "--server", "--database", database, "--prefix", "x", "--skip-hooks", "--skip-agents")

	if res.failed {
		t.Fatalf("bd init --server failed against the server the central server config names:\n%s", res.output)
	}
	requireNoLaunch(t, res)
	data, err := os.ReadFile(filepath.Join(f.repoDir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("metadata.json: %v\n%s", err, data)
	}
	if meta["dolt_mode"] != configfile.DoltModeServer {
		t.Errorf("metadata.json dolt_mode = %v, want %s:\n%s", meta["dolt_mode"], configfile.DoltModeServer, data)
	}
	for _, key := range []string{"dolt_server_host", "dolt_server_port", "dolt_server_user", "dolt_server_tls"} {
		if v, ok := meta[key]; ok {
			t.Errorf("metadata.json records %s = %v from the central server config:\n%s", key, v, data)
		}
	}
}

// TestInitStoreTakesCentralServerConfig pins the rest of what init's store
// takes from the central server config (GH#3895): its user and TLS, which
// no dial error names, and its precedence below the flags and the
// environment.
func TestInitStoreTakesCentralServerConfig(t *testing.T) {
	setup := func(t *testing.T) string {
		t.Helper()
		for _, name := range []string{"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
			"BEADS_DOLT_SERVER_USER", "BEADS_DOLT_SERVER_TLS", "BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE"} {
			t.Setenv(name, "")
		}
		central := filepath.Join(t.TempDir(), "server.json")
		body := `{"dolt_server_host":"db.invalid","dolt_server_port":15124,"dolt_server_user":"central_user","dolt_server_tls":true}`
		if err := os.WriteFile(central, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_CENTRAL_CONFIG", central)
		return filepath.Join(t.TempDir(), ".beads")
	}

	t.Run("no flag or environment", func(t *testing.T) {
		beadsDir := setup(t)
		host := initStoreServerHost("")
		port, source, _, _ := initStoreServerPort(beadsDir, host)
		if host != "db.invalid" || port != 15124 || source != doltserver.PortSourceCentralConfig {
			t.Errorf("init's store dials %s:%d (%s), want db.invalid:15124 (%s)", host, port, source, doltserver.PortSourceCentralConfig)
		}
		if got := initStoreServerUser(""); got != "central_user" {
			t.Errorf("initStoreServerUser() = %q, want central_user", got)
		}
		if !initDoltServerTLS() {
			t.Error("initDoltServerTLS() = false, want the central server config's true")
		}
		if !initCentralConfigNamesServer("", host, source) {
			t.Error("init would auto-start a local server for the central server config's endpoint")
		}
	})

	t.Run("central port at a host bd runs its server at", func(t *testing.T) {
		beadsDir := setup(t)
		t.Setenv("BEADS_DOLT_SERVER_HOST", "127.0.0.1")
		port, source, _, _ := initStoreServerPort(beadsDir, "127.0.0.1")
		if port != 15124 || source != doltserver.PortSourceCentralConfig {
			t.Errorf("initStoreServerPort = %d (%s), want 15124 (%s)", port, source, doltserver.PortSourceCentralConfig)
		}
		if !initCentralConfigNamesServer("", "127.0.0.1", source) {
			t.Error("init would auto-start a local server on the central server config's port")
		}
	})

	t.Run("flags and environment win", func(t *testing.T) {
		beadsDir := setup(t)
		t.Setenv("BEADS_DOLT_SERVER_PORT", "15200")
		t.Setenv("BEADS_DOLT_SERVER_USER", "env_user")
		t.Setenv("BEADS_DOLT_SERVER_TLS", "0")
		if got := initStoreServerHost("flag.invalid"); got != "flag.invalid" {
			t.Errorf("initStoreServerHost(--server-host) = %q, want flag.invalid", got)
		}
		port, source, _, _ := initStoreServerPort(beadsDir, "127.0.0.1")
		if port != 15200 || source != doltserver.PortSourceEnv {
			t.Errorf("initStoreServerPort = %d (%s), want 15200 (%s)", port, source, doltserver.PortSourceEnv)
		}
		if got := initStoreServerUser(""); got != "env_user" {
			t.Errorf("initStoreServerUser() = %q, want env_user", got)
		}
		if got := initStoreServerUser("flag_user"); got != "flag_user" {
			t.Errorf("initStoreServerUser(--server-user) = %q, want flag_user", got)
		}
		if initDoltServerTLS() {
			t.Error("initDoltServerTLS() = true, want BEADS_DOLT_SERVER_TLS=0 to disable it")
		}
		if initCentralConfigNamesServer("", "127.0.0.1", source) {
			t.Error("init treats an environment-named local endpoint as the central server config's")
		}
	})
}

// namedPortWorkspace writes a server-mode metadata.json into the fixture's
// project, naming metadataPort as dolt_server_port when it is nonzero.
func namedPortWorkspace(t *testing.T, f *externalInitFixture, metadataPort int) string {
	t.Helper()
	beadsDir := filepath.Join(f.repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := configfile.DefaultConfig()
	meta.Backend = configfile.BackendDolt
	meta.DoltMode = configfile.DoltModeServer
	meta.DoltDatabase = "named_port"
	meta.DoltServerPort = metadataPort
	if err := meta.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

// namedPortConfigYaml writes beadsDir's config.yaml with port as dolt.port.
func namedPortConfigYaml(t *testing.T, beadsDir string, port int) {
	t.Helper()
	yaml := fmt.Sprintf("dolt:\n  port: %d\n", port)
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestNamedPortServerUnreachableAdvice covers every other command: an
// ordinary command against a workspace whose named port (the environment, or
// metadata.json's dolt_server_port) declares its server externally managed
// starts no server and gives the same advice, naming the endpoint it dialed.
// metadata.json's port declares the server external even when config.yaml's
// dolt.port, which outranks it, chose the port: `bd dolt set port N
// --update-config` writes both.
func TestNamedPortServerUnreachableAdvice(t *testing.T) {
	for _, tc := range []struct {
		name       string
		setup      func(t *testing.T, f *externalInitFixture, port int)
		declaredBy string
	}{
		{"BEADS_DOLT_SERVER_PORT", func(t *testing.T, f *externalInitFixture, port int) {
			namedPortWorkspace(t, f, 0)
			f.env = append(f.env, "BEADS_DOLT_SERVER_PORT="+strconv.Itoa(port))
		}, "BEADS_DOLT_SERVER_PORT declares the Dolt server at"},
		{"metadata.json dolt_server_port", func(t *testing.T, f *externalInitFixture, port int) {
			namedPortWorkspace(t, f, port)
		}, "metadata.json declares the Dolt server at"},
		// Every successful command records the port it reached in bd's port
		// file, which then outranks dolt_server_port. It writes no pid file:
		// the server is still the one dolt_server_port declares.
		{"metadata.json dolt_server_port beside the port file an open wrote", func(t *testing.T, f *externalInitFixture, port int) {
			beadsDir := namedPortWorkspace(t, f, port)
			if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte(strconv.Itoa(port)), 0o600); err != nil {
				t.Fatal(err)
			}
			// This package runs bd in test mode, which takes a port-file
			// port for a production server and dials port 1 instead.
			// BEADS_TEST_SERVER=1 is that heuristic's opt-out.
			f.env = append(f.env, "BEADS_TEST_SERVER=1")
		}, "metadata.json declares the Dolt server at"},
		{"bd dolt set port --update-config", func(t *testing.T, f *externalInitFixture, port int) {
			beadsDir := namedPortWorkspace(t, f, 0)
			// --update-config writes dolt.port only into a config.yaml that exists.
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if res := f.run("", "dolt", "set", "port", strconv.Itoa(port), "--update-config"); res.failed {
				t.Fatalf("bd dolt set port failed:\n%s", res.output)
			}
			yaml, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
			if err != nil || !strings.Contains(string(yaml), strconv.Itoa(port)) {
				t.Fatalf("bd dolt set port --update-config left config.yaml without the port (%v):\n%s", err, yaml)
			}
		}, "metadata.json declares the Dolt server at"},
		{"config.yaml dolt.port other than dolt_server_port", func(t *testing.T, f *externalInitFixture, port int) {
			metadataPort := freeLoopbackPort(t)
			for metadataPort == port {
				metadataPort = freeLoopbackPort(t)
			}
			namedPortConfigYaml(t, namedPortWorkspace(t, f, metadataPort), port)
		}, "metadata.json declares this workspace's Dolt server externally managed, so bd did not start one at"},
		// config.yaml's dolt.port outranks the legacy BEADS_DOLT_PORT for the
		// dial, and the env var naming the same port still declares the server
		// external.
		{"config.yaml dolt.port equal to BEADS_DOLT_PORT", func(t *testing.T, f *externalInitFixture, port int) {
			namedPortConfigYaml(t, namedPortWorkspace(t, f, 0), port)
			f.env = append(f.env, "BEADS_DOLT_PORT="+strconv.Itoa(port))
		}, "BEADS_DOLT_PORT declares the Dolt server at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExternalInitFixture(t)
			port := freeLoopbackPort(t)
			ep := tcpEndpoint(port)
			tc.setup(t, f, port)

			res := f.run("", "list")

			if !res.failed {
				t.Errorf("bd list succeeded against a dead endpoint %s:\n%s", ep.addr, res.output)
			}
			requireNoLaunch(t, res)
			requireExternalUnreachableMessage(t, res.output, ep.addr)
			if !strings.Contains(res.output, tc.declaredBy) {
				t.Errorf("output does not contain %q:\n%s", tc.declaredBy, res.output)
			}
		})
	}
	// `bd dolt set port N` rewrites dolt_server_port and leaves the port file
	// an earlier command wrote, which outranks it, so bd still dials the old
	// port. The advice names that file as where the port came from and says
	// to remove it, not dolt_server_port, whose removal would not move the dial.
	t.Run("bd dolt set port beside the port file an open wrote", func(t *testing.T) {
		f := newExternalInitFixture(t)
		port := freeLoopbackPort(t)
		moved := freeLoopbackPort(t)
		for moved == port {
			moved = freeLoopbackPort(t)
		}
		ep := tcpEndpoint(port)
		beadsDir := namedPortWorkspace(t, f, port)
		if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte(strconv.Itoa(port)), 0o600); err != nil {
			t.Fatal(err)
		}
		// Test mode takes a port-file port for a production server and dials
		// port 1 instead; BEADS_TEST_SERVER=1 is that heuristic's opt-out.
		f.env = append(f.env, "BEADS_TEST_SERVER=1")
		if res := f.run("", "dolt", "set", "port", strconv.Itoa(moved)); res.failed {
			t.Fatalf("bd dolt set port failed:\n%s", res.output)
		}

		res := f.run("", "list")

		if !res.failed {
			t.Errorf("bd list succeeded against a dead endpoint %s:\n%s", ep.addr, res.output)
		}
		requireNoLaunch(t, res)
		requireExternalUnreachableMessage(t, res.output, ep.addr)
		for _, want := range []string{
			"metadata.json declares this workspace's Dolt server externally managed, so bd did not start one at " + ep.addr,
			fmt.Sprintf("dolt-server.port, which records the port an earlier command reached and outranks dolt_server_port (%d). "+
				"Start that server, or remove that file.", moved),
		} {
			if !strings.Contains(res.output, want) {
				t.Errorf("output does not contain %q:\n%s", want, res.output)
			}
		}
		if strings.Contains(res.output, "Removing dolt_server_port") {
			t.Errorf("output suggests removing dolt_server_port, which does not move the dial off the port file:\n%s", res.output)
		}
	})
}

// TestNamedPortServerDiagnosticsAdvice covers the commands that dial the
// server themselves instead of opening the store: they report a dead
// named-port endpoint with the same advice, not `bd dolt start`. The legacy
// BEADS_DOLT_PORT must make them dial its port, as the store does, rather
// than 127.0.0.1:0 when it is alone, or a port-file port left over beside it.
// A config.yaml dolt.port outranks it for the dial, as in the store, and the
// advice still names it when both name the same port. A remote host gets the
// remote-host advice. A host set only in the central server config is
// dialed, as bd list dials it (GH#3895).
func TestNamedPortServerDiagnosticsAdvice(t *testing.T) {
	diagnostics := [][]string{
		{"dolt", "test"},
		{"dolt", "clean-databases", "--dry-run"},
		{"doctor", "--perf"},
	}
	for _, envName := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		for _, args := range diagnostics {
			t.Run(envName+"/"+strings.Join(args, " "), func(t *testing.T) {
				runNamedPortDiagnostic(t, envName, "", args)
			})
		}
	}
	for _, args := range diagnostics {
		t.Run("BEADS_DOLT_PORT beside a port file/"+strings.Join(args, " "), func(t *testing.T) {
			runNamedPortDiagnostic(t, "BEADS_DOLT_PORT", "a port file", args)
		})
		t.Run("BEADS_DOLT_PORT equal to config.yaml dolt.port/"+strings.Join(args, " "), func(t *testing.T) {
			runNamedPortDiagnostic(t, "BEADS_DOLT_PORT", "config.yaml", args)
		})
	}
	for _, args := range diagnostics {
		t.Run("remote host/"+strings.Join(args, " "), func(t *testing.T) {
			runRemoteHostDiagnostic(t, args)
		})
		t.Run("host only in the central server config/"+strings.Join(args, " "), func(t *testing.T) {
			runCentralConfigHostDiagnostic(t, args)
		})
		for _, host := range []string{"::1", "[::1]"} {
			t.Run("IPv6 host "+host+"/"+strings.Join(args, " "), func(t *testing.T) {
				runIPv6HostDiagnostic(t, host, args)
			})
		}
	}
}

// runIPv6HostDiagnostic runs bd list and one diagnostic against a server at
// an IPv6 host that BEADS_DOLT_SERVER_HOST names bare ("::1") or bracketed
// ("[::1]"). The listener speaks no MySQL, so both fail, but both must reach
// it, as they would a server at any other host: bd list with its fail-fast
// probe and its connection, the diagnostic with its probe or connection. A
// diagnostic that dials ::1:port or [[::1]]:port instead reports the running
// server as not running.
func runIPv6HostDiagnostic(t *testing.T, host string, args []string) {
	t.Helper()
	port, accepted := testutil.IPv6LoopbackListener(t)
	f := newExternalInitFixture(t)
	namedPortWorkspace(t, f, 0)
	f.env = append(f.env, "BEADS_DOLT_SERVER_HOST="+host, "BEADS_DOLT_SERVER_PORT="+strconv.Itoa(port))

	for _, cmdArgs := range [][]string{{"list"}, args} {
		want := int64(1)
		if cmdArgs[0] == "list" {
			want = 2 // the fail-fast probe and the connection
		}
		before := accepted()
		res := f.run("", cmdArgs...)

		requireNoLaunch(t, res)
		for deadline := time.Now().Add(2 * time.Second); accepted()-before < want && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
		if got := accepted() - before; got < want {
			t.Errorf("bd %s made %d connections to the server at [::1]:%d, want at least %d:\n%s",
				strings.Join(cmdArgs, " "), got, port, want, res.output)
		}
		for _, banned := range []string{"not running", "[[::1]]", "no such host", "too many colons", "missing port"} {
			if strings.Contains(res.output, banned) {
				t.Errorf("bd %s output contains %q:\n%s", strings.Join(cmdArgs, " "), banned, res.output)
			}
		}
		if want := "Testing connection to " + net.JoinHostPort("::1", strconv.Itoa(port)); cmdArgs[0] == "dolt" &&
			cmdArgs[1] == "test" && !strings.Contains(res.output, want) {
			t.Errorf("bd dolt test output does not contain %q:\n%s", want, res.output)
		}
	}
}

// runCentralConfigHostDiagnostic runs bd list and one diagnostic against a
// workspace whose metadata.json names a dead port and whose host only the
// central server config (~/.config/beads/server.json) names. bd list's store
// open takes that host (GH#3895), so it dials db.invalid, and the diagnostic
// must dial the same endpoint: --perf times bd list, and the others check the
// server it uses.
func runCentralConfigHostDiagnostic(t *testing.T, args []string) {
	t.Helper()
	f := newExternalInitFixture(t)
	port := freeLoopbackPort(t)
	namedPortWorkspace(t, f, port)
	central := filepath.Join(f.home, "server.json")
	if err := os.WriteFile(central, []byte(`{"dolt_server_host":"db.invalid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "BEADS_CENTRAL_CONFIG="+central)
	want := net.JoinHostPort("db.invalid", strconv.Itoa(port))
	// The loopback endpoint, not "127.0.0.1" alone: a resolver on 127.0.0.1
	// can appear in the failed lookup of db.invalid.
	banned := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	for _, cmdArgs := range [][]string{{"list"}, args} {
		res := f.run("", cmdArgs...)

		requireNoLaunch(t, res)
		if !res.failed {
			t.Errorf("bd %s succeeded with no server:\n%s", strings.Join(cmdArgs, " "), res.output)
		}
		if !strings.Contains(res.output, want) {
			t.Errorf("bd %s output does not contain %q:\n%s", strings.Join(cmdArgs, " "), want, res.output)
		}
		if strings.Contains(res.output, banned) {
			t.Errorf("bd %s dialed %s, not the central server config's host:\n%s", strings.Join(cmdArgs, " "), banned, res.output)
		}
	}
}

// TestRuntimeCommandDialsCentralServerConfig is GH#3895 for an ordinary
// command: a server-mode workspace whose metadata.json names no host, port or
// user takes all three from the central server config (server.json, here the
// file BEADS_CENTRAL_CONFIG names), so bd list dials db.invalid:N, not
// 127.0.0.1, and, the endpoint being one the whole machine shares, starts no
// dolt server of its own.
func TestRuntimeCommandDialsCentralServerConfig(t *testing.T) {
	f := newExternalInitFixture(t)
	port := freeLoopbackPort(t)
	namedPortWorkspace(t, f, 0)
	central := filepath.Join(f.home, "server.json")
	body := fmt.Sprintf(`{"dolt_server_host":"db.invalid","dolt_server_port":%d,"dolt_server_user":"central_user"}`, port)
	if err := os.WriteFile(central, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "BEADS_CENTRAL_CONFIG="+central)

	res := f.run("", "list")

	requireNoLaunch(t, res)
	if !res.failed {
		t.Errorf("bd list succeeded with no server:\n%s", res.output)
	}
	if want := net.JoinHostPort("db.invalid", strconv.Itoa(port)); !strings.Contains(res.output, want) {
		t.Errorf("bd list output does not name the central server config's endpoint %q:\n%s", want, res.output)
	}
	if banned := net.JoinHostPort("127.0.0.1", strconv.Itoa(port)); strings.Contains(res.output, banned) {
		t.Errorf("bd list dialed %s, not the central server config's host:\n%s", banned, res.output)
	}
}

// TestRuntimeCommandPrefersConfigYAMLHostOverCentralServerConfig pins the
// other half of GH#3895's precedence: server.json is the lowest source, so a
// config.yaml dolt.host outranks its host while its port still applies.
func TestRuntimeCommandPrefersConfigYAMLHostOverCentralServerConfig(t *testing.T) {
	f := newExternalInitFixture(t)
	port := freeLoopbackPort(t)
	beadsDir := namedPortWorkspace(t, f, 0)
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt.host: yaml.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	central := filepath.Join(f.home, "server.json")
	body := fmt.Sprintf(`{"dolt_server_host":"db.invalid","dolt_server_port":%d}`, port)
	if err := os.WriteFile(central, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "BEADS_CENTRAL_CONFIG="+central)

	res := f.run("", "list")

	requireNoLaunch(t, res)
	if !res.failed {
		t.Errorf("bd list succeeded with no server:\n%s", res.output)
	}
	if want := net.JoinHostPort("yaml.invalid", strconv.Itoa(port)); !strings.Contains(res.output, want) {
		t.Errorf("bd list output does not name config.yaml's host with the central port %q:\n%s", want, res.output)
	}
	if strings.Contains(res.output, "db.invalid") {
		t.Errorf("bd list dialed the central server config's host over config.yaml dolt.host:\n%s", res.output)
	}
}

// runRemoteHostDiagnostic runs one diagnostic against a server on another
// machine whose port BEADS_DOLT_SERVER_PORT names. It must dial that host, not
// 127.0.0.1, and give the remote-host advice: not `bd dolt start`, which
// refuses a remote host, and not the local externally managed advice.
func runRemoteHostDiagnostic(t *testing.T, args []string) {
	t.Helper()
	f := newExternalInitFixture(t)
	port := freeLoopbackPort(t)
	namedPortWorkspace(t, f, 0)
	f.env = append(f.env, "BEADS_DOLT_SERVER_HOST=db.invalid", "BEADS_DOLT_SERVER_PORT="+strconv.Itoa(port))

	res := f.run("", args...)

	requireNoLaunch(t, res)
	if !res.failed {
		t.Errorf("bd %s succeeded with no server:\n%s", strings.Join(args, " "), res.output)
	}
	for _, want := range []string{net.JoinHostPort("db.invalid", strconv.Itoa(port)), fmt.Sprintf("nc -zv db.invalid %d", port)} {
		if !strings.Contains(res.output, want) {
			t.Errorf("output does not contain %q:\n%s", want, res.output)
		}
	}
	// The loopback endpoint, not "127.0.0.1" alone: a resolver on 127.0.0.1
	// can appear in the failed lookup of db.invalid.
	for _, banned := range []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), "bd dolt start", "externally managed"} {
		if strings.Contains(res.output, banned) {
			t.Errorf("output contains %q:\n%s", banned, res.output)
		}
	}
}

// runNamedPortDiagnostic runs one diagnostic against a dead port named by
// envName and checks it dials that port and names envName. beside adds
// another port source first: "a port file" leaves bd's port file behind on
// another dead port, which the named port must outrank; "config.yaml" names
// the same port as dolt.port, which outranks the legacy env var for the dial.
func runNamedPortDiagnostic(t *testing.T, envName, beside string, args []string) {
	t.Helper()
	f := newExternalInitFixture(t)
	port := freeLoopbackPort(t)
	ep := tcpEndpoint(port)
	beadsDir := namedPortWorkspace(t, f, 0)
	switch beside {
	case "":
	case "a port file":
		stale := freeLoopbackPort(t)
		for stale == port {
			stale = freeLoopbackPort(t)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "dolt-server.port"), []byte(strconv.Itoa(stale)), 0o600); err != nil {
			t.Fatal(err)
		}
	case "config.yaml":
		namedPortConfigYaml(t, beadsDir, port)
	default:
		t.Fatalf("unknown port source %q", beside)
	}
	f.env = append(f.env, envName+"="+strconv.Itoa(port))

	res := f.run("", args...)

	requireNoLaunch(t, res)
	requireExternalUnreachableMessage(t, res.output, ep.addr)
	if !strings.Contains(res.output, envName+" declares") {
		t.Errorf("output does not name %s:\n%s", envName, res.output)
	}
}

// TestInitSharedServerExternalDeadPortStillFails pins the sibling route that
// already honors --external: the same dead endpoint under --shared-server
// exits non-zero without launching dolt. Without it the test above could pass
// because every init fails here, not because --server honors the flag.
func TestInitSharedServerExternalDeadPortStillFails(t *testing.T) {
	f := newExternalInitFixture(t)
	ep := tcpEndpoint(freeLoopbackPort(t))

	res := f.run("", append(append([]string{"init", "--quiet", "--shared-server", "--external"}, ep.flags...),
		"--prefix", "x", "--skip-hooks", "--skip-agents")...)

	if !res.failed {
		t.Errorf("bd init --shared-server --external succeeded against a dead endpoint %s:\n%s", ep.addr, res.output)
	}
	requireNoLaunch(t, res)
}

// TestInitServerWithoutNamedPortStillStartsServer pins the other half of the
// contract: a --server init that names no port is where bd starts a repo-local
// dolt by design, so the no-launch assertions above cannot pass merely because
// nothing can ever launch one. It names no port, in argv or in the environment
// (envWithoutBeadsStorageSettings drops every BEADS_DOLT_* variable): a named
// port is declared external by rule 2c (#5934) with or without --external, so
// it would launch nothing here either way.
func TestInitServerWithoutNamedPortStillStartsServer(t *testing.T) {
	f := newExternalInitFixture(t)

	res := f.run("", "init", "--quiet", "--server", "--prefix", "x", "--skip-hooks", "--skip-agents")

	if res.launches == "" {
		t.Errorf("bd init --server naming no port never reached dolt, so auto-start regressed:\n%s", res.output)
	}
}

// TestInitExternalUnreachableMessage covers the wording across the routes an
// --external init can take. Every one of them gives the same message, whether
// auto-start is pinned off by the environment, the server is a shared one, or
// the endpoint is a socket.
func TestInitExternalUnreachableMessage(t *testing.T) {
	cases := []struct {
		name      string
		mode      string
		autoStart string
		endpoint  func(t *testing.T) externalEndpoint
	}{
		{"server host:port with auto-start pinned off", "--server", "0",
			func(t *testing.T) externalEndpoint { return tcpEndpoint(freeLoopbackPort(t)) }},
		{"shared-server host:port", "--shared-server", "",
			func(t *testing.T) externalEndpoint { return tcpEndpoint(freeLoopbackPort(t)) }},
		{"server socket", "--server", "",
			func(t *testing.T) externalEndpoint { return unixEndpoint(externalInitSocketPath(t)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newExternalInitFixture(t)
			ep := tc.endpoint(t)

			res := f.run(tc.autoStart, append(append([]string{"init", "--quiet", tc.mode, "--external"}, ep.flags...),
				"--prefix", "x", "--skip-hooks", "--skip-agents")...)

			if !res.failed {
				t.Fatalf("bd init succeeded against a dead endpoint %s:\n%s", ep.addr, res.output)
			}
			requireExternalUnreachableMessage(t, res.output, ep.addr)
		})
	}
}

// TestInitExternalProvisionerShape runs init with the argv a provisioner of an
// externally managed server uses, in both endpoint forms, with auto-start
// unset and pinned off, against a server that is up and one that is not. Every
// cell is a control: a port named with --server-port is already declared
// external by rule 2c (#5934), and a socket is never auto-started, so none of
// them launched dolt before init honored --external either. They pin that the
// provisioner shape keeps launching nothing, keeps failing against a dead
// endpoint, and still records the endpoint it was given. The reachable cells
// need the Dolt test server and skip without it.
func TestInitExternalProvisionerShape(t *testing.T) {
	for _, form := range []string{"host-port", "socket"} {
		for _, autoStart := range []string{"unset", "0"} {
			for _, reachable := range []bool{false, true} {
				state := "unreachable"
				if reachable {
					state = "reachable"
				}
				t.Run(fmt.Sprintf("%s/auto-start=%s/%s", form, autoStart, state), func(t *testing.T) {
					runProvisionerCell(t, form, autoStart, reachable)
				})
			}
		}
	}
}

func runProvisionerCell(t *testing.T, form, autoStart string, reachable bool) {
	t.Helper()
	if reachable {
		skipIfNoDolt(t)
	}
	f := newExternalInitFixture(t)

	var ep externalEndpoint
	var socketConnections func() int
	switch {
	case form == "host-port" && reachable:
		ep = tcpEndpoint(testDoltServerPort)
	case form == "host-port":
		ep = tcpEndpoint(freeLoopbackPort(t))
	case reachable:
		var sock string
		sock, socketConnections = serveUnixProxy(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(testDoltServerPort)))
		ep = unixEndpoint(sock)
	default:
		ep = unixEndpoint(externalInitSocketPath(t))
	}

	database := "provisioned"
	if reachable {
		database = uniqueTestDBName(t)
		t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
	}

	args := append([]string{"init", "--init-if-missing", "--quiet", "--server", "--external"}, ep.flags...)
	args = append(args, "-p", "prov", "--skip-hooks", "--skip-agents", "--database", database, f.repoDir)
	pin := ""
	if autoStart == "0" {
		pin = "0"
	}

	res := f.run(pin, args...)

	requireNoLaunch(t, res)
	if !reachable {
		if !res.failed {
			t.Errorf("bd init succeeded against an unreachable endpoint %s:\n%s", ep.addr, res.output)
		}
		requireNothingListening(t, ep.network, ep.addr)
		return
	}
	if res.failed {
		t.Fatalf("bd init against the reachable server %s failed:\n%s", ep.addr, res.output)
	}
	requireDoltRootEmpty(t, f.repoDir)
	data, err := os.ReadFile(filepath.Join(f.repoDir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("expected metadata.json after init: %v", err)
	}
	var meta configfile.Config
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse metadata.json: %v\n%s", err, data)
	}
	if form == "socket" {
		if meta.DoltServerSocket != ep.addr {
			t.Errorf("metadata.json dolt_server_socket = %q, want the socket init was given, %q", meta.DoltServerSocket, ep.addr)
		}
		if socketConnections() == 0 {
			t.Errorf("bd never connected through the socket %s", ep.addr)
		}
		return
	}
	if meta.DoltServerHost != "127.0.0.1" || meta.DoltServerPort != testDoltServerPort {
		t.Errorf("metadata.json names %s:%d, want the endpoint init was given, %s", meta.DoltServerHost, meta.DoltServerPort, ep.addr)
	}
}
