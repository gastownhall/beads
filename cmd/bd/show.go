package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
)

var showCmd = &cobra.Command{
	Use:     "show [id...]",
	GroupID: "issues",
	Short:   "Show issue details",
	Args:    cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		showThread, _ := cmd.Flags().GetBool("thread")
		ctx := rootCtx

		// Check database freshness before reading (bd-2q6d, bd-c4rq)
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
				if jsonOutput {
					// Get labels and deps for JSON output
					type IssueDetails struct {
						*types.Issue
						Labels       []string                             `json:"labels,omitempty"`
						Dependencies []*types.IssueWithDependencyMetadata `json:"dependencies,omitempty"`
						Dependents   []*types.IssueWithDependencyMetadata `json:"dependents,omitempty"`
						Comments     []*types.Comment                     `json:"comments,omitempty"`
					}
					details := &IssueDetails{Issue: issue}
					details.Labels, _ = issueStore.GetLabels(ctx, issue.ID)
					if sqliteStore, ok := issueStore.(*sqlite.SQLiteStorage); ok {
						details.Dependencies, _ = sqliteStore.GetDependenciesWithMetadata(ctx, issue.ID)
						details.Dependents, _ = sqliteStore.GetDependentsWithMetadata(ctx, issue.ID)
					}
					details.Comments, _ = issueStore.GetIssueComments(ctx, issue.ID)
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
					type IssueDetails struct {
						types.Issue
						Labels       []string                             `json:"labels,omitempty"`
						Dependencies []*types.IssueWithDependencyMetadata `json:"dependencies,omitempty"`
						Dependents   []*types.IssueWithDependencyMetadata `json:"dependents,omitempty"`
						Comments     []*types.Comment                     `json:"comments,omitempty"`
					}
					var details IssueDetails
					if err := json.Unmarshal(resp.Data, &details); err == nil {
						allDetails = append(allDetails, details)
					}
				} else {
					// Check if issue exists (daemon returns null for non-existent issues)
					if string(resp.Data) == "null" || len(resp.Data) == 0 {
						fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
						continue
					}
					if displayIdx > 0 {
						fmt.Println("\n" + strings.Repeat("─", 60))
					}
					displayIdx++

					// Parse response and use existing formatting code
					type IssueDetails struct {
						types.Issue
						Labels       []string                             `json:"labels,omitempty"`
						Dependencies []*types.IssueWithDependencyMetadata `json:"dependencies,omitempty"`
						Dependents   []*types.IssueWithDependencyMetadata `json:"dependents,omitempty"`
						Comments     []*types.Comment                     `json:"comments,omitempty"`
					}
					var details IssueDetails
					if err := json.Unmarshal(resp.Data, &details); err != nil {
						fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
						os.Exit(1)
					}
					issue := &details.Issue

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

			if jsonOutput {
				// Include labels, dependencies (with metadata), dependents (with metadata), and comments in JSON output
				type IssueDetails struct {
					*types.Issue
					Labels       []string                             `json:"labels,omitempty"`
					Dependencies []*types.IssueWithDependencyMetadata `json:"dependencies,omitempty"`
					Dependents   []*types.IssueWithDependencyMetadata `json:"dependents,omitempty"`
					Comments     []*types.Comment                     `json:"comments,omitempty"`
				}
				details := &IssueDetails{Issue: issue}
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

var updateCmd = &cobra.Command{
	Use:     "update [id...]",
	GroupID: "issues",
	Short:   "Update one or more issues",
	Args:    cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("update")
		updates := make(map[string]interface{})

		if cmd.Flags().Changed("status") {
			status, _ := cmd.Flags().GetString("status")
			updates["status"] = status
		}
		if cmd.Flags().Changed("priority") {
			priorityStr, _ := cmd.Flags().GetString("priority")
			priority, err := validation.ValidatePriority(priorityStr)
			if err != nil {
				FatalErrorRespectJSON("%v", err)
			}
			updates["priority"] = priority
		}
		if cmd.Flags().Changed("title") {
			title, _ := cmd.Flags().GetString("title")
			updates["title"] = title
		}
		if cmd.Flags().Changed("assignee") {
			assignee, _ := cmd.Flags().GetString("assignee")
			updates["assignee"] = assignee
		}
		description, descChanged := getDescriptionFlag(cmd)
		if descChanged {
			updates["description"] = description
		}
		if cmd.Flags().Changed("design") {
			design, _ := cmd.Flags().GetString("design")
			updates["design"] = design
		}
		if cmd.Flags().Changed("notes") {
			notes, _ := cmd.Flags().GetString("notes")
			updates["notes"] = notes
		}
		if cmd.Flags().Changed("acceptance") || cmd.Flags().Changed("acceptance-criteria") {
			var acceptanceCriteria string
			if cmd.Flags().Changed("acceptance") {
				acceptanceCriteria, _ = cmd.Flags().GetString("acceptance")
			} else {
				acceptanceCriteria, _ = cmd.Flags().GetString("acceptance-criteria")
			}
			updates["acceptance_criteria"] = acceptanceCriteria
		}
		if cmd.Flags().Changed("external-ref") {
			externalRef, _ := cmd.Flags().GetString("external-ref")
			updates["external_ref"] = externalRef
		}
		if cmd.Flags().Changed("estimate") {
			estimate, _ := cmd.Flags().GetInt("estimate")
			if estimate < 0 {
				FatalErrorRespectJSON("estimate must be a non-negative number of minutes")
			}
			updates["estimated_minutes"] = estimate
		}
		if cmd.Flags().Changed("type") {
			issueType, _ := cmd.Flags().GetString("type")
			// Validate issue type
			if !types.IssueType(issueType).IsValid() {
				FatalErrorRespectJSON("invalid issue type %q. Valid types: bug, feature, task, epic, chore, merge-request, molecule, gate", issueType)
			}
			updates["issue_type"] = issueType
		}
		if cmd.Flags().Changed("add-label") {
			addLabels, _ := cmd.Flags().GetStringSlice("add-label")
			updates["add_labels"] = addLabels
		}
		if cmd.Flags().Changed("remove-label") {
			removeLabels, _ := cmd.Flags().GetStringSlice("remove-label")
			updates["remove_labels"] = removeLabels
		}
		if cmd.Flags().Changed("set-labels") {
			setLabels, _ := cmd.Flags().GetStringSlice("set-labels")
			updates["set_labels"] = setLabels
		}
		if cmd.Flags().Changed("type") {
			issueType, _ := cmd.Flags().GetString("type")
			// Validate issue type
			if _, err := validation.ParseIssueType(issueType); err != nil {
				FatalErrorRespectJSON("%v", err)
			}
			updates["issue_type"] = issueType
		}

		if len(updates) == 0 {
			fmt.Println("No updates specified")
			return
		}

		ctx := rootCtx

		// Resolve partial IDs first
		var resolvedIDs []string
		if daemonClient != nil {
			for _, id := range args {
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
		} else {
			var err error
			resolvedIDs, err = utils.ResolvePartialIDs(ctx, store, args)
			if err != nil {
				FatalErrorRespectJSON("%v", err)
			}
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			updatedIssues := []*types.Issue{}
			for _, id := range resolvedIDs {
				updateArgs := &rpc.UpdateArgs{ID: id}

				// Map updates to RPC args
				if status, ok := updates["status"].(string); ok {
					updateArgs.Status = &status
				}
				if priority, ok := updates["priority"].(int); ok {
					updateArgs.Priority = &priority
				}
				if title, ok := updates["title"].(string); ok {
					updateArgs.Title = &title
				}
				if assignee, ok := updates["assignee"].(string); ok {
					updateArgs.Assignee = &assignee
				}
				if description, ok := updates["description"].(string); ok {
					updateArgs.Description = &description
				}
				if design, ok := updates["design"].(string); ok {
					updateArgs.Design = &design
				}
				if notes, ok := updates["notes"].(string); ok {
					updateArgs.Notes = &notes
				}
				if acceptanceCriteria, ok := updates["acceptance_criteria"].(string); ok {
					updateArgs.AcceptanceCriteria = &acceptanceCriteria
				}
				if externalRef, ok := updates["external_ref"].(string); ok {
					updateArgs.ExternalRef = &externalRef
				}
				if estimate, ok := updates["estimated_minutes"].(int); ok {
					updateArgs.EstimatedMinutes = &estimate
				}
				if issueType, ok := updates["issue_type"].(string); ok {
					updateArgs.IssueType = &issueType
				}
				if addLabels, ok := updates["add_labels"].([]string); ok {
					updateArgs.AddLabels = addLabels
				}
				if removeLabels, ok := updates["remove_labels"].([]string); ok {
					updateArgs.RemoveLabels = removeLabels
				}
				if setLabels, ok := updates["set_labels"].([]string); ok {
					updateArgs.SetLabels = setLabels
				}
				if issueType, ok := updates["issue_type"].(string); ok {
					updateArgs.IssueType = &issueType
				}

				resp, err := daemonClient.Update(updateArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
					continue
				}

				var issue types.Issue
				if err := json.Unmarshal(resp.Data, &issue); err == nil {
					// Run update hook (bd-kwro.8)
					if hookRunner != nil {
						hookRunner.Run(hooks.EventUpdate, &issue)
					}
					if jsonOutput {
						updatedIssues = append(updatedIssues, &issue)
					}
				}
				if !jsonOutput {
					fmt.Printf("%s Updated issue: %s\n", ui.RenderPass("✓"), id)
				}
			}

			if jsonOutput && len(updatedIssues) > 0 {
				outputJSON(updatedIssues)
			}
			return
		}

		// Direct mode
		updatedIssues := []*types.Issue{}
		for _, id := range resolvedIDs {
			// Check if issue is a template (beads-1ra): templates are read-only
			issue, err := store.GetIssue(ctx, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting %s: %v\n", id, err)
				continue
			}
			if err := validateIssueUpdatable(id, issue); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				continue
			}

			// Apply regular field updates if any
			regularUpdates := make(map[string]interface{})
			for k, v := range updates {
				if k != "add_labels" && k != "remove_labels" && k != "set_labels" {
					regularUpdates[k] = v
				}
			}
			if len(regularUpdates) > 0 {
				if err := store.UpdateIssue(ctx, id, regularUpdates, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
					continue
				}
			}

			// Handle label operations
			var setLabels, addLabels, removeLabels []string
			if v, ok := updates["set_labels"].([]string); ok {
				setLabels = v
			}
			if v, ok := updates["add_labels"].([]string); ok {
				addLabels = v
			}
			if v, ok := updates["remove_labels"].([]string); ok {
				removeLabels = v
			}
			if len(setLabels) > 0 || len(addLabels) > 0 || len(removeLabels) > 0 {
				if err := applyLabelUpdates(ctx, store, id, actor, setLabels, addLabels, removeLabels); err != nil {
					fmt.Fprintf(os.Stderr, "Error updating labels for %s: %v\n", id, err)
					continue
				}
			}

			// Run update hook (bd-kwro.8)
			updatedIssue, _ := store.GetIssue(ctx, id)
			if updatedIssue != nil && hookRunner != nil {
				hookRunner.Run(hooks.EventUpdate, updatedIssue)
			}

			if jsonOutput {
				if updatedIssue != nil {
					updatedIssues = append(updatedIssues, updatedIssue)
				}
			} else {
				fmt.Printf("%s Updated issue: %s\n", ui.RenderPass("✓"), id)
			}
		}

		// Schedule auto-flush if any issues were updated
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(updatedIssues) > 0 {
			outputJSON(updatedIssues)
		}
	},
}

var editCmd = &cobra.Command{
	Use:     "edit [id]",
	GroupID: "issues",
	Short:   "Edit an issue field in $EDITOR",
	Long: `Edit an issue field using your configured $EDITOR.

By default, edits the description. Use flags to edit other fields.

Examples:
  bd edit bd-42                    # Edit description
  bd edit bd-42 --title            # Edit title
  bd edit bd-42 --design           # Edit design notes
  bd edit bd-42 --notes            # Edit notes
  bd edit bd-42 --acceptance       # Edit acceptance criteria`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("edit")
		id := args[0]
		ctx := rootCtx

		// Resolve partial ID if in direct mode
		if daemonClient == nil {
			fullID, err := utils.ResolvePartialID(ctx, store, id)
			if err != nil {
				FatalErrorRespectJSON("resolving %s: %v", id, err)
			}
			id = fullID
		}

		// Determine which field to edit
		fieldToEdit := "description"
		if cmd.Flags().Changed("title") {
			fieldToEdit = "title"
		} else if cmd.Flags().Changed("design") {
			fieldToEdit = "design"
		} else if cmd.Flags().Changed("notes") {
			fieldToEdit = "notes"
		} else if cmd.Flags().Changed("acceptance") {
			fieldToEdit = "acceptance_criteria"
		}

		// Get the editor from environment
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = os.Getenv("VISUAL")
		}
		if editor == "" {
			// Try common defaults
			for _, defaultEditor := range []string{"vim", "vi", "nano", "emacs"} {
				if _, err := exec.LookPath(defaultEditor); err == nil {
					editor = defaultEditor
					break
				}
			}
		}
		if editor == "" {
			FatalErrorRespectJSON("no editor found. Set $EDITOR or $VISUAL environment variable")
		}

		// Get the current issue
		var issue *types.Issue
		var err error

		if daemonClient != nil {
			// Daemon mode
			showArgs := &rpc.ShowArgs{ID: id}
			resp, err := daemonClient.Show(showArgs)
			if err != nil {
				FatalErrorRespectJSON("fetching issue %s: %v", id, err)
			}

			issue = &types.Issue{}
			if err := json.Unmarshal(resp.Data, issue); err != nil {
				FatalErrorRespectJSON("parsing issue data: %v", err)
			}
		} else {
			// Direct mode
			issue, err = store.GetIssue(ctx, id)
			if err != nil {
				FatalErrorRespectJSON("fetching issue %s: %v", id, err)
			}
			if issue == nil {
				FatalErrorRespectJSON("issue %s not found", id)
			}
		}

		// Get the current field value
		var currentValue string
		switch fieldToEdit {
		case "title":
			currentValue = issue.Title
		case "description":
			currentValue = issue.Description
		case "design":
			currentValue = issue.Design
		case "notes":
			currentValue = issue.Notes
		case "acceptance_criteria":
			currentValue = issue.AcceptanceCriteria
		}

		// Create a temporary file with the current value
		tmpFile, err := os.CreateTemp("", fmt.Sprintf("bd-edit-%s-*.txt", fieldToEdit))
		if err != nil {
			FatalErrorRespectJSON("creating temp file: %v", err)
		}
		tmpPath := tmpFile.Name()
		defer func() { _ = os.Remove(tmpPath) }()

		// Write current value to temp file
		if _, err := tmpFile.WriteString(currentValue); err != nil {
			_ = tmpFile.Close()
			FatalErrorRespectJSON("writing to temp file: %v", err)
		}
		_ = tmpFile.Close()

		// Open the editor
		editorCmd := exec.Command(editor, tmpPath)
		editorCmd.Stdin = os.Stdin
		editorCmd.Stdout = os.Stdout
		editorCmd.Stderr = os.Stderr

		if err := editorCmd.Run(); err != nil {
			FatalErrorRespectJSON("running editor: %v", err)
		}

		// Read the edited content
		// #nosec G304 -- tmpPath was created earlier in this function
		editedContent, err := os.ReadFile(tmpPath)
		if err != nil {
			FatalErrorRespectJSON("reading edited file: %v", err)
		}

		newValue := string(editedContent)

		// Check if the value changed
		if newValue == currentValue {
			fmt.Println("No changes made")
			return
		}

		// Validate title if editing title
		if fieldToEdit == "title" && strings.TrimSpace(newValue) == "" {
			FatalErrorRespectJSON("title cannot be empty")
		}

		// Update the issue
		updates := map[string]interface{}{
			fieldToEdit: newValue,
		}

		if daemonClient != nil {
			// Daemon mode
			updateArgs := &rpc.UpdateArgs{ID: id}

			switch fieldToEdit {
			case "title":
				updateArgs.Title = &newValue
			case "description":
				updateArgs.Description = &newValue
			case "design":
				updateArgs.Design = &newValue
			case "notes":
				updateArgs.Notes = &newValue
			case "acceptance_criteria":
				updateArgs.AcceptanceCriteria = &newValue
			}

			_, err := daemonClient.Update(updateArgs)
			if err != nil {
				FatalErrorRespectJSON("updating issue: %v", err)
			}
		} else {
			// Direct mode
			if err := store.UpdateIssue(ctx, id, updates, actor); err != nil {
				FatalErrorRespectJSON("updating issue: %v", err)
			}
			markDirtyAndScheduleFlush()
		}

		fieldName := strings.ReplaceAll(fieldToEdit, "_", " ")
		fmt.Printf("%s Updated %s for issue: %s\n", ui.RenderPass("✓"), fieldName, id)
	},
}

