package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
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
	doctorFix            bool
	doctorYes            bool
	doctorInteractive    bool   // per-fix confirmation mode
	doctorDryRun         bool   // preview fixes without applying
	doctorOutput         string // export diagnostics to file
	doctorFixChildParent bool   // opt-in fix for child→parent deps
	doctorVerbose        bool   // show detailed output during fixes
	doctorForce          bool   // force repair mode, bypass validation where safe
	doctorSource         string // source of truth selection: auto, jsonl, db
	perfMode             bool
	checkHealthMode      bool
	doctorCheckFlag      string // run specific check (e.g., "pollution")
	doctorClean          bool   // for pollution check, delete detected issues
	doctorDeep           bool   // full graph integrity validation
)

// ConfigKeyHintsDoctor is the config key for suppressing doctor hints
const ConfigKeyHintsDoctor = "hints.doctor"

// minSyncBranchHookVersion is the minimum hook version that supports sync-branch bypass (issue #532)
const minSyncBranchHookVersion = "0.29.0"

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
  - Multiple database files
  - Multiple JSONL files
  - Daemon health (version mismatches, stale processes)
  - Database-JSONL sync status
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
  - pollution: Detect and optionally clean test issues from database

Deep Validation Mode (--deep):
  Validate full graph integrity. May be slow on large databases.
  Additional checks:
  - Parent consistency: All parent-child deps point to existing issues
  - Dependency integrity: All deps reference valid issues
  - Epic completeness: Find epics ready to close (all children closed)
  - Agent bead integrity: Agent beads have valid state values
  - Mail thread integrity: Thread IDs reference existing issues
  - Molecule integrity: Molecules have valid parent-child structures

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
  bd doctor --check=pollution          # Show potential test issues
  bd doctor --check=pollution --clean  # Delete test issues (with confirmation)
  bd doctor --deep             # Full graph integrity validation`,
	Run: func(cmd *cobra.Command, args []string) {
		// Use global jsonOutput set by PersistentPreRun

		// Determine path to check
		checkPath := "."
		if len(args) > 0 {
			checkPath = args[0]
		}

		// Convert to absolute path
		absPath, err := filepath.Abs(checkPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to resolve path: %v\n", err)
			os.Exit(1)
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
			default:
				fmt.Fprintf(os.Stderr, "Error: unknown check %q\n", doctorCheckFlag)
				fmt.Fprintf(os.Stderr, "Available checks: pollution\n")
				os.Exit(1)
			}
		}

		// Run deep validation if --deep flag is set
		if doctorDeep {
			runDeepValidation(absPath)
			return
		}

		// Run diagnostics
		result := runDiagnostics(absPath)

		// Preview fixes (dry-run) or apply fixes if requested
		if doctorDryRun {
			previewFixes(result)
		} else if doctorFix {
			applyFixes(result)
			// Re-run diagnostics to show results
			result = runDiagnostics(absPath)
		}

		// Add timestamp and platform info for export
		if doctorOutput != "" || jsonOutput {
			result.Timestamp = time.Now().UTC().Format(time.RFC3339)
			result.Platform = doctor.CollectPlatformInfo(absPath)
		}

		// Export to file if --output specified
		if doctorOutput != "" {
			if err := exportDiagnostics(result, doctorOutput); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to export diagnostics: %v\n", err)
				os.Exit(1)
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
	doctorCmd.Flags().BoolVarP(&doctorVerbose, "verbose", "v", false, "Show detailed output during fixes (e.g., list each removed dependency)")
	doctorCmd.Flags().BoolVar(&doctorForce, "force", false, "Force repair mode: attempt recovery even when database cannot be opened")
	doctorCmd.Flags().StringVar(&doctorSource, "source", "auto", "Choose source of truth for recovery: auto (detect), jsonl (prefer JSONL), db (prefer database)")
}

