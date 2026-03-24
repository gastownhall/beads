package main

import (
	"os"
	"path/filepath"
)

// isOrchestratorRoot returns true when path looks like a multi-project
// orchestrator workspace root (not a single-project beads repo).
//
// Detection: presence of both:
//  1. .beads/routes.jsonl (cross-project routing config)
//  2. mayor/ directory (orchestrator agent workspace)
//
// This prevents bd doctor --fix from running at the workspace root,
// where repairs should go through the orchestrator's own doctor command.
func isOrchestratorRoot(path string) bool {
	if path == "" {
		return false
	}

	routes := filepath.Join(path, ".beads", "routes.jsonl")
	mayorDir := filepath.Join(path, "mayor")

	if _, err := os.Stat(routes); err != nil {
		return false
	}
	if fi, err := os.Stat(mayorDir); err != nil || !fi.IsDir() {
		return false
	}

	return true
}
