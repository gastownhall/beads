package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// endpointCredentialsEnv clears the settings a Dolt endpoint is resolved from
// and points the credentials file at one holding, for each host and port, a
// password naming them ("<host>-<port>").
func endpointCredentialsEnv(t *testing.T, hosts []string, ports ...int) {
	t.Helper()
	for _, name := range []string{
		"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_PASSWORD", "BEADS_DOLT_SERVER_USER", "BEADS_DOLT_SERVER_SOCKET",
		"BEADS_DOLT_CREDENTIAL_COMMAND", "BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_AUTO_START",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	var b strings.Builder
	for _, host := range hosts {
		for _, port := range ports {
			fmt.Fprintf(&b, "[%s:%d]\npassword=%s-%d\n\n", host, port, host, port)
		}
	}
	credentials := filepath.Join(home, "credentials")
	if err := os.WriteFile(credentials, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_CREDENTIALS_FILE", credentials)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
}

// TestResolveDoltServerConnectionPresentsThePasswordForThePortItDials covers
// the CLI store open and bd serve, which share resolveDoltServerConnection:
// the port it hands dolt.New must be the one New dials, and the password the
// credentials-file entry for that port. With only the legacy BEADS_DOLT_PORT
// naming the server, the password used to be looked up for the port resolved
// before New applied that variable (none, a port file's, the shared default
// 3308's), so every command was refused with "Access denied" while the same
// port named by BEADS_DOLT_SERVER_PORT worked (#7804 review).
func TestResolveDoltServerConnectionPresentsThePasswordForThePortItDials(t *testing.T) {
	const legacy, portFile, envPort = 13307, 14444, 13400
	for _, tc := range []struct {
		name       string
		legacyPort int
		envPort    int
		portFile   bool
		shared     bool
		wantPort   int
	}{
		{name: "legacy BEADS_DOLT_PORT only", legacyPort: legacy, wantPort: legacy},
		{name: "legacy over the port file", legacyPort: legacy, portFile: true, wantPort: legacy},
		{name: "legacy over the shared default", legacyPort: legacy, shared: true, wantPort: legacy},
		{name: "BEADS_DOLT_SERVER_PORT", envPort: envPort, wantPort: envPort},
		{name: "port file", portFile: true, wantPort: portFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpointCredentialsEnv(t, []string{"127.0.0.1"}, legacy, portFile, envPort, doltserver.DefaultSharedServerPort)
			if tc.legacyPort > 0 {
				t.Setenv("BEADS_DOLT_PORT", fmt.Sprint(tc.legacyPort))
			}
			if tc.envPort > 0 {
				t.Setenv("BEADS_DOLT_SERVER_PORT", fmt.Sprint(tc.envPort))
			}
			if tc.shared {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
			}
			beadsDir := t.TempDir()
			if tc.portFile {
				if err := doltserver.EnsurePortFile(beadsDir, portFile); err != nil {
					t.Fatal(err)
				}
			}
			fileCfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer}
			doltCfg := &dolt.Config{BeadsDir: beadsDir, ServerMode: true}
			if err := resolveDoltServerConnection(context.Background(), beadsDir, fileCfg, doltCfg); err != nil {
				t.Fatalf("resolveDoltServerConnection: %v", err)
			}
			// What New does to the port before it dials.
			dialed := *doltCfg
			dolt.ResolveServerPort(&dialed)

			if doltCfg.ServerPort != tc.wantPort || dialed.ServerPort != tc.wantPort {
				t.Fatalf("resolved port %d, New dials %d, want %d", doltCfg.ServerPort, dialed.ServerPort, tc.wantPort)
			}
			if want := fmt.Sprintf("127.0.0.1-%d", tc.wantPort); doltCfg.ServerPassword != want {
				t.Errorf("password = %q, want %q, the entry for the port New dials", doltCfg.ServerPassword, want)
			}
		})
	}
}

// TestServeTopologyDialsTheLegacyPort covers bd serve in a server-mode
// workspace whose port only the legacy BEADS_DOLT_PORT names: it fronts that
// server with that port's password, where it used to refuse "cannot determine
// the Dolt server port" while bd list worked.
func TestServeTopologyDialsTheLegacyPort(t *testing.T) {
	const legacy = 13307
	endpointCredentialsEnv(t, []string{"127.0.0.1"}, legacy)
	beadsDir := serverModeBeadsDir(t, &configfile.Config{DoltDatabase: "beads_serve"})
	t.Setenv("BEADS_DOLT_PORT", fmt.Sprint(legacy))

	topology, err := resolveServerModeUOWTopology(context.Background(), beadsDir)
	if err != nil {
		t.Fatalf("resolveServerModeUOWTopology: %v", err)
	}
	if topology.external == nil || topology.external.Port != legacy {
		t.Fatalf("serve fronts %+v, want the server at port %d", topology.external, legacy)
	}
	if want := fmt.Sprintf("127.0.0.1-%d", legacy); topology.rootPassword != want {
		t.Errorf("serve presents password %q, want %q", topology.rootPassword, want)
	}
}

// freeLocalPort is a 127.0.0.1 port nothing listens on.
func freeLocalPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// endpointDiagnosticCommands are the bd commands that report, probe or dial
// a workspace's externally managed Dolt server without opening its store,
// each of which must name the endpoint the store dials.
var endpointDiagnosticCommands = [][]string{
	{"dolt", "status"},
	{"dolt", "status", "--json"},
	{"doctor"},
}

