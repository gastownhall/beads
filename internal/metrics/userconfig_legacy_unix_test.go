//go:build unix

package metrics

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO at the legacy path is refused by the stat before any open, so the
// bootstrap neither blocks on it nor treats it as missing: it fails closed.
func TestLegacyDarwinCarryOver_FIFOFailsClosedWithoutBlocking(t *testing.T) {
	home := setupUserConfigHome(t)
	notice := captureCarryOverNotice(t)
	probes := useLegacyCarryOver(t, "darwin")
	legacy := legacyDarwinConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := syscall.Mkfifo(legacy, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- EnsureUserConfigDefaults() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureUserConfigDefaults: %v", err)
		}
	case <-time.After(10 * time.Second):
		// Unblock the stuck open so the goroutine can finish.
		if f, err := os.OpenFile(legacy, os.O_WRONLY, 0); err == nil {
			_ = f.Close()
		}
		t.Fatal("EnsureUserConfigDefaults blocked on a FIFO at the legacy path")
	}
	assertLegacyProbes(t, probes, home, 1)
	assertFailedClosed(t, home, notice)
}

// An existing user config whose directory refuses new files cannot be
// rewritten atomically, so the probe is skipped.
func TestLegacyDarwinCarryOver_UnwritableConfigDirNeverProbes(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home, "metrics:\n  disabled: true\n")
	path := userConfigPath(home)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("actor: unwritable-dir\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A lock file left by an earlier run means taking the lock no longer
	// needs a new file in dir: only the writability check stops the probe.
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatalf("seed lock: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	requireDirRefusesFiles(t, dir)

	for run := 1; run <= 2; run++ {
		if err := EnsureUserConfigDefaults(); err == nil {
			t.Fatalf("run %d: EnsureUserConfigDefaults returned nil for an unwritable config dir", run)
		}
	}
	assertLegacyProbes(t, probes, home, 0)
}

// A user config that does not exist yet, under a parent bd cannot create it
// in, never triggers the probe either.
func TestLegacyDarwinCarryOver_UncreatableConfigNeverProbes(t *testing.T) {
	home := setupUserConfigHome(t)
	probes := useLegacyCarryOver(t, "darwin")
	plantLegacyDarwinConfig(t, home, "metrics:\n  disabled: true\n")
	parent := filepath.Join(home, ".config")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	requireDirRefusesFiles(t, parent)

	for run := 1; run <= 2; run++ {
		if err := EnsureUserConfigDefaults(); err == nil {
			t.Fatalf("run %d: EnsureUserConfigDefaults returned nil under an unwritable parent", run)
		}
	}
	assertLegacyProbes(t, probes, home, 0)
}

func requireDirRefusesFiles(t *testing.T, dir string) {
	t.Helper()
	f, err := os.CreateTemp(dir, "probe-")
	if err == nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		t.Fatalf("precondition: %s still accepts new files (running as root?)", dir)
	}
}
