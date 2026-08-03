package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

// storageExecutor handles operations that need a store connection
type storageExecutor func(store storage.DoltStorage) error

// withStorage executes an operation with either the direct store or a read-only store
func withStorage(ctx context.Context, store storage.DoltStorage, dbPath string, fn storageExecutor) error {
	if store != nil {
		return fn(store)
	} else if dbPath != "" {
		// Open read-only connection using repo metadata when available so
		// helper paths keep the correct Dolt database and server endpoint.
		roStore, err := openReadOnlyStoreForDBPath(ctx, dbPath)
		if err != nil {
			return err
		}
		defer func() { _ = roStore.Close() }() // Best effort cleanup
		return fn(roStore)
	}
	return fmt.Errorf("no storage available")
}

// issueSnapshot builds a comparable string from issue IDs, statuses, and
// update times so we can detect when the result set has changed.
func issueSnapshot(issues []*types.Issue) string {
	var b strings.Builder
	for _, issue := range issues {
		fmt.Fprintf(&b, "%s:%s:%d;", issue.ID, issue.Status, issue.UpdatedAt.UnixNano())
	}
	return b.String()
}

// skipLabelsIssueView wraps IssueWithCounts so the JSON encoder always emits
// `labels: []` regardless of the omitempty tag on Issue.Labels. AD-02 contract:
// with --skip-labels, every issue's labels field is present and empty.
type skipLabelsIssueView struct {
	*types.IssueWithCounts
	Labels []string `json:"labels"`
}

type skipLabelsListJSONResponse struct {
	Issues []skipLabelsIssueView `json:"issues"`
	Meta   skipLabelsListMeta    `json:"meta"`
}

type skipLabelsListMeta struct {
	SkipLabels bool `json:"skip_labels"`
	Count      int  `json:"count"`
}

func newSkipLabelsListJSONResponse(issues []*types.IssueWithCounts) skipLabelsListJSONResponse {
	views := make([]skipLabelsIssueView, len(issues))
	for i, issue := range issues {
		views[i] = skipLabelsIssueView{
			IssueWithCounts: issue,
			Labels:          []string{},
		}
	}
	return skipLabelsListJSONResponse{
		Issues: views,
		Meta: skipLabelsListMeta{
			SkipLabels: true,
			Count:      len(views),
		},
	}
}

// skipLabelsConflicts returns the names of label-filter flags that conflict
// with --skip-labels. Empty result means no conflict. AD-02 Wireframe 5.
func skipLabelsConflicts(labels, labelsAny []string, labelPattern, labelRegex string, excludeLabels []string, noLabels bool) []string {
	var conflicts []string
	if len(labels) > 0 {
		conflicts = append(conflicts, "--label")
	}
	if len(labelsAny) > 0 {
		conflicts = append(conflicts, "--label-any")
	}
	if labelPattern != "" {
		conflicts = append(conflicts, "--label-pattern")
	}
	if labelRegex != "" {
		conflicts = append(conflicts, "--label-regex")
	}
	if len(excludeLabels) > 0 {
		conflicts = append(conflicts, "--exclude-label")
	}
	if noLabels {
		conflicts = append(conflicts, "--no-labels")
	}
	return conflicts
}

// skipLabelsFooterText is the AD-02 Wireframe 2 footer note.
// The leading newline keeps the note visually distinct from the table.
func skipLabelsFooterText() string {
	return "\nnote: --skip-labels in effect — labels suppressed in output.\n"
}

// printSkipLabelsFooter writes the AD-02 footer to stdout when the flag is set
// and --quiet is not. Used by output paths that don't go through the buffered
// pager (pretty/tree mode).
func printSkipLabelsFooter(skipLabels bool) {
	if !skipLabels || isQuiet() {
		return
	}
	fmt.Print(skipLabelsFooterText())
}

// formatSkipLabelsConflictError builds the user-facing error message for AD-02
// Wireframe 5. The got: line echoes the conflicting flags so the user can see
// which input to remove without re-reading their command line.
func formatSkipLabelsConflictError(conflicts []string) string {
	return fmt.Sprintf(
		"error: --skip-labels cannot be combined with --label,\n"+
			"       --label-any, --label-pattern, --label-regex,\n"+
			"       --exclude-label, or --no-labels (the filter).\n"+
			"       (got: --skip-labels %s)\n"+
			"reason: --skip-labels suppresses the labels JOIN that those\n"+
			"        filters depend on.\n\n"+
			"To filter by labels: drop --skip-labels.\n"+
			"To get a label-free result fast: drop --label flags.\n",
		strings.Join(conflicts, " "))
}

