package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var showCmd = &cobra.Command{
	Use:     "show [id...] [--id=<id>...]",
	Aliases: []string{"view"},
	GroupID: "issues",
	Short:   "Show issue details",
	Args:    cobra.ArbitraryArgs, // Allow zero positional args when --id is used
	Run: func(cmd *cobra.Command, args []string) {
		showThread, _ := cmd.Flags().GetBool("thread")
		shortMode, _ := cmd.Flags().GetBool("short")
		showRefs, _ := cmd.Flags().GetBool("refs")
		showChildren, _ := cmd.Flags().GetBool("children")
		asOfRef, _ := cmd.Flags().GetString("as-of")
		idFlags, _ := cmd.Flags().GetStringArray("id")
		localTime, _ := cmd.Flags().GetBool("local-time")
		ctx := rootCtx

		// Helper to format timestamp based on --local-time flag
		formatTime := func(t time.Time) string {
			if localTime {
				t = t.Local()
			}
			return t.Format("2006-01-02 15:04")
		}

		// Merge --id flag values with positional args
		// This allows IDs that look like flags (e.g., --xyz or gt--abc) to be passed safely
		args = append(args, idFlags...)

		// Validate that at least one ID is provided
		if len(args) == 0 {
			FatalErrorRespectJSON("at least one issue ID is required (use positional args or --id flag)")
		}

		// Handle --as-of flag: show issue at a specific point in history
		if asOfRef != "" {
			showIssueAsOf(ctx, args, asOfRef, shortMode)
			return
		}

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

		// Handle --children flag: show only children of this issue
		if showChildren {
			showIssueChildren(ctx, args, resolvedIDs, routedArgs, jsonOutput, shortMode)
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
					details.Dependencies, _ = issueStore.GetDependenciesWithMetadata(ctx, issue.ID)
					details.Dependents, _ = issueStore.GetDependentsWithMetadata(ctx, issue.ID)
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
						fmt.Println("\n" + ui.RenderMuted(strings.Repeat("─", 60)))
					}
					// Tufte-aligned header: STATUS_ICON ID · Title   [Priority · STATUS]
					fmt.Printf("\n%s\n", formatIssueHeader(issue))
					// Metadata: Owner · Type | Created · Updated
					fmt.Println(formatIssueMetadata(issue))
					if issue.Description != "" {
						fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), ui.RenderMarkdown(issue.Description))
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
						fmt.Println("\n" + ui.RenderMuted(strings.Repeat("─", 60)))
					}
					displayIdx++

					// Tufte-aligned header: STATUS_ICON ID · Title   [Priority · STATUS]
					fmt.Printf("\n%s\n", formatIssueHeader(issue))

					// Metadata: Owner · Type | Created · Updated
					fmt.Println(formatIssueMetadata(issue))

					// Compaction info (if applicable)
					if issue.CompactionLevel > 0 {
						fmt.Println()
						if issue.OriginalSize > 0 {
							currentSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
							saved := issue.OriginalSize - currentSize
							if saved > 0 {
								reduction := float64(saved) / float64(issue.OriginalSize) * 100
								fmt.Printf("📊 %d → %d bytes (%.0f%% reduction)\n",
									issue.OriginalSize, currentSize, reduction)
							}
						}
					}

					// Content sections
					if issue.Description != "" {
						fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), ui.RenderMarkdown(issue.Description))
					}
					if issue.Design != "" {
						fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESIGN"), ui.RenderMarkdown(issue.Design))
					}
					if issue.Notes != "" {
						fmt.Printf("\n%s\n%s\n", ui.RenderBold("NOTES"), ui.RenderMarkdown(issue.Notes))
					}
					if issue.AcceptanceCriteria != "" {
						fmt.Printf("\n%s\n%s\n", ui.RenderBold("ACCEPTANCE CRITERIA"), ui.RenderMarkdown(issue.AcceptanceCriteria))
					}

					if len(details.Labels) > 0 {
						fmt.Printf("\n%s %s\n", ui.RenderBold("LABELS:"), strings.Join(details.Labels, ", "))
					}

					// Collect related issues from both directions for deduplication
					// (relates-to is bidirectional, so we merge and show once)
					relatedSeen := make(map[string]*types.IssueWithDependencyMetadata)

					// Dependencies grouped by type with semantic colors
					if len(details.Dependencies) > 0 {
						var blocks, parent, discovered []*types.IssueWithDependencyMetadata
						for _, dep := range details.Dependencies {
							switch dep.DependencyType {
							case types.DepBlocks:
								blocks = append(blocks, dep)
							case types.DepParentChild:
								parent = append(parent, dep)
							case types.DepRelated, types.DepRelatesTo:
								relatedSeen[dep.ID] = dep
							case types.DepDiscoveredFrom:
								discovered = append(discovered, dep)
							default:
								blocks = append(blocks, dep)
							}
						}

						if len(parent) > 0 {
							fmt.Printf("\n%s\n", ui.RenderBold("PARENT"))
							for _, dep := range parent {
								fmt.Println(formatDependencyLine("↑", dep))
							}
						}
						if len(blocks) > 0 {
							fmt.Printf("\n%s\n", ui.RenderBold("DEPENDS ON"))
							for _, dep := range blocks {
								fmt.Println(formatDependencyLine("→", dep))
							}
						}
						if len(discovered) > 0 {
							fmt.Printf("\n%s\n", ui.RenderBold("DISCOVERED FROM"))
							for _, dep := range discovered {
								fmt.Println(formatDependencyLine("◊", dep))
							}
						}
					}

					// Dependents grouped by type with semantic colors
					if len(details.Dependents) > 0 {
						var blocks, children, discovered []*types.IssueWithDependencyMetadata
						for _, dep := range details.Dependents {
							switch dep.DependencyType {
							case types.DepBlocks:
								blocks = append(blocks, dep)
							case types.DepParentChild:
								children = append(children, dep)
							case types.DepRelated, types.DepRelatesTo:
								relatedSeen[dep.ID] = dep
							case types.DepDiscoveredFrom:
								discovered = append(discovered, dep)
							default:
								blocks = append(blocks, dep)
							}
						}

						if len(children) > 0 {
							fmt.Printf("\n%s\n", ui.RenderBold("CHILDREN"))
							for _, dep := range children {
								fmt.Println(formatDependencyLine("↳", dep))
							}
						}
						if len(blocks) > 0 {
							fmt.Printf("\n%s\n", ui.RenderBold("BLOCKS"))
							for _, dep := range blocks {
								fmt.Println(formatDependencyLine("←", dep))
							}
						}
						if len(discovered) > 0 {
							fmt.Printf("\n%s\n", ui.RenderBold("DISCOVERED"))
							for _, dep := range discovered {
								fmt.Println(formatDependencyLine("◊", dep))
							}
						}
					}

					// Print deduplicated RELATED section (bidirectional links shown once)
					if len(relatedSeen) > 0 {
						fmt.Printf("\n%s\n", ui.RenderBold("RELATED"))
						for _, dep := range relatedSeen {
							fmt.Println(formatDependencyLine("↔", dep))
						}
					}

					if len(details.Comments) > 0 {
						fmt.Printf("\n%s\n", ui.RenderBold("COMMENTS"))
						for _, comment := range details.Comments {
							fmt.Printf("  %s %s\n", ui.RenderMuted(formatTime(comment.CreatedAt)), comment.Author)
							rendered := ui.RenderMarkdown(comment.Text)
							// TrimRight removes trailing newlines that Glamour adds, preventing extra blank lines
							for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
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
				details.Dependencies, _ = issueStore.GetDependenciesWithMetadata(ctx, issue.ID)
				details.Dependents, _ = issueStore.GetDependentsWithMetadata(ctx, issue.ID)

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
				fmt.Println("\n" + ui.RenderMuted(strings.Repeat("─", 60)))
			}

			// Tufte-aligned header: STATUS_ICON ID · Title   [Priority · STATUS]
			fmt.Printf("\n%s\n", formatIssueHeader(issue))

			// Metadata: Owner · Type | Created · Updated
			fmt.Println(formatIssueMetadata(issue))

			// Compaction info (if applicable)
			if issue.CompactionLevel > 0 {
				fmt.Println()
				if issue.OriginalSize > 0 {
					currentSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
					saved := issue.OriginalSize - currentSize
					if saved > 0 {
						reduction := float64(saved) / float64(issue.OriginalSize) * 100
						fmt.Printf("📊 %d → %d bytes (%.0f%% reduction)\n",
							issue.OriginalSize, currentSize, reduction)
					}
				}
			}

			// Content sections
			if issue.Description != "" {
				fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), ui.RenderMarkdown(issue.Description))
			}
			if issue.Design != "" {
				fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESIGN"), ui.RenderMarkdown(issue.Design))
			}
			if issue.Notes != "" {
				fmt.Printf("\n%s\n%s\n", ui.RenderBold("NOTES"), ui.RenderMarkdown(issue.Notes))
			}
			if issue.AcceptanceCriteria != "" {
				fmt.Printf("\n%s\n%s\n", ui.RenderBold("ACCEPTANCE CRITERIA"), ui.RenderMarkdown(issue.AcceptanceCriteria))
			}

			// Show labels
			labels, _ := issueStore.GetLabels(ctx, issue.ID)
			if len(labels) > 0 {
				fmt.Printf("\n%s %s\n", ui.RenderBold("LABELS:"), strings.Join(labels, ", "))
			}

			// Collect related issues from both directions for deduplication
			// (relates-to is bidirectional, so we merge and show once)
			relatedSeen := make(map[string]*types.IssueWithDependencyMetadata)

			// Show dependencies - grouped by dependency type for clarity
			depsWithMeta, _ := issueStore.GetDependenciesWithMetadata(ctx, issue.ID)
			if len(depsWithMeta) > 0 {
				// Group by dependency type
				var blocks, parent, discovered []*types.IssueWithDependencyMetadata
				for _, dep := range depsWithMeta {
					switch dep.DependencyType {
					case types.DepBlocks:
						blocks = append(blocks, dep)
					case types.DepParentChild:
						parent = append(parent, dep)
					case types.DepRelated, types.DepRelatesTo:
						relatedSeen[dep.ID] = dep
					case types.DepDiscoveredFrom:
						discovered = append(discovered, dep)
					default:
						blocks = append(blocks, dep) // Default to blocks
					}
				}

				if len(parent) > 0 {
					fmt.Printf("\n%s\n", ui.RenderBold("PARENT"))
					for _, dep := range parent {
						fmt.Println(formatDependencyLine("↑", dep))
					}
				}
				if len(blocks) > 0 {
					fmt.Printf("\n%s\n", ui.RenderBold("DEPENDS ON"))
					for _, dep := range blocks {
						fmt.Println(formatDependencyLine("→", dep))
					}
				}
				if len(discovered) > 0 {
					fmt.Printf("\n%s\n", ui.RenderBold("DISCOVERED FROM"))
					for _, dep := range discovered {
						fmt.Println(formatDependencyLine("◊", dep))
					}
				}
			}

			// Show dependents - grouped by dependency type for clarity
			dependentsWithMeta, _ := issueStore.GetDependentsWithMetadata(ctx, issue.ID)
			if len(dependentsWithMeta) > 0 {
				// Group by dependency type
				var blocks, children, discovered []*types.IssueWithDependencyMetadata
				for _, dep := range dependentsWithMeta {
					switch dep.DependencyType {
					case types.DepBlocks:
						blocks = append(blocks, dep)
					case types.DepParentChild:
						children = append(children, dep)
					case types.DepRelated, types.DepRelatesTo:
						relatedSeen[dep.ID] = dep
					case types.DepDiscoveredFrom:
						discovered = append(discovered, dep)
					default:
						blocks = append(blocks, dep) // Default to blocks
					}
				}

				if len(children) > 0 {
					fmt.Printf("\n%s\n", ui.RenderBold("CHILDREN"))
					for _, dep := range children {
						fmt.Println(formatDependencyLine("↳", dep))
					}
				}
				if len(blocks) > 0 {
					fmt.Printf("\n%s\n", ui.RenderBold("BLOCKS"))
					for _, dep := range blocks {
						fmt.Println(formatDependencyLine("←", dep))
					}
				}
				if len(discovered) > 0 {
					fmt.Printf("\n%s\n", ui.RenderBold("DISCOVERED"))
					for _, dep := range discovered {
						fmt.Println(formatDependencyLine("◊", dep))
					}
				}
			}

			// Print deduplicated RELATED section (bidirectional links shown once)
			if len(relatedSeen) > 0 {
				fmt.Printf("\n%s\n", ui.RenderBold("RELATED"))
				for _, dep := range relatedSeen {
					fmt.Println(formatDependencyLine("↔", dep))
				}
			}

			// Show comments
			comments, _ := issueStore.GetIssueComments(ctx, issue.ID)
			if len(comments) > 0 {
				fmt.Printf("\n%s\n", ui.RenderBold("COMMENTS"))
				for _, comment := range comments {
					fmt.Printf("  %s %s\n", ui.RenderMuted(formatTime(comment.CreatedAt)), comment.Author)
					rendered := ui.RenderMarkdown(comment.Text)
					// TrimRight removes trailing newlines that Glamour adds, preventing extra blank lines
					for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
						fmt.Printf("    %s\n", line)
					}
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
// Format: STATUS_ICON ID PRIORITY [Type] Title
func formatShortIssue(issue *types.Issue) string {
	statusIcon := ui.RenderStatusIcon(string(issue.Status))
	priorityTag := ui.RenderPriority(issue.Priority)

	// Type badge only for notable types
	typeBadge := ""
	switch issue.IssueType {
	case "epic":
		typeBadge = ui.TypeEpicStyle.Render("[epic]") + " "
	case "bug":
		typeBadge = ui.TypeBugStyle.Render("[bug]") + " "
	}

	// Closed issues: entire line is muted
	if issue.Status == types.StatusClosed {
		return fmt.Sprintf("%s %s %s %s%s",
			statusIcon,
			ui.RenderMuted(issue.ID),
			ui.RenderMuted(fmt.Sprintf("● P%d", issue.Priority)),
			ui.RenderMuted(string(issue.IssueType)),
			ui.RenderMuted(" "+issue.Title))
	}

	return fmt.Sprintf("%s %s %s %s%s", statusIcon, issue.ID, priorityTag, typeBadge, issue.Title)
}

// formatIssueHeader returns the Tufte-aligned header line
// Format: ID · Title   [Priority · STATUS]
// All elements in bd show get semantic colors since focus is on one issue
func formatIssueHeader(issue *types.Issue) string {
	// Get status icon and style
	statusIcon := ui.RenderStatusIcon(string(issue.Status))
	statusStyle := ui.GetStatusStyle(string(issue.Status))
	statusStr := statusStyle.Render(strings.ToUpper(string(issue.Status)))

	// Priority with semantic color (includes ● icon)
	priorityTag := ui.RenderPriority(issue.Priority)

	// Type badge for notable types
	typeBadge := ""
	switch issue.IssueType {
	case "epic":
		typeBadge = " " + ui.TypeEpicStyle.Render("[EPIC]")
	case "bug":
		typeBadge = " " + ui.TypeBugStyle.Render("[BUG]")
	}

	// Compaction indicator
	tierEmoji := ""
	switch issue.CompactionLevel {
	case 1:
		tierEmoji = " 🗜️"
	case 2:
		tierEmoji = " 📦"
	}

	// Build header: STATUS_ICON ID · Title   [Priority · STATUS]
	idStyled := ui.RenderAccent(issue.ID)
	return fmt.Sprintf("%s %s%s · %s%s   [%s · %s]",
		statusIcon, idStyled, typeBadge, issue.Title, tierEmoji, priorityTag, statusStr)
}

// formatIssueMetadata returns the metadata line(s) with grouped info
// Format: Owner: user · Type: task
//
//	Created: 2026-01-06 · Updated: 2026-01-08
func formatIssueMetadata(issue *types.Issue) string {
	var lines []string

	// Line 1: Owner/Assignee · Type
	metaParts := []string{}
	if issue.CreatedBy != "" {
		metaParts = append(metaParts, fmt.Sprintf("Owner: %s", issue.CreatedBy))
	}
	if issue.Assignee != "" {
		metaParts = append(metaParts, fmt.Sprintf("Assignee: %s", issue.Assignee))
	}

	// Type with semantic color
	typeStr := string(issue.IssueType)
	switch issue.IssueType {
	case "epic":
		typeStr = ui.TypeEpicStyle.Render("epic")
	case "bug":
		typeStr = ui.TypeBugStyle.Render("bug")
	}
	metaParts = append(metaParts, fmt.Sprintf("Type: %s", typeStr))

	if len(metaParts) > 0 {
		lines = append(lines, strings.Join(metaParts, " · "))
	}

	// Line 2: Created · Updated · Due/Defer
	timeParts := []string{}
	timeParts = append(timeParts, fmt.Sprintf("Created: %s", issue.CreatedAt.Format("2006-01-02")))
	timeParts = append(timeParts, fmt.Sprintf("Updated: %s", issue.UpdatedAt.Format("2006-01-02")))

	if issue.DueAt != nil {
		timeParts = append(timeParts, fmt.Sprintf("Due: %s", issue.DueAt.Format("2006-01-02")))
	}
	if issue.DeferUntil != nil {
		timeParts = append(timeParts, fmt.Sprintf("Deferred: %s", issue.DeferUntil.Format("2006-01-02")))
	}
	if len(timeParts) > 0 {
		lines = append(lines, strings.Join(timeParts, " · "))
	}

	// Line 3: Close reason (if closed)
	if issue.Status == types.StatusClosed && issue.CloseReason != "" {
		lines = append(lines, ui.RenderMuted(fmt.Sprintf("Close reason: %s", issue.CloseReason)))
	}

	// Line 4: External ref (if exists)
	if issue.ExternalRef != nil && *issue.ExternalRef != "" {
		lines = append(lines, fmt.Sprintf("External: %s", *issue.ExternalRef))
	}
	if issue.SpecID != "" {
		lines = append(lines, fmt.Sprintf("Spec: %s", issue.SpecID))
	}

	// Line 5: Wisp type (if ephemeral with classification)
	if issue.Ephemeral && issue.WispType != "" {
		lines = append(lines, fmt.Sprintf("Wisp type: %s", ui.RenderMuted(string(issue.WispType))))
	}

	return strings.Join(lines, "\n")
}

// formatDependencyLine formats a single dependency with semantic colors
// Closed items get entire row muted - the work is done, no need for attention
func formatDependencyLine(prefix string, dep *types.IssueWithDependencyMetadata) string {
	// Status icon (always rendered with semantic color)
	statusIcon := ui.GetStatusIcon(string(dep.Status))

	// Closed items: mute entire row since the work is complete
	if dep.Status == types.StatusClosed {
		return fmt.Sprintf("  %s %s %s: %s %s",
			prefix, statusIcon,
			ui.RenderMuted(dep.ID),
			ui.RenderMuted(dep.Title),
			ui.RenderMuted(fmt.Sprintf("● P%d", dep.Priority)))
	}

	// Active items: ID with status color, priority with semantic color
	style := ui.GetStatusStyle(string(dep.Status))
	idStr := style.Render(dep.ID)
	priorityTag := ui.RenderPriority(dep.Priority)

	// Type indicator for epics/bugs
	typeStr := ""
	if dep.IssueType == "epic" {
		typeStr = ui.TypeEpicStyle.Render("(EPIC)") + " "
	} else if dep.IssueType == "bug" {
		typeStr = ui.TypeBugStyle.Render("(BUG)") + " "
	}

	return fmt.Sprintf("  %s %s %s: %s%s %s", prefix, statusIcon, idStr, typeStr, dep.Title, priorityTag)
}

// formatSimpleDependencyLine formats a dependency without metadata (fallback)
// Closed items get entire row muted - the work is done, no need for attention
func formatSimpleDependencyLine(prefix string, dep *types.Issue) string {
	statusIcon := ui.GetStatusIcon(string(dep.Status))

	// Closed items: mute entire row since the work is complete
	if dep.Status == types.StatusClosed {
		return fmt.Sprintf("  %s %s %s: %s %s",
			prefix, statusIcon,
			ui.RenderMuted(dep.ID),
			ui.RenderMuted(dep.Title),
			ui.RenderMuted(fmt.Sprintf("● P%d", dep.Priority)))
	}

	// Active items: use semantic colors
	style := ui.GetStatusStyle(string(dep.Status))
	idStr := style.Render(dep.ID)
	priorityTag := ui.RenderPriority(dep.Priority)

	return fmt.Sprintf("  %s %s %s: %s %s", prefix, statusIcon, idStr, dep.Title, priorityTag)
}

// showIssueRefs displays issues that reference the given issue(s), grouped by relationship type
func showIssueRefs(ctx context.Context, args []string, resolvedIDs []string, routedArgs []string, jsonOut bool) {
	// Collect all refs for all issues
	allRefs := make(map[string][]*types.IssueWithDependencyMetadata)

	// Process each issue
	processIssue := func(issueID string, issueStore storage.Storage) error {
		refs, err := issueStore.GetDependentsWithMetadata(ctx, issueID)
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
			// Use factory to respect backend configuration (bd-m2jr: SQLite fallback fix)
			dbStore, err := factory.NewFromConfig(ctx, filepath.Dir(dbPath))
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
// Closed items get entire row muted - the work is done, no need for attention
func displayRefGroup(depType types.DependencyType, refs []*types.IssueWithDependencyMetadata) {
	// Get emoji for type
	emoji := getRefTypeEmoji(depType)
	fmt.Printf("\n  %s %s (%d):\n", emoji, depType, len(refs))

	for _, ref := range refs {
		// Closed items: mute entire row since the work is complete
		if ref.Status == types.StatusClosed {
			fmt.Printf("    %s: %s %s\n",
				ui.RenderMuted(ref.ID),
				ui.RenderMuted(ref.Title),
				ui.RenderMuted(fmt.Sprintf("[P%d - %s]", ref.Priority, ref.Status)))
			continue
		}

		// Active items: color ID based on status
		var idStr string
		switch ref.Status {
		case types.StatusOpen:
			idStr = ui.StatusOpenStyle.Render(ref.ID)
		case types.StatusInProgress:
			idStr = ui.StatusInProgressStyle.Render(ref.ID)
		case types.StatusBlocked:
			idStr = ui.StatusBlockedStyle.Render(ref.ID)
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

// showIssueChildren displays only the children of the specified issue(s)
func showIssueChildren(ctx context.Context, args []string, resolvedIDs []string, routedArgs []string, jsonOut bool, shortMode bool) {
	// Collect all children for all issues
	allChildren := make(map[string][]*types.IssueWithDependencyMetadata)

	// Process each issue to get its children
	processIssue := func(issueID string, issueStore storage.Storage) error {
		// Initialize entry so "no children" message can be shown
		if _, exists := allChildren[issueID]; !exists {
			allChildren[issueID] = []*types.IssueWithDependencyMetadata{}
		}

		// Get all dependents with metadata so we can filter for children
		refs, err := issueStore.GetDependentsWithMetadata(ctx, issueID)
		if err != nil {
			return err
		}
		// Filter for only parent-child relationships
		for _, ref := range refs {
			if ref.DependencyType == types.DepParentChild {
				allChildren[issueID] = append(allChildren[issueID], ref)
			}
		}
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
			fmt.Fprintf(os.Stderr, "Error getting children for %s: %v\n", id, err)
		}
		result.Close()
	}

	// Handle resolved IDs (daemon mode)
	if daemonClient != nil {
		for _, id := range resolvedIDs {
			// Need to open direct connection for GetDependentsWithMetadata
			// Use factory to respect backend configuration (bd-m2jr: SQLite fallback fix)
			dbStore, err := factory.NewFromConfig(ctx, filepath.Dir(dbPath))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error opening database: %v\n", err)
				continue
			}
			if err := processIssue(id, dbStore); err != nil {
				fmt.Fprintf(os.Stderr, "Error getting children for %s: %v\n", id, err)
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
				fmt.Fprintf(os.Stderr, "Error getting children for %s: %v\n", id, err)
			}
			result.Close()
		}
	}

	// Output results
	if jsonOut {
		outputJSON(allChildren)
		return
	}

	// Display children
	for issueID, children := range allChildren {
		if len(children) == 0 {
			fmt.Printf("%s: No children found\n", ui.RenderAccent(issueID))
			continue
		}

		fmt.Printf("%s Children of %s (%d):\n", ui.RenderAccent("↳"), issueID, len(children))
		for _, child := range children {
			if shortMode {
				fmt.Printf("  %s\n", formatShortIssue(&child.Issue))
			} else {
				fmt.Println(formatDependencyLine("↳", child))
			}
		}
		fmt.Println()
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

// showIssueAsOf displays issues as they existed at a specific commit or branch ref.
// This requires a versioned storage backend (e.g., Dolt).
func showIssueAsOf(ctx context.Context, args []string, ref string, shortMode bool) {
	// Check if storage supports versioning
	vs, ok := storage.AsVersioned(store)
	if !ok {
		FatalErrorRespectJSON("--as-of requires Dolt backend (current backend does not support versioning)")
	}

	var allIssues []*types.Issue
	for idx, id := range args {
		issue, err := vs.AsOf(ctx, id, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching %s as of %s: %v\n", id, ref, err)
			continue
		}
		if issue == nil {
			fmt.Fprintf(os.Stderr, "Issue %s did not exist at %s\n", id, ref)
			continue
		}

		if shortMode {
			fmt.Println(formatShortIssue(issue))
			continue
		}

		if jsonOutput {
			allIssues = append(allIssues, issue)
			continue
		}

		if idx > 0 {
			fmt.Println("\n" + ui.RenderMuted(strings.Repeat("-", 60)))
		}

		// Display header with ref indicator
		fmt.Printf("\n%s (as of %s)\n", formatIssueHeader(issue), ui.RenderMuted(ref))
		fmt.Println(formatIssueMetadata(issue))

		if issue.Description != "" {
			fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), ui.RenderMarkdown(issue.Description))
		}
		fmt.Println()
	}

	if jsonOutput && len(allIssues) > 0 {
		outputJSON(allIssues)
	}
}

func init() {
	showCmd.Flags().Bool("thread", false, "Show full conversation thread (for messages)")
	showCmd.Flags().Bool("short", false, "Show compact one-line output per issue")
	showCmd.Flags().Bool("refs", false, "Show issues that reference this issue (reverse lookup)")
	showCmd.Flags().Bool("children", false, "Show only the children of this issue")
	showCmd.Flags().String("as-of", "", "Show issue as it existed at a specific commit hash or branch (requires Dolt)")
	showCmd.Flags().StringArray("id", nil, "Issue ID (use for IDs that look like flags, e.g., --id=gt--xyz)")
	showCmd.Flags().Bool("local-time", false, "Show timestamps in local time instead of UTC")
	showCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(showCmd)
}
