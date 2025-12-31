package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

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

Phase 1 shows a static checklist. Future phases will add:
- --check: Run checks automatically
- --fix: Auto-fix where possible

Examples:
  bd preflight              # Show checklist
  bd preflight --check      # (future) Run checks automatically
  bd preflight --fix        # (future) Auto-fix where possible
`,
	Run: runPreflight,
}

func init() {
	// Future flags (documented but not yet implemented)
	preflightCmd.Flags().Bool("check", false, "Run checks automatically (not yet implemented)")
	preflightCmd.Flags().Bool("fix", false, "Auto-fix issues where possible (not yet implemented)")

	rootCmd.AddCommand(preflightCmd)
}

func runPreflight(cmd *cobra.Command, args []string) {
	// Check for future flags
	check, _ := cmd.Flags().GetBool("check")
	fix, _ := cmd.Flags().GetBool("fix")

	if check || fix {
		fmt.Println("Note: --check and --fix are not yet implemented.")
		fmt.Println("See bd-lfak.2 through bd-lfak.5 for implementation roadmap.")
		fmt.Println()
	}

	fmt.Println("PR Readiness Checklist:")
	fmt.Println()
	fmt.Println("[ ] Tests pass: go test -short ./...")
	fmt.Println("[ ] Lint passes: golangci-lint run ./...")
	fmt.Println("[ ] No beads pollution: check .beads/issues.jsonl diff")
	fmt.Println("[ ] Nix hash current: go.sum unchanged or vendorHash updated")
	fmt.Println("[ ] Version sync: version.go matches default.nix")
	fmt.Println()
	fmt.Println("Run 'bd preflight --check' to validate automatically (coming soon).")
}
