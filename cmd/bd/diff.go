package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/ui"
)

var diffCmd = &cobra.Command{
	Use:     "diff <from-ref> <to-ref>",
	GroupID: "views",
	Short:   "Show changes between two commits or branches (requires Dolt backend)",
	Long: `Show the differences in issues between two commits or branches.

This command requires the Dolt storage backend. The refs can be:
- Commit hashes (e.g., abc123def)
- Branch names (e.g., main, feature-branch)
- Special refs like HEAD, HEAD~1

Examples:
  bd diff main feature-branch   # Compare main to feature branch
  bd diff HEAD~5 HEAD           # Show changes in last 5 commits
  bd diff abc123 def456         # Compare two specific commits`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx
		fromRef := args[0]
		toRef := args[1]

		// Check if storage supports versioning
		vs, ok := storage.AsVersioned(store)
		if !ok {
			FatalErrorRespectJSON("diff requires Dolt backend (current backend does not support versioning)")
		}

		// Get diff between refs
		entries, err := vs.Diff(ctx, fromRef, toRef)
		if err != nil {
			FatalErrorRespectJSON("failed to get diff: %v", err)
		}

		if len(entries) == 0 {
			fmt.Printf("No changes between %s and %s\n", fromRef, toRef)
			return
		}

		if jsonOutput {
			outputJSON(entries)
			return
		}

		// Display diff in human-readable format
		fmt.Printf("\n%s Changes from %s to %s (%d issues affected)\n\n",
			ui.RenderAccent("📊"),
			ui.RenderMuted(fromRef),
			ui.RenderMuted(toRef),
			len(entries))

		// Group by diff type
		var added, modified, removed []*storage.DiffEntry
		for _, entry := range entries {
			switch entry.DiffType {
			case "added":
				added = append(added, entry)
			case "modified":
				modified = append(modified, entry)
			case "removed":
				removed = append(removed, entry)
			}
		}

		// Display added issues
		if len(added) > 0 {
			fmt.Printf("%s Added (%d):\n", ui.RenderAccent("+"), len(added))
			for _, entry := range added {
				if entry.NewValue != nil {
					fmt.Printf("  + %s: %s\n",
						ui.StatusOpenStyle.Render(entry.IssueID),
						entry.NewValue.Title)
				} else {
					fmt.Printf("  + %s\n", ui.StatusOpenStyle.Render(entry.IssueID))
				}
			}
			fmt.Println()
		}

		// Display modified issues
		if len(modified) > 0 {
			fmt.Printf("%s Modified (%d):\n", ui.RenderAccent("~"), len(modified))
			for _, entry := range modified {
				fmt.Printf("  ~ %s", ui.StatusInProgressStyle.Render(entry.IssueID))
				if entry.OldValue != nil && entry.NewValue != nil {
					// Show what changed
					changes := []string{}
					if entry.OldValue.Title != entry.NewValue.Title {
						changes = append(changes, "title")
					}
					if entry.OldValue.Status != entry.NewValue.Status {
						changes = append(changes, fmt.Sprintf("status: %s -> %s",
							entry.OldValue.Status, entry.NewValue.Status))
					}
					if entry.OldValue.Priority != entry.NewValue.Priority {
						changes = append(changes, fmt.Sprintf("priority: P%d -> P%d",
							entry.OldValue.Priority, entry.NewValue.Priority))
					}
					if entry.OldValue.Description != entry.NewValue.Description {
						changes = append(changes, "description")
					}
					if len(changes) > 0 {
						fmt.Printf(" (%s)", ui.RenderMuted(joinStrings(changes, ", ")))
					}
				}
				fmt.Println()
			}
			fmt.Println()
		}

		// Display removed issues
		if len(removed) > 0 {
			fmt.Printf("%s Removed (%d):\n", ui.RenderAccent("-"), len(removed))
			for _, entry := range removed {
				if entry.OldValue != nil {
					fmt.Printf("  - %s: %s\n",
						ui.RenderMuted(entry.IssueID),
						ui.RenderMuted(entry.OldValue.Title))
				} else {
					fmt.Printf("  - %s\n", ui.RenderMuted(entry.IssueID))
				}
			}
			fmt.Println()
		}
	},
}

// joinStrings joins strings with a separator (simple helper to avoid importing strings)
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}

func init() {
	rootCmd.AddCommand(diffCmd)
}
