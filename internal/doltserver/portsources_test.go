package doltserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// isolateUserConfig points the user-level config locations (HOME, XDG, and
// USERPROFILE) at an empty directory. config.Initialize merges the
// user-level configs (~/.config/bd/config.yaml, legacy ~/.beads/config.yaml),
// and the integration TestMain does not redirect HOME, so without this an
// operator's dolt.shared-server: true switches DefaultConfig to the shared
// server's state dir and its port file. It also drops any config an earlier
// test in the run loaded (from the real HOME), and does so again afterwards.
func isolateUserConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
}

// Asserts the precedence chain against DefaultConfig itself by layering each
// source on top of the previous ones: env > port file > Dolt server
// config.yaml > Beads config.yaml > metadata.json (GH#4511).
func TestDefaultConfigPrecedenceChain(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	isolateUserConfig(t)

	beadsDir := t.TempDir()

	// 1. Nothing configured: ephemeral (port 0).
	if got := DefaultConfig(beadsDir).Port; got != 0 {
		t.Fatalf("no source configured: port = %d, want 0", got)
	}

	// 2. metadata.json dolt_server_port is the lowest-priority source.
	writeMetadataPort(t, beadsDir, 5001)
	if got := DefaultConfig(beadsDir).Port; got != 5001 {
		t.Fatalf("metadata.json only: port = %d, want 5001", got)
	}

	// 3. Beads config.yaml (dolt.port) must beat metadata.json.
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("dolt.port: 5002\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", configDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
	if got := DefaultConfig(beadsDir).Port; got != 5002 {
		t.Fatalf("beads config.yaml + metadata.json: port = %d, want 5002 (yaml over metadata)", got)
	}

	// 4. Dolt server's own config.yaml (listener.port, in the dolt data dir)
	// must beat Beads config.yaml — these are two distinct sources with
	// distinct precedence, not one collapsed entry.
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDir, "config.yaml"), []byte("listener:\n  host: 127.0.0.1\n  port: 5003\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DefaultConfig(beadsDir).Port; got != 5003 {
		t.Fatalf("+ dolt server config.yaml: port = %d, want 5003 (dolt yaml over beads yaml)", got)
	}

	// 5. The port file must beat both YAML sources.
	if err := writePortFile(beadsDir, 5004); err != nil {
		t.Fatal(err)
	}
	if got := DefaultConfig(beadsDir).Port; got != 5004 {
		t.Fatalf("+ port file: port = %d, want 5004 (port file over both yaml sources)", got)
	}

	// 6. The env var override must beat everything, including the port file.
	t.Setenv("BEADS_DOLT_SERVER_PORT", "5005")
	if got := DefaultConfig(beadsDir).Port; got != 5005 {
		t.Fatalf("+ env var: port = %d, want 5005 (env over port file)", got)
	}
}

