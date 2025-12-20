package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/syncbranch"
)

// Status constants for doctor checks
const (
	statusOK      = "ok"
	statusWarning = "warning"
	statusError   = "error"
)

type doctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // statusOK, statusWarning, or statusError
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"` // Additional detail like storage type
	Fix     string `json:"fix,omitempty"`
}

type doctorResult struct {
	Path       string            `json:"path"`
	Checks     []doctorCheck     `json:"checks"`
	OverallOK  bool              `json:"overall_ok"`
	CLIVersion string            `json:"cli_version"`
	Timestamp  string            `json:"timestamp,omitempty"`  // bd-9cc: ISO8601 timestamp for historical tracking
	Platform   map[string]string `json:"platform,omitempty"`   // bd-9cc: platform info for debugging
}

var (
	doctorFix         bool
	doctorYes         bool
	doctorInteractive bool   // bd-3xl: per-fix confirmation mode
	doctorDryRun      bool   // bd-a5z: preview fixes without applying
	doctorOutput      string // bd-9cc: export diagnostics to file
	perfMode          bool
	checkHealthMode   bool
)

// ConfigKeyHintsDoctor is the config key for suppressing doctor hints
const ConfigKeyHintsDoctor = "hints.doctor"

// minSyncBranchHookVersion is the minimum hook version that supports sync-branch bypass (issue #532)
const minSyncBranchHookVersion = "0.29.0"

var doctorCmd = &cobra.Command{
	Use:   "doctor [path]",
	Short: "Check beads installation health",
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

Examples:
  bd doctor              # Check current directory
  bd doctor /path/to/repo # Check specific repository
  bd doctor --json       # Machine-readable output
  bd doctor --fix        # Automatically fix issues (with confirmation)
  bd doctor --fix --yes  # Automatically fix issues (no confirmation)
  bd doctor --fix -i     # Confirm each fix individually (bd-3xl)
  bd doctor --dry-run    # Preview what --fix would do without making changes
  bd doctor --perf       # Performance diagnostics
  bd doctor --output diagnostics.json  # Export diagnostics to file`,
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

		// Run diagnostics
		result := runDiagnostics(absPath)

		// bd-a5z: Preview fixes (dry-run) or apply fixes if requested
		if doctorDryRun {
			previewFixes(result)
		} else if doctorFix {
			applyFixes(result)
			// Re-run diagnostics to show results
			result = runDiagnostics(absPath)
		}

		// bd-9cc: Add timestamp and platform info for export
		if doctorOutput != "" || jsonOutput {
			result.Timestamp = time.Now().UTC().Format(time.RFC3339)
			result.Platform = doctor.CollectPlatformInfo(absPath)
		}

		// bd-9cc: Export to file if --output specified
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
	doctorCmd.Flags().BoolVarP(&doctorInteractive, "interactive", "i", false, "Confirm each fix individually (bd-3xl)")
	doctorCmd.Flags().BoolVar(&doctorDryRun, "dry-run", false, "Preview fixes without making changes (bd-a5z)")
}

// previewFixes shows what would be fixed without applying changes (bd-a5z)
func previewFixes(result doctorResult) {
	// Collect all fixable issues
	var fixableIssues []doctorCheck
	for _, check := range result.Checks {
		if (check.Status == statusWarning || check.Status == statusError) && check.Fix != "" {
			fixableIssues = append(fixableIssues, check)
		}
	}

	if len(fixableIssues) == 0 {
		fmt.Println("\n✓ No fixable issues found (dry-run)")
		return
	}

	fmt.Println("\n[DRY-RUN] The following issues would be fixed with --fix:")
	fmt.Println()

	for i, issue := range fixableIssues {
		// Show the issue details
		fmt.Printf("  %d. %s\n", i+1, issue.Name)
		if issue.Status == statusError {
			color.Red("     Status: ERROR\n")
		} else {
			color.Yellow("     Status: WARNING\n")
		}
		fmt.Printf("     Issue:  %s\n", issue.Message)
		if issue.Detail != "" {
			fmt.Printf("     Detail: %s\n", issue.Detail)
		}
		fmt.Printf("     Fix:    %s\n", issue.Fix)
		fmt.Println()
	}

	fmt.Printf("[DRY-RUN] Would attempt to fix %d issue(s)\n", len(fixableIssues))
	fmt.Println("Run 'bd doctor --fix' to apply these fixes")
}

