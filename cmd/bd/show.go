package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
)

var showCmd = &cobra.Command{
	Use:   "show [id...]",
	Short: "Show issue details",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		jsonOutput, _ := cmd.Flags().GetBool("json")
		ctx := rootCtx

		// Check database freshness before reading (bd-2q6d, bd-c4rq)
		// Skip check when using daemon (daemon auto-imports on staleness)
		if daemonClient == nil {
			if err := ensureDatabaseFresh(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// Resolve partial IDs first
		var resolvedIDs []string
		if daemonClient != nil {
			// In daemon mode, resolve via RPC
			for _, id := range args {
				resolveArgs := &rpc.ResolveIDArgs{ID: id}
				resp, err := daemonClient.ResolveID(resolveArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error resolving ID %s: %v\n", id, err)
					os.Exit(1)
				}
				var resolvedID string
				if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
					fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
					os.Exit(1)
				}
				resolvedIDs = append(resolvedIDs, resolvedID)
			}
		} else {
			// In direct mode, resolve via storage
			var err error
			resolvedIDs, err = utils.ResolvePartialIDs(ctx, store, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			allDetails := []interface{}{}
			for idx, id := range resolvedIDs {
				showArgs := &rpc.ShowArgs{ID: id}
				resp, err := daemonClient.Show(showArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					type IssueDetails struct {
						types.Issue
						Labels       []string       `json:"labels,omitempty"`
						Dependencies []*types.IssueWithDependencyMetadata `json:"dependencies,omitempty"`
						Dependents   []*types.IssueWithDependencyMetadata `json:"dependents,omitempty"`
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
					if idx > 0 {
						fmt.Println("\n" + strings.Repeat("─", 60))
					}

					// Parse response and use existing formatting code
					type IssueDetails struct {
						types.Issue
						Labels       []string       `json:"labels,omitempty"`
						Dependencies []*types.IssueWithDependencyMetadata `json:"dependencies,omitempty"`
						Dependents   []*types.IssueWithDependencyMetadata `json:"dependents,omitempty"`
					}
					var details IssueDetails
					if err := json.Unmarshal(resp.Data, &details); err != nil {
						fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
						os.Exit(1)
					}
					issue := &details.Issue

					cyan := color.New(color.FgCyan).SprintFunc()

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

					fmt.Printf("\n%s: %s%s\n", cyan(issue.ID), issue.Title, tierEmoji)
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
								fmt.Printf("  ↳ %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
							}
						}
						if len(blocks) > 0 {
							fmt.Printf("\nBlocks (%d):\n", len(blocks))
							for _, dep := range blocks {
								fmt.Printf("  ← %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
							}
						}
						if len(related) > 0 {
							fmt.Printf("\nRelated (%d):\n", len(related))
							for _, dep := range related {
								fmt.Printf("  ↔ %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
							}
						}
						if len(discovered) > 0 {
							fmt.Printf("\nDiscovered (%d):\n", len(discovered))
							for _, dep := range discovered {
								fmt.Printf("  ◊ %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
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

		// Direct mode
		allDetails := []interface{}{}
		for idx, id := range resolvedIDs {
			issue, err := store.GetIssue(ctx, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id, err)
				continue
			}
			if issue == nil {
				fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
				continue
			}

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
				details.Labels, _ = store.GetLabels(ctx, issue.ID)

				// Get dependencies with metadata (dependency_type field)
				if sqliteStore, ok := store.(*sqlite.SQLiteStorage); ok {
					details.Dependencies, _ = sqliteStore.GetDependenciesWithMetadata(ctx, issue.ID)
					details.Dependents, _ = sqliteStore.GetDependentsWithMetadata(ctx, issue.ID)
				} else {
					// Fallback to regular methods without metadata for other storage backends
					deps, _ := store.GetDependencies(ctx, issue.ID)
					for _, dep := range deps {
						details.Dependencies = append(details.Dependencies, &types.IssueWithDependencyMetadata{Issue: *dep})
					}
					dependents, _ := store.GetDependents(ctx, issue.ID)
					for _, dependent := range dependents {
						details.Dependents = append(details.Dependents, &types.IssueWithDependencyMetadata{Issue: *dependent})
					}
				}

				details.Comments, _ = store.GetIssueComments(ctx, issue.ID)
				allDetails = append(allDetails, details)
				continue
			}

			if idx > 0 {
				fmt.Println("\n" + strings.Repeat("─", 60))
			}

			cyan := color.New(color.FgCyan).SprintFunc()

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

			fmt.Printf("\n%s: %s%s\n", cyan(issue.ID), issue.Title, tierEmoji)
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
			labels, _ := store.GetLabels(ctx, issue.ID)
			if len(labels) > 0 {
				fmt.Printf("\nLabels: %v\n", labels)
			}

			// Show dependencies
			deps, _ := store.GetDependencies(ctx, issue.ID)
			if len(deps) > 0 {
				fmt.Printf("\nDepends on (%d):\n", len(deps))
				for _, dep := range deps {
					fmt.Printf("  → %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
				}
			}

			// Show dependents - grouped by dependency type for clarity
			// Use GetDependentsWithMetadata to get the dependency type
			sqliteStore, ok := store.(*sqlite.SQLiteStorage)
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
							fmt.Printf("  ↳ %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
						}
					}
					if len(blocks) > 0 {
						fmt.Printf("\nBlocks (%d):\n", len(blocks))
						for _, dep := range blocks {
							fmt.Printf("  ← %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
						}
					}
					if len(related) > 0 {
						fmt.Printf("\nRelated (%d):\n", len(related))
						for _, dep := range related {
							fmt.Printf("  ↔ %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
						}
					}
					if len(discovered) > 0 {
						fmt.Printf("\nDiscovered (%d):\n", len(discovered))
						for _, dep := range discovered {
							fmt.Printf("  ◊ %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
						}
					}
				}
			} else {
				// Fallback for non-SQLite storage
				dependents, _ := store.GetDependents(ctx, issue.ID)
				if len(dependents) > 0 {
					fmt.Printf("\nBlocks (%d):\n", len(dependents))
					for _, dep := range dependents {
						fmt.Printf("  ← %s: %s [P%d]\n", dep.ID, dep.Title, dep.Priority)
					}
				}
			}

			// Show comments
			comments, _ := store.GetIssueComments(ctx, issue.ID)
			if len(comments) > 0 {
				fmt.Printf("\nComments (%d):\n", len(comments))
				for _, comment := range comments {
					fmt.Printf("  [%s at %s]\n  %s\n\n", comment.Author, comment.CreatedAt.Format("2006-01-02 15:04"), comment.Text)
				}
			}

			fmt.Println()
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
	Use:   "update [id...]",
	Short: "Update one or more issues",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("update")
		jsonOutput, _ := cmd.Flags().GetBool("json")
		updates := make(map[string]interface{})

		if cmd.Flags().Changed("status") {
			status, _ := cmd.Flags().GetString("status")
			updates["status"] = status
		}
		if cmd.Flags().Changed("priority") {
			priorityStr, _ := cmd.Flags().GetString("priority")
			priority, err := validation.ValidatePriority(priorityStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
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
				fmt.Fprintf(os.Stderr, "Error: estimate must be a non-negative number of minutes\n")
				os.Exit(1)
			}
			updates["estimated_minutes"] = estimate
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
					fmt.Fprintf(os.Stderr, "Error resolving ID %s: %v\n", id, err)
					os.Exit(1)
				}
				var resolvedID string
				if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
					fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
					os.Exit(1)
				}
				resolvedIDs = append(resolvedIDs, resolvedID)
			}
		} else {
			var err error
			resolvedIDs, err = utils.ResolvePartialIDs(ctx, store, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
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
				if addLabels, ok := updates["add_labels"].([]string); ok {
					updateArgs.AddLabels = addLabels
				}
				if removeLabels, ok := updates["remove_labels"].([]string); ok {
					updateArgs.RemoveLabels = removeLabels
				}
				if setLabels, ok := updates["set_labels"].([]string); ok {
					updateArgs.SetLabels = setLabels
				}

				resp, err := daemonClient.Update(updateArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						updatedIssues = append(updatedIssues, &issue)
					}
				} else {
					green := color.New(color.FgGreen).SprintFunc()
					fmt.Printf("%s Updated issue: %s\n", green("✓"), id)
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
			// Set labels (replaces all existing labels)
			if setLabels, ok := updates["set_labels"].([]string); ok && len(setLabels) > 0 {
				// Get current labels
				currentLabels, err := store.GetLabels(ctx, id)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error getting labels for %s: %v\n", id, err)
					continue
				}
				// Remove all current labels
				for _, label := range currentLabels {
					if err := store.RemoveLabel(ctx, id, label, actor); err != nil {
						fmt.Fprintf(os.Stderr, "Error removing label %s from %s: %v\n", label, id, err)
						continue
					}
				}
				// Add new labels
				for _, label := range setLabels {
					if err := store.AddLabel(ctx, id, label, actor); err != nil {
						fmt.Fprintf(os.Stderr, "Error setting label %s on %s: %v\n", label, id, err)
						continue
					}
				}
			}

			// Add labels
			if addLabels, ok := updates["add_labels"].([]string); ok {
				for _, label := range addLabels {
					if err := store.AddLabel(ctx, id, label, actor); err != nil {
						fmt.Fprintf(os.Stderr, "Error adding label %s to %s: %v\n", label, id, err)
						continue
					}
				}
			}

			// Remove labels
			if removeLabels, ok := updates["remove_labels"].([]string); ok {
				for _, label := range removeLabels {
					if err := store.RemoveLabel(ctx, id, label, actor); err != nil {
						fmt.Fprintf(os.Stderr, "Error removing label %s from %s: %v\n", label, id, err)
						continue
					}
				}
			}

			if jsonOutput {
				issue, _ := store.GetIssue(ctx, id)
				if issue != nil {
					updatedIssues = append(updatedIssues, issue)
				}
			} else {
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("%s Updated issue: %s\n", green("✓"), id)
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
	Use:   "edit [id]",
	Short: "Edit an issue field in $EDITOR",
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
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
				os.Exit(1)
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
			fmt.Fprintf(os.Stderr, "Error: No editor found. Set $EDITOR or $VISUAL environment variable.\n")
			os.Exit(1)
		}

		// Get the current issue
		var issue *types.Issue
		var err error

		if daemonClient != nil {
			// Daemon mode
			showArgs := &rpc.ShowArgs{ID: id}
			resp, err := daemonClient.Show(showArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issue %s: %v\n", id, err)
				os.Exit(1)
			}

			issue = &types.Issue{}
			if err := json.Unmarshal(resp.Data, issue); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing issue data: %v\n", err)
				os.Exit(1)
			}
		} else {
			// Direct mode
			issue, err = store.GetIssue(ctx, id)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error fetching issue %s: %v\n", id, err)
				os.Exit(1)
			}
			if issue == nil {
				fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
				os.Exit(1)
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
			fmt.Fprintf(os.Stderr, "Error creating temp file: %v\n", err)
			os.Exit(1)
		}
		tmpPath := tmpFile.Name()
		defer func() { _ = os.Remove(tmpPath) }()

		// Write current value to temp file
		if _, err := tmpFile.WriteString(currentValue); err != nil {
			_ = tmpFile.Close()
			fmt.Fprintf(os.Stderr, "Error writing to temp file: %v\n", err)
			os.Exit(1)
		}
		_ = tmpFile.Close()

		// Open the editor
		editorCmd := exec.Command(editor, tmpPath)
		editorCmd.Stdin = os.Stdin
		editorCmd.Stdout = os.Stdout
		editorCmd.Stderr = os.Stderr

		if err := editorCmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Error running editor: %v\n", err)
			os.Exit(1)
		}

		// Read the edited content
		// #nosec G304 -- tmpPath was created earlier in this function
		editedContent, err := os.ReadFile(tmpPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading edited file: %v\n", err)
			os.Exit(1)
		}

		newValue := string(editedContent)

		// Check if the value changed
		if newValue == currentValue {
			fmt.Println("No changes made")
			return
		}

		// Validate title if editing title
		if fieldToEdit == "title" && strings.TrimSpace(newValue) == "" {
			fmt.Fprintf(os.Stderr, "Error: title cannot be empty\n")
			os.Exit(1)
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
				fmt.Fprintf(os.Stderr, "Error updating issue: %v\n", err)
				os.Exit(1)
			}
		} else {
			// Direct mode
			if err := store.UpdateIssue(ctx, id, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error updating issue: %v\n", err)
				os.Exit(1)
			}
			markDirtyAndScheduleFlush()
		}

		green := color.New(color.FgGreen).SprintFunc()
		fieldName := strings.ReplaceAll(fieldToEdit, "_", " ")
		fmt.Printf("%s Updated %s for issue: %s\n", green("✓"), fieldName, id)
	},
}

var closeCmd = &cobra.Command{
	Use:   "close [id...]",
	Short: "Close one or more issues",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("close")
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			reason = "Closed"
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")

		ctx := rootCtx

		// Resolve partial IDs first
		var resolvedIDs []string
		if daemonClient != nil {
			for _, id := range args {
				resolveArgs := &rpc.ResolveIDArgs{ID: id}
				resp, err := daemonClient.ResolveID(resolveArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error resolving ID %s: %v\n", id, err)
					os.Exit(1)
				}
				var resolvedID string
				if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
					fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
					os.Exit(1)
				}
				resolvedIDs = append(resolvedIDs, resolvedID)
			}
		} else {
			var err error
			resolvedIDs, err = utils.ResolvePartialIDs(ctx, store, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			closedIssues := []*types.Issue{}
			for _, id := range resolvedIDs {
				closeArgs := &rpc.CloseArgs{
					ID:     id,
					Reason: reason,
				}
				resp, err := daemonClient.CloseIssue(closeArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var issue types.Issue
					if err := json.Unmarshal(resp.Data, &issue); err == nil {
						closedIssues = append(closedIssues, &issue)
					}
				} else {
					green := color.New(color.FgGreen).SprintFunc()
					fmt.Printf("%s Closed %s: %s\n", green("✓"), id, reason)
				}
			}

			if jsonOutput && len(closedIssues) > 0 {
				outputJSON(closedIssues)
			}
			return
		}

		// Direct mode
		closedIssues := []*types.Issue{}
		for _, id := range resolvedIDs {
			if err := store.CloseIssue(ctx, id, reason, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Error closing %s: %v\n", id, err)
				continue
			}
			if jsonOutput {
				issue, _ := store.GetIssue(ctx, id)
				if issue != nil {
					closedIssues = append(closedIssues, issue)
				}
			} else {
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("%s Closed %s: %s\n", green("✓"), id, reason)
			}
		}

		// Schedule auto-flush if any issues were closed
		if len(args) > 0 {
			markDirtyAndScheduleFlush()
		}

		if jsonOutput && len(closedIssues) > 0 {
			outputJSON(closedIssues)
		}
	},
}

func init() {
	showCmd.Flags().Bool("json", false, "Output JSON format")
	rootCmd.AddCommand(showCmd)

	updateCmd.Flags().StringP("status", "s", "", "New status")
	registerPriorityFlag(updateCmd, "")
	updateCmd.Flags().String("title", "", "New title")
	registerCommonIssueFlags(updateCmd)
	updateCmd.Flags().String("notes", "", "Additional notes")
	updateCmd.Flags().String("acceptance-criteria", "", "DEPRECATED: use --acceptance")
	_ = updateCmd.Flags().MarkHidden("acceptance-criteria")
	updateCmd.Flags().IntP("estimate", "e", 0, "Time estimate in minutes (e.g., 60 for 1 hour)")
	updateCmd.Flags().StringSlice("add-label", nil, "Add labels (repeatable)")
	updateCmd.Flags().StringSlice("remove-label", nil, "Remove labels (repeatable)")
	updateCmd.Flags().StringSlice("set-labels", nil, "Set labels, replacing all existing (repeatable)")

	updateCmd.Flags().Bool("json", false, "Output JSON format")
	rootCmd.AddCommand(updateCmd)

	editCmd.Flags().Bool("title", false, "Edit the title")
	editCmd.Flags().Bool("description", false, "Edit the description (default)")
	editCmd.Flags().Bool("design", false, "Edit the design notes")
	editCmd.Flags().Bool("notes", false, "Edit the notes")
	editCmd.Flags().Bool("acceptance", false, "Edit the acceptance criteria")
	rootCmd.AddCommand(editCmd)

	closeCmd.Flags().StringP("reason", "r", "", "Reason for closing")
	closeCmd.Flags().Bool("json", false, "Output JSON format")
	rootCmd.AddCommand(closeCmd)
}
