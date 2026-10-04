package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// gitContext holds cached git repository information.
// All fields are populated with a single git call for efficiency.
type gitContext struct {
	// gitDirRaw is --git-dir in Git's own spelling, which is relative for an
	// ordinary repository root (".git"). Unlike commonDir and repoRoot it is
	// NOT anchored to the directory the git call ran in, so it is meaningful
	// only relative to that directory; GetGitDir preserves the spelling for its
	// existing callers. Anchor it (absoluteGitPath) before exposing it from a
	// per-directory resolver, or a caller resolving it against the process
	// working directory will name a different repository's git directory.
	gitDirRaw  string
	commonDir  string // Result of --git-common-dir (absolute, anchored to the git call's directory)
	repoRoot   string // Result of --show-toplevel (normalized, symlinks resolved)
	isWorktree bool   // Derived: anchored gitDirRaw != commonDir
	err        error  // Any error during initialization
}

var (
	gitCtxOnce sync.Once
	gitCtx     gitContext

	// pinnedRootForTesting is the root directory PinNoRepositoryUnderForTesting
	// was given, or "" when no pin is active. Deliberately NOT cleared by
	// ResetCaches: see that function's comment and PinNoRepositoryUnderForTesting's
	// doc for why the pin must outlive a cache reset to do its job.
	pinnedRootForTesting string
	// pinnedRootRawForTesting is the pin as given (absolute but not
	// symlink-resolved). underPinnedRootForTesting matches against both forms
	// so canonicalization only ever widens the fence, never narrows it.
	pinnedRootRawForTesting string
)

