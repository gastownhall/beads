package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

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
// bd doctor through the binary for a server-mode workspace on 127.0.0.1 with
// auto-start off, whose server only the legacy BEADS_DOLT_PORT names. The
// store dials that port (bd list reaches the server there), so each command
// must name it, not report port 0 or dial 127.0.0.1:0. On main both resolved
// the port without the legacy variable (#7803 review): status printed
// "Port: 0" and doctor "no Dolt server port configured". BEADS_DOLT_SERVER_PORT
// is the control, which main already resolved.
func TestDoltDiagnosticsNameTheEndpointTheStoreDials(t *testing.T) {
	bd := buildBDForInitTests(t)
	for _, tc := range []struct {
		name      string
		autoStart string
		portVar   string
	}{
		{name: "legacy BEADS_DOLT_PORT, auto-start off", autoStart: "false", portVar: "BEADS_DOLT_PORT"},
		{name: "BEADS_DOLT_SERVER_PORT, auto-start off", autoStart: "false", portVar: "BEADS_DOLT_SERVER_PORT"},
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
