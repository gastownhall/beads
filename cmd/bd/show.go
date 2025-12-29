package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var showCmd = &cobra.Command{
	Use:     "show [id...]",
	GroupID: "issues",
	Short:   "Show issue details",
	Args:    cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		showThread, _ := cmd.Flags().GetBool("thread")
		shortMode, _ := cmd.Flags().GetBool("short")
		ctx := rootCtx

		// Check database freshness before reading
		// Skip check when using daemon (daemon auto-imports on staleness)
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				FatalErrorRespectJSON("%v", err)
			}
		}

		// Resolve partial IDs first (daemon mode only - direct mode uses routed resolution)
		var resolvedIDs []string
		var routedArgs []string // IDs that need cross-repo routing (bypass daemon)
		if daemonClient != nil {
			// In daemon mode, resolve via RPC - but check routing first
			for _, id := range args {
				// Check if this ID needs routing to a different beads directory
				if needsRouting(id) {
					routedArgs = append(routedArgs, id)
					continue
				}
				resolveArgs := &rpc.ResolveIDArgs{ID: id}
				resp, err := daemonClient.ResolveID(resolveArgs)
				if err != nil {
					FatalErrorRespectJSON("resolving ID %s: %v", id, err)
				}
				var resolvedID string
				if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
					FatalErrorRespectJSON("unmarshaling resolved ID: %v", err)
				}
				resolvedIDs = append(resolvedIDs, resolvedID)
			}
		}
		// Note: Direct mode uses resolveAndGetIssueWithRouting for prefix-based routing

		// Handle --thread flag: show full conversation thread
		if showThread {
			if daemonClient != nil && len(resolvedIDs) > 0 {
				showMessageThread(ctx, resolvedIDs[0], jsonOutput)
				return
			} else if len(args) > 0 {
				// Direct mode - resolve first arg with routing
				result, err := resolveAndGetIssueWithRouting(ctx, store, args[0])
				if result != nil {
					defer result.Close()
				}
				if err == nil && result != nil && result.ResolvedID != "" {
					showMessageThread(ctx, result.ResolvedID, jsonOutput)
					return
				}
			}
		}

		// If daemon is running, use RPC (but fall back to direct mode for routed IDs)
		if daemonClient != nil {
			allDetails := []interface{}{}
			displayIdx := 0

			// First, handle routed IDs via direct mode
			for _, id := range routedArgs {
				result, err := resolveAndGetIssueWithRouting(ctx, store, id)
				if err != nil {
					if result != nil {
						result.Close()
					}
					fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id, err)
					continue
				}
				if result == nil || result.Issue == nil {
					if result != nil {
						result.Close()
					}
					fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
					continue
				}
				issue := result.Issue
				issueStore := result.Store
				if shortMode {
					fmt.Println(formatShortIssue(issue))
					result.Close()
					continue
				}
				if jsonOutput {
					// Get labels and deps for JSON output
					details := &types.IssueDetails{Issue: *issue}
					details.Labels, _ = issueStore.GetLabels(ctx, issue.ID)
					if sqliteStore, ok := issueStore.(*sqlite.SQLiteStorage); ok {
						details.Dependencies, _ = sqliteStore.GetDependenciesWithMetadata(ctx, issue.ID)
						details.Dependents, _ = sqliteStore.GetDependentsWithMetadata(ctx, issue.ID)
					}
					details.Comments, _ = issueStore.GetIssueComments(ctx, issue.ID)
					// Compute parent from dependencies
					for _, dep := range details.Dependencies {
						if dep.DependencyType == types.DepParentChild {
							details.Parent = &dep.ID
							break
						}
					}
					allDetails = append(allDetails, details)
				} else {
					if displayIdx > 0 {
						fmt.Println("\n" + strings.Repeat("─", 60))
					}
					fmt.Printf("\n%s: %s\n", ui.RenderAccent(issue.ID), issue.Title)
					fmt.Printf("Status: %s\n", issue.Status)
					fmt.Printf("Priority: P%d\n", issue.Priority)
					fmt.Printf("Type: %s\n", issue.IssueType)
					if issue.Description != "" {
						fmt.Printf("\nDescription:\n%s\n", issue.Description)
					}
					fmt.Println()
					displayIdx++
				}
				result.Close() // Close immediately after processing each routed ID
			}

			// Then, handle local IDs via daemon
			for _, id := range resolvedIDs {
				showArgs := &rpc.ShowArgs{ID: id}
				resp, err := daemonClient.Show(showArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var details types.IssueDetails
					if err := json.Unmarshal(resp.Data, &details); err == nil {
						// Compute parent from dependencies
						for _, dep := range details.Dependencies {
							if dep.DependencyType == types.DepParentChild {
								details.Parent = &dep.ID
								break
							}
						}
						allDetails = append(allDetails, details)
					}
				} else {
					// Check if issue exists (daemon returns null for non-existent issues)
					if string(resp.Data) == "null" || len(resp.Data) == 0 {
						fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
						continue
					}

					// Parse response first to check shortMode before output
					var details types.IssueDetails
					if err := json.Unmarshal(resp.Data, &details); err != nil {
						fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
						os.Exit(1)
					}
					issue := &details.Issue

					if shortMode {
						fmt.Println(formatShortIssue(issue))
						continue
					}

					if displayIdx > 0 {
						fmt.Println("\n" + strings.Repeat("─", 60))
					}
					displayIdx++

					// Format output (same as direct mode below)
					tierEmoji := ""
					statusSuffix := ""
					switch issue.CompactionLevel {
					case 1:
						tierEmoji = " 🗜️"
						statusSuffix = " (compacted L1)"
					case 2:
						tierEmoji = " 📦"
						statusSuffix = " (compacted L2)"
					}

					fmt.Printf("\n%s: %s%s\n", ui.RenderAccent(issue.ID), issue.Title, tierEmoji)
					fmt.Printf("Status: %s%s\n", issue.Status, statusSuffix)
					if issue.CloseReason != "" {
						fmt.Printf("Close reason: %s\n", issue.CloseReason)
					}
					fmt.Printf("Priority: P%d\n", issue.Priority)
					fmt.Printf("Type: %s\n", issue.IssueType)
					if issue.Assignee != "" {
						fmt.Printf("Assignee: %s\n", issue.Assignee)
					}
					if issue.EstimatedMinutes != nil {
						fmt.Printf("Estimated: %d minutes\n", *issue.EstimatedMinutes)
					}
					fmt.Printf("Created: %s\n", issue.CreatedAt.Format("2006-01-02 15:04"))
					if issue.CreatedBy != "" {
						fmt.Printf("Created by: %s\n", issue.CreatedBy)
					}
					fmt.Printf("Updated: %s\n", issue.UpdatedAt.Format("2006-01-02 15:04"))

					// Show compaction status
					if issue.CompactionLevel > 0 {
						fmt.Println()
						if issue.OriginalSize > 0 {
							currentSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
							saved := issue.OriginalSize - currentSize
							if saved > 0 {
								reduction := float64(saved) / float64(issue.OriginalSize) * 100
								fmt.Printf("📊 Original: %d bytes | Compressed: %d bytes (%.0f%% reduction)\n",
									issue.OriginalSize, currentSize, reduction)
							}
						}
						tierEmoji2 := "🗜️"
						if issue.CompactionLevel == 2 {
							tierEmoji2 = "📦"
						}
						compactedDate := ""
						if issue.CompactedAt != nil {
							compactedDate = issue.CompactedAt.Format("2006-01-02")
						}
						fmt.Printf("%s Compacted: %s (Tier %d)\n", tierEmoji2, compactedDate, issue.CompactionLevel)
					}

					if issue.Description != "" {
						fmt.Printf("\nDescription:\n%s\n", issue.Description)
					}
					if issue.Design != "" {
						fmt.Printf("\nDesign:\n%s\n", issue.Design)
					}
					if issue.Notes != "" {
						fmt.Printf("\nNotes:\n%s\n", issue.Notes)
					}
					if issue.AcceptanceCriteria != "" {
						fmt.Printf("\nAcceptance Criteria:\n%s\n", issue.AcceptanceCriteria)
					}

					if len(details.Labels) > 0 {
						fmt.Printf("\nLabels: %v\n", details.Labels)
					}

					if len(details.Dependencies) > 0 {
						fmt.Printf("\nDepends on (%d):\n", len(details.Dependencies))
						for _, dep := range details.Dependencies {
							fmt.Printf("  → %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
						}
					}

					if len(details.Dependents) > 0 {
						// Group by dependency type for clarity
						var blocks, children, related, discovered []*types.IssueWithDependencyMetadata
						for _, dep := range details.Dependents {
							switch dep.DependencyType {
							case types.DepBlocks:
								blocks = append(blocks, dep)
							case types.DepParentChild:
								children = append(children, dep)
							case types.DepRelated:
								related = append(related, dep)
							case types.DepDiscoveredFrom:
								discovered = append(discovered, dep)
							default:
								blocks = append(blocks, dep)
							}
						}

						if len(children) > 0 {
							fmt.Printf("\nChildren (%d):\n", len(children))
							for _, dep := range children {
								fmt.Printf("  ↳ %s: %s [P%d - %s]\n", dep.ID, dep.Title, dep.Priority, dep.Status)
							}
						}
						if len(blocks) > 0 {
							fmt.Printf("\nBlocks (%d):\n", len(blocks))
							for _, dep := range blocks {
								fmt.Printf("  ← %s: %s [P%d - %s]\n", dep.ID, dep.Title, dep.Priority, dep.Status)
							}
						}
						if len(related) > 0 {
							fmt.Printf("\nRelated (%d):\n", len(related))
							for _, dep := range related {
								fmt.Printf("  ↔ %s: %s [P%d - %s]\n", dep.ID, dep.Title, dep.Priority, dep.Status)
							}
						}
						if len(discovered) > 0 {
							fmt.Printf("\nDiscovered (%d):\n", len(discovered))
							for _, dep := range discovered {
								fmt.Printf("  ◊ %s: %s [P%d - %s]\n", dep.ID, dep.Title, dep.Priority, dep.Status)
							}
						}
					}

					if len(details.Comments) > 0 {
						fmt.Printf("\nComments (%d):\n", len(details.Comments))
						for _, comment := range details.Comments {
							fmt.Printf("  [%s] %s\n", comment.Author, comment.CreatedAt.Format("2006-01-02 15:04"))
							commentLines := strings.Split(comment.Text, "\n")
							for _, line := range commentLines {
								fmt.Printf("    %s\n", line)
							}
						}
					}

					fmt.Println()
				}
			}

			if jsonOutput && len(allDetails) > 0 {
				outputJSON(allDetails)
			}
			return
		}

		// Direct mode - use routed resolution for cross-repo lookups
		allDetails := []interface{}{}
		for idx, id := range args {
			// Resolve and get issue with routing (e.g., gt-xyz routes to gastown)
			result, err := resolveAndGetIssueWithRouting(ctx, store, id)
			if err != nil {
				if result != nil {
					result.Close()
				}
				fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id, err)
				continue
			}
			if result == nil || result.Issue == nil {
				if result != nil {
					result.Close()
				}
				fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
				continue
			}
			issue := result.Issue
			issueStore := result.Store // Use the store that contains this issue
			// Note: result.Close() called at end of loop iteration

			if shortMode {
				fmt.Println(formatShortIssue(issue))
				result.Close()
				continue
			}

			if jsonOutput {
				// Include labels, dependencies (with metadata), dependents (with metadata), and comments in JSON output
				details := &types.IssueDetails{Issue: *issue}
				details.Labels, _ = issueStore.GetLabels(ctx, issue.ID)

				// Get dependencies with metadata (dependency_type field)
				if sqliteStore, ok := issueStore.(*sqlite.SQLiteStorage); ok {
					details.Dependencies, _ = sqliteStore.GetDependenciesWithMetadata(ctx, issue.ID)
					details.Dependents, _ = sqliteStore.GetDependentsWithMetadata(ctx, issue.ID)
				} else {
					// Fallback to regular methods without metadata for other storage backends
					deps, _ := issueStore.GetDependencies(ctx, issue.ID)
					for _, dep := range deps {
						details.Dependencies = append(details.Dependencies, &types.IssueWithDependencyMetadata{Issue: *dep})
					}
					dependents, _ := issueStore.GetDependents(ctx, issue.ID)
					for _, dependent := range dependents {
						details.Dependents = append(details.Dependents, &types.IssueWithDependencyMetadata{Issue: *dependent})
					}
				}

				details.Comments, _ = issueStore.GetIssueComments(ctx, issue.ID)
				// Compute parent from dependencies
				for _, dep := range details.Dependencies {
					if dep.DependencyType == types.DepParentChild {
						details.Parent = &dep.ID
						break
					}
				}
				allDetails = append(allDetails, details)
				result.Close() // Close before continuing to next iteration
				continue
			}

			if idx > 0 {
				fmt.Println("\n" + strings.Repeat("─", 60))
			}

			// Add compaction emoji to title line
			tierEmoji := ""
			statusSuffix := ""
			switch issue.CompactionLevel {
			case 1:
				tierEmoji = " 🗜️"
				statusSuffix = " (compacted L1)"
			case 2:
				tierEmoji = " 📦"
				statusSuffix = " (compacted L2)"
			}

			fmt.Printf("\n%s: %s%s\n", ui.RenderAccent(issue.ID), issue.Title, tierEmoji)
			fmt.Printf("Status: %s%s\n", issue.Status, statusSuffix)
			if issue.CloseReason != "" {
				fmt.Printf("Close reason: %s\n", issue.CloseReason)
			}
			fmt.Printf("Priority: P%d\n", issue.Priority)
			fmt.Printf("Type: %s\n", issue.IssueType)
			if issue.Assignee != "" {
				fmt.Printf("Assignee: %s\n", issue.Assignee)
			}
			if issue.EstimatedMinutes != nil {
				fmt.Printf("Estimated: %d minutes\n", *issue.EstimatedMinutes)
			}
			fmt.Printf("Created: %s\n", issue.CreatedAt.Format("2006-01-02 15:04"))
			if issue.CreatedBy != "" {
				fmt.Printf("Created by: %s\n", issue.CreatedBy)
			}
			fmt.Printf("Updated: %s\n", issue.UpdatedAt.Format("2006-01-02 15:04"))

			// Show compaction status footer
			if issue.CompactionLevel > 0 {
				tierEmoji := "🗜️"
				if issue.CompactionLevel == 2 {
					tierEmoji = "📦"
				}
				tierName := fmt.Sprintf("Tier %d", issue.CompactionLevel)

				fmt.Println()
				if issue.OriginalSize > 0 {
					currentSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
					saved := issue.OriginalSize - currentSize
					if saved > 0 {
						reduction := float64(saved) / float64(issue.OriginalSize) * 100
						fmt.Printf("📊 Original: %d bytes | Compressed: %d bytes (%.0f%% reduction)\n",
							issue.OriginalSize, currentSize, reduction)
					}
				}
				compactedDate := ""
				if issue.CompactedAt != nil {
					compactedDate = issue.CompactedAt.Format("2006-01-02")
				}
				fmt.Printf("%s Compacted: %s (%s)\n", tierEmoji, compactedDate, tierName)
			}

			if issue.Description != "" {
				fmt.Printf("\nDescription:\n%s\n", issue.Description)
			}
			if issue.Design != "" {
				fmt.Printf("\nDesign:\n%s\n", issue.Design)
			}
			if issue.Notes != "" {
				fmt.Printf("\nNotes:\n%s\n", issue.Notes)
			}
			if issue.AcceptanceCriteria != "" {
				fmt.Printf("\nAcceptance Criteria:\n%s\n", issue.AcceptanceCriteria)
			}

			// Show labels
			labels, _ := issueStore.GetLabels(ctx, issue.ID)
			if len(labels) > 0 {
				fmt.Printf("\nLabels: %v\n", labels)
			}

			// Show dependencies
			deps, _ := issueStore.GetDependencies(ctx, issue.ID)
			if len(deps) > 0 {
				fmt.Printf("\nDepends on (%d):\n", len(deps))
				for _, dep := range deps {
					fmt.Printf("  → %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
				}
			}

			// Show dependents - grouped by dependency type for clarity
			// Use GetDependentsWithMetadata to get the dependency type
			sqliteStore, ok := issueStore.(*sqlite.SQLiteStorage)
			if ok {
				dependentsWithMeta, _ := sqliteStore.GetDependentsWithMetadata(ctx, issue.ID)
				if len(dependentsWithMeta) > 0 {
					// Group by dependency type
					var blocks, children, related, discovered []*types.IssueWithDependencyMetadata
					for _, dep := range dependentsWithMeta {
						switch dep.DependencyType {
						case types.DepBlocks:
							blocks = append(blocks, dep)
						case types.DepParentChild:
							children = append(children, dep)
						case types.DepRelated:
							related = append(related, dep)
						case types.DepDiscoveredFrom:
							discovered = append(discovered, dep)
						default:
							blocks = append(blocks, dep) // Default to blocks
						}
					}

					if len(children) > 0 {
						fmt.Printf("\nChildren (%d):\n", len(children))
						for _, dep := range children {
							fmt.Printf("  ↳ %s: %s [P%d - %s]\n", dep.ID, dep.Title, dep.Priority, dep.Status)
						}
					}
					if len(blocks) > 0 {
						fmt.Printf("\nBlocks (%d):\n", len(blocks))
						for _, dep := range blocks {
							fmt.Printf("  ← %s: %s [P%d - %s]\n", dep.ID, dep.Title, dep.Priority, dep.Status)
						}
					}
					if len(related) > 0 {
						fmt.Printf("\nRelated (%d):\n", len(related))
						for _, dep := range related {
							fmt.Printf("  ↔ %s: %s [P%d - %s]\n", dep.ID, dep.Title, dep.Priority, dep.Status)
						}
					}
					if len(discovered) > 0 {
						fmt.Printf("\nDiscovered (%d):\n", len(discovered))
						for _, dep := range discovered {
							fmt.Printf("  ◊ %s: %s [P%d - %s]\n", dep.ID, dep.Title, dep.Priority, dep.Status)
						}
					}
				}
			} else {
				// Fallback for non-SQLite storage
				dependents, _ := issueStore.GetDependents(ctx, issue.ID)
				if len(dependents) > 0 {
					fmt.Printf("\nBlocks (%d):\n", len(dependents))
					for _, dep := range dependents {
						fmt.Printf("  ← %s: %s [P%d - %s]\n", dep.ID, dep.Title, dep.Priority, dep.Status)
					}
				}
			}

			// Show comments
			comments, _ := issueStore.GetIssueComments(ctx, issue.ID)
			if len(comments) > 0 {
				fmt.Printf("\nComments (%d):\n", len(comments))
				for _, comment := range comments {
					fmt.Printf("  [%s at %s]\n  %s\n\n", comment.Author, comment.CreatedAt.Format("2006-01-02 15:04"), comment.Text)
				}
			}

			fmt.Println()
			result.Close() // Close routed storage after each iteration
		}

		if jsonOutput && len(allDetails) > 0 {
			outputJSON(allDetails)
		} else if len(allDetails) > 0 {
			// Show tip after successful show (non-JSON mode)
			maybeShowTip(store)
		}
	},
}


// formatShortIssue returns a compact one-line representation of an issue
// Format: <id> [<status>] P<priority> <type>: <title>
func formatShortIssue(issue *types.Issue) string {
	return fmt.Sprintf("%s [%s] P%d %s: %s",
		issue.ID, issue.Status, issue.Priority, issue.IssueType, issue.Title)
}

func init() {
	showCmd.Flags().Bool("thread", false, "Show full conversation thread (for messages)")
	showCmd.Flags().Bool("short", false, "Show compact one-line output per issue")
	rootCmd.AddCommand(showCmd)
}
