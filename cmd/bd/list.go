package main

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
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

// withFetchOneExtra bumps Limit by one so the caller can detect "more
// results than the limit" (GH#3212) by comparing len(results) against the
// original limit.
//
// `--limit N --max-rows N` (equal) needs special handling so this bump
// doesn't collide with the MaxRows cap check: EffectiveSearchLimit treats
// Limit==MaxRows as "no overage sniff" (returns Limit verbatim, no +1 — see
// EffectiveSearchLimit's doc and the WithLimit_LimitAtCap_NoError storage
// test), but bumping Limit to N+1 alone flips that to Limit>MaxRows, which
// *does* sniff for overage — so the probe row itself would trip
// EnforceMaxRowsCap(N+1, N, ...) even though the delivered set is always
// trimmed back to N. Bumping MaxRows by one in lockstep, only in this exact
// N==M case, keeps the probe row inside the (temporarily N+1) cap so
// EnforceMaxRowsCap never fires on it while still fetching N+1 rows for the
// truncation check below. This can't false-negative a real violation: the
// query's own LIMIT is capped at N+1 either way, so "more than N+1 matches
// exist" was already undetectable at Limit==MaxRows before this bump ever
// existed (same "no overage detection above the equal cap" contract
// EffectiveSearchLimit documents for the unbumped case).
//
// This does not change the Limit>MaxRows (tighter-cap) or Limit<MaxRows
// cases: EffectiveSearchLimit's branch selection there is unaffected by a
// one-row bump to Limit, and MaxRows is left untouched, so a genuine
// tighter-cap violation still fires with the correct (unbumped) Cap value
// in the error message.
func withFetchOneExtra(filter types.IssueFilter) types.IssueFilter {
	if filter.Limit > 0 {
		if filter.MaxRows > 0 && filter.Limit == filter.MaxRows {
			filter.MaxRows++
		}
		filter.Limit++
	}
	return filter
}

func readyWorkFilterFromIssueFilter(filter types.IssueFilter) types.WorkFilter {
	wf := types.WorkFilter{
		Status:         types.StatusOpen,
		Limit:          filter.Limit,
		Offset:         filter.Offset,
		Labels:         filter.Labels,
		LabelsAny:      filter.LabelsAny,
		ExcludeLabels:  filter.ExcludeLabels,
		LabelPattern:   filter.LabelPattern,
		LabelRegex:     filter.LabelRegex,
		ParentID:       filter.ParentID,
		MolType:        filter.MolType,
		WispType:       filter.WispType,
		ExcludeTypes:   filter.ExcludeTypes,
		MetadataFields: filter.MetadataFields,
		HasMetadataKey: filter.HasMetadataKey,
		MaxRows:        filter.MaxRows,
		MaxRowsSource:  filter.MaxRowsSource,
	}
	if filter.IssueType != nil {
		wf.Type = string(*filter.IssueType)
	}
	if filter.Priority != nil {
		wf.Priority = filter.Priority
	}
	if filter.Assignee != nil {
		wf.Assignee = filter.Assignee
	}
	if filter.NoAssignee {
		wf.Unassigned = true
	}
	if filter.Ephemeral != nil && *filter.Ephemeral {
		wf.IncludeEphemeral = true
	}
	return wf
}