// underPinnedRootForTesting reports whether wd is pinnedRootForTesting itself
// or a descendant of it. Called with the live working directory on every
// getGitContext lookup while a pin is active, so a test that chdirs outside
// the pinned root (e.g. into its own t.TempDir() fixture) still gets real git
// detection scoped to that fixture.
//
// Both sides are compared in canonical form (see canonicalPinPath): on macOS
// t.TempDir() and os.TempDir() live under /var/folders/..., but /var is a
// symlink to /private/var and getcwd(2) reports the resolved
// /private/var/folders/... path, so a raw prefix test never matched and the
// pin silently did nothing there.
func underPinnedRootForTesting(wd string) bool {
	if pinnedRootForTesting == "" || wd == "" {
		return false
	}
	canonicalWD := canonicalPinPath(wd)
	for _, root := range []string{pinnedRootForTesting, pinnedRootRawForTesting} {
		if root == "" {
			continue
		}
		for _, dir := range []string{wd, canonicalWD} {
			if dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

// canonicalPinPath returns p as an absolute, symlink-resolved, cleaned path,
// falling back to the best form available when resolution fails (e.g. the
// path no longer exists), so the pin comparison is never stricter than the
// raw strings.
func canonicalPinPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return filepath.Clean(p)
}

// initGitContext populates the gitContext with a single git call.
// This is called once per process via sync.Once.
func initGitContext() {
	gitCtx = loadGitContext("", nil)
}

func loadGitContext(workDir string, env []string) gitContext {
	var ctx gitContext
	// Get all three values with a single git call
	cmd := exec.Command("git", "rev-parse", "--git-dir", "--git-common-dir", "--show-toplevel")
	cmd.Dir, cmd.Env = workDir, env
	output, err := cmd.Output()
	if err != nil {
		if workDir != "" {
			ctx.err = fmt.Errorf("resolve Git working tree: %w", err)
			var exit *exec.ExitError
			if errors.As(err, &exit) && len(exit.Stderr) > 0 {
				ctx.err = fmt.Errorf("%w: %s", ctx.err, strings.TrimSpace(string(exit.Stderr)))
			}
		} else {
			ctx.err = fmt.Errorf("not a git repository: %w", err)
		}
		return ctx
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 3 {
		ctx.err = fmt.Errorf("unexpected git rev-parse output: got %d lines, expected 3", len(lines))
		return ctx
	}

	ctx.gitDirRaw = strings.TrimSpace(lines[0])
	commonDirRaw := strings.TrimSpace(lines[1])
	repoRootRaw := strings.TrimSpace(lines[2])

	// Convert commonDir to absolute for reliable comparison
	absCommon, err := absoluteGitPath(workDir, commonDirRaw)
	if err != nil {
		ctx.err = fmt.Errorf("failed to resolve common dir path: %w", err)
		return ctx
	}
	ctx.commonDir = absCommon

	// Convert the raw gitDir to absolute for worktree comparison
	absGitDir, err := absoluteGitPath(workDir, ctx.gitDirRaw)
	if err != nil {
		ctx.err = fmt.Errorf("failed to resolve git dir path: %w", err)
		return ctx
	}

	// Derive isWorktree from comparing absolute paths
	ctx.isWorktree = absGitDir != absCommon

	// Process repoRoot: normalize Windows paths, resolve symlinks,
	// and canonicalize case on case-insensitive filesystems (GH#880).
	// This is critical for git worktree operations which string-compare paths.
	repoRoot := NormalizePath(repoRootRaw)
	if resolved, err := filepath.EvalSymlinks(repoRoot); err == nil {
		repoRoot = resolved
	}
	// Canonicalize case on macOS/Windows (GH#880)
	if canonicalized := canonicalizeCase(repoRoot); canonicalized != "" {
		repoRoot = canonicalized
	}
	ctx.repoRoot = repoRoot
	return ctx
}

// absoluteGitPath anchors relative Git output to the command directory.
func absoluteGitPath(workDir, path string) (string, error) {
	if workDir != "" && !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	return filepath.Abs(path)
}

// HooksContext is a detached snapshot of a working repository's hook paths.
// MainRepoRoot carries GetMainRepoRoot's definition unchanged: for a linked
// worktree it is the parent of the shared Git directory, which is the main work
// tree only when that directory is a conventional ".git" inside it. For a
// worktree of a bare repository the parent is merely the directory holding the
// bare repository, so MainRepoRoot is not a repository and has no work tree
// there; callers that anchor installs at it must tolerate that case.
type HooksContext struct {
	HooksDir, CommonDir, RepoRoot, MainRepoRoot string
}

// normalizeHooksWorkDir anchors workDir and normalizes its symlinks and case
// once. Every HooksContext producer shares it so they cannot drift into emitting
// different spellings of the same directory: worktree code string-compares these
// paths (GH#880).
// The caller-supplied directory must resolve before it can select a repo:
// unlike the discovered repoRoot spelling in loadGitContext, it is an input
// to Git.
func normalizeHooksWorkDir(workDir string) (string, error) {
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	// This caller-supplied directory must resolve before it can select a repo.
	// Unlike the discovered repoRoot spelling, it is an input to Git.
	workDir, err = filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve hooks working directory: %w", err)
	}
	if canonical := canonicalizeCase(workDir); canonical != "" {
		workDir = canonical
	}
	return workDir, nil
}

// ResolveHooksContext reads fresh paths without changing the legacy cache.
// workDir must be nonempty and is anchored and symlink/case-normalized once;
// bare/non-repositories error. Configured absolute hook paths retain their spelling.
// Nil env inherits; a nonnil env is supplied unchanged, including an empty one.
// The call neither mutates nor retains env; callers must not change it during the call.
// Routing variables are honored, not filtered. Tilde expansion uses the Go
// process's home directory, independently of HOME supplied to child Git.
// A sandbox HOME therefore does not redirect a configured ~/ hook path: callers
// that install there would still write under the Go process's home directory.
//
// This is the explicit-context sibling of GetGitHooksDir. It returns a struct
// instead of following this file's GetXFrom(startDir) convention because all
// four paths must come from one resolution of one directory. The first in-repo
// caller is the selected-hook setup in cmd/bd/init_git_hooks.go (GH#6440).
func ResolveHooksContext(workDir string, env []string) (HooksContext, error) {
	if workDir == "" {
		return HooksContext{}, fmt.Errorf("hooks context requires a working directory")
	}
	workDir, err := normalizeHooksWorkDir(workDir)
	if err != nil {
		return HooksContext{}, err
	}
	ctx := loadGitContext(workDir, env)
	if ctx.err != nil {
		return HooksContext{}, ctx.err
	}
	cmd := exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir, cmd.Env = workDir, env
	hooksDir, err := gitHooksDir(cmd, func() (*gitContext, error) { return &ctx, nil })
	if err != nil {
		return HooksContext{}, err
	}
	return HooksContext{HooksDir: hooksDir, CommonDir: ctx.commonDir,
		RepoRoot: ctx.repoRoot, MainRepoRoot: ctx.mainRepoRoot()}, nil
}

// ResolveWorkTreelessHooksContext resolves hook paths for a repository whose
// common directory resolves but whose work tree does not: a bare repository, or
// a directory such as a repository's own .git that Git answers from while
// reporting no work tree. ResolveHooksContext requires one, because it cannot
// honor its RepoRoot contract without it; this is the explicit counterpart, so a
// caller opts into the weaker shape instead of silently receiving one. RepoRoot
// and MainRepoRoot are empty — there is no work-tree root — and a relative
// core.hooksPath is anchored to the common directory, which is where Git runs
// hooks without a work tree.
// It fails for non-repositories and for directories inside a work tree alike, so
// callers can use it as a fallback after ResolveHooksContext without widening
// that call's failure contract. workDir and env are handled exactly as
// ResolveHooksContext handles them, anchoring and normalization included.
func ResolveWorkTreelessHooksContext(workDir string, env []string) (HooksContext, error) {
	if workDir == "" {
		return HooksContext{}, fmt.Errorf("hooks context requires a working directory")
	}
	workDir, err := normalizeHooksWorkDir(workDir)
	if err != nil {
		return HooksContext{}, err
	}
	probe := exec.Command("git", "rev-parse", "--is-inside-work-tree", "--git-common-dir")
	probe.Dir, probe.Env = workDir, env
	output, err := probe.Output()
	if err != nil {
		return HooksContext{}, fmt.Errorf("resolve work-tree-less Git repository: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return HooksContext{}, fmt.Errorf("unexpected git rev-parse output: got %d lines, expected 2", len(lines))
	}
	// A work tree resolves here, so ResolveHooksContext's stronger contract is
	// the one that applies and its error is the one the caller should surface.
	if strings.TrimSpace(lines[0]) == "true" {
		return HooksContext{}, fmt.Errorf("%s is inside a Git work tree", workDir)
	}
	commonDir, err := absoluteGitPath(workDir, strings.TrimSpace(lines[1]))
	if err != nil {
		return HooksContext{}, fmt.Errorf("failed to resolve common dir path: %w", err)
	}
	cmd := exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir, cmd.Env = workDir, env
	// Without a work tree Git runs hooks in the common directory, so it anchors a
	// relative core.hooksPath in place of the absent work-tree root.
	ctx := gitContext{gitDirRaw: commonDir, commonDir: commonDir, repoRoot: commonDir}
	hooksDir, err := gitHooksDir(cmd, func() (*gitContext, error) { return &ctx, nil })
	if err != nil {
		return HooksContext{}, err
	}
	return HooksContext{HooksDir: hooksDir, CommonDir: commonDir}, nil
}

// getGitContext returns the cached git context, initializing it if needed.
func getGitContext() (*gitContext, error) {
	if pinnedRootForTesting != "" {
		if wd, err := os.Getwd(); err == nil && underPinnedRootForTesting(wd) {
			return nil, errPinnedNoRepository
		}
	}
	gitCtxOnce.Do(initGitContext)
	if gitCtx.err != nil {
		return nil, gitCtx.err
	}
	return &gitCtx, nil
}

// GetGitDir returns the actual .git directory path for the current repository.
// In a normal repo, this is ".git". In a worktree, .git is a file
// containing "gitdir: /path/to/actual/git/dir", so we use git rev-parse.
//
// This function uses Git's native worktree-aware APIs and should be used
// instead of direct filepath.Join(path, ".git") throughout the codebase.
func GetGitDir() (string, error) {
	ctx, err := getGitContext()
	if err != nil {
		return "", err
	}
	return ctx.gitDirRaw, nil
}

// GetGitCommonDir returns the common git directory shared across all worktrees.
// For regular repos, this equals GetGitDir(). For worktrees, this returns
// the main repository's .git directory where shared data (like worktree
// registrations, hooks, and objects) lives.
//
// Use this instead of GetGitDir() when you need to create new worktrees or
// access shared git data that should not be scoped to a single worktree.
// GH#639: This is critical for bare repo setups where GetGitDir() returns
// a worktree-specific path that cannot host new worktrees.
func GetGitCommonDir() (string, error) {
	ctx, err := getGitContext()
	if err != nil {
		return "", err
	}
	return ctx.commonDir, nil
}

// GetGitHooksDir returns the path to the Git hooks directory.
// This function is worktree-aware: hooks are shared across all worktrees
// and live in the common git directory (e.g., /repo/.git/hooks), not in
// the worktree-specific directory (e.g., /repo/.git/worktrees/feature/hooks).
func GetGitHooksDir() (string, error) {
	return gitHooksDir(exec.Command("git", "config", "--get", "core.hooksPath"), getGitContext)
}

// GetGitHooksDirFrom resolves a fresh hook path without reading the legacy cache.
// Like GetGitHooksDir, an absolute configured path needs no working repository.
// Routing and supplied env are honored unchanged; tilde uses the process home.
func GetGitHooksDirFrom(workDir string, env []string) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("hooks path requires a working directory")
	}
	workDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	workDir, err = filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve hooks working directory: %w", err)
	}
	if canonical := canonicalizeCase(workDir); canonical != "" {
		workDir = canonical
	}
	cmd := exec.Command("git", "config", "--get", "core.hooksPath")
	cmd.Dir, cmd.Env = workDir, env
	return gitHooksDir(cmd, func() (*gitContext, error) {
		ctx := loadGitContext(workDir, env)
		return &ctx, ctx.err
	})
}

