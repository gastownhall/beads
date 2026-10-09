package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

// LargeMoleculeThreshold is the step count above which we show summary instead of full list.
// This prevents overwhelming output and slow queries for mega-molecules.
const LargeMoleculeThreshold = 100

// MoleculeProgress holds the progress information for a molecule
type MoleculeProgress struct {
	MoleculeID    string        `json:"molecule_id"`
	MoleculeTitle string        `json:"molecule_title"`
	Assignee      string        `json:"assignee,omitempty"`
	CurrentStep   *types.Issue  `json:"current_step,omitempty"`
	NextStep      *types.Issue  `json:"next_step,omitempty"`
	Steps         []*StepStatus `json:"steps"`
	Completed     int           `json:"completed"`
	Total         int           `json:"total"`
}

// StepStatus represents the status of a step in a molecule
type StepStatus struct {
	Issue     *types.Issue `json:"issue"`
	Status    string       `json:"status"`     // "done", "current", "ready", "blocked", "pending"
	IsCurrent bool         `json:"is_current"` // true if this is the in_progress step
}

var molCurrentCmd = &cobra.Command{
	Use:   "current [molecule-id]",
	Short: "Show current position in molecule workflow",
	Long: `Show where you are in a molecule workflow.

If molecule-id is given, show status for that molecule.
If not given, infer from in_progress issues assigned to current agent.

The output shows all steps with status indicators:
  [done]     - Step is complete (closed)
  [current]  - Step is in_progress (you are here)
  [ready]    - Step is ready to start (unblocked)
  [blocked]  - Step is blocked by dependencies
  [pending]  - Step is waiting

For large molecules (>100 steps), a summary is shown instead.
Use --limit or --range to view specific steps:
  bd mol current <id> --limit 50       # Show first 50 steps
  bd mol current <id> --range 100-150  # Show steps 100-150`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("mol-current")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		forAgent, _ := cmd.Flags().GetString("for")
		limit, _ := cmd.Flags().GetInt("limit")
		rangeStr, _ := cmd.Flags().GetString("range")

		agent := forAgent
		if agent == "" {
			agent = currentActor()
		}

		if usesProxiedServer() {
			return runMolCurrentProxiedServer(rootCtx, args, agent, limit, rangeStr)
		}
		if store == nil {
			return HandleErrorRespectJSON("no database connection")
		}
		resolve := func(ctx context.Context, arg string) (string, error) {
			return utils.ResolvePartialID(ctx, store, arg)
		}
		return runMolCurrent(rootCtx, store, resolve, args, agent, limit, rangeStr)
	},
}

