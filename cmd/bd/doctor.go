package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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
	"github.com/steveyegge/beads/internal/daemon"
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
	Path       string        `json:"path"`
	Checks     []doctorCheck `json:"checks"`
	OverallOK  bool          `json:"overall_ok"`
	CLIVersion string        `json:"cli_version"`
}

var (
	doctorFix       bool
	perfMode        bool
	checkHealthMode bool
)

// ConfigKeyHintsDoctor is the config key for suppressing doctor hints
const ConfigKeyHintsDoctor = "hints.doctor"

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

Examples:
  bd doctor              # Check current directory
  bd doctor /path/to/repo # Check specific repository
  bd doctor --json       # Machine-readable output
  bd doctor --fix        # Automatically fix issues
  bd doctor --perf       # Performance diagnostics`,
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

		// Apply fixes if requested
		if doctorFix {
			applyFixes(result)
			// Re-run diagnostics to show results
			result = runDiagnostics(absPath)
		}

		// Output results
		if jsonOutput {
			outputJSON(result)
		} else {
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

	// Ask for confirmation
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

	// Apply fixes
	fmt.Println("\nApplying fixes...")
	fixedCount := 0
	errorCount := 0

	for _, check := range fixableIssues {
		fmt.Printf("\nFixing %s...\n", check.Name)

		var err error
		switch check.Name {
		case "Gitignore":
			err = doctor.FixGitignore()
		case "Git Hooks":
			err = fix.GitHooks(result.Path)
		case "Daemon Health":
			err = fix.Daemon(result.Path)
		case "DB-JSONL Sync":
			err = fix.DBJSONLSync(result.Path)
		case "Permissions":
			err = fix.Permissions(result.Path)
		case "Database":
			err = fix.DatabaseVersion(result.Path)
		case "Schema Compatibility":
			err = fix.SchemaCompatibility(result.Path)
		case "Git Merge Driver":
			err = fix.MergeDriver(result.Path)
		case "Sync Branch Config":
			err = fix.SyncBranchConfig(result.Path)
		case "Database Config":
			err = fix.DatabaseConfig(result.Path)
		case "JSONL Config":
			err = fix.LegacyJSONLConfig(result.Path)
		case "Deletions Manifest":
			err = fix.HydrateDeletionsManifest(result.Path)
		case "Untracked Files":
			err = fix.UntrackedJSONL(result.Path)
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
		if issue := checkHooksQuick(path); issue != "" {
			printCheckHealthHint([]string{issue})
		}
		return
	}

	// Open database once for all checks (bd-xyc: single DB connection)
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err != nil {
		// Can't open DB - only check hooks
		if issue := checkHooksQuick(path); issue != "" {
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

	// Check 2: Sync branch not configured
	if issue := checkSyncBranchQuickDB(db); issue != "" {
		issues = append(issues, issue)
	}

	// Check 3: Outdated git hooks
	if issue := checkHooksQuick(path); issue != "" {
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

// checkSyncBranchQuickDB checks if sync.branch is configured.
// Uses an existing DB connection (bd-xyc).
func checkSyncBranchQuickDB(db *sql.DB) string {
	var value string
	err := db.QueryRow("SELECT value FROM config WHERE key = 'sync.branch'").Scan(&value)
	if err != nil || value == "" {
		return "sync.branch not configured"
	}

	return ""
}

// checkHooksQuick does a fast check for outdated git hooks.
// Checks all beads hooks: pre-commit, post-merge, pre-push, post-checkout (bd-2em).
func checkHooksQuick(path string) string {
	hooksDir := filepath.Join(path, ".git", "hooks")

	// Check if .git/hooks exists
	if _, err := os.Stat(hooksDir); os.IsNotExist(err) {
		return "" // No git hooks directory, skip
	}

	// Check all beads-managed hooks (bd-2em: expanded from just post-merge)
	hookNames := []string{"pre-commit", "post-merge", "pre-push", "post-checkout"}

	var outdatedHooks []string
	var oldestVersion string

	for _, hookName := range hookNames {
		hookPath := filepath.Join(hooksDir, hookName)
		content, err := os.ReadFile(hookPath) // #nosec G304 - path is controlled
		if err != nil {
			continue // Hook doesn't exist, skip (will be caught by full doctor)
		}

		// Look for version marker
		hookContent := string(content)
		if !strings.Contains(hookContent, "bd-hooks-version:") {
			continue // Not a bd hook or old format, skip
		}

		// Extract version
		for _, line := range strings.Split(hookContent, "\n") {
			if strings.Contains(line, "bd-hooks-version:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					hookVersion := strings.TrimSpace(parts[1])
					if hookVersion != Version {
						outdatedHooks = append(outdatedHooks, hookName)
						// Track the oldest version for display
						if oldestVersion == "" || compareVersions(hookVersion, oldestVersion) < 0 {
							oldestVersion = hookVersion
						}
					}
				}
				break
			}
		}
	}

	if len(outdatedHooks) == 0 {
		return ""
	}

	// Return summary of outdated hooks
	if len(outdatedHooks) == 1 {
		return fmt.Sprintf("Git hook %s outdated (%s → %s)", outdatedHooks[0], oldestVersion, Version)
	}
	return fmt.Sprintf("Git hooks outdated: %s (%s → %s)", strings.Join(outdatedHooks, ", "), oldestVersion, Version)
}

func runDiagnostics(path string) doctorResult {
	result := doctorResult{
		Path:       path,
		CLIVersion: Version,
		OverallOK:  true,
	}

	// Check 1: Installation (.beads/ directory)
	installCheck := checkInstallation(path)
	result.Checks = append(result.Checks, installCheck)
	if installCheck.Status != statusOK {
		result.OverallOK = false
	}

	// Check Git Hooks early (even if .beads/ doesn't exist yet)
	hooksCheck := checkGitHooks(path)
	result.Checks = append(result.Checks, hooksCheck)
	// Don't fail overall check for missing hooks, just warn

	// If no .beads/, skip remaining checks
	if installCheck.Status != statusOK {
		return result
	}

	// Check 2: Database version
	dbCheck := checkDatabaseVersion(path)
	result.Checks = append(result.Checks, dbCheck)
	if dbCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 2a: Schema compatibility (bd-ckvw)
	schemaCheck := checkSchemaCompatibility(path)
	result.Checks = append(result.Checks, schemaCheck)
	if schemaCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 3: ID format (hash vs sequential)
	idCheck := checkIDFormat(path)
	result.Checks = append(result.Checks, idCheck)
	if idCheck.Status == statusWarning {
		result.OverallOK = false
	}

	// Check 4: CLI version (GitHub)
	versionCheck := checkCLIVersion()
	result.Checks = append(result.Checks, versionCheck)
	// Don't fail overall check for outdated CLI, just warn

	// Check 5: Multiple database files
	multiDBCheck := checkMultipleDatabases(path)
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

	// Check 8: Daemon health
	daemonCheck := checkDaemonStatus(path)
	result.Checks = append(result.Checks, daemonCheck)
	if daemonCheck.Status == statusWarning || daemonCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 9: Database-JSONL sync
	syncCheck := checkDatabaseJSONLSync(path)
	result.Checks = append(result.Checks, syncCheck)
	if syncCheck.Status == statusWarning || syncCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 9: Permissions
	permCheck := checkPermissions(path)
	result.Checks = append(result.Checks, permCheck)
	if permCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 10: Dependency cycles
	cycleCheck := checkDependencyCycles(path)
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
	mergeDriverCheck := checkMergeDriver(path)
	result.Checks = append(result.Checks, mergeDriverCheck)
	// Don't fail overall check for merge driver, just warn

	// Check 16: Metadata.json version tracking (bd-u4sb)
	metadataCheck := checkMetadataVersionTracking(path)
	result.Checks = append(result.Checks, metadataCheck)
	// Don't fail overall check for metadata, just warn

	// Check 17: Sync branch configuration (bd-rsua)
	syncBranchCheck := checkSyncBranchConfig(path)
	result.Checks = append(result.Checks, syncBranchCheck)
	// Don't fail overall check for missing sync.branch, just warn

	// Check 18: Deletions manifest (prevents zombie resurrection)
	deletionsCheck := checkDeletionsManifest(path)
	result.Checks = append(result.Checks, deletionsCheck)
	// Don't fail overall check for missing deletions manifest, just warn

	// Check 19: Untracked .beads/*.jsonl files (bd-pbj)
	untrackedCheck := checkUntrackedBeadsFiles(path)
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

func checkInstallation(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		// Auto-detect prefix from directory name
		prefix := filepath.Base(path)
		prefix = strings.TrimRight(prefix, "-")

		return doctorCheck{
			Name:    "Installation",
			Status:  statusError,
			Message: "No .beads/ directory found",
			Fix:     fmt.Sprintf("Run 'bd init --prefix %s' to initialize beads", prefix),
		}
	}

	return doctorCheck{
		Name:    "Installation",
		Status:  statusOK,
		Message: ".beads/ directory found",
	}
}

func checkDatabaseVersion(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Check metadata.json first for custom database name
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		// Fall back to canonical database name
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// Check if database file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Check if JSONL exists (--no-db mode)
		// Check canonical (issues.jsonl) first, then legacy (beads.jsonl)
		issuesJSONL := filepath.Join(beadsDir, "issues.jsonl")
		beadsJSONL := filepath.Join(beadsDir, "beads.jsonl")

		if _, err := os.Stat(issuesJSONL); err == nil {
			return doctorCheck{
				Name:    "Database",
				Status:  statusOK,
				Message: "JSONL-only mode",
				Detail:  "Using issues.jsonl (no SQLite database)",
			}
		}

		if _, err := os.Stat(beadsJSONL); err == nil {
			return doctorCheck{
				Name:    "Database",
				Status:  statusOK,
				Message: "JSONL-only mode",
				Detail:  "Using issues.jsonl (no SQLite database)",
			}
		}

		return doctorCheck{
			Name:    "Database",
			Status:  statusError,
			Message: "No beads.db found",
			Fix:     "Run 'bd init' to create database",
		}
	}

	// Get database version
	dbVersion := getDatabaseVersionFromPath(dbPath)

	if dbVersion == "unknown" {
		return doctorCheck{
			Name:    "Database",
			Status:  statusError,
			Message: "Unable to read database version",
			Detail:  "Storage: SQLite",
			Fix:     "Database may be corrupted. Try 'bd migrate'",
		}
	}

	if dbVersion == "pre-0.17.5" {
		return doctorCheck{
			Name:    "Database",
			Status:  statusWarning,
			Message: fmt.Sprintf("version %s (very old)", dbVersion),
			Detail:  "Storage: SQLite",
			Fix:     "Run 'bd migrate' to upgrade database schema",
		}
	}

	if dbVersion != Version {
		return doctorCheck{
			Name:    "Database",
			Status:  statusWarning,
			Message: fmt.Sprintf("version %s (CLI: %s)", dbVersion, Version),
			Detail:  "Storage: SQLite",
			Fix:     "Run 'bd migrate' to sync database with CLI version",
		}
	}

	return doctorCheck{
		Name:    "Database",
		Status:  statusOK,
		Message: fmt.Sprintf("version %s", dbVersion),
		Detail:  "Storage: SQLite",
	}
}

func checkIDFormat(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Check metadata.json first for custom database name
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		// Fall back to canonical database name
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// Check if using JSONL-only mode
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// Check if JSONL exists (--no-db mode)
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if _, err := os.Stat(jsonlPath); err == nil {
			return doctorCheck{
				Name:    "Issue IDs",
				Status:  statusOK,
				Message: "N/A (JSONL-only mode)",
			}
		}
		// No database and no JSONL
		return doctorCheck{
			Name:    "Issue IDs",
			Status:  statusOK,
			Message: "No issues yet (will use hash-based IDs)",
		}
	}

	// Open database
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return doctorCheck{
			Name:    "Issue IDs",
			Status:  statusError,
			Message: "Unable to open database",
		}
	}
	defer func() { _ = db.Close() }() // Intentionally ignore close error

	// Get sample of issues to check ID format (up to 10 for pattern analysis)
	rows, err := db.Query("SELECT id FROM issues ORDER BY created_at LIMIT 10")
	if err != nil {
		return doctorCheck{
			Name:    "Issue IDs",
			Status:  statusError,
			Message: "Unable to query issues",
		}
	}
	defer rows.Close()

	var issueIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			issueIDs = append(issueIDs, id)
		}
	}

	if len(issueIDs) == 0 {
		return doctorCheck{
			Name:    "Issue IDs",
			Status:  statusOK,
			Message: "No issues yet (will use hash-based IDs)",
		}
	}

	// Detect ID format using robust heuristic
	if detectHashBasedIDs(db, issueIDs) {
		return doctorCheck{
			Name:    "Issue IDs",
			Status:  statusOK,
			Message: "hash-based ✓",
		}
	}

	// Sequential IDs - recommend migration
	return doctorCheck{
		Name:    "Issue IDs",
		Status:  statusWarning,
		Message: "sequential (e.g., bd-1, bd-2, ...)",
		Fix:     "Run 'bd migrate --to-hash-ids' to upgrade (prevents ID collisions in multi-worker scenarios)",
	}
}

