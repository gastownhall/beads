package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/testutil"
)

// beforeTestsHook is set by CGO-tagged test files to perform setup before tests run
// (e.g., starting a shared test Dolt server). Returns a cleanup function.
var beforeTestsHook func() func()

// testTempRoot is the parent directory for per-process test temp dirs.
// It is set by testMainInner and used by the package-level sync.Once
// helpers (build binaries, isolated HOMEs) that previously called
// os.MkdirTemp("", ...) and leaked on every run. Anchoring those temp
// dirs under testTempRoot means the defer in testMainInner cleans them
// all up in one place (bd-3q2u / gastownhall/beads#4106).
//
// When tests run without TestMain (e.g. a single test invoked with the
// internal test binary directly), testTempRoot is empty and helpers
// fall back to os.TempDir().
var testTempRoot string

// testTempDir returns os.MkdirTemp under testTempRoot when it is set,
// otherwise it falls back to the system temp dir (os.MkdirTemp's
// default). Use this in package-level sync.Once builders so leaked
// directories get reaped by testMainInner's deferred cleanup.
func testTempDir(pattern string) (string, error) {
	return os.MkdirTemp(testTempRoot, pattern)
}

// runTestsAndSweep runs the suite and then best-effort reaps any dolt
// sql-server left running under testTempRoot (e.g. auto-started by a CLI
// test's embedded `bd` invocation, if a SIGKILLed run left one behind).
// This is the suite most likely to leak — most e2e tests here run a real
// `bd` binary against a `.beads` dir under testTempRoot with auto-start
// enabled. See gastownhall/beads mybd-q6cz.
type testRunner interface {
	Run() int
}

