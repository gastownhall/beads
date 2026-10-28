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
// real binary on an embedded workspace. Since #4857 a metadata.json that
// exists but cannot be parsed fails safe wherever a workspace is selected,
// doctor and init included (TestDoctorRejectsCorruptMetadataBeforeSharedServerChecks
// pins doctor's shared-server half): binding the workspace warns, naming the
// file, bd version still runs, every workspace command refuses on the
// unparsable metadata instead of answering false-empty from the embedded
// fallback, nothing rewrites the file, and restoring it brings the data back.
func TestEmbeddedCorruptMetadataFailsLoudUntilRestored(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cm")
	bdCreate(t, bd, dir, "survives the corruption")

	metadataPath := filepath.Join(beadsDir, "metadata.json")
	original, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	corrupt := []byte(`{"dolt_mode":"serv`)
	if err := os.WriteFile(metadataPath, corrupt, 0o600); err != nil {
		t.Fatalf("corrupt metadata.json: %v", err)
	}

	// run keeps stdout and stderr apart. Binding the workspace prints the
	// same warning before every command, bd version included, so a check on
	// the whole output passes on that warning whatever the command itself
	// does: the warning is checked on its own line, and each refusal on its
	// Error line.
	run := func(args ...string) (stdout, stderr string, err error) {
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = bdEnv(dir)
		outBuf, errBuf, err := runCommandBuffers(t, cmd)
		return outBuf.String(), errBuf.String(), err
	}

	// checkWarning finds that warning by its own prefix, not as the first
	// warning: bdEnv passes on the caller's BD_OTEL_* variables, and
	// telemetry warns about a legacy one before the workspace is bound.
	checkWarning := func(cmdline, stderr string) {
		t.Helper()
		warning := firstLineWithPrefix(stderr, "warning: load ")
		for _, want := range []string{"metadata.json", "parsing config:", "no storage database was opened or modified"} {
			if !strings.Contains(warning, want) {
				t.Errorf("%s with corrupt metadata.json: warning %q missing %q\nstderr:\n%s", cmdline, warning, want, stderr)
			}
		}
	}

	// bd version needs no workspace: it runs, after the warning naming the
	// file.
	stdout, stderr, err := run("version")
	if err != nil {
		t.Errorf("bd version with corrupt metadata.json: want success, got %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	checkWarning("bd version", stderr)

	// Every command that selects the workspace refuses on the unparsable
	// metadata before opening or modifying storage, after the same warning:
	// the refusal does not name the file, so the warning is what tells the
	// user which file to repair.
	for _, args := range [][]string{
		{"doctor"},
		{"list", "--json"},
		// init's own refusal would offer --reinit-local, but the admission
		// guard refuses first, --reinit-local included (bd-p613k8); fixing
		// that may change this case.
		{"init", "--prefix", "cm", "--quiet", "--skip-hooks", "--skip-agents"},
	} {
		cmdline := "bd " + strings.Join(args, " ")
		stdout, stderr, err := run(args...)
		if err == nil {
			t.Errorf("%s with corrupt metadata.json: want a loud failure, got success\nstdout:\n%s\nstderr:\n%s", cmdline, stdout, stderr)
			continue
		}
		checkWarning(cmdline, stderr)
		if refusal := firstLineWithPrefix(stderr, "Error: "); !strings.Contains(refusal, "parsing config:") {
			t.Errorf("%s with corrupt metadata.json: want its Error line to refuse the unparsable metadata (parsing config:), got %q\nstdout:\n%s\nstderr:\n%s", cmdline, refusal, stdout, stderr)
		}
	}
	if after, err := os.ReadFile(metadataPath); err != nil || string(after) != string(corrupt) {
		t.Fatalf("a refused command rewrote metadata.json (err %v):\nbefore: %q\nafter:  %q", err, corrupt, after)
	}

	// Restoring the file is the repair, and the data was never touched.
	if err := os.WriteFile(metadataPath, original, 0o600); err != nil {
		t.Fatalf("restore metadata.json: %v", err)
	}
	stdout, stderr, err = run("list", "--json")
	if err != nil {
		t.Fatalf("bd list after restoring metadata.json: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "survives the corruption") {
		t.Fatalf("bd list after restoring metadata.json lost the issue created before the corruption:\n%s", stdout)
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