func checkCLIVersion() doctorCheck {
	latestVersion, err := fetchLatestGitHubRelease()
	if err != nil {
		// Network error or API issue - don't fail, just warn
		return doctorCheck{
			Name:    "CLI Version",
			Status:  statusOK,
			Message: fmt.Sprintf("%s (unable to check for updates)", Version),
		}
	}

	if latestVersion == "" || latestVersion == Version {
		return doctorCheck{
			Name:    "CLI Version",
			Status:  statusOK,
			Message: fmt.Sprintf("%s (latest)", Version),
		}
	}

	// Compare versions using simple semver-aware comparison
	if compareVersions(latestVersion, Version) > 0 {
		upgradeCmds := `  • Homebrew: brew upgrade bd
  • Script: curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash`

		return doctorCheck{
			Name:    "CLI Version",
			Status:  statusWarning,
			Message: fmt.Sprintf("%s (latest: %s)", Version, latestVersion),
			Fix:     fmt.Sprintf("Upgrade to latest version:\n%s", upgradeCmds),
		}
	}

	return doctorCheck{
		Name:    "CLI Version",
		Status:  statusOK,
		Message: fmt.Sprintf("%s (latest)", Version),
	}
}

func getDatabaseVersionFromPath(dbPath string) string {
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return "unknown"
	}
	defer db.Close()

	// Try to read version from metadata table
	var version string
	err = db.QueryRow("SELECT value FROM metadata WHERE key = 'bd_version'").Scan(&version)
	if err == nil {
		return version
	}

	// Check if metadata table exists
	var tableName string
	err = db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='metadata'
	`).Scan(&tableName)

	if err == sql.ErrNoRows {
		return "pre-0.17.5"
	}

	return "unknown"
}

// detectHashBasedIDs uses multiple heuristics to determine if the database uses hash-based IDs.
// This is more robust than checking a single ID's format, since base36 hash IDs can be all-numeric.
func detectHashBasedIDs(db *sql.DB, sampleIDs []string) bool {
	// Heuristic 1: Check for child_counters table (added for hash ID support)
	var tableName string
	err := db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name='child_counters'
	`).Scan(&tableName)
	if err == nil {
		// child_counters table exists - this is a strong indicator of hash IDs
		return true
	}

	// Heuristic 2: Check if any sample ID clearly contains letters (a-z)
	// Hash IDs use base36 (0-9, a-z), sequential IDs are purely numeric
	for _, id := range sampleIDs {
		if isHashID(id) {
			return true
		}
	}

	// Heuristic 3: Look for patterns that indicate hash IDs
	if len(sampleIDs) >= 2 {
		// Extract suffixes (part after prefix-) for analysis
		var suffixes []string
		for _, id := range sampleIDs {
			parts := strings.SplitN(id, "-", 2)
			if len(parts) == 2 {
				// Strip hierarchical suffix like .1 or .1.2
				baseSuffix := strings.Split(parts[1], ".")[0]
				suffixes = append(suffixes, baseSuffix)
			}
		}

		if len(suffixes) >= 2 {
			// Check for variable lengths (strong indicator of adaptive hash IDs)
			// BUT: sequential IDs can also have variable length (1, 10, 100)
			// So we need to check if the length variation is natural (1→2→3 digits)
			// or random (3→8→4 chars typical of adaptive hash IDs)
			lengths := make(map[int]int) // length -> count
			for _, s := range suffixes {
				lengths[len(s)]++
			}
			
			// If we have 3+ different lengths, likely hash IDs (adaptive length)
			// Sequential IDs typically have 1-2 lengths (e.g., 1-9, 10-99, 100-999)
			if len(lengths) >= 3 {
				return true
			}

			// Check for leading zeros (rare in sequential IDs, common in hash IDs)
			// Sequential IDs: bd-1, bd-2, bd-10, bd-100
			// Hash IDs: bd-0088, bd-02a4, bd-05a1
			hasLeadingZero := false
			for _, s := range suffixes {
				if len(s) > 1 && s[0] == '0' {
					hasLeadingZero = true
					break
				}
			}
			if hasLeadingZero {
				return true
			}

			// Check for non-sequential ordering
			// Try to parse as integers - if they're not sequential, likely hash IDs
			allNumeric := true
			var nums []int
			for _, s := range suffixes {
				var num int
				if _, err := fmt.Sscanf(s, "%d", &num); err == nil {
					nums = append(nums, num)
				} else {
					allNumeric = false
					break
				}
			}

			if allNumeric && len(nums) >= 2 {
				// Check if they form a roughly sequential pattern (1,2,3 or 10,11,12)
				// Hash IDs would be more random (e.g., 88, 13452, 676)
				isSequentialPattern := true
				for i := 1; i < len(nums); i++ {
					diff := nums[i] - nums[i-1]
					// Allow for some gaps (deleted issues), but should be mostly sequential
					if diff < 0 || diff > 100 {
						isSequentialPattern = false
						break
					}
				}
				// If the numbers are NOT sequential, they're likely hash IDs
				if !isSequentialPattern {
					return true
				}
			}
		}
	}

	// If we can't determine for sure, default to assuming sequential IDs
	// This is conservative - better to recommend migration than miss sequential IDs
	return false
}

