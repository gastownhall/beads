package doltserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// Asserts the precedence chain against DefaultConfig itself by layering each
// source on top of the previous ones: env > port file > Dolt server
// config.yaml > Beads config.yaml > metadata.json (GH#4511).
func TestDefaultConfigPrecedenceChain(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

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
	if cfg.PortSource != PortSourceUnset {
		t.Fatalf("shared mode fallback: source = %q, want %q", cfg.PortSource, PortSourceUnset)
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
