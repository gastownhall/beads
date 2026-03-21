package dolt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

func TestAutoStart_DisabledWithExternalMode(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")

	got := resolveAutoStart(false, "", ServerModeExternal)
	if got != false {
		t.Error("resolveAutoStart should return false when server mode is External")
	}
}

// TestAutoStart_ExternalMode_CallerOverrideIgnored verifies that even a caller
// requesting AutoStart=true is overridden when the server is externally managed.
func TestAutoStart_ExternalMode_CallerOverrideIgnored(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")

	got := resolveAutoStart(true, "", ServerModeExternal)
	if got != false {
		t.Error("resolveAutoStart should return false with External mode even when caller requests true")
	}
}

// TestAutoStart_EnvOverrideStillWins verifies that BEADS_DOLT_AUTO_START=0
// still takes precedence even in Owned mode (defense-in-depth).
func TestAutoStart_EnvOverrideStillWins(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	got := resolveAutoStart(true, "", ServerModeOwned) // owned mode, but env says no
	if got != false {
		t.Error("BEADS_DOLT_AUTO_START=0 should still disable auto-start in Owned mode")
	}
}

// TestAutoStartReleaseNoErrorWhenServerAlreadyStopped verifies that
// autoStartRelease does not return an error when the server is already
// gone, preventing false "failed to stop auto-started dolt server"
// warnings (GH#2670). Also verifies stale PID/port files are cleaned up.
func TestAutoStartReleaseNoErrorWhenServerAlreadyStopped(t *testing.T) {
	dir := t.TempDir()

	// Create stale PID/port files to verify cleanup
	pidFile := filepath.Join(dir, doltserver.PIDFileName)
	portFile := filepath.Join(dir, doltserver.PortFileName)
	if err := os.WriteFile(pidFile, []byte("999999999"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portFile, []byte("13307"), 0600); err != nil {
		t.Fatal(err)
	}

	// Simulate: acquire a reference, then release it when no server is running.
	autoStartAcquire(dir)
	t.Cleanup(func() {
		// Ensure global refcount is cleaned up even if test fails early.
		autoStartRefs.mu.Lock()
		delete(autoStartRefs.m, dir)
		autoStartRefs.mu.Unlock()
	})

	err := autoStartRelease(dir)
	if err != nil {
		t.Errorf("autoStartRelease should not error when server is already stopped, got: %v", err)
	}

	// Verify stale state files were cleaned up by Stop → cleanupStateFiles
	if _, statErr := os.Stat(pidFile); !os.IsNotExist(statErr) {
		t.Error("PID file should be removed after autoStartRelease on dead server")
	}
	if _, statErr := os.Stat(portFile); !os.IsNotExist(statErr) {
		t.Error("port file should be removed after autoStartRelease on dead server")
	}
}

// TestAutoStartReleaseNilMap verifies autoStartRelease returns nil
// when the refcount map was never initialized.
func TestAutoStartReleaseNilMap(t *testing.T) {
	// Save and clear the global map.
	autoStartRefs.mu.Lock()
	saved := autoStartRefs.m
	autoStartRefs.m = nil
	autoStartRefs.mu.Unlock()
	t.Cleanup(func() {
		autoStartRefs.mu.Lock()
		autoStartRefs.m = saved
		autoStartRefs.mu.Unlock()
	})

	err := autoStartRelease("/nonexistent")
	if err != nil {
		t.Errorf("expected nil when map is nil, got: %v", err)
	}
}

// TestAutoStartReleaseRefcountAboveZero verifies autoStartRelease
// doesn't call Stop when the refcount is still above zero.
func TestAutoStartReleaseRefcountAboveZero(t *testing.T) {
	dir := t.TempDir()

	// Acquire twice so the first release leaves refcount > 0.
	autoStartAcquire(dir)
	autoStartAcquire(dir)
	t.Cleanup(func() {
		autoStartRefs.mu.Lock()
		delete(autoStartRefs.m, dir)
		autoStartRefs.mu.Unlock()
	})

	err := autoStartRelease(dir)
	if err != nil {
		t.Errorf("expected nil when refcount > 0, got: %v", err)
	}

	// Verify refcount is now 1, not 0.
	autoStartRefs.mu.Lock()
	count := autoStartRefs.m[dir]
	autoStartRefs.mu.Unlock()
	if count != 1 {
		t.Errorf("expected refcount 1, got %d", count)
	}
}
