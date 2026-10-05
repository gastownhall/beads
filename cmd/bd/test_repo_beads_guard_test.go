package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/ceiling"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/testutil/credentialcmd"
	"github.com/steveyegge/beads/internal/workspacegate"
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
	stdout, stderr := os.Stdout, os.Stderr
	code := m.Run()
	code = checkStdioAfterRun(code, stdout, stderr)
	swept := doltserver.SweepSuiteTestServers(testTempRoot)
	return doltserver.ApplyLeakPolicy("cmd/bd", code, swept)
}

// suiteRootPrefix is testMainInner's PinSuiteTempRoot pattern without its random
// tail. It is what SweepDeadSuiteRoots globs for, so the two must not drift.
const suiteRootPrefix = "beads-bd-tests-"

// Guardrail: ensure the cmd/bd test suite does not touch the real repo .beads state.
// Disable with BEADS_TEST_GUARD_DISABLE=1 (useful when running tests while actively using beads).
func TestMain(m *testing.M) {
	if code, ok := credentialcmd.Dispatch(); ok {
		os.Exit(code)
	}
	// Delegate to testMainInner so defers run before os.Exit.
	code := testMainInner(m)
	if err := credentialcmd.Cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "credential command fixture cleanup: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func testMainInner(m *testing.M) int {
	// A bd parent (say, a git hook or `bd` driving `go test`) must not make
	// this binary's bd subprocesses skip the workspace gate's writer queue.
	_ = os.Unsetenv(workspacegate.InheritedHoldEnv)
	origWD, _ := os.Getwd()
	// Computed once and reused below for the pin, the ceiling-var boundary,
	// and the guard's own watch setup, so all three agree on exactly which
	// checkout this process started in.
	repoRoot := findRepoRootFrom(origWD)

	// Fence the whole test binary's beads/git discovery before any test (or
	// package-level init/sync.Once) gets a chance to make the first
	// cached-git-context call from this worktree checkout. Without this, that
	// first call permanently answers isWorktree=true / a real
	// --git-common-dir for the rest of the process — regardless of later
	// per-test chdirs — and beads.FindBeadsDir's worktree-fallback discovery
	// then treats that as license to read and write the main checkout's
	// shared .beads database (e.g. /data/projects/beads/.beads when this
	// worktree is a sibling checkout). Pinning "no repository" for repoRoot
	// (not just the package directory this process started in) makes that
	// ambient state inert by default for any subdirectory of the checkout a
	// test might chdir into, and keeps answering that way even after
	// git.ResetCaches: see PinNoRepositoryUnderForTesting's doc. Tests that
	// need real git behavior chdir OUTSIDE repoRoot into a fixture they
	// control (cmd/bd/git_test_helpers.go's runInDir, normally a
	// t.TempDir()) and call git.ResetCaches/beads.ResetCaches there, which
	// gets real detection scoped to that fixture — the pin only answers for
	// repoRoot and its descendants.
	//
	// Residual scope this pin does NOT cover: -C (main.go's flag handling)
	// and doctor.ResolveBeadsDirForRepo's callers resolve a beads directory
	// by shelling out to `git -C <dir> ...` directly
	// (internal/beads/beads.go's FindBeadsDirFrom/ResolveBeadsDirForRepo),
	// never reading this package's cached git context at all, so this pin
	// cannot fence them. No test in this package currently exercises that
	// path against the real checkout, so it is an untested gap rather than
	// a guarded one — flagged here rather than silently assumed covered.
	if repoRoot != "" {
		git.PinNoRepositoryUnderForTesting(repoRoot)
	}
	beads.ResetCaches()

	// Separately bound any literal ancestor-directory walk that starts
	// at (or under) this checkout: the pin above only defeats the
	// git-context-cache vector, not a plain upward walk from a worktree
	// nested under a directory that itself has a .beads (e.g. a scratch
	// checkout under a dir with a live .beads ancestor). Append rather than
	// overwrite: Bazel's tools/bazel/test_env.sh already sets a ceiling
	// derived from TEST_SRCDIR/TEST_TMPDIR for bazel-run tests, and this
	// must only add a boundary, never remove one those runs rely on.
	if repoRoot != "" {
		ceilingList := repoRoot
		if existing := os.Getenv(ceiling.EnvVar); existing != "" {
			ceilingList = existing + string(os.PathListSeparator) + repoRoot
		}
		_ = os.Setenv(ceiling.EnvVar, ceilingList)
	}

	// Isolate config discovery from the repo's tracked `.beads/config.yaml`.
	// Many tests expect default config values; running from within this repo would
	// cause config.Initialize() to walk up from CWD and load `.beads/config.yaml`,
	// which may set non-default config values and makes tests assert the wrong behavior.
	// Before claiming a root of our own, clear out the roots of EARLIER runs
	// of this suite whose process is gone — a `go test -timeout` panic skips
	// both the defer below and the post-Run sweep, so the servers those runs
	// started outlive every cleanup this process installs, and nothing else
	// ever looks at a dead run's tree again (wy-j2zc8q). Roots with no owner
	// marker, and roots whose owner is still running, are left untouched.
	doltserver.SweepDeadSuiteRoots(os.TempDir(), suiteRootPrefix)

	tmp, err := testutil.PinSuiteTempRoot(suiteRootPrefix + "*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		return 1
	}
	defer func() { _ = forceRemoveAll(tmp) }()

	// Claim the root for this process so the NEXT run can tell our debris
	// from a concurrent run's live tree.
	if err := doltserver.WriteSuiteOwnerMarker(tmp); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not claim suite temp root %s: %v\n", tmp, err)
	}

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

	// Keep HOME beside the fixture directories, not above them. Tests may
	// create ~/.beads; putting it on their ancestry would make repository
	// discovery pick up unrelated suite state before trying worktree fallback.
	home := filepath.Join(tmp, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create test home: %v\n", err)
		return 1
	}
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("USERPROFILE", home) // Windows compatibility
	_ = os.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
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

	// Pin the migration-freeze override to a path that cannot exist (dc-6jaq).
	// The freeze gate walks every ancestor of the workspace and of the cwd up
	// to the filesystem root, so a stray MIGRATION-FREEZE above TMPDIR — or in
	// a developer's home, or exported by their shell — would refuse every
	// write in every subprocess suite in this package with exit 14. The
	// override is authoritative, so pinning it here holds the walk off
	// globally; the freeze tests that need the walk clear it per-run.
	_ = os.Setenv(migration.EnvFreezeFile, filepath.Join(tmp, "no-such-freeze-marker"))

	// Also reset viper state that was loaded by main.go's init().
	config.ResetForTesting()

	// Record every flag's registered state before any test executes a
	// command, so in-process runners can put the tree back between runs
	// (resetCommandFlags).
	snapshotCommandFlags(rootCmd)
	// And undo what each in-process execution leaves in the process env and
	// the storage-mode globals (installExecuteIsolation).
	installExecuteIsolation()

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
	// This is startup isolation only: fresh in-process command fixtures should
	// use isolateBeadsDirForTest before setup to contain later dispatch mutations.
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

	if repoRoot == "" {
		return runTestsAndSweep(m)
	}

	repoBeadsDir := filepath.Join(repoRoot, ".beads")
	// Backstop: also watch the shared worktree-fallback .beads directory that
	// beads.FindBeadsDir's discovery would fall back to for a linked worktree
	// (e.g. the main checkout's .beads when this package's repo root is a
	// worktree). This is computed directly via git plumbing, independent of
	// the process-wide cache PinNoRepositoryUnderForTesting neutralizes
	// above, so it still catches pollution if that pin is ever bypassed or
	// narrowed. Computed and checked BEFORE deciding whether repoBeadsDir
	// itself exists: a linked worktree with no local .beads of its own is
	// exactly the layout where the fallback is live, so skipping this whole
	// function when repoBeadsDir is absent would guard nothing in precisely
	// the case that matters most.
	fallbackBeadsDir := worktreeFallbackBeadsDirDirect(repoRoot)

	var guardDirs []string
	if _, err := os.Stat(repoBeadsDir); err == nil {
		guardDirs = append(guardDirs, repoBeadsDir)
	}
	if fallbackBeadsDir != "" && fallbackBeadsDir != repoBeadsDir {
		if _, err := os.Stat(fallbackBeadsDir); err == nil {
			guardDirs = append(guardDirs, fallbackBeadsDir)
		}
	}
	if len(guardDirs) == 0 {
		return runTestsAndSweep(m)
	}

	// Top-level files checked by exact name. interactions.jsonl is excluded:
	// legitimately created by init during tests. last-touched is excluded:
	// it is bumped by any `bd` invocation anywhere in the checkout, including
	// other processes sharing it concurrently, so it is not a reliable signal
	// of this suite's own writes.
	watchFiles := []string{
		"beads.db",
		"beads.db-wal",
		"beads.db-shm",
		"beads.db-journal",
		"issues.jsonl",
		"beads.jsonl",
		"metadata.json",
		"config.yaml",
		"routes.jsonl",
		"deletions.jsonl",
		"molecules.jsonl",
	}

	// dirSnapshot pairs the flat top-level watch with a recursive snapshot of
	// dolt/, the Dolt backend's own state directory. A flat name list can't
	// see writes inside dolt/ (e.g. a `bd create` landing a real commit in
	// the fallback's database), so this is required for the guard to mean
	// what its error message claims for a Dolt-backed fallback workspace.
	type dirSnapshot struct {
		flat, dolt map[string]fileSnap
	}
	snapshotDir := func(dir string) dirSnapshot {
		return dirSnapshot{
			flat: snapshotFiles(dir, watchFiles),
			dolt: snapshotDoltTree(filepath.Join(dir, "dolt")),
		}
	}

	before := make(map[string]dirSnapshot, len(guardDirs))
	for _, dir := range guardDirs {
		before[dir] = snapshotDir(dir)
	}
	code := runTestsAndSweep(m)

	var allDiffs string
	for _, dir := range guardDirs {
		after := snapshotDir(dir)
		diff := diffSnapshots(before[dir].flat, after.flat)
		diff += diffSnapshots(before[dir].dolt, after.dolt)
		if diff != "" {
			allDiffs += fmt.Sprintf("in %s:\n%s", dir, diff)
		}
	}

	if allDiffs != "" {
		fmt.Fprintf(os.Stderr, "ERROR: test suite modified repo .beads state:\n%s\n", allDiffs)
		if code == 0 {
			code = 1
		}
	}

	return code
}

