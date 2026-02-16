package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
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
		watchMode, _ := cmd.Flags().GetBool("watch")
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

		// Handle --watch mode (GH#654)
		// Watch mode requires direct store access for file watching
		if watchMode {
			if err := ensureDirectMode("watch mode requires direct database access"); err != nil {
				FatalErrorRespectJSON("%v", err)
			}
			if len(args) != 1 {
				FatalErrorRespectJSON("watch mode requires exactly one issue ID")
			}
			watchIssue(ctx, args[0])
			return
		}


		// Note: Direct mode uses resolveAndGetIssueWithRouting for prefix-based routing

		// Handle --thread flag: show full conversation thread
		if showThread {
			if len(args) > 0 {
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
			showIssueRefs(ctx, args, jsonOutput)
			return
		}

		// Handle --children flag: show only children of this issue
		if showChildren {
			showIssueChildren(ctx, args, jsonOutput, shortMode)
			return
		}

		// Direct mode - use routed resolution for cross-repo lookups
		allDetails := []interface{}{}
		foundCount := 0
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
			foundCount++

			if shortMode {
				fmt.Println(formatShortIssue(issue))
				result.Close()
				continue
			}

			if jsonOutput {
				// Include labels, dependencies (with metadata), dependents (with metadata), and comments in JSON output
				details := &types.IssueDetails{Issue: *issue}
				details.Labels, _ = issueStore.GetLabels(ctx, issue.ID) // Best effort: show issue even if label fetch fails

				// Get dependencies with metadata (dependency_type field)
				details.Dependencies, _ = issueStore.GetDependenciesWithMetadata(ctx, issue.ID) // Best effort: show issue even if deps unavailable
				// Resolve external deps via routing (bd-k0pfm)
				if externalDeps, err := resolveExternalDepsViaRouting(ctx, issueStore, issue.ID); err == nil {
					details.Dependencies = append(details.Dependencies, externalDeps...)
				}
				details.Dependents, _ = issueStore.GetDependentsWithMetadata(ctx, issue.ID) // Best effort: show issue even if dependents unavailable

				details.Comments, _ = issueStore.GetIssueComments(ctx, issue.ID) // Best effort: show issue even if comments unavailable
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
				fmt.Printf("\n%s\n", formatIssueHeader(issue))
			} else {
				fmt.Printf("%s\n", formatIssueHeader(issue))
			}

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
			labels, _ := issueStore.GetLabels(ctx, issue.ID) // Best effort: show issue even if label fetch fails
			if len(labels) > 0 {
				fmt.Printf("\n%s %s\n", ui.RenderBold("LABELS:"), strings.Join(labels, ", "))
			}

			// Collect related issues from both directions for deduplication
			// (relates-to is bidirectional, so we merge and show once)
			relatedSeen := make(map[string]*types.IssueWithDependencyMetadata)

			// Show dependencies - grouped by dependency type for clarity
			depsWithMeta, _ := issueStore.GetDependenciesWithMetadata(ctx, issue.ID) // Best effort: show issue even if deps unavailable

			// Resolve external deps via routing (bd-k0pfm)
			// GetDependenciesWithMetadata JOINs on issues table, so external refs
			// (e.g., "external:gastown:gt-42zaq") are silently dropped.
			// Resolve them via prefix routes and merge into the dep list.
			if externalDeps, err := resolveExternalDepsViaRouting(ctx, issueStore, issue.ID); err == nil {
				depsWithMeta = append(depsWithMeta, externalDeps...)
			}

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
			dependentsWithMeta, _ := issueStore.GetDependentsWithMetadata(ctx, issue.ID) // Best effort: show issue even if dependents unavailable
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
			comments, _ := issueStore.GetIssueComments(ctx, issue.ID) // Best effort: show issue even if comments unavailable
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

		if jsonOutput {
			if len(allDetails) > 0 {
				outputJSON(allDetails)
			} else {
				// No issues found - exit non-zero with structured JSON error
				// so downstream consumers (e.g., gt bd move) get a proper error
				// instead of empty stdout causing "unexpected end of JSON input"
				FatalErrorRespectJSON("no issues found matching the provided IDs")
			}
		} else if foundCount > 0 {
			// Show tip after successful show (non-JSON mode)
			maybeShowTip(store)
		} else {
			os.Exit(1)
		}

		// Track first shown issue as last touched
		if len(args) > 0 {
			SetLastTouchedID(args[0])
		}
	},
}

func init() {
	showCmd.Flags().Bool("thread", false, "Show full conversation thread (for messages)")
	showCmd.Flags().Bool("short", false, "Show compact one-line output per issue")
	showCmd.Flags().Bool("refs", false, "Show issues that reference this issue (reverse lookup)")
	showCmd.Flags().Bool("children", false, "Show only the children of this issue")
	showCmd.Flags().String("as-of", "", "Show issue as it existed at a specific commit hash or branch (requires Dolt)")
	showCmd.Flags().StringArray("id", nil, "Issue ID (use for IDs that look like flags, e.g., --id=gt--xyz)")
	showCmd.Flags().Bool("local-time", false, "Show timestamps in local time instead of UTC")
	showCmd.Flags().BoolP("watch", "w", false, "Watch for changes and auto-refresh display")
	showCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(showCmd)
}