// Note: isHashID is defined in migrate_hash_ids.go to avoid duplication

// compareVersions compares two semantic version strings.
// Returns: -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
// Handles versions like "0.20.1", "1.2.3", etc.
func compareVersions(v1, v2 string) int {
	// Split versions into parts
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	// Compare each part
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var p1, p2 int

		// Get part value or default to 0 if part doesn't exist
		if i < len(parts1) {
			_, _ = fmt.Sscanf(parts1[i], "%d", &p1)
		}
		if i < len(parts2) {
			_, _ = fmt.Sscanf(parts2[i], "%d", &p2)
		}

		if p1 < p2 {
			return -1
		}
		if p1 > p2 {
			return 1
		}
	}

	return 0
}

func fetchLatestGitHubRelease() (string, error) {
	url := "https://api.github.com/repos/steveyegge/beads/releases/latest"

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	// Set User-Agent as required by GitHub API
	req.Header.Set("User-Agent", "beads-cli-doctor")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var release struct {
		TagName string `json:"tag_name"`
	}

	if err := json.Unmarshal(body, &release); err != nil {
		return "", err
	}

	// Strip 'v' prefix if present
	version := strings.TrimPrefix(release.TagName, "v")

	return version, nil
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

func checkMultipleDatabases(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Find all .db files (excluding backups and vc.db)
	files, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
	if err != nil {
		return doctorCheck{
			Name:    "Database Files",
			Status:  statusError,
			Message: "Unable to check for multiple databases",
		}
	}

	// Filter out backups and vc.db
	var dbFiles []string
	for _, f := range files {
		base := filepath.Base(f)
		if !strings.HasSuffix(base, ".backup.db") && base != "vc.db" {
			dbFiles = append(dbFiles, base)
		}
	}

	if len(dbFiles) == 0 {
		return doctorCheck{
			Name:    "Database Files",
			Status:  statusOK,
			Message: "No database files (JSONL-only mode)",
		}
	}

	if len(dbFiles) == 1 {
		return doctorCheck{
			Name:    "Database Files",
			Status:  statusOK,
			Message: "Single database file",
		}
	}

	// Multiple databases found
	return doctorCheck{
		Name:    "Database Files",
		Status:  statusWarning,
		Message: fmt.Sprintf("Multiple database files found: %s", strings.Join(dbFiles, ", ")),
		Fix:     "Run 'bd migrate' to consolidate databases or manually remove old .db files",
	}
}

func checkDaemonStatus(path string) doctorCheck {
	// Normalize path for reliable comparison (handles symlinks)
	wsNorm, err := filepath.EvalSymlinks(path)
	if err != nil {
		// Fallback to absolute path if EvalSymlinks fails
		wsNorm, _ = filepath.Abs(path)
	}

	// Use global daemon discovery (registry-based)
	daemons, err := daemon.DiscoverDaemons(nil)
	if err != nil {
		return doctorCheck{
			Name:    "Daemon Health",
			Status:  statusWarning,
			Message: "Unable to check daemon health",
			Detail:  err.Error(),
		}
	}

	// Filter to this workspace using normalized paths
	var workspaceDaemons []daemon.DaemonInfo
	for _, d := range daemons {
		dPath, err := filepath.EvalSymlinks(d.WorkspacePath)
		if err != nil {
			dPath, _ = filepath.Abs(d.WorkspacePath)
		}
		if dPath == wsNorm {
			workspaceDaemons = append(workspaceDaemons, d)
		}
	}

	// Check for stale socket directly (catches cases where RPC failed so WorkspacePath is empty)
	beadsDir := filepath.Join(path, ".beads")
	socketPath := filepath.Join(beadsDir, "bd.sock")
	if _, err := os.Stat(socketPath); err == nil {
		// Socket exists - try to connect
		if len(workspaceDaemons) == 0 {
			// Socket exists but no daemon found in registry - likely stale
			return doctorCheck{
				Name:    "Daemon Health",
				Status:  statusWarning,
				Message: "Stale daemon socket detected",
				Detail:  fmt.Sprintf("Socket exists at %s but daemon is not responding", socketPath),
				Fix:     "Run 'bd daemons killall' to clean up stale sockets",
			}
		}
	}

	if len(workspaceDaemons) == 0 {
		return doctorCheck{
			Name:    "Daemon Health",
			Status:  statusOK,
			Message: "No daemon running (will auto-start on next command)",
		}
	}

	// Warn if multiple daemons for same workspace
	if len(workspaceDaemons) > 1 {
		return doctorCheck{
			Name:    "Daemon Health",
			Status:  statusWarning,
			Message: fmt.Sprintf("Multiple daemons detected for this workspace (%d)", len(workspaceDaemons)),
			Fix:     "Run 'bd daemons killall' to clean up duplicate daemons",
		}
	}

	// Check for stale or version mismatched daemons
	for _, d := range workspaceDaemons {
		if !d.Alive {
			return doctorCheck{
				Name:    "Daemon Health",
				Status:  statusWarning,
				Message: "Stale daemon detected",
				Detail:  fmt.Sprintf("PID %d is not alive", d.PID),
				Fix:     "Run 'bd daemons killall' to clean up stale daemons",
			}
		}

		if d.Version != Version {
			return doctorCheck{
				Name:    "Daemon Health",
				Status:  statusWarning,
				Message: fmt.Sprintf("Version mismatch (daemon: %s, CLI: %s)", d.Version, Version),
				Fix:     "Run 'bd daemons killall' to restart daemons with current version",
			}
		}
	}

	return doctorCheck{
		Name:    "Daemon Health",
		Status:  statusOK,
		Message: fmt.Sprintf("Daemon running (PID %d, version %s)", workspaceDaemons[0].PID, workspaceDaemons[0].Version),
	}
}

func checkDatabaseJSONLSync(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)

	// Find JSONL file
	var jsonlPath string
	for _, name := range []string{"issues.jsonl", "beads.jsonl"} {
		testPath := filepath.Join(beadsDir, name)
		if _, err := os.Stat(testPath); err == nil {
			jsonlPath = testPath
			break
		}
	}

	// If no database, skip this check
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  statusOK,
			Message: "N/A (no database)",
		}
	}

	// If no JSONL, skip this check
	if jsonlPath == "" {
		return doctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  statusOK,
			Message: "N/A (no JSONL file)",
		}
	}

	// Try to read JSONL first (doesn't depend on database)
	jsonlCount, jsonlPrefixes, jsonlErr := countJSONLIssues(jsonlPath)

	// Single database open for all queries (instead of 3 separate opens)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		// Database can't be opened. If JSONL has issues, suggest recovery.
		if jsonlErr == nil && jsonlCount > 0 {
			return doctorCheck{
				Name:    "DB-JSONL Sync",
				Status:  statusWarning,
				Message: fmt.Sprintf("Database cannot be opened but JSONL contains %d issues", jsonlCount),
				Detail:  err.Error(),
				Fix:     fmt.Sprintf("Run 'bd import -i %s --rename-on-import' to recover issues from JSONL", filepath.Base(jsonlPath)),
			}
		}
		return doctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  statusWarning,
			Message: "Unable to open database",
			Detail:  err.Error(),
		}
	}
	defer db.Close()

	// Get database count
	var dbCount int
	err = db.QueryRow("SELECT COUNT(*) FROM issues").Scan(&dbCount)
	if err != nil {
		// Database opened but can't query. If JSONL has issues, suggest recovery.
		if jsonlErr == nil && jsonlCount > 0 {
			return doctorCheck{
				Name:    "DB-JSONL Sync",
				Status:  statusWarning,
				Message: fmt.Sprintf("Database cannot be queried but JSONL contains %d issues", jsonlCount),
				Detail:  err.Error(),
				Fix:     fmt.Sprintf("Run 'bd import -i %s --rename-on-import' to recover issues from JSONL", filepath.Base(jsonlPath)),
			}
		}
		return doctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  statusWarning,
			Message: "Unable to query database",
			Detail:  err.Error(),
		}
	}

	// Get database prefix
	var dbPrefix string
	err = db.QueryRow("SELECT value FROM config WHERE key = ?", "issue_prefix").Scan(&dbPrefix)
	if err != nil && err != sql.ErrNoRows {
		return doctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  statusWarning,
			Message: "Unable to read database prefix",
			Detail:  err.Error(),
		}
	}

	// Use JSONL error if we got it earlier
	if jsonlErr != nil {
		return doctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  statusWarning,
			Message: "Unable to read JSONL file",
			Detail:  jsonlErr.Error(),
		}
	}

	// Check for issues
	var issues []string

	// Count mismatch
	if dbCount != jsonlCount {
		issues = append(issues, fmt.Sprintf("Count mismatch: database has %d issues, JSONL has %d", dbCount, jsonlCount))
	}

	// Prefix mismatch (only check most common prefix in JSONL)
	if dbPrefix != "" && len(jsonlPrefixes) > 0 {
		var mostCommonPrefix string
		maxCount := 0
		for prefix, count := range jsonlPrefixes {
			if count > maxCount {
				maxCount = count
				mostCommonPrefix = prefix
			}
		}

		// Only warn if majority of issues have wrong prefix
		if mostCommonPrefix != dbPrefix && maxCount > jsonlCount/2 {
			issues = append(issues, fmt.Sprintf("Prefix mismatch: database uses %q but most JSONL issues use %q", dbPrefix, mostCommonPrefix))
		}
	}

	// If we found issues, report them
	if len(issues) > 0 {
		return doctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  statusWarning,
			Message: strings.Join(issues, "; "),
			Fix:     "Run 'bd sync --import-only' to import JSONL updates or 'bd import -i issues.jsonl --rename-on-import' to fix prefixes",
		}
	}

	// Check modification times (only if counts match)
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		return doctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  statusWarning,
			Message: "Unable to check database file",
		}
	}

	jsonlInfo, err := os.Stat(jsonlPath)
	if err != nil {
		return doctorCheck{
			Name:    "DB-JSONL Sync",
			Status:  statusWarning,
			Message: "Unable to check JSONL file",
		}
	}

	if jsonlInfo.ModTime().After(dbInfo.ModTime()) {
		timeDiff := jsonlInfo.ModTime().Sub(dbInfo.ModTime())
		if timeDiff > 30*time.Second {
			return doctorCheck{
				Name:    "DB-JSONL Sync",
				Status:  statusWarning,
				Message: "JSONL is newer than database",
				Fix:     "Run 'bd sync --import-only' to import JSONL updates",
			}
		}
	}

	return doctorCheck{
		Name:    "DB-JSONL Sync",
		Status:  statusOK,
		Message: "Database and JSONL are in sync",
	}
}

