package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/ui"
)

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

// runServerHealth runs Dolt server mode health checks
func runServerHealth(path string) {
	result := doctor.RunServerHealthChecks(path)

	if jsonOutput {
		jsonBytes, _ := json.Marshal(result)
		fmt.Println(string(jsonBytes))
	} else {
		fmt.Println("Dolt Server Mode Health Check")
		fmt.Println()
		printServerHealthResult(result)
	}

	if !result.OverallOK {
		os.Exit(1)
	}
}

// printServerHealthResult prints the server health check results
func printServerHealthResult(result doctor.ServerHealthResult) {
	var passCount, warnCount, failCount int
	var warnings []doctor.DoctorCheck

	for _, check := range result.Checks {
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
			// Indent detail lines
			for _, line := range strings.Split(check.Detail, "\n") {
				fmt.Printf("     %s%s\n", ui.MutedStyle.Render(ui.TreeLast), ui.RenderMuted(line))
			}
		}
	}

	fmt.Println()

	// Summary line
	fmt.Println(ui.RenderSeparator())
	summary := fmt.Sprintf("%s %d passed  %s %d warnings  %s %d failed",
		ui.RenderPassIcon(), passCount,
		ui.RenderWarnIcon(), warnCount,
		ui.RenderFailIcon(), failCount,
	)
	fmt.Println(summary)

	// Print fixes for any errors/warnings
	if len(warnings) > 0 {
		fmt.Println()
		fmt.Println(ui.RenderWarn(ui.IconWarn + "  FIXES NEEDED"))
		for i, check := range warnings {
			if check.Fix == "" {
				continue
			}
			line := fmt.Sprintf("%s: %s", check.Name, check.Message)
			if check.Status == statusError {
				fmt.Printf("  %s  %s %s\n", ui.RenderFailIcon(), ui.RenderFail(fmt.Sprintf("%d.", i+1)), ui.RenderFail(line))
			} else {
				fmt.Printf("  %s  %s %s\n", ui.RenderWarnIcon(), ui.RenderWarn(fmt.Sprintf("%d.", i+1)), line)
			}
			fmt.Printf("        %s%s\n", ui.MutedStyle.Render(ui.TreeLast), check.Fix)
		}
	} else if result.OverallOK {
		fmt.Println()
		fmt.Printf("%s\n", ui.RenderPass("✓ All server health checks passed"))
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
