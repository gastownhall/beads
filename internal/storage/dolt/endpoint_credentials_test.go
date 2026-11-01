package dolt

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// storeEndpointTestEnv clears the settings the store's endpoint reads and
// points the credentials file at one holding an entry per port in ports,
// whose password names its port.
func storeEndpointTestEnv(t *testing.T, hosts []string, ports ...int) string {
	t.Helper()
	for _, name := range []string{
		"BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_PASSWORD", "BEADS_DOLT_SERVER_USER", "BEADS_DOLT_SERVER_SOCKET",
		"BEADS_TEST_MODE", "BEADS_DOLT_CREDENTIAL_COMMAND", "BEADS_DOLT_SERVER_DATABASE",
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
	return t.TempDir()
}

// TestStoreOpenPresentsThePasswordForThePortItDials covers the library open
// (NewFromConfig*: bd doctor's store checks, routed stores, bootstrap's
// warmup, the Go SDK): the config New connects with must carry the
// credentials-file password for the [host:port] it dials. The password used
// to be looked up for the port resolved before applyConfigDefaults applied
// the legacy BEADS_DOLT_PORT, so with only that variable naming the port the
// open presented no password, or another port's (a port file's, the shared
// default 3308's), and the server refused it.
func TestStoreOpenPresentsThePasswordForThePortItDials(t *testing.T) {
	const legacy, portFile, envPort, callerPort = 13307, 14444, 13400, 13500
	for _, tc := range []struct {
		name       string
		legacyPort int
		envPort    int
		portFile   bool
		shared     bool
		callerHost string
		callerPort int
		callerSrc  doltserver.PortSource
		wantHost   string
		wantPort   int
	}{
		{name: "legacy BEADS_DOLT_PORT only", legacyPort: legacy, wantHost: "127.0.0.1", wantPort: legacy},
		{name: "legacy over the port file", legacyPort: legacy, portFile: true, wantHost: "127.0.0.1", wantPort: legacy},
		{name: "legacy over the shared default", legacyPort: legacy, shared: true, wantHost: "127.0.0.1", wantPort: legacy},
		{name: "BEADS_DOLT_SERVER_PORT", envPort: envPort, legacyPort: legacy, wantHost: "127.0.0.1", wantPort: envPort},
		{name: "port file", portFile: true, wantHost: "127.0.0.1", wantPort: portFile},
		{name: "caller host", legacyPort: legacy, callerHost: "db.example", wantHost: "db.example", wantPort: legacy},
		{name: "caller port-file port that legacy replaces", legacyPort: legacy, callerPort: portFile, callerSrc: doltserver.PortSourcePortFile, wantHost: "127.0.0.1", wantPort: legacy},
		{name: "caller-explicit port", legacyPort: legacy, callerPort: callerPort, wantHost: "127.0.0.1", wantPort: callerPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := storeEndpointTestEnv(t, []string{"127.0.0.1", "db.example"},
				legacy, portFile, envPort, callerPort, doltserver.DefaultSharedServerPort, configfile.DefaultDoltServerPort)
			if tc.legacyPort > 0 {
				t.Setenv("BEADS_DOLT_PORT", fmt.Sprint(tc.legacyPort))
			}
			if tc.envPort > 0 {
				t.Setenv("BEADS_DOLT_SERVER_PORT", fmt.Sprint(tc.envPort))
			}
			if tc.shared {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
			}
			if tc.portFile {
				if err := doltserver.EnsurePortFile(beadsDir, portFile); err != nil {
					t.Fatal(err)
				}
			}
			fileCfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "endpoint_creds"}
			cfg := &Config{ServerHost: tc.callerHost, ServerPort: tc.callerPort, ServerPortSource: tc.callerSrc}

			if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
				t.Fatalf("applyResolvedConfig: %v", err)
			}
			applyConfigDefaults(cfg)

			if cfg.ServerHost != tc.wantHost || cfg.ServerPort != tc.wantPort {
				t.Fatalf("store dials %s:%d, want %s:%d", cfg.ServerHost, cfg.ServerPort, tc.wantHost, tc.wantPort)
			}
			if want := fmt.Sprintf("%s-%d", tc.wantHost, tc.wantPort); cfg.ServerPassword != want {
				t.Errorf("store presents password %q at %s:%d, want %q, the entry for the port it dials",
					cfg.ServerPassword, cfg.ServerHost, cfg.ServerPort, want)
			}
		})
	}
}

// TestResolveServerPortLeavesTheTestModeGuardToNew pins that the store's port
// rule, which callers apply to compute a dial target outside New (bd init's
// clone), never carries New's BEADS_TEST_MODE port 1, and that New still
// applies the guard.
func TestResolveServerPortLeavesTheTestModeGuardToNew(t *testing.T) {
	storeEndpointTestEnv(t, nil)
	t.Setenv("BEADS_TEST_MODE", "1")
	cfg := &Config{BeadsDir: t.TempDir()}
	ResolveServerPort(cfg)
	if cfg.ServerPort != 0 {
		t.Errorf("ResolveServerPort with nothing naming a port = %d, want 0 (unresolved), not the test-mode guard's 1", cfg.ServerPort)
	}
	cfg = &Config{BeadsDir: t.TempDir(), Path: filepath.Join(t.TempDir(), "dolt")}
	applyConfigDefaults(cfg)
	if cfg.ServerPort != 1 {
		t.Errorf("applyConfigDefaults under BEADS_TEST_MODE = port %d, want the guard's 1", cfg.ServerPort)
	}
}

