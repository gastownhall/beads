package main

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
)

// storageExecutor handles operations that need to work with both direct store and daemon mode
type storageExecutor func(store *dolt.DoltStore) error

// withStorage executes an operation with either the direct store or a read-only store in daemon mode
func withStorage(ctx context.Context, store *dolt.DoltStore, dbPath string, lockTimeout time.Duration, fn storageExecutor) error {
	if store != nil {
		return fn(store)
	} else if dbPath != "" {
		// Daemon mode: open read-only connection
		roStore, err := dolt.New(ctx, &dolt.Config{Path: dbPath, ReadOnly: true, OpenTimeout: lockTimeout})
		if err != nil {
			return err
		}
		defer func() { _ = roStore.Close() }() // Best effort cleanup
		return fn(roStore)
	}
	return fmt.Errorf("no storage available")
}

// getHierarchicalChildren handles the --tree --parent combination logic
func getHierarchicalChildren(ctx context.Context, store *dolt.DoltStore, dbPath string, lockTimeout time.Duration, parentID string) ([]*types.Issue, error) {
	// First verify that the parent issue exists
	var parentIssue *types.Issue
	err := withStorage(ctx, store, dbPath, lockTimeout, func(s *dolt.DoltStore) error {
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

	// Use recursive search to find all descendants using the same logic as --parent filter
	// This works around issues with GetDependencyTree not finding all dependents properly
	allDescendants := make(map[string]*types.Issue)

	// Always include the parent
	allDescendants[parentID] = parentIssue

	// Recursively find all descendants
	err = findAllDescendants(ctx, store, dbPath, lockTimeout, parentID, allDescendants, 0, 10) // max depth 10
	if err != nil {
		return nil, fmt.Errorf("error finding descendants: %v", err)
	}

	// Convert map to slice for display
	treeIssues := make([]*types.Issue, 0, len(allDescendants))
	for _, issue := range allDescendants {
		treeIssues = append(treeIssues, issue)
	}

	return treeIssues, nil
}

// findAllDescendants recursively finds all descendants using parent filtering
func findAllDescendants(ctx context.Context, store *dolt.DoltStore, dbPath string, lockTimeout time.Duration, parentID string, result map[string]*types.Issue, currentDepth, maxDepth int) error {
	if currentDepth >= maxDepth {
		return nil // Prevent infinite recursion
	}

	// Get direct children using the same filter logic as regular --parent
	var children []*types.Issue
	err := withStorage(ctx, store, dbPath, lockTimeout, func(s *dolt.DoltStore) error {
		filter := types.IssueFilter{
			ParentID: &parentID,
		}
		var err error
		children, err = s.SearchIssues(ctx, "", filter)
		return err
	})
	if err != nil {
		return err
	}

	// Add children and recursively find their descendants
	for _, child := range children {
		if _, exists := result[child.ID]; !exists {
			result[child.ID] = child
			// Recursively find this child's descendants
			err = findAllDescendants(ctx, store, dbPath, lockTimeout, child.ID, result, currentDepth+1, maxDepth)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// watchIssues starts watching for changes and re-displays (GH#654)
func watchIssues(ctx context.Context, store *dolt.DoltStore, filter types.IssueFilter, sortBy string, reverse bool) {
	// Find .beads directory
	beadsDir := ".beads"
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: .beads directory not found\n")
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating watcher: %v\n", err)
		return
	}
	defer func() { _ = watcher.Close() }() // Best effort cleanup

	// Watch the .beads directory
	if err := watcher.Add(beadsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error watching directory: %v\n", err)
		return
	}

	// Initial display
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying issues: %v\n", err)
		return
	}
	sortIssues(issues, sortBy, reverse)
	displayPrettyList(issues, true)

	fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")

	// Handle Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Debounce timer
	var debounceTimer *time.Timer
	debounceDelay := 500 * time.Millisecond

	for {
		select {
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nStopped watching.\n")
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Only react to writes on issues.jsonl or database files
			if event.Has(fsnotify.Write) {
				basename := filepath.Base(event.Name)
				if basename == "issues.jsonl" || strings.HasSuffix(basename, ".db") {
					// Debounce rapid changes
					if debounceTimer != nil {
						debounceTimer.Stop()
					}
					debounceTimer = time.AfterFunc(debounceDelay, func() {
						issues, err := store.SearchIssues(ctx, "", filter)
						if err != nil {
							fmt.Fprintf(os.Stderr, "Error refreshing issues: %v\n", err)
							return
						}
						sortIssues(issues, sortBy, reverse)
						displayPrettyList(issues, true)
						fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")
					})
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "Watcher error: %v\n", err)
		}
	}
}

// sortIssues sorts a slice of issues by the specified field and direction
func sortIssues(issues []*types.Issue, sortBy string, reverse bool) {
	if sortBy == "" {
		return
	}

	slices.SortFunc(issues, func(a, b *types.Issue) int {
		var result int

		switch sortBy {
		case "priority":
			// Lower priority numbers come first (P0 > P1 > P2 > P3 > P4)
			result = cmp.Compare(a.Priority, b.Priority)
		case "created":
			// Default: newest first (descending)
			result = b.CreatedAt.Compare(a.CreatedAt)
		case "updated":
			// Default: newest first (descending)
			result = b.UpdatedAt.Compare(a.UpdatedAt)
		case "closed":
			// Default: newest first (descending)
			// Handle nil ClosedAt values
			if a.ClosedAt == nil && b.ClosedAt == nil {
				result = 0
			} else if a.ClosedAt == nil {
				result = 1 // nil sorts last
			} else if b.ClosedAt == nil {
				result = -1 // non-nil sorts before nil
			} else {
				result = b.ClosedAt.Compare(*a.ClosedAt)
			}
		case "status":
			result = cmp.Compare(a.Status, b.Status)
		case "id":
			result = cmp.Compare(a.ID, b.ID)
		case "title":
			result = cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		case "type":
			result = cmp.Compare(a.IssueType, b.IssueType)
		case "assignee":
			result = cmp.Compare(a.Assignee, b.Assignee)
		default:
			// Unknown sort field, no sorting
			result = 0
		}

		if reverse {
			return -result
		}
		return result
	})
}

var listCmd = &cobra.Command{
	Use:     "list",
	GroupID: "issues",
	Short:   "List issues",
	Run: func(cmd *cobra.Command, args []string) {
		status, _ := cmd.Flags().GetString("status")
		assignee, _ := cmd.Flags().GetString("assignee")
		issueType, _ := cmd.Flags().GetString("type")
		issueType = utils.NormalizeIssueType(issueType) // Expand aliases (mr→merge-request, etc.)
		limit, _ := cmd.Flags().GetInt("limit")
		allFlag, _ := cmd.Flags().GetBool("all")
		formatStr, _ := cmd.Flags().GetString("format")
		labels, _ := cmd.Flags().GetStringSlice("label")
		labelsAny, _ := cmd.Flags().GetStringSlice("label-any")
		labelPattern, _ := cmd.Flags().GetString("label-pattern")
		labelRegex, _ := cmd.Flags().GetString("label-regex")
		titleSearch, _ := cmd.Flags().GetString("title")
		specPrefix, _ := cmd.Flags().GetString("spec")
		idFilter, _ := cmd.Flags().GetString("id")
		longFormat, _ := cmd.Flags().GetBool("long")
		sortBy, _ := cmd.Flags().GetString("sort")
		reverse, _ := cmd.Flags().GetBool("reverse")

		// Pattern matching flags
		titleContains, _ := cmd.Flags().GetString("title-contains")
		descContains, _ := cmd.Flags().GetString("desc-contains")
		notesContains, _ := cmd.Flags().GetString("notes-contains")

		// Date range flags
		createdAfter, _ := cmd.Flags().GetString("created-after")
		createdBefore, _ := cmd.Flags().GetString("created-before")
		updatedAfter, _ := cmd.Flags().GetString("updated-after")
		updatedBefore, _ := cmd.Flags().GetString("updated-before")
		closedAfter, _ := cmd.Flags().GetString("closed-after")
		closedBefore, _ := cmd.Flags().GetString("closed-before")

		// Empty/null check flags
		emptyDesc, _ := cmd.Flags().GetBool("empty-description")
		noAssignee, _ := cmd.Flags().GetBool("no-assignee")
		noLabels, _ := cmd.Flags().GetBool("no-labels")

		// Priority range flags
		priorityMinStr, _ := cmd.Flags().GetString("priority-min")
		priorityMaxStr, _ := cmd.Flags().GetString("priority-max")

		// Pinned filtering flags
		pinnedFlag, _ := cmd.Flags().GetBool("pinned")
		noPinnedFlag, _ := cmd.Flags().GetBool("no-pinned")

		// Template filtering
		includeTemplates, _ := cmd.Flags().GetBool("include-templates")

		// Gate filtering (bd-7zka.2)
		includeGates, _ := cmd.Flags().GetBool("include-gates")

		// Parent filtering (--filter-parent is alias for --parent)
		parentID, _ := cmd.Flags().GetString("parent")
		if parentID == "" {
			// Flag registered; GetString only errors if flag doesn't exist
			parentID, _ = cmd.Flags().GetString("filter-parent")
		}
		noParent, _ := cmd.Flags().GetBool("no-parent")

		// Molecule type filtering
		molTypeStr, _ := cmd.Flags().GetString("mol-type")
		var molType *types.MolType
		if molTypeStr != "" {
			mt := types.MolType(molTypeStr)
			if !mt.IsValid() {
				fmt.Fprintf(os.Stderr, "Error: invalid mol-type %q (must be swarm, patrol, or work)\n", molTypeStr)
				os.Exit(1)
			}
			molType = &mt
		}

		// Wisp type filtering (TTL-based compaction classification)
		wispTypeStr, _ := cmd.Flags().GetString("wisp-type")
		var wispType *types.WispType
		if wispTypeStr != "" {
			wt := types.WispType(wispTypeStr)
			if !wt.IsValid() {
				fmt.Fprintf(os.Stderr, "Error: invalid wisp-type %q (must be heartbeat, ping, patrol, gc_report, recovery, error, or escalation)\n", wispTypeStr)
				os.Exit(1)
			}
			wispType = &wt
		}

		// Time-based scheduling filters (GH#820)
		deferredFlag, _ := cmd.Flags().GetBool("deferred")
		deferAfter, _ := cmd.Flags().GetString("defer-after")
		deferBefore, _ := cmd.Flags().GetString("defer-before")
		dueAfter, _ := cmd.Flags().GetString("due-after")
		dueBefore, _ := cmd.Flags().GetString("due-before")
		overdueFlag, _ := cmd.Flags().GetBool("overdue")

		// Pretty and watch flags (GH#654)
		prettyFormat, _ := cmd.Flags().GetBool("pretty")
		treeFormat, _ := cmd.Flags().GetBool("tree")
		prettyFormat = prettyFormat || treeFormat // --tree is alias for --pretty
		watchMode, _ := cmd.Flags().GetBool("watch")

		// Pager control (bd-jdz3)
		noPager, _ := cmd.Flags().GetBool("no-pager")

		// Ready filter (bd-ihu31)
		readyFlag, _ := cmd.Flags().GetBool("ready")

		// Watch mode implies pretty format
		if watchMode {
			prettyFormat = true
		}

		// Use global jsonOutput set by PersistentPreRun

		// Normalize labels: trim, dedupe, remove empty
		labels = utils.NormalizeLabels(labels)
		labelsAny = utils.NormalizeLabels(labelsAny)

		// Apply directory-aware label scoping if no labels explicitly provided (GH#541)
		if len(labels) == 0 && len(labelsAny) == 0 {
			if dirLabels := config.GetDirectoryLabels(); len(dirLabels) > 0 {
				labelsAny = dirLabels
			}
		}

		// Resolve effective limit. Priority order:
		// 1. Explicit --limit always wins (user intent is clear)
		// 2. --all implies unlimited when --limit is not set (GH#1840)
		// 3. Agent mode uses a lower default for context efficiency
		// 4. Default limit (50) otherwise
		limitChanged := cmd.Flags().Changed("limit")
		effectiveLimit := limit
		switch {
		case limitChanged:
			effectiveLimit = limit // Explicit value (including --limit 0 for unlimited)
		case allFlag:
			effectiveLimit = 0 // --all implies unlimited regardless of other flags
		case ui.IsAgentMode():
			effectiveLimit = 20 // Agent mode default
		}

		filter := types.IssueFilter{
			Limit: effectiveLimit,
		}

		// --ready flag: show only open issues (excludes hooked/in_progress/blocked/deferred) (bd-ihu31)
		if readyFlag {
			s := types.StatusOpen
			filter.Status = &s
		} else if status != "" && status != "all" {
			s := types.Status(status)
			filter.Status = &s
		}

		// Default to non-closed issues unless --all or explicit --status (GH#788)
		if status == "" && !allFlag && !readyFlag {
			filter.ExcludeStatus = []types.Status{types.StatusClosed}
		}
		// Use Changed() to properly handle P0 (priority=0)
		if cmd.Flags().Changed("priority") {
			priorityStr, _ := cmd.Flags().GetString("priority")
			priority, err := validation.ValidatePriority(priorityStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			filter.Priority = &priority
		}
		if assignee != "" {
			filter.Assignee = &assignee
		}
		if issueType != "" {
			t := types.IssueType(issueType)
			filter.IssueType = &t
		}
		if len(labels) > 0 {
			filter.Labels = labels
		}
		if len(labelsAny) > 0 {
			filter.LabelsAny = labelsAny
		}
		if labelPattern != "" {
			filter.LabelPattern = labelPattern
		}
		if labelRegex != "" {
			filter.LabelRegex = labelRegex
		}
		if titleSearch != "" {
			filter.TitleSearch = titleSearch
		}
		if idFilter != "" {
			ids := utils.NormalizeLabels(strings.Split(idFilter, ","))
			if len(ids) > 0 {
				filter.IDs = ids
			}
		}
		if specPrefix != "" {
			filter.SpecIDPrefix = specPrefix
		}

		// Pattern matching
		if titleContains != "" {
			filter.TitleContains = titleContains
		}
		if descContains != "" {
			filter.DescriptionContains = descContains
		}
		if notesContains != "" {
			filter.NotesContains = notesContains
		}

		// Date ranges
		if createdAfter != "" {
			t, err := parseTimeFlag(createdAfter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --created-after: %v\n", err)
				os.Exit(1)
			}
			filter.CreatedAfter = &t
		}
		if createdBefore != "" {
			t, err := parseTimeFlag(createdBefore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --created-before: %v\n", err)
				os.Exit(1)
			}
			filter.CreatedBefore = &t
		}
		if updatedAfter != "" {
			t, err := parseTimeFlag(updatedAfter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --updated-after: %v\n", err)
				os.Exit(1)
			}
			filter.UpdatedAfter = &t
		}
		if updatedBefore != "" {
			t, err := parseTimeFlag(updatedBefore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --updated-before: %v\n", err)
				os.Exit(1)
			}
			filter.UpdatedBefore = &t
		}
		if closedAfter != "" {
			t, err := parseTimeFlag(closedAfter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --closed-after: %v\n", err)
				os.Exit(1)
			}
			filter.ClosedAfter = &t
		}
		if closedBefore != "" {
			t, err := parseTimeFlag(closedBefore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --closed-before: %v\n", err)
				os.Exit(1)
			}
			filter.ClosedBefore = &t
		}

		// Empty/null checks
		if emptyDesc {
			filter.EmptyDescription = true
		}
		if noAssignee {
			filter.NoAssignee = true
		}
		if noLabels {
			filter.NoLabels = true
		}

		// Priority ranges
		if cmd.Flags().Changed("priority-min") {
			priorityMin, err := validation.ValidatePriority(priorityMinStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --priority-min: %v\n", err)
				os.Exit(1)
			}
			filter.PriorityMin = &priorityMin
		}
		if cmd.Flags().Changed("priority-max") {
			priorityMax, err := validation.ValidatePriority(priorityMaxStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --priority-max: %v\n", err)
				os.Exit(1)
			}
			filter.PriorityMax = &priorityMax
		}

		// Pinned filtering: --pinned and --no-pinned are mutually exclusive
		if pinnedFlag && noPinnedFlag {
			fmt.Fprintf(os.Stderr, "Error: --pinned and --no-pinned are mutually exclusive\n")
			os.Exit(1)
		}
		if pinnedFlag {
			pinned := true
			filter.Pinned = &pinned
		} else if noPinnedFlag {
			pinned := false
			filter.Pinned = &pinned
		}

		// Template filtering: exclude templates by default
		// Use --include-templates to show all issues including templates
		if !includeTemplates {
			isTemplate := false
			filter.IsTemplate = &isTemplate
		}

		// Gate filtering: exclude gate issues by default (bd-7zka.2)
		// Use --include-gates or --type gate to show gate issues
		if !includeGates && issueType != "gate" {
			filter.ExcludeTypes = append(filter.ExcludeTypes, "gate")
		}

		// Parent filtering: filter children by parent issue
		if parentID != "" && noParent {
			fmt.Fprintf(os.Stderr, "Error: --parent and --no-parent are mutually exclusive\n")
			os.Exit(1)
		}
		if parentID != "" {
			filter.ParentID = &parentID
		}
		if noParent {
			filter.NoParent = true
		}

		// Molecule type filtering
		if molType != nil {
			filter.MolType = molType
		}

		// Wisp type filtering
		if wispType != nil {
			filter.WispType = wispType
		}

		// Time-based scheduling filters (GH#820)
		if deferredFlag {
			filter.Deferred = true
		}
		if deferAfter != "" {
			t, err := parseTimeFlag(deferAfter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --defer-after: %v\n", err)
				os.Exit(1)
			}
			filter.DeferAfter = &t
		}
		if deferBefore != "" {
			t, err := parseTimeFlag(deferBefore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --defer-before: %v\n", err)
				os.Exit(1)
			}
			filter.DeferBefore = &t
		}
		if dueAfter != "" {
			t, err := parseTimeFlag(dueAfter)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --due-after: %v\n", err)
				os.Exit(1)
			}
			filter.DueAfter = &t
		}
		if dueBefore != "" {
			t, err := parseTimeFlag(dueBefore)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --due-before: %v\n", err)
				os.Exit(1)
			}
			filter.DueBefore = &t
		}
		if overdueFlag {
			filter.Overdue = true
		}

		ctx := rootCtx

		// Handle --rig flag: query a different rig's database
		rigOverride, _ := cmd.Flags().GetString("rig")
		activeStore := store
		if rigOverride != "" {
			rigStore, err := openStoreForRig(ctx, rigOverride)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = rigStore.Close() }() // Best effort cleanup
			activeStore = rigStore
		}

		// Direct mode
		issues, err := activeStore.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Apply sorting
		sortIssues(issues, sortBy, reverse)

		// Handle watch mode (GH#654) - must be before other output modes
		if watchMode {
			watchIssues(ctx, activeStore, filter, sortBy, reverse)
			return
		}

		// Handle pretty format (GH#654)
		if prettyFormat {
			// Special handling for --tree --parent combination (hierarchical descendants)
			if parentID != "" {
				treeIssues, err := getHierarchicalChildren(ctx, activeStore, "", 0, parentID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}

				if len(treeIssues) == 0 {
					fmt.Printf("Issue '%s' has no children\n", parentID)
					return
				}

				// Load dependencies for tree structure
				// Best effort: display gracefully degrades with empty data
				allDeps, _ := activeStore.GetAllDependencyRecords(ctx)
				displayPrettyListWithDeps(treeIssues, false, allDeps)
				return
			}

			// Regular tree display (no parent filter)
			// Load dependencies for tree structure
			// Best effort: display gracefully degrades with empty data
			allDeps, _ := activeStore.GetAllDependencyRecords(ctx)
			displayPrettyListWithDeps(issues, false, allDeps)
			// Show truncation hint if we hit the limit (GH#788)
			if effectiveLimit > 0 && len(issues) == effectiveLimit {
				fmt.Fprintf(os.Stderr, "\nShowing %d issues (use --limit 0 for all)\n", effectiveLimit)
			}
			return
		}

		// Handle format flag
		if formatStr != "" {
			if err := outputFormattedList(ctx, activeStore, issues, formatStr); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		}

		if jsonOutput {
			// Get labels and dependency counts in bulk (single query instead of N queries)
			issueIDs := make([]string, len(issues))
			for i, issue := range issues {
				issueIDs[i] = issue.ID
			}
			// Best effort: display gracefully degrades with empty data
			labelsMap, _ := activeStore.GetLabelsForIssues(ctx, issueIDs)
			depCounts, _ := activeStore.GetDependencyCounts(ctx, issueIDs)
			allDeps, _ := activeStore.GetDependencyRecordsForIssues(ctx, issueIDs)
			commentCounts, _ := activeStore.GetCommentCounts(ctx, issueIDs)

			// Populate labels and dependencies for JSON output
			for _, issue := range issues {
				issue.Labels = labelsMap[issue.ID]
				issue.Dependencies = allDeps[issue.ID]
			}

			// Build response with counts
			issuesWithCounts := make([]*types.IssueWithCounts, len(issues))
			for i, issue := range issues {
				counts := depCounts[issue.ID]
				if counts == nil {
					counts = &types.DependencyCounts{DependencyCount: 0, DependentCount: 0}
				}
				issuesWithCounts[i] = &types.IssueWithCounts{
					Issue:           issue,
					DependencyCount: counts.DependencyCount,
					DependentCount:  counts.DependentCount,
					CommentCount:    commentCounts[issue.ID],
				}
			}
			outputJSON(issuesWithCounts)
			return
		}

		// Show upgrade notification if needed
		maybeShowUpgradeNotification()

		// Load labels in bulk for display
		issueIDs := make([]string, len(issues))
		for i, issue := range issues {
			issueIDs[i] = issue.ID
		}
		// Best effort: display gracefully degrades with empty data
		labelsMap, _ := activeStore.GetLabelsForIssues(ctx, issueIDs)

		// Load blocking info for displayed issues only (bd-7di).
		// Previously loaded ALL dependency records which was O(total_issues) and took 2-4s.
		// Now scoped to only the displayed issues, making it O(displayed_issues).
		// Best effort: display gracefully degrades with empty data
		blockedByMap, blocksMap, _ := activeStore.GetBlockingInfoForIssues(ctx, issueIDs)

		// Build output in buffer for pager support (bd-jdz3)
		var buf strings.Builder
		if ui.IsAgentMode() {
			// Agent mode: ultra-compact, no colors, no pager
			for _, issue := range issues {
				formatAgentIssue(&buf, issue, blockedByMap[issue.ID], blocksMap[issue.ID])
			}
			fmt.Print(buf.String())
			return
		} else if longFormat {
			// Long format: multi-line with details
			buf.WriteString(fmt.Sprintf("\nFound %d issues:\n\n", len(issues)))
			for _, issue := range issues {
				labels := labelsMap[issue.ID]
				formatIssueLong(&buf, issue, labels)
			}
		} else {
			// Compact format: one line per issue
			for _, issue := range issues {
				labels := labelsMap[issue.ID]
				formatIssueCompact(&buf, issue, labels, blockedByMap[issue.ID], blocksMap[issue.ID])
			}
		}

		// Output with pager support
		if err := ui.ToPager(buf.String(), ui.PagerOptions{NoPager: noPager}); err != nil {
			if _, writeErr := fmt.Fprint(os.Stdout, buf.String()); writeErr != nil {
				fmt.Fprintf(os.Stderr, "Error writing output: %v\n", writeErr)
			}
		}

		// Show truncation hint if we hit the limit (GH#788)
		if effectiveLimit > 0 && len(issues) == effectiveLimit {
			fmt.Fprintf(os.Stderr, "\nShowing %d issues (use --limit 0 for all)\n", effectiveLimit)
		}

		// Show tip after successful list (direct mode only)
		maybeShowTip(store)
	},
}

func init() {
	listCmd.Flags().StringP("status", "s", "", "Filter by status (open, in_progress, blocked, deferred, closed)")
	registerPriorityFlag(listCmd, "")
	listCmd.Flags().StringP("assignee", "a", "", "Filter by assignee")
	listCmd.Flags().StringP("type", "t", "", "Filter by type (bug, feature, task, epic, chore, decision, merge-request, molecule, gate, convoy). Aliases: mr→merge-request, feat→feature, mol→molecule, dec/adr→decision")
	listCmd.Flags().StringSliceP("label", "l", []string{}, "Filter by labels (AND: must have ALL). Can combine with --label-any")
	listCmd.Flags().StringSlice("label-any", []string{}, "Filter by labels (OR: must have AT LEAST ONE). Can combine with --label")
	listCmd.Flags().String("label-pattern", "", "Filter by label glob pattern (e.g., 'tech-*' matches tech-debt, tech-legacy)")
	listCmd.Flags().String("label-regex", "", "Filter by label regex pattern (e.g., 'tech-(debt|legacy)')")
	listCmd.Flags().String("title", "", "Filter by title text (case-insensitive substring match)")
	listCmd.Flags().String("spec", "", "Filter by spec_id prefix")
	listCmd.Flags().String("id", "", "Filter by specific issue IDs (comma-separated, e.g., bd-1,bd-5,bd-10)")
	listCmd.Flags().IntP("limit", "n", 50, "Limit results (default 50, use 0 for unlimited)")
	listCmd.Flags().String("format", "", "Output format: 'digraph' (for golang.org/x/tools/cmd/digraph), 'dot' (Graphviz), or Go template")
	listCmd.Flags().Bool("all", false, "Show all issues including closed (overrides default filter)")
	listCmd.Flags().Bool("long", false, "Show detailed multi-line output for each issue")
	listCmd.Flags().String("sort", "", "Sort by field: priority, created, updated, closed, status, id, title, type, assignee")
	listCmd.Flags().BoolP("reverse", "r", false, "Reverse sort order")

	// Pattern matching
	listCmd.Flags().String("title-contains", "", "Filter by title substring (case-insensitive)")
	listCmd.Flags().String("desc-contains", "", "Filter by description substring (case-insensitive)")
	listCmd.Flags().String("notes-contains", "", "Filter by notes substring (case-insensitive)")

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
	listCmd.Flags().Bool("tree", false, "Alias for --pretty: hierarchical tree format")
	listCmd.Flags().BoolP("watch", "w", false, "Watch for changes and auto-update display (implies --pretty)")

	// Pager control (bd-jdz3)
	listCmd.Flags().Bool("no-pager", false, "Disable pager output")

	// Ready filter: show only issues ready to be worked on (bd-ihu31)
	listCmd.Flags().Bool("ready", false, "Show only ready issues (status=open, excludes hooked/in_progress/blocked/deferred)")

	// Cross-rig routing: query a different rig's database (bd-rgdjr)
	listCmd.Flags().String("rig", "", "Query a different rig's database (e.g., --rig gastown, --rig gt-, --rig gt)")

	// Note: --json flag is defined as a persistent flag in main.go, not here
	rootCmd.AddCommand(listCmd)
}