// knownListFlags maps bare words that users might pass as positional args
// but are actually flag names. Each maps to a hint for the error message.
var knownListFlags = map[string]string{
	"ready":   "--ready",
	"tree":    "--tree",
	"flat":    "--flat",
	"all":     "--all",
	"long":    "--long",
	"watch":   "--watch",
	"pretty":  "--pretty",
	"pinned":  "--pinned",
	"overdue": "--overdue",
}

var listCmd = &cobra.Command{
	Use:     "list",
	GroupID: "issues",
	Short:   "List issues",
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		for _, arg := range args {
			if hint, ok := knownListFlags[arg]; ok {
				return fmt.Errorf("unknown argument %q; did you mean %q or 'bd %s'?", arg, hint, arg)
			}
		}
		return fmt.Errorf("bd list does not accept positional arguments; use flags instead (see bd list --help)")
	},
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		return runListCore(cmd, args)
	},
}

// runListCore runs the list query and rendering without emitting a metrics
// event, so the caller owns emission: `bd list` emits "list" exactly once, and
// the `bd children` alias emits "children" exactly once. children sets listCmd's
// flags and calls this core directly rather than listCmd.RunE, which would emit
// a second "list" event for a single user command.
func runListCore(cmd *cobra.Command, _ []string) error {
	in, err := gatherListInput(cmd)
	if err != nil {
		return err
	}

	if usesProxiedServer() {
		if err := rejectMaxRowsUnderProxiedServer(cmd); err != nil {
			return err
		}
		if err := runListProxiedServer(cmd, rootCtx, in); err != nil {
			return HandleError("%v", err)
		}
		return nil
	}

	if in.Offset > 0 {
		return HandleError("--offset is only supported under --proxied-server")
	}

	// `bd list` builds the filter rather than calling IssueReader(), because
	// this filter feeds --watch, the hierarchical --parent tree, the text
	// renderings that want []*types.Issue rather than a counted page, and the
	// --max-rows cap stamped on below — none of which the Reader role
	// expresses, and routing only the JSON path through it would fork the
	// command in two.
	//
	// It shares CONSTRUCTION with the role unconditionally: the same
	// issueops.ListRequest through the same builder, pinned by the builder's
	// golden file.
	//
	// It shares EXECUTION — the same workapi.FinishPage, same sort, same trim,
	// same has-more verdict — in every mode but one. The exception is the
	// hierarchical --parent tree under pretty output below: it renders the
	// recursive walk's own result and never reaches the epilogue, on this
	// route or the proxied one. For the modes that do reach it (JSON, plain
	// text, --watch, --ready) what differs from the HTTP listing is
	// presentation and the --max-rows cap. See issueops.Reader's doc comment.
	cfg, err := workapi.LoadStoreListConfig(rootCtx, store)
	if err != nil {
		return HandleError("%v", err)
	}
	filter, err := workapi.BuildListFilter(in.ListRequest, cfg)
	if err != nil {
		return HandleError("%v", err)
	}
	maxRows, maxRowsSource, err := resolveMaxRows(cmd)
	if err != nil {
		return err
	}
	filter.MaxRows = maxRows
	filter.MaxRowsSource = maxRowsSource

	ctx := rootCtx

	activeStore := store
	routedStore, routed, routingRule, err := openRoutedReadStore(ctx, activeStore)
	if err != nil {
		return HandleError("%v", err)
	}
	if routed {
		defer func() { _ = routedStore.Close() }()
		printContributorRoutingNotice(ctx, activeStore, routingRule)
		activeStore = routedStore
	}

	if in.watchMode {
		if err := watchIssues(ctx, activeStore, filter, in.ReadyFlag, in.ParentID, in.SortBy, in.Reverse, in.effectiveLimit); err != nil {
			if capErr := handleMaxRowsError(err); capErr != nil {
				return capErr
			}
			return HandleError("querying issues: %v", err)
		}
		return nil
	}

	if jsonOutput {
		var iwc []*types.IssueWithCounts
		var err error
		if in.ReadyFlag {
			iwc, err = activeStore.GetReadyWorkWithCounts(ctx, workapi.ReadyFilterFromIssueFilter(workapi.WithFetchOneExtra(filter)))
		} else {
			iwc, err = activeStore.SearchIssuesWithCounts(ctx, "", workapi.WithFetchOneExtra(filter))
		}
		if err != nil {
			if capErr := handleMaxRowsError(err); capErr != nil {
				return capErr
			}
			return HandleError("%v", err)
		}
		iwc, truncated := workapi.FinishPage(iwc, in.SortBy, in.Reverse, in.effectiveLimit, false)
		if in.SkipLabels {
			if err := outputJSON(newSkipLabelsListJSONResponse(iwc)); err != nil {
				return err
			}
			printTruncationHint(truncated, in.effectiveLimit)
			return nil
		}
		if err := outputJSON(iwc); err != nil {
			return err
		}
		printTruncationHint(truncated, in.effectiveLimit)
		return nil
	}

	var issues []*types.Issue
	if in.ReadyFlag {
		wf := workapi.ReadyFilterFromIssueFilter(workapi.WithFetchOneExtra(filter))
		var err error
		issues, err = activeStore.GetReadyWork(ctx, wf)
		if err != nil {
			if capErr := handleMaxRowsError(err); capErr != nil {
				return capErr
			}
			return HandleError("%v", err)
		}
	} else {
		var err error
		issues, err = activeStore.SearchIssues(ctx, "", workapi.WithFetchOneExtra(filter))
		if err != nil {
			if capErr := handleMaxRowsError(err); capErr != nil {
				return capErr
			}
			return HandleError("%v", err)
		}
	}

	issues, truncated := workapi.FinishPage(issues, in.SortBy, in.Reverse, in.effectiveLimit, false)

	if in.prettyFormat && !jsonOutput {
		if in.ParentID != "" && !in.ReadyFlag {
			treeIssues, err := getHierarchicalChildren(ctx, activeStore, "", in.ParentID, filter)
			if err != nil {
				return HandleError("%v", err)
			}

			if len(treeIssues) == 0 {
				fmt.Printf("Issue '%s' has no children\n", in.ParentID)
				return nil
			}

			allDeps, depErr := activeStore.GetAllDependencyRecords(ctx)
			if depErr != nil && in.depsMode != "" {
				return HandleError("loading dependencies for --deps: %v", depErr)
			}
			displayPrettyListWithDepsMode(treeIssues, false, allDeps, in.depsMode)
			printSkipLabelsFooter(in.SkipLabels)
			return nil
		}

		allDeps, depErr := activeStore.GetAllDependencyRecords(ctx)
		if depErr != nil && in.depsMode != "" {
			return HandleError("loading dependencies for --deps: %v", depErr)
		}
		displayPrettyListWithDepsMode(issues, false, allDeps, in.depsMode)
		printTruncationHint(truncated, in.effectiveLimit)
		printSkipLabelsFooter(in.SkipLabels)
		return nil
	}

	if in.formatStr != "" {
		depsByIssueID, _ := activeStore.GetAllDependencyRecords(ctx)
		if err := outputFormattedList(issues, depsByIssueID, in.formatStr); err != nil {
			return HandleError("%v", err)
		}
		printTruncationHint(truncated, in.effectiveLimit)
		return nil
	}

	maybeShowUpgradeNotification()

	issueIDs := make([]string, len(issues))
	labelsMap := make(map[string][]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
		if len(issue.Labels) > 0 {
			labelsMap[issue.ID] = issue.Labels
		}
	}

	blockedByMap, blocksMap, parentMap, _ := activeStore.GetBlockingInfoForIssues(ctx, issueIDs)

	var buf strings.Builder
	if ui.IsAgentMode() {
		for _, issue := range issues {
			formatAgentIssue(&buf, issue, blockedByMap[issue.ID], blocksMap[issue.ID], parentMap[issue.ID])
		}
		fmt.Print(buf.String())
		printTruncationHint(truncated, in.effectiveLimit)
		return nil
	} else if in.longFormat {
		buf.WriteString(fmt.Sprintf("\nFound %d issues:\n\n", len(issues)))
		for _, issue := range issues {
			labels := labelsMap[issue.ID]
			formatIssueLong(&buf, issue, labels, in.SkipLabels)
		}
	} else {
		for _, issue := range issues {
			labels := labelsMap[issue.ID]
			formatIssueCompact(&buf, issue, labels, blockedByMap[issue.ID], blocksMap[issue.ID], parentMap[issue.ID])
		}
	}

	if in.SkipLabels && !isQuiet() {
		buf.WriteString(skipLabelsFooterText())
	}

	if err := ui.ToPager(buf.String(), ui.PagerOptions{NoPager: in.noPager}); err != nil {
		if _, writeErr := fmt.Fprint(os.Stdout, buf.String()); writeErr != nil {
			fmt.Fprintf(os.Stderr, "Error writing output: %v\n", writeErr)
		}
	}

	printTruncationHint(truncated, in.effectiveLimit)

	maybeShowTip(store)
	return nil
}