// TestDoltDiagnosticsNameTheEndpointTheStoreDials drives bd dolt status and
// bd doctor through the binary for a server-mode workspace on 127.0.0.1
// whose server only a port variable names. The store dials that port (bd
// list reaches the server there), so each command must name it, not report
// port 0 or dial 127.0.0.1:0. With auto-start off, both resolved the port
// without the legacy variable (#7803 review): status printed "Port: 0" and
// doctor "no Dolt server port configured". A port variable declares the
// server externally managed, so with auto-start on no bd command starts a
// server there either, and status reported bd's PID file: "not running",
// "Expected port" (#7841 review). BEADS_DOLT_SERVER_PORT with auto-start off
// is the control, which was already right.
func TestDoltDiagnosticsNameTheEndpointTheStoreDials(t *testing.T) {
	bd := buildBDForInitTests(t)
	for _, tc := range []struct {
		name      string
		autoStart string
		portVar   string
	}{
		{name: "legacy BEADS_DOLT_PORT, auto-start off", autoStart: "false", portVar: "BEADS_DOLT_PORT"},
		{name: "BEADS_DOLT_SERVER_PORT, auto-start off", autoStart: "false", portVar: "BEADS_DOLT_SERVER_PORT"},
		{name: "legacy BEADS_DOLT_PORT, auto-start on", autoStart: "true", portVar: "BEADS_DOLT_PORT"},
		{name: "BEADS_DOLT_SERVER_PORT, auto-start on", autoStart: "true", portVar: "BEADS_DOLT_SERVER_PORT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _, env := externallyManagedStartWorkspace(t, tc.autoStart)
			port := strconv.Itoa(freeLocalPort(t))
			env = append(env, tc.portVar+"="+port)
			for _, args := range endpointDiagnosticCommands {
				cmd := exec.Command(bd, args...)
				cmd.Dir = root
				cmd.Env = env
				outBytes, _ := cmd.CombinedOutput()
				out := string(outBytes)
				name := "bd " + strings.Join(args, " ")
				if !strings.Contains(out, "127.0.0.1:"+port) && !strings.Contains(out, "Port:     "+port) && !strings.Contains(out, `"port": `+port) {
					t.Errorf("%s does not name the endpoint the store dials, port %s:\n%s", name, port, out)
				}
				for _, banned := range []string{"127.0.0.1:0", "Port:     0", `"port": 0`, "Expected port", "no Dolt server port"} {
					if strings.Contains(out, banned) {
						t.Errorf("%s contains %q: it resolved another port than the store's %s:\n%s", name, banned, port, out)
					}
				}
			}
		})
	}
}

// TestDoltDiagnosticsNeverDialAnUnresolvedPort drives bd dolt status (text
// and --json) and bd doctor through the binary for a server-mode workspace
// on 127.0.0.1 with auto-start off and no port named. No bd command starts
// its server, and every store open refuses with the advice to publish or pin
// its port: status gives that advice, where it dialed 127.0.0.1:0 and
// reported "dial tcp 127.0.0.1:0: connect: connection refused" (#7841
// review), and no diagnostic dials that port.
func TestDoltDiagnosticsNeverDialAnUnresolvedPort(t *testing.T) {
	bd := buildBDForInitTests(t)
	root, _, env := externallyManagedStartWorkspace(t, "false")
	const advice = "dolt.auto-start is off, so bd does not start this workspace's Dolt server."
	for _, args := range endpointDiagnosticCommands {
		cmd := exec.Command(bd, args...)
		cmd.Dir = root
		cmd.Env = env
		outBytes, _ := cmd.CombinedOutput()
		out := string(outBytes)
		name := "bd " + strings.Join(args, " ")
		if args[0] == "dolt" && !strings.Contains(out, advice) {
			t.Errorf("%s does not give the store's advice %q:\n%s", name, advice, out)
		}
		for _, banned := range []string{"127.0.0.1:0", "connection refused"} {
			if strings.Contains(out, banned) {
				t.Errorf("%s contains %q: it dialed a port nothing resolved:\n%s", name, banned, out)
			}
		}
	}
}

// TestDoltShowAndContextNameThePortTheStoreDials drives bd dolt show and bd
// context through the binary for a server-mode workspace on 127.0.0.1 whose
// server only the legacy BEADS_DOLT_PORT names, a listener that answers the
// MySQL handshake. bd list dials that port, so show must print it and pass
// its connection test there, and context must name it. Both took
// DefaultConfig's port, which leaves the legacy variable out (#7841 review):
// show printed "Port: 0" and "Server not reachable", and context printed
// 127.0.0.1:0 and dropped server_port from --json. BEADS_DOLT_SERVER_PORT is
// the control, which they already resolved.
func TestDoltShowAndContextNameThePortTheStoreDials(t *testing.T) {
	bd := buildBDForInitTests(t)
	for _, tc := range []struct {
		name      string
		autoStart string
		portVar   string
	}{
		{name: "legacy BEADS_DOLT_PORT, auto-start on", autoStart: "true", portVar: "BEADS_DOLT_PORT"},
		{name: "legacy BEADS_DOLT_PORT, auto-start off", autoStart: "false", portVar: "BEADS_DOLT_PORT"},
		{name: "BEADS_DOLT_SERVER_PORT, auto-start on", autoStart: "true", portVar: "BEADS_DOLT_SERVER_PORT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _, env := externallyManagedStartWorkspace(t, tc.autoStart)
			port := strconv.Itoa(newFakeMySQLLogin(t).port())
			env = append(env, tc.portVar+"="+port)
			for _, c := range []struct {
				args []string
				want []string
			}{
				{[]string{"dolt", "show"}, []string{"Port:     " + port, "Server connection OK"}},
				{[]string{"dolt", "show", "--json"}, []string{`"port": ` + port, `"connection_ok": true`}},
				{[]string{"context"}, []string{"server:       127.0.0.1:" + port}},
				{[]string{"context", "--json"}, []string{`"server_port": ` + port}},
			} {
				cmd := exec.Command(bd, c.args...)
				cmd.Dir = root
				cmd.Env = env
				outBytes, _ := cmd.CombinedOutput()
				out := string(outBytes)
				name := "bd " + strings.Join(c.args, " ")
				for _, want := range c.want {
					if !strings.Contains(out, want) {
						t.Errorf("%s does not contain %q, the port the store dials:\n%s", name, want, out)
					}
				}
				for _, banned := range []string{"127.0.0.1:0", "Port:     0", `"port": 0`, "not reachable"} {
					if strings.Contains(out, banned) {
						t.Errorf("%s contains %q: it resolved another port than the store's %s:\n%s", name, banned, port, out)
					}
				}
			}
		})
	}
}