// getHierarchicalChildren handles the --tree --parent combination logic.
// baseFilter carries CLI filters (--type, --status, etc.) through the recursive walk.
func getHierarchicalChildren(ctx context.Context, store storage.DoltStorage, dbPath string, parentID string, baseFilter types.IssueFilter) ([]*types.Issue, error) {
	// First verify that the parent issue exists
	var parentIssue *types.Issue
	err := withStorage(ctx, store, dbPath, func(s storage.DoltStorage) error {
		var err error
		parentIssue, err = s.GetIssue(ctx, parentID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("error checking parent issue: %v", err)
	}
	if parentIssue == nil {
		return nil, fmt.Errorf("parent issue '%s' not found", parentID)
	}

	// Use recursive search to find all descendants using the same logic as --parent filter.
	// The parent itself is NOT included in the result set — only actual children and
	// their descendants. This matches the behavior of --json and --flat (GH#3349).
	allDescendants := make(map[string]*types.Issue)

	err = findAllDescendants(ctx, store, dbPath, parentID, baseFilter, allDescendants)
	if err != nil {
		return nil, fmt.Errorf("error finding descendants: %v", err)
	}

	if len(allDescendants) == 0 {
		return nil, nil
	}

	// Include the parent as the tree root only when descendants exist,
	// so the tree renderer can draw the hierarchy with the parent at the top.
	allDescendants[parentID] = parentIssue

	treeIssues := make([]*types.Issue, 0, len(allDescendants))
	for _, issue := range allDescendants {
		treeIssues = append(treeIssues, issue)
	}

	return treeIssues, nil
}

// findAllDescendants recursively finds all descendants using parent filtering.
// baseFilter carries CLI filters (--type, --status, etc.) so the tree respects them.
func findAllDescendants(ctx context.Context, store storage.DoltStorage, dbPath string, parentID string, baseFilter types.IssueFilter, result map[string]*types.Issue) error {
	var children []*types.Issue
	err := withStorage(ctx, store, dbPath, func(s storage.DoltStorage) error {
		filter := baseFilter
		filter.ParentID = &parentID
		filter.Limit = 0 // unlimited per level to avoid truncating the tree walk
		var err error
		children, err = s.SearchIssues(ctx, "", filter)
		return err
	})
	if err != nil {
		return err
	}

	for _, child := range children {
		if _, exists := result[child.ID]; !exists {
			result[child.ID] = child
			err = findAllDescendants(ctx, store, dbPath, child.ID, baseFilter, result)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// watchIssues polls for changes and re-displays (GH#654)
// Uses polling instead of fsnotify because Dolt stores data in a server-side
// database, not files — file watchers never fire.
type watchListDependencyStore interface {
	GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error)
}

func loadWatchedIssues(ctx context.Context, store storage.DoltStorage, filter types.IssueFilter, ready bool, parentID string, sortBy string, reverse bool) ([]*types.Issue, error) {
	if ready {
		issues, err := store.GetReadyWork(ctx, readyWorkFilterFromIssueFilter(withFetchOneExtra(filter)))
		if err != nil {
			return nil, err
		}
		sortIssues(issues, sortBy, reverse)
		return issues, nil
	}

	if parentID != "" {
		issues, err := getHierarchicalChildren(ctx, store, "", parentID, filter)
		if err != nil {
			return nil, err
		}
		// getHierarchicalChildren builds its result from a map, so normalize the
		// slice before snapshot comparison to avoid spurious redraws.
		sortIssues(issues, "id", false)
		return issues, nil
	}

	issues, err := store.SearchIssues(ctx, "", withFetchOneExtra(filter))
	if err != nil {
		return nil, err
	}
	sortIssues(issues, sortBy, reverse)
	return issues, nil
}

func displayWatchedIssueList(ctx context.Context, store watchListDependencyStore, issues []*types.Issue) {
	var allDeps map[string][]*types.Dependency
	if store != nil {
		deps, err := store.GetAllDependencyRecords(ctx)
		if err == nil {
			allDeps = deps
		}
	}
	displayPrettyListWithDeps(issues, true, allDeps)
}

// watchIssues returns an error only for the initial query — a failure there
// means bd list --watch never displayed anything, so (unlike a mid-poll
// refresh failure, which just logs and keeps the last good snapshot on
// screen) it must propagate to the caller. In particular this lets
// runListCore route a MaxRows cap violation through handleMaxRowsError for
// exit-code-2 semantics instead of watchIssues swallowing it and exiting 0
// (be-x42v.4 follow-up, review MUST-FIX 5).
func watchIssues(ctx context.Context, store storage.DoltStorage, filter types.IssueFilter, ready bool, parentID string, sortBy string, reverse bool, effectiveLimit int) error {
	// Initial display
	issues, err := loadWatchedIssues(ctx, store, filter, ready, parentID, sortBy, reverse)
	if err != nil {
		return err
	}
	truncated := effectiveLimit > 0 && len(issues) > effectiveLimit
	if truncated {
		issues = issues[:effectiveLimit]
	}
	displayWatchedIssueList(ctx, store, issues)
	printTruncationHint(truncated, effectiveLimit)
	lastSnapshot := issueSnapshot(issues)

	fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")

	// Handle Ctrl+C — deferred Stop prevents signal handler leak
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	pollInterval := 2 * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nStopped watching.\n")
			return nil
		case <-ticker.C:
			issues, err := loadWatchedIssues(ctx, store, filter, ready, parentID, sortBy, reverse)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error refreshing issues: %v\n", err)
				continue
			}
			truncated := effectiveLimit > 0 && len(issues) > effectiveLimit
			if truncated {
				issues = issues[:effectiveLimit]
			}
			snap := issueSnapshot(issues)
			if snap != lastSnapshot {
				lastSnapshot = snap
				displayWatchedIssueList(ctx, store, issues)
				printTruncationHint(truncated, effectiveLimit)
				fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")
			}
		}
	}
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

func compareIssuesBy(a, b *types.Issue, sortBy string) int {
	switch sortBy {
	case "priority":
		return cmp.Compare(a.Priority, b.Priority)
	case "created":
		return b.CreatedAt.Compare(a.CreatedAt)
	case "updated":
		return b.UpdatedAt.Compare(a.UpdatedAt)
	case "closed":
		if a.ClosedAt == nil && b.ClosedAt == nil {
			return 0
		} else if a.ClosedAt == nil {
			return 1
		} else if b.ClosedAt == nil {
			return -1
		}
		return b.ClosedAt.Compare(*a.ClosedAt)
	case "status":
		return cmp.Compare(a.Status, b.Status)
	case "id":
		return utils.NaturalCompareIDs(a.ID, b.ID)
	case "title":
		return cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	case "type":
		return cmp.Compare(a.IssueType, b.IssueType)
	case "assignee":
		return cmp.Compare(a.Assignee, b.Assignee)
	}
	return 0
}

func sortIssues(issues []*types.Issue, sortBy string, reverse bool) {
	if sortBy == "" {
		return
	}
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		r := compareIssuesBy(a, b, sortBy)
		if reverse {
			return -r
		}
		return r
	})
}

