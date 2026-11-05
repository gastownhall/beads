package dolt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// externalPortAdviceEnv clears every setting that classifies a workspace's
// server, so each case below names exactly the one it is about.
func externalPortAdviceEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BEADS_SHARED_SERVER_DIR", filepath.Join(home, "shared-server"))
	for _, name := range []string{
		"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER", "BEADS_DOLT_SERVER_HOST",
		"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_AUTO_START", "BEADS_DOLT_SERVER_SOCKET",
	} {
		t.Setenv(name, "")
	}
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
}

// externalPortAdviceWorkspace writes a server-mode metadata.json, naming
// metadataPort as dolt_server_port when it is nonzero.
func externalPortAdviceWorkspace(t *testing.T, metadataPort int) string {
	t.Helper()
	beadsDir := t.TempDir()
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltDatabase = "advice"
	cfg.DoltServerPort = metadataPort
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

func deadLoopbackPort(t *testing.T) int {
	t.Helper()
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	return port
}

// TestNewServerModeExternallyManagedPortAdvice pins the advice for a dead
// endpoint whose port a setting names, which also declares the server
// externally managed: BEADS_DOLT_SERVER_PORT or BEADS_DOLT_PORT
// (ResolveServerMode check 2c), or metadata.json's dolt_server_port (check 4).
// bd starts no server for such a workspace, and `bd dolt start` would start
// its own over .beads/dolt on that port, so the advice names the endpoint that
// was dialed and the setting, says bd did not start a server, and never
// suggests `bd dolt start`. It must hold on both opens: the one that may not
// auto-start (every ordinary command, since ApplyCLIAutoStart turns
// auto-start off for an external server) and the one that may (bd init builds
// its config by hand), where EnsureRunning refuses instead of starting.
func TestNewServerModeExternallyManagedPortAdvice(t *testing.T) {
	cases := []struct {
		name         string
		envName      string // env var naming the port, or ""
		metadataPort bool
		source       doltserver.PortSource
		openedByInit bool
		setting      string
		otherPort    bool // metadataPort names a port other than the one dialed
	}{
		{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_SERVER_PORT", false, doltserver.PortSourceEnv, false, "BEADS_DOLT_SERVER_PORT", false},
		{"legacy BEADS_DOLT_PORT", "BEADS_DOLT_PORT", false, doltserver.PortSourceEnv, false, "BEADS_DOLT_PORT", false},
		{"metadata.json dolt_server_port", "", true, doltserver.PortSourceMetadataJSON, false, "metadata.json", false},
		// bd init promotes --server-port into BEADS_DOLT_SERVER_PORT and
		// passes the port itself as caller-explicit.
		{"bd init --server-port", "BEADS_DOLT_SERVER_PORT", false, doltserver.PortSourceCallerExplicit, true, "--server-port", false},
		// `bd dolt set port N --update-config` writes the port to both
		// config.yaml and metadata.json; config.yaml ranks first.
		{"config.yaml port equal to dolt_server_port", "", true, doltserver.PortSourceConfigYaml, false, "metadata.json", false},
		// The metadata port still makes the workspace external, but does not
		// name the endpoint that was dialed.
		{"config.yaml port other than dolt_server_port", "", true, doltserver.PortSourceConfigYaml, false, "metadata.json", true},
		// An authoritative config.yaml or listener.port outranks the legacy
		// BEADS_DOLT_PORT for the dial (applyConfigDefaults); the env var
		// naming the same port still declares the server external.
		{"config.yaml port equal to BEADS_DOLT_PORT", "BEADS_DOLT_PORT", false, doltserver.PortSourceConfigYaml, false, "BEADS_DOLT_PORT", false},
		{"listener.port equal to BEADS_DOLT_PORT", "BEADS_DOLT_PORT", false, doltserver.PortSourceDoltConfigYaml, false, "BEADS_DOLT_PORT", false},
		// A successful open records the port it reached in bd's port file,
		// which outranks dolt_server_port. No pid file of bd's stands beside
		// it, so it is not a server bd started.
		{"port file equal to dolt_server_port", "", true, doltserver.PortSourcePortFile, false, "metadata.json", false},
		{"port file other than dolt_server_port", "", true, doltserver.PortSourcePortFile, false, "metadata.json", true},
	}
	// "disabled" is auto-start off with dolt.auto-start pinned off too, the
	// shape a gc-managed city exports: the named advice must win over the
	// auto-start-disabled hint, whose remedy is `bd dolt start`.
	for _, autoStart := range []string{"false", "true", "disabled"} {
		for _, tc := range cases {
			t.Run(tc.name+"/auto-start="+autoStart, func(t *testing.T) {
				externalPortAdviceEnv(t)
				if autoStart == "disabled" {
					t.Setenv("BEADS_DOLT_AUTO_START", "0")
				}
				port := deadLoopbackPort(t)
				addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
				if tc.envName != "" {
					t.Setenv(tc.envName, strconv.Itoa(port))
				}
				metadataPort := 0
				if tc.metadataPort {
					metadataPort = port
				}
				if tc.otherPort {
					metadataPort = deadLoopbackPort(t)
					for metadataPort == port {
						metadataPort = deadLoopbackPort(t)
					}
				}
				beadsDir := externalPortAdviceWorkspace(t, metadataPort)
				cfg := &Config{
					Path:             filepath.Join(beadsDir, "dolt"),
					BeadsDir:         beadsDir,
					Database:         "advice",
					ServerHost:       "127.0.0.1",
					ServerPort:       port,
					ServerPortSource: tc.source,
					AutoStart:        autoStart == "true",
					OpenedByInit:     tc.openedByInit,
				}

				_, err := newServerMode(context.Background(), cfg)
				if err == nil {
					t.Fatalf("open of the dead endpoint %s succeeded", addr)
				}
				msg := err.Error()
				declared := tc.setting + " declares the Dolt server at " + addr + " externally managed"
				if tc.otherPort {
					declared = tc.setting + " declares this workspace's Dolt server externally managed, so bd did not start one at " + addr
				}
				wants := []string{declared, "did not start"}
				absent := []string{"bd dolt start", "auto-start failed", "auto-start is disabled"}
				switch {
				case tc.otherPort && tc.source == doltserver.PortSourcePortFile:
					// The port file outranks dolt_server_port: it is what to
					// remove, and removing dolt_server_port would not change
					// the port bd dials.
					wants = append(wants, fmt.Sprintf("bd took that port from %s, which records the port an earlier command reached and outranks dolt_server_port (%d)",
						filepath.Join(beadsDir, doltserver.PortFileName), metadataPort))
					absent = append(absent, "Removing dolt_server_port")
				case tc.otherPort:
					// Nothing else declares the server externally managed.
					wants = append(wants, "Removing dolt_server_port lets bd manage the server.")
				}
				for _, want := range wants {
					if !strings.Contains(msg, want) {
						t.Errorf("error does not contain %q:\n%s", want, msg)
					}
				}
				for _, banned := range absent {
					if strings.Contains(msg, banned) {
						t.Errorf("error contains %q:\n%s", banned, msg)
					}
				}
				var opErr *net.OpError
				if !errors.As(err, &opErr) {
					t.Errorf("error does not wrap the failed dial, so callers cannot tell it is one:\n%s", msg)
				}
				if _, statErr := os.Stat(filepath.Join(beadsDir, doltserver.PortFileName)); !os.IsNotExist(statErr) {
					t.Errorf("open wrote %s for a server it did not start (stat: %v)", doltserver.PortFileName, statErr)
				}
			})
		}
	}
}

// TestNewServerModeUnreachableAdviceOutsideNamedPorts pins the controls: an
// endpoint whose port no such setting names keeps its own advice, `bd dolt
// start` included, because bd may own that server. A server bd started (its
// pid file beside the port file), even on the port dolt_server_port names
// (`bd dolt set port N`, then `bd dolt start`), the shared-server port, and a
// bd-owned workspace with auto-start turned off.
func TestNewServerModeUnreachableAdviceOutsideNamedPorts(t *testing.T) {
	cases := []struct {
		name         string
		setup        func(t *testing.T, beadsDir string, port int)
		metadataPort bool // dolt_server_port names the dialed port
		source       doltserver.PortSource
		want         string
	}{
		{"bd's pid and port files beside dolt_server_port", func(t *testing.T, beadsDir string, port int) {
			// The server bd started is gone; its state files stay until
			// IsRunning or `bd dolt stop` clears them.
			for name, data := range map[string]string{
				doltserver.PortFileName: strconv.Itoa(port),
				doltserver.PIDFileName:  "99999999",
			} {
				if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}, true, doltserver.PortSourcePortFile, "The Dolt server may not be running"},
		{"shared server", func(t *testing.T, _ string, port int) {
			t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
			t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
		}, false, doltserver.PortSourceEnv, "The Dolt server may not be running"},
		{"owned, auto-start off", func(t *testing.T, _ string, _ int) {
			t.Setenv("BEADS_DOLT_AUTO_START", "0")
		}, false, doltserver.PortSourcePortFile, "auto-start is disabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			externalPortAdviceEnv(t)
			port := deadLoopbackPort(t)
			metadataPort := 0
			if tc.metadataPort {
				metadataPort = port
			}
			beadsDir := externalPortAdviceWorkspace(t, metadataPort)
			tc.setup(t, beadsDir, port)
			cfg := &Config{
				Path:             filepath.Join(beadsDir, "dolt"),
				BeadsDir:         beadsDir,
				Database:         "advice",
				ServerHost:       "127.0.0.1",
				ServerPort:       port,
				ServerPortSource: tc.source,
			}

			_, err := newServerMode(context.Background(), cfg)
			if err == nil {
				t.Fatal("open of a dead endpoint succeeded")
			}
			msg := err.Error()
			for _, want := range []string{tc.want, "bd dolt start"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error does not contain %q:\n%s", want, msg)
				}
			}
			if strings.Contains(msg, "externally managed") {
				t.Errorf("error calls a server bd may own externally managed:\n%s", msg)
			}
		})
	}
}

// cliServerConfig builds the config cmd/bd opens beadsDir's store with: the
// port resolved through ApplyResolvedServerPort and the auto-start policy
// through ApplyCLIAutoStart.
func cliServerConfig(beadsDir string) *Config {
	cfg := &Config{
		Path:       filepath.Join(beadsDir, "dolt"),
		BeadsDir:   beadsDir,
		Database:   "advice",
		ServerHost: "127.0.0.1",
	}
	ApplyResolvedServerPort(beadsDir, cfg)
	ApplyCLIAutoStart(beadsDir, cfg)
	return cfg
}

// TestNewServerModeNamedPortAdviceAfterASuccessfulOpen runs the sequence the
// port-file cases above take apart. A successful open of a dolt_server_port
// workspace records the port it reached in bd's port file
// (persistResolvedPortFile), so the next command resolves its port from that
// file, which outranks dolt_server_port, and then finds the server down. The
// advice must still name dolt_server_port, not `bd dolt start`: no pid file of
// bd's stands beside that port file, so it is not a server bd started.
func TestNewServerModeNamedPortAdviceAfterASuccessfulOpen(t *testing.T) {
	// "0" is the gc-city shape: dolt.auto-start pinned off as well.
	for _, autoStart := range []string{"", "0"} {
		t.Run("BEADS_DOLT_AUTO_START="+autoStart, func(t *testing.T) {
			externalPortAdviceEnv(t)
			t.Setenv("BEADS_DOLT_AUTO_START", autoStart)
			port := deadLoopbackPort(t)
			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			beadsDir := externalPortAdviceWorkspace(t, port)

			opened := cliServerConfig(beadsDir)
			if opened.ServerPort != port || opened.ServerPortSource != doltserver.PortSourceMetadataJSON || opened.AutoStart {
				t.Fatalf("first open = port %d from %s, auto-start %v; want port %d from metadata.json, auto-start off",
					opened.ServerPort, opened.ServerPortSource, opened.AutoStart, port)
			}
			// What newServerMode does once that open succeeds.
			if err := persistResolvedPortFile(opened, beadsDir); err != nil {
				t.Fatal(err)
			}

			cfg := cliServerConfig(beadsDir)
			if cfg.ServerPort != port || cfg.ServerPortSource != doltserver.PortSourcePortFile {
				t.Fatalf("next open = port %d from %s, want port %d from the port file the first open wrote",
					cfg.ServerPort, cfg.ServerPortSource, port)
			}
			_, err := newServerMode(context.Background(), cfg)
			if err == nil {
				t.Fatalf("open of the dead endpoint %s succeeded", addr)
			}
			msg := err.Error()
			for _, want := range []string{"metadata.json declares the Dolt server at " + addr + " externally managed", "did not start"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error does not contain %q:\n%s", want, msg)
				}
			}
			for _, banned := range []string{"bd dolt start", "auto-start is disabled"} {
				if strings.Contains(msg, banned) {
					t.Errorf("error contains %q:\n%s", banned, msg)
				}
			}
		})
	}
}