// TestDoltDiagnosticsDialAConfiguredSocket observes, on the wire, where bd
// dolt status, bd dolt show and bd doctor dial a server-mode workspace whose
// Dolt server only a unix socket names, as docs/architecture/dolt.md
// documents (no port; bd starts no server there). The store opens the
// socket, so each must reach the listener on it. Status read bd's PID file
// with auto-start on ("not running") and dialed TCP at 127.0.0.1:0 with it
// off; doctor's Fresh Clone check dialed 127.0.0.1:0 (#7841 review). Status
// then named host and port 0 for the socket it dialed, and show's connection
// test still probed 127.0.0.1:0 and reported the server not reachable (#7874
// review): each names the socket. bd doctor reaches the socket through its
// Fresh Clone check; its own connection still does not dial the socket (see
// TestDoctorChecksDialAConfiguredSocket in cmd/bd/doctor).
//
// bd bootstrap is deliberately still refused there, as before: its metadata
// reconcile stays TCP-only and stops it before any step that cannot reach a
// socket-only server yet, so it must make no login on the socket.
func TestDoltDiagnosticsDialAConfiguredSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	bd := buildBDForInitTests(t)
	for _, tc := range []struct {
		name      string
		autoStart string
		inEnv     bool // BEADS_DOLT_SERVER_SOCKET, else metadata.json dolt_server_socket
	}{
		{name: "BEADS_DOLT_SERVER_SOCKET, auto-start on", autoStart: "true", inEnv: true},
		{name: "metadata.json dolt_server_socket, auto-start on", autoStart: "true"},
		{name: "BEADS_DOLT_SERVER_SOCKET, auto-start off", autoStart: "false", inEnv: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, beadsDir, env := externallyManagedStartWorkspace(t, tc.autoStart)
			socket := diagnosticSocketPath(t)
			fake := newFakeMySQLLoginOn(t, "unix", socket)
			if tc.inEnv {
				env = append(env, "BEADS_DOLT_SERVER_SOCKET="+socket)
			} else {
				cfg, err := configfile.Load(beadsDir)
				if err != nil || cfg == nil {
					t.Fatalf("load metadata: %v", err)
				}
				cfg.DoltServerSocket = socket
				if err := cfg.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
			}
			// A tracked issues.jsonl and no .beads/dolt make doctor's Fresh
			// Clone check look for the database on the server.
			if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"t-1","title":"x"}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, c := range []struct {
				args []string
				// login: the command logs in on the socket. bd dolt show's
				// connection test only probes it for a greeting.
				login bool
				want  []string // the socket it dialed, as it names it
				ban   []string // the host and port it did not dial
			}{
				{args: []string{"dolt", "status"}, login: true,
					want: []string{"Socket:   unix:" + socket}, ban: []string{"Host:", "Port:"}},
				{args: []string{"dolt", "status", "--json"}, login: true,
					want: []string{`"socket": "` + socket + `"`}, ban: []string{`"host":`, `"port":`}},
				{args: []string{"dolt", "show"},
					want: []string{"Socket:   unix:" + socket, "Server connection OK"}, ban: []string{"not reachable"}},
				{args: []string{"dolt", "show", "--json"},
					want: []string{`"socket": "` + socket + `"`, `"connection_ok": true`}},
				{args: []string{"doctor"}, login: true},
			} {
				before := len(fake.Logins())
				cmd := exec.Command(bd, c.args...)
				cmd.Dir = root
				cmd.Env = env
				outBytes, _ := cmd.CombinedOutput()
				out := string(outBytes)
				name := "bd " + strings.Join(c.args, " ")
				if c.login && len(fake.Logins()) == before {
					t.Errorf("%s never reached the server on the socket the store dials, %s:\n%s", name, socket, out)
				}
				for _, want := range c.want {
					if !strings.Contains(out, want) {
						t.Errorf("%s does not contain %q: it does not name the socket it dialed:\n%s", name, want, out)
					}
				}
				for _, banned := range append([]string{"127.0.0.1:0", "Dolt server: not running", "Expected port", `"pid":`}, c.ban...) {
					if strings.Contains(out, banned) {
						t.Errorf("%s contains %q: it did not dial the socket the store dials:\n%s", name, banned, out)
					}
				}
			}
			for _, args := range [][]string{
				{"bootstrap", "--dry-run"},
				{"bootstrap", "--yes"},
			} {
				before := len(fake.Logins())
				cmd := exec.Command(bd, args...)
				cmd.Dir = root
				cmd.Env = env
				outBytes, err := cmd.CombinedOutput()
				name := "bd " + strings.Join(args, " ")
				if err == nil {
					t.Errorf("%s succeeded; it must keep refusing a socket-only server:\n%s", name, outBytes)
				}
				if len(fake.Logins()) != before {
					t.Errorf("%s logged in on the socket %s; it must keep refusing a socket-only server:\n%s", name, socket, outBytes)
				}
				for _, stray := range []string{"embeddeddolt", "dolt", "dolt-server.pid", "dolt-server.port"} {
					if _, statErr := os.Stat(filepath.Join(beadsDir, stray)); statErr == nil {
						t.Errorf("%s left .beads/%s behind; it must write nothing:\n%s", name, stray, outBytes)
					}
				}
			}
		})
	}
}

// TestDoltStatusNamesTheSocketItDialsBesideAPort pins bd dolt status's
// report to the transport its probe dials when a live unix socket and a
// resolved port both name the server: the store dials the socket, and so
// does the probe, so status names the socket (Socket, and socket in --json),
// not the host and port it never dialed. It named them as though the server
// there had answered (#7874 review).
func TestDoltStatusNamesTheSocketItDialsBesideAPort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	tcp := newFakeMySQLLogin(t)
	socketServer := newFakeMySQLLoginOn(t, "unix", diagnosticSocketPath(t))
	socket := socketServer.ln.Addr().String()
	endpointCredentialsEnv(t, []string{"127.0.0.1"}, tcp.port())
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(tcp.port()))
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", socket)
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "status_socket"}
	ep := doltserver.ResolveEndpoint(t.TempDir(), cfg)

	orig := jsonOutput
	t.Cleanup(func() { jsonOutput = orig })
	jsonOutput = false
	text := captureStdout(t, func() error { runExternalDoltStatusAt(ep, cfg); return nil })
	if !strings.Contains(text, "Socket:   unix:"+socket) {
		t.Errorf("status does not name unix:%s, the socket it dialed:\n%s", socket, text)
	}
	for _, banned := range []string{"Host:", "Port:"} {
		if strings.Contains(text, banned) {
			t.Errorf("status prints %q beside the socket it dialed:\n%s", banned, text)
		}
	}

	jsonOutput = true
	out := captureStdout(t, func() error { runExternalDoltStatusAt(ep, cfg); return nil })
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	if result["socket"] != socket {
		t.Errorf("status --json socket = %v, want %s, the socket it dialed:\n%s", result["socket"], socket, out)
	}
	for _, key := range []string{"host", "port"} {
		if _, ok := result[key]; ok {
			t.Errorf("status --json reports %q beside the socket it dialed:\n%s", key, out)
		}
	}

	if n := len(socketServer.Logins()); n != 2 {
		t.Errorf("status logged in on the socket %s %d time(s), want once per run", socket, n)
	}
	if n := len(tcp.Logins()); n != 0 {
		t.Errorf("status logged in over TCP at %s %d time(s); the store dials the live socket %s", tcp.ln.Addr(), n, socket)
	}
}