func gitHooksDir(cmd *exec.Cmd, context func() (*gitContext, error)) (string, error) {
	// Respect core.hooksPath if configured.
	// This is used by beads' Dolt backend (hooks installed to .beads/hooks/).
	if out, err := cmd.Output(); err == nil {
		hooksPath := strings.TrimSpace(string(out))
		if hooksPath != "" {
			// Expand tilde — git config may return ~/... which Go doesn't expand.
			// Without this, Windows treats "~/.githooks" as a relative path and
			// joins it to the repo root, creating a literal "~" directory. (GH#1796)
			if strings.HasPrefix(hooksPath, "~/") || strings.HasPrefix(hooksPath, "~\\") {
				if home, err := os.UserHomeDir(); err == nil {
					hooksPath = filepath.Join(home, hooksPath[2:])
				}
			} else if hooksPath == "~" {
				if home, err := os.UserHomeDir(); err == nil {
					hooksPath = home
				}
			}

			if filepath.IsAbs(hooksPath) {
				return hooksPath, nil
			}
			ctx, err := context()
			if err != nil {
				return "", err
			}
			// Git treats relative core.hooksPath as relative to the repo root in common usage.
			// (e.g., ".beads/hooks", ".githooks").
			p := filepath.Join(ctx.repoRoot, hooksPath)
			if abs, err := filepath.Abs(p); err == nil {
				return abs, nil
			}

			return p, nil
		}
	}

	// Default: hooks are stored in the common git directory.
	ctx, err := context()
	if err != nil {
		return "", err
	}
	return filepath.Join(ctx.commonDir, "hooks"), nil
}

