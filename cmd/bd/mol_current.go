package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// LargeMoleculeThreshold is the step count above which we show summary instead of full list.
// This prevents overwhelming output and slow queries for mega-molecules.
const LargeMoleculeThreshold = 100

// MoleculeProgress holds the progress information for a molecule
type MoleculeProgress struct {
	MoleculeID    string         `json:"molecule_id"`
	MoleculeTitle string         `json:"molecule_title"`
	Assignee      string         `json:"assignee,omitempty"`
	CurrentStep   *types.Issue   `json:"current_step,omitempty"`
	NextStep      *types.Issue   `json:"next_step,omitempty"`
	Steps         []*StepStatus  `json:"steps"`
	Completed     int            `json:"completed"`
	Total         int            `json:"total"`
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
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		forAgent, _ := cmd.Flags().GetString("for")
		limit, _ := cmd.Flags().GetInt("limit")
		rangeStr, _ := cmd.Flags().GetString("range")

		// Determine who we're looking for
		agent := forAgent
		if agent == "" {
			agent = actor // Default to current user/agent
		}

		// mol current requires direct store access for subgraph loading
		if store == nil {
			if daemonClient != nil {
				fmt.Fprintf(os.Stderr, "Error: mol current requires direct database access\n")
				fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon mol current\n")
			} else {
				fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			}
			os.Exit(1)
		}

		// Parse range flag if provided
		var rangeStart, rangeEnd int
		if rangeStr != "" {
			var err error
			rangeStart, rangeEnd, err = parseRange(rangeStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid range '%s': %v\n", rangeStr, err)
				os.Exit(1)
			}
		}

		// Determine if user explicitly requested steps
		explicitSteps := limit > 0 || rangeStr != ""

		var molecules []*MoleculeProgress

		if len(args) == 1 {
			// Explicit molecule ID given
			moleculeID, err := utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: molecule '%s' not found\n", args[0])
				os.Exit(1)
			}

			// Check child count first for large molecule detection
			stats, err := store.GetMoleculeProgress(ctx, moleculeID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
				os.Exit(1)
			}

			// If large molecule and no explicit flags, show summary
			if stats.Total > LargeMoleculeThreshold && !explicitSteps && !jsonOutput {
				printLargeMoleculeSummary(stats)
				return
			}

			progress, err := getMoleculeProgress(ctx, store, moleculeID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error loading molecule: %v\n", err)
				os.Exit(1)
			}

			// Apply limit or range filtering
			if rangeStr != "" {
				progress.Steps = filterStepsByRange(progress.Steps, rangeStart, rangeEnd)
			} else if limit > 0 && len(progress.Steps) > limit {
				progress.Steps = progress.Steps[:limit]
			}

			molecules = append(molecules, progress)
		} else {
			// Infer from in_progress issues
			molecules = findInProgressMolecules(ctx, store, agent)
			if len(molecules) == 0 {
				if jsonOutput {
					outputJSON([]interface{}{})
					return
				}
				fmt.Printf("No molecules in progress")
				if agent != "" {
					fmt.Printf(" for %s", agent)
				}
				fmt.Println(".")
				fmt.Println("\nTo start work on a molecule:")
				fmt.Println("  bd mol pour <proto-id>      # Instantiate a molecule from template")
				fmt.Println("  bd update <step-id> --status in_progress  # Claim a step")
				return
			}
		}

		if jsonOutput {
			outputJSON(molecules)
			return
		}

		// Human-readable output
		for i, mol := range molecules {
			if i > 0 {
				fmt.Println()
			}
			printMoleculeProgress(mol)
		}
	},
}

