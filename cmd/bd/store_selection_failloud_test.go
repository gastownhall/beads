//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeCorruptMetadata creates a .beads dir whose metadata.json exists but
// cannot be parsed — the state a reader sees when the file is caught
// mid-rewrite (os.WriteFile truncate window) or hit by a transient read
// failure under load.
func writeCorruptMetadata(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_mode":"serv`), 0o600); err != nil {
		t.Fatalf("write corrupt metadata.json: %v", err)
	}
	return beadsDir
}

// A present-but-unloadable metadata.json must be a hard error, never a
// silent fall-through to the embedded store. In managed server-mode
// deployments the embedded directory is an empty relic, so the silent
// fallback answers every query with an empty result set and exit 0 —
// callers read "no work" where the real store has rows (false-empty).
func TestNewDoltStoreFromConfigCorruptMetadataFailsLoud(t *testing.T) {
	beadsDir := writeCorruptMetadata(t)
	store, err := newDoltStoreFromConfig(context.Background(), beadsDir)
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("newDoltStoreFromConfig: want error for corrupt metadata.json, got nil (silent embedded fallback)")
	}
}

func TestNewReadOnlyStoreFromConfigCorruptMetadataFailsLoud(t *testing.T) {
	beadsDir := writeCorruptMetadata(t)
	store, err := newReadOnlyStoreFromConfig(context.Background(), beadsDir)
	if err == nil {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("newReadOnlyStoreFromConfig: want error for corrupt metadata.json, got nil (silent embedded fallback)")
	}
}

// loadServerModeFromBeadsDir feeds the serverMode globals that the primary
// store-init path consults; a swallowed load failure leaves serverMode=false
// and routes data commands to the embedded store. The error must surface.
func TestLoadServerModeFromBeadsDirCorruptMetadataReturnsError(t *testing.T) {
	beadsDir := writeCorruptMetadata(t)
	if err := loadServerModeFromBeadsDir(beadsDir); err == nil {
		t.Fatal("loadServerModeFromBeadsDir: want error for corrupt metadata.json, got nil")
	}
}

// End-to-end contract for a corrupt metadata.json, exercised through the
// real binary: diagnostic and repair commands still run (warn-and-continue),
// data commands fail loud instead of answering false-empty from the embedded
// fallback, and bd init can rewrite the file — after which data commands work
// again. Guards the scoping of the fail-loud behavior: fatal only where a
// store is actually selected, never on the repair path itself.
func TestCorruptMetadataDiagnosticsRunAndDataFailsLoud(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cm")

	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"dolt_mode":"serv`), 0o600); err != nil {
		t.Fatalf("corrupt metadata.json: %v", err)
	}

	run := func(args ...string) (string, error) {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// Diagnostic / repair-path commands must keep working.
	for _, args := range [][]string{{"version"}, {"doctor"}} {
		if out, err := run(args...); err != nil {
			t.Errorf("bd %s with corrupt metadata.json: want success, got %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	// Data commands must fail loud, naming the file — not answer false-empty.
	out, err := run("list", "--json")
	if err == nil {
		t.Fatalf("bd list with corrupt metadata.json: want loud failure, got success:\n%s", out)
	}
	if !strings.Contains(out, "metadata.json") {
		t.Fatalf("bd list error should name metadata.json:\n%s", out)
	}

	// bd init is the documented repair path: it must run, rewrite the file,
	// and restore data commands.
	runBDInit(t, bd, dir, "--prefix", "cm")
	if out, err := run("list", "--json"); err != nil {
		t.Fatalf("bd list after bd init repair: %v\n%s", err, out)
	}
}

// Absent metadata.json stays a legitimate fresh-repo default: no error.
func TestLoadServerModeFromBeadsDirAbsentMetadataIsFine(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := loadServerModeFromBeadsDir(beadsDir); err != nil {
		t.Fatalf("loadServerModeFromBeadsDir: want nil for absent metadata.json, got %v", err)
	}
}