// TestDialConfigDialsTheStorePort pins doltserver.DialConfig, the port the
// diagnostics and EnsureRunning's refusal dial and name, to the port the
// store's open dials for the same legacy BEADS_DOLT_PORT: valid, padded or
// invalid, with or without a port file for it to replace.
func TestDialConfigDialsTheStorePort(t *testing.T) {
	for _, legacy := range []string{"13307", " 13307\n", "not-a-port"} {
		for _, portFile := range []bool{false, true} {
			t.Run(strconv.Quote(legacy)+"/port file "+strconv.FormatBool(portFile), func(t *testing.T) {
				externalPortAdviceEnv(t)
				// Test mode rewrites an unset or production port to 1.
				t.Setenv("BEADS_TEST_MODE", "")
				t.Setenv("BEADS_DOLT_PORT", legacy)
				beadsDir := externalPortAdviceWorkspace(t, 0)
				if portFile {
					if err := os.WriteFile(filepath.Join(beadsDir, doltserver.PortFileName), []byte("14444"), 0o600); err != nil {
						t.Fatal(err)
					}
				}

				store := &Config{BeadsDir: beadsDir, Database: "advice"}
				ApplyResolvedServerPort(beadsDir, store)
				applyConfigDefaults(store)
				dial := doltserver.DialConfig(beadsDir)
				if dial.Port != store.ServerPort || dial.PortSource != store.ServerPortSource {
					t.Errorf("DialConfig = port %d from %s, store dials port %d from %s",
						dial.Port, dial.PortSource, store.ServerPort, store.ServerPortSource)
				}
			})
		}
	}
}