var closeCmd = &cobra.Command{
	Use:     "close [id...]",
	GroupID: "issues",
	Short:   "Close one or more issues",
	Args:    cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("close")
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			// Check --resolution alias (Jira CLI convention)
			reason, _ = cmd.Flags().GetString("resolution")
		}
		if reason == "" {
			reason = "Closed"
		}
		force, _ := cmd.Flags().GetBool("force")
		continueFlag, _ := cmd.Flags().GetBool("continue")
		noAuto, _ := cmd.Flags().GetBool("no-auto")
		suggestNext, _ := cmd.Flags().GetBool("suggest-next")

		ctx := rootCtx

		// --continue only works with a single issue
		if continueFlag && len(args) > 1 {
			FatalErrorRespectJSON("--continue only works when closing a single issue")
		}

		// --suggest-next only works with a single issue
		if suggestNext && len(args) > 1 {
			FatalErrorRespectJSON("--suggest-next only works when closing a single issue")
		}

		// Resolve partial IDs first
		var resolvedIDs []string
		if daemonClient != nil {
			for _, id := range args {
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
		} else {
			var err error
			resolvedIDs, err = utils.ResolvePartialIDs(ctx, store, args)
			if err != nil {
				FatalErrorRespectJSON("%v", err)
			}
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			closedIssues := []*types.Issue{}
			for _, id := range resolvedIDs {
				// Get issue for template and pinned checks
				showArgs := &rpc.ShowArgs{ID: id}
				showResp, showErr := daemonClient.Show(showArgs)
				if showErr == nil {
					var issue types.Issue
					if json.Unmarshal(showResp.Data, &issue) == nil {
						if err := validateIssueClosable(id, &issue, force); err != nil {
							fmt.Fprintf(os.Stderr, "%s\n", err)
							continue
						}
					}
				}

				closeArgs := &rpc.CloseArgs{
					ID:          id,
					Reason:      reason,
					SuggestNext: suggestNext,
				}
				resp, err := daemonClient.CloseIssue(closeArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
					continue
				}

				// Handle response based on whether SuggestNext was requested (GH#679)
				if suggestNext {
					var result rpc.CloseResult
					if err := json.Unmarshal(resp.Data, &result); err == nil {
						if result.Closed != nil {
							// Run close hook (bd-kwro.8)
							if hookRunner != nil {
								hookRunner.Run(hooks.EventClose, result.Closed)
							}
							if jsonOutput {
								closedIssues = append(closedIssues, result.Closed)
							}
						}
						if !jsonOutput {
							fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), id, reason)
							// Display newly unblocked issues (GH#679)
							if len(result.Unblocked) > 0 {
								fmt.Printf("\nNewly unblocked:\n")
								for _, issue := range result.Unblocked {
									fmt.Printf("  • %s %q (P%d)\n", issue.ID, issue.Title, issue.Priority)
								}
							}
						}
					}
				} else {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						// Run close hook (bd-kwro.8)
						if hookRunner != nil {
							hookRunner.Run(hooks.EventClose, &issue)
						}
						if jsonOutput {
							closedIssues = append(closedIssues, &issue)
						}
					}
					if !jsonOutput {
						fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), id, reason)
					}
				}
			}

			// Handle --continue flag in daemon mode (bd-ieyy)
			// Note: --continue requires direct database access to walk parent-child chain
			if continueFlag && len(closedIssues) > 0 {
				fmt.Fprintf(os.Stderr, "\nNote: --continue requires direct database access\n")
				fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon close %s --continue\n", resolvedIDs[0])
			}

			if jsonOutput && len(closedIssues) > 0 {
				outputJSON(closedIssues)
			}
			return
		}

		// Direct mode
		closedIssues := []*types.Issue{}
		closedCount := 0
		for _, id := range resolvedIDs {
			// Get issue for checks
			issue, _ := store.GetIssue(ctx, id)

			if err := validateIssueClosable(id, issue, force); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", err)
				continue
			}

			if err := store.CloseIssue(ctx, id, reason, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
				continue
			}

			closedCount++

			// Run close hook (bd-kwro.8)
			closedIssue, _ := store.GetIssue(ctx, id)
			if closedIssue != nil && hookRunner != nil {
				hookRunner.Run(hooks.EventClose, closedIssue)
			}

			if jsonOutput {
				if closedIssue != nil {
					closedIssues = append(closedIssues, closedIssue)
				}
			} else {
				fmt.Printf("%s Closed %s: %s\n", ui.RenderPass("✓"), id, reason)
			}
		}

		// Handle --suggest-next flag in direct mode (GH#679)
		if suggestNext && len(resolvedIDs) == 1 && closedCount > 0 {
			unblocked, err := store.GetNewlyUnblockedByClose(ctx, resolvedIDs[0])
			if err == nil && len(unblocked) > 0 {
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"closed":    closedIssues,
						"unblocked": unblocked,
					})
					return
				}
				fmt.Printf("\nNewly unblocked:\n")
				for _, issue := range unblocked {
					fmt.Printf("  • %s %q (P%d)\n", issue.ID, issue.Title, issue.Priority)
				}
			}
		}

		// Schedule auto-flush if any issues were closed
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}

		// Handle --continue flag (bd-ieyy)
		if continueFlag && len(resolvedIDs) == 1 && closedCount > 0 {
			autoClaim := !noAuto
			result, err := AdvanceToNextStep(ctx, store, resolvedIDs[0], autoClaim, actor)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not advance to next step: %v\n", err)
			} else if result != nil {
				if jsonOutput {
					// Include continue result in JSON output
					outputJSON(map[string]interface{}{
						"closed":   closedIssues,
						"continue": result,
					})
					return
				}
				PrintContinueResult(result)
			}
		}

		if jsonOutput && len(closedIssues) > 0 {
			outputJSON(closedIssues)
		}
	},
}

