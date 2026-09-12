package main

import (
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/routing"
)

func activeRepoPathForRouting() string {
	rc, err := beads.GetRepoContext()
	if err == nil && rc != nil && rc.RepoRoot != "" {
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
		if explicitBeadsSelection() || !rc.IsRedirected {
			return rc.RepoRoot
		}
		if rc.CWDRepoRoot != "" {
			return rc.CWDRepoRoot
		}
		return "."
	}
	if beadsDir := beads.FindBeadsDir(); beadsDir != "" {
		return filepath.Dir(beadsDir)
	}
	return "."
}

func detectUserRoleForActiveRepo() (routing.UserRole, error) {
	return routing.DetectUserRole(activeRepoPathForRouting())
}
