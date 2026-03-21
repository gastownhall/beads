package main

import (
	"os"
	"path/filepath"
)

// isOrchestratorRoot returns true when path looks like an orchestrator workspace root.
//
// We require both:
//  1. mayor/town.json (town identity/config), and
//  2. .beads/routes.jsonl (town-level routing config)
//
// This keeps detection strict and avoids blocking normal repos that merely
// contain a .beads directory.
func isOrchestratorRoot(path string) bool {
	if path == "" {
		return false
	}

	townConfig := filepath.Join(path, "mayor", "town.json")
	routes := filepath.Join(path, ".beads", "routes.jsonl")

	if _, err := os.Stat(townConfig); err != nil {
		return false
	}
	if _, err := os.Stat(routes); err != nil {
		return false
	}

	return true
}