// TestRemoteHostStoreDialsTheAuthoritativePort pins the store's dial of a
// server on another machine to #5835's rule for a local one: metadata.json's
// dolt_server_port outranks the legacy BEADS_DOLT_PORT, valid or padded, and
// DialConfig, which the diagnostics dial, reports the same port.
func TestRemoteHostStoreDialsTheAuthoritativePort(t *testing.T) {
	const metadataPort = 15555
	for _, legacy := range []string{"13307", " 13307\n"} {
		t.Run(strconv.Quote(legacy), func(t *testing.T) {
			externalPortAdviceEnv(t)
			// Test mode rewrites an unset or production port to 1.
			t.Setenv("BEADS_TEST_MODE", "")
			t.Setenv("BEADS_DOLT_SERVER_HOST", "db.example.com")
			t.Setenv("BEADS_DOLT_PORT", legacy)
			beadsDir := externalPortAdviceWorkspace(t, metadataPort)

			store := &Config{BeadsDir: beadsDir, Database: "advice"}
			ApplyResolvedServerPort(beadsDir, store)
			applyConfigDefaults(store)
			if store.ServerHost != "db.example.com" || store.ServerPort != metadataPort || store.ServerPortSource != doltserver.PortSourceMetadataJSON {
				t.Errorf("store dials %s:%d from %s, want db.example.com:%d from %s",
					store.ServerHost, store.ServerPort, store.ServerPortSource, metadataPort, doltserver.PortSourceMetadataJSON)
			}
			if dial := doltserver.DialConfig(beadsDir); dial.Port != store.ServerPort || dial.PortSource != store.ServerPortSource {
				t.Errorf("DialConfig = port %d from %s, store dials port %d from %s",
					dial.Port, dial.PortSource, store.ServerPort, store.ServerPortSource)
			}
		})
	}
}

