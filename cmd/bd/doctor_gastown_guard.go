package main

import (
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
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

// freezeRoot resolves the directory that holds the MIGRATION-FREEZE sentinel.
//
// A Gas Town town root wins whenever there is one, so a town-wide freeze keeps
// behaving exactly as it did: one sentinel, every rig in the town stands down,
// and `gt migrate thaw` is still the way out.
//
// Outside a town — which is every other beads workspace — findTownRoot returns
// "" and migration.IsFrozen("") is false, so the freeze was UNREACHABLE: bd had
// a fleet-wide migration stand-down that only Gas Town could arm. The fallback
// is the .beads directory, the one workspace-scoped location every backend
// already agrees on and the same directory the freeze exists to protect.
//
// Nothing changes when neither file exists, which is the overwhelmingly common
// case: this returns a path, and IsFrozen still has to stat a file that is not
// there.
//
// The workspace is the one bd is RUN IN (BEADS_DIR, or the .beads found from
// the cwd; -C is applied before this runs). --db/--database do not feed it: a
// command aimed at another workspace's database consults this workspace's
// sentinel, the same way a town freeze covers whoever runs inside the town.
func freezeRoot() string {
	root, _ := freezeRootAndScope()
	return root
}

// freezeRootAndScope is freezeRoot plus WHICH kind of root it found, so a
// caller that words its message by that ("town is frozen" / "workspace is
// frozen", `gt migrate thaw` / remove the file) cannot disagree with the path
// it is reporting: one walk, one answer.
func freezeRootAndScope() (root string, inTown bool) {
	if townRoot := findTownRoot(); townRoot != "" {
		return townRoot, true
	}
	return beads.FindBeadsDir(), false
}