// showMessageThread displays a full conversation thread for a message
func showMessageThread(ctx context.Context, messageID string, jsonOutput bool) {
	// Get the starting message
	var startMsg *types.Issue
	var err error

	if daemonClient != nil {
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: messageID})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching message %s: %v\n", messageID, err)
			os.Exit(1)
		}
		if err := json.Unmarshal(resp.Data, &startMsg); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
			os.Exit(1)
		}
	} else {
		startMsg, err = store.GetIssue(ctx, messageID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching message %s: %v\n", messageID, err)
			os.Exit(1)
		}
	}

	if startMsg == nil {
		fmt.Fprintf(os.Stderr, "Message %s not found\n", messageID)
		os.Exit(1)
	}

	// Find the root of the thread by following replies-to dependencies upward
	// Per Decision 004, RepliesTo is now stored as a dependency, not an Issue field
	rootMsg := startMsg
	seen := make(map[string]bool)
	seen[rootMsg.ID] = true

	for {
		// Find parent via replies-to dependency
		parentID := findRepliesTo(ctx, rootMsg.ID, daemonClient, store)
		if parentID == "" {
			break // No parent, this is the root
		}
		if seen[parentID] {
			break // Avoid infinite loops
		}
		seen[parentID] = true

		var parentMsg *types.Issue
		if daemonClient != nil {
			resp, err := daemonClient.Show(&rpc.ShowArgs{ID: parentID})
			if err != nil {
				break // Parent not found, use current as root
			}
			if err := json.Unmarshal(resp.Data, &parentMsg); err != nil {
				break
			}
		} else {
			parentMsg, _ = store.GetIssue(ctx, parentID)
		}
		if parentMsg == nil {
			break
		}
		rootMsg = parentMsg
	}

	// Now collect all messages in the thread
	// Start from root and find all replies
	// Build a map of child ID -> parent ID for display purposes
	threadMessages := []*types.Issue{rootMsg}
	threadIDs := map[string]bool{rootMsg.ID: true}
	repliesTo := map[string]string{} // child ID -> parent ID
	queue := []string{rootMsg.ID}

	// BFS to find all replies
	for len(queue) > 0 {
		currentID := queue[0]
		queue = queue[1:]

		// Find all messages that reply to currentID via replies-to dependency
		// Per Decision 004, replies are found via dependents with type replies-to
		replies := findReplies(ctx, currentID, daemonClient, store)

		for _, reply := range replies {
			if threadIDs[reply.ID] {
				continue // Already seen
			}
			threadMessages = append(threadMessages, reply)
			threadIDs[reply.ID] = true
			repliesTo[reply.ID] = currentID // Track parent for display
			queue = append(queue, reply.ID)
		}
	}

	// Sort by creation time
	slices.SortFunc(threadMessages, func(a, b *types.Issue) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(threadMessages)
		return
	}

	// Display the thread
	fmt.Printf("\n%s Thread: %s\n", ui.RenderAccent("📬"), rootMsg.Title)
	fmt.Println(strings.Repeat("─", 66))

	for _, msg := range threadMessages {
		// Show indent based on depth (count replies_to chain using our map)
		depth := 0
		parent := repliesTo[msg.ID]
		for parent != "" && depth < 5 {
			depth++
			parent = repliesTo[parent]
		}
		indent := strings.Repeat("  ", depth)

		// Format timestamp
		timeStr := msg.CreatedAt.Format("2006-01-02 15:04")

		// Status indicator
		statusIcon := "📧"
		if msg.Status == types.StatusClosed {
			statusIcon = "✓"
		}

		fmt.Printf("%s%s %s %s\n", indent, statusIcon, ui.RenderAccent(msg.ID), ui.RenderMuted(timeStr))
		fmt.Printf("%s  From: %s  To: %s\n", indent, msg.Sender, msg.Assignee)
		if parentID := repliesTo[msg.ID]; parentID != "" {
			fmt.Printf("%s  Re: %s\n", indent, parentID)
		}
		fmt.Printf("%s  %s: %s\n", indent, ui.RenderMuted("Subject"), msg.Title)
		if msg.Description != "" {
			// Indent the body
			bodyLines := strings.Split(msg.Description, "\n")
			for _, line := range bodyLines {
				fmt.Printf("%s  %s\n", indent, line)
			}
		}
		fmt.Println()
	}

	fmt.Printf("Total: %d messages in thread\n\n", len(threadMessages))
}

