package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
)

var showCmd = &cobra.Command{
	Use:           "show [id...] [--id=<id>...] [--current]",
	Aliases:       []string{"view"},
	GroupID:       "issues",
	Short:         "Show issue details",
	Args:          cobra.ArbitraryArgs, // Allow zero positional args when --id is used
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("show")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runShowProxiedServer(cmd, rootCtx, args)
		}

		showThread, _ := cmd.Flags().GetBool("thread")
		shortMode, _ := cmd.Flags().GetBool("short")
		longMode, _ := cmd.Flags().GetBool("long")
		showRefs, _ := cmd.Flags().GetBool("refs")
		showChildren, _ := cmd.Flags().GetBool("children")
		asOfRef, _ := cmd.Flags().GetString("as-of")
		idFlags, _ := cmd.Flags().GetStringArray("id")
		localTime, _ := cmd.Flags().GetBool("local-time")
		watchMode, _ := cmd.Flags().GetBool("watch")
		currentMode, _ := cmd.Flags().GetBool("current")
		includeDepends, _ := cmd.Flags().GetBool("include-dependents")
		includeComments, _ := cmd.Flags().GetBool("include-comments")
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

		if currentMode {
			if len(args) > 0 {
				return HandleErrorRespectJSON("--current cannot be combined with explicit issue IDs")
			}
			currentID := resolveCurrentIssueID(ctx)
			if currentID == "" {
				return HandleErrorRespectJSON("no current issue found (no in-progress, hooked, or recently touched issues)")
			}
			args = []string{currentID}
		}

		if len(args) == 0 {
			return HandleErrorRespectJSON("at least one issue ID is required (use positional args, --id flag, or --current)")
		}

		if asOfRef != "" {
			return showIssueAsOf(ctx, args, asOfRef, shortMode)
		}

		if watchMode {
			if err := ensureDirectMode("watch mode requires direct database access"); err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			if len(args) != 1 {
				return HandleErrorRespectJSON("watch mode requires exactly one issue ID")
			}
			watchIssue(ctx, args[0])
			return nil
		}

		if showThread {
			if len(args) > 0 {
				result, err := resolveAndGetIssueWithRouting(ctx, store, args[0])
				if result != nil {
					defer result.Close()
				}
				if err == nil && result != nil && result.ResolvedID != "" {
					return showMessageThread(ctx, result.ResolvedID, jsonOutput)
				}
			}
		}

		if showRefs {
			return showIssueRefs(ctx, args, jsonOutput)
		}

		if showChildren {
			return showIssueChildren(ctx, args, jsonOutput, shortMode)
		}

		// Direct mode - use routed resolution for cross-repo lookups
		allDetails := []interface{}{}
		foundCount := 0
		for idx, id := range args {
			// Resolve and get issue with routing (e.g., gt-xyz routes to another rig)
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
				// be-ijck6q: default is count-only (no dependents/comments slice in output).
				// Use --include-dependents / --include-comments to stream the full lists.
				details := &types.IssueDetails{Issue: *issue}
				details.Labels, _ = issueStore.GetLabels(ctx, issue.ID)
				details.Dependencies, _ = issueStore.GetDependenciesWithMetadata(ctx, issue.ID)

				// Aggregate counts — O(1) queries, no row materialization.
				depCount, _ := issueStore.CountDependents(ctx, issue.ID)
				details.DependentCount = &depCount
				depnCount, _ := issueStore.CountDependencies(ctx, issue.ID)
				details.DependencyCount = &depnCount
				cmtCount, _ := issueStore.CountIssueComments(ctx, issue.ID)
				details.CommentCount = &cmtCount

				// --include-dependents: stream via Iter, shallow-copy each item.
				// May be slow on hub beads with many dependents.
				if includeDepends {
					iter, err := issueStore.IterDependentsWithMetadata(ctx, issue.ID)
					if err == nil {
						defer iter.Close() //nolint:errcheck
						var shallowDeps []*types.IssueWithDependencyMetadata
						for iter.Next(ctx) {
							item := iter.Value()
							shallowDeps = append(shallowDeps, &types.IssueWithDependencyMetadata{
								Issue: types.Issue{
									ID:        item.Issue.ID,
									Status:    item.Issue.Status,
									IssueType: item.Issue.IssueType,
									Priority:  item.Issue.Priority,
									Title:     item.Issue.Title,
								},
								DependencyType: item.DependencyType,
							})
						}
						details.Dependents = shallowDeps

						// Epic progress from streamed dependents.
						if issue.IssueType == types.TypeEpic && len(shallowDeps) > 0 {
							total, closed := 0, 0
							for _, dep := range shallowDeps {
								if dep.DependencyType == types.DepParentChild {
									total++
									if dep.Issue.Status == types.StatusClosed {
										closed++
									}
								}
							}
							if total > 0 {
								details.EpicTotalChildren = &total
								details.EpicClosedChildren = &closed
								closeable := total == closed
								details.EpicCloseable = &closeable
							}
						}
					}
				}

				// --include-comments: stream via Iter.
				// May be slow on issues with many comments.
				if includeComments {
					iter, err := issueStore.IterIssueComments(ctx, issue.ID)
					if err == nil {
						defer iter.Close() //nolint:errcheck
						for iter.Next(ctx) {
							details.Comments = append(details.Comments, iter.Value())
						}
					}
				}

				// Compute parent from dependencies.
				for _, dep := range details.Dependencies {
					if dep.DependencyType == types.DepParentChild {
						details.Parent = &dep.ID
						break
					}
				}
				allDetails = append(allDetails, details)
				result.Close()
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

			// Content sections — always show DESCRIPTION header so the user
			// can distinguish "empty" from "hidden" (GH#3336).
			if issue.Description != "" {
				fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), uimd.RenderMarkdown(issue.Description))
			} else {
				fmt.Printf("\n%s\n  %s\n", ui.RenderBold("DESCRIPTION"), ui.RenderMuted("(none)"))
			}
			if issue.Design != "" {
				fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESIGN"), uimd.RenderMarkdown(issue.Design))
			}
			if issue.Notes != "" {
				fmt.Printf("\n%s\n%s\n", ui.RenderBold("NOTES"), uimd.RenderMarkdown(issue.Notes))
			}
			if issue.AcceptanceCriteria != "" {
				fmt.Printf("\n%s\n%s\n", ui.RenderBold("ACCEPTANCE CRITERIA"), uimd.RenderMarkdown(issue.AcceptanceCriteria))
			}

			// Show labels
			labels, _ := issueStore.GetLabels(ctx, issue.ID) // Best effort: show issue even if label fetch fails
			if len(labels) > 0 {
				fmt.Printf("\n%s %s\n", ui.RenderBold("LABELS:"), strings.Join(labels, ", "))
			}

			// Show custom metadata (GH#1406)
			if metaStr := formatIssueCustomMetadata(issue); metaStr != "" {
				fmt.Printf("\n%s\n", metaStr)
			}

			// Collect related issues from both directions for deduplication
			// (relates-to is bidirectional, so we merge and show once)
			relatedSeen := make(map[string]*types.IssueWithDependencyMetadata)

			// Show dependencies - grouped by dependency type for clarity
			depsWithMeta, _ := issueStore.GetDependenciesWithMetadata(ctx, issue.ID) // Best effort: show issue even if deps unavailable

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
					// Epic progress summary
					if issue.IssueType == types.TypeEpic {
						closedCount := 0
						for _, dep := range children {
							if dep.Issue.Status == types.StatusClosed {
								closedCount++
							}
						}
						pct := 0
						if len(children) > 0 {
							pct = (closedCount * 100) / len(children)
						}
						if closedCount == len(children) {
							fmt.Printf("  %s %d/%d complete (%d%%) — eligible for close\n", ui.RenderPass("✓"), closedCount, len(children), pct)
						} else {
							fmt.Printf("  %s %d/%d complete (%d%%)\n", ui.RenderMuted("◐"), closedCount, len(children), pct)
						}
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
					rendered := uimd.RenderMarkdown(comment.Text)
					// TrimRight removes trailing newlines that Glamour adds, preventing extra blank lines
					for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
						fmt.Printf("    %s\n", line)
					}
				}
			}

			// Long mode: show all extended fields
			if longMode {
				fmt.Print(formatIssueLongExtras(issue, formatTime))
			}

			fmt.Println()
			result.Close() // Close routed storage after each iteration
		}

		if jsonOutput {
			if len(allDetails) > 0 {
				if jerr := outputJSON(allDetails); jerr != nil {
					return jerr
				}
			} else {
				return HandleErrorRespectJSON("no issues found matching the provided IDs")
			}
		} else if foundCount > 0 {
			maybeShowTip(store)
		} else {
			if len(args) > 0 {
				SetLastTouchedID(args[0])
			}
			return SilentExit()
		}

		if len(args) > 0 {
			SetLastTouchedID(args[0])
		}
		return nil
	},
}