// runMolCurrent is `bd mol current` on every route. The molecule's steps,
// their states, the progress counts and the current and next step are the
// library's (issueops.ViewMolecule, ActiveMoleculeIDs, ReadMoleculeProgress);
// what is here is the argument handling and the presentation.
func runMolCurrent(ctx context.Context, s storage.DoltStorage, resolve func(context.Context, string) (string, error), args []string, agent string, limit int, rangeStr string) error {
	var rangeStart, rangeEnd int
	if rangeStr != "" {
		var err error
		rangeStart, rangeEnd, err = parseRange(rangeStr)
		if err != nil {
			return HandleErrorRespectJSON("invalid range '%s': %v", rangeStr, err)
		}
	}
	explicitSteps := limit > 0 || rangeStr != ""

	roles, err := currentMoleculeRoles(s)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	var molecules []*MoleculeProgress
	if len(args) == 1 {
		moleculeID, err := resolve(ctx, args[0])
		if err != nil {
			return HandleErrorRespectJSON("molecule '%s' not found", args[0])
		}

		if !explicitSteps && !jsonOutput {
			stats, err := issueops.ReadMoleculeProgress(ctx, roles.molecule, moleculeID)
			if err != nil {
				return HandleErrorRespectJSON("loading molecule: %v", err)
			}
			if stats.Total > LargeMoleculeThreshold {
				printLargeMoleculeSummary(stats)
				return nil
			}
		}

		view, err := issueops.ViewMolecule(ctx, roles.molecule, moleculeID)
		if err != nil {
			return HandleErrorRespectJSON("loading molecule: %v", err)
		}
		progress := moleculeProgressOf(view)
		if rangeStr != "" {
			progress.Steps = filterStepsByRange(progress.Steps, rangeStart, rangeEnd)
		} else if limit > 0 && len(progress.Steps) > limit {
			progress.Steps = progress.Steps[:limit]
		}
		molecules = append(molecules, progress)
	} else {
		ids, err := issueops.ActiveMoleculeIDs(ctx, roles.reader, roles.molecule, agent)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		molecules, err = viewMolecules(ctx, roles, ids)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if len(molecules) == 0 {
			if jsonOutput {
				return outputJSON([]interface{}{})
			}
			fmt.Printf("No molecules in progress")
			if agent != "" {
				fmt.Printf(" for %s", agent)
			}
			fmt.Println(".")
			fmt.Println("\nTo start work on a molecule:")
			fmt.Println("  bd mol wisp create <proto-id>  # Instantiate as ephemeral wisp")
			fmt.Println("  bd update <step-id> --claim  # Claim a step")
			return nil
		}
	}

	if jsonOutput {
		return outputJSON(molecules)
	}
	for i, mol := range molecules {
		if i > 0 {
			fmt.Println()
		}
		printMoleculeProgress(mol)
	}
	return nil
}

// printMoleculeProgress prints the progress in human-readable format
func printMoleculeProgress(mol *MoleculeProgress) {
	fmt.Printf("You're working on molecule %s\n", ui.RenderAccent(mol.MoleculeID))
	fmt.Printf("  %s\n", mol.MoleculeTitle)
	if mol.Assignee != "" {
		fmt.Printf("  Assigned to: %s\n", mol.Assignee)
	}
	fmt.Println()

	for _, step := range mol.Steps {
		statusIcon := getStatusIcon(step.Status)
		marker := ""
		if step.IsCurrent {
			marker = " <- YOU ARE HERE"
		}
		fmt.Printf("  %s %s: %s%s\n", statusIcon, step.Issue.ID, step.Issue.Title, marker)
	}

	fmt.Println()
	fmt.Printf("Progress: %d/%d steps complete\n", mol.Completed, mol.Total)

	if mol.NextStep != nil && mol.CurrentStep == nil {
		fmt.Printf("\nNext ready: %s - %s\n", mol.NextStep.ID, mol.NextStep.Title)
		fmt.Printf("  Start with: bd update %s --claim\n", mol.NextStep.ID)
	}

	// Show hint about viewing step instructions
	var hintStepID string
	if mol.CurrentStep != nil {
		hintStepID = mol.CurrentStep.ID
	} else if mol.NextStep != nil {
		hintStepID = mol.NextStep.ID
	}
	if hintStepID != "" {
		fmt.Printf("\n%s Run `bd show %s` to see detailed instructions.\n", ui.RenderAccent("💡"), hintStepID)
	}
}

// getStatusIcon returns the icon for a step status
func getStatusIcon(status string) string {
	switch status {
	case "done":
		return ui.RenderPass("[done]")
	case "current":
		return ui.RenderWarn("[current]")
	case "ready":
		return ui.RenderAccent("[ready]")
	case "blocked":
		return ui.RenderFail("[blocked]")
	default:
		return "[pending]"
	}
}

// ContinueResult holds the result of advancing to the next molecule step
type ContinueResult struct {
	ClosedStep   *types.Issue `json:"closed_step"`
	NextStep     *types.Issue `json:"next_step,omitempty"`
	AutoAdvanced bool         `json:"auto_advanced"`
	MolComplete  bool         `json:"molecule_complete"`
	MoleculeID   string       `json:"molecule_id,omitempty"`
}

