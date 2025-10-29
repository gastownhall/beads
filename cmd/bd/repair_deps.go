package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
)

var repairDepsCmd = &cobra.Command{
	Use:   "repair-deps",
	Short: "Find and fix orphaned dependency references",
	Long: `Find issues that reference non-existent dependencies and optionally remove them.

This command scans all issues for dependency references (both blocks and related-to)
that point to issues that no longer exist in the database.

Example:
  bd repair-deps             # Show orphaned dependencies
  bd repair-deps --fix       # Remove orphaned references
  bd repair-deps --json      # Output in JSON format`,
	Run: func(cmd *cobra.Command, _ []string) {
		// Check daemon mode - not supported yet (uses direct storage access)
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: repair-deps command not yet supported in daemon mode\n")
			fmt.Fprintf(os.Stderr, "Use: bd --no-daemon repair-deps\n")
			os.Exit(1)
		}

		fix, _ := cmd.Flags().GetBool("fix")

		ctx := context.Background()

		// Get all issues
		allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching issues: %v\n", err)
			os.Exit(1)
		}

		// Build ID existence map
		existingIDs := make(map[string]bool)
		for _, issue := range allIssues {
			existingIDs[issue.ID] = true
		}

		// Find orphaned dependencies
		type orphanedDep struct {
			IssueID    string
			OrphanedID string
			DepType    string
		}
		
		var orphaned []orphanedDep

		for _, issue := range allIssues {
			// Check dependencies
			for _, dep := range issue.Dependencies {
				if !existingIDs[dep.DependsOnID] {
					orphaned = append(orphaned, orphanedDep{
						IssueID:    issue.ID,
						OrphanedID: dep.DependsOnID,
						DepType:    string(dep.Type),
					})
				}
			}
		}

		// Output results
		if jsonOutput {
			result := map[string]interface{}{
				"orphaned_count": len(orphaned),
				"fixed":          fix,
				"orphaned_deps":  []map[string]interface{}{},
			}

			for _, o := range orphaned {
				result["orphaned_deps"] = append(result["orphaned_deps"].([]map[string]interface{}), map[string]interface{}{
					"issue_id":     o.IssueID,
					"orphaned_id":  o.OrphanedID,
					"dep_type":     o.DepType,
				})
			}

			outputJSON(result)
			return
		}

		// Human-readable output
		if len(orphaned) == 0 {
			fmt.Println("No orphaned dependencies found!")
			return
		}

		fmt.Printf("Found %d orphaned dependencies:\n\n", len(orphaned))
		for _, o := range orphaned {
			fmt.Printf("  %s: depends on %s (%s) - DELETED\n", o.IssueID, o.OrphanedID, o.DepType)
		}

		if !fix {
			fmt.Printf("\nRun 'bd repair-deps --fix' to remove these references.\n")
			return
		}

		// Fix orphaned dependencies
		fmt.Printf("\nRemoving orphaned dependencies...\n")
		
		// Group by issue for efficient updates
		orphansByIssue := make(map[string][]string)
		for _, o := range orphaned {
			orphansByIssue[o.IssueID] = append(orphansByIssue[o.IssueID], o.OrphanedID)
		}

		fixed := 0
		for issueID, orphanedIDs := range orphansByIssue {
			// Get current issue to verify
			issue, err := store.GetIssue(ctx, issueID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", issueID, err)
				continue
			}

			// Collect orphaned dependency IDs to remove
			orphanedSet := make(map[string]bool)
			for _, orphanedID := range orphanedIDs {
				orphanedSet[orphanedID] = true
			}

			// Build list of dependencies to keep
			validDeps := []*types.Dependency{}
			for _, dep := range issue.Dependencies {
				if !orphanedSet[dep.DependsOnID] {
					validDeps = append(validDeps, dep)
				}
			}

			// Update via storage layer
			// We need to remove each orphaned dependency individually
			for _, orphanedID := range orphanedIDs {
				if err := store.RemoveDependency(ctx, issueID, orphanedID, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Error removing %s from %s: %v\n", orphanedID, issueID, err)
					continue
				}
				
				fmt.Printf("✓ Removed %s from %s dependencies\n", orphanedID, issueID)
				fixed++
			}
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		fmt.Printf("\nRepaired %d orphaned dependencies.\n", fixed)
	},
}

func init() {
	repairDepsCmd.Flags().Bool("fix", false, "Remove orphaned dependency references")
	rootCmd.AddCommand(repairDepsCmd)
}