// countJSONLIssues counts issues in the JSONL file and returns the count, prefixes, and any error.
func countJSONLIssues(jsonlPath string) (int, map[string]int, error) {
	// jsonlPath is safe: constructed from filepath.Join(beadsDir, hardcoded name)
	file, err := os.Open(jsonlPath) //nolint:gosec
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open JSONL file: %w", err)
	}
	defer file.Close()

	count := 0
	prefixes := make(map[string]int)
	errorCount := 0

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Parse JSON to get the ID
		var issue map[string]interface{}
		if err := json.Unmarshal(line, &issue); err != nil {
			errorCount++
			continue
		}

		if id, ok := issue["id"].(string); ok {
			count++
			// Extract prefix (everything before the last dash)
			lastDash := strings.LastIndex(id, "-")
			if lastDash != -1 {
				prefixes[id[:lastDash]]++
			} else {
				prefixes[id]++
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return count, prefixes, fmt.Errorf("failed to read JSONL file: %w", err)
	}

	if errorCount > 0 {
		return count, prefixes, fmt.Errorf("skipped %d malformed lines in JSONL", errorCount)
	}

	return count, prefixes, nil
}

func checkPermissions(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Check if .beads/ is writable
	testFile := filepath.Join(beadsDir, ".doctor-test-write")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		return doctorCheck{
			Name:    "Permissions",
			Status:  statusError,
			Message: ".beads/ directory is not writable",
			Fix:     fmt.Sprintf("Fix permissions: chmod u+w %s", beadsDir),
		}
	}
	_ = os.Remove(testFile) // Clean up test file (intentionally ignore error)

	// Check database permissions
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	if _, err := os.Stat(dbPath); err == nil {
		// Try to open database
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			return doctorCheck{
				Name:    "Permissions",
				Status:  statusError,
				Message: "Database file exists but cannot be opened",
				Fix:     fmt.Sprintf("Check database permissions: %s", dbPath),
			}
		}
		_ = db.Close() // Intentionally ignore close error

		// Try a write test
		db, err = sql.Open("sqlite", dbPath)
		if err == nil {
			_, err = db.Exec("SELECT 1")
			_ = db.Close() // Intentionally ignore close error
			if err != nil {
				return doctorCheck{
					Name:    "Permissions",
					Status:  statusError,
					Message: "Database file is not readable",
					Fix:     fmt.Sprintf("Fix permissions: chmod u+rw %s", dbPath),
				}
			}
		}
	}

	return doctorCheck{
		Name:    "Permissions",
		Status:  statusOK,
		Message: "All permissions OK",
	}
}

