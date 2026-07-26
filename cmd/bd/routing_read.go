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

func determineAutoRoutedRepoPath(ctx context.Context, store storage.DoltStorage) (string, routing.RoutingRule) {
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

	return routing.DetermineTargetRepoWithRule(routingConfig, userRole, ".")
}

// routingNoticeText returns the stderr note and remediation command for the
// given routing rule, so the notice always attributes the swap to the rule
// that actually matched instead of hardcoding the contributor-role case.
func routingNoticeText(rule routing.RoutingRule) (reason, fix string) {
	switch rule {
	case routing.RuleContributor:
		// The contributor rule fires on explicit beads.role=contributor OR on
		// origin-URL inference with beads.role unset — don't claim the config
		// key is set when it may not be.
		return "contributor routing (beads.role=contributor, or inferred from the origin URL) routes bd list/ready to the contributor planning store, not this project",
			"git config beads.role maintainer"
	case routing.RuleMaintainer:
		return "routing.maintainer routes bd list/ready to a different planning store than this project",
			"bd config unset routing.maintainer"
	case routing.RuleDefault:
		return "routing.default routes bd list/ready to a different planning store than this project",
			"bd config unset routing.default"
	default:
		return "an auto-routing rule sends bd list/ready to a different planning store than this project",
			"bd config get routing.mode routing.contributor routing.maintainer routing.default"
	}
}

// printContributorRoutingNotice tells the user that `bd list`/`bd ready` are
// reading from an auto-routed store instead of the local project database,
// so a short or empty result doesn't look like data loss. Without this, a
// routed-but-unrelated (often empty) planning store silently replaces the
// local result set with no indication anything changed: `bd stats` and
// `bd show <id>` don't route (see openRoutedReadStore callers), so they keep
// reporting the local project truth while list/ready go quiet — exactly the
// split that makes this so hard to diagnose from the CLI alone.
//
// The notice text and remediation command are branched on rule, the
// routing.RoutingRule that actually matched in determineAutoRoutedRepoPath,
// so a maintainer- or default-routed swap doesn't get misattributed to
// beads.role=contributor.
//
// Gated on !quietFlag: --quiet is documented as "Suppress non-essential
// output (errors only)" (cmd/bd/main.go), and other non-error stderr
// notices in this package (tips.go, metrics.go) respect it the same way.
func printContributorRoutingNotice(ctx context.Context, localStore storage.DoltStorage, rule routing.RoutingRule) {
	if quietFlag {
		return
	}
	countSuffix := ""
	if localStore != nil {
		if stats, err := localStore.GetStatistics(ctx); err == nil {
			// stats.TotalIssues is an unfiltered COUNT(*) FROM issues, so it
			// includes closed/deferred/blocked issues that bd ready's
			// predicates would never surface anyway. Report it as the
			// project's total issue count rather than claiming all of them
			// are "hidden as a result" of routing.
			countSuffix = fmt.Sprintf(" (this project has %d total issue(s))", stats.TotalIssues)
		}
		// If GetStatistics errors, countSuffix stays empty: the notice is
		// still accurate without the count, so the parenthetical is
		// silently dropped rather than surfacing a secondary error.
	}
	reason, fix := routingNoticeText(rule)
	fmt.Fprintf(os.Stderr, "note: %s%s.\n", reason, countSuffix)
	fmt.Fprintln(os.Stderr, "  Fix:", fix)
}

// openRoutedReadStore opens the auto-routed target store for read commands.
// Returns routed=false when reads should stay in the current store. The
// returned routing.RoutingRule identifies which rule matched, for callers
// that print a routing notice.
func openRoutedReadStore(ctx context.Context, store storage.DoltStorage) (storage.DoltStorage, bool, routing.RoutingRule, error) {
	repoPath, rule := determineAutoRoutedRepoPath(ctx, store)
	if repoPath == "" || repoPath == "." {
		return nil, false, routing.RuleNone, nil
	}

	targetRepoPath := routing.ExpandPath(repoPath)
	targetBeadsDir := filepath.Join(targetRepoPath, ".beads")
	targetStore, err := newReadOnlyStoreFromConfig(ctx, targetBeadsDir)
	if err != nil {
		return nil, false, rule, fmt.Errorf("failed to open routed store at %s: %w", targetRepoPath, err)
	}
	return targetStore, true, rule, nil
}
