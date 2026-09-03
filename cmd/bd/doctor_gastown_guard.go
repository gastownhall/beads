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
//  2. mayor/town.json (orchestrator configuration)
//
// This prevents bd doctor --fix from running at the workspace root,
// where repairs should go through the orchestrator's own doctor command.
func isOrchestratorRoot(path string) bool {
	if path == "" {
		return false
	}

	routes := filepath.Join(path, ".beads", "routes.jsonl")
	townJSON := filepath.Join(path, "mayor", "town.json")

	if _, err := os.Stat(routes); err != nil {
		return false
	}
	if _, err := os.Stat(townJSON); err != nil {
		return false
	}

	return true
}

// findTownRoot walks up from the current working directory looking for
// mayor/town.json — the same primary marker gt's own workspace package uses
// (see gastownhall/gastown internal/cmd/handoff.go's detectTownRootFromCwd).
// Falls back to GT_TOWN_ROOT then GT_ROOT (gt's env-var fallback chain,
// already how the rest of this codebase detects an orchestrator — see
// formula.go, molecules.go, doltserver.go) when cwd detection fails, e.g. a
// detached worktree or a cwd outside the town tree entirely.
//
// Used by CheckMigrationFreeze (dc-6jaq) to find the MIGRATION-FREEZE
// sentinel; unlike isOrchestratorRoot above this only needs the mayor/
// marker, not also .beads/routes.jsonl, since a rig-level bd invocation's
// cwd is never itself the orchestrator root.
func findTownRoot() string {
	if cwd, err := os.Getwd(); err == nil {
		path := cwd
		for {
			if _, statErr := os.Stat(filepath.Join(path, "mayor", "town.json")); statErr == nil {
				return path
			}
			parent := filepath.Dir(path)
			if parent == path {
				break
			}
			path = parent
		}
	}

	for _, envName := range []string{"GT_TOWN_ROOT", "GT_ROOT"} {
		envRoot := os.Getenv(envName)
		if envRoot == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(envRoot, "mayor", "town.json")); err == nil {
			return envRoot
		}
	}

	return ""
}