func runTestsAndSweep(m testRunner) int {
	code := m.Run()
	doltserver.SweepOrphanedTestServers(testTempRoot)
	return code
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
	// which may set non-default config values and makes tests assert the wrong behavior.
	tmp, err := os.MkdirTemp("", "beads-bd-tests-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		return 1
	}
	defer func() { _ = forceRemoveAll(tmp) }()

	// Anchor package-level sync.Once builders (test binaries, isolated
	// HOMEs) under this directory so the defer above sweeps them up too.
	// Without this, those helpers leaked ~179MB-1.4GB per test run into
	// /tmp and exhausted tmpfs over time (bd-3q2u).
	testTempRoot = tmp

	// Preserve Go build cache before changing HOME.
	// On macOS, GOCACHE defaults to $HOME/Library/Caches/go-build.
	// Changing HOME would cause tests that run `go build` (e.g., TestShow)
	// to miss the cache and do a full CGO rebuild (~80s each).
	if os.Getenv("GOCACHE") == "" {
		if out, err := exec.Command("go", "env", "GOCACHE").Output(); err == nil {
			_ = os.Setenv("GOCACHE", strings.TrimSpace(string(out)))
		}
	}

	// Same for the module cache: GOMODCACHE defaults to $HOME/go/pkg/mod,
	// so without this the in-test `go build` (buildEmbeddedBD) re-downloads
	// every dependency into the temp HOME on each run — slow, and a hard
	// failure when the network is unavailable.
	if os.Getenv("GOMODCACHE") == "" {
		if out, err := exec.Command("go", "env", "GOMODCACHE").Output(); err == nil {
			_ = os.Setenv("GOMODCACHE", strings.TrimSpace(string(out)))
		}
	}

	// The docker CLI's active context also lives under HOME
	// (~/.docker/config.json); resolve it into DOCKER_HOST now or every
	// container-gated test skips "Docker not available" on context-routed
	// daemons like OrbStack (bd-84kos).
	testutil.PinDockerHostFromContext()

	_ = os.Setenv("HOME", tmp)
	_ = os.Setenv("USERPROFILE", tmp) // Windows compatibility
	_ = os.Setenv("APPDATA", filepath.Join(tmp, "AppData", "Roaming"))
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg-config"))
	_ = os.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")

	// Keep telemetry out of the test suite entirely (wy-12x1p).
	//
	// Every `bd` run with metrics enabled ends in metrics.CloseAndFlush, which
	// (a) writes an eventkit queue under $HOME/.beads/eventsData and (b) spawns
	// a DETACHED `bd send-metrics` child (cmd.Process.Release — no Wait) that
	// outlives its parent. The e2e tests here run the bd binary with
	// HOME=t.TempDir(), so those orphans keep creating/removing .evtq files and
	// holding eventkit.lock under a temp dir the test is about to delete. Go's
	// t.TempDir cleanup then fails with
	//
	//   TempDir RemoveAll cleanup: unlinkat .../NNN: directory not empty
	//
	// which reddens the whole cmd/bd package with no assertion failure in
	// sight. It is load-dependent, so it flaked intermittently on a busy
	// machine (TestPrime_HookJSON_{Local,Redirected}PrimeOverride were the
	// observed victims, but every subprocess test here was exposed).
	//
	// Both vars are set: EnvDisableEventFlush alone would stop the detached
	// child, and EnvDisableMetrics additionally keeps the queue files out of
	// the isolated HOME — and a test suite should never upload telemetry.
	// Subprocess envs in this package are built with append(os.Environ(), ...),
	// so setting it here covers all of them. Tests that specifically exercise
	// metrics resolution already unset these per-test and restore them.
	_ = os.Setenv(metrics.EnvDisableMetrics, "1")
	_ = os.Setenv(metrics.EnvDisableEventFlush, "1")

	// Also reset viper state that was loaded by main.go's init().
	config.ResetForTesting()

	// Enable test mode that forces accessor functions to use legacy globals.
	// This ensures backward compatibility with tests that manipulate globals directly.
	enableTestModeGlobals()

	// Set BEADS_TEST_MODE once for the entire test run (bd-cqjoi).
	// Previously each test set/unset this env var via ensureTestMode(),
	// which raced under t.Parallel().
	_ = os.Setenv("BEADS_TEST_MODE", "1")
	// AD-01 (be-c5p): opt the cmd/bd test process into the dedicated
	// test-server lane so dolt.New's database-name firewall allows
	// testdb_*, benchdb_*, etc. on the spawned test container.
	_ = os.Setenv("BEADS_TEST_SERVER", "1")
	_ = os.Setenv("BEADS_TEST_CIRCUIT_DIR", filepath.Join(tmp, "circuit"))
	defer os.Unsetenv("BEADS_TEST_CIRCUIT_DIR")

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

	// Clear BD_BACKUP_ENABLED / BEADS_BACKUP_ENABLED (legacy alias) so tests
	// asserting on backup.enabled's auto-detected default aren't overridden by
	// whatever the invoking shell happens to export for real bd usage
	// (be-yjp4z). Tests that need a specific value set it explicitly via
	// t.Setenv.
	origBackupEnabled := os.Getenv("BD_BACKUP_ENABLED")
	os.Unsetenv("BD_BACKUP_ENABLED")
	defer func() {
		if origBackupEnabled != "" {
			os.Setenv("BD_BACKUP_ENABLED", origBackupEnabled)
		}
	}()
	origBeadsBackupEnabled := os.Getenv("BEADS_BACKUP_ENABLED")
	os.Unsetenv("BEADS_BACKUP_ENABLED")
	defer func() {
		if origBeadsBackupEnabled != "" {
			os.Setenv("BEADS_BACKUP_ENABLED", origBeadsBackupEnabled)
		}
	}()

	// BD_BRANCH is no longer used (all writers operate on main with transactions).

	// Start shared test Dolt server if the hook is registered (CGO builds).
	// This must happen after HOME is changed so dolt config goes to the temp dir.
	if beforeTestsHook != nil {
		cleanup := beforeTestsHook()
		defer cleanup()
	}

	if os.Getenv("BEADS_TEST_GUARD_DISABLE") != "" {
		return runTestsAndSweep(m)
	}

	repoRoot := findRepoRootFrom(origWD)
	if repoRoot == "" {
		return runTestsAndSweep(m)
	}

	repoBeadsDir := filepath.Join(repoRoot, ".beads")
	if _, err := os.Stat(repoBeadsDir); err != nil {
		return runTestsAndSweep(m)
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
	code := runTestsAndSweep(m)
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

// forceRemoveAll removes a directory tree, handling read-only files
// (e.g., Go module cache entries under $HOME/go/pkg/mod/).
// os.RemoveAll fails silently on read-only files; this makes them
// writable first so cleanup actually succeeds.
func forceRemoveAll(dir string) error {
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && info.Mode()&0200 == 0 {
			_ = os.Chmod(path, info.Mode()|0200)
		}
		return nil
	})
	return os.RemoveAll(dir)
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
