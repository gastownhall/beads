package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/ui"
)

// runCheckHealth runs lightweight health checks for git hooks.
// Silent on success, prints a hint if issues detected.
// Respects hints.doctor config setting.
func runCheckHealth(path string) error {
	beadsDir := doctor.ResolveBeadsDirForRepo(path)

	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return nil
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		if issue := doctor.CheckHooksQuick(Version); issue != "" {
			return printCheckHealthHint([]string{issue})
		}
		return nil
	}

	host := cfg.GetDoltServerHost()
	port := doltserver.DefaultConfig(beadsDir).Port
	if port == 0 {
		if issue := doctor.CheckHooksQuick(Version); issue != "" {
			return printCheckHealthHint([]string{issue})
		}
		return nil
	}
	database := cfg.GetDoltDatabase()

	var issues []string

	dsn := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     cfg.GetDoltServerUser(),
		Password: cfg.GetDoltServerPasswordForPort(port),
		Database: database,
		Timeout:  2 * time.Second,
		TLS:      cfg.GetDoltServerTLS(),
	}.String()
	db, err := sql.Open("mysql", dsn)
	if err == nil {
		defer db.Close()
		if pingErr := db.Ping(); pingErr == nil {
			if hintsDisabledDB(db) {
				return nil
			}
			if issue := checkVersionMismatchDB(db); issue != "" {
				issues = append(issues, issue)
			}
		}
	}

	if issue := doctor.CheckHooksQuick(Version); issue != "" {
		issues = append(issues, issue)
	}

	if len(issues) > 0 {
		return printCheckHealthHint(issues)
	}
	return nil
}

// runDeepValidation runs full graph integrity validation
func runDeepValidation(path string) error {
	fmt.Println("Running deep validation (may be slow on large databases)...")
	fmt.Println()

	result := doctor.RunDeepValidation(path)

	if jsonOutput {
		jsonBytes, err := doctor.DeepValidationResultJSON(result)
		if err != nil {
			return HandleError("%v", err)
		}
		fmt.Println(string(jsonBytes))
	} else {
		doctor.PrintDeepValidationResult(result)
	}

	if !result.OverallOK {
		return SilentExit()
	}
	return nil
}

// runServerHealth runs Dolt server mode health checks
func runServerHealth(path string) error {
	result := doctor.RunServerHealthChecks(path)

	if jsonOutput {
		jsonBytes, err := json.Marshal(result)
		if err != nil {
			return HandleError("failed to marshal health check result: %v", err)
		}
		fmt.Println(string(jsonBytes))
	} else {
		fmt.Println("Dolt Server Mode Health Check")
		fmt.Println()
		printServerHealthResult(result)
	}

	if !result.OverallOK {
		return SilentExit()
	}
	return nil
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

func printCheckHealthHint(issues []string) error {
	fmt.Fprintf(os.Stderr, "💡 bd doctor recommends a health check:\n")
	for _, issue := range issues {
		fmt.Fprintf(os.Stderr, "   • %s\n", issue)
	}
	fmt.Fprintf(os.Stderr, "   Run 'bd doctor' for details, or 'bd doctor --fix' to auto-repair\n")
	fmt.Fprintf(os.Stderr, "   (Suppress with: bd config set %s false)\n", ConfigKeyHintsDoctor)
	return SilentExit()
}

// hintsDisabledDB checks if hints.doctor is set to "false" using an existing DB connection.
// Used by runCheckHealth to avoid multiple DB opens.
func hintsDisabledDB(db *sql.DB) bool {
	var value string
	err := db.QueryRow("SELECT value FROM config WHERE `key` = ?", ConfigKeyHintsDoctor).Scan(&value)
	if err != nil {
		return false // Key not set, assume hints enabled
	}
	return strings.ToLower(value) == "false"
}

// checkVersionMismatchDB checks if CLI version differs from database bd_version.
// Uses an existing DB connection.
func checkVersionMismatchDB(db *sql.DB) string {
	var dbVersion string
	err := db.QueryRow("SELECT value FROM local_metadata WHERE `key` = 'bd_version'").Scan(&dbVersion)
	if err != nil {
		return "" // Can't read version, skip
	}

	if dbVersion != "" && dbVersion != Version {
		return fmt.Sprintf("Version mismatch (CLI: %s, database: %s)", Version, dbVersion)
	}

	return ""
}
