package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

var infoCmd = &cobra.Command{
	Use:     "info",
	GroupID: "setup",
	Short:   "Show database information",
	Long: `Display information about the current database.

This command helps debug issues where bd is using an unexpected database. It shows:
  - The absolute path to the database file
  - Database statistics (issue count)
  - Schema information (with --schema flag)
  - What's new in recent versions (with --whats-new flag)

Examples:
  bd info
  bd info --json
  bd info --schema --json
  bd info --whats-new
  bd info --whats-new --json
  bd info --thanks`,
	Run: func(cmd *cobra.Command, args []string) {
		schemaFlag, _ := cmd.Flags().GetBool("schema")
		whatsNewFlag, _ := cmd.Flags().GetBool("whats-new")
		thanksFlag, _ := cmd.Flags().GetBool("thanks")

		// Handle --thanks flag
		if thanksFlag {
			printThanksPage()
			return
		}

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
			"mode":          "direct",
		}

		// Get issue count from direct store
		if store != nil {
			ctx := rootCtx

			requireFreshDB(ctx)

			filter := types.IssueFilter{}
			issues, err := store.SearchIssues(ctx, "", filter)
			if err == nil {
				info["issue_count"] = len(issues)
			}
		}

		// Add config to info output
		if store != nil {
			ctx := rootCtx
			configMap, err := store.GetAllConfig(ctx)
			if err == nil && len(configMap) > 0 {
				info["config"] = configMap
			}
		}

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
			prefix, _ := store.GetConfig(ctx, "issue_prefix") // Best effort: empty prefix is valid
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
		fmt.Printf("Mode: direct\n")

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
		Version: "0.50.3",
		Date:    "2026-02-15",
		Changes: []string{
			"REFACTOR: All tracker CLIs (Linear, GitLab, Jira) now use shared SyncEngine — eliminates ~800 lines of duplicated sync code",
			"NEW: SyncEngine PullHooks/PushHooks for tracker-specific behaviors (GenerateID, FormatDescription, ContentEqual, etc.)",
			"NEW: Jira native integration in internal/jira/ with REST API v3, ADF conversion, field mapping",
			"NEW: Tracker plugin registry with auto-discovery (tracker.Register + init())",
			"FIX: Jira State mapping bug — stale pointer assignment could cause incorrect status mapping",
			"FIX: CI Windows build — test helper file renamed to _test.go suffix",
			"PERF: Test suite — cached git template replaces ~60 subprocess calls",
		},
	},
	{
		Version: "0.50.1",
		Date:    "2026-02-14",
		Changes: []string{
			"CHANGED: Default backend is now Dolt for new bd init projects (existing SQLite projects unaffected)",
			"NEW: bd graph terminal-native DAG visualization, DOT export, interactive HTML export",
			"NEW: bd sql command for raw SQL access (table, JSON, CSV output)",
			"NEW: bd help --all for complete command reference dump",
			"NEW: decision built-in issue type",
			"NEW: Cross-database dependency resolution via prefix routes in bd show/graph/blocked",
			"NEW: bd doctor artifact cleanup (--check=artifacts --clean) and Dolt corruption recovery (--fix)",
			"NEW: bd doctor Claude Code integration checks and grouped category output",
			"REMOVED: Daemon/RPC subsystem and JSONL sync layer fully removed",
			"FIX: bd close enforces gate satisfaction (--force to bypass)",
			"FIX: bd show exits non-zero when issue not found",
			"FIX: Dolt joinIter panic prevented (replaced IN/EXISTS subqueries with Go-level filtering)",
			"FIX: Embedded Dolt self-deadlock in git hooks and bd migrate",
			"FIX: bd ready excludes children of deferred parents",
		},
	},
	{
		Version: "0.49.6",
		Date:    "2026-02-09",
		Changes: []string{
			"REVERT: Embedded Dolt mode restored (removal was only intended for Gas Town, not Beads)",
			"REMOVED: Daemon subsystem fully removed from bd CLI (Dolt replaces daemon-based sync)",
			"REMOVED: JSONL flush/sync machinery deleted (-7,634 lines); JSONL functions are now no-ops",
			"CLEANUP: Removed 171 dead daemonClient branches and 46 markDirtyAndScheduleFlush no-op calls",
		},
	},
	{
		Version: "0.49.5",
		Date:    "2026-02-08",
		Changes: []string{
			"NEW: bd search --has/--no flags for content and null-check filtering",
			"NEW: bd promote command for wisp-to-bead promotion",
			"NEW: bd todo command for lightweight task management",
			"NEW: bd find-duplicates for AI-powered duplicate detection",
			"NEW: bd validate integrated into bd doctor --check=validate",
			"NEW: Dolt fail-fast TCP check before MySQL protocol init",
			"SECURITY: SQL identifier validation prevents injection in dynamic table/db names",
			"SECURITY: Path traversal fix in export handler; command injection fix in import",
			"FIX: RPC mutation events now include issueID (was zero-value for label/dep ops)",
			"FIX: Daemon YAML config recognizes both hyphen and underscore variants",
			"FIX: Doctor role check falls back to database config",
			"FIX: SQLite Close() idempotent (WAL retry deadlock fix)",
			"FIX: SQLITE_BUSY retry for all BEGIN IMMEDIATE calls",
			"FIX: Dolt cross-rig contamination prevented with prefix-based db names",
			"FIX: bd list separates parent-child from blocks display",
			"FIX: Cross-prefix ID resolution in multi-repo scenarios",
			"CHANGE: Embedded Dolt mode fully removed (server-only connections)",
			"CHANGE: bd init defaults to chaining hooks (no prompt)",
			"CHANGE: brew upgrade command corrected to 'brew upgrade beads'",
		},
	},
	{
		Version: "0.49.4",
		Date:    "2026-02-05",
		Changes: []string{
			"NEW: --label-pattern and --label-regex flags for bd list and bd ready - glob and regex filtering on labels",
			"NEW: Simple query language for complex bd list filtering",
			"NEW: spec_id field for linking issues to specification documents",
			"NEW: Wisp type field for TTL-based compaction of ephemeral molecules",
			"NEW: Dolt schema migration runner and doctor validation checks",
			"NEW: --metadata flag for bd update (JSON metadata from CLI)",
			"NEW: config.local.yaml for local configuration overrides",
			"FIX: JSONL file locking prevents race conditions in concurrent writes",
			"FIX: Merge driver preserves all issue fields (spec_id, metadata, deps)",
			"FIX: Atomic bd claim with compare-and-swap semantics",
			"FIX: Dolt lock contention - advisory flock prevents zombie processes",
			"FIX: Windows Dolt build via pure-Go regex backend",
			"CHANGE: bd ready excludes in_progress issues (shows only claimable work)",
		},
	},
	{
		Version: "0.49.3",
		Date:    "2026-01-31",
		Changes: []string{
			"FIX: Dolt split-brain eliminated - DatabasePath() always resolves to .beads/dolt/ for dolt backend; JSONL auto-import blocked in dolt-native mode",
			"CHANGE: Embedded Dolt is now the default - server mode is opt-in via dolt_mode: server",
			"FIX: Dolt mergeJoinIter panic on type-filtered queries eliminated",
			"FIX: CGO/ICU build - Makefile and test.sh auto-detect Homebrew icu4c paths on macOS",
		},
	},
	{
		Version: "0.49.2",
		Date:    "2026-01-31",
		Changes: []string{
			"NEW: GitLab backend - Bidirectional issue sync with GitLab (bd gitlab sync/status/projects)",
			"NEW: Key-value store - bd kv get/set/delete/list for persistent key-value storage",
			"NEW: Per-issue JSON metadata field for custom structured data (SQLite + Dolt)",
			"NEW: Events JSONL export - Opt-in audit trail via events-export config",
			"NEW: Role configuration - Explicit roles via git, interactive contributor prompt",
			"NEW: bd backend and bd sync mode subcommands for storage inspection",
			"NEW: Dolt auto-detect server mode during bd init",
			"NEW: comment_count in JSON views, comment timestamps with --local-time",
			"CHANGE: Removed Gas Town-specific code from beads core (hooks, validation, role types)",
			"CHANGE: Storage layer refactored for backend-agnostic access",
			"FIX: Worktree support - GIT_DIR/GIT_WORK_TREE in sync operations",
			"FIX: Graceful Dolt server-to-embedded fallback",
			"FIX: Multiple sync fixes for dolt-native mode and sync-branch",
			"FIX: Formula handlebars, ephemeral sync exclusion, daemon idempotency",
		},
	},
	{
		Version: "0.49.1",
		Date:    "2026-01-25",
		Changes: []string{
			"NEW: Dolt backend fully supported - Extensively tested and ready for community evaluation",
			"NOTE: Dolt is not enabled by default - We encourage users to try it and report feedback!",
			"NEW: bd activity --details/-d - Full issue information in activity feed (#1317)",
			"NEW: bd export --id/--parent - Targeted exports with filters (#1292)",
			"NEW: bd update --append-notes - Append to existing notes (#1304)",
			"NEW: bd show --id - For IDs that look like flags",
			"NEW: bd doctor --server - Dolt server mode health checks",
			"NEW: Dolt server mode - Multi-client access for shared Dolt databases",
			"NEW: Dolt auto-commit on writes with explicit commit authors (#1267)",
			"FIX: Daemon stack overflow on empty database path (#1288, #1313)",
			"FIX: bd list --json optimization - Fetch only needed dependencies (#1316)",
			"FIX: Import custom issue types (#1322)",
			"FIX: SQLite transaction improvements (#1272, #1276)",
			"FIX: Multiple Dolt backend fixes for hooks, routing, and daemon compatibility",
			"DOCS: Comprehensive docs/DOLT.md guide (#1310)",
		},
	},
	{
		Version: "0.49.0",
		Date:    "2026-01-21",
		Changes: []string{
			"NEW: Dolt federation - Peer-to-peer issue sync with bd federation sync command",
			"NEW: SQLite to Dolt migration - bd migrate dolt converts existing repos",
			"NEW: bd children <id> - Display child issues for a parent",
			"NEW: bd rename <old> <new> - Rename issue IDs",
			"NEW: bd view - Alias for bd show command",
			"NEW: bd config validate - Validate sync configuration",
			"NEW: Jujutsu (jj) VCS support - Beads now works with jj repositories",
			"NEW: Per-field merge strategies for conflict resolution",
			"NEW: -m flag as alias for --description in bd create",
			"CHANGED: Auto-routing disabled by default - Enable with routing.mode: auto (#1177)",
			"CHANGED: Gas Town types removed from core - Use types.custom configuration",
			"FIX: Daemon zombie state after DB replacement (#1213)",
			"FIX: WSL2 Docker Desktop - Detect bind mounts and disable WAL mode (#1224)",
			"FIX: Daemon stack overflow in handleStaleLock (#1238)",
			"FIX: Molecule steps excluded from bd ready (#1246)",
			"FIX: Tree ordering stabilization for consistent --tree output (#1228)",
		},
	},
	{
		Version: "0.48.0",
		Date:    "2026-01-17",
		Changes: []string{
			"NEW: VersionedStorage interface - Abstract storage layer with history/diff/branch operations",
			"NEW: bd types command - List valid issue types with descriptions",
			"NEW: bd close -m flag - Alias for --reason (git commit convention)",
			"NEW: RepoContext API - Centralized git operations context",
			"WIP: Dolt backend improvements - Bootstrap from JSONL, hook infrastructure, bd compact --dolt",
			"FIX: Doctor sync branch check - Removed destructive --fix behavior (GH#1062)",
			"FIX: Duplicate merge target - Use combined weight for better selection (GH#1022)",
			"FIX: Worktree exclude paths - Correct --git-common-dir usage (GH#1053)",
			"FIX: Daemon git.author - Apply configured author to sync commits",
			"FIX: Windows CGO-free builds - Enable building without CGO (#1117)",
			"FIX: Git hooks in worktrees - Fix hook execution in linked worktrees (#1126)",
		},
	},
	{
		Version: "0.47.2",
		Date:    "2026-01-14",
		Changes: []string{
			"NEW: Dolt backend (experimental) - bd init --backend=dolt for version-controlled storage",
			"NEW: bd show --children flag - Display child issues inline with parent",
			"NEW: Comprehensive NixOS support - Improved flake and home-manager integration",
			"FIX: Redirect + sync-branch incompatibility - bd sync works in redirected repos (bd-wayc3)",
			"FIX: Doctor project-level settings - Detects plugins/hooks/MCP in .claude/settings.json",
			"FIX: Contributor routing - bd init --contributor correctly sets up routing (#1088)",
			"CHANGED: Release workflow modernized - bump-version.sh replaced with molecule pointer",
			"DOCS: EXTENDING.md deprecated - Custom SQLite tables approach deprecated for Dolt migration",
		},
	},
	{
		Version: "0.47.1",
		Date:    "2026-01-12",
		Changes: []string{
			"NEW: bd list --ready flag - Show only issues with no blockers (bd-ihu31)",
			"NEW: Markdown rendering in comments - Enhanced display for notes (#1019)",
			"FIX: Nil pointer in wisp create - Prevent panic in molecule creation",
			"FIX: Route prefix for rig issues - Use correct prefix when creating (#1028)",
			"FIX: Duplicate merge target - Prefer issues with children/deps (GH#1022)",
			"FIX: SQLite cache rebuild after rename-prefix (GH#1016)",
			"FIX: MCP custom types - Support non-built-in types/statuses (#1023)",
			"FIX: Hyphenated prefix validation - Support hyphens in prefixes (#1013)",
			"FIX: Git worktree initialization - Prevent bd init in worktrees (#1026)",
		},
	},
	{
		Version: "0.47.0",
		Date:    "2026-01-11",
		Changes: []string{
			"NEW: Pull-first sync with 3-way merge - Reconciles local/remote before push (#918)",
			"NEW: bd resolve-conflicts command - Mechanical JSONL conflict resolution (bd-7e7ddffa)",
			"NEW: bd create --dry-run - Preview issue creation without side effects (bd-0hi7)",
			"NEW: bd ready --gated - Find molecules waiting on gates (bd-lhalq)",
			"NEW: Gate auto-discovery - Auto-discover workflow run ID in bd gate check (bd-fbkd)",
			"NEW: Multi-repo custom types - bd doctor discovers types across repos (bd-62g22)",
			"NEW: Stale DB handling - Read-only commands auto-import on stale DB (#977, #982)",
			"NEW: Linear project filter - linear.project_id config for sync (#938)",
			"FIX: Windows infinite loop in findLocalBeadsDir (GH#996)",
			"FIX: bd init hangs on Windows when not in git repo (#991)",
			"FIX: Daemon socket for deep paths - Long workspace paths now work (GH#1001)",
			"FIX: Prevent closing issues with open blockers (GH#962)",
			"FIX: bd edit parses EDITOR with args (GH#987)",
			"FIX: Worktree/redirect handling - Skip restore when redirected (bd-lmqhe)",
			"CHANGE: Daemon CLI refactored to subcommands (#1006)",
		},
	},
	{
		Version: "0.46.0",
		Date:    "2026-01-06",
		Changes: []string{
			"NEW: Custom type support - Configure custom issue types in config.yaml (bd-649s)",
			"NEW: Gas Town types extraction - Core Gas Town types in beads package (bd-i54l)",
			"FIX: Gate workflow discovery - Better matching of GitHub Actions runs (bd-m8ew)",
		},
	},
	{
		Version: "0.45.0",
		Date:    "2026-01-06",
		Changes: []string{
			"NEW: Dynamic shell completions - Tab complete issue IDs in bash/zsh/fish (#935)",
			"NEW: Android/Termux support - Native ARM64 binaries (#887)",
			"NEW: Deep pre-commit integration - bd doctor checks pre-commit configs (bd-28r5)",
			"NEW: Rig identity bead type - New 'rig' type for Gas Town tracking (gt-zmznh)",
			"NEW: --filter-parent alias - Alternative to --parent in bd list (bd-3p4u)",
			"NEW: Unified auto-sync config - Simpler daemon config for agents (#904)",
			"NEW: BD_SOCKET env var - Test isolation for daemon socket paths (#914)",
			"FIX: Init branch persistence - --branch flag persists to config.yaml (#934)",
			"FIX: Worktree resolution - Resolve worktrees by name from git registry (#921)",
			"FIX: Sync with redirect - Handle .beads/redirect in git status and import",
			"FIX: Doctor improvements - skip-worktree flag, duplicate detection, metadata queries",
			"FIX: Update prefix routing - bd update routes like bd show (bd-618f)",
		},
	},
	{
		Version: "0.44.0",
		Date:    "2026-01-04",
		Changes: []string{
			"NEW: Recipe-based setup - bd init refactored to modular recipes (bd-i3ed)",
			"NEW: Gate evaluation phases 2-4 - Timer, GitHub, cross-rig gate support",
			"NEW: bd gate check/discover/add-waiter/show - Gate workflow commands",
			"NEW: --blocks flag for bd dep add - Natural dependency syntax (GH#884)",
			"NEW: --blocked-by/--depends-on aliases for bd dep add (bd-09kt)",
			"NEW: Multi-prefix support - allowed_prefixes config option (#881)",
			"NEW: Sync divergence detection - JSONL/SQLite/git consistency checks (GH#885)",
			"NEW: PRIME.md override - Custom prime output per project (GH#876)",
			"NEW: Compound visualization - bd mol show displays compound structure (bd-iw4z)",
			"NEW: /handoff skill - Session cycling slash command (bd-xwvo)",
			"FIX: bd ready now shows in_progress issues (#894)",
			"FIX: macOS case-insensitive path handling for worktrees/daemon (GH#880)",
			"FIX: Sync metadata timing - finalize after commit not push (GH#885)",
			"FIX: Sparse checkout isolation - prevent config leak to main repo (GH#886)",
			"FIX: close_reason preserved during merge/sync (GH#891)",
			"FIX: Hyphenated rig names supported in agent IDs (GH#854, GH#868)",
		},
	},
	{
		Version: "0.43.0",
		Date:    "2026-01-02",
		Changes: []string{
			"NEW: Step.Gate evaluation Phase 1 - Human gates for workflow control",
			"NEW: bd lint command - Template validation against schema",
			"NEW: bd ready --pretty - Formatted human-friendly output",
			"FIX: Cross-rig routing for bd close and bd update",
			"FIX: Agent ID validation accepts any rig prefix (GH#827)",
			"FIX: bd sync in bare repo worktrees - Exit 128 error (GH#827)",
			"FIX: bd --no-db dep tree shows complete tree (GH#836)",
		},
	},
	{
		Version: "0.42.0",
		Date:    "2025-12-30",
		Changes: []string{
			"NEW: llms.txt standard support - AI agent discoverability endpoint (#784)",
			"NEW: bd preflight command - PR readiness checks (Phase 1)",
			"NEW: --claim flag for bd update - Atomic work queue semantics",
			"NEW: bd state/set-state commands - Label-based state management",
			"NEW: bd activity --town - Cross-rig aggregated activity feed",
			"NEW: Convoy issue type - Reactive completion with 'tracks' relation",
			"NEW: prepare-commit-msg hook - Agent identity trailers in commits",
			"NEW: Daemon RPC endpoints - Config and mol stale queries",
			"NEW: Non-TTY auto-detection - Cleaner output in pipes",
			"FIX: Git hook chaining now works correctly (GH#816)",
			"FIX: .beads/redirect not committed - Prevents worktree conflicts (GH#814)",
			"FIX: bd sync with sync-branch - Worktree copy direction fixed (GH#810, #812)",
			"FIX: sync.branch validation - Rejects main/master as sync branch (GH#807)",
			"FIX: Read operations read-only - No DB writes on list/ready/show (GH#804)",
			"FIX: bd list defaults - Non-closed issues, 50 limit (GH#788)",
			"FIX: External direct-commit bypass when sync.branch configured (bd-n663)",
			"FIX: Migration 022 SQL syntax error on v0.30.3 upgrade",
			"FIX: MCP plugin follows .beads/redirect files",
			"FIX: Jira sync error message when Python script not found (GH#803)",
		},
	},
	{
		Version: "0.41.0",
		Date:    "2025-12-29",
		Changes: []string{
			"NEW: bd swarm commands - Create/status/validate for multi-agent batch coordination",
			"NEW: bd repair command - Detect and repair orphaned foreign key references",
			"NEW: bd init --from-jsonl - Preserve manual JSONL edits on reinit",
			"NEW: bd human command - Focused help menu for humans",
			"NEW: bd show --short - Compact output mode for scripting",
			"NEW: bd delete --reason - Audit trail for deletions",
			"NEW: 'hooked' status - Hook-based work assignment for orchestrators",
			"NEW: mol_type schema field - Molecule classification tracking",
			"FIX: --var flag allows commas in values (GH#786)",
			"FIX: bd sync in bare repo worktrees (GH#785)",
			"FIX: bd delete --cascade recursive deletion (GH#787)",
			"FIX: bd doctor pre-push hook detection (GH#799)",
			"FIX: Illumos/Solaris disk space check (GH#798)",
			"FIX: hq- prefix routing - Correctly finds town root for routes.jsonl",
			"FIX: Pre-migration orphan cleanup - Avoids chicken-and-egg failures",
			"CHANGED: CLI command consolidation - Reduced top-level surface area",
			"CHANGED: Code organization - Split large cmd/bd files to meet 800-line limit",
		},
	},
	{
		Version: "0.39.1",
		Date:    "2025-12-27",
		Changes: []string{
			"NEW: bd where command - Show active beads location after following redirects",
			"NEW: --parent flag for bd update - Reparent issues between epics",
			"NEW: Redirect info in bd prime - Shows when database is redirected",
			"FIX: bd doctor follows redirects - Multi-clone compatibility",
			"FIX: Remove 8-char prefix limit - bd rename-prefix allows longer prefixes",
			"CHANGED: Git context consolidation - Internal refactor for efficiency",
			"DOCS: Database Redirects section - ADVANCED.md documentation",
			"DOCS: Community Tools update - Added opencode-beads to README",
		},
	},
	{
		Version: "0.39.0",
		Date:    "2025-12-27",
		Changes: []string{
			"NEW: bd orphans command - Detect issues mentioned in commits but never closed",
			"NEW: bd admin parent command - Consolidated cleanup/compact/reset under bd admin",
			"NEW: --prefix flag for bd create - Create issues in other rigs from any directory",
			"CHANGED: bd mol catalog → bd formula list - Aligns with formula terminology",
			"CHANGED: bd info --thanks - Contributors list moved under bd info",
			"CHANGED: Removed unused bd pin/unpin/hook commands - Use gt mol commands",
			"CHANGED: bd doctor --check=pollution - Test pollution check integrated into doctor",
			"FIX: macOS codesigning in bump-version.sh --install - Prevents quarantine issues",
			"FIX: Lint errors and Nix vendorHash - Clean builds on all platforms",
			"DOCS: Issue Statuses section in CLI_REFERENCE.md - Comprehensive status docs",
			"DOCS: Consolidated duplicate UI_PHILOSOPHY files - Single source of truth",
			"DOCS: README and PLUGIN.md fixes - Corrected installation instructions",
		},
	},
	{
		Version: "0.38.0",
		Date:    "2025-12-27",
		Changes: []string{
			"NEW: Prefix-based routing - bd commands auto-route to correct rig via routes.jsonl",
			"NEW: Cross-rig ID auto-resolve - bd dep add auto-resolves IDs across rigs",
			"NEW: bd mol pour/wisp moved under bd mol subcommand - cleaner command hierarchy",
			"NEW: bd show displays comments - Comments now visible in issue details",
			"NEW: created_by field on issues - Track issue creator for audit trail",
			"NEW: Database corruption recovery in bd doctor --fix - Auto-repair corrupted databases",
			"NEW: JSONL integrity check in bd doctor - Detect and fix malformed JSONL",
			"NEW: Git hygiene checks in bd doctor - Detect stale branches and sync issues",
			"NEW: pre-commit config for local lint enforcement - Consistent code quality",
			"NEW: Chaos testing flag for release script - --run-chaos-tests for thorough validation",
			"CHANGED: Sync backoff and tips consolidation - Smarter daemon sync timing",
			"CHANGED: Wisp/Ephemeral name finalized as 'wisp' - bd mol wisp is the canonical command",
			"FIX: Comments display outside dependents block - Proper formatting",
			"FIX: no-db mode storeActive initialization - JSONL-only mode works correctly",
			"FIX: --resolution alias restored for bd close - Backwards compatibility",
			"FIX: bd graph works with daemon running - Graph generation no longer conflicts",
			"FIX: created_by field in RPC path - Daemon correctly propagates creator",
			"FIX: Migration 028 idempotency - Migration handles partial/re-runs",
			"FIX: Routed IDs bypass daemon in show command - Cross-rig show works correctly",
			"FIX: Storage connections closed per iteration - Prevents resource leaks",
			"FIX: Modern git init compatibility - Tests use --initial-branch=main",
			"FIX: golangci-lint errors resolved - Clean lint on all platforms",
			"IMPROVED: Test coverage - doctor, daemon, storage, RPC client paths covered",
		},
	},
	{
		Version: "0.37.0",
		Date:    "2025-12-26",
		Changes: []string{
			"BREAKING: Ephemeral API rename - Wisp→Ephemeral: JSON 'wisp'→'ephemeral', bd wisp→bd ephemeral",
			"NEW: bd gate create/show/list/close/wait - Async coordination primitives for agent workflows",
			"NEW: bd gate eval - Evaluate timer gates and GitHub gates (gh:run, gh:pr, mail)",
			"NEW: bd gate approve - Human gate approval command",
			"NEW: bd close --suggest-next - Show newly unblocked issues after close",
			"NEW: bd ready/blocked --parent - Scope by epic or parent bead",
			"NEW: TOML support for formulas - .formula.toml files alongside JSON",
			"NEW: Fork repo auto-detection - Offer to configure .git/info/exclude",
			"NEW: Control flow operators - loop and gate operators for formula composition",
			"NEW: Aspect composition - Cross-cutting concerns via aspects field in formulas",
			"NEW: Runtime expansion - on_complete and for-each dynamic step generation",
			"NEW: bd formula list/show - Discover and inspect available formulas",
			"NEW: bd mol stale - Detect complete-but-unclosed molecules",
			"NEW: Stale molecules check in bd doctor - Proactive detection",
			"NEW: Distinct ID prefixes - bd-proto-xxx, bd-mol-xxx, bd-wisp-xxx",
			"NEW: no-git-ops config - bd config set no-git-ops true for manual git control",
			"NEW: beads-release formula - 18-step molecular workflow for version releases",
			"CHANGED: Formula format YAML→JSON - Formulas now use .formula.json extension",
			"CHANGED: bd mol run removed - Orchestration moved to gt commands",
			"CHANGED: Wisp architecture simplified - Single DB with Wisp=true flag",
			"FIX: Gate await fields preserved during upsert - Multirepo sync fix",
			"FIX: closed_at timestamp preserved during soft deletes",
			"FIX: Git detection caching - Eliminates worktree slowness",
			"FIX: installed_plugins.json v2 format - bd doctor handles new Claude Code format",
			"FIX: git.IsWorktree() hang on Windows - bd init no longer hangs outside git repos",
			"FIX: Skill files deleted by bd sync - .claude/ files now preserved",
			"FIX: doctor false positives - Skips interactions.jsonl and molecules.jsonl",
			"FIX: bd sync commits non-.beads files - Now only commits .beads/ directory",
			"FIX: Aspect self-matching recursion - Prevents infinite loops",
			"FIX: Map expansion nested matching - Correctly matches child steps",
			"FIX: Content-level merge for divergence - Better conflict resolution",
			"FIX: Windows MCP graceful fallback - Daemon mode on Windows",
			"FIX: Windows npm postinstall file locking - Install reliability",
		},
	},
	{
		Version: "0.36.0",
		Date:    "2025-12-24",
		Changes: []string{
			"NEW: Formula system - bd cook <formula> for declarative workflow templates",
			"NEW: Gate issue type - bd gate create/open/close for async coordination",
			"NEW: bd list --pretty --watch - Built-in colorized viewer with live updates",
			"NEW: bd search --after/--before/--priority/--content - Enhanced search filters",
			"NEW: bd export --priority - Exact priority filter for exports",
			"NEW: --resolution alias for --reason on bd close",
			"NEW: Config-based close hooks - Custom scripts on issue close",
			"CHANGED: bd mol spawn removed - Use bd pour/bd wisp create only",
			"CHANGED: bd ready excludes workflow types by default",
			"FIX: Child→parent deps now blocked - Prevents LLM temporal reasoning trap",
			"FIX: Dots in prefix handling - my.project prefixes work correctly",
			"FIX: Child counter updates - Explicit child IDs update counters",
			"FIX: Comment timestamps preserved during import",
			"FIX: sync.remote config respected in daemon",
			"FIX: Multi-hyphen prefixes - my-project-name works correctly",
			"FIX: Stealth mode uses .git/info/exclude - Truly local",
			"FIX: MCP output_schema=None for Claude Code",
			"IMPROVED: Test coverage - daemon 72%, compact 82%, setup 54%",
		},
	},
	{
		Version: "0.35.0",
		Date:    "2025-12-23",
		Changes: []string{
			"NEW: bd activity command - Real-time state feed for molecule monitoring",
			"NEW: Dynamic molecule bonding - bd mol bond --ref <id> attaches protos at runtime",
			"NEW: waits-for dependency type - Fanout gates for parallel step coordination",
			"NEW: Parallel step detection - Molecules auto-detect parallelizable steps",
			"NEW: bd list --parent flag - Filter issues by parent",
			"NEW: Molecule navigation - bd mol next/prev/current for step traversal",
			"NEW: Entity tracking types - Creator and Validations fields for work attribution",
			"IMPROVED: bd doctor --fix replaces manual commands",
			"IMPROVED: bd dep tree shows external dependencies",
			"IMPROVED: Performance indexes for large databases",
			"FIX: Rich mutation events emitted for status changes",
			"FIX: External deps filtered from GetBlockedIssues",
			"FIX: bd create -f works with daemon mode",
			"FIX: Parallel execution migration race conditions",
		},
	},
	{
		Version: "0.34.0",
		Date:    "2025-12-22",
		Changes: []string{
			"NEW: Wisp commands - bd wisp create/list/gc for ephemeral molecule management",
			"NEW: Chemistry UX - bd pour, bd mol bond --wisp/--pour for phase control",
			"NEW: Cross-project deps - external:<repo>:<id> syntax, bd ship command",
			"BREAKING: bd repo add/remove now writes to .beads/config.yaml (not DB)",
			"FIX: Wisps use Wisp=true flag in main database (not exported to JSONL)",
		},
	},
	{
		Version: "0.33.2",
		Date:    "2025-12-21",
		Changes: []string{
			"FIX: P0 priority preserved - omitempty removed from Priority field",
			"FIX: nil pointer check in markdown parsing",
			"CHORE: Remove dead deprecated wrapper functions from deletion_tracking.go",
		},
	},
	{
		Version: "0.33.1",
		Date:    "2025-12-21",
		Changes: []string{
			"BREAKING: Ephemeral → Wisp rename - JSON field changed from 'ephemeral' to 'wisp'",
			"BREAKING: CLI flag changed from --ephemeral to --wisp (bd cleanup)",
			"NOTE: SQLite column remains 'ephemeral' (no migration needed)",
		},
	},
	{
		Version: "0.33.0",
		Date:    "2025-12-21",
		Changes: []string{
			"NEW: Wisp molecules - use 'bd wisp create' for ephemeral wisps",
			"NEW: Wisp issues live only in SQLite, never export to JSONL (prevents zombie resurrection)",
			"NEW: Use 'bd pour' for persistent mols, 'bd wisp create' for ephemeral wisps",
			"NEW: bd mol squash compresses wisp children into digest issue",
			"NEW: --summary flag on bd mol squash for agent-provided AI summaries",
			"FIX: DeleteIssue now cascades to comments table",
		},
	},
	{
		Version: "0.32.1",
		Date:    "2025-12-21",
		Changes: []string{
			"NEW: MCP output control params - brief, brief_deps, fields, max_description_length",
			"NEW: MCP filtering params - labels, labels_any, query, unassigned, sort_policy",
			"NEW: BriefIssue, BriefDep, OperationResult models for 97% context reduction",
			"FIX: Pin field not in allowed update fields - bd update --pinned now works",
		},
	},
	{
		Version: "0.32.0",
		Date:    "2025-12-20",
		Changes: []string{
			"REMOVED: bd mail commands (send, inbox, read, ack, reply) - Mail is orchestration, not data plane",
			"NOTE: Data model unchanged - type=message, Sender, Ephemeral, replies_to fields remain",
			"NOTE: Orchestration tools should implement mail UI on top of beads data model",
			"FIX: Symlink preservation in atomicWriteFile - bd setup no longer clobbers nix/home-manager configs",
			"FIX: Broken link to LABELS.md in examples",
		},
	},
	{
		Version: "0.31.0",
		Date:    "2025-12-20",
		Changes: []string{
			"NEW: bd defer/bd undefer commands - Deferred status for icebox issues",
			"NEW: Agent audit trail - .beads/interactions.jsonl with bd audit record/label",
			"NEW: Directory-aware label scoping for monorepos - Auto-filter by directory.labels config",
			"NEW: Molecules catalog - Templates in separate molecules.jsonl with hierarchical loading",
			"NEW: Git commit config - git.author and git.no-gpg-sign options",
			"NEW: create.require-description config option",
			"CHANGED: bd stats merged into bd status - stats is now alias, colorized output",
			"CHANGED: Thin hook shims - Hooks delegate to bd hooks run, no more version drift",
			"CHANGED: MCP context tool consolidation - set_context/where_am_i/init merged into single context tool",
			"FIX: relates-to excluded from cycle detection",
			"FIX: Doctor checks .local_version instead of deprecated LastBdVersion",
			"FIX: Read-only gitignore in stealth mode prints manual instructions",
		},
	},
	{
		Version: "0.30.7",
		Date:    "2025-12-19",
		Changes: []string{
			"FIX: bd graph no longer crashes with nil pointer on epics",
			"FIX: Windows npm installer no longer fails with file lock error",
			"NEW: Version Bump molecule template for repeatable release workflows",
		},
	},
	{
		Version: "0.30.6",
		Date:    "2025-12-18",
		Changes: []string{
			"bd graph command shows dependency counts using subgraph formatting",
			"types.StatusPinned for persistent beads that survive cleanup",
			"CRITICAL: Fixed dependency resurrection bug in 3-way merge - removals now win",
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
			"bd template instantiate - Create beads issues from Beads templates",
			"--assignee flag for template instantiate - Auto-assign during instantiation",
			"bd mail inbox --identity fix - Now properly filters by identity parameter",
			"Orphan detection fixes - No longer warns about closed issues",
			"EXPERIMENTAL: Graph link fields (relates_to, replies_to, duplicate_of, superseded_by) and mail commands are subject to breaking changes",
		},
	},
	{
		Version: "0.30.3",
		Date:    "2025-12-17",
		Changes: []string{
			"SECURITY: Data loss race condition fixed - Removed unsafe ClearDirtyIssues() method",
			"Stale database warning - Commands now warn when DB is out of sync with JSONL",
			"Staleness check error handling improved - Proper warnings on check failures",
		},
	},
	{
		Version: "0.30.2",
		Date:    "2025-12-16",
		Changes: []string{
			"bd setup droid - Factory.ai (Droid) IDE support",
			"Messaging schema fields - New 'message' issue type, sender/wisp/replies_to/relates_to/duplicate_of/superseded_by fields",
			"New dependency types: replies-to, relates-to, duplicates, supersedes",
			"Windows build fixes - gosec lint errors resolved",
			"Issue ID prefix extraction fix - Word-like suffixes now parse correctly",
			"Legacy deletions.jsonl code removed - Dolt handles delete propagation natively",
		},
	},
	{
		Version: "0.30.1",
		Date:    "2025-12-16",
		Changes: []string{
			"bd reset command - Complete beads removal from a repository",
			"bd update --type flag - Change issue type after creation",
			"bd q silent mode - Quick-capture without output for scripting",
			"bd show displays dependent issue status - Shows status for blocked-by/blocking issues",
			"claude.local.md support - Local-only documentation, gitignored by default",
			"Auto-disable daemon in git worktrees - Prevents database conflicts",
			"Enhanced Git Worktree Support - Shared .beads database across worktrees",
		},
	},
	{
		Version: "0.30.0",
		Date:    "2025-12-15",
		Changes: []string{
			"Git Worktree Support - Shared database across worktrees, worktree-aware hooks",
			"MCP Context Engineering - 80-90% context reduction for MCP responses",
			"bd thanks command - List contributors to your project",
			"BD_NO_INSTALL_HOOKS env var - Disable automatic git hook installation",
			"Claude Code skill marketplace - Install beads skill via marketplace",
			"Daemon delete auto-sync - Delete operations trigger auto-sync",
			"close_reason persistence - Close reasons now saved to database on close",
			"JSONL-only mode improvements - GetReadyWork/GetBlockedIssues for memory storage",
			"Lock file improvements - Fast fail on stale locks, 98% test coverage",
		},
	},
	{
		Version: "0.29.0",
		Date:    "2025-12-03",
		Changes: []string{
			"--estimate flag for bd create/update - Add time estimates to issues in minutes",
			"bd doctor improvements - SQLite integrity check, config validation, stale sync branch detection",
			"bd doctor --output flag - Export diagnostics to file for sharing/debugging",
			"bd doctor --dry-run flag - Preview fixes without applying them",
			"bd doctor per-fix confirmation mode - Approve each fix individually",
			"--readonly flag - Read-only mode for worker sandboxes",
			"bd sync safety improvements - Auto-push after merge, diverged history handling",
			"Auto-resolve merge conflicts deterministically - All field conflicts resolved without prompts",
			"3-char all-letter base36 hash support - Fixes prefix extraction edge case",
		},
	},
	{
		Version: "0.28.0",
		Date:    "2025-12-01",
		Changes: []string{
			"bd daemon --local flag - Run daemon without git operations for multi-repo/worktree setups",
			"bd daemon --foreground flag - Run in foreground for systemd/supervisord integration",
			"bd migrate-sync command - Migrate to sync.branch workflow for cleaner main branch",
			"Database migration: close_reason column - Fixes sync loops with close_reason",
			"Multi-repo prefix filtering - Issues filtered by prefix when flushing from non-primary repos",
			"Parent-child dependency UX - Fixed documentation and UI labels for dependencies",
			"sync.branch workflow fixes - Fixed .beads/ restoration and doctor detection",
			"Jira API migration - Updated from deprecated v2 to v3 API",
		},
	},
	{
		Version: "0.27.2",
		Date:    "2025-11-30",
		Changes: []string{
			"CRITICAL: Mass database deletion protection - Safety guard prevents purging entire DB on JSONL reset",
			"Fresh Clone Initialization - bd init auto-detects prefix from existing JSONL, works without --prefix flag",
			"3-Character Hash Support - ExtractIssuePrefix now handles base36 hashes 3+ chars",
			"Import Warnings - New warning when issues skipped due to deletions manifest",
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
			"Out-of-Order Dependencies - JSONL import handles deps before targets exist",
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
			"MCP Protocol Fix - Subprocess stdin no longer breaks MCP JSON-RPC",
			"Git Worktree Staleness Fix - Staleness check works after writes in worktrees",
			"Multi-Part Prefix Support - Handles prefixes like 'my-app-123' correctly",
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
			"JSONL Canonicalization - Default JSONL filename is now issues.jsonl; legacy beads.jsonl still supported",
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
	infoCmd.Flags().Bool("thanks", false, "Show thank you page for contributors")
	infoCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	rootCmd.AddCommand(infoCmd)
}
