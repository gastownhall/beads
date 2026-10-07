package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/routing"
)

// activeRepoPathForRouting returns the repository whose beads.role governs
// routing for this command.
//
// NOTE: internal/beads owns a second, independent role oracle,
// RepoContext.Role() (internal/beads/context.go), which short-circuits to
// Contributor whenever the beads dir is redirected or external. It is a known
// divergence from the doctrine below — `bd context` can print Contributor while
// `bd create` routes with the selected repo's beads.role — that pre-dates this
// helper; unifying the two oracles is out of scope here.
func activeRepoPathForRouting() string {
	rc, err := beads.GetRepoContext()
	if err != nil || rc == nil || rc.RepoRoot == "" {
		if beadsDir := beads.FindBeadsDir(); beadsDir != "" {
			return logRolePath("beads-dir-parent", filepath.Dir(beadsDir), false)
		}
		return logRolePath("cwd-no-repo-context", ".", false)
	}

	// Two ways BeadsDir can resolve outside the CWD's repo, with opposite
	// answers for role detection:
	//   - explicit selection (bd -C, or the user exported BEADS_DIR before
	//     bd ran): the user chose that beads project, so its repo root is
	//     the active repo. This is the GH#4242 fix.
	//   - a .beads/redirect followed from the CWD workspace: the redirect
	//     relocates STORAGE, not the project — beads.role belongs to the
	//     workspace repo the user is operating in, not to wherever its
	//     data happens to live (which may not even be a git repo).
	// The live BEADS_DIR env var cannot make this distinction: normal
	// command startup (prepareSelectedCommandContext) sets it to the
	// resolved target for every command, redirects included, so only
	// startup provenance separates user selection from discovery.
	if explicitBeadsSelection() {
		// A selection is not automatically a selection OF the storage root.
		// Every resolver follows a .beads/redirect before this guard runs
		// (ExplicitBeadsDir, FindBeadsDir, and -C's own resolution), so an
		// explicit selection of a REDIRECTED workspace arrives here already
		// rewritten to the storage root — the same misroute GH#4242 reported,
		// one resolution step further in. Recover the workspace the user
		// actually named before honoring the selection.
		if workspace := selectedWorkspaceRepoRoot(rc); workspace != "" {
			return logRolePath("explicit-selection-through-redirect", workspace, rc.IsRedirected)
		}
		return logRolePath("explicit-selection", rc.RepoRoot, rc.IsRedirected)
	}
	if !rc.IsRedirected {
		return logRolePath("cwd-repo", rc.RepoRoot, false)
	}
	if rc.CWDRepoRoot != "" {
		return logRolePath("redirect-workspace", rc.CWDRepoRoot, true)
	}
	return logRolePath("cwd-redirected-no-workspace-repo", ".", true)
}

// selectedWorkspaceRepoRoot returns the repo root of the workspace the user
// explicitly selected, but only when that selection resolved THROUGH a
// .beads/redirect and so no longer names the project that was selected. It
// returns "" when the selection genuinely names its own project's storage, in
// which case rc.RepoRoot is already the right answer and must be kept — that is
// the GH#4242 fix this helper must not undo.
func selectedWorkspaceRepoRoot(rc *beads.RepoContext) string {
	// `bd -C dir`: the selection is the -C argument, and it must be read
	// PRE-redirect. applyChangeDirSelection has already rewritten BEADS_DIR to
	// the resolved target, and GetRedirectInfo would answer about the process
	// CWD — an unrelated directory under `bd -C` — so ask about dir itself.
	// GetRedirectInfoFrom is documented for exactly this `bd -C dir` case
	// (GH#5509) and deliberately does not consult BEADS_DIR.
	if dir := strings.TrimSpace(changeDir); dir != "" {
		if !beads.GetRedirectInfoFrom(dir).IsRedirected {
			return ""
		}
		return beads.RepoRootFor(dir)
	}

	// Exported BEADS_DIR: the selection is vacuous when it merely names the CWD
	// workspace's own redirect, and it is vacuous under both spellings of it,
	// because ExplicitBeadsDir follows the redirect: BEADS_DIR=<workspace>/.beads
	// and BEADS_DIR=<storage>/.beads both arrive here as the storage dir (the
	// bd-wayc3 tooling shape).
	//
	// "Own" is the redirect discovery finds from the CWD with BEADS_DIR ignored,
	// which is the redirect that keeps role detection on the CWD workspace
	// when BEADS_DIR is unset. GetRedirectInfo cannot stand in for it: when the
	// CWD repo root's .beads holds no redirect it falls back to BEADS_DIR itself.
	// That masks a workspace nested below its repo root, or a git worktree that
	// inherits its main checkout's redirect, since the pre-RunE rebind
	// (prepareSelectedCommandContext) has already replaced BEADS_DIR with the
	// storage dir, and it would let a selected FOREIGN workspace vouch for its
	// own redirect. A foreign redirected selection therefore falls through to
	// rc.RepoRoot, its storage root. Binding the foreign workspace itself would
	// need the raw startup BEADS_DIR, which nothing keeps; that is a known
	// limit, not an oversight.
	own := beads.GetDiscoveredRedirectInfo()
	if !own.IsRedirected || !sameDirPath(beads.ExplicitBeadsDir(), own.TargetDir) {
		return ""
	}
	return rc.CWDRepoRoot
}