func applyFixes(result doctorResult) {
	// Collect all fixable issues
	var fixableIssues []doctorCheck
	for _, check := range result.Checks {
		if (check.Status == statusWarning || check.Status == statusError) && check.Fix != "" {
			fixableIssues = append(fixableIssues, check)
		}
	}

	if len(fixableIssues) == 0 {
		fmt.Println("\nNo fixable issues found.")
		return
	}

	// Show what will be fixed
	fmt.Println("\nFixable issues:")
	for i, issue := range fixableIssues {
		fmt.Printf("  %d. %s: %s\n", i+1, issue.Name, issue.Message)
	}

	// bd-3xl: Interactive mode - confirm each fix individually
	if doctorInteractive {
		applyFixesInteractive(result.Path, fixableIssues)
		return
	}

	// Ask for confirmation (skip if --yes flag is set)
	if !doctorYes {
		fmt.Printf("\nThis will attempt to fix %d issue(s). Continue? (Y/n): ", len(fixableIssues))
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			return
		}

		response = strings.TrimSpace(strings.ToLower(response))
		if response != "" && response != "y" && response != "yes" {
			fmt.Println("Fix canceled.")
			return
		}
	}

	// Apply fixes
	fmt.Println("\nApplying fixes...")
	applyFixList(result.Path, fixableIssues)
}

// applyFixesInteractive prompts for each fix individually (bd-3xl)
func applyFixesInteractive(path string, issues []doctorCheck) {
	reader := bufio.NewReader(os.Stdin)
	applyAll := false
	var approvedFixes []doctorCheck

	fmt.Println("\nReview each fix:")
	fmt.Println("  [y]es - apply this fix")
	fmt.Println("  [n]o  - skip this fix")
	fmt.Println("  [a]ll - apply all remaining fixes")
	fmt.Println("  [q]uit - stop without applying more fixes")
	fmt.Println()

	for i, issue := range issues {
		// Show issue details
		fmt.Printf("(%d/%d) %s\n", i+1, len(issues), issue.Name)
		if issue.Status == statusError {
			color.Red("  Status: ERROR\n")
		} else {
			color.Yellow("  Status: WARNING\n")
		}
		fmt.Printf("  Issue:  %s\n", issue.Message)
		if issue.Detail != "" {
			fmt.Printf("  Detail: %s\n", issue.Detail)
		}
		fmt.Printf("  Fix:    %s\n", issue.Fix)

		// Check if we should apply all remaining
		if applyAll {
			fmt.Println("  → Auto-approved (apply all)")
			approvedFixes = append(approvedFixes, issue)
			continue
		}

		// Prompt for this fix
		fmt.Print("\n  Apply this fix? [y/n/a/q]: ")
		response, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			return
		}

		response = strings.TrimSpace(strings.ToLower(response))
		switch response {
		case "y", "yes":
			approvedFixes = append(approvedFixes, issue)
			fmt.Println("  → Approved")
		case "n", "no", "":
			fmt.Println("  → Skipped")
		case "a", "all":
			applyAll = true
			approvedFixes = append(approvedFixes, issue)
			fmt.Println("  → Approved (applying all remaining)")
		case "q", "quit":
			fmt.Println("  → Quit")
			if len(approvedFixes) > 0 {
				fmt.Printf("\nApplying %d approved fix(es)...\n", len(approvedFixes))
				applyFixList(path, approvedFixes)
			} else {
				fmt.Println("\nNo fixes applied.")
			}
			return
		default:
			// Treat unknown input as skip
			fmt.Println("  → Skipped (unrecognized input)")
		}
		fmt.Println()
	}

	// Apply all approved fixes
	if len(approvedFixes) > 0 {
		fmt.Printf("\nApplying %d approved fix(es)...\n", len(approvedFixes))
		applyFixList(path, approvedFixes)
	} else {
		fmt.Println("\nNo fixes approved.")
	}
}

