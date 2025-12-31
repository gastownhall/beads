package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
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
		showRefs, _ := cmd.Flags().GetBool("refs")
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

		// Handle --refs flag: show issues that reference this issue
		if showRefs {
			showIssueRefs(ctx, args, resolvedIDs, routedArgs, jsonOutput)
			return
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

			// Track first shown issue as last touched
			if len(resolvedIDs) > 0 {
				SetLastTouchedID(resolvedIDs[0])
			} else if len(routedArgs) > 0 {
				SetLastTouchedID(routedArgs[0])
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

		// Track first shown issue as last touched
		if len(args) > 0 {
			SetLastTouchedID(args[0])
		}
	},
}


// formatShortIssue returns a compact one-line representation of an issue
// Format: <id> [<status>] P<priority> <type>: <title>
func formatShortIssue(issue *types.Issue) string {
	return fmt.Sprintf("%s [%s] P%d %s: %s",
		issue.ID, issue.Status, issue.Priority, issue.IssueType, issue.Title)
}

// showIssueRefs displays issues that reference the given issue(s), grouped by relationship type
func showIssueRefs(ctx context.Context, args []string, resolvedIDs []string, routedArgs []string, jsonOut bool) {
	// Collect all refs for all issues
	allRefs := make(map[string][]*types.IssueWithDependencyMetadata)

	// Process each issue
	processIssue := func(issueID string, issueStore storage.Storage) error {
		sqliteStore, ok := issueStore.(*sqlite.SQLiteStorage)
		if !ok {
			// Fallback: try to get dependents without metadata
			dependents, err := issueStore.GetDependents(ctx, issueID)
			if err != nil {
				return err
			}
			for _, dep := range dependents {
				allRefs[issueID] = append(allRefs[issueID], &types.IssueWithDependencyMetadata{Issue: *dep})
			}
			return nil
		}

		refs, err := sqliteStore.GetDependentsWithMetadata(ctx, issueID)
		if err != nil {
			return err
		}
		allRefs[issueID] = refs
		return nil
	}

	// Handle routed IDs via direct mode
	for _, id := range routedArgs {
		result, err := resolveAndGetIssueWithRouting(ctx, store, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
			continue
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
			continue
		}
		if err := processIssue(result.ResolvedID, result.Store); err != nil {
			fmt.Fprintf(os.Stderr, "Error getting refs for %s: %v\n", id, err)
		}
		result.Close()
	}

	// Handle resolved IDs (daemon mode)
	if daemonClient != nil {
		for _, id := range resolvedIDs {
			// Need to open direct connection for GetDependentsWithMetadata
			dbStore, err := sqlite.New(ctx, dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
				continue
			}
			if err := processIssue(id, dbStore); err != nil {
				fmt.Fprintf(os.Stderr, "Error getting refs for %s: %v\n", id, err)
			}
			_ = dbStore.Close()
		}
	} else {
		// Direct mode - process each arg
		for _, id := range args {
			if containsStr(routedArgs, id) {
				continue // Already processed above
			}
			result, err := resolveAndGetIssueWithRouting(ctx, store, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				continue
			}
			if result == nil || result.Issue == nil {
				if result != nil {
					result.Close()
				}
				fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
				continue
			}
			if err := processIssue(result.ResolvedID, result.Store); err != nil {
				fmt.Fprintf(os.Stderr, "Error getting refs for %s: %v\n", id, err)
			}
			result.Close()
		}
	}

	// Output results
	if jsonOut {
		outputJSON(allRefs)
		return
	}

	// Display refs grouped by issue and relationship type
	for issueID, refs := range allRefs {
		if len(refs) == 0 {
			fmt.Printf("\n%s: No references found\n", ui.RenderAccent(issueID))
			continue
		}

		fmt.Printf("\n%s References to %s:\n", ui.RenderAccent("📎"), issueID)

		// Group refs by type
		refsByType := make(map[types.DependencyType][]*types.IssueWithDependencyMetadata)
		for _, ref := range refs {
			refsByType[ref.DependencyType] = append(refsByType[ref.DependencyType], ref)
		}

		// Display each type
		typeOrder := []types.DependencyType{
			types.DepUntil, types.DepCausedBy, types.DepValidates,
			types.DepBlocks, types.DepParentChild, types.DepRelatesTo,
			types.DepTracks, types.DepDiscoveredFrom, types.DepRelated,
			types.DepSupersedes, types.DepDuplicates, types.DepRepliesTo,
			types.DepApprovedBy, types.DepAuthoredBy, types.DepAssignedTo,
		}

		// First show types in order, then any others
		shown := make(map[types.DependencyType]bool)
		for _, depType := range typeOrder {
			if refs, ok := refsByType[depType]; ok {
				displayRefGroup(depType, refs)
				shown[depType] = true
			}
		}
		// Show any remaining types
		for depType, refs := range refsByType {
			if !shown[depType] {
				displayRefGroup(depType, refs)
			}
		}
		fmt.Println()
	}
}

// displayRefGroup displays a group of references with a given type
func displayRefGroup(depType types.DependencyType, refs []*types.IssueWithDependencyMetadata) {
	// Get emoji for type
	emoji := getRefTypeEmoji(depType)
	fmt.Printf("\n  %s %s (%d):\n", emoji, depType, len(refs))

	for _, ref := range refs {
		// Color ID based on status
		var idStr string
		switch ref.Status {
		case types.StatusOpen:
			idStr = ui.StatusOpenStyle.Render(ref.ID)
		case types.StatusInProgress:
			idStr = ui.StatusInProgressStyle.Render(ref.ID)
		case types.StatusBlocked:
			idStr = ui.StatusBlockedStyle.Render(ref.ID)
		case types.StatusClosed:
			idStr = ui.StatusClosedStyle.Render(ref.ID)
		default:
			idStr = ref.ID
		}
		fmt.Printf("    %s: %s [P%d - %s]\n", idStr, ref.Title, ref.Priority, ref.Status)
	}
}

// getRefTypeEmoji returns an emoji for a dependency/reference type
func getRefTypeEmoji(depType types.DependencyType) string {
	switch depType {
	case types.DepUntil:
		return "⏳" // Hourglass - waiting until
	case types.DepCausedBy:
		return "⚡" // Lightning - triggered by
	case types.DepValidates:
		return "✅" // Checkmark - validates
	case types.DepBlocks:
		return "🚫" // Blocked
	case types.DepParentChild:
		return "↳" // Child arrow
	case types.DepRelatesTo, types.DepRelated:
		return "↔" // Bidirectional
	case types.DepTracks:
		return "👁" // Watching
	case types.DepDiscoveredFrom:
		return "◊" // Diamond - discovered
	case types.DepSupersedes:
		return "⬆" // Upgrade
	case types.DepDuplicates:
		return "🔄" // Duplicate
	case types.DepRepliesTo:
		return "💬" // Chat
	case types.DepApprovedBy:
		return "👍" // Approved
	case types.DepAuthoredBy:
		return "✏" // Authored
	case types.DepAssignedTo:
		return "👤" // Assigned
	default:
		return "→" // Default arrow
	}
}

// containsStr checks if a string slice contains a value
func containsStr(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func init() {
	showCmd.Flags().Bool("thread", false, "Show full conversation thread (for messages)")
	showCmd.Flags().Bool("short", false, "Show compact one-line output per issue")
	showCmd.Flags().Bool("refs", false, "Show issues that reference this issue (reverse lookup)")
	rootCmd.AddCommand(showCmd)
}
