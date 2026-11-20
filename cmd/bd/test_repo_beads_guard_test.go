package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/ceiling"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/initprocs"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/migration"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/testutil/bazeltest"
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

// testBDBuildDir is the temp dir cli_fast_test.go's init() builds the bd
// binary into when no prebuilt one is supplied. init() runs before TestMain
// pins testTempRoot, so the dir lands in the system temp dir and the
// testMainInner defer does not reach it; TestMain removes it explicitly.
// Each build is ~200MB, so leaking it on every run fills shared hosts.
var testBDBuildDir string

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

// preRunStdout and preRunStderr are os.Stdout/os.Stderr as runTestsAndSweep
// saw them before m.Run: TestZZStdioNotLeaked's baseline when
// TestAAAStdioBaseline did not run in this process.
var preRunStdout, preRunStderr *os.File

func runTestsAndSweep(m testRunner) int {
	stdout, stderr := os.Stdout, os.Stderr
	preRunStdout, preRunStderr = stdout, stderr
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
	// main() is not run here, so hand back the Ps initprocs held during init,
	// and the -test.parallel default testing derived from the capped count.
	// An explicit -test.parallel is parsed later, by m.Run, and still wins.
	initprocs.Done()
	if f := flag.Lookup("test.parallel"); f != nil {
		_ = f.Value.Set(strconv.Itoa(runtime.GOMAXPROCS(0)))
	}
	if code, ok := credentialcmd.Dispatch(); ok {
		os.Exit(code)
	}
	// Delegate to testMainInner so defers run before os.Exit.
	code := testMainInner(m)
	if testBDBuildDir != "" {
		_ = os.RemoveAll(testBDBuildDir)
	}
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

	// Drop inherited git repository-location vars (GIT_DIR, GIT_WORK_TREE,
	// GIT_COMMON_DIR, GIT_INDEX_FILE, GIT_CONFIG, ...) for the whole test
	// process. Fixture helpers in this package run `git config`/`git init`
	// with only cmd.Dir set; a location var leaked in from the invoking shell
	// or a hook overrides that Dir and points every such write at a
	// repository the suite does not own — one of the vectors behind the r880
	// host .git/config clobber (wy-qlvlmf). GIT_CEILING_DIRECTORIES,
	// GIT_CONFIG_GLOBAL and GIT_CONFIG_NOSYSTEM stay: tools/bazel/test_env.sh
	// sets them on purpose. Tests that need a location var set it per-test
	// with t.Setenv.
	if err := testutil.UnsetGitRepoLocationEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		return 1
	}

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

	// Preserve Go build cache before changing HOME. On macOS, GOCACHE
	// defaults to $HOME/Library/Caches/go-build. Changing HOME would cause
	// tests that run `go build` (e.g., TestShow) to miss the cache and do a
	// full CGO rebuild (~80s each). Under Bazel there is no host Go
	// toolchain on the hermetic test PATH (tools/bazel/test_env.sh) and no
	// in-test `go build` to preserve a cache for, so skip the host `go env`
	// exec entirely rather than rely on its error being ignored.
	if !bazeltest.IsBazel() {
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

	// The checkout's own git state is guarded alongside its .beads state: on
	// 2026-10-16 a `go test ./cmd/bd` run from a linked worktree rewrote the
	// host rig's shared .git/config (core.bare=true, extensions.worktreeConfig,
	// a test remote.origin.url over the real origin) and its hooks, breaking
	// every worktree of that clone (wy-qlvlmf, r880). The common config and
	// hooks are shared by every worktree of the clone. Common-config changes
	// that ordinary git use in any checkout makes (branch tracking, a remote
	// other than origin added or removed whole) only draw a note. Other
	// worktrees of a shared clone rewrite more of its common config while this
	// suite runs, so there only keys of the kind the clobber rewrote fail the
	// run. Any other common-config change in a clone no other checkout uses,
	// and any change to a hook or to this checkout's own config.worktree,
	// means a test most likely ran git against this checkout instead of a
	// fixture it owns.
	gitDirs, err := locateRepoGitDirs(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: this checkout's own git state is not guarded in this run: %v\n", err)
	}
	var gitStateBefore repoGitState
	if gitDirs.common != "" {
		gitStateBefore = readRepoGitState(gitDirs)
	}

	if len(guardDirs) == 0 && gitDirs.common == "" {
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

	if gitDirs.common != "" {
		changes := diffRepoGitState(gitStateBefore, readRepoGitState(gitDirs))
		// PinSuiteTempRoot pointed TMPDIR at the suite root, which the
		// deferred cleanup deletes; keep the pre-run copies beside it.
		report, failed := reportGitStateChanges(changes, gitStateBefore, filepath.Dir(testTempRoot))
		fmt.Fprint(os.Stderr, report)
		if failed && code == 0 {
			code = 1
		}
	}

	return code
}

// repoGitDirs names a checkout's git directories: gitDir is its own (a linked
// worktree's .git/worktrees/<name>), common is the one every worktree of the
// clone shares. They are the same directory for a main checkout. shared
// records that the clone has linked worktrees registered (a linked worktree
// is one of them), so checkouts other than this one can write its common
// config.
type repoGitDirs struct {
	gitDir, common string
	shared         bool
}

func (d repoGitDirs) commonConfig() string { return filepath.Join(d.common, "config") }

// worktreeConfig is the checkout's own config.worktree. A main checkout's sits
// in the common dir beside the shared config. With extensions.worktreeConfig
// set, git reads it after the shared config, so its keys win, and
// `git config --worktree` writes there.
func (d repoGitDirs) worktreeConfig() string {
	if d.gitDir == "" {
		return ""
	}
	return filepath.Join(d.gitDir, "config.worktree")
}

// locateRepoGitDirs resolves repoRoot's git directories once, before the run,
// so the post-run read looks at the same files even if a test rewrote the
// checkout's .git pointer. A repoRoot outside any checkout (Bazel runfiles, a
// source tarball) has nothing to guard and is not an error.
func locateRepoGitDirs(repoRoot string) (repoGitDirs, error) {
	if repoRoot == "" {
		return repoGitDirs{}, nil
	}
	cmd := gitCommand("-C", repoRoot, "rev-parse", "--git-dir", "--git-common-dir")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if _, statErr := os.Lstat(filepath.Join(repoRoot, ".git")); errors.Is(statErr, os.ErrNotExist) {
			return repoGitDirs{}, nil
		}
		return repoGitDirs{}, fmt.Errorf("git rev-parse in %s: %w: %s", repoRoot, err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return repoGitDirs{}, fmt.Errorf("git rev-parse in %s: unexpected output %q", repoRoot, out)
	}
	dirs := repoGitDirs{
		gitDir: resolveGitPath(repoRoot, strings.TrimSpace(lines[0])),
		common: resolveGitPath(repoRoot, strings.TrimSpace(lines[1])),
	}
	if dirs.common == "" {
		return repoGitDirs{}, fmt.Errorf("git rev-parse in %s: no common git dir in %q", repoRoot, out)
	}
	// Decided here, before the run, so a test that registers a worktree on
	// this clone cannot relax the check of its own writes.
	dirs.shared = hasLinkedWorktrees(dirs.common)
	return dirs, nil
}

// hasLinkedWorktrees reports whether a clone's common dir has any linked
// worktree registered. A stale entry that `git worktree prune` has not removed
// yet counts too, which only relaxes the guard.
func hasLinkedWorktrees(common string) bool {
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	return err == nil && len(entries) > 0
}

// repoGitState captures the parts of a checkout's own git state that the r880
// incident showed a test escape mutates: the common (shared) config, the
// checkout's own config.worktree, and every file under the common hooks dir.
// Files are keyed by absolute path, and a missing one is recorded as missing,
// so a deletion and a re-creation both show up.
type repoGitState struct {
	dirs  repoGitDirs
	files map[string]gitFileSnap
}

type gitFileSnap struct {
	present bool
	data    string
}

func readRepoGitState(dirs repoGitDirs) repoGitState {
	state := repoGitState{dirs: dirs, files: map[string]gitFileSnap{}}
	snapFile := func(p string) {
		data, err := os.ReadFile(p)
		state.files[p] = gitFileSnap{present: err == nil, data: string(data)}
	}
	snapFile(dirs.commonConfig())
	if p := dirs.worktreeConfig(); p != "" {
		snapFile(p)
	}
	_ = filepath.Walk(filepath.Join(dirs.common, "hooks"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil //nolint:nilerr // best-effort snapshot; a walk error just narrows what gets watched
		}
		snapFile(path)
		return nil
	})
	return state
}