// GetGitRefsDir returns the path to the Git refs directory.
// This function is worktree-aware and handles both regular repos and worktrees.
func GetGitRefsDir() (string, error) {
	gitDir, err := GetGitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, "refs"), nil
}

// GetGitHeadPath returns the path to the Git HEAD file.
// This function is worktree-aware and handles both regular repos and worktrees.
func GetGitHeadPath() (string, error) {
	gitDir, err := GetGitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, "HEAD"), nil
}

// IsWorktree returns true if the current directory is in a Git worktree.
// This is determined by comparing --git-dir and --git-common-dir.
// The result is cached after the first call since worktree status doesn't
// change during a single command execution.
func IsWorktree() bool {
	ctx, err := getGitContext()
	if err != nil {
		return false
	}
	return ctx.isWorktree
}

// GetMainRepoRoot returns the main repository root directory.
// When in a worktree, this returns the main repository root.
// Otherwise, it returns the regular repository root.
//
// For nested worktrees (worktrees located under the main repo, e.g.,
// /project/.worktrees/feature/), this correctly returns the main repo
// root (/project/) by using git rev-parse --git-common-dir which always
// points to the main repo's .git directory. (GH#509)
// The result is cached after the first call.
func GetMainRepoRoot() (string, error) {
	ctx, err := getGitContext()
	if err != nil {
		return "", err
	}
	return ctx.mainRepoRoot(), nil
}