// TestEndpointBDStartsServerIsTheStoresAutoStartRule pins "a bd command will
// start this server" (doltserver.Endpoint.BDStartsServer), which bd
// bootstrap and the metadata reconcile ask when no port is resolved, to the
// store's own decision for the same workspace: the store config
// (applyResolvedConfig, with the socket the CLI open sets from
// metadata.json, ApplyCLIAutoStart) and serverOpenCanAutoStart. A rule of
// host and mode alone misses a gateway, whose credential command turns the
// store's auto-start off, and a socket, which the store never auto-starts.
func TestEndpointBDStartsServerIsTheStoresAutoStartRule(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the credential command below is a POSIX shell command")
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		meta configfile.Config
		want bool
	}{
		{name: "bd's own server", want: true},
		{name: "auto-start off", env: map[string]string{"BEADS_DOLT_AUTO_START": "0"}},
		{name: "BEADS_DOLT_SERVER_MODE=1", env: map[string]string{"BEADS_DOLT_SERVER_MODE": "1"}},
		{name: "gateway credential command", env: map[string]string{"BEADS_DOLT_CREDENTIAL_COMMAND": "printf tok-gw"}},
		{name: "socket in metadata.json", meta: configfile.Config{DoltServerSocket: "/tmp/beads-autostart-rule.sock"}},
		{name: "socket in the environment", env: map[string]string{"BEADS_DOLT_SERVER_SOCKET": "/tmp/beads-autostart-rule.sock"}},
		{name: "remote host", meta: configfile.Config{DoltServerHost: "db.example.com"}},
		{name: "IPv6 loopback", meta: configfile.Config{DoltServerHost: "::1"}},
		{name: "metadata.json dolt_server_port", meta: configfile.Config{DoltServerPort: 13999}},
		{name: "shared server", env: map[string]string{"BEADS_DOLT_SHARED_SERVER": "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := storeEndpointTestEnv(t, nil)
			t.Setenv("BEADS_DOLT_AUTO_START", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			meta := tc.meta
			meta.Backend, meta.DoltMode, meta.DoltDatabase = configfile.BackendDolt, configfile.DoltModeServer, "autostart_rule"
			if err := meta.Save(beadsDir); err != nil {
				t.Fatal(err)
			}
			fileCfg, err := configfile.Load(beadsDir)
			if err != nil {
				t.Fatal(err)
			}
			// The CLI open sets the socket from metadata.json
			// (resolveDoltServerConnection); New reads the environment's.
			cfg := &Config{ServerSocket: fileCfg.GetDoltServerSocket()}
			if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
				t.Fatalf("applyResolvedConfig: %v", err)
			}
			ApplyCLIAutoStart(beadsDir, cfg)
			applyConfigDefaults(cfg)
			store := serverOpenCanAutoStart(cfg) && !doltserver.IsAutoStartDisabled()
			ep := doltserver.ResolveEndpoint(beadsDir, fileCfg)
			if got := ep.BDStartsServer(); got != store || got != tc.want {
				t.Errorf("Endpoint.BDStartsServer() = %v, store auto-starts = %v, want both %v", got, store, tc.want)
			}
		})
	}
}

// TestStoreOpenReadsTheCredentialsFileOnce pins that the library open looks
// the password up once. Every read of the credentials file checks its
// permissions and warns about a loose file (configfile.readPasswordFromFile),
// so a second lookup would print that warning twice per open.
func TestStoreOpenReadsTheCredentialsFileOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the permissions warning is unix-only")
	}
	const port = 13317
	beadsDir := storeEndpointTestEnv(t, []string{"127.0.0.1"}, port)
	if err := os.Chmod(os.Getenv("BEADS_CREDENTIALS_FILE"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_SERVER_PORT", fmt.Sprint(port))
	fileCfg := &configfile.Config{Backend: configfile.BackendDolt, DoltMode: configfile.DoltModeServer, DoltDatabase: "endpoint_creds"}
	cfg := &Config{}

	stderr := captureOpenStderr(t, func() {
		if err := applyResolvedConfig(context.Background(), beadsDir, fileCfg, cfg); err != nil {
			t.Fatalf("applyResolvedConfig: %v", err)
		}
	})
	if want := fmt.Sprintf("127.0.0.1-%d", port); cfg.ServerPassword != want {
		t.Fatalf("store presents password %q, want %q", cfg.ServerPassword, want)
	}
	if n := strings.Count(stderr, "overly permissive permissions"); n != 1 {
		t.Errorf("one store open warned about the credentials file %d times, want once:\n%s", n, stderr)
	}
}

// captureOpenStderr is what fn writes to os.Stderr. Not for parallel tests:
// os.Stderr is process-global.
func captureOpenStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = orig })
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	out := <-done
	_ = r.Close()
	return out
}
