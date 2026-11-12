package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
	"github.com/steveyegge/beads/issueops"
)

// showNotFoundHint explains that an unresolved issue ID is not proof the ID
// never existed: bd delete and wisp purge both remove a row (and its
// events) from the live tables with no trace left behind, so a deleted or
// purged ID looks identical to one that was never created. bd history can
// sometimes tell the two apart via Dolt's commit history, but that scan is
// real, sometimes-slow work (and wisps are never committed to history at
// all), so it's offered as a pointer rather than run automatically here
// (ga-m6inyb).
func showNotFoundHint(id string) string {
	return fmt.Sprintf("this ID may have never existed, or may reference a deleted/purged record with no trace left in the live database — try 'bd history %s'", id)
}

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
		detail := showDetailFlagsFrom(cmd)
		commentsTail, _ := cmd.Flags().GetInt("comments-tail")
		if err := validateCommentsTail(commentsTail); err != nil {
			return err
		}
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
			currentID, err := resolveCurrentIssueID(ctx)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
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
			return watchIssue(ctx, args[0], commentsTail)
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
				if isNotFoundErr(err) {
					fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
					fmt.Fprintf(os.Stderr, "Hint: %s\n", showNotFoundHint(id))
				} else {
					fmt.Fprintf(os.Stderr, "Error fetching %s: %s\n", id, remedy.PrintedMessage(err))
				}
				continue
			}
			if result == nil || result.Issue == nil {
				if result != nil {
					result.Close()
				}
				fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
				fmt.Fprintf(os.Stderr, "Hint: %s\n", showNotFoundHint(id))
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
				// CLI: the count-only fast path (--count-only, be-ijck6q), the
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
				details, derr := rd.Get(ctx, showGetRequest(issue.ID, detail))
				if derr != nil {
					result.Close()
					return HandleErrorRespectJSON("%v", derr)
				}
				allDetails = append(allDetails, details)
				result.Close()
				continue
			}
			// Dependencies are read here rather than at the DEPENDS ON
			// section below because the header needs them: the derived GATED
			// marker (wy-j2upyy) is the gate-typed subset of this very set,
			// so hoisting the read decorates the header and the meta block
			// without a second query. Best effort, as it always was: a failed
			// read renders the issue undecorated rather than not at all.
			//
			// Counts first — see readDepCounts for why the order matters. The
			// listing's error is KEPT, not discarded: rendering stays best
			// effort, but a FAILED listing and a SHORT one both leave the
			// slice empty, and only the second is an unresolvable edge.
			depCountsSnapshot := readDepCounts(ctx, issueStore, issue.ID)
			depsWithMeta, depsErr := issueStore.GetDependenciesWithMetadata(ctx, issue.ID)
			gates := types.GatesHolding(issue, depsWithMeta)

			if idx > 0 {
				fmt.Println("\n" + ui.RenderMuted(strings.Repeat("─", 60)))
				fmt.Printf("\n%s\n", formatIssueHeaderWithGates(issue, gates))
			} else {
				fmt.Printf("%s\n", formatIssueHeaderWithGates(issue, gates))
			}

			// Metadata: Owner · Type | Created · Updated
			fmt.Println(formatIssueMetadataWithGates(issue, gates))

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
			// (read above, with the header's gate decoration).
			for _, sec := range groupDepSections(depsWithMeta, true, relatedSeen) {
				printDepSection(sec)
			}

			// Show dependents - grouped by dependency type for clarity
			dependentsWithMeta, dependentsErr := issueStore.GetDependentsWithMetadata(ctx, issue.ID) // Best effort: show issue even if dependents unavailable
			for _, sec := range groupDepSections(dependentsWithMeta, false, relatedSeen) {
				printDepSection(sec)
				if sec.Type == types.DepParentChild && issue.IssueType == types.TypeEpic {
					printEpicChildProgress(sec.Deps)
				}
			}

			// Both listings above drop every edge whose far end has no row in
			// this database, so a cross-repo or `external:` dependency renders
			// as no dependency at all — indistinguishable from having none
			// (be-lpi). --json says so in unresolvable_dependencies; say it
			// here too, or `bd dep add x liveop-y` reports success and then
			// `bd show x` shows nothing.
			warnUnresolvableDepEdges(issue.ID, depCountsSnapshot,
				depListing{rows: len(depsWithMeta), err: depsErr},
				depListing{rows: len(dependentsWithMeta), err: dependentsErr})

			printRelatedSection(relatedSeen)

			// Show comments
			comments, _ := issueStore.GetIssueComments(ctx, issue.ID) // Best effort: show issue even if comments unavailable
			printComments(comments, commentsTail, formatTime, issue.ID)

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
				return HandleErrorWithHintRespectJSON("no issues found matching the provided IDs",
					"some IDs may reference deleted/purged records with no trace left in the live database — try 'bd history <id>' to check")
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
	showCmd.Flags().String("as-of", "", "Show issue as it existed at a specific commit hash or branch (requires Dolt; commit hashes must be the full 32-character hash from 'bd history', not a prefix)")
	showCmd.Flags().StringArray("id", nil, "Issue ID (use for IDs that look like flags, e.g., --id=gt--xyz)")
	showCmd.Flags().Bool("local-time", false, "Show timestamps in local time instead of UTC (date-only fields are always local)")
	showCmd.Flags().BoolP("watch", "w", false, "Watch for changes and auto-refresh display")
	showCmd.Flags().Bool("current", false, "Show the currently active issue (in-progress, hooked, or last touched)")
	showCmd.Flags().Bool("count-only", false, "Leave the dependents and comments arrays out of JSON output and report them only as dependent_count and comment_count "+
		"(--json only; the fast path for hub beads and polling callers). An issue whose comments were left out carries "+
		"comments_omitted=true, so check it before reading an absent comments field as none. "+
		"--include-dependents / --include-comments add their array back.")
	showCmd.Flags().Bool("include-dependents", false, "With --count-only, stream the dependent rows anyway (--json only). The default JSON output already includes them.")
	showCmd.Flags().Bool("include-comments", false, "With --count-only, stream the full comment bodies anyway (--json only). The default JSON output already includes them.")
	showCmd.Flags().Bool("brief-deps", false, "Reduce each dependency to its identity fields in JSON output (--json only; drops description, design, notes and acceptance criteria)")
	showCmd.Flags().Int("comments-tail", 0, "Render only the last N comments in text output, preceded by one elision line naming how many older ones were hidden (text output only; 0, the default, renders every comment unchanged; N must be >= 0)")
	showCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(showCmd)
}