// PrintContinueResult prints the result of advancing to the next step
func PrintContinueResult(result *ContinueResult) {
	if result == nil {
		return
	}

	if result.MolComplete {
		fmt.Printf("\n%s Molecule %s complete! All steps closed.\n", ui.RenderPass("✓"), result.MoleculeID)
		fmt.Println("Consider: bd mol squash " + result.MoleculeID + " --summary '...'")
		return
	}

	if result.NextStep == nil {
		fmt.Println("\nNo ready steps in molecule (may be blocked).")
		return
	}

	fmt.Printf("\nNext ready in molecule:\n")
	fmt.Printf("  %s: %s\n", result.NextStep.ID, result.NextStep.Title)

	if result.AutoAdvanced {
		fmt.Printf("\n%s Marked in_progress (use --no-auto to skip)\n", ui.RenderWarn("→"))
	} else {
		fmt.Printf("\nStart with: bd update %s --claim\n", result.NextStep.ID)
	}
}

// parseRange parses a range string like "1-50" or "100-150" into start and end indices.
// Returns 1-based indices (start=1 means first step).
func parseRange(rangeStr string) (start, end int, err error) {
	parts := strings.Split(rangeStr, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected format 'start-end' (e.g., '1-50')")
	}
	start, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start: %w", err)
	}
	end, err = strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid end: %w", err)
	}
	if start < 1 {
		return 0, 0, fmt.Errorf("start must be >= 1")
	}
	if end < start {
		return 0, 0, fmt.Errorf("end must be >= start")
	}
	return start, end, nil
}

// filterStepsByRange filters steps to a 1-based range [start, end].
func filterStepsByRange(steps []*StepStatus, start, end int) []*StepStatus {
	// Convert to 0-based indices
	startIdx := start - 1
	endIdx := end

	if startIdx >= len(steps) {
		return nil
	}
	if endIdx > len(steps) {
		endIdx = len(steps)
	}
	return steps[startIdx:endIdx]
}

// printLargeMoleculeSummary prints a summary for molecules with many steps.
func printLargeMoleculeSummary(stats *types.MoleculeProgressStats) {
	fmt.Printf("Molecule: %s\n", ui.RenderAccent(stats.MoleculeID))
	fmt.Printf("  %s\n", stats.MoleculeTitle)
	fmt.Println()

	// Progress summary
	var percent float64
	if stats.Total > 0 {
		percent = float64(stats.Completed) * 100 / float64(stats.Total)
	}
	fmt.Printf("Progress: %d / %d steps (%.1f%%)\n", stats.Completed, stats.Total, percent)

	if stats.CurrentStepID != "" {
		fmt.Printf("Current step: %s\n", stats.CurrentStepID)
	} else if stats.InProgress > 0 {
		fmt.Printf("In progress: %d step(s)\n", stats.InProgress)
	}

	fmt.Println()
	fmt.Printf("%s This molecule has %d steps (threshold: %d).\n",
		ui.RenderWarn("Note:"), stats.Total, LargeMoleculeThreshold)
	fmt.Println("To view steps, use one of:")
	fmt.Printf("  bd mol current %s --limit 50        # First 50 steps\n", stats.MoleculeID)
	fmt.Printf("  bd mol current %s --range 1-50     # Steps 1-50\n", stats.MoleculeID)
	fmt.Printf("  bd mol progress %s                 # Efficient progress summary\n", stats.MoleculeID)

	// Show hint about viewing step instructions
	if stats.CurrentStepID != "" {
		fmt.Printf("\n%s Run `bd show %s` to see detailed instructions.\n", ui.RenderAccent("💡"), stats.CurrentStepID)
	}
}

func init() {
	molCurrentCmd.Flags().String("for", "", "Show molecules for a specific agent/assignee")
	molCurrentCmd.Flags().Int("limit", 0, "Maximum number of steps to display (0 = auto, use 'all' threshold)")
	molCurrentCmd.Flags().String("range", "", "Display specific step range (e.g., '1-50', '100-150')")
	molCmd.AddCommand(molCurrentCmd)
}