// shallowDependentsForJSON returns a copy of raw with each embedded Issue
// stripped down to identity-and-shape fields (ID, Status, IssueType, Priority,
// Title). The heavy fields (Description, Design, Notes, AcceptanceCriteria,
// metadata blobs, etc.) are dropped.
//
// be-4d36f2: hub beads with thousands of dependents previously caused
// `bd show --json <hub>` to allocate 5-13 GB while marshaling full Issue
// records into JSON. The shallow shape preserves what callers actually
// consume (counts, status, type) and drops what they don't (free-form
// text fields). On a 4-dependent hub this trims output ~60%; on a hub
// with thousands of dependents, savings scale linearly.
func shallowDependentsForJSON(raw []*types.IssueWithDependencyMetadata) []*types.IssueWithDependencyMetadata {
	if len(raw) == 0 {
		return nil
	}
	shallow := make([]*types.IssueWithDependencyMetadata, 0, len(raw))
	for _, dep := range raw {
		if dep == nil {
			continue
		}
		shallow = append(shallow, &types.IssueWithDependencyMetadata{
			Issue: types.Issue{
				ID:        dep.Issue.ID,
				Status:    dep.Issue.Status,
				IssueType: dep.Issue.IssueType,
				Priority:  dep.Issue.Priority,
				Title:     dep.Issue.Title,
			},
			DependencyType: dep.DependencyType,
		})
	}
	return shallow
}