// showDetailFlags is the CLI's whole opinion about the detail view's shape:
// the four flags that decide it, read the same way on both show routes.
type showDetailFlags struct {
	countOnly         bool
	includeDependents bool
	includeComments   bool
	briefDeps         bool
}

// showDetailFlagsFrom reads the detail-shaping flags off the command. Every
// route goes through here, so a flag added to one reaches both by
// construction, and a flag read anywhere else is a bug the missing helper call
// makes visible.
func showDetailFlagsFrom(cmd *cobra.Command) showDetailFlags {
	var f showDetailFlags
	f.countOnly, _ = cmd.Flags().GetBool("count-only")
	f.includeDependents, _ = cmd.Flags().GetBool("include-dependents")
	f.includeComments, _ = cmd.Flags().GetBool("include-comments")
	f.briefDeps, _ = cmd.Flags().GetBool("brief-deps")
	return f
}

// showGetRequest carries the show flags onto the read contract. Extracted so
// the hop is reachable from a test: both show routes build this request
// independently, and a dropped field here is invisible to every other test.
//
// bd show --json defaults to the full detail view, the v0.49.6 contract
// (#4122); --count-only opts into the counts-only view a zero GetRequest
// names, and --include-* re-add one relation under it. The positional form
// this replaced let a new flag silently mean the old thing at a call site, so
// the struct makes every one of them fail to compile instead.
func showGetRequest(id string, f showDetailFlags) issueops.GetRequest {
	return issueops.GetRequest{
		ID:                id,
		IncludeDependents: !f.countOnly || f.includeDependents,
		IncludeComments:   !f.countOnly || f.includeComments,
		BriefDeps:         f.briefDeps,
	}
}
