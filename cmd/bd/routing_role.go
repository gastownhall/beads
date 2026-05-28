package main

import (
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/routing"
)

func activeRepoPathForRouting() string {
	rc, err := beads.GetRepoContext()
	if err == nil && rc != nil && rc.RepoRoot != "" {
		return rc.RepoRoot
	}
	if beadsDir := beads.FindBeadsDir(); beadsDir != "" {
		return filepath.Dir(beadsDir)
	}
	return "."
}

func detectUserRoleForActiveRepo() (routing.UserRole, error) {
	return routing.DetectUserRole(activeRepoPathForRouting())
}