// TestDoltStatusAndShowDialASocketOnlyServerBdDoesNotRun drives bd dolt
// status and bd dolt show through the binary for a server-mode workspace
// whose server only a unix socket names, at a host bd does not run: the IPv6
// loopback, another loopback address, another machine. The store dials that
// socket at any host, so each command probes it, and status names it. They
// advised pinning a port instead, which the store never needs, and probed
// nothing (#7874 review). With no socket that advice stands (see
// TestDoltCommandsAdviseAPortForAServerBdDoesNotRun).
func TestDoltStatusAndShowDialASocketOnlyServerBdDoesNotRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	bd := buildBDForInitTests(t)
	for _, host := range []string{"::1", "127.0.0.2", "db.invalid"} {
		t.Run(host, func(t *testing.T) {
			root, beadsDir, env := externallyManagedStartWorkspace(t, "true")
			env = configureDoltServerHost(t, beadsDir, env, host, false)
			socket := diagnosticSocketPath(t)
			fake := newFakeMySQLLoginOn(t, "unix", socket)
			env = append(env, "BEADS_DOLT_SERVER_SOCKET="+socket)
			for _, c := range []struct {
				args []string
				// login: the command logs in on the socket. bd dolt show's
				// connection test only probes it for a greeting.
				login bool
				want  []string
			}{
				{args: []string{"dolt", "status"}, login: true, want: []string{"Socket:   unix:" + socket}},
				{args: []string{"dolt", "status", "--json"}, login: true, want: []string{`"socket": "` + socket + `"`}},
				{args: []string{"dolt", "show"}, want: []string{"Server connection OK"}},
				{args: []string{"dolt", "show", "--json"}, want: []string{`"connection_ok": true`}},
			} {
				before := len(fake.Logins())
				cmd := exec.Command(bd, c.args...)
				cmd.Dir = root
				cmd.Env = env
				outBytes, _ := cmd.CombinedOutput()
				out := string(outBytes)
				name := "bd " + strings.Join(c.args, " ")
				if c.login && len(fake.Logins()) == before {
					t.Errorf("%s never logged in on the socket the store dials, %s:\n%s", name, socket, out)
				}
				for _, want := range c.want {
					if !strings.Contains(out, want) {
						t.Errorf("%s does not contain %q: it did not probe the socket the store dials:\n%s", name, want, out)
					}
				}
				for _, banned := range []string{"pin its port", "port configured"} {
					if strings.Contains(out, banned) {
						t.Errorf("%s contains %q: it advised pinning a port for a server the store dials on its socket:\n%s", name, banned, out)
					}
				}
			}
		})
	}
}

// TestDoctorFixDialsTheEndpointTheStoreDials observes, on the wire, the
// connection bd doctor --fix's SQL fixes open for a server-mode workspace on
// 127.0.0.1 (fix.RecomputeBlocked here; every SQL fix opens it the same way).
// bd doctor diagnoses, and bd list opens, the server the store dials, with the
// password the store presents there: the credentials-file entry for that
// port. The fixes took DefaultConfig's port, which leaves the legacy
// BEADS_DOLT_PORT out, so with only that variable naming the server each was
// skipped as unreachable at 127.0.0.1:0; they keyed the password to
// metadata.json's port (3307 when it names none), so at a port file's port
// they presented another entry's password (#7841 review). A socket-only
// server is deliberately still not dialed, as before: the fix connection and
// doctor's schema gate keep withholding writes there (see openFixDB), and the
// fix reports itself skipped (fix.ErrSkipped). Beside a resolved port, live
// socket or dead, the fixes dial TCP at that port, as base and plain doctor's
// own connection do, behind the project-id guard.
func TestDoctorFixDialsTheEndpointTheStoreDials(t *testing.T) {
	for _, tc := range []struct {
		name     string
		network  string // "tcp", or "unix" for a socket-only server
		legacy   bool   // BEADS_DOLT_PORT names the server
		portFile bool   // the port file names the server
		socket   string // BEADS_DOLT_SERVER_SOCKET beside the port: "live" (another server on it) or "dead"
	}{
		{name: "legacy BEADS_DOLT_PORT only", network: "tcp", legacy: true},
		{name: "port file, password for its port", network: "tcp", portFile: true},
		{name: "unix socket only, still skipped", network: "unix"},
		{name: "live unix socket beside BEADS_DOLT_PORT, TCP at the port", network: "tcp", legacy: true, socket: "live"},
		{name: "dead unix socket beside BEADS_DOLT_PORT, TCP at the port", network: "tcp", legacy: true, socket: "dead"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (tc.network == "unix" || tc.socket != "") && runtime.GOOS == "windows" {
				t.Skip("unix socket fixture")
			}
			var fake *fakeMySQLLogin
			port := 0
			if tc.network == "unix" {
				fake = newFakeMySQLLoginOn(t, "unix", diagnosticSocketPath(t))
			} else {
				fake = newFakeMySQLLogin(t)
				port = fake.port()
			}
			endpointCredentialsEnv(t, []string{"127.0.0.1"}, port, configfile.DefaultDoltServerPort)
			repo := t.TempDir()
			beadsDir := serverModeWorkspaceAt(t, repo)
			t.Setenv("BEADS_DIR", beadsDir)
			switch {
			case tc.network == "unix":
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", fake.ln.Addr().String())
			case tc.legacy:
				t.Setenv("BEADS_DOLT_PORT", strconv.Itoa(port))
			case tc.portFile:
				if err := doltserver.EnsurePortFile(beadsDir, port); err != nil {
					t.Fatal(err)
				}
			}
			var socketServer *fakeMySQLLogin
			switch tc.socket {
			case "live":
				socketServer = newFakeMySQLLoginOn(t, "unix", diagnosticSocketPath(t))
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", socketServer.ln.Addr().String())
			case "dead":
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", diagnosticSocketPath(t))
			}

			// The fake refuses every login, so the fix skips once it has
			// logged in; where it logged in is what this test observes.
			err := fix.RecomputeBlocked(repo)
			if err != nil && !errors.Is(err, fix.ErrSkipped) {
				t.Fatalf("RecomputeBlocked: %v", err)
			}
			logins := fake.Logins()
			if tc.network == "unix" {
				if len(logins) != 0 {
					t.Fatalf("the fix wrote through the socket-only server at %s; it must keep skipping it", fake.ln.Addr())
				}
				if !errors.Is(err, fix.ErrSkipped) {
					t.Fatalf("RecomputeBlocked = %v on a socket-only server, want it reported skipped (fix.ErrSkipped)", err)
				}
				return
			}
			if socketServer != nil && len(socketServer.Logins()) != 0 {
				t.Fatalf("the fix logged in on the socket %s; it does not dial a socket yet", socketServer.ln.Addr())
			}
			if len(logins) == 0 {
				t.Fatalf("the fix never reached the server the store dials (%s)", fake.ln.Addr())
			}
			want := ""
			if port > 0 {
				want = fmt.Sprintf("127.0.0.1-%d", port)
			}
			for _, login := range logins {
				if !fake.PresentsPassword(login, want) {
					t.Errorf("the fix logged in as %q without the password the store presents there (%q)", login.User, want)
				}
			}
		})
	}
}

