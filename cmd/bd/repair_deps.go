// Package main implements the bd CLI dependency repair command.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

var repairDepsCmd = &cobra.Command{
	Use:   "repair-deps",
	Short: "Find and fix orphaned dependency references",
	Long: `Scans all issues for dependencies pointing to non-existent issues.

Reports orphaned dependencies and optionally removes them with --fix.
Interactive mode with --interactive prompts for each orphan.`,
	Run: func(cmd *cobra.Command, args []string) {
		fix, _ := cmd.Flags().GetBool("fix")
		interactive, _ := cmd.Flags().GetBool("interactive")

		// If daemon is running but doesn't support this command, use direct storage
		if daemonClient != nil && store == nil {
			var err error
			store, err = sqlite.New(dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = store.Close() }()
		}

		ctx := context.Background()

		// Get all dependency records
		allDeps, err := store.GetAllDependencyRecords(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to get dependencies: %v\n", err)
			os.Exit(1)
		}

		// Get all issues to check existence
		issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to list issues: %v\n", err)
			os.Exit(1)
		}

		// Build set of valid issue IDs
		validIDs := make(map[string]bool)
		for _, issue := range issues {
			validIDs[issue.ID] = true
		}

		// Find orphaned dependencies
		type orphan struct {
			issueID     string
			dependsOnID string
			depType     types.DependencyType
		}
		var orphans []orphan

		for issueID, deps := range allDeps {
			if !validIDs[issueID] {
				// The issue itself doesn't exist, skip (will be cleaned up separately)
				continue
			}
			for _, dep := range deps {
				if !validIDs[dep.DependsOnID] {
					orphans = append(orphans, orphan{
						issueID:     dep.IssueID,
						dependsOnID: dep.DependsOnID,
						depType:     dep.Type,
					})
				}
			}
		}

		if jsonOutput {
			result := map[string]interface{}{
				"orphans_found": len(orphans),
				"orphans":       []map[string]string{},
			}
			if len(orphans) > 0 {
				orphanList := make([]map[string]string, len(orphans))
				for i, o := range orphans {
					orphanList[i] = map[string]string{
						"issue_id":      o.issueID,
						"depends_on_id": o.dependsOnID,
						"type":          string(o.depType),
					}
				}
				result["orphans"] = orphanList
			}
			if fix || interactive {
				result["fixed"] = len(orphans)
			}
			outputJSON(result)
			return
		}

		// Report results
		if len(orphans) == 0 {
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("\n%s No orphaned dependencies found\n\n", green("✓"))
			return
		}

		yellow := color.New(color.FgYellow).SprintFunc()
		fmt.Printf("\n%s Found %d orphaned dependencies:\n\n", yellow("⚠"), len(orphans))

		for i, o := range orphans {
			fmt.Printf("%d. %s → %s (%s) [%s does not exist]\n",
				i+1, o.issueID, o.dependsOnID, o.depType, o.dependsOnID)
		}
		fmt.Println()

		// Fix if requested
		if interactive {
			fixed := 0
			for _, o := range orphans {
				fmt.Printf("Remove dependency %s → %s (%s)? [y/N]: ", o.issueID, o.dependsOnID, o.depType)
				var response string
				_, _ = fmt.Scanln(&response)
				if response == "y" || response == "Y" {
					// Use direct SQL to remove orphaned dependencies
					// RemoveDependency tries to mark the depends_on issue as dirty, which fails for orphans
					db := store.UnderlyingDB()
					_, err := db.ExecContext(ctx, "DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?",
						o.issueID, o.dependsOnID)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Error removing dependency: %v\n", err)
					} else {
						// Mark the issue as dirty
						_, _ = db.ExecContext(ctx, "INSERT OR IGNORE INTO dirty_issues (issue_id) VALUES (?)", o.issueID)
						fixed++
					}
				}
			}
			markDirtyAndScheduleFlush()
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("\n%s Fixed %d orphaned dependencies\n\n", green("✓"), fixed)
		} else if fix {
			db := store.UnderlyingDB()
			for _, o := range orphans {
				// Use direct SQL to remove orphaned dependencies
				_, err := db.ExecContext(ctx, "DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?",
					o.issueID, o.dependsOnID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error removing dependency %s → %s: %v\n",
						o.issueID, o.dependsOnID, err)
				} else {
					// Mark the issue as dirty
					_, _ = db.ExecContext(ctx, "INSERT OR IGNORE INTO dirty_issues (issue_id) VALUES (?)", o.issueID)
				}
			}
			markDirtyAndScheduleFlush()
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("%s Fixed %d orphaned dependencies\n\n", green("✓"), len(orphans))
		} else {
			fmt.Printf("Run with --fix to automatically remove orphaned dependencies\n")
			fmt.Printf("Run with --interactive to review each dependency\n\n")
		}
	},
}

func init() {
	repairDepsCmd.Flags().Bool("fix", false, "Automatically remove orphaned dependencies")
	repairDepsCmd.Flags().Bool("interactive", false, "Interactively review each orphaned dependency")
	repairDepsCmd.Flags().BoolVar(&jsonOutput, "json", false, "Output repair results in JSON format")
	rootCmd.AddCommand(repairDepsCmd)
}