func init() {
	listCmd.Flags().StringP("status", "s", "", "Filter by stored status (open, in_progress, blocked, deferred, closed). Comma-separated for multiple: --status open,in_progress. Note: repeating -s/--status silently overwrites the previous value — always use the comma-separated form for multi-status filters.")
	listCmd.Flags().String("state", "", "Alias for --status")
	_ = listCmd.Flags().MarkHidden("state")
	registerPriorityFlag(listCmd, "")
	listCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	listCmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore, decision, merge-request, molecule, gate, convoy). Aliases: mr→merge-request, feat→feature, mol→molecule, dec/adr→decision")
	listCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL). Can combine with --label-any")
	listCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE). Can combine with --label")
	listCmd.Flags().StringSlice("exclude-label", []string{}, "Exclude issues that have ANY of these labels")
	listCmd.Flags().String("label-pattern", "", "Filter by label glob pattern (e.g., 'tech-*' matches tech-debt, tech-legacy)")
	listCmd.Flags().String("label-regex", "", "Filter by label regex pattern (e.g., 'tech-(debt|legacy)')")
	listCmd.Flags().String("title", "", "Filter by title text (case-insensitive substring match)")
	listCmd.Flags().String("spec", "", "Filter by spec_id prefix")
	listCmd.Flags().String("id", "", "Filter by specific issue IDs (comma-separated, e.g., bd-1,bd-5,bd-10)")
	listCmd.Flags().IntP("limit", "n", workapi.DefaultListLimit, "Limit results (default 50, use 0 for unlimited)")
	listCmd.Flags().Int("offset", 0, "Skip the first N matching results (0-based). Only supported under --proxied-server.")
	listCmd.Flags().String("format", "", "Output format: 'digraph' (for golang.org/x/tools/cmd/digraph), 'dot' (Graphviz), or Go template")
	listCmd.Flags().Bool("all", false, "Show all issues including closed (overrides default filter)")
	listCmd.Flags().Bool("long", false, "Show detailed multi-line output for each issue")
	listCmd.Flags().String("sort", "", "Sort by field: priority, created, updated, closed, status, id, title, type, assignee")
	listCmd.Flags().BoolP("reverse", "r", false, "Reverse sort order")

	// Pattern matching
	listCmd.Flags().String("title-contains", "", "Filter by title substring (case-insensitive)")
	listCmd.Flags().String("desc-contains", "", "Filter by description substring (case-insensitive)")
	listCmd.Flags().String("notes-contains", "", "Filter by notes substring (case-insensitive)")
	listCmd.Flags().String("external-contains", "", "Filter by external ref substring (case-insensitive)")
	listCmd.Flags().String("external-ref", "", "Filter by exact external_ref value")

	// Date ranges
	listCmd.Flags().String("created-after", "", "Filter issues created after date (YYYY-MM-DD or RFC3339)")
	listCmd.Flags().String("created-before", "", "Filter issues created before date (YYYY-MM-DD or RFC3339)")
	listCmd.Flags().String("updated-after", "", "Filter issues updated after date (YYYY-MM-DD or RFC3339)")
	listCmd.Flags().String("updated-before", "", "Filter issues updated before date (YYYY-MM-DD or RFC3339)")
	listCmd.Flags().String("closed-after", "", "Filter issues closed after date (YYYY-MM-DD or RFC3339)")
	listCmd.Flags().String("closed-before", "", "Filter issues closed before date (YYYY-MM-DD or RFC3339)")

	// Empty/null checks
	listCmd.Flags().Bool("empty-description", false, "Filter issues with empty or missing description")
	listCmd.Flags().Bool("no-assignee", false, "Filter issues with no assignee")
	listCmd.Flags().Bool("no-labels", false, "Filter issues with no labels")

	// Hydration toggle (AD-02). Distinct from --no-labels (filter).
	listCmd.Flags().Bool("skip-labels", false,
		"Skip label hydration. The labels field in output will be empty regardless "+
			"of actual labels. Use only when the caller does not depend on label data. "+
			"Cannot combine with --label, --label-any, --label-pattern, --label-regex, "+
			"--exclude-label, or --no-labels.")

	// Priority ranges
	listCmd.Flags().String("priority-min", "", "Filter by minimum priority (inclusive, 0-4 or P0-P4)")
	listCmd.Flags().String("priority-max", "", "Filter by maximum priority (inclusive, 0-4 or P0-P4)")

	// Pinned filtering
	listCmd.Flags().Bool("pinned", false, "Show only pinned issues")
	listCmd.Flags().Bool("no-pinned", false, "Exclude pinned issues")

	// Template filtering: exclude templates by default
	listCmd.Flags().Bool("include-templates", false, "Include template molecules in output")

	// Gate filtering: exclude gate issues by default (bd-7zka.2)
	listCmd.Flags().Bool("include-gates", false, "Include gate issues in output (normally hidden)")

	// Infra type filtering: exclude agent/role/message by default
	listCmd.Flags().Bool("include-infra", false, "Include infrastructure beads (agent/role/message) in output")

	// Explicit type exclusion
	listCmd.Flags().StringSlice("exclude-type", nil, "Exclude issue types from results (comma-separated or repeatable, e.g., --exclude-type=convoy,epic)")

	// Parent filtering: filter children by parent issue
	listCmd.Flags().String("parent", "", "Filter by parent issue ID (shows children of specified issue)")
	listCmd.Flags().String("filter-parent", "", "Alias for --parent")
	_ = listCmd.Flags().MarkHidden("filter-parent") // Only fails if flag missing (caught in tests)
	listCmd.Flags().Bool("no-parent", false, "Exclude child issues (show only top-level issues)")

	// Molecule type filtering
	listCmd.Flags().String("mol-type", "", "Filter by molecule type: swarm, patrol, or work")

	// Wisp type filtering (TTL-based compaction classification)
	listCmd.Flags().String("wisp-type", "", "Filter by wisp type: heartbeat, ping, patrol, gc_report, recovery, error, escalation")

	// Time-based scheduling filters (GH#820)
	listCmd.Flags().Bool("deferred", false, "Show only issues with defer_until set")
	listCmd.Flags().String("defer-after", "", "Filter issues deferred after date (supports relative: +6h, tomorrow)")
	listCmd.Flags().String("defer-before", "", "Filter issues deferred before date (supports relative: +6h, tomorrow)")
	listCmd.Flags().String("due-after", "", "Filter issues due after date (supports relative: +6h, tomorrow)")
	listCmd.Flags().String("due-before", "", "Filter issues due before date (supports relative: +6h, tomorrow)")
	listCmd.Flags().Bool("overdue", false, "Show only issues with due_at in the past (not closed)")

	// Pretty and watch flags (GH#654)
	listCmd.Flags().Bool("pretty", false, "Display issues in a tree format with status/priority symbols")
	listCmd.Flags().Bool("tree", true, "Hierarchical tree format (default: true; use --flat to disable)")
	listCmd.Flags().Bool("flat", false, "Disable tree format and use legacy flat list output")
	listCmd.Flags().BoolP("watch", "w", false, "Watch for changes and auto-update display (implies --pretty)")
	// --deps annotates the tree with dependency edges and orders siblings by them.
	// Bare --deps means "scheduling"; --deps=all also shows knowledge-graph edges.
	listCmd.Flags().String("deps", "", "Annotate tree with dependency edges and order siblings by them: 'scheduling' (bare --deps) or 'all'")
	if f := listCmd.Flags().Lookup("deps"); f != nil {
		f.NoOptDefVal = "scheduling"
	}

	// Metadata filtering (GH#1406)
	listCmd.Flags().StringArray("metadata-field", nil, "Filter by metadata field (key=value, repeatable)")
	listCmd.Flags().String("has-metadata-key", "", "Filter issues that have this metadata key set")

	// Pager control (bd-jdz3)
	listCmd.Flags().Bool("no-pager", false, "Disable pager output")

	// Ready filter: show only issues ready to be worked on (bd-ihu31)
	listCmd.Flags().Bool("ready", false, "Show only ready issues (no active blockers, same semantics as bd ready)")

	// Defensive row cap (be-x42v): exits 2 on overage, default disabled.
	addMaxRowsFlag(listCmd)

	// Note: --json flag is defined as a persistent flag in main.go, not here
	rootCmd.AddCommand(listCmd)
}
