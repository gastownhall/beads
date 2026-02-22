package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/ui"
)

// Status constants for doctor checks
const (
	statusOK      = "ok"
	statusWarning = "warning"
	statusError   = "error"
)

type doctorCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // statusOK, statusWarning, or statusError
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"` // Additional detail like storage type
	Fix      string `json:"fix,omitempty"`
	Category string `json:"category,omitempty"` // category for grouping in output
}

type doctorResult struct {
	Path       string            `json:"path"`
	Checks     []doctorCheck     `json:"checks"`
	OverallOK  bool              `json:"overall_ok"`
	CLIVersion string            `json:"cli_version"`
	Timestamp  string            `json:"timestamp,omitempty"` // ISO8601 timestamp for historical tracking
	Platform   map[string]string `json:"platform,omitempty"`  // platform info for debugging
}

var (
	doctorFix                  bool
	doctorYes                  bool
	doctorInteractive          bool   // per-fix confirmation mode
	doctorDryRun               bool   // preview fixes without applying
	doctorOutput               string // export diagnostics to file
	doctorFixChildParent       bool   // opt-in fix for child→parent deps
	doctorVerbose              bool   // show detailed output during fixes
	doctorForce                bool   // force repair mode, bypass validation where safe
	doctorSource               string // source of truth selection: auto, jsonl, db
	perfMode                   bool
	checkHealthMode            bool
	doctorCheckFlag            string // run specific check (e.g., "pollution")
	doctorClean                bool   // for pollution check, delete detected issues
	doctorDeep                 bool   // full graph integrity validation
	doctorGastown              bool   // running in gastown multi-workspace mode
	gastownDuplicatesThreshold int    // duplicate tolerance threshold for gastown mode
	doctorServer               bool   // run server mode health checks
	doctorMigration            string // migration validation mode: "pre" or "post"
)

// ConfigKeyHintsDoctor is the config key for suppressing doctor hints
const ConfigKeyHintsDoctor = "hints.doctor"

