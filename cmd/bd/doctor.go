package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/daemon"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
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

var doctorCmd = &cobra.Command{
	Use:   "doctor [path]",
	Short: "Check beads installation health",
	Long: `Sanity check the beads installation for the current directory or specified path.

This command checks:
  - If .beads/ directory exists
  - Database version and schema compatibility
  - Whether using hash-based vs sequential IDs
  - If CLI version is current (checks GitHub releases)
  - Multiple database files
  - Multiple JSONL files
  - Daemon health (version mismatches, stale processes)
  - Database-JSONL sync status
  - File permissions
  - Circular dependencies
  - Git hooks (pre-commit, post-merge, pre-push)

Examples:
  bd doctor              # Check current directory
  bd doctor /path/to/repo # Check specific repository
  bd doctor --json       # Machine-readable output`,
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

		// Run diagnostics
		result := runDiagnostics(absPath)

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

	// Check 6: Multiple JSONL files
	multiJSONLCheck := checkMultipleJSONLFiles(path)
	result.Checks = append(result.Checks, multiJSONLCheck)
	if multiJSONLCheck.Status == statusWarning || multiJSONLCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 7: Daemon health
	daemonCheck := checkDaemonStatus(path)
	result.Checks = append(result.Checks, daemonCheck)
	if daemonCheck.Status == statusWarning || daemonCheck.Status == statusError {
		result.OverallOK = false
	}

	// Check 8: Database-JSONL sync
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

	return result
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
		jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
		if _, err := os.Stat(jsonlPath); err == nil {
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
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return doctorCheck{
			Name:    "Issue IDs",
			Status:  statusError,
			Message: "Unable to open database",
		}
	}
	defer func() { _ = db.Close() }() // Intentionally ignore close error

	// Get first issue to check ID format
	var issueID string
	err = db.QueryRow("SELECT id FROM issues ORDER BY created_at LIMIT 1").Scan(&issueID)
	if err == sql.ErrNoRows {
		return doctorCheck{
			Name:    "Issue IDs",
			Status:  statusOK,
			Message: "No issues yet (will use hash-based IDs)",
		}
	}
	if err != nil {
		return doctorCheck{
			Name:    "Issue IDs",
			Status:  statusError,
			Message: "Unable to query issues",
		}
	}

	// Detect ID format
	if isHashID(issueID) {
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
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
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

func checkMultipleJSONLFiles(path string) doctorCheck {
	beadsDir := filepath.Join(path, ".beads")
	
	var jsonlFiles []string
	for _, name := range []string{"issues.jsonl", "beads.jsonl"} {
		jsonlPath := filepath.Join(beadsDir, name)
		if _, err := os.Stat(jsonlPath); err == nil {
			jsonlFiles = append(jsonlFiles, name)
		}
	}

	if len(jsonlFiles) == 0 {
		return doctorCheck{
			Name:    "JSONL Files",
			Status:  statusOK,
			Message: "No JSONL files found (database-only mode)",
		}
	}

	if len(jsonlFiles) == 1 {
		return doctorCheck{
			Name:    "JSONL Files",
			Status:  statusOK,
			Message: fmt.Sprintf("Using %s", jsonlFiles[0]),
		}
	}

	// Multiple JSONL files found
	return doctorCheck{
		Name:    "JSONL Files",
		Status:  statusWarning,
		Message: fmt.Sprintf("Multiple JSONL files found: %s", strings.Join(jsonlFiles, ", ")),
		Fix:     "Standardize on one JSONL file (issues.jsonl recommended). Delete or rename the other.",
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
		path := filepath.Join(beadsDir, name)
		if _, err := os.Stat(path); err == nil {
			jsonlPath = path
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

	// Compare modification times
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

	// If JSONL is newer, warn about potential sync issue
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
		db, err := sql.Open("sqlite", dbPath)
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
	db, err := sql.Open("sqlite", dbPath)
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
		"pre-commit":  "Flushes pending bd changes to JSONL before commit",
		"post-merge":  "Imports updated JSONL after git pull/merge",
		"pre-push":    "Exports database to JSONL before push",
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

	if len(installedHooks) > 0 {
		return doctorCheck{
			Name:    "Git Hooks",
			Status:  statusWarning,
			Message: fmt.Sprintf("Missing %d recommended hook(s)", len(missingHooks)),
			Detail:  fmt.Sprintf("Missing: %s", strings.Join(missingHooks, ", ")),
			Fix:     "Run './examples/git-hooks/install.sh' to install recommended git hooks",
		}
	}

	return doctorCheck{
		Name:    "Git Hooks",
		Status:  statusWarning,
		Message: "No recommended git hooks installed",
		Detail:  fmt.Sprintf("Recommended: %s", strings.Join([]string{"pre-commit", "post-merge", "pre-push"}, ", ")),
		Fix:     "Run './examples/git-hooks/install.sh' to install recommended git hooks",
	}
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