func init() {
	showCmd.Flags().Bool("thread", false, "Show full conversation thread (for messages)")
	showCmd.Flags().Bool("short", false, "Show compact one-line output per issue")
	showCmd.Flags().Bool("long", false, "Show all available fields (extended metadata, agent identity, gate fields, etc.)")
	showCmd.Flags().Bool("refs", false, "Show issues that reference this issue (reverse lookup)")
	showCmd.Flags().Bool("children", false, "Show only the children of this issue")
	showCmd.Flags().String("as-of", "", "Show issue as it existed at a specific commit hash or branch (requires Dolt)")
	showCmd.Flags().StringArray("id", nil, "Issue ID (use for IDs that look like flags, e.g., --id=gt--xyz)")
	showCmd.Flags().Bool("local-time", false, "Show timestamps in local time instead of UTC")
	showCmd.Flags().BoolP("watch", "w", false, "Watch for changes and auto-refresh display")
	showCmd.Flags().Bool("current", false, "Show the currently active issue (in-progress, hooked, or last touched)")
	showCmd.Flags().Bool("include-dependents", false, "Stream full dependent issues in JSON output (--json only; may be slow on hub beads)")
	showCmd.Flags().Bool("include-comments", false, "Stream full comment bodies in JSON output (--json only; may be slow on issues with many comments)")
	showCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(showCmd)
}

// resolveCurrentIssueID determines the current active issue for the agent.
// Priority: in-progress assigned to actor > hooked > last touched.
func resolveCurrentIssueID(ctx context.Context) string {
	if store == nil {
		// No store — fall back to last touched
		return GetLastTouchedID()
	}

	currentActor := getActorWithGit()

	// 1. In-progress issues assigned to current actor
	if currentActor != "" {
		status := types.StatusInProgress
		filter := types.IssueFilter{
			Status:   &status,
			Assignee: &currentActor,
		}
		issues, err := store.SearchIssues(ctx, "", filter)
		if err == nil && len(issues) > 0 {
			return issues[0].ID
		}
	}

	// 2. Hooked issues assigned to current actor
	if currentActor != "" {
		status := types.StatusHooked
		filter := types.IssueFilter{
			Status:   &status,
			Assignee: &currentActor,
		}
		issues, err := store.SearchIssues(ctx, "", filter)
		if err == nil && len(issues) > 0 {
			return issues[0].ID
		}
	}

	// 3. Last touched issue (fallback)
	return GetLastTouchedID()
}
