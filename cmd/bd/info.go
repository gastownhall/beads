package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

var infoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show database and daemon information",
	Long: `Display information about the current database path and daemon status.

This command helps debug issues where bd is using an unexpected database
or daemon connection. It shows:
  - The absolute path to the database file
  - Daemon connection status (daemon or direct mode)
  - If using daemon: socket path, health status, version
  - Database statistics (issue count)
  - Schema information (with --schema flag)
  - What's new in recent versions (with --whats-new flag)

Examples:
  bd info
  bd info --json
  bd info --schema --json
  bd info --whats-new
  bd info --whats-new --json`,
	Run: func(cmd *cobra.Command, args []string) {
		schemaFlag, _ := cmd.Flags().GetBool("schema")
		whatsNewFlag, _ := cmd.Flags().GetBool("whats-new")

		// Handle --whats-new flag
		if whatsNewFlag {
			showWhatsNew()
			return
		}

		// Get database path (absolute)
		absDBPath, err := filepath.Abs(dbPath)
		if err != nil {
			absDBPath = dbPath
		}

		// Build info structure
		info := map[string]interface{}{
			"database_path": absDBPath,
			"mode":          daemonStatus.Mode,
		}

		// Add daemon details if connected
		if daemonClient != nil {
			info["daemon_connected"] = true
			info["socket_path"] = daemonStatus.SocketPath

			// Get daemon health
			health, err := daemonClient.Health()
			if err == nil {
				info["daemon_version"] = health.Version
				info["daemon_status"] = health.Status
				info["daemon_compatible"] = health.Compatible
				info["daemon_uptime"] = health.Uptime
			}

			// Get issue count from daemon
			resp, err := daemonClient.Stats()
			if err == nil {
				var stats types.Statistics
				if jsonErr := json.Unmarshal(resp.Data, &stats); jsonErr == nil {
					info["issue_count"] = stats.TotalIssues
				}
			}
		} else {
			// Direct mode
			info["daemon_connected"] = false
			if daemonStatus.FallbackReason != "" && daemonStatus.FallbackReason != FallbackNone {
				info["daemon_fallback_reason"] = daemonStatus.FallbackReason
			}
			if daemonStatus.Detail != "" {
				info["daemon_detail"] = daemonStatus.Detail
			}

			// Get issue count from direct store
			if store != nil {
				ctx := rootCtx

				// Check database freshness before reading (bd-2q6d, bd-c4rq)
				// Skip check when using daemon (daemon auto-imports on staleness)
				if daemonClient == nil {
					if err := ensureDatabaseFresh(ctx); err != nil {
						fmt.Fprintf(os.Stderr, "Error: %v\n", err)
						os.Exit(1)
					}
				}

				filter := types.IssueFilter{}
				issues, err := store.SearchIssues(ctx, "", filter)
				if err == nil {
					info["issue_count"] = len(issues)
				}
			}
		}

		// Add config to info output (requires direct mode to access config table)
		// Save current daemon state
		wasDaemon := daemonClient != nil
		var tempErr error

		if wasDaemon {
			// Temporarily switch to direct mode to read config
			tempErr = ensureDirectMode("info: reading config")
		}

		if store != nil {
			ctx := rootCtx
			configMap, err := store.GetAllConfig(ctx)
			if err == nil && len(configMap) > 0 {
				info["config"] = configMap
			}
		}

		// Note: We don't restore daemon mode since info is a read-only command
		// and the process will exit immediately after this
		_ = tempErr // silence unused warning

		// Add schema information if requested
		if schemaFlag && store != nil {
			ctx := rootCtx

			// Get schema version
			schemaVersion, err := store.GetMetadata(ctx, "bd_version")
			if err != nil {
				schemaVersion = "unknown"
			}

			// Get tables
			tables := []string{"issues", "dependencies", "labels", "config", "metadata"}

			// Get config
			configMap := make(map[string]string)
			prefix, _ := store.GetConfig(ctx, "issue_prefix")
			if prefix != "" {
				configMap["issue_prefix"] = prefix
			}

			// Get sample issue IDs
			filter := types.IssueFilter{}
			issues, err := store.SearchIssues(ctx, "", filter)
			sampleIDs := []string{}
			detectedPrefix := ""
			if err == nil && len(issues) > 0 {
				// Get first 3 issue IDs as samples
				maxSamples := 3
				if len(issues) < maxSamples {
					maxSamples = len(issues)
				}
				for i := 0; i < maxSamples; i++ {
					sampleIDs = append(sampleIDs, issues[i].ID)
				}
				// Detect prefix from first issue
				if len(issues) > 0 {
					detectedPrefix = extractPrefix(issues[0].ID)
				}
			}

			info["schema"] = map[string]interface{}{
				"tables":           tables,
				"schema_version":   schemaVersion,
				"config":           configMap,
				"sample_issue_ids": sampleIDs,
				"detected_prefix":  detectedPrefix,
			}
		}

		// JSON output
		if jsonOutput {
			outputJSON(info)
			return
		}

		// Human-readable output
		fmt.Println("\nBeads Database Information")
		fmt.Println("===========================")
		fmt.Printf("Database: %s\n", absDBPath)
		fmt.Printf("Mode: %s\n", daemonStatus.Mode)

		if daemonClient != nil {
			fmt.Println("\nDaemon Status:")
			fmt.Printf("  Connected: yes\n")
			fmt.Printf("  Socket: %s\n", daemonStatus.SocketPath)

			health, err := daemonClient.Health()
			if err == nil {
				fmt.Printf("  Version: %s\n", health.Version)
				fmt.Printf("  Health: %s\n", health.Status)
				if health.Compatible {
					fmt.Printf("  Compatible: ✓ yes\n")
				} else {
					fmt.Printf("  Compatible: ✗ no (restart recommended)\n")
				}
				fmt.Printf("  Uptime: %.1fs\n", health.Uptime)
			}
		} else {
			fmt.Println("\nDaemon Status:")
			fmt.Printf("  Connected: no\n")
			if daemonStatus.FallbackReason != "" && daemonStatus.FallbackReason != FallbackNone {
				fmt.Printf("  Reason: %s\n", daemonStatus.FallbackReason)
			}
			if daemonStatus.Detail != "" {
				fmt.Printf("  Detail: %s\n", daemonStatus.Detail)
			}
		}

		// Show issue count
		if count, ok := info["issue_count"].(int); ok {
			fmt.Printf("\nIssue Count: %d\n", count)
		}

		// Show schema information if requested
		if schemaFlag {
			if schemaInfo, ok := info["schema"].(map[string]interface{}); ok {
				fmt.Println("\nSchema Information:")
				fmt.Printf("  Tables: %v\n", schemaInfo["tables"])
				if version, ok := schemaInfo["schema_version"].(string); ok {
					fmt.Printf("  Schema Version: %s\n", version)
				}
				if prefix, ok := schemaInfo["detected_prefix"].(string); ok && prefix != "" {
					fmt.Printf("  Detected Prefix: %s\n", prefix)
				}
				if samples, ok := schemaInfo["sample_issue_ids"].([]string); ok && len(samples) > 0 {
					fmt.Printf("  Sample Issues: %v\n", samples)
				}
			}
		}

		// Check git hooks status
		hookStatuses := CheckGitHooks()
		if warning := FormatHookWarnings(hookStatuses); warning != "" {
			fmt.Printf("\n%s\n", warning)
		}

		fmt.Println()
	},
}

