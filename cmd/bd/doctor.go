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
	_ "modernc.org/sqlite"
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

Examples:
  bd doctor              # Check current directory
  bd doctor /path/to/repo # Check specific repository
  bd doctor --json       # Machine-readable output`,
	Run: func(cmd *cobra.Command, args []string) {
		// Get json flag from command
		jsonOutput, _ := cmd.Flags().GetBool("json")

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
		// If no .beads/, skip other checks
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
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)

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
	dbPath := filepath.Join(beadsDir, beads.CanonicalDatabaseName)

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
			fmt.Sscanf(parts1[i], "%d", &p1)
		}
		if i < len(parts2) {
			fmt.Sscanf(parts2[i], "%d", &p2)
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
	defer resp.Body.Close()

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

func init() {
	doctorCmd.Flags().Bool("json", false, "Output JSON format")
	rootCmd.AddCommand(doctorCmd)
}