// worktreeFallbackBeadsDirDirect resolves the shared .beads directory a
// linked worktree at repoRoot would fall back to, via a direct git shell-out
// independent of internal/git's process-wide cache (which
// PinNoRepositoryUnderForTesting deliberately neutralizes above). Mirrors
// internal/beads's worktreeFallbackBeadsDirForRepo; kept local and minimal
// since this is a test-only detection backstop, not production discovery.
func worktreeFallbackBeadsDirDirect(repoRoot string) string {
	if repoRoot == "" {
		return ""
	}
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--git-dir", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return ""
	}
	gitDir := resolveGitPath(repoRoot, strings.TrimSpace(lines[0]))
	commonDir := resolveGitPath(repoRoot, strings.TrimSpace(lines[1]))
	if gitDir == "" || commonDir == "" || gitDir == commonDir {
		return "" // not a worktree
	}
	if filepath.Base(commonDir) == ".git" {
		return filepath.Join(filepath.Dir(commonDir), ".beads")
	}
	return filepath.Join(commonDir, ".beads")
}

func resolveGitPath(repoRoot, p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoRoot, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	return abs
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

// snapshotDoltTree walks dir recursively and returns a map keyed by path
// relative to dir, for directories whose structure itself needs watching
// (e.g. .beads/dolt, which stores workspace state as an arbitrary tree of
// files rather than a fixed set of top-level names snapshotFiles's explicit
// list can name). A missing or unreadable dir yields an empty map, not an
// error: a .beads without its own dolt/ is normal (a non-Dolt backend, or a
// workspace that was never provisioned), and must not be treated as 212
// files vanishing on the "after" side.
func snapshotDoltTree(dir string) map[string]fileSnap {
	out := make(map[string]fileSnap)
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil //nolint:nilerr // best-effort snapshot; a walk error just narrows what gets watched
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = path
		}
		out[rel] = fileSnap{exists: true, size: info.Size(), modUnix: info.ModTime().UnixNano()}
		return nil
	})
	return out
}

// diffSnapshots reports entries that appeared, disappeared, or changed size
// between before and after. Checks both directions (before→after for
// removals/changes, after→before for additions) so it works for a recursive
// snapshotDoltTree map, where "after" can legitimately contain keys "before"
// never had — unlike snapshotFiles's fixed name list, where the key set is
// identical on both sides and only the addition-check is a no-op.
func diffSnapshots(before, after map[string]fileSnap) string {
	var out string
	for name, b := range before {
		a, ok := after[name]
		if !ok {
			a = fileSnap{exists: false}
		}
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
	for name, a := range after {
		if _, ok := before[name]; !ok && a.exists {
			out += fmt.Sprintf("- %s: new file (size %d)\n", name, a.size)
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
