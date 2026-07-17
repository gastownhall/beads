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

var routingConfigKeys = []string{
	"routing.mode",
	"routing.contributor",
	"routing.default",
	"routing.maintainer",
	"contributor.auto_route",
	"contributor.planning_repo",
}

func resolveRoutingConfigValue(key string, dbValues map[string]string) string {
	if src := config.GetValueSource(key); src != config.SourceDefault {
		if value := strings.TrimSpace(config.GetString(key)); value != "" {
			return value
		}
	}
	return strings.TrimSpace(dbValues[key])
}

func getRoutingConfigValue(ctx context.Context, store storage.DoltStorage, key string) string {
	if src := config.GetValueSource(key); src != config.SourceDefault {
		if value := strings.TrimSpace(config.GetString(key)); value != "" {
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

func determineAutoRoutedRepoPath(ctx context.Context, store storage.DoltStorage) string {
	userRole, err := routing.DetectUserRole(".")
	if err != nil {
		debug.Logf("Warning: failed to detect user role: %v\n", err)
	}

	var dbValues map[string]string
	if store != nil {
		all, allErr := store.GetAllConfig(ctx)
		if allErr != nil {
			debug.Logf("DEBUG: failed to read config from store: %v\n", allErr)
		} else {
			dbValues = make(map[string]string, len(routingConfigKeys))
			for _, key := range routingConfigKeys {
				if v, ok := all[key]; ok {
					dbValues[key] = v
				}
			}
		}
	}

	routingMode := resolveRoutingConfigValue("routing.mode", dbValues)
	contributorRepo := resolveRoutingConfigValue("routing.contributor", dbValues)

	if routingMode == "" {
		if resolveRoutingConfigValue("contributor.auto_route", dbValues) == "true" {
			routingMode = "auto"
		}
	}
	if contributorRepo == "" {
		contributorRepo = resolveRoutingConfigValue("contributor.planning_repo", dbValues)
	}

	routingConfig := &routing.RoutingConfig{
		Mode:             routingMode,
		DefaultRepo:      resolveRoutingConfigValue("routing.default", dbValues),
		MaintainerRepo:   resolveRoutingConfigValue("routing.maintainer", dbValues),
		ContributorRepo:  contributorRepo,
		ExplicitOverride: "",
	}

	return routing.DetermineTargetRepo(routingConfig, userRole, ".")
}

// printContributorRoutingNotice tells the user that `bd list`/`bd ready` are
// reading from the auto-routed contributor planning store instead of the
// local project database, so a short or empty result doesn't look like data
// loss. Without this, a routed-but-unrelated (often empty) planning store
// silently replaces the local result set with no indication anything changed:
// `bd stats` and `bd show <id>` don't route (see openRoutedReadStore callers),
// so they keep reporting the local project truth while list/ready go quiet —
// exactly the split that makes this so hard to diagnose from the CLI alone.
// Deliberately not gated on --quiet: this is safety-critical routing
// information, not decorative output (matches the unconditional stderr
// truncation notice in `bd ready --json`, not the --quiet-respecting
// --skip-labels footer).
func printContributorRoutingNotice(ctx context.Context, localStore storage.DoltStorage) {
	countSuffix := ""
	if localStore != nil {
		if stats, err := localStore.GetStatistics(ctx); err == nil {
			countSuffix = fmt.Sprintf(" (%d issue(s) in this project are hidden as a result)", stats.TotalIssues)
		}
	}
	fmt.Fprintf(os.Stderr, "note: beads.role=contributor routes bd list/ready to the contributor planning store, not this project%s.\n", countSuffix)
	fmt.Fprintln(os.Stderr, "  Fix: git config beads.role maintainer")
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