// TestDoctorFixReportsASkippedRepairAsSkipped pins what bd doctor --fix
// prints for a SQL fix that did not run (fix.ErrSkipped). Each fix printed
// "... fix skipped (...)" and returned nil, which applyFixList counted as
// fixed: "✓ Fixed" and "1 fixed, 0 errors" for a repair that never ran, as
// for a server only a unix socket names. Beside a live socket and a resolved
// port the fix dials TCP at the port, as base did, and the fake there refuses
// the login, so that fix is reported skipped as well (#7874 review).
func TestDoctorFixReportsASkippedRepairAsSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	for _, tc := range []struct {
		name string
		port bool // BEADS_DOLT_PORT names a TCP server beside the socket
	}{
		{name: "live unix socket only"},
		{name: "live unix socket beside BEADS_DOLT_PORT", port: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tcp := newFakeMySQLLogin(t)
			endpointCredentialsEnv(t, []string{"127.0.0.1"}, tcp.port())
			repo := t.TempDir()
			t.Setenv("BEADS_DIR", serverModeWorkspaceAt(t, repo))
			socketServer := newFakeMySQLLoginOn(t, "unix", diagnosticSocketPath(t))
			t.Setenv("BEADS_DOLT_SERVER_SOCKET", socketServer.ln.Addr().String())
			if tc.port {
				t.Setenv("BEADS_DOLT_PORT", strconv.Itoa(tcp.port()))
			}

			out := captureStdout(t, func() error {
				applyFixList(repo, []doctorCheck{{Name: "Blocked State", Status: statusWarning, Message: "stale is_blocked", Fix: "bd doctor --fix"}})
				return nil
			})
			if strings.Contains(out, " Fixed\n") || !strings.Contains(out, "Skipped: ") {
				t.Errorf("bd doctor --fix reported a repair it skipped as fixed:\n%s", out)
			}
			if !strings.Contains(out, "Fix summary: 0 fixed, 1 skipped, 0 errors") {
				t.Errorf("bd doctor --fix summary does not count the skip:\n%s", out)
			}
			if tc.port && len(tcp.Logins()) == 0 {
				t.Errorf("the fix never dialed TCP at the resolved port %d beside the live socket:\n%s", tcp.port(), out)
			}
			if !tc.port && strings.Contains(out, "127.0.0.1:0") {
				t.Errorf("the fix dialed 127.0.0.1:0 for a socket-only server:\n%s", out)
			}
			if len(socketServer.Logins()) != 0 {
				t.Errorf("the fix logged in on the socket %s; it does not dial a socket yet", socketServer.ln.Addr())
			}
		})
	}
}

// TestDoctorScansReadTheServerTheirFixesWrite observes, on the wire, where bd
// doctor's read-only SQL scans connect: the Clone-Local FKs check
// (fix.ScanSeveredCloneLocalFKs) and --check=validate's Cross-Table
// Duplicates check (fix.CountCrossTableDuplicates). Beside a resolved port
// they read the server their fixes write (openFixDB), TCP at the port, live
// socket or dead. They dialed a live socket there, as the store does, so
// --fix repaired the server at the port while the problem stayed on the
// socket's (#7874 review). A server only a unix socket names, which the
// fixes do not dial, they read on its socket; they had shared the fixes'
// failed dial of 127.0.0.1:0 there, and each check reported "N/A", having
// queried no server (#7874 review).
//
// A socket that accepts and never sends the MySQL greeting, as a stopped or
// paused sql-server's does, is live to the store's probe, so the scans dial
// it too, and each must give up on its own deadline. Their ping had none, so
// bd doctor waited on that greeting forever at the Clone-Local FKs check,
// where it had failed at once dialing 127.0.0.1:0 (#7874 review).
func TestDoctorScansReadTheServerTheirFixesWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	// greetingWait bounds a scan on a mute socket: the scans' 5s ping
	// deadline, with room for a loaded runner.
	const greetingWait = 15 * time.Second
	for _, tc := range []struct {
		name   string
		port   bool   // BEADS_DOLT_SERVER_PORT names the TCP server
		socket string // BEADS_DOLT_SERVER_SOCKET: "live" (a server on it), "dead", or "mute" (accepts, never greets)
	}{
		{name: "live unix socket beside a port, TCP as the fixes", port: true, socket: "live"},
		{name: "live unix socket only", socket: "live"},
		{name: "dead unix socket beside a port, TCP as the fixes", port: true, socket: "dead"},
		{name: "mute unix socket only, the scans give up", socket: "mute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tcp := newFakeMySQLLogin(t)
			endpointCredentialsEnv(t, []string{"127.0.0.1"}, tcp.port())
			repo := t.TempDir()
			t.Setenv("BEADS_DIR", serverModeWorkspaceAt(t, repo))
			if tc.port {
				t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(tcp.port()))
			}
			// want is the server the scans read: TCP's beside a port, the
			// live socket's without one, and none on a mute socket, where no
			// login is made. other, the server beside it, they do not.
			want := tcp
			var other *fakeMySQLLogin
			switch tc.socket {
			case "live":
				socketServer := newFakeMySQLLoginOn(t, "unix", diagnosticSocketPath(t))
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", socketServer.ln.Addr().String())
				want, other = socketServer, tcp
				if tc.port {
					want, other = tcp, socketServer
				}
			case "mute":
				socket := diagnosticSocketPath(t)
				newMuteListenerOn(t, "unix", socket)
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", socket)
				want = nil
			default:
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", diagnosticSocketPath(t))
			}

			// The fake refuses every login, so each scan fails once it has
			// logged in; where it logged in is what this test observes.
			for _, scan := range []struct {
				name string
				run  func() error
			}{
				{"fix.CountCrossTableDuplicates", func() error { _, err := fix.CountCrossTableDuplicates(repo); return err }},
				{"fix.ScanSeveredCloneLocalFKs", func() error { _, err := fix.ScanSeveredCloneLocalFKs(repo); return err }},
			} {
				if want == nil {
					done := make(chan error, 1)
					go func() { done <- scan.run() }()
					select {
					case err := <-done:
						if err == nil {
							t.Errorf("%s succeeded on a socket that never sends the MySQL greeting", scan.name)
						}
					case <-time.After(greetingWait):
						t.Errorf("%s still waits for the MySQL greeting after %s on a socket that never sends one; it must give up on its own deadline", scan.name, greetingWait)
					}
					continue
				}
				before := len(want.Logins())
				err := scan.run()
				if len(want.Logins()) == before {
					t.Errorf("%s never logged in to the server it reads here (%s): %v", scan.name, want.ln.Addr(), err)
				}
			}
			if other != nil && len(other.Logins()) != 0 {
				t.Errorf("the scans logged in to %s; here they read %s", other.ln.Addr(), want.ln.Addr())
			}
		})
	}
}