var doctorCmd = &cobra.Command{
	Use:     "doctor [path]",
	GroupID: "maint",
	Short:   "Check and fix beads installation health (start here)",
	Long: `Sanity check the beads installation for the current directory or specified path.

This command checks:
  - If .beads/ directory exists
  - Database version and migration status
  - Schema compatibility (all required tables and columns present)
  - Whether using hash-based vs sequential IDs
  - If CLI version is current (checks GitHub releases)
  - If Claude plugin is current (when running in Claude Code)
  - File permissions
  - Circular dependencies
  - Git hooks (pre-commit, post-merge, pre-push)
  - .beads/.gitignore up to date
  - Metadata.json version tracking (LastBdVersion field)

Performance Mode (--perf):
  Run performance diagnostics on your database:
  - Times key operations (bd ready, bd list, bd show, etc.)
  - Collects system info (OS, arch, SQLite version, database stats)
  - Generates CPU profile for analysis
  - Outputs shareable report for bug reports

Export Mode (--output):
  Save diagnostics to a JSON file for historical analysis and bug reporting.
  Includes timestamp and platform info for tracking intermittent issues.

Specific Check Mode (--check):
  Run a specific check in detail. Available checks:
  - artifacts: Detect and optionally clean beads classic artifacts
    (stale JSONL, SQLite files, cruft .beads dirs). Use with --clean.
  - pollution: Detect and optionally clean test issues from database
  - validate: Run focused data-integrity checks (duplicates, orphaned
    deps, test pollution, git conflicts). Use with --fix to auto-repair.

Deep Validation Mode (--deep):
  Validate full graph integrity. May be slow on large databases.
  Additional checks:
  - Parent consistency: All parent-child deps point to existing issues
  - Dependency integrity: All deps reference valid issues
  - Epic completeness: Find epics ready to close (all children closed)
  - Agent bead integrity: Agent beads have valid state values
  - Mail thread integrity: Thread IDs reference existing issues
  - Molecule integrity: Molecules have valid parent-child structures

Server Mode (--server):
  Run health checks for Dolt server mode connections (bd-dolt.2.3):
  - Server reachable: Can connect to configured host:port?
  - Dolt version: Is it a Dolt server (not vanilla MySQL)?
  - Database exists: Does the 'beads' database exist?
  - Schema compatible: Can query beads tables?
  - Connection pool: Pool health metrics

Migration Validation Mode (--migration):
  Run Dolt migration validation checks with machine-parseable output.
  Use --migration=pre before migration to verify readiness:
  - JSONL file exists and is valid (parseable, no corruption)
  - All JSONL issues are present in SQLite (or explains discrepancies)
  - No blocking issues prevent migration
  Use --migration=post after migration to verify completion:
  - Dolt database exists and is healthy
  - All issues from JSONL are present in Dolt
  - No data was lost during migration
  - Dolt database has no locks or uncommitted changes
  Combine with --json for machine-parseable output for automation.

Examples:
  bd doctor              # Check current directory
  bd doctor /path/to/repo # Check specific repository
  bd doctor --json       # Machine-readable output
  bd doctor --fix        # Automatically fix issues (with confirmation)
  bd doctor --fix --yes  # Automatically fix issues (no confirmation)
  bd doctor --fix -i     # Confirm each fix individually
  bd doctor --fix --fix-child-parent  # Also fix child→parent deps (opt-in)
  bd doctor --fix --force # Force repair even when database can't be opened
  bd doctor --fix --source=jsonl # Rebuild database from JSONL (source of truth)
  bd doctor --dry-run    # Preview what --fix would do without making changes
  bd doctor --perf       # Performance diagnostics
  bd doctor --output diagnostics.json  # Export diagnostics to file
  bd doctor --check=artifacts           # Show classic artifacts (JSONL, SQLite, cruft dirs)
  bd doctor --check=artifacts --clean  # Delete safe-to-delete artifacts (with confirmation)
  bd doctor --check=pollution          # Show potential test issues
  bd doctor --check=pollution --clean  # Delete test issues (with confirmation)
  bd doctor --check=validate         # Data-integrity checks only
  bd doctor --check=validate --fix   # Auto-fix data-integrity issues
  bd doctor --deep             # Full graph integrity validation
  bd doctor --server           # Dolt server mode health checks
  bd doctor --migration=pre    # Validate readiness for Dolt migration
  bd doctor --migration=post   # Validate Dolt migration completed
  bd doctor --migration=pre --json  # Machine-parseable migration validation`,
	Run: func(cmd *cobra.Command, args []string) {
		// Use global jsonOutput set by PersistentPreRun

		// Determine path to check
		// Precedence: explicit arg > BEADS_DIR (parent) > CWD
		var checkPath string
		if len(args) > 0 {
			checkPath = args[0]
		} else if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
			// BEADS_DIR points to .beads directory, doctor needs parent
			checkPath = filepath.Dir(beadsDir)
		} else {
			checkPath = "."
		}

		// Convert to absolute path
		absPath, err := filepath.Abs(checkPath)
		if err != nil {
			FatalError("failed to resolve path: %v", err)
		}

		// Run performance diagnostics if --perf flag is set
		if perfMode {
			doctor.RunPerformanceDiagnostics(absPath)
			return
		}

		// Run quick health check if --check-health flag is set
		if checkHealthMode {
			runCheckHealth(absPath)
			return
		}

		// Run specific check if --check flag is set
		if doctorCheckFlag != "" {
			switch doctorCheckFlag {
			case "pollution":
				runPollutionCheck(absPath, doctorClean, doctorYes)
				return
			case "validate":
				runValidateCheck(absPath)
				return
			case "artifacts":
				runArtifactsCheck(absPath, doctorClean, doctorYes)
				return
			default:
				FatalErrorWithHint(fmt.Sprintf("unknown check %q", doctorCheckFlag), "Available checks: artifacts, pollution, validate")
			}
		}

		// Run deep validation if --deep flag is set
		if doctorDeep {
			runDeepValidation(absPath)
			return
		}

		// Run server mode health checks if --server flag is set
		if doctorServer {
			runServerHealth(absPath)
			return
		}

		// Run migration validation if --migration flag is set
		if doctorMigration != "" {
			runMigrationValidation(absPath, doctorMigration)
			return
		}

		// Run diagnostics
		result := runDiagnostics(absPath)

		// Preview fixes (dry-run) or apply fixes if requested
		if doctorDryRun {
			previewFixes(result)
		} else if doctorFix {
			// Release any Dolt locks left by diagnostics before applying fixes.
			releaseDiagnosticLocks(absPath)
			applyFixes(result)
			// Note: we intentionally do NOT re-run diagnostics here.
			// If any Close() timed out during the first diagnostic pass,
			// leaked goroutines may hold internal noms locks and a second
			// open could deadlock. Users should run 'bd doctor' again to verify fixes.
			fmt.Println("\nRun 'bd doctor' again to verify fixes.")
		}

		// Add timestamp and platform info for export
		if doctorOutput != "" || jsonOutput {
			result.Timestamp = time.Now().UTC().Format(time.RFC3339)
			result.Platform = doctor.CollectPlatformInfo(absPath)
		}

		// Export to file if --output specified
		if doctorOutput != "" {
			if err := exportDiagnostics(result, doctorOutput); err != nil {
				FatalError("failed to export diagnostics: %v", err)
			}
			fmt.Printf("✓ Diagnostics exported to %s\n", doctorOutput)
		}

		// Output results
		if jsonOutput {
			outputJSON(result)
		} else if doctorOutput == "" {
			// Only print to console if not exporting (to avoid duplicate output)
			printDiagnostics(result)
		}

		// Exit with error if any checks failed
		if !result.OverallOK {
			os.Exit(1)
		}
	},
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorFix, "fix", false, "Automatically fix issues where possible")
	doctorCmd.Flags().BoolVarP(&doctorYes, "yes", "y", false, "Skip confirmation prompt (for non-interactive use)")
	doctorCmd.Flags().BoolVarP(&doctorInteractive, "interactive", "i", false, "Confirm each fix individually")
	doctorCmd.Flags().BoolVar(&doctorDryRun, "dry-run", false, "Preview fixes without making changes")
	doctorCmd.Flags().BoolVar(&doctorFixChildParent, "fix-child-parent", false, "Remove child→parent dependencies (opt-in)")
	doctorCmd.Flags().BoolVarP(&doctorVerbose, "verbose", "v", false, "Show all checks (default shows only warnings/errors)")
	doctorCmd.Flags().BoolVar(&doctorForce, "force", false, "Force repair mode: attempt recovery even when database cannot be opened")
	doctorCmd.Flags().StringVar(&doctorSource, "source", "auto", "Choose source of truth for recovery: auto (detect), jsonl (prefer JSONL), db (prefer database)")
	doctorCmd.Flags().BoolVar(&doctorGastown, "gastown", false, "Running in gastown multi-workspace mode (routes.jsonl is expected, higher duplicate tolerance)")
	doctorCmd.Flags().IntVar(&gastownDuplicatesThreshold, "gastown-duplicates-threshold", 1000, "Duplicate tolerance threshold for gastown mode (wisps are ephemeral)")
	doctorCmd.Flags().BoolVar(&doctorServer, "server", false, "Run Dolt server mode health checks (connectivity, version, schema)")
	doctorCmd.Flags().StringVar(&doctorMigration, "migration", "", "Run Dolt migration validation: 'pre' (before migration) or 'post' (after migration)")
}

