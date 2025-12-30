package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var molStaleCmd = &cobra.Command{
	Use:   "stale",
	Short: "Detect complete-but-unclosed molecules",
	Long: `Detect molecules (epics with children) that are complete but still open.

A molecule is considered stale if:
  1. All children are closed (Completed == Total)
  2. Root issue is still open
  3. Not assigned to anyone (optional, use --unassigned)
  4. Is blocking other work (optional, use --blocking)

By default, shows all complete-but-unclosed molecules.

Examples:
  bd mol stale              # List all stale molecules
  bd mol stale --json       # Machine-readable output
  bd mol stale --blocking   # Only show those blocking other work
  bd mol stale --unassigned # Only show unassigned molecules
  bd mol stale --all        # Include molecules with 0 children`,
	Run: runMolStale,
}

// StaleMolecule holds info about a stale molecule
type StaleMolecule struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	TotalChildren  int      `json:"total_children"`
	ClosedChildren int      `json:"closed_children"`
	Assignee       string   `json:"assignee,omitempty"`
	BlockingIssues []string `json:"blocking_issues,omitempty"`
	BlockingCount  int      `json:"blocking_count"`
}

// StaleResult holds the result of the stale check
type StaleResult struct {
	StaleMolecules []*StaleMolecule `json:"stale_molecules"`
	TotalCount     int              `json:"total_count"`
	BlockingCount  int              `json:"blocking_count"`
}

func runMolStale(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	blockingOnly, _ := cmd.Flags().GetBool("blocking")
	unassignedOnly, _ := cmd.Flags().GetBool("unassigned")
	showAll, _ := cmd.Flags().GetBool("all")

	// Get storage (direct or daemon)
	var result *StaleResult
	var err error

	if daemonClient != nil {
		// Daemon mode - use RPC to get stale molecules
		rpcResp, rpcErr := daemonClient.MolStale(&rpc.MolStaleArgs{
			BlockingOnly:   blockingOnly,
			UnassignedOnly: unassignedOnly,
			ShowAll:        showAll,
		})
		if rpcErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", rpcErr)
			os.Exit(1)
		}
		// Convert RPC response to local StaleResult
		result = &StaleResult{
			TotalCount:    rpcResp.TotalCount,
			BlockingCount: rpcResp.BlockingCount,
		}
		for _, mol := range rpcResp.StaleMolecules {
			result.StaleMolecules = append(result.StaleMolecules, &StaleMolecule{
				ID:             mol.ID,
				Title:          mol.Title,
				TotalChildren:  mol.TotalChildren,
				ClosedChildren: mol.ClosedChildren,
				Assignee:       mol.Assignee,
				BlockingIssues: mol.BlockingIssues,
				BlockingCount:  mol.BlockingCount,
			})
		}
	} else {
		if store == nil {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
			os.Exit(1)
		}

		result, err = findStaleMolecules(ctx, store, blockingOnly, unassignedOnly, showAll)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	if len(result.StaleMolecules) == 0 {
		fmt.Println("No stale molecules found.")
		return
	}

	// Print header
	if blockingOnly {
		fmt.Printf("%s Stale molecules (complete but unclosed, blocking work):\n\n",
			ui.RenderWarnIcon())
	} else {
		fmt.Printf("%s Stale molecules (complete but unclosed):\n\n",
			ui.RenderInfoIcon())
	}

	// Print each stale molecule
	for _, mol := range result.StaleMolecules {
		progress := fmt.Sprintf("%d/%d", mol.ClosedChildren, mol.TotalChildren)

		if mol.BlockingCount > 0 {
			fmt.Printf("  %s  %s  (%s) [blocking %d]\n",
				ui.RenderID(mol.ID), mol.Title, progress, mol.BlockingCount)
			fmt.Printf("       → Close with: bd close %s\n", mol.ID)
			if mol.BlockingCount <= 3 {
				fmt.Printf("       → Blocking: %v\n", mol.BlockingIssues)
			}
		} else {
			fmt.Printf("  %s  %s  (%s)\n",
				ui.RenderID(mol.ID), mol.Title, progress)
			fmt.Printf("       → Close with: bd close %s\n", mol.ID)
		}
		fmt.Println()
	}

	// Summary
	fmt.Printf("Total: %d stale", result.TotalCount)
	if result.BlockingCount > 0 {
		fmt.Printf(", %d blocking other work", result.BlockingCount)
	}
	fmt.Println()
}

// findStaleMolecules queries the database for stale molecules
func findStaleMolecules(ctx context.Context, s storage.Storage, blockingOnly, unassignedOnly, showAll bool) (*StaleResult, error) {
	// Get all epics eligible for closure (complete but unclosed)
	epicStatuses, err := s.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying epics: %w", err)
	}

	// Get blocked issues to find what each stale molecule is blocking
	blockedIssues, err := s.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		return nil, fmt.Errorf("querying blocked issues: %w", err)
	}

	// Build map of issue ID -> what issues it's blocking
	blockingMap := buildBlockingMap(blockedIssues)

	var staleMolecules []*StaleMolecule
	blockingCount := 0

	for _, es := range epicStatuses {
		// Skip if not eligible for close (not all children closed)
		if !es.EligibleForClose {
			continue
		}

		// Skip if no children and not showing all
		if es.TotalChildren == 0 && !showAll {
			continue
		}

		// Filter by unassigned if requested
		if unassignedOnly && es.Epic.Assignee != "" {
			continue
		}

		// Find what this molecule is blocking
		blocking := blockingMap[es.Epic.ID]
		blockingIssueCount := len(blocking)

		// Filter by blocking if requested
		if blockingOnly && blockingIssueCount == 0 {
			continue
		}

		mol := &StaleMolecule{
			ID:             es.Epic.ID,
			Title:          es.Epic.Title,
			TotalChildren:  es.TotalChildren,
			ClosedChildren: es.ClosedChildren,
			Assignee:       es.Epic.Assignee,
			BlockingIssues: blocking,
			BlockingCount:  blockingIssueCount,
		}

		staleMolecules = append(staleMolecules, mol)

		if blockingIssueCount > 0 {
			blockingCount++
		}
	}

	return &StaleResult{
		StaleMolecules: staleMolecules,
		TotalCount:     len(staleMolecules),
		BlockingCount:  blockingCount,
	}, nil
}

// buildBlockingMap creates a map of issue ID -> list of issues it's blocking
func buildBlockingMap(blockedIssues []*types.BlockedIssue) map[string][]string {
	result := make(map[string][]string)

	for _, blocked := range blockedIssues {
		// Each blocked issue has a list of what's blocking it
		for _, blockerID := range blocked.BlockedBy {
			result[blockerID] = append(result[blockerID], blocked.ID)
		}
	}

	return result
}

func init() {
	molStaleCmd.Flags().Bool("blocking", false, "Only show molecules blocking other work")
	molStaleCmd.Flags().Bool("unassigned", false, "Only show unassigned molecules")
	molStaleCmd.Flags().Bool("all", false, "Include molecules with 0 children")
	molStaleCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	molCmd.AddCommand(molStaleCmd)
}