// TestDefaultConfigPortSource asserts that DefaultConfig records which step
// of the precedence chain resolved Port, layering sources the same way
// TestDefaultConfigPrecedenceChain does. Auto-start (GH#4052) uses PortSource
// to decide whether silently retargeting a stale port is safe (bd's own
// port-file bookkeeping) or not (a source where the user, or config on the
// user's behalf, explicitly asserted the port).
func TestDefaultConfigPortSource(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	isolateUserConfig(t)

	beadsDir := t.TempDir()

	// 1. Nothing configured: unset source, port 0.
	cfg := DefaultConfig(beadsDir)
	if cfg.Port != 0 || cfg.PortSource != PortSourceUnset {
		t.Fatalf("no source configured: port=%d source=%q, want 0/%q", cfg.Port, cfg.PortSource, PortSourceUnset)
	}

	// 2. metadata.json dolt_server_port (deprecated fallback).
	writeMetadataPort(t, beadsDir, 5001)
	cfg = DefaultConfig(beadsDir)
	if cfg.Port != 5001 || cfg.PortSource != PortSourceMetadataJSON {
		t.Fatalf("metadata.json only: port=%d source=%q, want 5001/%q", cfg.Port, cfg.PortSource, PortSourceMetadataJSON)
	}

	// 3. Beads config.yaml (dolt.port) — global/project config source.
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("dolt.port: 5002\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", configDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
	cfg = DefaultConfig(beadsDir)
	if cfg.Port != 5002 || cfg.PortSource != PortSourceConfigYaml {
		t.Fatalf("beads config.yaml: port=%d source=%q, want 5002/%q", cfg.Port, cfg.PortSource, PortSourceConfigYaml)
	}
	if cfg.PortSource != PortSourceGlobalConfig {
		t.Fatalf("PortSourceGlobalConfig alias diverged from PortSourceConfigYaml: %q vs %q", PortSourceGlobalConfig, PortSourceConfigYaml)
	}

	// 4. Dolt server's own config.yaml (listener.port).
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDir, "config.yaml"), []byte("listener:\n  host: 127.0.0.1\n  port: 5003\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = DefaultConfig(beadsDir)
	if cfg.Port != 5003 || cfg.PortSource != PortSourceDoltConfigYaml {
		t.Fatalf("dolt server config.yaml: port=%d source=%q, want 5003/%q", cfg.Port, cfg.PortSource, PortSourceDoltConfigYaml)
	}

	// 5. Port file — bd's own bookkeeping, not authoritative.
	if err := writePortFile(beadsDir, 5004); err != nil {
		t.Fatal(err)
	}
	cfg = DefaultConfig(beadsDir)
	if cfg.Port != 5004 || cfg.PortSource != PortSourcePortFile {
		t.Fatalf("port file: port=%d source=%q, want 5004/%q", cfg.Port, cfg.PortSource, PortSourcePortFile)
	}
	if cfg.PortSource.IsAuthoritative() {
		t.Fatalf("port file source must not be authoritative (bd's own bookkeeping)")
	}

	// 6. Env var — highest precedence, authoritative.
	t.Setenv("BEADS_DOLT_SERVER_PORT", "5005")
	cfg = DefaultConfig(beadsDir)
	if cfg.Port != 5005 || cfg.PortSource != PortSourceEnv {
		t.Fatalf("env var: port=%d source=%q, want 5005/%q", cfg.Port, cfg.PortSource, PortSourceEnv)
	}
	if !cfg.PortSource.IsAuthoritative() {
		t.Fatalf("env var source must be authoritative")
	}
}

// TestDefaultConfigPortSharedServer asserts that DefaultConfig sets
// PortSharedServer whenever port resolution happened in shared-server mode
// (BEADS_DOLT_SHARED_SERVER=1) — both when a source resolves a port from the
// shared server directory, and when no source resolves one and the fixed
// DefaultSharedServerPort fallback applies — and that it stays false
// otherwise. newServerMode's auto-start fail-closed check (GH#4052) relies
// on this: in shared-server mode, retargeting always means auto-start spun
// up a repo-local server distinct from the shared one, regardless of
// PortSource.IsAuthoritative().
func TestDefaultConfigPortSharedServer(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	// Per-project mode: never shared, regardless of source.
	beadsDir := t.TempDir()
	if cfg := DefaultConfig(beadsDir); cfg.PortSharedServer {
		t.Fatalf("per-project mode, no port configured: PortSharedServer = true, want false")
	}
	writeMetadataPort(t, beadsDir, 6001)
	if cfg := DefaultConfig(beadsDir); cfg.PortSharedServer {
		t.Fatalf("per-project mode, metadata.json port: PortSharedServer = true, want false")
	}

	// Shared-server mode, isolated to a temp shared-server dir so this test
	// doesn't touch the real ~/.beads/shared-server.
	sharedDir := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	// No port source resolves in the shared dir: falls back to the fixed
	// shared server port.
	cfg := DefaultConfig(t.TempDir())
	if cfg.Port != DefaultSharedServerPort {
		t.Fatalf("shared mode fallback: port = %d, want %d", cfg.Port, DefaultSharedServerPort)
	}
	if cfg.PortSource != PortSourceSharedServerDefault {
		t.Fatalf("shared mode fallback: source = %q, want %q", cfg.PortSource, PortSourceSharedServerDefault)
	}
	// Sourced, but deliberately NOT authoritative: bd picked 3308 on the
	// user's behalf. Callers that copy this Port into dolt.Config need a
	// non-empty source (or applyConfigDefaults infers caller_explicit), and
	// they need it to answer false here (or auto-start defends a port the
	// user never configured).
	if cfg.PortSource.IsAuthoritative() {
		t.Fatalf("shared mode fallback: source %q reports authoritative, want non-authoritative", cfg.PortSource)
	}
	if !cfg.PortSharedServer {
		t.Fatalf("shared mode fallback: PortSharedServer = false, want true")
	}

	// A source resolves within the shared dir (metadata.json in this case):
	// still shared, and PortSource still reports the resolving source.
	writeMetadataPort(t, sharedDir, 6002)
	cfg = DefaultConfig(t.TempDir())
	if cfg.Port != 6002 || cfg.PortSource != PortSourceMetadataJSON {
		t.Fatalf("shared mode, source resolved: port=%d source=%q, want 6002/%q", cfg.Port, cfg.PortSource, PortSourceMetadataJSON)
	}
	if !cfg.PortSharedServer {
		t.Fatalf("shared mode, source resolved: PortSharedServer = false, want true")
	}
}

func writeMetadataPort(t *testing.T, beadsDir string, port int) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"dolt_server_port": port})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestResolversLeaveLegacyConfigJSONUnchanged pins that doltserver's
// resolvers never migrate a workspace's legacy config.json. They run for
// workspaces bd was not launched in (beads.Open of another path reaches
// DefaultConfig through the Dolt constructor; DefaultConfigForMode serves
// diagnostics of another workspace; EnsureRunning's error path reads the
// host) before anything has admitted them, so a migrating configfile.Load
// there rewrote a pre-Dolt workspace's config.json into metadata.json,
// dropping the keys Config does not know. A resolver that reads the legacy
// file still sees its settings in place.
func TestResolversLeaveLegacyConfigJSONUnchanged(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_DATA_DIR", "")
	isolateUserConfig(t)

	const legacy = `{"database":"beads.db","version":"0.20.1","jsonl_export":"issues.jsonl","dolt_mode":"server","dolt_server_host":"db.example.internal","dolt_server_port":5009}`
	resolvers := map[string]func(t *testing.T, beadsDir string){
		"DefaultConfig": func(t *testing.T, beadsDir string) {
			if got := DefaultConfig(beadsDir).Port; got != 5009 {
				t.Errorf("DefaultConfig port = %d, want config.json's 5009 read in place", got)
			}
		},
		"DialConfig": func(_ *testing.T, beadsDir string) { _ = DialConfig(beadsDir) },
		"DefaultConfigForMode/owned": func(t *testing.T, beadsDir string) {
			if got := DefaultConfigForMode(beadsDir, false).Port; got != 5009 {
				t.Errorf("DefaultConfigForMode port = %d, want config.json's 5009 read in place", got)
			}
		},
		"DefaultConfigForMode/shared": func(_ *testing.T, beadsDir string) { _ = DefaultConfigForMode(beadsDir, true) },
		"externalNonLocalhostHost": func(t *testing.T, beadsDir string) {
			if host, ok := externalNonLocalhostHost(beadsDir); !ok || host != "db.example.internal" {
				t.Errorf("externalNonLocalhostHost = %q, %v, want config.json's host read in place", host, ok)
			}
		},
		"ResolveServerMode":        func(_ *testing.T, beadsDir string) { _ = ResolveServerMode(beadsDir) },
		"ResolveRemotesAPIPort":    func(_ *testing.T, beadsDir string) { _ = ResolveRemotesAPIPort(beadsDir) },
		"ResolveDoltDir":           func(_ *testing.T, beadsDir string) { _ = ResolveDoltDir(beadsDir) },
		"ResolvePhysicalRoots":     func(_ *testing.T, beadsDir string) { _, _ = ResolvePhysicalRoots(beadsDir) },
		"LibraryOpenRootPath":      func(_ *testing.T, beadsDir string) { _ = LibraryOpenRootPath(beadsDir) },
		"ExternalPortSetting":      func(_ *testing.T, beadsDir string) { _ = ExternalPortSetting(beadsDir, PortSourceMetadataJSON, 5009) },
		"IsSharedServerModeForDir": func(_ *testing.T, beadsDir string) { _ = IsSharedServerModeForDir(beadsDir) },
	}
	for name, resolve := range resolvers {
		t.Run(name, func(t *testing.T) {
			beadsDir := t.TempDir()
			legacyPath := filepath.Join(beadsDir, "config.json")
			if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
				t.Fatal(err)
			}
			resolve(t, beadsDir)
			entries, err := os.ReadDir(beadsDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "config.json" {
				names := make([]string, 0, len(entries))
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Fatalf("%s changed the workspace: entries %v, want only config.json", name, names)
			}
			if data, err := os.ReadFile(legacyPath); err != nil || string(data) != legacy {
				t.Fatalf("%s rewrote config.json: %q, %v", name, data, err)
			}
		})
	}
}

