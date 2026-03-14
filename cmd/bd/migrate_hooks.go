package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/gastownhall/beads/cmd/bd/doctor"
	"golang.org/x/term"
)

var migrateHooksCmd = &cobra.Command{
	Use:   "hooks [path]",
	Short: "Plan or apply git hook migration to marker-managed format",
	Long: `Analyze git hook files and sidecar artifacts for migration to marker-managed format.

Modes:
  --dry-run  Preview migration operations without changing files
  --apply    Apply migration operations

Examples:
  bd migrate hooks --dry-run
  bd migrate hooks --apply
  bd migrate hooks --apply --yes
  bd migrate hooks --dry-run --json`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requestedDryRun, _ := cmd.Flags().GetBool("dry-run")
		requestedApply, _ := cmd.Flags().GetBool("apply")
		requestedYes, _ := cmd.Flags().GetBool("yes")

		mode, err := validateHookMigrationMode(requestedDryRun, requestedApply, requestedYes)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}

		if mode.RequestedApply {
			CheckReadonly("migrate hooks")
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

		execPlan := buildHookMigrationExecutionPlan(plan)

		if mode.RequestedApply {
			if len(execPlan.BlockingErrors) > 0 {
				FatalErrorRespectJSON("hook migration is blocked:\n- %s", strings.Join(execPlan.BlockingErrors, "\n- "))
			}
			if execPlan.operationCount() > 0 {
				if err := validateHookMigrationApplyConsent(mode.RequestedYes, term.IsTerminal(int(os.Stdin.Fd())), jsonOutput); err != nil {
					FatalErrorRespectJSON("%v", err)
				}
			}
		}

		if jsonOutput {
			if mode.RequestedApply {
				summary, applied, applyErr := maybeApplyHookMigration(execPlan, mode.RequestedYes)
				if applyErr != nil {
					FatalErrorRespectJSON("applying hook migration: %v", applyErr)
				}
				if !applied {
					summary.SkippedArtifacts = append(summary.SkippedArtifacts, "canceled")
					summary.SkippedCount = len(summary.SkippedArtifacts)
				}
				outputJSON(buildHookMigrationJSON(plan, mode, execPlan, &summary))
				return
			}
			outputJSON(buildHookMigrationJSON(plan, mode, execPlan, nil))
			return
		}

		fmt.Println(strings.Join(formatHookMigrationPlan(plan, mode), "\n"))
		fmt.Println()
		fmt.Println(strings.Join(formatHookMigrationOperations(execPlan), "\n"))

		if mode.RequestedDryRun {
			return
		}

		summary, applied, applyErr := maybeApplyHookMigration(execPlan, mode.RequestedYes)
		if applyErr != nil {
			FatalErrorRespectJSON("applying hook migration: %v", applyErr)
		}
		if !applied {
			fmt.Println()
			fmt.Println("Migration canceled.")
			return
		}

		for _, line := range formatHookMigrationApplySummary(summary) {
			fmt.Println(line)
		}
	},
}

func maybeApplyHookMigration(execPlan hookMigrationExecutionPlan, autoYes bool) (hookMigrationApplySummary, bool, error) {
	if execPlan.operationCount() == 0 {
		return hookMigrationApplySummary{}, true, nil
	}

	if !autoYes {
		confirmed, err := confirmHookMigrationApply(execPlan.operationCount())
		if err != nil {
			return hookMigrationApplySummary{}, false, err
		}
		if !confirmed {
			return hookMigrationApplySummary{}, false, nil
		}
	}

	summary, err := applyHookMigrationExecution(execPlan)
	return summary, err == nil, err
}

func buildHookMigrationJSON(plan doctor.HookMigrationPlan, mode hookMigrationMode, execPlan hookMigrationExecutionPlan, summary *hookMigrationApplySummary) map[string]interface{} {
	status := "preview"
	if mode.RequestedApply {
		status = "applied"
	}

	output := map[string]interface{}{
		"status":               status,
		"dry_run":              mode.RequestedDryRun,
		"apply":                mode.RequestedApply,
		"plan":                 plan,
		"operations":           execPlan.outputOperations(),
		"operation_count":      execPlan.operationCount(),
		"blocking_errors":      execPlan.BlockingErrors,
		"blocking_error_count": len(execPlan.BlockingErrors),
	}

	if summary != nil {
		output["result"] = summary
	}

	return output
}

func formatHookMigrationPlan(plan doctor.HookMigrationPlan, mode hookMigrationMode) []string {
	lines := []string{
		"Hook migration plan",
	}

	if mode.RequestedDryRun {
		lines = append(lines, "Mode: dry-run")
	} else if mode.RequestedApply {
		lines = append(lines, "Mode: apply")
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
		if mode.RequestedDryRun {
			lines = append(lines, "Next: run 'bd migrate hooks --apply' to execute this migration plan.")
		} else {
			lines = append(lines, "Applying migration operations...")
		}
	} else {
		lines = append(lines, "No hook migration is required.")
	}

	return lines
}

func formatHookMigrationOperations(execPlan hookMigrationExecutionPlan) []string {
	lines := []string{"Planned operations:"}
	if execPlan.operationCount() == 0 {
		lines = append(lines, "- none")
		return lines
	}

	for _, op := range execPlan.outputOperations() {
		switch op.Action {
		case "write_hook":
			source := op.SourcePath
			if source == "" {
				source = "<template>"
			}
			lines = append(lines, fmt.Sprintf("- write %s: %s (source: %s)", op.HookName, op.Path, source))
		case "retire_sidecar":
			lines = append(lines, fmt.Sprintf("- retire %s: %s -> %s", op.HookName, op.SourcePath, op.Destination))
		}
	}

	if len(execPlan.BlockingErrors) > 0 {
		lines = append(lines, "Blocking issues:")
		for _, blocking := range execPlan.BlockingErrors {
			lines = append(lines, "- "+blocking)
		}
	}

	return lines
}

func formatHookMigrationApplySummary(summary hookMigrationApplySummary) []string {
	lines := []string{
		"",
		"Hook migration apply summary",
		fmt.Sprintf("- hooks written: %d", summary.WrittenHookCount),
		fmt.Sprintf("- artifacts retired: %d", summary.RetiredCount),
		fmt.Sprintf("- artifacts skipped: %d", summary.SkippedCount),
	}

	if summary.WrittenHookCount > 0 {
		lines = append(lines, "Written hooks:")
		for _, hook := range summary.WrittenHooks {
			lines = append(lines, "- "+hook)
		}
	}

	if summary.RetiredCount > 0 {
		lines = append(lines, "Retired artifacts:")
		for _, retired := range summary.RetiredArtifacts {
			lines = append(lines, "- "+retired)
		}
	}

	if summary.SkippedCount > 0 {
		lines = append(lines, "Skipped artifacts:")
		for _, skipped := range summary.SkippedArtifacts {
			lines = append(lines, "- "+skipped)
		}
	}

	return lines
}