func runDiagnostics(path string) doctorResult {
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
	}

	// Check Git Hooks early (even if .beads/ doesn't exist yet)
	hooksCheck := convertWithCategory(doctor.CheckGitHooks(), doctor.CategoryGit)
	result.Checks = append(result.Checks, hooksCheck)
	// Don't fail overall check for missing hooks, just warn

	// Check sync-branch hook compatibility (issue #532)
	syncBranchHookCheck := convertWithCategory(doctor.CheckSyncBranchHookCompatibility(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, syncBranchHookCheck)
	if syncBranchHookCheck.Status == statusError {
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

	// Check 5: Multiple database files
	multiDBCheck := convertWithCategory(doctor.CheckMultipleDatabases(path), doctor.CategoryData)
	result.Checks = append(result.Checks, multiDBCheck)
	if multiDBCheck.Status == statusWarning || multiDBCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 6: Multiple JSONL files (excluding merge artifacts)
	jsonlCheck := convertWithCategory(doctor.CheckLegacyJSONLFilename(path), doctor.CategoryData)
	result.Checks = append(result.Checks, jsonlCheck)
	if jsonlCheck.Status == statusWarning || jsonlCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 6a: Legacy JSONL config (migrate beads.jsonl to issues.jsonl)
	legacyConfigCheck := convertWithCategory(doctor.CheckLegacyJSONLConfig(path), doctor.CategoryData)
	result.Checks = append(result.Checks, legacyConfigCheck)
	// Don't fail overall check for legacy config, just warn

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

	// Check 7b: JSONL integrity (malformed lines, missing IDs)
	jsonlIntegrityCheck := convertWithCategory(doctor.CheckJSONLIntegrity(path), doctor.CategoryData)
	result.Checks = append(result.Checks, jsonlIntegrityCheck)
	if jsonlIntegrityCheck.Status == statusWarning || jsonlIntegrityCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 8: Daemon health
	daemonCheck := convertWithCategory(doctor.CheckDaemonStatus(path, Version), doctor.CategoryRuntime)
	result.Checks = append(result.Checks, daemonCheck)
	if daemonCheck.Status == statusWarning || daemonCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 9: Database-JSONL sync
	syncCheck := convertWithCategory(doctor.CheckDatabaseJSONLSync(path), doctor.CategoryData)
	result.Checks = append(result.Checks, syncCheck)
	if syncCheck.Status == statusWarning || syncCheck.Status == statusError {
		result.OverallOK = false
	}

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

	// Check 11a: bd in PATH (needed for Claude hooks to work)
	bdPathCheck := convertWithCategory(doctor.CheckBdInPath(), doctor.CategoryIntegration)
	result.Checks = append(result.Checks, bdPathCheck)
	// Don't fail overall check for missing bd in PATH, just warn

	// Check 11b: Documentation bd prime references match installed version
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

	// Check 15: Git merge driver configuration
	mergeDriverCheck := convertWithCategory(doctor.CheckMergeDriver(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, mergeDriverCheck)
	// Don't fail overall check for merge driver, just warn

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

	// Check 17: Sync branch configuration
	syncBranchCheck := convertWithCategory(doctor.CheckSyncBranchConfig(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, syncBranchCheck)
	// Don't fail overall check for missing sync.branch, just warn

	// Check 17a: Sync branch health
	syncBranchHealthCheck := convertWithCategory(doctor.CheckSyncBranchHealth(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, syncBranchHealthCheck)
	// Don't fail overall check for sync branch health, just warn

	// Check 17b: Orphaned issues - referenced in commits but still open
	orphanedIssuesCheck := convertWithCategory(doctor.CheckOrphanedIssues(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, orphanedIssuesCheck)
	// Don't fail overall check for orphaned issues, just warn

	// Check 18: Deletions manifest (legacy, now replaced by tombstones)
	deletionsCheck := convertWithCategory(doctor.CheckDeletionsManifest(path), doctor.CategoryMetadata)
	result.Checks = append(result.Checks, deletionsCheck)
	// Don't fail overall check for missing deletions manifest, just warn

	// Check 19: Tombstones health
	tombstonesCheck := convertWithCategory(doctor.CheckTombstones(path), doctor.CategoryMetadata)
	result.Checks = append(result.Checks, tombstonesCheck)
	// Don't fail overall check for tombstone issues, just warn

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
	duplicatesCheck := convertDoctorCheck(doctor.CheckDuplicateIssues(path))
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

	// Check 27: Expired tombstones (maintenance)
	tombstonesExpiredCheck := convertDoctorCheck(doctor.CheckExpiredTombstones(path))
	result.Checks = append(result.Checks, tombstonesExpiredCheck)
	// Don't fail overall check for expired tombstones, just warn

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
	// Print header with version
	fmt.Printf("\nbd doctor v%s\n\n", result.CLIVersion)

	// Group checks by category
	checksByCategory := make(map[string][]doctorCheck)
	for _, check := range result.Checks {
		cat := check.Category
		if cat == "" {
			cat = "Other"
		}
		checksByCategory[cat] = append(checksByCategory[cat], check)
	}

	// Track counts
	var passCount, warnCount, failCount int
	var warnings []doctorCheck

	// Print checks by category in defined order
	for _, category := range doctor.CategoryOrder {
		checks, exists := checksByCategory[category]
		if !exists || len(checks) == 0 {
			continue
		}

		// Print category header
		fmt.Println(ui.RenderCategory(category))

		// Print each check in this category
		for _, check := range checks {
			// Determine status icon
			var statusIcon string
			switch check.Status {
			case statusOK:
				statusIcon = ui.RenderPassIcon()
				passCount++
			case statusWarning:
				statusIcon = ui.RenderWarnIcon()
				warnCount++
				warnings = append(warnings, check)
			case statusError:
				statusIcon = ui.RenderFailIcon()
				failCount++
				warnings = append(warnings, check)
			}

			// Print check line: icon + name + message
			fmt.Printf("  %s  %s", statusIcon, check.Name)
			if check.Message != "" {
				fmt.Printf("%s", ui.RenderMuted(" "+check.Message))
			}
			fmt.Println()

			// Print detail if present (indented)
			if check.Detail != "" {
				fmt.Printf("     %s%s\n", ui.MutedStyle.Render(ui.TreeLast), ui.RenderMuted(check.Detail))
			}
		}
		fmt.Println()
	}

	// Print any checks without a category
	if otherChecks, exists := checksByCategory["Other"]; exists && len(otherChecks) > 0 {
		fmt.Println(ui.RenderCategory("Other"))
		for _, check := range otherChecks {
			var statusIcon string
			switch check.Status {
			case statusOK:
				statusIcon = ui.RenderPassIcon()
				passCount++
			case statusWarning:
				statusIcon = ui.RenderWarnIcon()
				warnCount++
				warnings = append(warnings, check)
			case statusError:
				statusIcon = ui.RenderFailIcon()
				failCount++
				warnings = append(warnings, check)
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

	// Print summary line
	fmt.Println(ui.RenderSeparator())
	summary := fmt.Sprintf("%s %d passed  %s %d warnings  %s %d failed",
		ui.RenderPassIcon(), passCount,
		ui.RenderWarnIcon(), warnCount,
		ui.RenderFailIcon(), failCount,
	)
	fmt.Println(summary)

	// Print warnings/errors section with fixes
	if len(warnings) > 0 {
		fmt.Println()
		fmt.Println(ui.RenderWarn(ui.IconWarn + "  WARNINGS"))

		// Sort by severity: errors first, then warnings
		slices.SortStableFunc(warnings, func(a, b doctorCheck) int {
			// Errors (statusError) come before warnings (statusWarning)
			if a.Status == statusError && b.Status != statusError {
				return -1
			}
			if a.Status != statusError && b.Status == statusError {
				return 1
			}
			return 0 // maintain original order within same severity
		})

		for i, check := range warnings {
			// Show numbered items with icon and color based on status
			// Errors get entire line in red, warnings just the number in yellow
			line := fmt.Sprintf("%s: %s", check.Name, check.Message)
			if check.Status == statusError {
				fmt.Printf("  %s  %s %s\n", ui.RenderFailIcon(), ui.RenderFail(fmt.Sprintf("%d.", i+1)), ui.RenderFail(line))
			} else {
				fmt.Printf("  %s  %s %s\n", ui.RenderWarnIcon(), ui.RenderWarn(fmt.Sprintf("%d.", i+1)), line)
			}
			if check.Fix != "" {
				fmt.Printf("        %s%s\n", ui.MutedStyle.Render(ui.TreeLast), check.Fix)
			}
		}
	} else {
		fmt.Println()
		fmt.Printf("%s\n", ui.RenderPass("✓ All checks passed"))
	}
}

