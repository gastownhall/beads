package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
	"github.com/steveyegge/beads/issueops"
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
		briefDeps, _ := cmd.Flags().GetBool("brief-deps")
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
				// Shaping the detail view belongs to the reader role, not the
				// CLI: the count-only default (be-ijck6q), the
				// comments-omitted flag (ga-clgh), and the shallow dependent
				// rows (be-4d36f2) have to read the same from every frontend,
				// and going through the accessor is what makes that true by
				// construction rather than by everyone remembering to call the
				// same helper.
				//
				// The id handed over is the CANONICAL one this command already
				// resolved. Fuzzy and cross-repo resolution stay here on
				// purpose: an affordance that can answer with a different
				// issue than the caller named has no place on a contract an
				// unattended HTTP client also calls.
				rd, rerr := issueStore.IssueReader()
				if rerr != nil {
					result.Close()
					return HandleErrorRespectJSON("%v", rerr)
				}
				details, derr := rd.Get(ctx, showGetRequest(issue.ID, includeDepends, includeComments, briefDeps))
				if derr != nil {
					result.Close()
					return HandleErrorRespectJSON("%v", derr)
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
			for _, sec := range groupDepSections(depsWithMeta, true, relatedSeen) {
				printDepSection(sec)
			}

			// Show dependents - grouped by dependency type for clarity
			dependentsWithMeta, _ := issueStore.GetDependentsWithMetadata(ctx, issue.ID) // Best effort: show issue even if dependents unavailable
			for _, sec := range groupDepSections(dependentsWithMeta, false, relatedSeen) {
				printDepSection(sec)
				if sec.Type == types.DepParentChild && issue.IssueType == types.TypeEpic {
					printEpicChildProgress(sec.Deps)
				}
			}

			printRelatedSection(relatedSeen)

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
	showCmd.Flags().Bool("brief-deps", false, "Reduce each dependency to its identity fields in JSON output (--json only; drops description, design, notes and acceptance criteria)")
	showCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(showCmd)
}

// showGetRequest carries the show flags onto the read contract. Extracted so
// the hop is reachable from a test: both show routes build this request
// independently, and a dropped field here is invisible to every other test.
func showGetRequest(id string, includeDependents, includeComments, briefDeps bool) issueops.GetRequest {
	return issueops.GetRequest{
		ID:                id,
		IncludeDependents: includeDependents,
		IncludeComments:   includeComments,
		BriefDeps:         briefDeps,
	}
}