func checkDependencyCycles(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)

	// If no database, skip this check
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Dependency Cycles",
			Status:  statusOK,
			Message: "N/A (no database)",
		}
	}

	// Open database to check for cycles
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return doctorCheck{
			Name:    "Dependency Cycles",
			Status:  statusWarning,
			Message: "Unable to open database",
			Detail:  err.Error(),
		}
	}
	defer db.Close()

	// Query for cycles using simplified SQL
	query := `
		WITH RECURSIVE paths AS (
			SELECT
				issue_id,
				depends_on_id,
				issue_id as start_id,
				issue_id || '→' || depends_on_id as path,
				0 as depth
			FROM dependencies

			UNION ALL

			SELECT
				d.issue_id,
				d.depends_on_id,
				p.start_id,
				p.path || '→' || d.depends_on_id,
				p.depth + 1
			FROM dependencies d
			JOIN paths p ON d.issue_id = p.depends_on_id
			WHERE p.depth < 100
			  AND p.path NOT LIKE '%' || d.depends_on_id || '→%'
		)
		SELECT DISTINCT start_id
		FROM paths
		WHERE depends_on_id = start_id`

	rows, err := db.Query(query)
	if err != nil {
		return doctorCheck{
			Name:    "Dependency Cycles",
			Status:  statusWarning,
			Message: "Unable to check for cycles",
			Detail:  err.Error(),
		}
	}
	defer rows.Close()

	cycleCount := 0
	var firstCycle string
	for rows.Next() {
		var startID string
		if err := rows.Scan(&startID); err != nil {
			continue
		}
		cycleCount++
		if cycleCount == 1 {
			firstCycle = startID
		}
	}

	if cycleCount == 0 {
		return doctorCheck{
			Name:    "Dependency Cycles",
			Status:  statusOK,
			Message: "No circular dependencies detected",
		}
	}

	return doctorCheck{
		Name:    "Dependency Cycles",
		Status:  statusError,
		Message: fmt.Sprintf("Found %d circular dependency cycle(s)", cycleCount),
		Detail:  fmt.Sprintf("First cycle involves: %s", firstCycle),
		Fix:     "Run 'bd dep cycles' to see full cycle paths, then 'bd dep remove' to break cycles",
	}
}

