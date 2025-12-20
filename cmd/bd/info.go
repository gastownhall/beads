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
	Use:     "info",
	GroupID: "setup",
	Short:   "Show database and daemon information",
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
		Version: "0.30.7",
		Date:    "2025-12-19",
		Changes: []string{
			"FIX: bd graph no longer crashes with nil pointer on epics (fixes #657)",
			"FIX: Windows npm installer no longer fails with file lock error (fixes #652)",
			"NEW: Version Bump molecule template for repeatable release workflows",
		},
	},
	{
		Version: "0.30.6",
		Date:    "2025-12-18",
		Changes: []string{
			"bd graph command shows dependency counts using subgraph formatting (bd-6v2)",
			"types.StatusPinned for persistent beads that survive cleanup",
			"CRITICAL: Fixed dependency resurrection bug in 3-way merge (bd-ndye) - removals now win",
		},
	},
	{
		Version: "0.30.5",
		Date:    "2025-12-18",
		Changes: []string{
			"REMOVED: YAML simple template system - --from-template flag removed from bd create",
			"REMOVED: Embedded templates (bug.yaml, epic.yaml, feature.yaml) - Use Beads templates instead",
			"Templates are now purely Beads-based - Create epic with 'template' label, use bd template instantiate",
		},
	},
	{
		Version: "0.30.4",
		Date:    "2025-12-18",
		Changes: []string{
			"bd template instantiate (bd-r6a.2) - Create beads issues from Beads templates",
			"--assignee flag for template instantiate - Auto-assign during instantiation",
			"bd mail inbox --identity fix - Now properly filters by identity parameter",
			"Orphan detection fixes - No longer warns about closed issues or tombstones",
			"EXPERIMENTAL: Graph link fields (relates_to, replies_to, duplicate_of, superseded_by) and mail commands are subject to breaking changes",
		},
	},
	{
		Version: "0.30.3",
		Date:    "2025-12-17",
		Changes: []string{
			"SECURITY: Data loss race condition fixed (bd-b6xo) - Removed unsafe ClearDirtyIssues() method",
			"Stale database warning (bd-2q6d) - Commands now warn when DB is out of sync with JSONL",
			"Staleness check error handling improved (bd-n4td, bd-o4qy) - Proper warnings on check failures",
		},
	},
	{
		Version: "0.30.2",
		Date:    "2025-12-16",
		Changes: []string{
			"bd setup droid (GH#598) - Factory.ai (Droid) IDE support",
			"Messaging schema fields (bd-kwro.1) - New 'message' issue type, sender/ephemeral/replies_to/relates_to/duplicate_of/superseded_by fields",
			"New dependency types: replies-to, relates-to, duplicates, supersedes",
			"Windows build fixes (GH#585) - gosec lint errors resolved",
			"Issue ID prefix extraction fix - Word-like suffixes now parse correctly",
			"Legacy deletions.jsonl code removed (bd-fom) - Fully migrated to inline tombstones",
		},
	},
	{
		Version: "0.30.1",
		Date:    "2025-12-16",
		Changes: []string{
			"bd reset command (GH#505) - Complete beads removal from a repository",
			"bd update --type flag (GH#522) - Change issue type after creation",
			"bd q silent mode (GH#540) - Quick-capture without output for scripting",
			"bd show displays dependent issue status (GH#583) - Shows status for blocked-by/blocking issues",
			"claude.local.md support - Local-only documentation, gitignored by default",
			"Auto-disable daemon in git worktrees (GH#567) - Prevents database conflicts",
			"Inline tombstones for soft-delete (bd-vw8) - Deleted issues become tombstones in issues.jsonl",
			"bd migrate-tombstones command (bd-8f9) - Converts legacy deletions.jsonl to inline tombstones",
			"Enhanced Git Worktree Support (bd-737) - Shared .beads database across worktrees",
		},
	},
	{
		Version: "0.30.0",
		Date:    "2025-12-15",
		Changes: []string{
			"TOMBSTONE ARCHITECTURE - Deleted issues become inline tombstones in issues.jsonl (bd-vw8)",
			"bd migrate-tombstones - Convert legacy deletions.jsonl to inline tombstones (bd-8f9)",
			"bd doctor tombstone health checks - Detects orphaned/expired tombstones (bd-s3v)",
			"Git Worktree Support (bd-737) - Shared database across worktrees, worktree-aware hooks",
			"MCP Context Engineering (GH #481) - 80-90% context reduction for MCP responses",
			"bd thanks command (GH #555) - List contributors to your project",
			"BD_NO_INSTALL_HOOKS env var (GH #500) - Disable automatic git hook installation",
			"Claude Code skill marketplace (GH #468) - Install beads skill via marketplace",
			"Daemon delete auto-sync (GH #528, #537) - Delete operations trigger auto-sync",
			"close_reason persistence (GH #551) - Close reasons now saved to database on close",
			"JSONL-only mode improvements (GH #549) - GetReadyWork/GetBlockedIssues for memory storage",
			"Lock file improvements (GH #484, #555) - Fast fail on stale locks, 98% test coverage",
		},
	},
	{
		Version: "0.29.0",
		Date:    "2025-12-03",
		Changes: []string{
			"--estimate flag for bd create/update (GH #443) - Add time estimates to issues in minutes",
			"bd doctor improvements - SQLite integrity check, config validation, stale sync branch detection",
			"bd doctor --output flag (bd-9cc) - Export diagnostics to file for sharing/debugging",
			"bd doctor --dry-run flag (bd-qn5) - Preview fixes without applying them",
			"bd doctor per-fix confirmation mode (bd-3xl) - Approve each fix individually",
			"--readonly flag (bd-ymo) - Read-only mode for worker sandboxes",
			"bd sync safety improvements - Auto-push after merge, diverged history handling (bd-3s8)",
			"Auto-resolve merge conflicts deterministically (bd-6l8) - All field conflicts resolved without prompts",
			"3-char all-letter base36 hash support (GH #446) - Fixes prefix extraction edge case",
		},
	},
	{
		Version: "0.28.0",
		Date:    "2025-12-01",
		Changes: []string{
			"bd daemon --local flag (#433) - Run daemon without git operations for multi-repo/worktree setups",
			"bd daemon --foreground flag - Run in foreground for systemd/supervisord integration",
			"bd migrate-sync command (bd-epn) - Migrate to sync.branch workflow for cleaner main branch",
			"Database migration: close_reason column (bd-uyu) - Fixes sync loops with close_reason",
			"Multi-repo prefix filtering (GH #437) - Issues filtered by prefix when flushing from non-primary repos",
			"Parent-child dependency UX (GH #440) - Fixed documentation and UI labels for dependencies",
			"sync.branch workflow fixes (bd-epn) - Fixed .beads/ restoration and doctor detection",
			"Jira API migration - Updated from deprecated v2 to v3 API",
		},
	},
	{
		Version: "0.27.2",
		Date:    "2025-11-30",
		Changes: []string{
			"CRITICAL: Mass database deletion protection - Safety guard prevents purging entire DB on JSONL reset (bd-t5m)",
			"Fresh Clone Initialization - bd init auto-detects prefix from existing JSONL, works without --prefix flag (bd-4h9)",
			"3-Character Hash Support - ExtractIssuePrefix now handles base36 hashes 3+ chars (#425)",
			"Import Warnings - New warning when issues skipped due to deletions manifest (bd-4zy)",
		},
	},
	{
		Version: "0.27.0",
		Date:    "2025-11-29",
		Changes: []string{
			"Git hooks now sync.branch aware - pre-commit/pre-push skip .beads checks when sync.branch configured",
			"Custom Status States - Define project-specific statuses via config (testing, blocked, review)",
			"Contributor Fork Workflows - `bd init --contributor` auto-configures sync.remote=upstream",
			"Git Worktree Support - Full support for worktrees in hooks and detection",
			"CRITICAL: Sync corruption prevention - Hash-based staleness + reverse ZFC checks",
			"Out-of-Order Dependencies (#414) - JSONL import handles deps before targets exist",
			"--from-main defaults to noGitHistory=true - Prevents spurious deletions",
			"bd sync --squash - Batch multiple sync commits into one",
			"Fresh Clone Detection - bd doctor suggests 'bd init' when JSONL exists but no DB",
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
