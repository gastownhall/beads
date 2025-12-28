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
	"github.com/steveyegge/beads/internal/types"
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
}

// previewFixes shows what would be fixed without applying changes
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

	// Interactive mode - confirm each fix individually
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

// applyFixesInteractive prompts for each fix individually
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
	// Apply fixes in a dependency-aware order.
	// Rough dependency chain:
	// permissions/daemon cleanup → config sanity → DB integrity/migrations → DB↔JSONL sync.
	order := []string{
		"Permissions",
		"Daemon Health",
		"Database Config",
		"JSONL Config",
		"Database Integrity",
		"Database",
		"Schema Compatibility",
		"JSONL Integrity",
		"DB-JSONL Sync",
	}
	priority := make(map[string]int, len(order))
	for i, name := range order {
		priority[name] = i
	}
	slices.SortStableFunc(fixes, func(a, b doctorCheck) int {
		pa, oka := priority[a.Name]
		if !oka {
			pa = 1000
		}
		pb, okb := priority[b.Name]
		if !okb {
			pb = 1000
		}
		if pa < pb {
			return -1
		}
		if pa > pb {
			return 1
		}
		return 0
	})

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
		case "Database Integrity":
			// Corruption detected - try recovery from JSONL
			err = fix.DatabaseCorruptionRecovery(path)
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
		case "JSONL Integrity":
			err = fix.JSONLIntegrity(path)
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
		case "Child-Parent Dependencies":
			// Requires explicit opt-in flag (destructive, may remove intentional deps)
			if !doctorFixChildParent {
				fmt.Printf("  ⚠ Child→parent deps require explicit opt-in: bd doctor --fix --fix-child-parent\n")
				continue
			}
			err = fix.ChildParentDependencies(path)
		case "Duplicate Issues":
			// No auto-fix: duplicates require user review
			fmt.Printf("  ⚠ Run 'bd duplicates' to review and merge duplicates\n")
			continue
		case "Test Pollution":
			// No auto-fix: test cleanup requires user review
			fmt.Printf("  ⚠ Run 'bd doctor --check=pollution' to review and clean test issues\n")
			continue
		case "Git Conflicts":
			// No auto-fix: git conflicts require manual resolution
			fmt.Printf("  ⚠ Resolve conflicts manually: git checkout --ours or --theirs .beads/issues.jsonl\n")
			continue
		case "Stale Closed Issues":
			// consolidate cleanup into doctor --fix
			err = fix.StaleClosedIssues(path)
		case "Expired Tombstones":
			// consolidate cleanup into doctor --fix
			err = fix.ExpiredTombstones(path)
		case "Compaction Candidates":
			// No auto-fix: compaction requires agent review
			fmt.Printf("  ⚠ Run 'bd compact --analyze' to review candidates\n")
			continue
		case "Large Database":
			// No auto-fix: pruning deletes data, must be user-controlled
			fmt.Printf("  ⚠ Run 'bd cleanup --older-than 90' to prune old closed issues\n")
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

	// Get database path once (centralized path resolution)
	dbPath := getCheckHealthDBPath(beadsDir)

	// Check if database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// No database - only check hooks
		if issue := doctor.CheckHooksQuick(Version); issue != "" {
			printCheckHealthHint([]string{issue})
		}
		return
	}

	// Open database once for all checks (single DB connection)
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

// runDeepValidation runs full graph integrity validation
func runDeepValidation(path string) {
	// Show warning about potential slowness
	fmt.Println("Running deep validation (may be slow on large databases)...")
	fmt.Println()

	result := doctor.RunDeepValidation(path)

	if jsonOutput {
		jsonBytes, err := doctor.DeepValidationResultJSON(result)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(jsonBytes))
	} else {
		doctor.PrintDeepValidationResult(result)
	}

	if !result.OverallOK {
		os.Exit(1)
	}
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
// This centralizes the path resolution logic.
func getCheckHealthDBPath(beadsDir string) string {
	if cfg, err := configfile.Load(beadsDir); err == nil && cfg != nil && cfg.Database != "" {
		return cfg.DatabasePath(beadsDir)
	}
	return filepath.Join(beadsDir, beads.CanonicalDatabaseName)
}

// hintsDisabledDB checks if hints.doctor is set to "false" using an existing DB connection.
// Used by runCheckHealth to avoid multiple DB opens.
func hintsDisabledDB(db *sql.DB) bool {
	var value string
	err := db.QueryRow("SELECT value FROM config WHERE key = ?", ConfigKeyHintsDoctor).Scan(&value)
	if err != nil {
		return false // Key not set, assume hints enabled
	}
	return strings.ToLower(value) == "false"
}