// TestDoctorChecksReadTheServerTheirRepairsWrite observes, on the wire, that
// each bd doctor check whose problem bd doctor --fix repairs over SQL reads
// the server that repair writes when a live unix socket and a resolved port
// name two servers: Fresh Clone and fix.FreshCloneImport, Cross-Table
// Duplicates and fix.CrossTableDuplicates, Clone-Local FKs and
// fix.CloneLocalFKEnforcement, Orphaned Dependencies and
// fix.OrphanedDependencies, Child-Parent Dependencies and
// fix.ChildParentDependencies, Dependency Keys and fix.DependencyKeys, and
// Blocked State and fix.RecomputeBlocked. The SQL fixes dial TCP at the port
// (openFixDB). The Fresh Clone repair opens the store through the library
// open, which dials BEADS_DOLT_SERVER_SOCKET socket first and never reads
// metadata.json's dolt_server_socket. The checks dialed the live socket the
// CLI's store dials, so --fix repaired the server at the port while the
// check had read the socket's, and Fresh Clone's repair imported
// issues.jsonl over live rows there (#7874 review). Orphaned Dependencies,
// Child-Parent Dependencies, Dependency Keys and Blocked State still read the
// store's socket once the first three read the server their repairs write,
// so their repairs deleted, re-keyed or recomputed rows at the port that the
// check had not read (#7874 review).
//
// Each check also logs in as the user its repair presents. The library open
// presents a BEADS_DOLT_CREDENTIAL_COMMAND token over the static user. The
// Fresh Clone check presented the static user, which a gateway refuses, so
// it reported the server unreachable while its repair imported issues.jsonl
// through that gateway over live rows (#7874 review).
func TestDoctorChecksReadTheServerTheirRepairsWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	const gatewayToken = "tok-gw"
	for _, tc := range []struct {
		name    string
		inEnv   bool   // BEADS_DOLT_SERVER_SOCKET, else metadata.json dolt_server_socket
		credCmd string // BEADS_DOLT_CREDENTIAL_COMMAND: a gateway
	}{
		{name: "BEADS_DOLT_SERVER_SOCKET beside a port", inEnv: true},
		{name: "metadata.json dolt_server_socket beside a port"},
		{name: "BEADS_DOLT_SERVER_SOCKET beside a port, gateway credential command", inEnv: true, credCmd: "printf " + gatewayToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tcp := newFakeMySQLLogin(t)
			socketServer := newFakeMySQLLoginOn(t, "unix", diagnosticSocketPath(t))
			socket := socketServer.ln.Addr().String()
			endpointCredentialsEnv(t, []string{"127.0.0.1"}, tcp.port())
			t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", tc.credCmd)
			repo := t.TempDir()
			beadsDir := serverModeWorkspaceAt(t, repo)
			t.Setenv("BEADS_DIR", beadsDir)
			t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(tcp.port()))
			if tc.inEnv {
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", socket)
			} else {
				cfg, err := configfile.Load(beadsDir)
				if err != nil || cfg == nil {
					t.Fatalf("load metadata: %v", err)
				}
				cfg.DoltServerSocket = socket
				if err := cfg.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
			}
			// A tracked issues.jsonl and no .beads/dolt make the Fresh Clone
			// check look for the database on the server.
			if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"t-1","title":"x"}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			// The fakes refuse every login, so each check and repair fails
			// once it has logged in; where, and as whom, it logged in is
			// what this test observes.
			for _, pair := range []struct {
				name          string
				check, repair func()
				libraryOpen   bool // the repair opens the store through the library open
			}{
				{name: "Fresh Clone", libraryOpen: true,
					check:  func() { doctor.CheckFreshClone(repo) },
					repair: func() { _ = fix.FreshCloneImport(repo, "") }},
				{name: "Cross-Table Duplicates",
					check:  func() { _, _ = fix.CountCrossTableDuplicates(repo) },
					repair: func() { _ = fix.CrossTableDuplicates(repo, false) }},
				{name: "Clone-Local FKs",
					check:  func() { _, _ = fix.ScanSeveredCloneLocalFKs(repo) },
					repair: func() { _ = fix.CloneLocalFKEnforcement(repo, false) }},
				{name: "Orphaned Dependencies",
					check:  func() { doctor.CheckOrphanedDependencies(repo, nil) },
					repair: func() { _ = fix.OrphanedDependencies(repo, false) }},
				{name: "Child-Parent Dependencies",
					check:  func() { doctor.CheckChildParentDependencies(repo, nil) },
					repair: func() { _ = fix.ChildParentDependencies(repo, false) }},
				{name: "Dependency Keys",
					check:  func() { doctor.CheckDependencyKeysWithStore(repo, nil) },
					repair: func() { _ = fix.DependencyKeys(repo, false) }},
				{name: "Blocked State",
					check:  func() { doctor.CheckBlockedConsistencyWithStore(repo, nil) },
					repair: func() { _ = fix.RecomputeBlocked(repo) }},
			} {
				want, other := tcp, socketServer
				if pair.libraryOpen && tc.inEnv {
					want, other = socketServer, tcp
				}
				var users [2][]string // the users the check, then its repair, logged in as
				for i, step := range []struct {
					name string
					run  func()
				}{
					{pair.name + " check", pair.check},
					{pair.name + " repair", pair.repair},
				} {
					wantBefore, otherBefore := len(want.Logins()), len(other.Logins())
					step.run()
					logins := want.Logins()
					if len(logins) == wantBefore {
						t.Errorf("the %s never logged in to %s, the server the check and its repair must both reach", step.name, want.ln.Addr())
					}
					if len(other.Logins()) != otherBefore {
						t.Errorf("the %s logged in to %s, the other server", step.name, other.ln.Addr())
					}
					for _, login := range logins[wantBefore:] {
						if !slices.Contains(users[i], login.User) {
							users[i] = append(users[i], login.User)
						}
					}
				}
				if !slices.Equal(users[0], users[1]) {
					t.Errorf("the %s check logged in as %q and its repair as %q; the check must present the identity its repair presents", pair.name, users[0], users[1])
				}
				if tc.credCmd != "" && pair.libraryOpen && !slices.Equal(users[1], []string{gatewayToken}) {
					t.Errorf("the %s repair logged in as %q; the library open presents the credential command's token %q", pair.name, users[1], gatewayToken)
				}
			}
		})
	}
}

