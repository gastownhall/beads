package main

import (
	"context"
	"fmt"
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