// TestCentralConfigPortIsTheLastPortStep pins GH#3895's port step: the central
// server config's dolt_server_port (server.json, or the file
// BEADS_CENTRAL_CONFIG names) is the port DialConfig and ResolveEndpoint
// resolve when no other source names one, with source central_config. Every
// other source still wins, the legacy BEADS_DOLT_PORT does not replace it, and
// the password is the credentials-file entry for that host and port.
func TestCentralConfigPortIsTheLastPortStep(t *testing.T) {
	const centralPort = 5100
	setup := func(t *testing.T, centralBody string) string {
		t.Helper()
		beadsDir := endpointTestEnv(t, map[string]string{
			"db.invalid:5100": "central-5100",
			"db.invalid:3307": "wrong-3307",
			"db.invalid:0":    "wrong-0",
		})
		t.Setenv("BEADS_DIR", "")
		writeEndpointMetadata(t, beadsDir, &configfile.Config{DoltMode: configfile.DoltModeServer, DoltDatabase: "central"})
		centralPath := filepath.Join(t.TempDir(), "server.json")
		if err := os.WriteFile(centralPath, []byte(centralBody), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_CENTRAL_CONFIG", centralPath)
		return beadsDir
	}
	const hostAndPort = `{"dolt_server_host":"db.invalid","dolt_server_port":5100}`
	const portOnly = `{"dolt_server_port":5100}`

	t.Run("fills a port nothing else names", func(t *testing.T) {
		beadsDir := setup(t, hostAndPort)
		cfg := DialConfig(beadsDir)
		if cfg.Port != centralPort || cfg.PortSource != PortSourceCentralConfig {
			t.Errorf("DialConfig port=%d source=%q, want %d/%q", cfg.Port, cfg.PortSource, centralPort, PortSourceCentralConfig)
		}
		if got := DefaultConfigForMode(beadsDir, false); got.Port != centralPort || got.PortSource != PortSourceCentralConfig {
			t.Errorf("DefaultConfigForMode port=%d source=%q, want %d/%q", got.Port, got.PortSource, centralPort, PortSourceCentralConfig)
		}
		ep := ResolveEndpoint(beadsDir, nil)
		if ep.Host != "db.invalid" || ep.Port != centralPort {
			t.Errorf("ResolveEndpoint = %s:%d, want db.invalid:%d", ep.Host, ep.Port, centralPort)
		}
		if got := ep.Password(); got != "central-5100" {
			t.Errorf("password = %q, want the [db.invalid:5100] entry", got)
		}
	})

	t.Run("legacy BEADS_DOLT_PORT does not replace it", func(t *testing.T) {
		beadsDir := setup(t, hostAndPort)
		t.Setenv("BEADS_DOLT_PORT", "5200")
		if cfg := DialConfig(beadsDir); cfg.Port != centralPort || cfg.PortSource != PortSourceCentralConfig {
			t.Errorf("DialConfig port=%d source=%q, want %d/%q", cfg.Port, cfg.PortSource, centralPort, PortSourceCentralConfig)
		}
	})

	for _, tc := range []struct {
		name   string
		apply  func(t *testing.T, beadsDir string)
		port   int
		source PortSource
	}{
		{"BEADS_DOLT_SERVER_PORT", func(t *testing.T, _ string) { t.Setenv("BEADS_DOLT_SERVER_PORT", "5201") }, 5201, PortSourceEnv},
		{"bd-host port file", func(t *testing.T, beadsDir string) {
			if err := writePortFile(beadsDir, 5202); err != nil {
				t.Fatal(err)
			}
		}, 5202, PortSourcePortFile},
		{"listener.port", func(t *testing.T, beadsDir string) {
			doltDir := filepath.Join(beadsDir, "dolt")
			if err := os.MkdirAll(doltDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(doltDir, "config.yaml"), []byte("listener:\n  host: 127.0.0.1\n  port: 5203\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, 5203, PortSourceDoltConfigYaml},
		{"dolt.port", func(t *testing.T, _ string) {
			configDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("dolt.port: 5204\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BEADS_DIR", configDir)
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}
		}, 5204, PortSourceConfigYaml},
		{"metadata.json dolt_server_port", func(t *testing.T, beadsDir string) {
			writeEndpointMetadata(t, beadsDir, &configfile.Config{DoltMode: configfile.DoltModeServer, DoltDatabase: "central", DoltServerPort: 5205})
		}, 5205, PortSourceMetadataJSON},
	} {
		t.Run(tc.name+" wins", func(t *testing.T) {
			beadsDir := setup(t, portOnly)
			tc.apply(t, beadsDir)
			if cfg := DialConfig(beadsDir); cfg.Port != tc.port || cfg.PortSource != tc.source {
				t.Errorf("DialConfig port=%d source=%q, want %d/%q", cfg.Port, cfg.PortSource, tc.port, tc.source)
			}
			if ep := ResolveEndpoint(beadsDir, nil); ep.Port != tc.port {
				t.Errorf("ResolveEndpoint port=%d, want %d", ep.Port, tc.port)
			}
		})
	}

	t.Run("shared-server mode keeps its own default", func(t *testing.T) {
		beadsDir := setup(t, portOnly)
		t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
		if cfg := DialConfig(beadsDir); cfg.Port != DefaultSharedServerPort {
			t.Errorf("shared mode DialConfig port=%d source=%q, want %d", cfg.Port, cfg.PortSource, DefaultSharedServerPort)
		}
		if cfg := DefaultConfigForMode(beadsDir, true); cfg.Port != DefaultSharedServerPort {
			t.Errorf("shared mode DefaultConfigForMode port=%d source=%q, want %d", cfg.Port, cfg.PortSource, DefaultSharedServerPort)
		}
	})

	t.Run("bd dolt show lists it last", func(t *testing.T) {
		labels := PortSourceLabels()
		if last := labels[len(labels)-1]; !strings.Contains(last, "server.json") {
			t.Errorf("last port source label = %q, want the central server config (server.json)", last)
		}
	})
}
