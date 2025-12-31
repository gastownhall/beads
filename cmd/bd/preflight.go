package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// CheckResult represents the result of a single preflight check.
type CheckResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Skipped bool   `json:"skipped,omitempty"`
	Output  string `json:"output,omitempty"`
	Command string `json:"command"`
}

// PreflightResult represents the overall preflight check results.
type PreflightResult struct {
	Checks  []CheckResult `json:"checks"`
	Passed  bool          `json:"passed"`
	Summary string        `json:"summary"`
}

var preflightCmd = &cobra.Command{
	Use:     "preflight",
	GroupID: "maint",
	Short:   "Show PR readiness checklist",
	Long: `Display a checklist of common pre-PR checks for contributors.

This command helps catch common issues before pushing to CI:
- Tests not run locally
- Lint errors
- Stale nix vendorHash
- Version mismatches

Examples:
  bd preflight              # Show checklist
  bd preflight --check      # Run checks automatically
  bd preflight --check --json  # JSON output for programmatic use
`,
	Run: runPreflight,
}

func init() {
	preflightCmd.Flags().Bool("check", false, "Run checks automatically")
	preflightCmd.Flags().Bool("fix", false, "Auto-fix issues where possible (not yet implemented)")
	preflightCmd.Flags().Bool("json", false, "Output results as JSON")

	rootCmd.AddCommand(preflightCmd)
}

func runPreflight(cmd *cobra.Command, args []string) {
	check, _ := cmd.Flags().GetBool("check")
	fix, _ := cmd.Flags().GetBool("fix")
	jsonOutput, _ := cmd.Flags().GetBool("json")

	if fix {
		fmt.Println("Note: --fix is not yet implemented.")
		fmt.Println("See bd-lfak.3 through bd-lfak.5 for implementation roadmap.")
		fmt.Println()
	}

	if check {
		runChecks(jsonOutput)
		return
	}

	// Static checklist mode
	fmt.Println("PR Readiness Checklist:")
	fmt.Println()
	fmt.Println("[ ] Tests pass: go test -short ./...")
	fmt.Println("[ ] Lint passes: golangci-lint run ./...")
	fmt.Println("[ ] No beads pollution: check .beads/issues.jsonl diff")
	fmt.Println("[ ] Nix hash current: go.sum unchanged or vendorHash updated")
	fmt.Println("[ ] Version sync: version.go matches default.nix")
	fmt.Println()
	fmt.Println("Run 'bd preflight --check' to validate automatically.")
}

// runChecks executes all preflight checks and reports results.
func runChecks(jsonOutput bool) {
	var results []CheckResult

	// Run test check
	testResult := runTestCheck()
	results = append(results, testResult)

	// Run lint check
	lintResult := runLintCheck()
	results = append(results, lintResult)

	// Calculate overall result
	allPassed := true
	passCount := 0
	skipCount := 0
	for _, r := range results {
		if r.Skipped {
			skipCount++
		} else if r.Passed {
			passCount++
		} else {
			allPassed = false
		}
	}

	runCount := len(results) - skipCount
	summary := fmt.Sprintf("%d/%d checks passed", passCount, runCount)
	if skipCount > 0 {
		summary += fmt.Sprintf(" (%d skipped)", skipCount)
	}

	if jsonOutput {
		result := PreflightResult{
			Checks:  results,
			Passed:  allPassed,
			Summary: summary,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
	} else {
		// Human-readable output
		for _, r := range results {
			if r.Skipped {
				fmt.Printf("⚠ %s (skipped)\n", r.Name)
			} else if r.Passed {
				fmt.Printf("✓ %s\n", r.Name)
			} else {
				fmt.Printf("✗ %s\n", r.Name)
			}
			fmt.Printf("  Command: %s\n", r.Command)
			if r.Skipped && r.Output != "" {
				// Show skip reason
				fmt.Printf("  Reason: %s\n", r.Output)
			} else if !r.Passed && r.Output != "" {
				// Truncate output for terminal display
				output := truncateOutput(r.Output, 500)
				fmt.Printf("  Output:\n")
				for _, line := range strings.Split(output, "\n") {
					fmt.Printf("    %s\n", line)
				}
			}
			fmt.Println()
		}
		fmt.Println(summary)
	}

	if !allPassed {
		os.Exit(1)
	}
}

// runTestCheck runs go test -short ./... and returns the result.
func runTestCheck() CheckResult {
	command := "go test -short ./..."
	cmd := exec.Command("go", "test", "-short", "./...")
	output, err := cmd.CombinedOutput()

	return CheckResult{
		Name:    "Tests pass",
		Passed:  err == nil,
		Output:  string(output),
		Command: command,
	}
}

// runLintCheck runs golangci-lint and returns the result.
func runLintCheck() CheckResult {
	command := "golangci-lint run ./..."

	// Check if golangci-lint is available
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		return CheckResult{
			Name:    "Lint passes",
			Passed:  false,
			Skipped: true,
			Output:  "golangci-lint not found in PATH",
			Command: command,
		}
	}

	cmd := exec.Command("golangci-lint", "run", "./...")
	output, err := cmd.CombinedOutput()

	return CheckResult{
		Name:    "Lint passes",
		Passed:  err == nil,
		Output:  string(output),
		Command: command,
	}
}

// truncateOutput truncates output to maxLen characters, adding ellipsis if truncated.
func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[:maxLen]) + "\n... (truncated)"
}