func (ctx *gitContext) mainRepoRoot() string {
	if ctx.isWorktree {
		// For worktrees, the main repo root is the parent of the shared .git directory.
		return filepath.Dir(ctx.commonDir)
	}

	// For regular repos (including submodules), repoRoot is the correct root.
	return ctx.repoRoot
}

// GetRepoRoot returns the root directory of the current git repository.
// Returns empty string if not in a git repository.
//
// This function is worktree-aware and handles Windows path normalization
// (Git on Windows may return paths like /c/Users/... or C:/Users/...).
// It also resolves symlinks to get the canonical path.
// The result is cached after the first call.
func GetRepoRoot() string {
	ctx, err := getGitContext()
	if err != nil {
		return ""
	}
	return ctx.repoRoot
}

// canonicalizeCase resolves a path to its true filesystem case on
// case-insensitive filesystems (macOS/Windows). This is needed because
// git operations string-compare paths exactly - a path with wrong case
// will fail even though it points to the same location. (GH#880)
//
// On macOS, uses realpath(1) which returns the canonical case.
// Returns empty string if resolution fails or isn't needed.
func canonicalizeCase(path string) string {
	if runtime.GOOS == "darwin" {
		// Use realpath to get canonical path with correct case
		cmd := exec.Command("realpath", path)
		output, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(output))
		}
	}
	// Windows: filepath.EvalSymlinks already handles case
	// Linux: case-sensitive, no canonicalization needed
	return ""
}

// NormalizePath converts Git's Windows path formats to native format.
// Git on Windows may return paths like /c/Users/... or C:/Users/...
// This function converts them to native Windows format (C:\Users\...).
// On non-Windows systems, this is a no-op.
func NormalizePath(path string) string {
	// Only apply Windows normalization on Windows
	if filepath.Separator != '\\' {
		return path
	}

	// Convert /c/Users/... to C:\Users\...
	if len(path) >= 3 && path[0] == '/' && path[2] == '/' {
		return strings.ToUpper(string(path[1])) + ":" + filepath.FromSlash(path[2:])
	}

	// Convert C:/Users/... to C:\Users\...
	return filepath.FromSlash(path)
}

// ResetCaches resets all cached git information. This is intended for use
// by tests that need to change directory between subtests.
// In production, these caches are safe because the working directory
// doesn't change during a single command execution.
//
// Deliberately does NOT clear a PinNoRepositoryUnderForTesting pin: that pin
// is directory-scoped (re-evaluated against the live working directory on
// every call, not baked into the one-shot gitCtx this function clears), so a
// test fixture that chdirs out of the pinned root, calls ResetCaches, and
// does real git work there is unaffected — and a test that chdirs back under
// the pinned root and calls ResetCaches on the way out (e.g. cmd/bd's
// runInDir/resetRepoCachesForTest idiom) must keep answering "not a
// repository", or every later test in the binary loses the fence the pin
// exists to provide. See PinNoRepositoryUnderForTesting.
//
// WARNING: Not thread-safe. Only call from single-threaded test contexts.
func ResetCaches() {
	gitCtxOnce = sync.Once{}
	gitCtx = gitContext{}
}

