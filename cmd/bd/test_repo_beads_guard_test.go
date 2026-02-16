package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

// Guardrail: ensure the cmd/bd test suite does not touch the real repo .beads state.
// Disable with BEADS_TEST_GUARD_DISABLE=1 (useful when running tests while actively using beads).
func TestMain(m *testing.M) {
	origWD, _ := os.Getwd()

	// Isolate config discovery from the repo's tracked `.beads/config.yaml`.
	// Many tests expect default config values; running from within this repo would
	// cause config.Initialize() to walk up from CWD and load `.beads/config.yaml`,
	// which sets sync.mode=dolt-native and makes tests assert the wrong behavior.
	tmp, err := os.MkdirTemp("", "beads-bd-tests-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	// Preserve Go build cache before changing HOME.
	// On macOS, GOCACHE defaults to $HOME/Library/Caches/go-build.
	// Changing HOME would cause tests that run `go build` (e.g., TestShow)
	// to miss the cache and do a full CGO rebuild (~80s each).
	if os.Getenv("GOCACHE") == "" {
		if out, err := exec.Command("go", "env", "GOCACHE").Output(); err == nil {
			_ = os.Setenv("GOCACHE", strings.TrimSpace(string(out)))
		}
	}

	_ = os.Setenv("HOME", tmp)
	_ = os.Setenv("USERPROFILE", tmp) // Windows compatibility
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg-config"))
	_ = os.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")

	// Also reset viper state that was loaded by main.go's init().
	config.ResetForTesting()

	// Enable test mode that forces accessor functions to use legacy globals.
	// This ensures backward compatibility with tests that manipulate globals directly.
	enableTestModeGlobals()

	// Set BEADS_TEST_MODE once for the entire test run (bd-cqjoi).
	// Previously each test set/unset this env var via ensureTestMode(),
	// which raced under t.Parallel().
	_ = os.Setenv("BEADS_TEST_MODE", "1")

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
	repoRoot := findRepoRootFrom(origWD)
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
		// interactions.jsonl excluded: legitimately created by init during tests
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
	return findRepoRootFrom(wd)
}

func findRepoRootFrom(wd string) string {
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
