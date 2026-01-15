package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Guardrail: ensure the cmd/bd test suite does not touch the real repo .beads state.
// Disable with BEADS_TEST_GUARD_DISABLE=1 (useful when running tests while actively using beads).
func TestMain(m *testing.M) {
	// Enable test mode that forces accessor functions to use legacy globals.
	// This ensures backward compatibility with tests that manipulate globals directly.
	enableTestModeGlobals()

	// Prevent daemon auto-start and ensure tests don't interact with any running daemon.
	// This prevents false positives in the test guard when a background daemon touches
	// .beads files (like issues.jsonl via auto-sync) during test execution.
	origNoDaemon := os.Getenv("BEADS_NO_DAEMON")
	os.Setenv("BEADS_NO_DAEMON", "1")
	defer func() {
		if origNoDaemon != "" {
			os.Setenv("BEADS_NO_DAEMON", origNoDaemon)
		} else {
			os.Unsetenv("BEADS_NO_DAEMON")
		}
	}()

	// Clear BEADS_DIR to prevent tests from accidentally picking up the project's
	// .beads directory via git repo detection when there's a redirect file.
	// Each test that needs a .beads directory should set BEADS_DIR explicitly.
	origBeadsDir := os.Getenv("BEADS_DIR")
	os.Unsetenv("BEADS_DIR")
	defer func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		}
	}()

	if os.Getenv("BEADS_TEST_GUARD_DISABLE") != "" {
		os.Exit(m.Run())
	}

	// Stop any running daemon for this repo to prevent false positives in the guard.
	// The daemon auto-syncs and touches files like issues.jsonl, which would trigger
	// the guard even though tests didn't cause the change.
	repoRoot := findRepoRoot()
	if repoRoot != "" {
		stopRepoDaemon(repoRoot)
	} else {
		os.Exit(m.Run())
	}

	repoBeadsDir := filepath.Join(repoRoot, ".beads")
	if _, err := os.Stat(repoBeadsDir); err != nil {
		os.Exit(m.Run())
	}

	watch := []string{
		"beads.db",
		"beads.db-wal",
		"beads.db-shm",
		"beads.db-journal",
		"issues.jsonl",
		"beads.jsonl",
		"metadata.json",
		"interactions.jsonl",
		"deletions.jsonl",
		"molecules.jsonl",
		"daemon.lock",
		"daemon.pid",
		"bd.sock",
	}

	before := snapshotFiles(repoBeadsDir, watch)
	code := m.Run()
	after := snapshotFiles(repoBeadsDir, watch)

	if diff := diffSnapshots(before, after); diff != "" {
		fmt.Fprintf(os.Stderr, "ERROR: test suite modified repo .beads state:\n%s\n", diff)
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

type fileSnap struct {
	exists  bool
	size    int64
	modUnix int64
}

func snapshotFiles(dir string, names []string) map[string]fileSnap {
	out := make(map[string]fileSnap, len(names))
	for _, name := range names {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil {
			out[name] = fileSnap{exists: false}
			continue
		}
		out[name] = fileSnap{exists: true, size: info.Size(), modUnix: info.ModTime().UnixNano()}
	}
	return out
}

func diffSnapshots(before, after map[string]fileSnap) string {
	var out string
	for name, b := range before {
		a := after[name]
		if b.exists != a.exists {
			out += fmt.Sprintf("- %s: exists %v → %v\n", name, b.exists, a.exists)
			continue
		}
		if !b.exists {
			continue
		}
		// Only report size changes (actual content modification).
		// Ignore mtime-only changes - SQLite shm/wal files can have mtime updated
		// from read-only operations (config loading, etc.) which is not pollution.
		if b.size != a.size {
			out += fmt.Sprintf("- %s: size %d → %d\n", name, b.size, a.size)
		}
	}
	return out
}

func findRepoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 25; i++ {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	return ""
}

// stopRepoDaemon stops any running daemon for the given repository.
// This prevents false positives in the test guard when a background daemon
// touches .beads files during test execution. Uses exec to avoid import cycles.
func stopRepoDaemon(repoRoot string) {
	beadsDir := filepath.Join(repoRoot, ".beads")
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Check if socket exists (quick check before shelling out)
	if _, err := os.Stat(socketPath); err != nil {
		return // no daemon running
	}

	// Shell out to bd daemon stop. We can't call the daemon functions directly
	// from TestMain because they have complex dependencies. Using exec is cleaner.
	cmd := exec.Command("bd", "daemon", "stop")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "BEADS_DIR="+beadsDir)

	// Best-effort stop - ignore errors (daemon may not be running)
	_ = cmd.Run()

	// Give daemon time to shutdown gracefully
	time.Sleep(500 * time.Millisecond)
}