func sortIssuesWithCounts(items []*types.IssueWithCounts, sortBy string, reverse bool) {
	if sortBy == "" {
		return
	}
	slices.SortFunc(items, func(a, b *types.IssueWithCounts) int {
		ai, bi := issueOrNil(a), issueOrNil(b)
		if ai == nil {
			if bi == nil {
				return 0
			}
			return 1
		}
		if bi == nil {
			return -1
		}
		r := compareIssuesBy(ai, bi, sortBy)
		if reverse {
			return -r
		}
		return r
	})
}

func issueOrNil(iwc *types.IssueWithCounts) *types.Issue {
	if iwc == nil {
		return nil
	}
	return iwc.Issue
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

	if in.offset > 0 {
		return HandleError("--offset is only supported under --proxied-server")
	}

	cfg, err := loadDirectListFilterConfig(rootCtx, store)
	if err != nil {
		return HandleError("%v", err)
	}
	filter, err := buildListFilter(in, cfg)
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
	routedStore, routed, err := openRoutedReadStore(ctx, activeStore)
	if err != nil {
		return HandleError("%v", err)
	}
	if routed {
		defer func() { _ = routedStore.Close() }()
		activeStore = routedStore
	}

	if in.watchMode {
		if err := watchIssues(ctx, activeStore, filter, in.readyFlag, in.parentID, in.sortBy, in.reverse, in.effectiveLimit); err != nil {
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
		if in.readyFlag {
			iwc, err = activeStore.GetReadyWorkWithCounts(ctx, readyWorkFilterFromIssueFilter(withFetchOneExtra(filter)))
		} else {
			iwc, err = activeStore.SearchIssuesWithCounts(ctx, "", withFetchOneExtra(filter))
		}
		if err != nil {
			if capErr := handleMaxRowsError(err); capErr != nil {
				return capErr
			}
			return HandleError("%v", err)
		}
		sortIssuesWithCounts(iwc, in.sortBy, in.reverse)
		truncated := in.effectiveLimit > 0 && len(iwc) > in.effectiveLimit
		if truncated {
			iwc = iwc[:in.effectiveLimit]
		}
		if iwc == nil {
			iwc = []*types.IssueWithCounts{}
		}
		if in.skipLabels {
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
	if in.readyFlag {
		wf := readyWorkFilterFromIssueFilter(withFetchOneExtra(filter))
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
		issues, err = activeStore.SearchIssues(ctx, "", withFetchOneExtra(filter))
		if err != nil {
			if capErr := handleMaxRowsError(err); capErr != nil {
				return capErr
			}
			return HandleError("%v", err)
		}
	}

	sortIssues(issues, in.sortBy, in.reverse)

	truncated := in.effectiveLimit > 0 && len(issues) > in.effectiveLimit
	if truncated {
		issues = issues[:in.effectiveLimit]
	}

	if in.prettyFormat && !jsonOutput {
		if in.parentID != "" && !in.readyFlag {
			treeIssues, err := getHierarchicalChildren(ctx, activeStore, "", in.parentID, filter)
			if err != nil {
				return HandleError("%v", err)
			}

			if len(treeIssues) == 0 {
				fmt.Printf("Issue '%s' has no children\n", in.parentID)
				return nil
			}

			allDeps, _ := activeStore.GetAllDependencyRecords(ctx)
			displayPrettyListWithDeps(treeIssues, false, allDeps)
			printSkipLabelsFooter(in.skipLabels)
			return nil
		}

		allDeps, _ := activeStore.GetAllDependencyRecords(ctx)
		displayPrettyListWithDeps(issues, false, allDeps)
		printTruncationHint(truncated, in.effectiveLimit)
		printSkipLabelsFooter(in.skipLabels)
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
			formatIssueLong(&buf, issue, labels, in.skipLabels)
		}
	} else {
		for _, issue := range issues {
			labels := labelsMap[issue.ID]
			formatIssueCompact(&buf, issue, labels, blockedByMap[issue.ID], blocksMap[issue.ID], parentMap[issue.ID])
		}
	}

	if in.skipLabels && !isQuiet() {
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
	listCmd.Flags().IntP("limit", "n", 50, "Limit results (default 50, use 0 for unlimited)")
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
