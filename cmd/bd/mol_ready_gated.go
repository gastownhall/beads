package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// GatedMolecule represents a molecule ready for gate-resume dispatch
type GatedMolecule struct {
	MoleculeID    string       `json:"molecule_id"`
	MoleculeTitle string       `json:"molecule_title"`
	ClosedGate    *types.Issue `json:"closed_gate"`
	ReadyStep     *types.Issue `json:"ready_step"`
}

// GatedReadyOutput is the JSON output for bd mol ready --gated
type GatedReadyOutput struct {
	Molecules []*GatedMolecule `json:"molecules"`
	Count     int              `json:"count"`
}

var molReadyGatedCmd = &cobra.Command{
	Use:   "ready --gated",
	Short: "Find molecules ready for gate-resume dispatch",
	Long: `Find molecules where a gate has closed and the workflow is ready to resume.

This command discovers molecules waiting at a gate step where:
1. The molecule has a gate bead that blocks a step
2. The gate bead is now closed (condition satisfied)
3. The blocked step is now ready to proceed
4. No agent currently has this molecule hooked

This enables discovery-based resume without explicit waiter tracking.
The Deacon patrol uses this to find and dispatch gate-ready molecules.

Examples:
  bd mol ready --gated           # Find all gate-ready molecules
  bd mol ready --gated --json    # JSON output for automation`,
	Run: runMolReadyGated,
}

func runMolReadyGated(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	// --gated mode requires direct store access
	if store == nil {
		fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		os.Exit(1)
	}

	// Find gate-ready molecules
	molecules, err := findGateReadyMolecules(ctx, store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		output := GatedReadyOutput{
			Molecules: molecules,
			Count:     len(molecules),
		}
		if output.Molecules == nil {
			output.Molecules = []*GatedMolecule{}
		}
		outputJSON(output)
		return
	}

	// Human-readable output
	if len(molecules) == 0 {
		fmt.Printf("\n%s No molecules ready for gate-resume dispatch\n\n", ui.RenderWarn(""))
		return
	}

	fmt.Printf("\n%s Molecules ready for gate-resume dispatch (%d):\n\n",
		ui.RenderAccent(""), len(molecules))

	for i, mol := range molecules {
		fmt.Printf("%d. %s: %s\n", i+1, ui.RenderID(mol.MoleculeID), mol.MoleculeTitle)
		if mol.ClosedGate != nil {
			fmt.Printf("   Gate closed: %s (%s)\n", mol.ClosedGate.ID, mol.ClosedGate.AwaitType)
		}
		if mol.ReadyStep != nil {
			fmt.Printf("   Ready step: %s - %s\n", mol.ReadyStep.ID, mol.ReadyStep.Title)
		}
		fmt.Println()
	}

	fmt.Println("To dispatch a molecule:")
	fmt.Println("  gt sling <agent> --mol <molecule-id>")
}

// findGateReadyMolecules finds molecules where a gate has closed and work can resume.
//
// Logic:
// 1. Find all closed gate beads
// 2. For each closed gate, find what step it was blocking
// 3. Check if that step is now ready (unblocked)
// 4. Find the parent molecule
// 5. Filter out molecules that are already hooked by someone
func findGateReadyMolecules(ctx context.Context, s *dolt.DoltStore) ([]*GatedMolecule, error) {
	// Step 1: Find all closed gate beads
	gateType := types.IssueType("gate")
	closedStatus := types.StatusClosed
	gateFilter := types.IssueFilter{
		IssueType: &gateType,
		Status:    &closedStatus,
		Limit:     100,
	}

	closedGates, err := s.SearchIssues(ctx, "", gateFilter)
	if err != nil {
		return nil, fmt.Errorf("searching closed gates: %w", err)
	}

	if len(closedGates) == 0 {
		return nil, nil
	}

	// Step 2: Get ready work to check which steps are ready
	// IncludeMolSteps: true because we specifically need to see molecule steps here
	readyIssues, err := s.GetReadyWork(ctx, types.WorkFilter{Limit: 500, IncludeMolSteps: true})
	if err != nil {
		return nil, fmt.Errorf("getting ready work: %w", err)
	}
	readyIDs := make(map[string]bool)
	for _, issue := range readyIssues {
		readyIDs[issue.ID] = true
	}

	// Step 3: Get hooked molecules to filter out
	hookedStatus := types.StatusHooked
	hookedFilter := types.IssueFilter{
		Status: &hookedStatus,
		Limit:  100,
	}
	hookedIssues, err := s.SearchIssues(ctx, "", hookedFilter)
	if err != nil {
		// Non-fatal: just continue without filtering
		hookedIssues = nil
	}
	hookedMolecules := make(map[string]bool)
	for _, issue := range hookedIssues {
		// If the hooked issue is a molecule root, mark it
		hookedMolecules[issue.ID] = true
		// Also find parent molecule for hooked steps
		if parentMol := findParentMolecule(ctx, s, issue.ID); parentMol != "" {
			hookedMolecules[parentMol] = true
		}
	}

	// Step 4: For each closed gate, find issues that depend on it (were blocked)
	moleculeMap := make(map[string]*GatedMolecule)

	for _, gate := range closedGates {
		// Find issues that depend on this gate (GetDependents returns issues where depends_on_id = gate.ID)
		dependents, err := s.GetDependents(ctx, gate.ID)
		if err != nil {
			continue
		}

		for _, dependent := range dependents {
			// Check if the previously blocked step is now ready
			if !readyIDs[dependent.ID] {
				continue
			}

			// Find the parent molecule
			moleculeID := findParentMolecule(ctx, s, dependent.ID)
			if moleculeID == "" {
				continue
			}

			// Skip if already hooked
			if hookedMolecules[moleculeID] {
				continue
			}

			// Get molecule details
			moleculeIssue, err := s.GetIssue(ctx, moleculeID)
			if err != nil || moleculeIssue == nil {
				continue
			}

			// Add to results (dedupe by molecule ID)
			if _, exists := moleculeMap[moleculeID]; !exists {
				moleculeMap[moleculeID] = &GatedMolecule{
					MoleculeID:    moleculeID,
					MoleculeTitle: moleculeIssue.Title,
					ClosedGate:    gate,
					ReadyStep:     dependent,
				}
			}
		}
	}

	// Convert to slice and sort
	var molecules []*GatedMolecule
	for _, mol := range moleculeMap {
		molecules = append(molecules, mol)
	}
	sort.Slice(molecules, func(i, j int) bool {
		return molecules[i].MoleculeID < molecules[j].MoleculeID
	})

	return molecules, nil
}

func init() {
	// Note: --gated flag is registered in ready.go
	// Also add as a subcommand under mol for discoverability
	molCmd.AddCommand(molReadyGatedCmd)
}
