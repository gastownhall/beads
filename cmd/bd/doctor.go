package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/syncbranch"
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
			fmt.Printf("     Status: %s\n", ui.RenderFail("ERROR"))
		} else {
			fmt.Printf("     Status: %s\n", ui.RenderWarn("WARNING"))
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
			fmt.Printf("  Status: %s\n", ui.RenderFail("ERROR"))
		} else {
			fmt.Printf("  Status: %s\n", ui.RenderWarn("WARNING"))
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
		case "Repo Fingerprint":
			err = fix.RepoFingerprint(path)
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
		case "Merge Artifacts":
			err = fix.MergeArtifacts(path)
		case "Orphaned Dependencies":
			err = fix.OrphanedDependencies(path)
		case "Duplicate Issues":
			// No auto-fix: duplicates require user review
			fmt.Printf("  ⚠ Run 'bd duplicates' to review and merge duplicates\n")
			continue
		case "Test Pollution":
			// No auto-fix: test cleanup requires user review
			fmt.Printf("  ⚠ Run 'bd detect-pollution' to review and clean test issues\n")
			continue
		case "Git Conflicts":
			// No auto-fix: git conflicts require manual resolution
			fmt.Printf("  ⚠ Resolve conflicts manually: git checkout --ours or --theirs .beads/issues.jsonl\n")
			continue
		case "Stale Closed Issues":
			// bd-bqcc: consolidate cleanup into doctor --fix
			err = fix.StaleClosedIssues(path)
		case "Expired Tombstones":
			// bd-bqcc: consolidate cleanup into doctor --fix
			err = fix.ExpiredTombstones(path)
		case "Compaction Candidates":
			// No auto-fix: compaction requires agent review
			fmt.Printf("  ⚠ Run 'bd compact --analyze' to review candidates\n")
			continue
		default:
			fmt.Printf("  ⚠ No automatic fix available for %s\n", check.Name)
			fmt.Printf("  Manual fix: %s\n", check.Fix)
			continue
		}

		if err != nil {
			errorCount++
			fmt.Printf("  %s Error: %v\n", ui.RenderFail("✗"), err)
			fmt.Printf("  Manual fix: %s\n", check.Fix)
		} else {
			fixedCount++
			fmt.Printf("  %s Fixed\n", ui.RenderPass("✓"))
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

	// Check 1a: Fresh clone detection (bd-4ew)
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

	// Check 2a: Schema compatibility (bd-ckvw)
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

	// Check 2c: Database integrity (bd-2au)
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

	// Check 6a: Legacy JSONL config (bd-6xd: migrate beads.jsonl to issues.jsonl)
	legacyConfigCheck := convertWithCategory(doctor.CheckLegacyJSONLConfig(path), doctor.CategoryData)
	result.Checks = append(result.Checks, legacyConfigCheck)
	// Don't fail overall check for legacy config, just warn

	// Check 7: Database/JSONL configuration mismatch
	configCheck := convertWithCategory(doctor.CheckDatabaseConfig(path), doctor.CategoryData)
	result.Checks = append(result.Checks, configCheck)
	if configCheck.Status == statusWarning || configCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 7a: Configuration value validation (bd-alz)
	configValuesCheck := convertWithCategory(doctor.CheckConfigValues(path), doctor.CategoryData)
	result.Checks = append(result.Checks, configValuesCheck)
	// Don't fail overall check for config value warnings, just warn

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

	// Check 15: Git merge driver configuration
	mergeDriverCheck := convertWithCategory(doctor.CheckMergeDriver(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, mergeDriverCheck)
	// Don't fail overall check for merge driver, just warn

	// Check 16: Metadata.json version tracking (bd-u4sb)
	metadataCheck := convertWithCategory(doctor.CheckMetadataVersionTracking(path, Version), doctor.CategoryMetadata)
	result.Checks = append(result.Checks, metadataCheck)
	// Don't fail overall check for metadata, just warn

	// Check 17: Sync branch configuration (bd-rsua)
	syncBranchCheck := convertWithCategory(doctor.CheckSyncBranchConfig(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, syncBranchCheck)
	// Don't fail overall check for missing sync.branch, just warn

	// Check 17a: Sync branch health (bd-6rf)
	syncBranchHealthCheck := convertWithCategory(doctor.CheckSyncBranchHealth(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, syncBranchHealthCheck)
	// Don't fail overall check for sync branch health, just warn

	// Check 17b: Orphaned issues - referenced in commits but still open (bd-5hrq)
	orphanedIssuesCheck := convertWithCategory(doctor.CheckOrphanedIssues(path), doctor.CategoryGit)
	result.Checks = append(result.Checks, orphanedIssuesCheck)
	// Don't fail overall check for orphaned issues, just warn

	// Check 18: Deletions manifest (legacy, now replaced by tombstones)
	deletionsCheck := convertWithCategory(doctor.CheckDeletionsManifest(path), doctor.CategoryMetadata)
	result.Checks = append(result.Checks, deletionsCheck)
	// Don't fail overall check for missing deletions manifest, just warn

	// Check 19: Tombstones health (bd-s3v)
	tombstonesCheck := convertWithCategory(doctor.CheckTombstones(path), doctor.CategoryMetadata)
	result.Checks = append(result.Checks, tombstonesCheck)
	// Don't fail overall check for tombstone issues, just warn

	// Check 20: Untracked .beads/*.jsonl files (bd-pbj)
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

	// Check 26: Stale closed issues (maintenance, bd-bqcc)
	staleClosedCheck := convertDoctorCheck(doctor.CheckStaleClosedIssues(path))
	result.Checks = append(result.Checks, staleClosedCheck)
	// Don't fail overall check for stale issues, just warn

	// Check 27: Expired tombstones (maintenance, bd-bqcc)
	tombstonesExpiredCheck := convertDoctorCheck(doctor.CheckExpiredTombstones(path))
	result.Checks = append(result.Checks, tombstonesExpiredCheck)
	// Don't fail overall check for expired tombstones, just warn

	// Check 28: Compaction candidates (maintenance, bd-bqcc)
	compactionCheck := convertDoctorCheck(doctor.CheckCompactionCandidates(path))
	result.Checks = append(result.Checks, compactionCheck)
	// Info only, not a warning - compaction requires human review

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

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&perfMode, "perf", false, "Run performance diagnostics and generate CPU profile")
	doctorCmd.Flags().BoolVar(&checkHealthMode, "check-health", false, "Quick health check for git hooks (silent on success)")
	doctorCmd.Flags().StringVarP(&doctorOutput, "output", "o", "", "Export diagnostics to JSON file (bd-9cc)")
}