// releaseDiagnosticLocks removes stale noms LOCK files that the diagnostics
// phase may have left behind. CloseWithTimeout can leave goroutines (and
// their LOCK files) behind when it times out.
func releaseDiagnosticLocks(path string) {
	beadsDir := filepath.Join(path, ".beads")
	beadsDir = beads.FollowRedirect(beadsDir)

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return // Can't determine config, skip cleanup
	}

	// Only clean up for Dolt backend.
	if cfg.GetBackend() != configfile.BackendDolt {
		return
	}

	doltPath := cfg.DatabasePath(beadsDir)
	entries, err := os.ReadDir(doltPath)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		nomsLock := filepath.Join(doltPath, entry.Name(), ".dolt", "noms", "LOCK")
		if _, err := os.Stat(nomsLock); err == nil {
			_ = os.Remove(nomsLock)
		}
	}
}

func runDiagnostics(path string) doctorResult {
	result := doctorResult{
		Path:       path,
		CLIVersion: Version,
		OverallOK:  true,
	}

	// Auto-detect gastown mode: routes.jsonl is only created by gastown workspaces
	if !doctorGastown {
		routesFile := filepath.Join(path, ".beads", "routes.jsonl")
		if _, err := os.Stat(routesFile); err == nil {
			doctorGastown = true
		}
	}

	// Check 1: Installation (.beads/ directory)
	installCheck := convertWithCategory(doctor.CheckInstallation(path), doctor.CategoryCore)
	result.Checks = append(result.Checks, installCheck)
	if installCheck.Status != statusOK {
		result.OverallOK = false
	}

	// Check Git Hooks early (even if .beads/ doesn't exist yet)
	hooksCheck := convertWithCategory(doctor.CheckGitHooks(Version), doctor.CategoryGit)
	result.Checks = append(result.Checks, hooksCheck)
	// Don't fail overall check for missing hooks, just warn

	// Check git hooks Dolt compatibility (hooks without Dolt check cause errors)
	doltHooksCheck := convertWithCategory(doctor.CheckGitHooksDoltCompatibility(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, doltHooksCheck)
	if doltHooksCheck.Status == statusError {
		result.OverallOK = false
	}

	// If no .beads/, skip remaining checks
	if installCheck.Status != statusOK {
		return result
	}

	// Check 1a: Fresh clone detection
	// Must come early - if this is a fresh clone, other checks may be misleading
	freshCloneCheck := convertWithCategory(doctor.CheckFreshClone(path), doctor.CategoryCore)
	result.Checks = append(result.Checks, freshCloneCheck)
	if freshCloneCheck.Status == statusWarning || freshCloneCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 2: Database version
	dbCheck := convertWithCategory(doctor.CheckDatabaseVersion(path, Version), doctor.CategoryCore)
	result.Checks = append(result.Checks, dbCheck)
	if dbCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 2a: Schema compatibility
	schemaCheck := convertWithCategory(doctor.CheckSchemaCompatibility(path), doctor.CategoryCore)
	result.Checks = append(result.Checks, schemaCheck)
	if schemaCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 2b: Repo fingerprint (detects wrong database or URL change)
	fingerprintCheck := convertWithCategory(doctor.CheckRepoFingerprint(path), doctor.CategoryCore)
	result.Checks = append(result.Checks, fingerprintCheck)
	if fingerprintCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 2c: Database integrity
	integrityCheck := convertWithCategory(doctor.CheckDatabaseIntegrity(path), doctor.CategoryCore)
	result.Checks = append(result.Checks, integrityCheck)
	if integrityCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 3: ID format (hash vs sequential)
	idCheck := convertWithCategory(doctor.CheckIDFormat(path), doctor.CategoryCore)
	result.Checks = append(result.Checks, idCheck)
	if idCheck.Status == statusWarning {
		result.OverallOK = false
	}

	// Check 4: CLI version (GitHub)
	versionCheck := convertWithCategory(doctor.CheckCLIVersion(Version), doctor.CategoryCore)
	result.Checks = append(result.Checks, versionCheck)
	// Don't fail overall check for outdated CLI, just warn

	// Check 4.5: Claude plugin version (if running in Claude Code)
	pluginCheck := convertWithCategory(doctor.CheckClaudePlugin(), doctor.CategoryIntegration)
	result.Checks = append(result.Checks, pluginCheck)
	// Don't fail overall check for outdated plugin, just warn

	// Check 7: Database/JSONL configuration mismatch
	configCheck := convertWithCategory(doctor.CheckDatabaseConfig(path), doctor.CategoryData)
	result.Checks = append(result.Checks, configCheck)
	if configCheck.Status == statusWarning || configCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 7a: Configuration value validation
	configValuesCheck := convertWithCategory(doctor.CheckConfigValues(path), doctor.CategoryData)
	result.Checks = append(result.Checks, configValuesCheck)
	// Don't fail overall check for config value warnings, just warn

	// Check 7b: Multi-repo custom types discovery (bd-9ji4z)
	multiRepoTypesCheck := convertWithCategory(doctor.CheckMultiRepoTypes(path), doctor.CategoryData)
	result.Checks = append(result.Checks, multiRepoTypesCheck)
	// Don't fail overall check for multi-repo types, just informational

	// Check 7c: Role configuration (beads.role)
	roleCheck := convertDoctorCheck(doctor.CheckBeadsRole(path))
	result.Checks = append(result.Checks, roleCheck)
	// Don't fail overall check for role config, just warn - URL heuristic fallback still works

	// Check 7d: JSONL integrity (malformed lines, missing IDs)
	jsonlIntegrityCheck := convertWithCategory(doctor.CheckJSONLIntegrity(path), doctor.CategoryData)
	result.Checks = append(result.Checks, jsonlIntegrityCheck)
	if jsonlIntegrityCheck.Status == statusWarning || jsonlIntegrityCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 7e: Stale lock files (bootstrap, sync, daemon, startup)
	staleLockCheck := convertDoctorCheck(doctor.CheckStaleLockFiles(path))
	result.Checks = append(result.Checks, staleLockCheck)
	if staleLockCheck.Status == statusWarning || staleLockCheck.Status == statusError {
		result.OverallOK = false
	}

	// Dolt health checks (connection, schema, sync, status via AccessLock)
	// Run BEFORE federation checks: federation opens Dolt connections that may
	// leave noms LOCK files on disk. CheckLockHealth (inside RunDoltHealthChecks)
	// must run first to avoid false positives from doctor's own connections (#1925).
	for _, dc := range doctor.RunDoltHealthChecks(path) {
		result.Checks = append(result.Checks, convertDoctorCheck(dc))
	}

	// Federation health checks (bd-wkumz.6)
	// Check 8d: Federation remotesapi port accessibility
	remotesAPICheck := convertWithCategory(doctor.CheckFederationRemotesAPI(path), doctor.CategoryFederation)
	result.Checks = append(result.Checks, remotesAPICheck)
	// Don't fail overall for federation issues - they're only relevant for Dolt users

	// Check 8e: Federation peer connectivity
	peerConnCheck := convertWithCategory(doctor.CheckFederationPeerConnectivity(path), doctor.CategoryFederation)
	result.Checks = append(result.Checks, peerConnCheck)

	// Check 8f: Federation sync staleness
	syncStalenessCheck := convertWithCategory(doctor.CheckFederationSyncStaleness(path), doctor.CategoryFederation)
	result.Checks = append(result.Checks, syncStalenessCheck)

	// Check 8g: Federation conflict detection
	fedConflictsCheck := convertWithCategory(doctor.CheckFederationConflicts(path), doctor.CategoryFederation)
	result.Checks = append(result.Checks, fedConflictsCheck)
	if fedConflictsCheck.Status == statusError {
		result.OverallOK = false // Unresolved conflicts are a real problem
	}

	// Check 8h: Dolt server mode configuration check
	doltModeCheck := convertWithCategory(doctor.CheckDoltServerModeMismatch(path), doctor.CategoryFederation)
	result.Checks = append(result.Checks, doltModeCheck)

	// Check 9a: Sync divergence (JSONL/SQLite/git) - GH#885
	syncDivergenceCheck := convertWithCategory(doctor.CheckSyncDivergence(path), doctor.CategoryData)
	result.Checks = append(result.Checks, syncDivergenceCheck)
	if syncDivergenceCheck.Status == statusError {
		result.OverallOK = false
	}
	// Warning-level divergence is informational, doesn't fail overall

	// Check 9: Permissions
	permCheck := convertWithCategory(doctor.CheckPermissions(path), doctor.CategoryCore)
	result.Checks = append(result.Checks, permCheck)
	if permCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 10: Dependency cycles
	cycleCheck := convertWithCategory(doctor.CheckDependencyCycles(path), doctor.CategoryMetadata)
	result.Checks = append(result.Checks, cycleCheck)
	if cycleCheck.Status == statusError || cycleCheck.Status == statusWarning {
		result.OverallOK = false
	}

	// Check 11: Claude integration
	claudeCheck := convertWithCategory(doctor.CheckClaude(), doctor.CategoryIntegration)
	result.Checks = append(result.Checks, claudeCheck)
	// Don't fail overall check for missing Claude integration, just warn

	// Check 11a: Claude settings file health (malformed JSON detection)
	claudeSettingsCheck := convertWithCategory(doctor.CheckClaudeSettingsHealth(), doctor.CategoryIntegration)
	result.Checks = append(result.Checks, claudeSettingsCheck)
	if claudeSettingsCheck.Status == statusError {
		result.OverallOK = false // Malformed settings is a real problem
	}

	// Check 11b: Claude hook completeness (both SessionStart and PreCompact)
	claudeHookCheck := convertWithCategory(doctor.CheckClaudeHookCompleteness(), doctor.CategoryIntegration)
	result.Checks = append(result.Checks, claudeHookCheck)
	// Don't fail overall check for incomplete hooks, just warn

	// Check 11c: bd prime output verification
	bdPrimeOutputCheck := convertWithCategory(doctor.VerifyPrimeOutput(), doctor.CategoryIntegration)
	result.Checks = append(result.Checks, bdPrimeOutputCheck)
	// Don't fail overall check for prime output issues, just warn

	// Check 11e: bd in PATH (needed for Claude hooks to work)
	bdPathCheck := convertWithCategory(doctor.CheckBdInPath(), doctor.CategoryIntegration)
	result.Checks = append(result.Checks, bdPathCheck)
	// Don't fail overall check for missing bd in PATH, just warn

	// Check 11f: Documentation bd prime references match installed version
	bdPrimeDocsCheck := convertWithCategory(doctor.CheckDocumentationBdPrimeReference(path), doctor.CategoryIntegration)
	result.Checks = append(result.Checks, bdPrimeDocsCheck)
	// Don't fail overall check for doc mismatch, just warn

	// Check 12: Agent documentation presence
	agentDocsCheck := convertWithCategory(doctor.CheckAgentDocumentation(path), doctor.CategoryIntegration)
	result.Checks = append(result.Checks, agentDocsCheck)
	// Don't fail overall check for missing docs, just warn

	// Check 13: Legacy beads slash commands in documentation
	legacyDocsCheck := convertWithCategory(doctor.CheckLegacyBeadsSlashCommands(path), doctor.CategoryMetadata)
	result.Checks = append(result.Checks, legacyDocsCheck)
	// Don't fail overall check for legacy docs, just warn

	// Check 13a: MCP tool references in documentation
	mcpToolRefsCheck := convertWithCategory(doctor.CheckLegacyMCPToolReferences(path), doctor.CategoryIntegration)
	result.Checks = append(result.Checks, mcpToolRefsCheck)
	// Don't fail overall check for MCP tool refs, just warn

	// Check 14: Gitignore up to date
	gitignoreCheck := convertWithCategory(doctor.CheckGitignore(), doctor.CategoryGit)
	result.Checks = append(result.Checks, gitignoreCheck)
	// Don't fail overall check for gitignore, just warn

	// Check 14a: issues.jsonl tracking (catches global gitignore conflicts)
	issuesTrackingCheck := convertWithCategory(doctor.CheckIssuesTracking(), doctor.CategoryGit)
	result.Checks = append(result.Checks, issuesTrackingCheck)
	// Don't fail overall check for tracking issues, just warn

	// Check 14b: redirect file tracking (worktree redirect files shouldn't be committed)
	redirectTrackingCheck := convertWithCategory(doctor.CheckRedirectNotTracked(), doctor.CategoryGit)
	result.Checks = append(result.Checks, redirectTrackingCheck)
	// Don't fail overall check for redirect tracking, just warn

	// Check 14c: redirect target validity (target exists and has valid db)
	redirectTargetCheck := convertWithCategory(doctor.CheckRedirectTargetValid(), doctor.CategoryGit)
	result.Checks = append(result.Checks, redirectTargetCheck)
	// Don't fail overall check for redirect target, just warn

	// Check 14d: redirect target sync worktree (target has beads-sync if needed)
	redirectTargetSyncCheck := convertWithCategory(doctor.CheckRedirectTargetSyncWorktree(), doctor.CategoryGit)
	result.Checks = append(result.Checks, redirectTargetSyncCheck)
	// Don't fail overall check for redirect target sync, just warn

	// Check 14e: vestigial sync worktrees (unused worktrees in redirected repos)
	vestigialWorktreesCheck := convertWithCategory(doctor.CheckNoVestigialSyncWorktrees(), doctor.CategoryGit)
	result.Checks = append(result.Checks, vestigialWorktreesCheck)
	// Don't fail overall check for vestigial worktrees, just warn

	// Check 14g: last-touched file tracking (runtime state shouldn't be committed)
	lastTouchedTrackingCheck := convertWithCategory(doctor.CheckLastTouchedNotTracked(), doctor.CategoryGit)
	result.Checks = append(result.Checks, lastTouchedTrackingCheck)
	// Don't fail overall check for last-touched tracking, just warn

	// Check 15a: Git working tree cleanliness (AGENTS.md hygiene)
	gitWorkingTreeCheck := convertWithCategory(doctor.CheckGitWorkingTree(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, gitWorkingTreeCheck)
	// Don't fail overall check for dirty working tree, just warn

	// Check 15b: Git upstream sync (ahead/behind/diverged)
	gitUpstreamCheck := convertWithCategory(doctor.CheckGitUpstream(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, gitUpstreamCheck)
	// Don't fail overall check for upstream drift, just warn

	// Check 16: Metadata.json version tracking
	metadataCheck := convertWithCategory(doctor.CheckMetadataVersionTracking(path, Version), doctor.CategoryMetadata)
	result.Checks = append(result.Checks, metadataCheck)
	// Don't fail overall check for metadata, just warn

	// Check 17b: Orphaned issues - referenced in commits but still open
	orphanedIssuesCheck := convertWithCategory(doctor.CheckOrphanedIssues(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, orphanedIssuesCheck)
	// Don't fail overall check for orphaned issues, just warn

	// Check 18: Deletions manifest (legacy)
	deletionsCheck := convertWithCategory(doctor.CheckDeletionsManifest(path), doctor.CategoryMetadata)
	result.Checks = append(result.Checks, deletionsCheck)
	// Don't fail overall check for missing deletions manifest, just warn

	// Check 20: Untracked .beads/*.jsonl files
	untrackedCheck := convertWithCategory(doctor.CheckUntrackedBeadsFiles(path), doctor.CategoryData)
	result.Checks = append(result.Checks, untrackedCheck)
	// Don't fail overall check for untracked files, just warn

	// Check 21: Merge artifacts (from bd clean)
	mergeArtifactsCheck := convertDoctorCheck(doctor.CheckMergeArtifacts(path))
	result.Checks = append(result.Checks, mergeArtifactsCheck)
	// Don't fail overall check for merge artifacts, just warn

	// Check 22: Orphaned dependencies (from bd repair-deps, bd validate)
	orphanedDepsCheck := convertDoctorCheck(doctor.CheckOrphanedDependencies(path))
	result.Checks = append(result.Checks, orphanedDepsCheck)
	// Don't fail overall check for orphaned deps, just warn

	// Check 22a: Child→parent dependencies (anti-pattern)
	childParentDepsCheck := convertDoctorCheck(doctor.CheckChildParentDependencies(path))
	result.Checks = append(result.Checks, childParentDepsCheck)
	// Don't fail overall check for child→parent deps, just warn

	// Check 23: Duplicate issues (from bd validate)
	duplicatesCheck := convertDoctorCheck(doctor.CheckDuplicateIssues(path, doctorGastown, gastownDuplicatesThreshold))
	result.Checks = append(result.Checks, duplicatesCheck)
	// Don't fail overall check for duplicates, just warn

	// Check 24: Test pollution (from bd validate)
	pollutionCheck := convertDoctorCheck(doctor.CheckTestPollution(path))
	result.Checks = append(result.Checks, pollutionCheck)
	// Don't fail overall check for test pollution, just warn

	// Check 25: Git conflicts in JSONL (from bd validate)
	conflictsCheck := convertDoctorCheck(doctor.CheckGitConflicts(path))
	result.Checks = append(result.Checks, conflictsCheck)
	if conflictsCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 26: Stale closed issues (maintenance)
	staleClosedCheck := convertDoctorCheck(doctor.CheckStaleClosedIssues(path))
	result.Checks = append(result.Checks, staleClosedCheck)
	// Don't fail overall check for stale issues, just warn

	// Check 26a: Stale molecules (complete but unclosed)
	staleMoleculesCheck := convertDoctorCheck(doctor.CheckStaleMolecules(path))
	result.Checks = append(result.Checks, staleMoleculesCheck)
	// Don't fail overall check for stale molecules, just warn

	// Check 26b: Persistent mol- issues (should have been ephemeral)
	persistentMolCheck := convertDoctorCheck(doctor.CheckPersistentMolIssues(path))
	result.Checks = append(result.Checks, persistentMolCheck)
	// Don't fail overall check for persistent mol issues, just warn

	// Check 26c: Legacy merge queue files (gastown mrqueue remnants)
	staleMQFilesCheck := convertDoctorCheck(doctor.CheckStaleMQFiles(path))
	result.Checks = append(result.Checks, staleMQFilesCheck)
	// Don't fail overall check for legacy MQ files, just warn

	// Check 26d: Patrol pollution (patrol digests, session beads)
	patrolPollutionCheck := convertDoctorCheck(doctor.CheckPatrolPollution(path))
	result.Checks = append(result.Checks, patrolPollutionCheck)
	// Don't fail overall check for patrol pollution, just warn

	// Check 28: Compaction candidates (maintenance)
	compactionCheck := convertDoctorCheck(doctor.CheckCompactionCandidates(path))
	result.Checks = append(result.Checks, compactionCheck)
	// Info only, not a warning - compaction requires human review

	// Check 29: Database size (pruning suggestion)
	// Note: This check has no auto-fix - pruning is destructive and user-controlled
	sizeCheck := convertDoctorCheck(doctor.CheckDatabaseSize(path))
	result.Checks = append(result.Checks, sizeCheck)
	// Don't fail overall check for size warning, just inform

	// Check 30: Pending migrations (summarizes all available migrations)
	migrationsCheck := convertDoctorCheck(doctor.CheckPendingMigrations(path))
	result.Checks = append(result.Checks, migrationsCheck)
	// Status is determined by the check itself based on migration priorities

	// Check 31: KV store sync status
	kvSyncCheck := convertDoctorCheck(doctor.CheckKVSyncStatus(path))
	result.Checks = append(result.Checks, kvSyncCheck)
	// Don't fail overall check for KV sync warning, just inform

	// Check 32: Dolt locks (uncommitted changes)
	doltLocksCheck := convertDoctorCheck(doctor.CheckDoltLocks(path))
	result.Checks = append(result.Checks, doltLocksCheck)
	// Don't fail overall check for Dolt locks, just warn

	// Check 33: Classic artifacts (post-Dolt-migration cleanup)
	classicArtifactsCheck := convertDoctorCheck(doctor.CheckClassicArtifacts(path))
	result.Checks = append(result.Checks, classicArtifactsCheck)
	// Don't fail overall check for classic artifacts, just warn

	return result
}

// runInitDiagnostics runs a limited subset of diagnostics appropriate for a
// freshly-initialized project. Unlike runDiagnostics (which checks everything),
// this only validates that the init itself succeeded: the .beads directory exists,
// the database is openable with correct schema, and permissions are correct.
// Checks that require git, federation remotes, or other post-setup configuration
// are skipped since they cannot be satisfied in a fresh project.
func runInitDiagnostics(path string) doctorResult {
	result := doctorResult{
		Path:       path,
		CLIVersion: Version,
		OverallOK:  true,
	}

	// Check 1: Installation (.beads/ directory)
	installCheck := convertWithCategory(doctor.CheckInstallation(path), doctor.CategoryCore)
	result.Checks = append(result.Checks, installCheck)
	if installCheck.Status != statusOK {
		result.OverallOK = false
		return result
	}

	// Check 2: Database version
	dbCheck := convertWithCategory(doctor.CheckDatabaseVersion(path, Version), doctor.CategoryCore)
	result.Checks = append(result.Checks, dbCheck)
	if dbCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 3: Schema compatibility
	schemaCheck := convertWithCategory(doctor.CheckSchemaCompatibility(path), doctor.CategoryCore)
	result.Checks = append(result.Checks, schemaCheck)
	if schemaCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 4: Permissions
	permCheck := convertWithCategory(doctor.CheckPermissions(path), doctor.CategoryCore)
	result.Checks = append(result.Checks, permCheck)
	if permCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 5: Dolt connection — validates init actually created a working DB
	doltConnCheck := convertDoctorCheck(doctor.CheckDoltConnection(path))
	result.Checks = append(result.Checks, doltConnCheck)
	if doltConnCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 6: Dolt schema — validates tables were created
	doltSchemaCheck := convertDoctorCheck(doctor.CheckDoltSchema(path))
	result.Checks = append(result.Checks, doltSchemaCheck)
	if doltSchemaCheck.Status == statusError {
		result.OverallOK = false
	}

	return result
}

// convertDoctorCheck converts doctor package check to main package check
func convertDoctorCheck(dc doctor.DoctorCheck) doctorCheck {
	return doctorCheck{
		Name:     dc.Name,
		Status:   dc.Status,
		Message:  dc.Message,
		Detail:   dc.Detail,
		Fix:      dc.Fix,
		Category: dc.Category,
	}
}

// convertWithCategory converts a doctor check and sets its category
func convertWithCategory(dc doctor.DoctorCheck, category string) doctorCheck {
	check := convertDoctorCheck(dc)
	check.Category = category
	return check
}

// exportDiagnostics writes the doctor result to a JSON file
func exportDiagnostics(result doctorResult, outputPath string) error {
	// #nosec G304 - outputPath is a user-provided flag value for file generation
	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("failed to write JSON: %w", err)
	}

	return nil
}

func printDiagnostics(result doctorResult) {
	// Pre-calculate counts and collect issues grouped by category
	checksByCategory := make(map[string][]doctorCheck)
	issuesByCategory := make(map[string][]doctorCheck)
	var passCount, warnCount, failCount int
	hasIssues := false

	for _, check := range result.Checks {
		cat := check.Category
		if cat == "" {
			cat = "Other"
		}
		checksByCategory[cat] = append(checksByCategory[cat], check)

		switch check.Status {
		case statusOK:
			passCount++
		case statusWarning:
			warnCount++
			issuesByCategory[cat] = append(issuesByCategory[cat], check)
			hasIssues = true
		case statusError:
			failCount++
			issuesByCategory[cat] = append(issuesByCategory[cat], check)
			hasIssues = true
		}
	}

	// Print header with version and summary
	fmt.Printf("\nbd doctor v%s", result.CLIVersion)
	fmt.Printf("  %s  %s %d passed  %s %d warnings  %s %d errors\n",
		ui.RenderSeparator(),
		ui.RenderPassIcon(), passCount,
		ui.RenderWarnIcon(), warnCount,
		ui.RenderFailIcon(), failCount,
	)

	if doctorVerbose {
		// Verbose mode: show all checks grouped by category
		fmt.Println()
		printAllChecks(checksByCategory)
	}

	// Print warnings/errors grouped by category
	if hasIssues {
		fmt.Println()

		// Walk categories in defined order, only showing those with issues
		for _, category := range doctor.CategoryOrder {
			issues, exists := issuesByCategory[category]
			if !exists || len(issues) == 0 {
				continue
			}

			// Sort within category: errors first, then warnings
			slices.SortStableFunc(issues, func(a, b doctorCheck) int {
				if a.Status == statusError && b.Status != statusError {
					return -1
				}
				if a.Status != statusError && b.Status == statusError {
					return 1
				}
				return 0
			})

			// Category header
			catChecks := checksByCategory[category]
			catPass := 0
			for _, c := range catChecks {
				if c.Status == statusOK {
					catPass++
				}
			}
			fmt.Printf("%s %s\n", ui.RenderCategory(category),
				ui.RenderMuted(fmt.Sprintf("(%d/%d passed)", catPass, len(catChecks))))

			for _, check := range issues {
				line := fmt.Sprintf("%s: %s", check.Name, check.Message)
				if check.Status == statusError {
					fmt.Printf("  %s  %s\n", ui.RenderFailIcon(), ui.RenderFail(line))
				} else {
					fmt.Printf("  %s  %s\n", ui.RenderWarnIcon(), line)
				}
				if check.Detail != "" {
					fmt.Printf("      %s\n", ui.RenderMuted(check.Detail))
				}
				if check.Fix != "" {
					lines := strings.Split(check.Fix, "\n")
					for j, fixLine := range lines {
						if j == 0 {
							fmt.Printf("      %s%s\n", ui.MutedStyle.Render(ui.TreeLast), fixLine)
						} else {
							fmt.Printf("        %s\n", fixLine)
						}
					}
				}
			}
			fmt.Println()
		}

		// Handle "Other" category
		if otherIssues, exists := issuesByCategory["Other"]; exists && len(otherIssues) > 0 {
			fmt.Printf("%s\n", ui.RenderCategory("Other"))
			for _, check := range otherIssues {
				line := fmt.Sprintf("%s: %s", check.Name, check.Message)
				if check.Status == statusError {
					fmt.Printf("  %s  %s\n", ui.RenderFailIcon(), ui.RenderFail(line))
				} else {
					fmt.Printf("  %s  %s\n", ui.RenderWarnIcon(), line)
				}
				if check.Detail != "" {
					fmt.Printf("      %s\n", ui.RenderMuted(check.Detail))
				}
				if check.Fix != "" {
					lines := strings.Split(check.Fix, "\n")
					for j, fixLine := range lines {
						if j == 0 {
							fmt.Printf("      %s%s\n", ui.MutedStyle.Render(ui.TreeLast), fixLine)
						} else {
							fmt.Printf("        %s\n", fixLine)
						}
					}
				}
			}
			fmt.Println()
		}

		if !doctorVerbose {
			fmt.Printf("%s\n", ui.RenderMuted("Run with --verbose to see all checks"))
		}
	} else {
		fmt.Println()
		fmt.Printf("%s\n", ui.RenderPass("✓ All checks passed"))
		if !doctorVerbose {
			fmt.Printf("%s\n", ui.RenderMuted("Run with --verbose to see all checks"))
		}
	}
}

// printAllChecks prints all checks grouped by category with section headers.
func printAllChecks(checksByCategory map[string][]doctorCheck) {
	// Print checks in defined category order
	for _, category := range doctor.CategoryOrder {
		checks, exists := checksByCategory[category]
		if !exists || len(checks) == 0 {
			continue
		}

		fmt.Println(ui.RenderCategory(category))

		for _, check := range checks {
			var statusIcon string
			switch check.Status {
			case statusOK:
				statusIcon = ui.RenderPassIcon()
			case statusWarning:
				statusIcon = ui.RenderWarnIcon()
			case statusError:
				statusIcon = ui.RenderFailIcon()
			}

			fmt.Printf("  %s  %s", statusIcon, check.Name)
			if check.Message != "" {
				fmt.Printf("%s", ui.RenderMuted(" "+check.Message))
			}
			fmt.Println()

			if check.Detail != "" {
				fmt.Printf("     %s%s\n", ui.MutedStyle.Render(ui.TreeLast), ui.RenderMuted(check.Detail))
			}
		}
		fmt.Println()
	}

	// Print any checks without a known category
	if otherChecks, exists := checksByCategory["Other"]; exists && len(otherChecks) > 0 {
		fmt.Println(ui.RenderCategory("Other"))
		for _, check := range otherChecks {
			var statusIcon string
			switch check.Status {
			case statusOK:
				statusIcon = ui.RenderPassIcon()
			case statusWarning:
				statusIcon = ui.RenderWarnIcon()
			case statusError:
				statusIcon = ui.RenderFailIcon()
			}
			fmt.Printf("  %s  %s", statusIcon, check.Name)
			if check.Message != "" {
				fmt.Printf("%s", ui.RenderMuted(" "+check.Message))
			}
			fmt.Println()
			if check.Detail != "" {
				fmt.Printf("     %s%s\n", ui.MutedStyle.Render(ui.TreeLast), ui.RenderMuted(check.Detail))
			}
		}
		fmt.Println()
	}
}

// runMigrationValidation runs Dolt migration validation checks.
// Phase can be "pre" (before migration) or "post" (after migration).
// Outputs machine-parseable JSON when --json flag is set.
func runMigrationValidation(path string, phase string) {
	var check doctorCheck
	var result doctor.MigrationValidationResult

	switch phase {
	case "pre":
		dc, mr := doctor.CheckMigrationReadiness(path)
		check = convertDoctorCheck(dc)
		result = mr
	case "post":
		dc, mr := doctor.CheckMigrationCompletion(path)
		check = convertDoctorCheck(dc)
		result = mr
	default:
		FatalError("invalid migration phase %q (use 'pre' or 'post')", phase)
	}

	// JSON output for machine consumption
	if jsonOutput {
		output := struct {
			Check      doctorCheck                      `json:"check"`
			Validation doctor.MigrationValidationResult `json:"validation"`
			CLIVersion string                           `json:"cli_version"`
			Timestamp  string                           `json:"timestamp"`
		}{
			Check:      check,
			Validation: result,
			CLIVersion: Version,
			Timestamp:  time.Now().UTC().Format(time.RFC3339),
		}
		outputJSON(output)
		if !result.Ready {
			os.Exit(1)
		}
		return
	}

	// Human-readable output
	fmt.Printf("\nbd doctor --migration=%s v%s\n\n", phase, Version)

	// Print main status
	var statusIcon string
	switch check.Status {
	case statusOK:
		statusIcon = ui.RenderPassIcon()
	case statusWarning:
		statusIcon = ui.RenderWarnIcon()
	case statusError:
		statusIcon = ui.RenderFailIcon()
	}

	fmt.Printf("%s  %s: %s\n", statusIcon, check.Name, check.Message)
	if check.Detail != "" {
		for _, line := range strings.Split(check.Detail, "\n") {
			fmt.Printf("     %s\n", ui.RenderMuted(line))
		}
	}

	// Print validation details
	fmt.Println()
	fmt.Println(ui.RenderCategory("Validation Details"))
	fmt.Printf("  Backend:     %s\n", result.Backend)
	fmt.Printf("  JSONL Count: %d\n", result.JSONLCount)
	if result.SQLiteCount > 0 {
		fmt.Printf("  SQLite Count: %d\n", result.SQLiteCount)
	}
	if result.DoltCount > 0 {
		fmt.Printf("  Dolt Count:  %d\n", result.DoltCount)
	}
	fmt.Printf("  JSONL Valid: %v\n", result.JSONLValid)
	if result.JSONLMalformed > 0 {
		fmt.Printf("  Malformed Lines: %d\n", result.JSONLMalformed)
	}

	// Print warnings
	if len(result.Warnings) > 0 {
		fmt.Println()
		fmt.Println(ui.RenderCategory("Warnings"))
		for _, warn := range result.Warnings {
			fmt.Printf("  %s  %s\n", ui.RenderWarnIcon(), warn)
		}
	}

	// Print errors
	if len(result.Errors) > 0 {
		fmt.Println()
		fmt.Println(ui.RenderCategory("Errors"))
		for _, err := range result.Errors {
			fmt.Printf("  %s  %s\n", ui.RenderFailIcon(), err)
		}
	}

	// Print fix suggestion
	if check.Fix != "" {
		fmt.Println()
		fmt.Printf("%s  %s\n", ui.RenderMuted("Fix:"), check.Fix)
	}

	fmt.Println()
	if result.Ready {
		fmt.Printf("%s\n", ui.RenderPass("✓ Migration validation passed"))
	} else {
		fmt.Printf("%s\n", ui.RenderFail("✗ Migration validation failed"))
		os.Exit(1)
	}
}