// applyFixList applies a list of fixes and reports results
func applyFixList(path string, fixes []doctorCheck) {
	fixedCount := 0
	errorCount := 0

	for _, check := range fixes {
		fmt.Printf("\nFixing %s...\n", check.Name)

		var err error
		switch check.Name {
		case "Gitignore":
			err = doctor.FixGitignore()
		case "Git Hooks":
			err = fix.GitHooks(path)
		case "Daemon Health":
			err = fix.Daemon(path)
		case "DB-JSONL Sync":
			err = fix.DBJSONLSync(path)
		case "Permissions":
			err = fix.Permissions(path)
		case "Database":
			err = fix.DatabaseVersion(path)
		case "Schema Compatibility":
			err = fix.SchemaCompatibility(path)
		case "Git Merge Driver":
			err = fix.MergeDriver(path)
		case "Sync Branch Config":
			// No auto-fix: sync-branch should be added to config.yaml (version controlled)
			fmt.Printf("  ⚠ Add 'sync-branch: beads-sync' to .beads/config.yaml\n")
			continue
		case "Database Config":
			err = fix.DatabaseConfig(path)
		case "JSONL Config":
			err = fix.LegacyJSONLConfig(path)
		case "Deletions Manifest":
			err = fix.MigrateTombstones(path)
		case "Untracked Files":
			err = fix.UntrackedJSONL(path)
		case "Sync Branch Health":
			// Get sync branch from config
			syncBranch := syncbranch.GetFromYAML()
			if syncBranch == "" {
				fmt.Printf("  ⚠ No sync branch configured in config.yaml\n")
				continue
			}
			err = fix.SyncBranchHealth(path, syncBranch)
		default:
			fmt.Printf("  ⚠ No automatic fix available for %s\n", check.Name)
			fmt.Printf("  Manual fix: %s\n", check.Fix)
			continue
		}

		if err != nil {
			errorCount++
			color.Red("  ✗ Error: %v\n", err)
			fmt.Printf("  Manual fix: %s\n", check.Fix)
		} else {
			fixedCount++
			color.Green("  ✓ Fixed\n")
		}
	}

	// Summary
	fmt.Printf("\nFix summary: %d fixed, %d errors\n", fixedCount, errorCount)
	if errorCount > 0 {
		fmt.Println("\nSome fixes failed. Please review the errors above and apply manual fixes as needed.")
	}
}

// runCheckHealth runs lightweight health checks for git hooks.
// Silent on success, prints a hint if issues detected.
// Respects hints.doctor config setting.
func runCheckHealth(path string) {
	beadsDir := filepath.Join(path, ".beads")

	// Check if .beads/ exists
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		// No .beads directory - nothing to check
		return
	}

	// Get database path once (bd-b8h: centralized path resolution)
	dbPath := getCheckHealthDBPath(beadsDir)

	// Check if database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// No database - only check hooks
		if issue := doctor.CheckHooksQuick(Version); issue != "" {
			printCheckHealthHint([]string{issue})
		}
		return
	}

	// Open database once for all checks (bd-xyc: single DB connection)
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err != nil {
		// Can't open DB - only check hooks
		if issue := doctor.CheckHooksQuick(Version); issue != "" {
			printCheckHealthHint([]string{issue})
		}
		return
	}
	defer db.Close()

	// Check if hints.doctor is disabled in config
	if hintsDisabledDB(db) {
		return
	}

	// Run lightweight checks
	var issues []string

	// Check 1: Database version mismatch (CLI vs database bd_version)
	if issue := checkVersionMismatchDB(db); issue != "" {
		issues = append(issues, issue)
	}

	// Check 2: Sync branch not configured (now reads from config.yaml, not DB)
	if issue := doctor.CheckSyncBranchQuick(); issue != "" {
		issues = append(issues, issue)
	}

	// Check 3: Outdated git hooks
	if issue := doctor.CheckHooksQuick(Version); issue != "" {
		issues = append(issues, issue)
	}

	// Check 3: Sync-branch hook compatibility (issue #532)
	if issue := doctor.CheckSyncBranchHookQuick(path); issue != "" {
		issues = append(issues, issue)
	}

	// If any issues found, print hint
	if len(issues) > 0 {
		printCheckHealthHint(issues)
	}
	// Silent exit on success
}

