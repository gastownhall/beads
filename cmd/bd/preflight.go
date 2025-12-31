package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// CheckResult represents the outcome of a single preflight check
type CheckResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Command string `json:"command"`
	Output  string `json:"output,omitempty"`
}

// PreflightResults holds all check results for JSON output
type PreflightResults struct {
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
  bd preflight --check      # Run tests automatically
  bd preflight --check --json  # Run tests with JSON output
  bd preflight --fix        # (future) Auto-fix where possible
`,
	Run: runPreflight,
}

func init() {
	preflightCmd.Flags().Bool("check", false, "Run checks automatically")
	preflightCmd.Flags().Bool("fix", false, "Auto-fix issues where possible (not yet implemented)")

	rootCmd.AddCommand(preflightCmd)
}

func runPreflight(cmd *cobra.Command, args []string) {
	check, _ := cmd.Flags().GetBool("check")
	fix, _ := cmd.Flags().GetBool("fix")

	if fix {
		fmt.Println("Note: --fix is not yet implemented.")
		fmt.Println("See bd-lfak.3 through bd-lfak.5 for implementation roadmap.")
		fmt.Println()
	}

	if check {
		runChecks(cmd)
		return
	}

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

// runChecks executes the preflight checks and reports results
func runChecks(cmd *cobra.Command) {
	results := []CheckResult{
		runTestCheck(),
	}

	allPassed := true
	for _, r := range results {
		if !r.Passed {
			allPassed = false
			break
		}
	}

	// Build summary
	passCount := 0
	failCount := 0
	for _, r := range results {
		if r.Passed {
			passCount++
		} else {
			failCount++
		}
	}
	summary := fmt.Sprintf("%d passed, %d failed", passCount, failCount)

	preflightResults := PreflightResults{
		Checks:  results,
		Passed:  allPassed,
		Summary: summary,
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(preflightResults)
	} else {
		for _, r := range results {
			printCheckResult(r)
		}
		fmt.Println()
		fmt.Println(summary)
	}

	if !allPassed {
		os.Exit(1)
	}
}

// runTestCheck runs go test -short ./... and returns the result
func runTestCheck() CheckResult {
	command := "go test -short ./..."
	cmd := exec.Command("go", "test", "-short", "./...")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	// Truncate output if too long
	// On failure, keep beginning (failure context) and end (summary)
	if len(output) > 3000 {
		lines := strings.Split(output, "\n")
		// Keep first 30 lines and last 20 lines
		if len(lines) > 50 {
			firstPart := strings.Join(lines[:30], "\n")
			lastPart := strings.Join(lines[len(lines)-20:], "\n")
			output = firstPart + "\n\n...(truncated " + fmt.Sprintf("%d", len(lines)-50) + " lines)...\n\n" + lastPart
		}
	}

	return CheckResult{
		Name:    "tests",
		Passed:  err == nil,
		Command: command,
		Output:  strings.TrimSpace(output),
	}
}

// printCheckResult prints a single check result with formatting
func printCheckResult(r CheckResult) {
	if r.Passed {
		fmt.Printf("✓ %s\n", capitalizeFirst(r.Name))
		fmt.Printf("  Command: %s\n", r.Command)
	} else {
		fmt.Printf("✗ %s\n", capitalizeFirst(r.Name))
		fmt.Printf("  Command: %s\n", r.Command)
		if r.Output != "" {
			fmt.Println("  Output:")
			for _, line := range strings.Split(r.Output, "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}
}

// capitalizeFirst capitalizes the first letter of a string
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