// errPinnedNoRepository is returned by getGitContext for any working
// directory PinNoRepositoryUnderForTesting pinned.
var errPinnedNoRepository = errors.New("not a git repository (pinned for testing)")

// PinNoRepositoryUnderForTesting pins "not a git repository" for root and
// every directory under it, so IsWorktree, GetRepoRoot, and GetMainRepoRoot
// answer as if no repository were present for any process working directory
// at or below root — including across ResetCaches, unlike the one-shot
// sentinel this replaced.
//
// This exists for whole-binary test fencing (GH#7145-style cmd/bd pollution):
// without it, the first cached-git-context call made anywhere in a test
// binary — before any individual test has had a chance to chdir into its own
// fixture — permanently answers for the rest of the process from whatever
// repository the binary happened to start in. For a worktree checkout, that
// answer includes a real --git-common-dir, which beads' worktree-fallback
// discovery (FindBeadsDir) treats as license to read and write the main
// checkout's shared .beads database. root should be the repository root the
// test binary started in (not a narrower directory like the package dir),
// so the fence covers any subdirectory of that checkout a test might chdir
// into without leaving it.
//
// The pin is directory-scoped, not a cache snapshot: getGitContext checks
// the live working directory against root on every call, before touching
// gitCtxOnce/gitCtx at all. A test that chdirs to a fixture OUTSIDE root
// (e.g. its own t.TempDir(), which is not nested under a checkout root) and
// calls ResetCaches gets real git detection scoped to that fixture, exactly
// as before this pin existed (see cmd/bd/git_test_helpers.go's runInDir).
// A test that chdirs back under root — including the package directory
// itself, where most tests run without ever chdir'ing away — keeps
// answering "not a repository" even after ResetCaches, because ResetCaches
// does not clear pinnedRootForTesting.
//
// This lives in production code rather than a _test.go file (like
// ResetCaches, which it pairs with) only so a test binary's TestMain can call
// it: TestMain runs in the package under test, not in a _test.go-only
// helper's package, and Go does not let a non-test file reach a _test.go
// identifier. Treat it exactly like ResetCaches despite that: the only
// intended caller is a TestMain (currently cmd/bd's), called once, before
// m.Run(). Production code must never call this.
//
// WARNING: Not thread-safe, like ResetCaches. Only call before m.Run(),
// before any goroutines that might read the git context are started.
func PinNoRepositoryUnderForTesting(root string) {
	pinnedRootRawForTesting = root
	if abs, err := filepath.Abs(root); err == nil {
		pinnedRootRawForTesting = abs
	}
	pinnedRootForTesting = canonicalPinPath(root)
	gitCtxOnce = sync.Once{}
	gitCtx = gitContext{}
}

// IsJujutsuRepo returns true if the current directory is in a jujutsu (jj) repository.
// Jujutsu stores its data in a .jj directory at the repository root.
func IsJujutsuRepo() bool {
	_, err := GetJujutsuRoot()
	return err == nil
}

// IsColocatedJJGit returns true if this is a colocated jujutsu+git repository.
// Colocated repos have both .jj and .git directories, created via `jj git init --colocate`.
// In colocated repos, git hooks work normally since jj manages the git repo.
func IsColocatedJJGit() bool {
	if !IsJujutsuRepo() {
		return false
	}
	// If we're also in a git repo, it's colocated
	_, err := getGitContext()
	return err == nil
}

// JJSecondaryWorkspaceRoot returns the secondary workspace root and true if
// CWD is inside a jujutsu secondary workspace; otherwise ("", false).
// Secondary workspaces have .jj/repo as a file (pointer to the primary's repo
// directory) rather than a directory.
func JJSecondaryWorkspaceRoot() (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	return JJSecondaryWorkspaceRootFrom(cwd)
}