// printCheckHealthHint prints the health check hint and exits with error.
func printCheckHealthHint(issues []string) {
	fmt.Fprintf(os.Stderr, "💡 bd doctor recommends a health check:\n")
	for _, issue := range issues {
		fmt.Fprintf(os.Stderr, "   • %s\n", issue)
	}
	fmt.Fprintf(os.Stderr, "   Run 'bd doctor' for details, or 'bd doctor --fix' to auto-repair\n")
	fmt.Fprintf(os.Stderr, "   (Suppress with: bd config set %s false)\n", ConfigKeyHintsDoctor)
	os.Exit(1)
}

// getCheckHealthDBPath returns the database path for check-health operations.
// This centralizes the path resolution logic (bd-b8h).
func getCheckHealthDBPath(beadsDir string) string {
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		return cfg.DatabasePath(beadsDir)
	}
	return filepath.Join(beadsDir, beads.CanonicalDatabaseName)
}

// hintsDisabledDB checks if hints.doctor is set to "false" using an existing DB connection.
// Used by runCheckHealth to avoid multiple DB opens (bd-xyc).
func hintsDisabledDB(db *sql.DB) bool {
	var value string
	err := db.QueryRow("SELECT value FROM config WHERE key = ?", ConfigKeyHintsDoctor).Scan(&value)
	if err != nil {
		return false // Key not set, assume hints enabled
	}
	return strings.ToLower(value) == "false"
}

// checkVersionMismatchDB checks if CLI version differs from database bd_version.
// Uses an existing DB connection (bd-xyc).
func checkVersionMismatchDB(db *sql.DB) string {
	var dbVersion string
	err := db.QueryRow("SELECT value FROM metadata WHERE key = 'bd_version'").Scan(&dbVersion)
	if err != nil {
		return "" // Can't read version, skip
	}

	if dbVersion != "" && dbVersion != Version {
		return fmt.Sprintf("Version mismatch (CLI: %s, database: %s)", Version, dbVersion)
	}

	return ""
}

