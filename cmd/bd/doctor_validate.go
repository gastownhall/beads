package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/ui"
)

// validateCheckResult pairs a doctor check with whether it can be auto-fixed.
type validateCheckResult struct {
	check   doctorCheck
	fixable bool
}

// runValidateCheck runs focused data-integrity checks and exits non-zero on failure.
func runValidateCheck(path string) {
	if !runValidateCheckInner(path) {
		os.Exit(1)
	}
}

// runValidateCheckInner runs the checks and returns true if all passed.
// Separated from runValidateCheck so tests can call it without os.Exit.
func runValidateCheckInner(path string) bool {
	checks := collectValidateChecks(path)

	// Apply fixes if --fix is set, then re-check to reflect post-fix state
	if doctorFix {
		applyValidateFixes(path, checks)
		checks = collectValidateChecks(path)
	}

	overallOK := validateOverallOK(checks)

	// JSON output
	if jsonOutput {
		result := struct {
			Path      string        `json:"path"`
			Checks    []doctorCheck `json:"checks"`
			OverallOK bool          `json:"overall_ok"`
		}{
			Path:      path,
			OverallOK: overallOK,
		}
		for _, cr := range checks {
			result.Checks = append(result.Checks, cr.check)
		}
		outputJSON(result)
		return overallOK
	}

	// Human-readable output
	printValidateChecks(checks)

	if !doctorFix && !overallOK {
		// Suggest --fix if there are fixable issues
		for _, cr := range checks {
			if cr.fixable && cr.check.Status != statusOK {
				fmt.Printf("\n%s\n", ui.RenderMuted("Tip: Use 'bd doctor --check=validate --fix' to auto-repair fixable issues"))
				break
			}
		}
	}

	if overallOK {
		fmt.Println()
		fmt.Printf("%s\n", ui.RenderPass("✓ All data-integrity checks passed"))
	}

	return overallOK
}

// collectValidateChecks runs the four data-integrity checks.
func collectValidateChecks(path string) []validateCheckResult {
	return []validateCheckResult{
		{check: convertDoctorCheck(doctor.CheckDuplicateIssues(path, doctorGastown, gastownDuplicatesThreshold))},
		{check: convertDoctorCheck(doctor.CheckOrphanedDependencies(path)), fixable: true},
		{check: convertDoctorCheck(doctor.CheckTestPollution(path))},
		{check: convertDoctorCheck(doctor.CheckGitConflicts(path))},
	}
}

func validateOverallOK(checks []validateCheckResult) bool {
	for _, cr := range checks {
		if cr.check.Status == statusError || cr.check.Status == statusWarning {
			return false
		}
	}
	return true
}

func printValidateChecks(checks []validateCheckResult) {
	fmt.Println()
	fmt.Println(ui.RenderCategory("Data Integrity"))

	var passCount, warnCount, failCount int
	for _, cr := range checks {
		var statusIcon string
		switch cr.check.Status {
		case statusOK:
			statusIcon = ui.RenderPassIcon()
			passCount++
		case statusWarning:
			statusIcon = ui.RenderWarnIcon()
			warnCount++
		case statusError:
			statusIcon = ui.RenderFailIcon()
			failCount++
		}

		fmt.Printf("  %s  %s", statusIcon, cr.check.Name)
		if cr.check.Message != "" {
			fmt.Printf("%s", ui.RenderMuted(" "+cr.check.Message))
		}
		fmt.Println()
		if cr.check.Detail != "" {
			fmt.Printf("     %s%s\n", ui.MutedStyle.Render(ui.TreeLast), ui.RenderMuted(cr.check.Detail))
		}
	}

	fmt.Println()
	fmt.Println(ui.RenderSeparator())
	fmt.Printf("%s %d passed  %s %d warnings  %s %d failed\n",
		ui.RenderPassIcon(), passCount,
		ui.RenderWarnIcon(), warnCount,
		ui.RenderFailIcon(), failCount,
	)
}

// applyValidateFixes auto-repairs fixable validation issues.
// Reuses doctor's applyFixList for dispatch (doctor_fix.go), which already
// handles the "Orphaned Dependencies" case and any future fixable checks.
func applyValidateFixes(path string, checks []validateCheckResult) {
	var fixable []doctorCheck
	for _, cr := range checks {
		if cr.fixable && cr.check.Status != statusOK {
			fixable = append(fixable, cr.check)
		}
	}

	if len(fixable) == 0 {
		return
	}

	// Confirm unless --yes (matching doctor's applyFixes pattern)
	if !doctorYes {
		fmt.Println("\nFixable issues:")
		for i, check := range fixable {
			fmt.Printf("  %d. %s: %s\n", i+1, check.Name, check.Message)
		}
		fmt.Printf("\nThis will attempt to fix %d issue(s). Continue? (Y/n): ", len(fixable))
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

	fmt.Println("\nApplying fixes...")
	applyFixList(path, fixable)
}