// getMoleculeProgress loads a molecule and computes progress
func getMoleculeProgress(ctx context.Context, s storage.Storage, moleculeID string) (*MoleculeProgress, error) {
	subgraph, err := loadTemplateSubgraph(ctx, s, moleculeID)
	if err != nil {
		return nil, err
	}

	progress := &MoleculeProgress{
		MoleculeID:    subgraph.Root.ID,
		MoleculeTitle: subgraph.Root.Title,
		Assignee:      subgraph.Root.Assignee,
		Total:         len(subgraph.Issues) - 1, // Exclude root
	}

	// Get ready issues for this molecule
	readyIDs := make(map[string]bool)
	readyIssues, err := s.GetReadyWork(ctx, types.WorkFilter{})
	if err == nil {
		for _, issue := range readyIssues {
			readyIDs[issue.ID] = true
		}
	}

	// Build step status list (exclude root)
	var steps []*StepStatus
	for _, issue := range subgraph.Issues {
		if issue.ID == subgraph.Root.ID {
			continue // Skip root
		}

		step := &StepStatus{
			Issue: issue,
		}

		switch issue.Status {
		case types.StatusClosed:
			step.Status = "done"
			progress.Completed++
		case types.StatusInProgress:
			step.Status = "current"
			step.IsCurrent = true
			progress.CurrentStep = issue
		case types.StatusBlocked:
			step.Status = "blocked"
		default:
			// Check if ready (unblocked)
			if readyIDs[issue.ID] {
				step.Status = "ready"
				if progress.NextStep == nil {
					progress.NextStep = issue
				}
			} else {
				step.Status = "pending"
			}
		}

		steps = append(steps, step)
	}

	// Sort steps by dependency order
	sortStepsByDependencyOrder(steps, subgraph)
	progress.Steps = steps

	// If no current step but there's a ready step, set it as next
	if progress.CurrentStep == nil && progress.NextStep == nil {
		for _, step := range steps {
			if step.Status == "ready" {
				progress.NextStep = step.Issue
				break
			}
		}
	}

	return progress, nil
}

// findInProgressMolecules finds molecules with in_progress steps for an agent
func findInProgressMolecules(ctx context.Context, s storage.Storage, agent string) []*MoleculeProgress {
	// Query for in_progress issues
	var inProgressIssues []*types.Issue

	if daemonClient != nil {
		listArgs := &rpc.ListArgs{
			Status:   "in_progress",
			Assignee: agent,
		}
		resp, err := daemonClient.List(listArgs)
		if err == nil {
			_ = json.Unmarshal(resp.Data, &inProgressIssues)
		}
	} else {
		// Direct query - search for in_progress issues
		status := types.StatusInProgress
		filter := types.IssueFilter{Status: &status}
		if agent != "" {
			filter.Assignee = &agent
		}
		allIssues, err := s.SearchIssues(ctx, "", filter)
		if err == nil {
			inProgressIssues = allIssues
		}
	}

	if len(inProgressIssues) == 0 {
		return nil
	}

	// For each in_progress issue, find its parent molecule
	moleculeMap := make(map[string]*MoleculeProgress)
	for _, issue := range inProgressIssues {
		moleculeID := findParentMolecule(ctx, s, issue.ID)
		if moleculeID == "" {
			// Not part of a molecule, skip
			continue
		}

		if _, exists := moleculeMap[moleculeID]; !exists {
			progress, err := getMoleculeProgress(ctx, s, moleculeID)
			if err == nil {
				moleculeMap[moleculeID] = progress
			}
		}
	}

	// Convert to slice
	var molecules []*MoleculeProgress
	for _, mol := range moleculeMap {
		molecules = append(molecules, mol)
	}

	// Sort by molecule ID for consistent output
	sort.Slice(molecules, func(i, j int) bool {
		return molecules[i].MoleculeID < molecules[j].MoleculeID
	})

	return molecules
}