func runDiagnostics(path string) doctorResult {
	result := doctorResult{
		Path:       path,
		CLIVersion: Version,
		OverallOK:  true,
	}

	// Check 1: Installation (.beads/ directory)
	installCheck := convertDoctorCheck(doctor.CheckInstallation(path))
	result.Checks = append(result.Checks, installCheck)
	if installCheck.Status != statusOK {
		result.OverallOK = false
	}

	// Check Git Hooks early (even if .beads/ doesn't exist yet)
	hooksCheck := convertDoctorCheck(doctor.CheckGitHooks())
	result.Checks = append(result.Checks, hooksCheck)
	// Don't fail overall check for missing hooks, just warn

	// Check sync-branch hook compatibility (issue #532)
	syncBranchHookCheck := convertDoctorCheck(doctor.CheckSyncBranchHookCompatibility(path))
	result.Checks = append(result.Checks, syncBranchHookCheck)
	if syncBranchHookCheck.Status == statusError {
		result.OverallOK = false
	}

	// If no .beads/, skip remaining checks
	if installCheck.Status != statusOK {
		return result
	}

	// Check 1a: Fresh clone detection (bd-4ew)
	// Must come early - if this is a fresh clone, other checks may be misleading
	freshCloneCheck := convertDoctorCheck(doctor.CheckFreshClone(path))
	result.Checks = append(result.Checks, freshCloneCheck)
	if freshCloneCheck.Status == statusWarning || freshCloneCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 2: Database version
	dbCheck := convertDoctorCheck(doctor.CheckDatabaseVersion(path, Version))
	result.Checks = append(result.Checks, dbCheck)
	if dbCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 2a: Schema compatibility (bd-ckvw)
	schemaCheck := convertDoctorCheck(doctor.CheckSchemaCompatibility(path))
	result.Checks = append(result.Checks, schemaCheck)
	if schemaCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 2b: Database integrity (bd-2au)
	integrityCheck := convertDoctorCheck(doctor.CheckDatabaseIntegrity(path))
	result.Checks = append(result.Checks, integrityCheck)
	if integrityCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 3: ID format (hash vs sequential)
	idCheck := convertDoctorCheck(doctor.CheckIDFormat(path))
	result.Checks = append(result.Checks, idCheck)
	if idCheck.Status == statusWarning {
		result.OverallOK = false
	}

	// Check 4: CLI version (GitHub)
	versionCheck := convertDoctorCheck(doctor.CheckCLIVersion(Version))
	result.Checks = append(result.Checks, versionCheck)
	// Don't fail overall check for outdated CLI, just warn

	// Check 4.5: Claude plugin version (if running in Claude Code)
	pluginCheck := convertDoctorCheck(doctor.CheckClaudePlugin())
	result.Checks = append(result.Checks, pluginCheck)
	// Don't fail overall check for outdated plugin, just warn

	// Check 5: Multiple database files
	multiDBCheck := convertDoctorCheck(doctor.CheckMultipleDatabases(path))
	result.Checks = append(result.Checks, multiDBCheck)
	if multiDBCheck.Status == statusWarning || multiDBCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 6: Multiple JSONL files (excluding merge artifacts)
	jsonlCheck := convertDoctorCheck(doctor.CheckLegacyJSONLFilename(path))
	result.Checks = append(result.Checks, jsonlCheck)
	if jsonlCheck.Status == statusWarning || jsonlCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 6a: Legacy JSONL config (bd-6xd: migrate beads.jsonl to issues.jsonl)
	legacyConfigCheck := convertDoctorCheck(doctor.CheckLegacyJSONLConfig(path))
	result.Checks = append(result.Checks, legacyConfigCheck)
	// Don't fail overall check for legacy config, just warn

	// Check 7: Database/JSONL configuration mismatch
	configCheck := convertDoctorCheck(doctor.CheckDatabaseConfig(path))
	result.Checks = append(result.Checks, configCheck)
	if configCheck.Status == statusWarning || configCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 7a: Configuration value validation (bd-alz)
	configValuesCheck := convertDoctorCheck(doctor.CheckConfigValues(path))
	result.Checks = append(result.Checks, configValuesCheck)
	// Don't fail overall check for config value warnings, just warn

	// Check 8: Daemon health
	daemonCheck := convertDoctorCheck(doctor.CheckDaemonStatus(path, Version))
	result.Checks = append(result.Checks, daemonCheck)
	if daemonCheck.Status == statusWarning || daemonCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 9: Database-JSONL sync
	syncCheck := convertDoctorCheck(doctor.CheckDatabaseJSONLSync(path))
	result.Checks = append(result.Checks, syncCheck)
	if syncCheck.Status == statusWarning || syncCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 9: Permissions
	permCheck := convertDoctorCheck(doctor.CheckPermissions(path))
	result.Checks = append(result.Checks, permCheck)
	if permCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 10: Dependency cycles
	cycleCheck := convertDoctorCheck(doctor.CheckDependencyCycles(path))
	result.Checks = append(result.Checks, cycleCheck)
	if cycleCheck.Status == statusError || cycleCheck.Status == statusWarning {
		result.OverallOK = false
	}

	// Check 11: Claude integration
	claudeCheck := convertDoctorCheck(doctor.CheckClaude())
	result.Checks = append(result.Checks, claudeCheck)
	// Don't fail overall check for missing Claude integration, just warn

	// Check 11a: bd in PATH (needed for Claude hooks to work)
	bdPathCheck := convertDoctorCheck(doctor.CheckBdInPath())
	result.Checks = append(result.Checks, bdPathCheck)
	// Don't fail overall check for missing bd in PATH, just warn

	// Check 11b: Documentation bd prime references match installed version
	bdPrimeDocsCheck := convertDoctorCheck(doctor.CheckDocumentationBdPrimeReference(path))
	result.Checks = append(result.Checks, bdPrimeDocsCheck)
	// Don't fail overall check for doc mismatch, just warn

	// Check 12: Agent documentation presence
	agentDocsCheck := convertDoctorCheck(doctor.CheckAgentDocumentation(path))
	result.Checks = append(result.Checks, agentDocsCheck)
	// Don't fail overall check for missing docs, just warn

	// Check 13: Legacy beads slash commands in documentation
	legacyDocsCheck := convertDoctorCheck(doctor.CheckLegacyBeadsSlashCommands(path))
	result.Checks = append(result.Checks, legacyDocsCheck)
	// Don't fail overall check for legacy docs, just warn

	// Check 14: Gitignore up to date
	gitignoreCheck := convertDoctorCheck(doctor.CheckGitignore())
	result.Checks = append(result.Checks, gitignoreCheck)
	// Don't fail overall check for gitignore, just warn

	// Check 15: Git merge driver configuration
	mergeDriverCheck := convertDoctorCheck(doctor.CheckMergeDriver(path))
	result.Checks = append(result.Checks, mergeDriverCheck)
	// Don't fail overall check for merge driver, just warn

	// Check 16: Metadata.json version tracking (bd-u4sb)
	metadataCheck := convertDoctorCheck(doctor.CheckMetadataVersionTracking(path, Version))
	result.Checks = append(result.Checks, metadataCheck)
	// Don't fail overall check for metadata, just warn

	// Check 17: Sync branch configuration (bd-rsua)
	syncBranchCheck := convertDoctorCheck(doctor.CheckSyncBranchConfig(path))
	result.Checks = append(result.Checks, syncBranchCheck)
	// Don't fail overall check for missing sync.branch, just warn

	// Check 17a: Sync branch health (bd-6rf)
	syncBranchHealthCheck := convertDoctorCheck(doctor.CheckSyncBranchHealth(path))
	result.Checks = append(result.Checks, syncBranchHealthCheck)
	// Don't fail overall check for sync branch health, just warn

	// Check 18: Deletions manifest (legacy, now replaced by tombstones)
	deletionsCheck := convertDoctorCheck(doctor.CheckDeletionsManifest(path))
	result.Checks = append(result.Checks, deletionsCheck)
	// Don't fail overall check for missing deletions manifest, just warn

	// Check 19: Tombstones health (bd-s3v)
	tombstonesCheck := convertDoctorCheck(doctor.CheckTombstones(path))
	result.Checks = append(result.Checks, tombstonesCheck)
	// Don't fail overall check for tombstone issues, just warn

	// Check 20: Untracked .beads/*.jsonl files (bd-pbj)
	untrackedCheck := convertDoctorCheck(doctor.CheckUntrackedBeadsFiles(path))
	result.Checks = append(result.Checks, untrackedCheck)
	// Don't fail overall check for untracked files, just warn

	return result
}