// TestDoctorFixImportsOnlyAFreshCloneTheCheckFound pins bd doctor --fix's
// Fresh Clone repair, the import of issues.jsonl (fix.FreshCloneImport), to a
// check that found the database missing (doctor.FreshCloneDetected). The
// check also warns when it could not log in to the server, and says that is
// not a fresh clone; --fix imported there all the same, so any server or
// identity the repair could reach and the check could not had its live rows
// overwritten with the tracked copy (#7874 review). That repair is reported
// skipped and never opens the server; a fresh clone the check found is
// still imported.
func TestDoctorFixImportsOnlyAFreshCloneTheCheckFound(t *testing.T) {
	fake := newFakeMySQLLogin(t)
	port := strconv.Itoa(fake.port())
	endpointCredentialsEnv(t, []string{"127.0.0.1"}, fake.port())
	repo := t.TempDir()
	beadsDir := serverModeWorkspaceAt(t, repo)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SERVER_PORT", port)
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"t-1","title":"x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake refuses every login, so the check reports the server
	// unreachable, and an import that runs fails once it has logged in.
	unreachable := convertDoctorCheck(doctor.CheckFreshClone(repo))
	if doctor.FreshCloneDetected(unreachable.Message) {
		t.Fatalf("Fresh Clone = %q against a server that refuses its login, want it reported unreachable", unreachable.Message)
	}
	for _, tc := range []struct {
		name       string
		check      doctorCheck
		wantImport bool
	}{
		{name: "server unreachable, skipped", check: unreachable},
		{name: "fresh clone detected, imported", wantImport: true, check: doctorCheck{
			Name: "Fresh Clone", Status: statusWarning, Fix: "bd bootstrap",
			Message: `Fresh clone detected: database "fix_endpoint" not found on server at 127.0.0.1:` + port + ".",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(fake.Logins())
			out := captureStdout(t, func() error {
				applyFixList(repo, []doctorCheck{tc.check})
				return nil
			})
			if imported := len(fake.Logins()) != before; imported != tc.wantImport {
				t.Errorf("Fresh Clone %q: the import opened the server: %v, want %v\n%s", tc.check.Message, imported, tc.wantImport, out)
			}
			if !tc.wantImport && !strings.Contains(out, "Fix summary: 0 fixed, 1 skipped, 0 errors") {
				t.Errorf("bd doctor --fix does not report the Fresh Clone repair it did not run as skipped:\n%s", out)
			}
		})
	}
}