func checkGitHooks(path string) doctorCheck {
	// Check if we're in a git repository
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Git Hooks",
			Status:  statusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Recommended hooks and their purposes
	recommendedHooks := map[string]string{
		"pre-commit": "Flushes pending bd changes to JSONL before commit",
		"post-merge": "Imports updated JSONL after git pull/merge",
		"pre-push":   "Exports database to JSONL before push",
	}

	hooksDir := filepath.Join(gitDir, "hooks")
	var missingHooks []string
	var installedHooks []string

	for hookName := range recommendedHooks {
		hookPath := filepath.Join(hooksDir, hookName)
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			missingHooks = append(missingHooks, hookName)
		} else {
			installedHooks = append(installedHooks, hookName)
		}
	}

	if len(missingHooks) == 0 {
		return doctorCheck{
			Name:    "Git Hooks",
			Status:  statusOK,
			Message: "All recommended hooks installed",
			Detail:  fmt.Sprintf("Installed: %s", strings.Join(installedHooks, ", ")),
		}
	}

	hookInstallMsg := "Install hooks with 'bd hooks install'. See https://github.com/steveyegge/beads/tree/main/examples/git-hooks for installation instructions"

	if len(installedHooks) > 0 {
		return doctorCheck{
			Name:    "Git Hooks",
			Status:  statusWarning,
			Message: fmt.Sprintf("Missing %d recommended hook(s)", len(missingHooks)),
			Detail:  fmt.Sprintf("Missing: %s", strings.Join(missingHooks, ", ")),
			Fix:     hookInstallMsg,
		}
	}

	return doctorCheck{
		Name:    "Git Hooks",
		Status:  statusWarning,
		Message: "No recommended git hooks installed",
		Detail:  fmt.Sprintf("Recommended: %s", strings.Join([]string{"pre-commit", "post-merge", "pre-push"}, ", ")),
		Fix:     hookInstallMsg,
	}
}

func checkSchemaCompatibility(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Check metadata.json first for custom database name
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		// Fall back to canonical database name
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// If no database, skip this check
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Schema Compatibility",
			Status:  statusOK,
			Message: "N/A (no database)",
		}
	}

	// Open database (bd-ckvw: This will run migrations and schema probe)
	// Note: We can't use the global 'store' because doctor can check arbitrary paths
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(30000)")
	if err != nil {
		return doctorCheck{
			Name:    "Schema Compatibility",
			Status:  statusError,
			Message: "Failed to open database",
			Detail:  err.Error(),
			Fix:     "Database may be corrupted. Try 'bd migrate' or restore from backup",
		}
	}
	defer db.Close()

	// Run schema probe (defined in internal/storage/sqlite/schema_probe.go)
	// This is a simplified version since we can't import the internal package directly
	// Check all critical tables and columns
	criticalChecks := map[string][]string{
		"issues":         {"id", "title", "content_hash", "external_ref", "compacted_at"},
		"dependencies":   {"issue_id", "depends_on_id", "type"},
		"child_counters": {"parent_id", "last_child"},
		"export_hashes":  {"issue_id", "content_hash"},
	}

	var missingElements []string
	for table, columns := range criticalChecks {
		// Try to query all columns
		query := fmt.Sprintf(
			"SELECT %s FROM %s LIMIT 0",
			strings.Join(columns, ", "),
			table,
		) // #nosec G201 -- table/column names sourced from hardcoded map
		_, err := db.Exec(query)

		if err != nil {
			errMsg := err.Error()
			if strings.Contains(errMsg, "no such table") {
				missingElements = append(missingElements, fmt.Sprintf("table:%s", table))
			} else if strings.Contains(errMsg, "no such column") {
				// Find which columns are missing
				for _, col := range columns {
					colQuery := fmt.Sprintf("SELECT %s FROM %s LIMIT 0", col, table) // #nosec G201 -- names come from static schema definition
					if _, colErr := db.Exec(colQuery); colErr != nil && strings.Contains(colErr.Error(), "no such column") {
						missingElements = append(missingElements, fmt.Sprintf("%s.%s", table, col))
					}
				}
			}
		}
	}

	if len(missingElements) > 0 {
		return doctorCheck{
			Name:    "Schema Compatibility",
			Status:  statusError,
			Message: "Database schema is incomplete or incompatible",
			Detail:  fmt.Sprintf("Missing: %s", strings.Join(missingElements, ", ")),
			Fix:     "Run 'bd migrate' to upgrade schema, or if daemon is running an old version, run 'bd daemons killall' to restart",
		}
	}

	return doctorCheck{
		Name:    "Schema Compatibility",
		Status:  statusOK,
		Message: "All required tables and columns present",
	}
}