// gitStateChange is one watched git-state file that differs across the run.
// detail names what changed (config keys, or a file-level summary) and never
// echoes a value: configs carry remote URLs and can carry credentials.
type gitStateChange struct {
	path    string
	detail  string
	routine bool
}

// diffRepoGitState compares two readRepoGitState results. A common-config
// change is routine when ordinary git use outside this suite explains every
// changed key (routineConfigKey). Every other change is the clobber class,
// including any change to a hook file or config.worktree, a common config
// created, deleted or left unparseable, and, in any clone, a change to a key
// of the kind the r880 clobber rewrote (clobberConfigKey).
func diffRepoGitState(before, after repoGitState) []gitStateChange {
	paths := make([]string, 0, len(before.files))
	for p := range before.files {
		paths = append(paths, p)
	}
	for p := range after.files {
		if _, ok := before.files[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)

	var changes []gitStateChange
	for _, p := range paths {
		b, a := before.files[p], after.files[p]
		switch {
		case b == a:
			continue
		case !b.present:
			changes = append(changes, gitStateChange{path: p, detail: fmt.Sprintf("new file (%d bytes)", len(a.data))})
		case !a.present:
			changes = append(changes, gitStateChange{path: p, detail: fmt.Sprintf("deleted (was %d bytes)", len(b.data))})
		case p == before.dirs.commonConfig():
			scope := soleCloneConfig
			if before.dirs.shared {
				scope = sharedCloneConfig
			}
			changes = append(changes, diffGitConfig(p, b.data, a.data, scope))
		case p == before.dirs.worktreeConfig():
			changes = append(changes, diffGitConfig(p, b.data, a.data, checkoutConfig))
		default:
			changes = append(changes, gitStateChange{path: p, detail: fmt.Sprintf("changed (%d → %d bytes)", len(b.data), len(a.data))})
		}
	}
	return changes
}

// gitConfigScope says which watched git config file a diff is for, and so how
// much of its churn ordinary git use outside this suite explains.
type gitConfigScope int

const (
	// checkoutConfig is a checkout's own config.worktree. Nothing but that
	// checkout writes it, so every key change fails the run.
	checkoutConfig gitConfigScope = iota
	// soleCloneConfig is the common config of a clone no other checkout uses.
	soleCloneConfig
	// sharedCloneConfig is the common config of a clone that other worktrees
	// use too; they rewrite it while this suite runs.
	sharedCloneConfig
)

// diffGitConfig names the keys that differ between two versions of a git
// config file of the given scope.
func diffGitConfig(path, before, after string, scope gitConfigScope) gitStateChange {
	change := gitStateChange{path: path}
	b, errBefore := gitConfigEntries(before)
	a, errAfter := gitConfigEntries(after)
	if err := errors.Join(errBefore, errAfter); err != nil {
		change.detail = fmt.Sprintf("changed (%d → %d bytes), not parseable as git config: %v", len(before), len(after), err)
		return change
	}
	var clobbered, routine []string
	for _, key := range changedConfigKeys(b, a) {
		if routineConfigKey(key, b, a, scope) {
			routine = append(routine, describeConfigKey(key, b, a))
		} else {
			clobbered = append(clobbered, describeConfigKey(key, b, a))
		}
	}
	switch {
	case len(clobbered) > 0:
		change.detail = summarizeConfigKeys(clobbered)
		if len(routine) > 0 {
			change.detail += fmt.Sprintf("; also %d routine key change(s)", len(routine))
		}
	case len(routine) > 0:
		change.routine = true
		change.detail = summarizeConfigKeys(routine)
	default:
		// Same keys and values in different bytes: an empty section left
		// behind by a removal, or reformatting. Nothing git reads changed.
		change.routine = true
		change.detail = "rewritten with no key or value change"
	}
	return change
}

// gitConfigEntries parses git config file contents with git itself. `git
// config --file -` reads only stdin (no includes, no -c or env config), and
// each key maps to its raw records in file order, so a multi-valued key
// compares as a list.
func gitConfigEntries(data string) (map[string][]string, error) {
	cmd := gitCommand("config", "--file", "-", "--list", "-z")
	cmd.Dir = os.TempDir()
	cmd.Stdin = strings.NewReader(data)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	entries := map[string][]string{}
	for _, record := range strings.Split(string(out), "\x00") {
		if record == "" {
			continue
		}
		key, _, _ := strings.Cut(record, "\n")
		entries[key] = append(entries[key], record)
	}
	return entries, nil
}

func changedConfigKeys(before, after map[string][]string) []string {
	var keys []string
	for key, values := range before {
		if !slices.Equal(values, after[key]) {
			keys = append(keys, key)
		}
	}
	for key := range after {
		if _, ok := before[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// routineConfigKey reports whether ordinary git use outside this suite
// explains a change to key in a config file of the given scope, so that it
// only draws a note. Nothing in a checkout's own config.worktree is routine,
// and no clobber key is anywhere. In the common config of any clone, branch
// tracking is (branch.<name>.*, from checkout -b, push -u or branch -d in any
// checkout), and so is a remote other than origin added or removed whole
// (remote.<name>.* where that remote exists on only one side). Other
// worktrees of a shared clone also rewrite keys like user.*, merge.*,
// rerere.* or beads.role while this suite runs, so in a shared clone's common
// config every key but the clobber keys is routine.
func routineConfigKey(key string, before, after map[string][]string, scope gitConfigScope) bool {
	section, subsection, name := splitConfigKey(key)
	switch {
	case scope == checkoutConfig, clobberConfigKey(section, subsection, name):
		return false
	case scope == sharedCloneConfig:
		return true
	case subsection == "":
		return false
	case section == "branch":
		return true
	case section == "remote":
		return !hasConfigSubsection(before, section, subsection) || !hasConfigSubsection(after, section, subsection)
	}
	return false
}

// clobberConfigKey reports whether a common-config key is one whose change
// fails the run even in a shared clone: a key the r880 clobber rewrote, or
// one of its kind, which changes how git runs in every worktree of the
// clone. These are core.bare, core.worktree, core.hooksPath, every
// extensions.* key, and every key of origin. That includes origin itself
// appearing or disappearing: the clone's worktrees do not add or remove it,
// while `git remote add origin` and `git remote remove origin` are common
// fixture steps.
func clobberConfigKey(section, subsection, name string) bool {
	switch section {
	case "core":
		return subsection == "" && (name == "bare" || name == "worktree" || name == "hookspath")
	case "extensions":
		return true
	case "remote":
		return subsection == "origin"
	}
	return false
}

// splitConfigKey splits a `git config --list` key into section, subsection and
// variable name. A subsection may contain dots; section and name cannot.
func splitConfigKey(key string) (section, subsection, name string) {
	first, last := strings.IndexByte(key, '.'), strings.LastIndexByte(key, '.')
	if first < 0 || first == last {
		section, name, _ = strings.Cut(key, ".")
		return section, "", name
	}
	return key[:first], key[first+1 : last], key[last+1:]
}

func hasConfigSubsection(entries map[string][]string, section, subsection string) bool {
	for key := range entries {
		if s, sub, _ := splitConfigKey(key); s == section && sub == subsection {
			return true
		}
	}
	return false
}

func describeConfigKey(key string, before, after map[string][]string) string {
	_, had := before[key]
	_, has := after[key]
	switch {
	case !had:
		return key + " added"
	case !has:
		return key + " removed"
	}
	return key + " changed"
}

// summarizeConfigKeys joins key descriptions, capped so a sibling deleting a
// few hundred branches cannot flood the test log.
func summarizeConfigKeys(descriptions []string) string {
	const limit = 12
	if len(descriptions) <= limit {
		return strings.Join(descriptions, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(descriptions[:limit], ", "), len(descriptions)-limit)
}

// reportGitStateChanges renders the guard's verdict on changes. Routine
// changes get a NOTE and leave the run green: another process using the clone
// could have made them, as could this suite. Anything else is an
// ERROR that fails the run, and the pre-run copies of the affected files are
// saved under saveDir so a clobbered config can be restored by hand.
func reportGitStateChanges(changes []gitStateChange, before repoGitState, saveDir string) (report string, failed bool) {
	var routine, clobbered []gitStateChange
	for _, c := range changes {
		if c.routine {
			routine = append(routine, c)
		} else {
			clobbered = append(clobbered, c)
		}
	}
	var out strings.Builder
	if len(routine) > 0 {
		fmt.Fprintf(&out, "NOTE: another process using this clone, or this suite, changed its git config "+
			"during the run; ordinary git use outside the suite makes these changes, so they do not fail "+
			"the run (values not shown):\n%s\n", formatGitStateChanges(routine))
	}
	if len(clobbered) > 0 {
		saved := "pre-run copies of the changed files: "
		if dir, err := saveGitStateBefore(saveDir, before, clobbered); err != nil {
			saved = fmt.Sprintf("could not save pre-run copies of the changed files: %v", err)
		} else {
			saved += dir
		}
		fmt.Fprintf(&out, "ERROR: test suite modified this checkout's own git state "+
			"(the wy-qlvlmf host-clobber class — a test ran git against the checkout instead of a fixture, "+
			"unless another process rewrote the clone during the run; values not shown):\n%s\n%s\n",
			formatGitStateChanges(clobbered), saved)
	}
	return out.String(), len(clobbered) > 0
}

func formatGitStateChanges(changes []gitStateChange) string {
	lines := make([]string, 0, len(changes))
	for _, c := range changes {
		lines = append(lines, fmt.Sprintf("- %s: %s", c.path, c.detail))
	}
	return strings.Join(lines, "\n")
}

// saveGitStateBefore copies the pre-run contents of each changed file that
// existed before the run into a new private directory under dir and returns
// that directory. Each copy is named after its original absolute path.
func saveGitStateBefore(dir string, before repoGitState, changes []gitStateChange) (string, error) {
	out, err := os.MkdirTemp(dir, "bd-test-git-state-before-*")
	if err != nil {
		return "", err
	}
	flatten := strings.NewReplacer("/", "_", `\`, "_", ":", "_")
	for _, c := range changes {
		f := before.files[c.path]
		if !f.present {
			continue
		}
		if err := os.WriteFile(filepath.Join(out, flatten.Replace(c.path)), []byte(f.data), 0o600); err != nil {
			return out, err
		}
	}
	return out, nil
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
	cmd := gitCommand("-C", repoRoot, "rev-parse", "--git-dir", "--git-common-dir")
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
