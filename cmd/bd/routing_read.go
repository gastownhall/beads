package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
)

// getRoutingConfigValue resolves routing config from YAML/env first, then DB config.
// Only uses the YAML value if it was explicitly set (not a Viper default), so that
// DB-stored values aren't shadowed by defaults like "~/.beads-planning".
func getRoutingConfigValue(ctx context.Context, store storage.DoltStorage, key string) string {
	// Only trust YAML/env values that were explicitly set, not Viper defaults.
	if src := config.GetValueSource(key); src != config.SourceDefault {
		value := strings.TrimSpace(config.GetString(key))
		if value != "" {
			return value
		}
	}

	if store == nil {
		return ""
	}

	dbValue, err := store.GetConfig(ctx, key)
	if err != nil {
		debug.Logf("DEBUG: failed to read config %q from store: %v\n", key, err)
		return ""
	}
	return strings.TrimSpace(dbValue)
}

// determineAutoRoutedRepoPath returns the repository path that should be used for
// issue reads when contributor auto-routing is enabled.
func determineAutoRoutedRepoPath(ctx context.Context, store storage.DoltStorage) string {
	userRole, err := routing.DetectUserRole(".")
	if err != nil {
		debug.Logf("Warning: failed to detect user role: %v\n", err)
	}

	// Build routing config with backward compatibility for legacy contributor.* keys.
	routingMode := getRoutingConfigValue(ctx, store, "routing.mode")
	contributorRepo := getRoutingConfigValue(ctx, store, "routing.contributor")

	// Backward compatibility - fall back to legacy contributor.* keys
	if routingMode == "" {
		if getRoutingConfigValue(ctx, store, "contributor.auto_route") == "true" {
			routingMode = "auto"
		}
	}
	if contributorRepo == "" {
		contributorRepo = getRoutingConfigValue(ctx, store, "contributor.planning_repo")
	}

	routingConfig := &routing.RoutingConfig{
		Mode:             routingMode,
		DefaultRepo:      getRoutingConfigValue(ctx, store, "routing.default"),
		MaintainerRepo:   getRoutingConfigValue(ctx, store, "routing.maintainer"),
		ContributorRepo:  contributorRepo,
		ExplicitOverride: "",
	}

	return routing.DetermineTargetRepo(routingConfig, userRole, ".")
}

// openRoutedReadStore opens the auto-routed target store for read commands.
// Returns routed=false when reads should stay in the current store.
func openRoutedReadStore(ctx context.Context, store storage.DoltStorage) (storage.DoltStorage, bool, error) {
	repoPath := determineAutoRoutedRepoPath(ctx, store)
	if repoPath == "" || repoPath == "." {
		return nil, false, nil
	}

	targetRepoPath := routing.ExpandPath(repoPath)
	targetBeadsDir := filepath.Join(targetRepoPath, ".beads")
	targetStore, err := newReadOnlyStoreFromConfig(ctx, targetBeadsDir)
	if err != nil {
		return nil, false, fmt.Errorf("failed to open routed store at %s: %w", targetRepoPath, err)
	}
	return targetStore, true, nil
}

// openTownRootReadStore opens a read-only store for the town root database.
// This is used as a fallback when looking up wisp IDs that may have been created
// in the town/HQ context but whose prefix routes to a different rig's database.
//
// The town root is found by walking up from the current beads directory (dbPath)
// to find a parent directory containing .beads/routes.jsonl — the presence of
// routes.jsonl indicates a multi-rig orchestrator root (the town).
//
// Returns nil if no town root is found, or if the town root is the same as the
// current database (no need to open a second store).
func openTownRootReadStore(ctx context.Context) (storage.DoltStorage, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("no database path available")
	}

	// Resolve the current beads directory from dbPath.
	currentBeadsDir := filepath.Dir(dbPath)

	// Walk up from the current beads directory's parent to find the town root.
	// The town root contains .beads/routes.jsonl (multi-rig routing config).
	dir := filepath.Dir(currentBeadsDir) // parent of .beads
	for dir != "" && dir != filepath.Dir(dir) {
		candidateBeadsDir := filepath.Join(dir, ".beads")
		candidateRoutes := filepath.Join(candidateBeadsDir, "routes.jsonl")

		if _, err := os.Stat(candidateRoutes); err == nil {
			// Found a town root with routes.jsonl.
			// Skip if it's the same as our current beads directory.
			absCandidate, _ := filepath.Abs(candidateBeadsDir)
			absCurrent, _ := filepath.Abs(currentBeadsDir)
			if absCandidate == absCurrent {
				debug.Logf("wisp town fallback: town root is same as current store, skipping")
				return nil, fmt.Errorf("town root is same as current store")
			}

			debug.Logf("wisp town fallback: found town root at %s", candidateBeadsDir)
			townStore, err := newReadOnlyStoreFromConfig(ctx, candidateBeadsDir)
			if err != nil {
				return nil, fmt.Errorf("failed to open town root store at %s: %w", candidateBeadsDir, err)
			}
			return townStore, nil
		}

		dir = filepath.Dir(dir)
	}

	return nil, fmt.Errorf("no town root found (no routes.jsonl in ancestor directories)")
}