// JJSecondaryWorkspaceRootFrom is the path-aware variant of
// JJSecondaryWorkspaceRoot: it resolves the jujutsu root starting from startDir
// rather than the current working directory.
func JJSecondaryWorkspaceRootFrom(startDir string) (string, bool) {
	jjRoot, err := getJujutsuRootFrom(startDir)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(filepath.Join(jjRoot, ".jj", "repo"))
	if err != nil || info.IsDir() {
		return "", false
	}
	return jjRoot, true
}

// IsJJSecondaryWorkspace returns true if CWD is inside a jujutsu secondary workspace.
func IsJJSecondaryWorkspace() bool {
	_, ok := JJSecondaryWorkspaceRoot()
	return ok
}

// GetJJPrimaryWorkspaceRoot returns the root directory of the primary jujutsu
// workspace when called from inside a secondary workspace. The secondary's
// .jj/repo file contains a path (relative or absolute) to the primary's
// .jj/repo directory; the primary workspace root is two levels above that.
//
// Returns an error if not in a jj secondary workspace or the path cannot be resolved.
func GetJJPrimaryWorkspaceRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}
	return GetJJPrimaryWorkspaceRootFrom(cwd)
}

// GetJJPrimaryWorkspaceRootFrom is the path-aware variant of
// GetJJPrimaryWorkspaceRoot: it resolves the jujutsu root starting from startDir
// rather than the current working directory.
func GetJJPrimaryWorkspaceRootFrom(startDir string) (string, error) {
	jjRoot, err := getJujutsuRootFrom(startDir)
	if err != nil {
		return "", err
	}

	// #nosec G304 -- .jj/repo path is within a jujutsu workspace we located by walking from cwd
	content, err := os.ReadFile(filepath.Join(jjRoot, ".jj", "repo"))
	if err != nil {
		return "", fmt.Errorf("failed to read .jj/repo: %w", err)
	}

	target := strings.TrimSpace(string(content))
	if target == "" {
		return "", fmt.Errorf(".jj/repo is empty")
	}

	// .jj/repo content is relative to the .jj/ directory, or absolute.
	var primaryRepoFile string
	if filepath.IsAbs(target) {
		primaryRepoFile = target
	} else {
		primaryRepoFile = filepath.Join(jjRoot, ".jj", target)
	}

	primaryRepoFile, err = filepath.Abs(primaryRepoFile)
	if err != nil {
		return "", fmt.Errorf("failed to resolve jj primary workspace path: %w", err)
	}

	// primaryRepoFile == <primary-root>/.jj/repo  →  root is Dir(Dir(that))
	primaryRoot := filepath.Dir(filepath.Dir(primaryRepoFile))

	if resolved, err := filepath.EvalSymlinks(primaryRoot); err == nil {
		primaryRoot = resolved
	}
	if canonical := canonicalizeCase(primaryRoot); canonical != "" {
		primaryRoot = canonical
	}

	return primaryRoot, nil
}

// GetJujutsuRoot returns the root directory of the jujutsu repository.
// Returns empty string and error if not in a jujutsu repository.
//
// Walking stops at a .git boundary: a git repo nested inside a JJ
// workspace (e.g. a clean-room scratch repo under a JJ-tracked parent) must not
// inherit the parent's JJ context.  Only the directory that contains .git itself
// is checked for a co-located .jj; we never walk further up.
func GetJujutsuRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}
	return getJujutsuRootFrom(cwd)
}

// getJujutsuRootFrom walks up from startDir to find the jujutsu root, applying
// the same .git-boundary rule as GetJujutsuRoot. startDir is made absolute first
// so that a relative input (e.g. ".") walks correctly rather than stalling at
// filepath.Dir(".") == ".".
func getJujutsuRootFrom(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve start directory: %w", err)
	}

	for {
		jjPath := filepath.Join(dir, ".jj")
		if info, err := os.Stat(jjPath); err == nil && info.IsDir() {
			return dir, nil
		}

		// Stop at a git repo boundary. If .git exists here but no .jj was
		// found at this level, this is a plain git repo (not JJ). Do not
		// walk further up — the parent may have .jj but it belongs to a
		// different (ancestor) repository.
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			break
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("not a jujutsu repository (no .jj directory found)")
}