// sameDirPath reports whether two directory paths name the same directory,
// tolerating the relative/absolute and symlink differences the various beads-dir
// resolvers introduce (t.TempDir hands back symlinked paths on macOS, and git
// reports physical ones).
func sameDirPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return canonicalDirPath(a) == canonicalDirPath(b)
}

func canonicalDirPath(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs)
}

// logRolePath records which branch answered, then returns the path unchanged.
// A GH#4242-class misroute report hinges on knowing which of the six sources
// role detection used, and the downstream "failed to detect user role" warning
// fires on none of them.
func logRolePath(branch, path string, redirected bool) string {
	debug.Logf("role detection path: %s -> %s (explicit=%v redirected=%v)\n",
		branch, path, explicitBeadsSelection(), redirected)
	return path
}

// rolePathNotARepoWarned limits warnIfRolePathIsNotARepo to one warning per
// process: an ID lookup falls back to auto-routing once per missing ID
// (resolveViaAutoRouting), and every fallback re-runs role detection.
var rolePathNotARepoWarned bool

// warnIfRolePathIsNotARepo warns when role detection is about to read beads.role
// from a path that is not inside a git repository. `git config --get` still
// succeeds there by falling back to the user's GLOBAL config, and
// routing.DetectUserRole returns on that first success, so its own "beads.role
// not configured" notice never fires: without this warning the command would be
// routed with the global role and no diagnostic on any stream.
func warnIfRolePathIsNotARepo(repoPath string) {
	if rolePathNotARepoWarned || beads.RepoRootFor(repoPath) != "" {
		return
	}
	rolePathNotARepoWarned = true
	fmt.Fprintf(os.Stderr, "warning: role detection path %q is not a git repository;\n", repoPath) //nolint:gosec // G705: stderr, not a browser context
	fmt.Fprintln(os.Stderr, "  beads.role will be read from your global git config, which may not match this project.")
	fmt.Fprintln(os.Stderr, "  Fix: set beads.role in the project you intend to route to,")
	fmt.Fprintln(os.Stderr, "       or select that project explicitly with bd -C <dir>.")
}

func detectUserRoleForActiveRepo() (routing.UserRole, error) {
	repoPath := activeRepoPathForRouting()
	warnIfRolePathIsNotARepo(repoPath)
	return routing.DetectUserRole(repoPath)
}

// detectUserRoleForRouting returns the user role for the routing decision cfg
// describes, but detects it only when that decision can depend on it
// (cfg.UsesUserRole), so cfg must already be fully resolved, legacy
// contributor.* fallbacks included. Detection runs git and can print the
// non-repo warning above or routing.DetectUserRole's "beads.role not
// configured" notice; neither belongs on a command whose routing never reads
// the role, which is every command while routing is off (the default). This is
// also what makes `routing.mode explicit` disable role detection entirely, as
// docs/multi-agent/routing.md promises.
func detectUserRoleForRouting(cfg *routing.RoutingConfig) routing.UserRole {
	if !cfg.UsesUserRole() {
		return ""
	}
	userRole, err := detectUserRoleForActiveRepo()
	if err != nil {
		debug.Logf("Warning: failed to detect user role: %v\n", err)
	}
	return userRole
}