// findParentMolecule walks up parent-child chain to find the root molecule
func findParentMolecule(ctx context.Context, s storage.Storage, issueID string) string {
	visited := make(map[string]bool)
	currentID := issueID

	for !visited[currentID] {
		visited[currentID] = true

		// Get dependencies for current issue
		deps, err := s.GetDependencyRecords(ctx, currentID)
		if err != nil {
			return ""
		}

		// Find parent-child dependency where current is the child
		var parentID string
		for _, dep := range deps {
			if dep.Type == types.DepParentChild && dep.IssueID == currentID {
				parentID = dep.DependsOnID
				break
			}
		}

		if parentID == "" {
			// No parent - check if current issue is a molecule root
			issue, err := s.GetIssue(ctx, currentID)
			if err != nil || issue == nil {
				return ""
			}
			// Check if it has the template label (molecules are spawned from templates)
			for _, label := range issue.Labels {
				if label == BeadsTemplateLabel {
					return currentID
				}
			}
			// Also check if it's an epic with children (ad-hoc molecule)
			if issue.IssueType == types.TypeEpic {
				return currentID
			}
			return ""
		}

		currentID = parentID
	}

	return ""
}

// sortStepsByDependencyOrder sorts steps by their dependency order
func sortStepsByDependencyOrder(steps []*StepStatus, subgraph *TemplateSubgraph) {
	// Build dependency graph
	depCount := make(map[string]int) // issue ID -> number of deps
	for _, step := range steps {
		depCount[step.Issue.ID] = 0
	}

	// Count blocking dependencies within the step set
	stepIDs := make(map[string]bool)
	for _, step := range steps {
		stepIDs[step.Issue.ID] = true
	}

	for _, dep := range subgraph.Dependencies {
		if dep.Type == types.DepBlocks && stepIDs[dep.IssueID] && stepIDs[dep.DependsOnID] {
			depCount[dep.IssueID]++
		}
	}

	// Stable sort by dependency count (fewer deps first)
	sort.SliceStable(steps, func(i, j int) bool {
		return depCount[steps[i].Issue.ID] < depCount[steps[j].Issue.ID]
	})
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
		fmt.Printf("  Start with: bd update %s --status in_progress\n", mol.NextStep.ID)
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

// AdvanceToNextStep finds the next ready step in a molecule after closing a step
// If autoClaim is true, it marks the next step as in_progress
// Returns nil if the issue is not part of a molecule
func AdvanceToNextStep(ctx context.Context, s storage.Storage, closedStepID string, autoClaim bool, actorName string) (*ContinueResult, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Get the closed step
	closedStep, err := s.GetIssue(ctx, closedStepID)
	if err != nil || closedStep == nil {
		return nil, fmt.Errorf("could not get closed step: %w", err)
	}

	result := &ContinueResult{
		ClosedStep: closedStep,
	}

	// Find parent molecule
	moleculeID := findParentMolecule(ctx, s, closedStepID)
	if moleculeID == "" {
		// Not part of a molecule - nothing to advance
		return nil, nil
	}
	result.MoleculeID = moleculeID

	// Load molecule progress
	progress, err := getMoleculeProgress(ctx, s, moleculeID)
	if err != nil {
		return nil, fmt.Errorf("could not load molecule: %w", err)
	}

	// Check if molecule is complete
	if progress.Completed >= progress.Total {
		result.MolComplete = true
		return result, nil
	}

	// Find next ready step
	var nextStep *types.Issue
	for _, step := range progress.Steps {
		if step.Status == "ready" {
			nextStep = step.Issue
			break
		}
	}

	if nextStep == nil {
		// No ready steps - might be blocked
		return result, nil
	}

	result.NextStep = nextStep

	// Auto-claim if requested
	if autoClaim {
		updates := map[string]interface{}{
			"status": types.StatusInProgress,
		}
		if err := s.UpdateIssue(ctx, nextStep.ID, updates, actorName); err != nil {
			return result, fmt.Errorf("could not claim next step: %w", err)
		}
		result.AutoAdvanced = true
	}

	return result, nil
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
		fmt.Printf("\nStart with: bd update %s --status in_progress\n", result.NextStep.ID)
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
}

func init() {
	molCurrentCmd.Flags().String("for", "", "Show molecules for a specific agent/assignee")
	molCurrentCmd.Flags().Int("limit", 0, "Maximum number of steps to display (0 = auto, use 'all' threshold)")
	molCurrentCmd.Flags().String("range", "", "Display specific step range (e.g., '1-50', '100-150')")
	molCmd.AddCommand(molCurrentCmd)
}