func checkMergeDriver(path string) doctorCheck {
	// Check if we're in a git repository
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Git Merge Driver",
			Status:  statusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Get current merge driver configuration
	cmd := exec.Command("git", "config", "merge.beads.driver")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		// Merge driver not configured
		return doctorCheck{
			Name:    "Git Merge Driver",
			Status:  statusWarning,
			Message: "Git merge driver not configured",
			Fix:     "Run 'bd init' to configure the merge driver, or manually: git config merge.beads.driver \"bd merge %A %O %A %B\"",
		}
	}

	currentConfig := strings.TrimSpace(string(output))
	correctConfig := "bd merge %A %O %A %B"

	// Check if using old incorrect placeholders
	if strings.Contains(currentConfig, "%L") || strings.Contains(currentConfig, "%R") {
		return doctorCheck{
			Name:    "Git Merge Driver",
			Status:  statusError,
			Message: fmt.Sprintf("Incorrect merge driver config: %q (uses invalid %%L/%%R placeholders)", currentConfig),
			Detail:  "Git only supports %O (base), %A (current), %B (other). Using %L/%R causes merge failures.",
			Fix:     "Run 'bd doctor --fix' to update to correct config, or manually: git config merge.beads.driver \"bd merge %A %O %A %B\"",
		}
	}

	// Check if config is correct
	if currentConfig != correctConfig {
		return doctorCheck{
			Name:    "Git Merge Driver",
			Status:  statusWarning,
			Message: fmt.Sprintf("Non-standard merge driver config: %q", currentConfig),
			Detail:  fmt.Sprintf("Expected: %q", correctConfig),
			Fix:     fmt.Sprintf("Run 'bd doctor --fix' to update config, or manually: git config merge.beads.driver \"%s\"", correctConfig),
		}
	}

	return doctorCheck{
		Name:    "Git Merge Driver",
		Status:  statusOK,
		Message: "Correctly configured",
		Detail:  currentConfig,
	}
}

func checkMetadataVersionTracking(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Load metadata.json
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return doctorCheck{
			Name:    "Metadata Version Tracking",
			Status:  statusError,
			Message: "Unable to read metadata.json",
			Detail:  err.Error(),
			Fix:     "Ensure metadata.json exists and is valid JSON. Run 'bd init' if needed.",
		}
	}

	// Check if metadata.json exists
	if cfg == nil {
		return doctorCheck{
			Name:    "Metadata Version Tracking",
			Status:  statusWarning,
			Message: "metadata.json not found",
			Fix:     "Run any bd command to create metadata.json with version tracking",
		}
	}

	// Check if LastBdVersion field is present
	if cfg.LastBdVersion == "" {
		return doctorCheck{
			Name:    "Metadata Version Tracking",
			Status:  statusWarning,
			Message: "LastBdVersion field is empty (first run)",
			Detail:  "Version tracking will be initialized on next command",
			Fix:     "Run any bd command to initialize version tracking",
		}
	}

	// Validate that LastBdVersion is a valid semver-like string
	// Simple validation: should be X.Y.Z format where X, Y, Z are numbers
	if !isValidSemver(cfg.LastBdVersion) {
		return doctorCheck{
			Name:    "Metadata Version Tracking",
			Status:  statusWarning,
			Message: fmt.Sprintf("LastBdVersion has invalid format: %q", cfg.LastBdVersion),
			Detail:  "Expected semver format like '0.24.2'",
			Fix:     "Run any bd command to reset version tracking to current version",
		}
	}

	// Check if LastBdVersion is very old (> 10 versions behind)
	// Calculate version distance
	versionDiff := compareVersions(Version, cfg.LastBdVersion)
	if versionDiff > 0 {
		// Current version is newer - check how far behind
		currentParts := parseVersionParts(Version)
		lastParts := parseVersionParts(cfg.LastBdVersion)

		// Simple heuristic: warn if minor version is 10+ behind or major version differs by 1+
		majorDiff := currentParts[0] - lastParts[0]
		minorDiff := currentParts[1] - lastParts[1]

		if majorDiff >= 1 || (majorDiff == 0 && minorDiff >= 10) {
			return doctorCheck{
				Name:    "Metadata Version Tracking",
				Status:  statusWarning,
				Message: fmt.Sprintf("LastBdVersion is very old: %s (current: %s)", cfg.LastBdVersion, Version),
				Detail:  "You may have missed important upgrade notifications",
				Fix:     "Run 'bd upgrade review' to see recent changes",
			}
		}

		// Version is behind but not too old
		return doctorCheck{
			Name:    "Metadata Version Tracking",
			Status:  statusOK,
			Message: fmt.Sprintf("Version tracking active (last: %s, current: %s)", cfg.LastBdVersion, Version),
		}
	}

	// Version is current or ahead (shouldn't happen, but handle it)
	return doctorCheck{
		Name:    "Metadata Version Tracking",
		Status:  statusOK,
		Message: fmt.Sprintf("Version tracking active (version: %s)", cfg.LastBdVersion),
	}
}

// isValidSemver checks if a version string is valid semver-like format (X.Y.Z)
func isValidSemver(version string) bool {
	if version == "" {
		return false
	}

	// Split by dots and ensure all parts are numeric
	versionParts := strings.Split(version, ".")
	if len(versionParts) < 1 {
		return false
	}

	// Parse each part to ensure it's a valid number
	for _, part := range versionParts {
		if part == "" {
			return false
		}
		var num int
		if _, err := fmt.Sscanf(part, "%d", &num); err != nil {
			return false
		}
		if num < 0 {
			return false
		}
	}

	return true
}

// parseVersionParts parses version string into numeric parts
// Returns [major, minor, patch, ...] or empty slice on error
func parseVersionParts(version string) []int {
	parts := strings.Split(version, ".")
	result := make([]int, 0, len(parts))

	for _, part := range parts {
		var num int
		if _, err := fmt.Sscanf(part, "%d", &num); err != nil {
			return result
		}
		result = append(result, num)
	}

	return result
}