// convertDoctorCheck converts doctor package check to main package check
func convertDoctorCheck(dc doctor.DoctorCheck) doctorCheck {
	return doctorCheck{
		Name:    dc.Name,
		Status:  dc.Status,
		Message: dc.Message,
		Detail:  dc.Detail,
		Fix:     dc.Fix,
	}
}

// exportDiagnostics writes the doctor result to a JSON file (bd-9cc)
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
	// Print header
	fmt.Println("\nDiagnostics")

	// Print each check with tree formatting
	for i, check := range result.Checks {
		// Determine prefix
		prefix := "├"
		if i == len(result.Checks)-1 {
			prefix = "└"
		}

		// Format status indicator
		var statusIcon string
		switch check.Status {
		case statusOK:
			statusIcon = ""
		case statusWarning:
			statusIcon = color.YellowString(" ⚠")
		case statusError:
			statusIcon = color.RedString(" ✗")
		}

		// Print main check line
		fmt.Printf(" %s %s: %s%s\n", prefix, check.Name, check.Message, statusIcon)

		// Print detail if present (indented under the check)
		if check.Detail != "" {
			detailPrefix := "│"
			if i == len(result.Checks)-1 {
				detailPrefix = " "
			}
			fmt.Printf(" %s   %s\n", detailPrefix, color.New(color.Faint).Sprint(check.Detail))
		}
	}

	fmt.Println()

	// Print warnings/errors with fixes
	hasIssues := false
	for _, check := range result.Checks {
		if check.Status != statusOK && check.Fix != "" {
			if !hasIssues {
				hasIssues = true
			}

			switch check.Status {
			case statusWarning:
				color.Yellow("⚠ Warning: %s\n", check.Message)
			case statusError:
				color.Red("✗ Error: %s\n", check.Message)
			}

			fmt.Printf("  Fix: %s\n\n", check.Fix)
		}
	}

	if !hasIssues {
		color.Green("✓ All checks passed\n")
	}
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&perfMode, "perf", false, "Run performance diagnostics and generate CPU profile")
	doctorCmd.Flags().BoolVar(&checkHealthMode, "check-health", false, "Quick health check for git hooks (silent on success)")
	doctorCmd.Flags().StringVarP(&doctorOutput, "output", "o", "", "Export diagnostics to JSON file (bd-9cc)")
}