// checkVersionMismatchDB checks if CLI version differs from database bd_version.
// Uses an existing DB connection.
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

// runPollutionCheck runs detailed test pollution detection
// This integrates the detect-pollution command functionality into doctor.
func runPollutionCheck(path string, clean bool, yes bool) {
	// Ensure we have a store initialized (uses direct mode, no daemon support yet)
	if err := ensureDirectMode("pollution check requires direct mode"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctx := rootCtx

	// Get all issues
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching issues: %v\n", err)
		os.Exit(1)
	}

	// Detect pollution (reuse detectTestPollution from detect_pollution.go)
	polluted := detectTestPollution(allIssues)

	if len(polluted) == 0 {
		if !jsonOutput {
			fmt.Println("No test pollution detected!")
		} else {
			outputJSON(map[string]interface{}{
				"polluted_count": 0,
				"issues":         []interface{}{},
			})
		}
		return
	}

	// Categorize by confidence
	highConfidence := []pollutionResult{}
	mediumConfidence := []pollutionResult{}

	for _, p := range polluted {
		if p.score >= 0.9 {
			highConfidence = append(highConfidence, p)
		} else {
			mediumConfidence = append(mediumConfidence, p)
		}
	}

	if jsonOutput {
		result := map[string]interface{}{
			"polluted_count":    len(polluted),
			"high_confidence":   len(highConfidence),
			"medium_confidence": len(mediumConfidence),
			"issues":            []map[string]interface{}{},
		}

		for _, p := range polluted {
			result["issues"] = append(result["issues"].([]map[string]interface{}), map[string]interface{}{
				"id":         p.issue.ID,
				"title":      p.issue.Title,
				"score":      p.score,
				"reasons":    p.reasons,
				"created_at": p.issue.CreatedAt,
			})
		}

		outputJSON(result)
		return
	}

	// Human-readable output
	fmt.Printf("Found %d potential test issues:\n\n", len(polluted))

	if len(highConfidence) > 0 {
		fmt.Printf("High Confidence (score ≥ 0.9):\n")
		for _, p := range highConfidence {
			fmt.Printf("  %s: %q (score: %.2f)\n", p.issue.ID, p.issue.Title, p.score)
			for _, reason := range p.reasons {
				fmt.Printf("    - %s\n", reason)
			}
		}
		fmt.Printf("  (Total: %d issues)\n\n", len(highConfidence))
	}

	if len(mediumConfidence) > 0 {
		fmt.Printf("Medium Confidence (score 0.7-0.9):\n")
		for _, p := range mediumConfidence {
			fmt.Printf("  %s: %q (score: %.2f)\n", p.issue.ID, p.issue.Title, p.score)
			for _, reason := range p.reasons {
				fmt.Printf("    - %s\n", reason)
			}
		}
		fmt.Printf("  (Total: %d issues)\n\n", len(mediumConfidence))
	}

	if !clean {
		fmt.Printf("Run 'bd doctor --check=pollution --clean' to delete these issues (with confirmation).\n")
		return
	}

	// Confirmation prompt
	if !yes {
		fmt.Printf("\nDelete %d test issues? [y/N] ", len(polluted))
		var response string
		_, _ = fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Canceled.")
			return
		}
	}

	// Backup to JSONL before deleting
	backupPath := ".beads/pollution-backup.jsonl"
	if err := backupPollutedIssues(polluted, backupPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error backing up issues: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Backed up %d issues to %s\n", len(polluted), backupPath)

	// Delete issues
	fmt.Printf("\nDeleting %d issues...\n", len(polluted))
	deleted := 0
	for _, p := range polluted {
		if err := deleteIssue(ctx, p.issue.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting %s: %v\n", p.issue.ID, err)
			continue
		}
		deleted++
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	fmt.Printf("%s Deleted %d test issues\n", ui.RenderPass("✓"), deleted)
	fmt.Printf("\nCleanup complete. To restore, run: bd import %s\n", backupPath)
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	doctorCmd.Flags().BoolVar(&perfMode, "perf", false, "Run performance diagnostics and generate CPU profile")
	doctorCmd.Flags().BoolVar(&checkHealthMode, "check-health", false, "Quick health check for git hooks (silent on success)")
	doctorCmd.Flags().StringVarP(&doctorOutput, "output", "o", "", "Export diagnostics to JSON file")
	doctorCmd.Flags().StringVar(&doctorCheckFlag, "check", "", "Run specific check in detail (e.g., 'pollution')")
	doctorCmd.Flags().BoolVar(&doctorClean, "clean", false, "For pollution check: delete detected test issues")
	doctorCmd.Flags().BoolVar(&doctorDeep, "deep", false, "Validate full graph integrity")
}