func checkSyncBranchConfig(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Skip if .beads doesn't exist
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Sync Branch Config",
			Status:  statusOK,
			Message: "N/A (no .beads directory)",
		}
	}

	// Check if we're in a git repository
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Sync Branch Config",
			Status:  statusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Check metadata.json first for custom database name
	var dbPath string
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		dbPath = cfg.DatabasePath(beadsDir)
	} else {
		// Fall back to canonical database name
		dbPath = filepath.Join(beadsDir, beads.CanonicalDatabaseName)
	}

	// Skip if no database (JSONL-only mode)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Sync Branch Config",
			Status:  statusOK,
			Message: "N/A (JSONL-only mode)",
		}
	}

	// Open database to check config
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return doctorCheck{
			Name:    "Sync Branch Config",
			Status:  statusWarning,
			Message: "Unable to check sync.branch config",
			Detail:  err.Error(),
		}
	}
	defer db.Close()

	// Check if sync.branch is configured
	var syncBranch string
	err = db.QueryRow("SELECT value FROM config WHERE key = ?", "sync.branch").Scan(&syncBranch)
	if err != nil && err != sql.ErrNoRows {
		return doctorCheck{
			Name:    "Sync Branch Config",
			Status:  statusWarning,
			Message: "Unable to read sync.branch config",
			Detail:  err.Error(),
		}
	}

	// If sync.branch is already configured, we're good
	if syncBranch != "" {
		return doctorCheck{
			Name:    "Sync Branch Config",
			Status:  statusOK,
			Message: fmt.Sprintf("Configured (%s)", syncBranch),
		}
	}

	// sync.branch is not configured - get current branch for the fix message
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return doctorCheck{
			Name:    "Sync Branch Config",
			Status:  statusWarning,
			Message: "sync.branch not configured",
			Detail:  "Unable to detect current branch",
			Fix:     "Run 'bd config set sync.branch <branch-name>' or 'bd doctor --fix' to auto-configure",
		}
	}

	currentBranch := strings.TrimSpace(string(output))
	return doctorCheck{
		Name:    "Sync Branch Config",
		Status:  statusWarning,
		Message: "sync.branch not configured",
		Detail:  fmt.Sprintf("Current branch: %s", currentBranch),
		Fix:     fmt.Sprintf("Run 'bd doctor --fix' to auto-configure to '%s', or manually: bd config set sync.branch <branch-name>", currentBranch),
	}
}

func checkDeletionsManifest(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Skip if .beads doesn't exist
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Deletions Manifest",
			Status:  statusOK,
			Message: "N/A (no .beads directory)",
		}
	}

	// Check if we're in a git repository
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Deletions Manifest",
			Status:  statusOK,
			Message: "N/A (not a git repository)",
		}
	}

	deletionsPath := filepath.Join(beadsDir, "deletions.jsonl")

	// Check if deletions.jsonl exists and has content
	info, err := os.Stat(deletionsPath)
	if err == nil && info.Size() > 0 {
		// Count entries
		file, err := os.Open(deletionsPath) // #nosec G304 - controlled path
		if err == nil {
			defer file.Close()
			count := 0
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				if len(scanner.Bytes()) > 0 {
					count++
				}
			}
			return doctorCheck{
				Name:    "Deletions Manifest",
				Status:  statusOK,
				Message: fmt.Sprintf("Present (%d entries)", count),
			}
		}
	}

	// deletions.jsonl doesn't exist or is empty
	// Check if there's git history that might have deletions
	// bd-6xd: Check canonical issues.jsonl first, then legacy beads.jsonl
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		jsonlPath = filepath.Join(beadsDir, "beads.jsonl")
		if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
			return doctorCheck{
				Name:    "Deletions Manifest",
				Status:  statusOK,
				Message: "N/A (no JSONL file)",
			}
		}
	}

	// Check if JSONL has any git history
	relPath, _ := filepath.Rel(path, jsonlPath)
	cmd := exec.Command("git", "log", "--oneline", "-1", "--", relPath) // #nosec G204 - args are controlled
	cmd.Dir = path
	if output, err := cmd.Output(); err != nil || len(output) == 0 {
		// No git history for JSONL
		return doctorCheck{
			Name:    "Deletions Manifest",
			Status:  statusOK,
			Message: "Not yet created (no deletions recorded)",
		}
	}

	// There's git history but no deletions manifest - recommend hydration
	return doctorCheck{
		Name:    "Deletions Manifest",
		Status:  statusWarning,
		Message: "Missing or empty (may have pre-v0.25.0 deletions)",
		Detail:  "Deleted issues from before v0.25.0 are not tracked and may resurrect on sync",
		Fix:     "Run 'bd doctor --fix' to hydrate deletions manifest from git history",
	}
}

// checkUntrackedBeadsFiles checks for untracked .beads/*.jsonl files that should be committed.
// This catches deletions.jsonl created by bd cleanup -f that hasn't been committed yet. (bd-pbj)
func checkUntrackedBeadsFiles(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")

	// Skip if .beads doesn't exist
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Untracked Files",
			Status:  statusOK,
			Message: "N/A (no .beads directory)",
		}
	}

	// Check if we're in a git repository
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return doctorCheck{
			Name:    "Untracked Files",
			Status:  statusOK,
			Message: "N/A (not a git repository)",
		}
	}

	// Run git status --porcelain to find untracked files in .beads/
	cmd := exec.Command("git", "status", "--porcelain", ".beads/")
	cmd.Dir = path
	output, err := cmd.Output()
	if err != nil {
		return doctorCheck{
			Name:    "Untracked Files",
			Status:  statusWarning,
			Message: "Unable to check git status",
			Detail:  err.Error(),
		}
	}

	// Parse output for untracked JSONL files (lines starting with "??")
	var untrackedJSONL []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Untracked files start with "?? "
		if strings.HasPrefix(line, "?? ") {
			file := strings.TrimPrefix(line, "?? ")
			// Only care about .jsonl files
			if strings.HasSuffix(file, ".jsonl") {
				untrackedJSONL = append(untrackedJSONL, filepath.Base(file))
			}
		}
	}

	if len(untrackedJSONL) == 0 {
		return doctorCheck{
			Name:    "Untracked Files",
			Status:  statusOK,
			Message: "All .beads/*.jsonl files are tracked",
		}
	}

	return doctorCheck{
		Name:    "Untracked Files",
		Status:  statusWarning,
		Message: fmt.Sprintf("Untracked JSONL files: %s", strings.Join(untrackedJSONL, ", ")),
		Detail:  "These files should be committed to propagate changes to other clones",
		Fix:     "Run 'bd doctor --fix' to stage and commit untracked files, or manually: git add .beads/*.jsonl && git commit",
	}
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&perfMode, "perf", false, "Run performance diagnostics and generate CPU profile")
	doctorCmd.Flags().BoolVar(&checkHealthMode, "check-health", false, "Quick health check for git hooks (silent on success)")
}
