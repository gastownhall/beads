package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/lockfile"
)

// beforeTestsHook is set by CGO-tagged test files to perform setup before tests run
// (e.g., starting a shared test Dolt server). Returns a cleanup function.
var beforeTestsHook func() func()

// acquireCmdBDHarnessLock serializes the cmd/bd package across concurrent
// `go test ./cmd/bd ...` processes. This suite intentionally mutates
// process-global state (HOME, BEADS_TEST_MODE, stdout/stderr, package globals)
// and shares external Dolt test harness resources, so cross-process overlap
// can create harness-only flakes that look like product failures.
func acquireCmdBDHarnessLock() (func(), error) {
	lockPath := filepath.Join(os.TempDir(), "beads-cmd-bd-tests.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open harness lock %s: %w", lockPath, err)
	}
	if err := lockfile.FlockExclusiveBlocking(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock harness file %s: %w", lockPath, err)
	}
	return func() {
		_ = lockfile.FlockUnlock(f)
		_ = f.Close()
	}, nil
}

// Guardrail: ensure the cmd/bd test suite does not touch the real repo .beads state.
// Disable with BEADS_TEST_GUARD_DISABLE=1 (useful when running tests while actively using beads).
func TestMain(m *testing.M) {
	// Delegate to testMainInner so defers run before os.Exit.
	os.Exit(testMainInner(m))
}

func testMainInner(m *testing.M) int {
	origWD, _ := os.Getwd()

	// Isolate config discovery from the repo's tracked `.beads/config.yaml`.
	// Many tests expect default config values; running from within this repo would
	// cause config.Initialize() to walk up from CWD and load `.beads/config.yaml`,
	// which sets sync.mode=dolt-native and makes tests assert the wrong behavior.
	tmp, err := os.MkdirTemp("", "beads-bd-tests-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		return 1
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

	releaseHarnessLock, err := acquireCmdBDHarnessLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to acquire cmd/bd harness lock: %v\n", err)
		return 1
	}
	defer releaseHarnessLock()

	// BD_BRANCH is no longer used (all writers operate on main with transactions).

	// Start shared test Dolt server if the hook is registered (CGO builds).
	// This must happen after HOME is changed so dolt config goes to the temp dir.
	if beforeTestsHook != nil {
		cleanup := beforeTestsHook()
		defer cleanup()
	}

	if os.Getenv("BEADS_TEST_GUARD_DISABLE") != "" {
		return m.Run()
	}

	repoRoot := findRepoRootFrom(origWD)
	if repoRoot == "" {
		return m.Run()
	}

	repoBeadsDir := filepath.Join(repoRoot, ".beads")
	if _, err := os.Stat(repoBeadsDir); err != nil {
		return m.Run()
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

	return code
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
