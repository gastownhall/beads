package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
)

const hookMigrationApplyTrackingIssue = "https://github.com/steveyegge/beads/issues/2218"

var migrateHooksCmd = &cobra.Command{
	Use:   "hooks [path]",
	Short: "Plan git hook migration to marker-managed format",
	Long: `Analyze git hook files and sidecar artifacts for migration to marker-managed format.

This command is planning-only in phase 1. It does not modify hook files.

Examples:
  bd migrate hooks
  bd migrate hooks --dry-run
  bd migrate hooks --json`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requestedDryRun, _ := cmd.Flags().GetBool("dry-run")
		if err := validateHookMigrationDryRunRequested(requestedDryRun); err != nil {
			FatalErrorRespectJSON("%v", err)
		}

		targetPath := "."
		if len(args) == 1 {
			targetPath = args[0]
		}

		absPath, err := filepath.Abs(targetPath)
		if err != nil {
			FatalErrorRespectJSON("resolving path: %v", err)
		}

		plan, err := doctor.PlanHookMigration(absPath)
		if err != nil {
			FatalErrorRespectJSON("building hook migration plan: %v", err)
		}

		if jsonOutput {
			outputJSON(buildHookMigrationJSON(plan, requestedDryRun))
			return
		}

		fmt.Println(strings.Join(formatHookMigrationPlan(plan), "\n"))
	},
}

func validateHookMigrationDryRunRequested(requestedDryRun bool) error {
	if requestedDryRun {
		return nil
	}
	return errors.New(
		"phase 1 is planning-only: --dry-run is required for 'bd migrate hooks'. " +
			"Apply mode is tracked in #2218 and will be enabled once the PR resolving #2218 is merged: " +
			hookMigrationApplyTrackingIssue,
	)
}

func buildHookMigrationJSON(plan doctor.HookMigrationPlan, requestedDryRun bool) map[string]interface{} {
	return map[string]interface{}{
		"status":            "planning_only",
		"planning_only":     true,
		"requested_dry_run": requestedDryRun,
		"dry_run":           true,
		"plan":              plan,
	}
}

func formatHookMigrationPlan(plan doctor.HookMigrationPlan) []string {
	lines := []string{
		"Hook migration plan (planning only)",
	}

	if !plan.IsGitRepo {
		lines = append(lines, fmt.Sprintf("Path: %s", plan.Path))
		lines = append(lines, "Result: not a git repository (no hook migration needed).")
		return lines
	}

	lines = append(lines,
		fmt.Sprintf("Repository: %s", plan.RepoRoot),
		fmt.Sprintf("Hooks dir: %s", plan.HooksDir),
		fmt.Sprintf("Needs migration: %d/%d", plan.NeedsMigrationCount, plan.TotalHooks),
	)

	if plan.BrokenMarkerCount > 0 {
		lines = append(lines, fmt.Sprintf("Broken markers detected: %d", plan.BrokenMarkerCount))
	}

	for _, hook := range plan.Hooks {
		decision := "no action"
		if hook.NeedsMigration {
			decision = "migrate"
		}

		lines = append(lines, fmt.Sprintf("- %s: %s [%s]", hook.Name, hook.State, decision))
		if hook.SuggestedAction != "" {
			lines = append(lines, fmt.Sprintf("  action: %s", hook.SuggestedAction))
		}
		if hook.ReadError != "" {
			lines = append(lines, fmt.Sprintf("  read_error: %s", hook.ReadError))
		}
	}

	if plan.NeedsMigrationCount > 0 {
		lines = append(lines, "Next: run 'bd migrate hooks --dry-run --json' for machine-readable planning output.")
	} else {
		lines = append(lines, "No hook migration is required.")
	}

	return lines
}