// TestNewDialsAnIPv6HostInEitherSpelling pins the address the store dials for
// an IPv6 server host, configured bare ("::1") or bracketed ("[::1]"), on the
// caller's config or in BEADS_DOLT_SERVER_HOST: the fail-fast probe and the
// connection both reach the server at [::1]:port. The listener speaks no
// MySQL, so the open fails, but only after both dials arrived.
func TestNewDialsAnIPv6HostInEitherSpelling(t *testing.T) {
	for _, tc := range []struct{ name, host, env string }{
		{"bare", "::1", ""},
		{"bracketed", "[::1]", ""},
		{"bracketed BEADS_DOLT_SERVER_HOST", "", "[::1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			externalPortAdviceEnv(t)
			t.Setenv("BEADS_DOLT_SERVER_HOST", tc.env)
			port, accepted := testutil.IPv6LoopbackListener(t)
			beadsDir := t.TempDir()
			cfg := &Config{
				Path:       filepath.Join(beadsDir, "dolt"),
				BeadsDir:   beadsDir,
				Database:   "ipv6_host",
				ServerHost: tc.host,
				ServerPort: port,
			}

			_, err := New(context.Background(), cfg)
			if err == nil {
				t.Fatal("open of a listener that speaks no MySQL succeeded")
			}
			for deadline := time.Now().Add(2 * time.Second); accepted() < 2 && time.Now().Before(deadline); {
				time.Sleep(10 * time.Millisecond)
			}
			if n := accepted(); n < 2 {
				t.Errorf("the server at [::1]:%d took %d connections, want the fail-fast probe's and the connection's: %v", port, n, err)
			}
			for _, banned := range []string{"unreachable", "no such host", "too many colons", "missing port"} {
				if strings.Contains(err.Error(), banned) {
					t.Errorf("error contains %q:\n%v", banned, err)
				}
			}
		})
	}
}
