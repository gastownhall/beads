package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// CheckResult represents the result of a single preflight check.
type CheckResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Skipped bool   `json:"skipped,omitempty"`
	Warning bool   `json:"warning,omitempty"`
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

	// Run nix hash check
	nixResult := runNixHashCheck()
	results = append(results, nixResult)

	// Run version sync check
	versionResult := runVersionSyncCheck()
	results = append(results, versionResult)

	// Calculate overall result
	allPassed := true
	passCount := 0
	skipCount := 0
	warnCount := 0
	for _, r := range results {
		if r.Skipped {
			skipCount++
		} else if r.Warning {
			warnCount++
			// Warnings don't fail the overall result but count as "not passed"
		} else if r.Passed {
			passCount++
		} else {
			allPassed = false
		}
	}

	runCount := len(results) - skipCount
	summary := fmt.Sprintf("%d/%d checks passed", passCount, runCount)
	if warnCount > 0 {
		summary += fmt.Sprintf(", %d warning(s)", warnCount)
	}
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
		if err := enc.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding preflight result: %v\n", err)
		}
	} else {
		// Human-readable output
		for _, r := range results {
			if r.Skipped {
				fmt.Printf("⚠ %s (skipped)\n", r.Name)
			} else if r.Warning {
				fmt.Printf("⚠ %s\n", r.Name)
			} else if r.Passed {
				fmt.Printf("✓ %s\n", r.Name)
			} else {
				fmt.Printf("✗ %s\n", r.Name)
			}
			fmt.Printf("  Command: %s\n", r.Command)
			if r.Skipped && r.Output != "" {
				// Show skip reason
				fmt.Printf("  Reason: %s\n", r.Output)
			} else if r.Warning && r.Output != "" {
				// Show warning message
				fmt.Printf("  Warning: %s\n", r.Output)
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

// runNixHashCheck checks if go.sum has uncommitted changes that may require vendorHash update.
func runNixHashCheck() CheckResult {
	command := "git diff HEAD -- go.sum"

	// Check for unstaged changes to go.sum
	cmd := exec.Command("git", "diff", "--name-only", "HEAD", "--", "go.sum")
	output, _ := cmd.Output()

	// Check for staged changes to go.sum
	stagedCmd := exec.Command("git", "diff", "--name-only", "--cached", "--", "go.sum")
	stagedOutput, _ := stagedCmd.Output()

	hasChanges := len(strings.TrimSpace(string(output))) > 0 || len(strings.TrimSpace(string(stagedOutput))) > 0

	if hasChanges {
		return CheckResult{
			Name:    "Nix hash current",
			Passed:  false,
			Warning: true,
			Output:  "go.sum has uncommitted changes - vendorHash in default.nix may need updating",
			Command: command,
		}
	}

	return CheckResult{
		Name:    "Nix hash current",
		Passed:  true,
		Output:  "",
		Command: command,
	}
}

// runVersionSyncCheck checks that version.go matches default.nix.
func runVersionSyncCheck() CheckResult {
	command := "Compare cmd/bd/version.go and default.nix"

	// Read version.go
	versionGoContent, err := os.ReadFile("cmd/bd/version.go")
	if err != nil {
		return CheckResult{
			Name:    "Version sync",
			Passed:  false,
			Skipped: true,
			Output:  fmt.Sprintf("Cannot read cmd/bd/version.go: %v", err),
			Command: command,
		}
	}

	// Extract version from version.go
	// Pattern: Version = "X.Y.Z"
	versionGoRe := regexp.MustCompile(`Version\s*=\s*"([^"]+)"`)
	versionGoMatch := versionGoRe.FindSubmatch(versionGoContent)
	if versionGoMatch == nil {
		return CheckResult{
			Name:    "Version sync",
			Passed:  false,
			Skipped: true,
			Output:  "Cannot parse version from version.go",
			Command: command,
		}
	}
	goVersion := string(versionGoMatch[1])

	// Read default.nix
	nixContent, err := os.ReadFile("default.nix")
	if err != nil {
		// No nix file = skip version check (not an error)
		return CheckResult{
			Name:    "Version sync",
			Passed:  true,
			Skipped: true,
			Output:  "default.nix not found (skipping nix version check)",
			Command: command,
		}
	}

	// Extract version from default.nix
	// Pattern: version = "X.Y.Z";
	nixRe := regexp.MustCompile(`version\s*=\s*"([^"]+)"`)
	nixMatch := nixRe.FindSubmatch(nixContent)
	if nixMatch == nil {
		return CheckResult{
			Name:    "Version sync",
			Passed:  false,
			Skipped: true,
			Output:  "Cannot parse version from default.nix",
			Command: command,
		}
	}
	nixVersion := string(nixMatch[1])

	if goVersion != nixVersion {
		return CheckResult{
			Name:    "Version sync",
			Passed:  false,
			Output:  fmt.Sprintf("Version mismatch: version.go=%s, default.nix=%s", goVersion, nixVersion),
			Command: command,
		}
	}

	return CheckResult{
		Name:    "Version sync",
		Passed:  true,
		Output:  fmt.Sprintf("Versions match: %s", goVersion),
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