// findRepliesTo finds the parent ID that this issue replies to via replies-to dependency.
// Returns empty string if no parent found.
func findRepliesTo(ctx context.Context, issueID string, daemonClient *rpc.Client, store storage.Storage) string {
	if daemonClient != nil {
		// In daemon mode, use Show to get dependencies with metadata
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: issueID})
		if err != nil {
			return ""
		}
		// Parse the full show response to get dependencies
		type showResponse struct {
			Dependencies []struct {
				ID             string `json:"id"`
				DependencyType string `json:"dependency_type"`
			} `json:"dependencies"`
		}
		var details showResponse
		if err := json.Unmarshal(resp.Data, &details); err != nil {
			return ""
		}
		for _, dep := range details.Dependencies {
			if dep.DependencyType == string(types.DepRepliesTo) {
				return dep.ID
			}
		}
		return ""
	}
	// Direct mode - query storage
	deps, err := store.GetDependencyRecords(ctx, issueID)
	if err != nil {
		return ""
	}
	for _, dep := range deps {
		if dep.Type == types.DepRepliesTo {
			return dep.DependsOnID
		}
	}
	return ""
}

// findReplies finds all issues that reply to this issue via replies-to dependency.
func findReplies(ctx context.Context, issueID string, daemonClient *rpc.Client, store storage.Storage) []*types.Issue {
	if daemonClient != nil {
		// In daemon mode, use Show to get dependents with metadata
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: issueID})
		if err != nil {
			return nil
		}
		// Parse the full show response to get dependents
		type showResponse struct {
			Dependents []struct {
				types.Issue
				DependencyType string `json:"dependency_type"`
			} `json:"dependents"`
		}
		var details showResponse
		if err := json.Unmarshal(resp.Data, &details); err != nil {
			return nil
		}
		var replies []*types.Issue
		for _, dep := range details.Dependents {
			if dep.DependencyType == string(types.DepRepliesTo) {
				issue := dep.Issue // Copy to avoid aliasing
				replies = append(replies, &issue)
			}
		}
		return replies
	}
	// Direct mode - query storage
	if sqliteStore, ok := store.(*sqlite.SQLiteStorage); ok {
		deps, err := sqliteStore.GetDependentsWithMetadata(ctx, issueID)
		if err != nil {
			return nil
		}
		var replies []*types.Issue
		for _, dep := range deps {
			if dep.DependencyType == types.DepRepliesTo {
				issue := dep.Issue // Copy to avoid aliasing
				replies = append(replies, &issue)
			}
		}
		return replies
	}

	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil
	}

	var replies []*types.Issue
	for childID, deps := range allDeps {
		for _, dep := range deps {
			if dep.Type == types.DepRepliesTo && dep.DependsOnID == issueID {
				issue, _ := store.GetIssue(ctx, childID)
				if issue != nil {
					replies = append(replies, issue)
				}
			}
		}
	}

	return replies
}