// TestDoctorDoltModeListsRemotesOnTheServerItProbes observes, on the wire,
// that bd doctor's Dolt Mode check (doctor.CheckDoltServerModeMismatch)
// lists federation remotes on the server its reachability probe reached when
// a live unix socket and a resolved port name two servers. The probe dials
// the store's socket, BEADS_DOLT_SERVER_SOCKET else metadata.json's
// dolt_server_socket, while the open that lists remotes (dolt.New) took its
// socket from BEADS_DOLT_SERVER_SOCKET alone, so beside metadata.json's
// socket the check reported that socket's server connected and counted the
// federation peers of the server at the port (#7874 review).
//
// The other federation checks in the same bd doctor run (Dolt Remote
// Migration, Federation remotesapi, Peer Connectivity, Sync Staleness and
// Federation Conflicts) open the same server: they still read the server at
// the port beside metadata.json's socket once Dolt Mode read the socket's
// (#7874 review).
func TestDoctorDoltModeListsRemotesOnTheServerItProbes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture")
	}
	for _, tc := range []struct {
		name  string
		inEnv bool // BEADS_DOLT_SERVER_SOCKET, else metadata.json dolt_server_socket
	}{
		{name: "BEADS_DOLT_SERVER_SOCKET beside a port", inEnv: true},
		{name: "metadata.json dolt_server_socket beside a port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tcp := newFakeMySQLLogin(t)
			socketServer := newFakeMySQLLoginOn(t, "unix", diagnosticSocketPath(t))
			socket := socketServer.ln.Addr().String()
			endpointCredentialsEnv(t, []string{"127.0.0.1"}, tcp.port())
			t.Setenv("BEADS_DOLT_DATA_DIR", "")
			repo := t.TempDir()
			beadsDir := serverModeWorkspaceAt(t, repo)
			// The check reads the server only for a workspace with a data
			// directory.
			if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BEADS_DIR", beadsDir)
			t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(tcp.port()))
			if tc.inEnv {
				t.Setenv("BEADS_DOLT_SERVER_SOCKET", socket)
			} else {
				cfg, err := configfile.Load(beadsDir)
				if err != nil || cfg == nil {
					t.Fatalf("load metadata: %v", err)
				}
				cfg.DoltServerSocket = socket
				if err := cfg.Save(beadsDir); err != nil {
					t.Fatal(err)
				}
			}

			// The fakes refuse every login, so each check's open fails once
			// it has logged in. Dolt Mode's probe only reads the greeting,
			// so the fakes count its connections and record no login.
			for _, federation := range []struct {
				name  string
				check func(string) doctor.DoctorCheck
			}{
				{"Dolt Mode", doctor.CheckDoltServerModeMismatch},
				{"Dolt Remote Migration", doctor.CheckLegacyCLIRemotes},
				{"Federation remotesapi", doctor.CheckFederationRemotesAPI},
				{"Peer Connectivity", doctor.CheckFederationPeerConnectivity},
				{"Sync Staleness", doctor.CheckFederationSyncStaleness},
				{"Federation Conflicts", doctor.CheckFederationConflicts},
			} {
				loginsBefore, connsBefore := len(socketServer.Logins()), tcp.Conns()
				check := federation.check(repo)
				report := fmt.Sprintf("%s: %s %s\n%s", federation.name, check.Status, check.Message, check.Detail)
				if len(socketServer.Logins()) == loginsBefore {
					t.Errorf("the %s check never opened %s, the server the Dolt Mode probe reached:\n%s", federation.name, socket, report)
				}
				if n := tcp.Conns() - connsBefore; n != 0 {
					t.Errorf("the %s check made %d connection(s) to the server at port %d; the Dolt Mode probe reached the socket's server %s:\n%s", federation.name, n, tcp.port(), socket, report)
				}
			}
		})
	}
}

// serverModeWorkspaceAt makes repo a server-mode workspace with no port named
// and returns its .beads directory.
func serverModeWorkspaceAt(t *testing.T, repo string) string {
	t.Helper()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "fix_endpoint"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

// diagnosticSocketPath is an unused unix socket path short enough to bind: socket
// paths are capped near 104 bytes, and t.TempDir() nests too deeply under
// some runners.
func diagnosticSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "dolt.sock")
}

// newMuteListenerOn listens on network and addr (a unix socket path for
// "unix") and accepts every connection without ever writing to it, as a
// stopped or paused sql-server does: a dial succeeds and the MySQL greeting
// never comes. The listener and every connection it accepted close with t,
// which ends a read still waiting on one.
func newMuteListenerOn(t *testing.T, network, addr string) {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	closed := false
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				_ = conn.Close()
			} else {
				conns = append(conns, conn)
			}
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		closed = true
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
}

// credentialEntryPointCommands are the bd commands that log in to a
// workspace's Dolt server, each of which must present the password the store
// presents there.
var credentialEntryPointCommands = [][]string{
	{"list"},
	{"dolt", "status"},
	{"doctor"},
	// The lightweight check git hooks run.
	{"doctor", "--check-health"},
	// Its first step, the metadata reconcile, reads the server's catalog
	// (bd doctor --fix runs the same reconcile).
	{"bootstrap", "--dry-run"},
}

// shOnlyPath is a PATH holding only sh, so dolt stays unfound and no bd
// command starts a server of its own.
func shOnlyPath(t *testing.T) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(sh, filepath.Join(dir, "sh")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDoltEntryPointsPresentTheStoresCredentials observes, on the wire, the
// password each entry point presents to a workspace's Dolt server: a
// listener speaking the MySQL handshake records every login. The workspace's
// server is named only by the legacy BEADS_DOLT_PORT and its password only
// by the credentials-file entry for that port. Every command must log in as
// BEADS_DOLT_SERVER_USER with that entry's password. On main the store
// looked up the password before BEADS_DOLT_PORT replaced the port (#7804
// review), so bd list and bd doctor's store checks presented none, and bd
// dolt status, bd doctor's direct SQL checks, bd doctor --check-health and
// bd bootstrap's metadata reconcile resolved port 0 without BEADS_DOLT_PORT
// and never logged in.
func TestDoltEntryPointsPresentTheStoresCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the PATH fixture is a POSIX shell")
	}
	bd := buildBDForInitTests(t)
	for _, tc := range []struct {
		name    string
		credCmd string
		user    string
	}{
		{name: "static user", user: "beads"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeMySQLLogin(t)
			port := fake.port()
			password := fmt.Sprintf("pw-%d", port)
			root, _, env := externallyManagedStartWorkspace(t, "false")
			credentials := filepath.Join(t.TempDir(), "credentials")
			if err := os.WriteFile(credentials, []byte(fmt.Sprintf("[127.0.0.1:%d]\npassword=%s\n", port, password)), 0o600); err != nil {
				t.Fatal(err)
			}
			env = append(env,
				"BEADS_DOLT_PORT="+strconv.Itoa(port),
				"BEADS_DOLT_SERVER_USER=beads",
				"BEADS_CREDENTIALS_FILE="+credentials,
				"PATH="+shOnlyPath(t))
			if tc.credCmd != "" {
				env = append(env, "BEADS_DOLT_CREDENTIAL_COMMAND="+tc.credCmd)
			}
			for _, args := range credentialEntryPointCommands {
				before := len(fake.Logins())
				cmd := exec.Command(bd, args...)
				cmd.Dir = root
				cmd.Env = env
				out, _ := cmd.CombinedOutput()
				name := "bd " + strings.Join(args, " ")
				logins := fake.Logins()[before:]
				if len(logins) == 0 {
					t.Errorf("%s never logged in to the server the store dials, port %d:\n%s", name, port, out)
				}
				for _, login := range logins {
					if login.User != tc.user || !fake.PresentsPassword(login, password) {
						t.Errorf("%s logged in as %q (password from the [127.0.0.1:%d] entry: %v), want %q with it:\n%s",
							name, login.User, port, fake.PresentsPassword(login, password), tc.user, out)
						break
					}
				}
			}
		})
	}
}
