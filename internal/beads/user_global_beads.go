package beads

import (
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/utils"
)

// userHomeForDiscovery returns the canonical user home directory, or "" when
// HOME (USERPROFILE on Windows) is unset, empty or relative: with no home
// there is no user-level .beads to keep out of discovery. It reads the same
// source as the user config path and the shared server directory.
func userHomeForDiscovery() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !filepath.IsAbs(home) {
		return ""
	}
	return utils.CanonicalizePath(home)
}

// skipsUserGlobalBeads reports whether an ancestor walk must pass over the
// .beads candidate in dir because dir is the user's home directory and
// repoRoot, the repository root that bounds the walk ("" for none), is not.
//
// <home>/.beads holds user-level state (the legacy user config.yaml, the
// shared Dolt server), not a project, and the ~/.beads fallback was removed
// from discovery on purpose. Without this skip, any directory under $HOME
// outside a repository adopted it through the walk and wrote to it silently
// (GH#4402). A dotfiles repository rooted at $HOME still owns <home>/.beads,
// and BEADS_DIR, BEADS_DB and --db still name it explicitly.
func skipsUserGlobalBeads(dir, repoRoot string) bool {
	home := userHomeForDiscovery()
	if home == "" || !utils.PathsEqual(dir, home) {
		return false
	}
	// Never canonicalize "": it resolves to the process cwd.
	if repoRoot != "" && utils.PathsEqual(utils.CanonicalizePath(git.NormalizePath(repoRoot)), home) {
		return false
	}
	return true
}

// SkippedUserGlobalBeadsDir returns the canonical <home>/.beads when discovery
// from the cwd passed it over as the user-level beads directory, and it holds
// a workspace (metadata.json, a database, or a redirect) the user may have
// meant to use. It returns "" otherwise, including when the walk from the cwd
// never reaches home (a temp-root or BEADS_CEILING_DIRECTORIES ceiling), when
// the cwd's repository is rooted at home, and when <home>/.beads holds only
// the user-level config.yaml.
//
// Callers that found no workspace use it to say why ~/.beads was not used.
func SkippedUserGlobalBeadsDir() string {
	home := userHomeForDiscovery()
	if home == "" {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if !skipsUserGlobalBeads(home, findGitRoot()) {
		return ""
	}
	reached := false
	walk := NewAncestorDirWalk(cwd, cwd)
	for dir, ok := walk.Next(); ok; dir, ok = walk.Next() {
		if utils.PathsEqual(dir, home) {
			reached = true
			break
		}
	}
	if !reached {
		return ""
	}

	beadsDir := filepath.Join(home, ".beads")
	if info, err := os.Stat(beadsDir); err != nil || !info.IsDir() {
		return ""
	}
	if _, err := os.Stat(filepath.Join(beadsDir, RedirectFileName)); err == nil {
		return beadsDir
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); err == nil {
		return beadsDir
	}
	if hasBeadsDatabase(beadsDir) {
		return beadsDir
	}
	return ""
}