func init() {
	showCmd.Flags().Bool("thread", false, "Show full conversation thread (for messages)")
	rootCmd.AddCommand(showCmd)

	updateCmd.Flags().StringP("status", "s", "", "New status")
	registerPriorityFlag(updateCmd, "")
	updateCmd.Flags().String("title", "", "New title")
	updateCmd.Flags().StringP("type", "t", "", "New type (bug|feature|task|epic|chore|merge-request|molecule|gate)")
	registerCommonIssueFlags(updateCmd)
	updateCmd.Flags().String("notes", "", "Additional notes")
	updateCmd.Flags().String("acceptance-criteria", "", "DEPRECATED: use --acceptance")
	_ = updateCmd.Flags().MarkHidden("acceptance-criteria") // Only fails if flag missing (caught in tests)
	updateCmd.Flags().IntP("estimate", "e", 0, "Time estimate in minutes (e.g., 60 for 1 hour)")
	updateCmd.Flags().StringSlice("add-label", nil, "Add labels (repeatable)")
	updateCmd.Flags().StringSlice("remove-label", nil, "Remove labels (repeatable)")
	updateCmd.Flags().StringSlice("set-labels", nil, "Set labels, replacing all existing (repeatable)")
	rootCmd.AddCommand(updateCmd)

	editCmd.Flags().Bool("title", false, "Edit the title")
	editCmd.Flags().Bool("description", false, "Edit the description (default)")
	editCmd.Flags().Bool("design", false, "Edit the design notes")
	editCmd.Flags().Bool("notes", false, "Edit the notes")
	editCmd.Flags().Bool("acceptance", false, "Edit the acceptance criteria")
	rootCmd.AddCommand(editCmd)

	closeCmd.Flags().StringP("reason", "r", "", "Reason for closing")
	closeCmd.Flags().String("resolution", "", "Alias for --reason (Jira CLI convention)")
	_ = closeCmd.Flags().MarkHidden("resolution") // Hidden alias for agent/CLI ergonomics
	closeCmd.Flags().BoolP("force", "f", false, "Force close pinned issues")
	closeCmd.Flags().Bool("continue", false, "Auto-advance to next step in molecule")
	closeCmd.Flags().Bool("no-auto", false, "With --continue, show next step but don't claim it")
	closeCmd.Flags().Bool("suggest-next", false, "Show newly unblocked issues after closing (GH#679)")
	rootCmd.AddCommand(closeCmd)
}
