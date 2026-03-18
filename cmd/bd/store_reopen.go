package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/utils"
)

// openReadOnlyStoreForDBPath reopens a read-only store from an existing dbPath
// while preserving repo-local metadata such as dolt_database and the resolved
// Dolt server port. Falls back to a raw path-only open when no matching
// metadata.json can be found.
func openReadOnlyStoreForDBPath(ctx context.Context, dbPath string) (storage.DoltStorage, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("no database path available")
	}

	if beadsDir := resolveBeadsDirForDBPath(dbPath); beadsDir != "" {
		return newReadOnlyStoreFromConfig(ctx, beadsDir)
	}

	// Fallback: derive beads dir from dbPath parent directory.
	return newReadOnlyStoreFromConfig(ctx, filepath.Dir(dbPath))
}

// resolveBeadsDirForDBPath maps a database path back to its owning .beads
// directory when metadata.json is available. This is needed for repos that use
// non-default dolt_database names or custom dolt_data_dir locations.
func resolveBeadsDirForDBPath(dbPath string) string {
	actualDBPath := utils.CanonicalizePath(dbPath)
	seen := map[string]struct{}{}
	candidates := make([]string, 0, 4)

	addCandidate := func(path string) {
		if path == "" {
			return
		}
		key := utils.NormalizePathForComparison(path)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, path)
	}

	addCandidate(filepath.Dir(dbPath))
	addCandidate(filepath.Dir(actualDBPath))

	if found := beads.FindBeadsDir(); found != "" {
		addCandidate(found)
		addCandidate(utils.CanonicalizePath(found))
	}

	for _, beadsDir := range candidates {
		cfg, err := configfile.Load(beadsDir)
		if err != nil || cfg == nil {
			continue
		}
		if utils.PathsEqual(cfg.DatabasePath(beadsDir), dbPath) || utils.PathsEqual(cfg.DatabasePath(beadsDir), actualDBPath) {
			return beadsDir
		}
	}

	return ""
}