// extractPrefix extracts the prefix from an issue ID (e.g., "bd-123" -> "bd")
// Uses the last hyphen before a numeric suffix, so "beads-vscode-1" -> "beads-vscode"
func extractPrefix(issueID string) string {
	// Try last hyphen first (handles multi-part prefixes like "beads-vscode-1")
	lastIdx := strings.LastIndex(issueID, "-")
	if lastIdx <= 0 {
		return ""
	}

	suffix := issueID[lastIdx+1:]
	// Check if suffix is numeric
	if len(suffix) > 0 {
		numPart := suffix
		if dotIdx := strings.Index(suffix, "."); dotIdx > 0 {
			numPart = suffix[:dotIdx]
		}
		var num int
		if _, err := fmt.Sscanf(numPart, "%d", &num); err == nil {
			return issueID[:lastIdx]
		}
	}

	// Suffix is not numeric, fall back to first hyphen
	firstIdx := strings.Index(issueID, "-")
	if firstIdx <= 0 {
		return ""
	}
	return issueID[:firstIdx]
}

// VersionChange represents agent-relevant changes for a specific version
type VersionChange struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Changes []string `json:"changes"`
}

// versionChanges contains agent-actionable changes for recent versions
var versionChanges = []VersionChange{
	{
		Version: "0.26.2",
		Date:    "2025-11-29",
		Changes: []string{
			"FIX (bd-f2f): Hash-based staleness detection prevents stale DB from corrupting JSONL",
			"Detects content differences even when issue counts match between DB and JSONL",
			"Computes SHA256 hash of JSONL content to detect mismatches missed by count-based checks",
		},
	},
	{
		Version: "0.26.1",
		Date:    "2025-11-29",
		Changes: []string{
			"CRITICAL FIX (bd-53c): Reverse ZFC check prevents stale DB from corrupting JSONL",
			"bd sync now detects when JSONL has more issues than DB and imports first",
			"Prevents fresh/stale clones from exporting incomplete database state",
		},
	},
	{
		Version: "0.26.0",
		Date:    "2025-11-27",
		Changes: []string{
			"bd doctor --check-health - Lightweight health checks for startup hooks (exit 0 on success)",
			"--no-git-history flag - Prevent spurious deletions when git history is unreliable",
			"gh2jsonl --id-mode hash - Hash-based ID generation for GitHub imports",
			"MCP Protocol Fix (PR #400) - Subprocess stdin no longer breaks MCP JSON-RPC",
			"Git Worktree Staleness Fix (#399) - Staleness check works after writes in worktrees",
			"Multi-Part Prefix Support (#398) - Handles prefixes like 'my-app-123' correctly",
			"bd sync Commit Scope Fixed - Only commits .beads/ files, not other staged files",
		},
	},
	{
		Version: "0.25.1",
		Date:    "2025-11-25",
		Changes: []string{
			"Zombie Resurrection Prevention - Stale clones can no longer resurrect deleted issues",
			"bd sync commit scope fixed - Now commits entire .beads/ directory before pull",
			"bd prime ephemeral branch detection - Auto-detects ephemeral branches and adjusts workflow",
			"JSONL Canonicalization (bd-6xd) - Default JSONL filename is now issues.jsonl; legacy beads.jsonl still supported",
		},
	},
	{
		Version: "0.25.0",
		Date:    "2025-11-25",
		Changes: []string{
			"Deletion Propagation - Deletions now sync across clones via deletions manifest",
			"Stealth Mode - `bd init --stealth` for invisible beads usage",
			"Ephemeral Branch Sync - `bd sync --from-main` to sync from main without pushing",
		},
	},
	{
		Version: "0.24.4",
		Date:    "2025-11-25",
		Changes: []string{
			"Transaction API - Full transactional support for atomic multi-operation workflows",
			"Tip System Infrastructure - Smart contextual hints for users",
			"Sorting for bd list/search - New `--sort` and `--reverse` flags",
			"Claude Integration Verification - New bd doctor checks",
			"ARM Linux Support - GoReleaser now builds for linux/arm64",
			"Orphan Detection Migration - Identifies orphaned child issues",
		},
	},
	{
		Version: "0.24.3",
		Date:    "2025-11-24",
		Changes: []string{
			"BD_GUIDE.md Generation - Version-stamped documentation for AI agents",
			"Configurable Export Error Policies - Flexible error handling for export operations",
			"Command Set Standardization - Global verbosity, dry-run, and label flags",
			"Auto-Migration on Version Bump - Automatic database schema updates",
			"Monitor Web UI Enhancements - Interactive stats cards, multi-select priority",
		},
	},
	{
		Version: "0.24.1",
		Date:    "2025-11-22",
		Changes: []string{
			"bd search filters - Date and priority filters added",
			"bd count - New command for counting and grouping issues",
			"Test Infrastructure - Automatic skip list for tests",
		},
	},
	{
		Version: "0.24.0",
		Date:    "2025-11-20",
		Changes: []string{
			"bd doctor --fix - Automatic repair functionality",
			"bd clean - Remove temporary merge artifacts",
			".beads/README.md Generation - Auto-generated during bd init",
			"blocked_issues_cache Table - Performance optimization for GetReadyWork",
			"Commit Hash in Version Output - Enhanced version reporting",
		},
	},
	{
		Version: "0.23.0",
		Date:    "2025-11-08",
		Changes: []string{
			"Agent Mail integration - Python adapter library with 98.5% reduction in git traffic",
			"`bd info --whats-new` - Quick upgrade summaries for agents (shows last 3 versions)",
			"`bd hooks install` - Embedded git hooks command (replaces external script)",
			"`bd cleanup` - Bulk deletion for agent-driven compaction",
			"`bd new` alias added - Agents often tried this instead of `bd create`",
			"`bd list` now one-line-per-issue by default - Prevents agent miscounting (use --long for old format)",
			"3-way JSONL merge auto-invoked on conflicts - No manual intervention needed",
			"Daemon crash recovery - Panic handler with socket cleanup prevents orphaned processes",
			"Auto-import when database missing - `bd import` now auto-initializes",
			"Stale database export prevention - ID-based staleness detection",
		},
	},
	{
		Version: "0.22.1",
		Date:    "2025-11-06",
		Changes: []string{
			"Native `bd merge` command vendored from beads-merge - no external binary needed",
			"`bd info` detects outdated git hooks - warns if version mismatch",
			"Multi-workspace deletion tracking fixed - deletions now propagate correctly",
			"Hash ID recognition improved - recognizes Base36 IDs without a-f letters",
			"Import/export deadlock fixed - no hanging when daemon running",
		},
	},
	{
		Version: "0.22.0",
		Date:    "2025-11-05",
		Changes: []string{
			"Intelligent merge driver auto-configured - eliminates most JSONL conflicts",
			"Onboarding wizards: `bd init --contributor` and `bd init --team`",
			"New `bd migrate-issues` command - migrate issues between repos with dependencies",
			"`bd show` displays blocker status - 'Blocked by N open issues' or 'Ready to work'",
			"SearchIssues N+1 query fixed - batch-loads labels for better performance",
			"Sync validation prevents infinite dirty loop - verifies JSONL export",
		},
	},
	{
		Version: "0.21.0",
		Date:    "2025-11-04",
		Changes: []string{
			"Hash-based IDs eliminate collisions - remove ID coordination workarounds",
			"Event-driven daemon mode (opt-in) - set BEADS_DAEMON_MODE=events",
			"Agent Mail integration - real-time multi-agent coordination (<100ms latency)",
			"`bd duplicates --auto-merge` - automated duplicate detection and merging",
			"Hierarchical children for epics - dotted IDs (bd-abc.1, bd-abc.2) up to 3 levels",
			"`--discovered-from` inline syntax - create with dependency in one command",
		},
	},
}

// showWhatsNew displays agent-relevant changes from recent versions
func showWhatsNew() {
	currentVersion := Version // from version.go

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"current_version": currentVersion,
			"recent_changes":  versionChanges,
		})
		return
	}

	// Human-readable output
	fmt.Printf("\n🆕 What's New in bd (Current: v%s)\n", currentVersion)
	fmt.Println("=" + strings.Repeat("=", 60))
	fmt.Println()

	for _, vc := range versionChanges {
		// Highlight if this is the current version
		versionMarker := ""
		if vc.Version == currentVersion {
			versionMarker = " ← current"
		}

		fmt.Printf("## v%s (%s)%s\n\n", vc.Version, vc.Date, versionMarker)

		for _, change := range vc.Changes {
			fmt.Printf("  • %s\n", change)
		}
		fmt.Println()
	}

	fmt.Println("💡 Tip: Use `bd info --whats-new --json` for machine-readable output")
	fmt.Println()
}

func init() {
	infoCmd.Flags().Bool("schema", false, "Include schema information in output")
	infoCmd.Flags().Bool("whats-new", false, "Show agent-relevant changes from recent versions")
	infoCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.AddCommand(infoCmd)
}
