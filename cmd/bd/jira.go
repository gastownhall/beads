package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/jira"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

var jiraCmd = &cobra.Command{
	Use:     "jira",
	GroupID: "advanced",
	Short:   "Jira integration commands",
	Long: `Synchronize issues between beads and Jira.

Configuration:
  bd config set jira.url "https://company.atlassian.net"
  bd config set jira.project "PROJ"
  bd config set jira.projects "PROJ1,PROJ2"   # Multiple projects
  bd config set jira.api_token "YOUR_TOKEN"
  bd config set jira.username "your_email@company.com"  # For Jira Cloud
  bd config set jira.push_prefix "hippo"       # Only push hippo-* issues to Jira
  bd config set jira.push_prefix "proj1,proj2" # Multiple prefixes (comma-separated)

Environment variables (alternative to config):
  JIRA_API_TOKEN  - Jira API token
  JIRA_USERNAME   - Jira username/email
  JIRA_PROJECTS   - Comma-separated project keys

Examples:
  bd jira sync --pull         # Import issues from Jira
  bd jira sync --push         # Export issues to Jira
  bd jira sync                # Bidirectional sync (pull then push)
  bd jira sync --dry-run      # Preview sync without changes
  bd jira status              # Show sync status`,
}

var jiraSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Synchronize issues with Jira",
	Long: `Synchronize issues between beads and Jira.

Modes:
  --pull         Import issues from Jira into beads
  --push         Export issues from beads to Jira
  (no flags)     Bidirectional sync: pull then push, with conflict resolution

Conflict Resolution:
  By default, newer timestamp wins. Override with:
  --prefer-local   Always prefer local beads version
  --prefer-jira    Always prefer Jira version

Examples:
  bd jira sync --pull                # Import from Jira
  bd jira sync --push --create-only  # Push new issues only
  bd jira sync --dry-run             # Preview without changes
  bd jira sync --prefer-local        # Bidirectional, local wins`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runJiraSync,
}

var jiraStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Jira sync status",
	Long: `Show the current Jira sync status, including:
  - Last sync timestamp
  - Configuration status
  - Number of issues with Jira links
  - Issues pending push (no external_ref)`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runJiraStatus,
}

func init() {
	jiraSyncCmd.Flags().Bool("pull", false, "Pull issues from Jira")
	jiraSyncCmd.Flags().Bool("push", false, "Push issues to Jira")
	jiraSyncCmd.Flags().Bool("dry-run", false, "Preview sync without making changes")
	jiraSyncCmd.Flags().Bool("prefer-local", false, "Prefer local version on conflicts")
	jiraSyncCmd.Flags().Bool("prefer-jira", false, "Prefer Jira version on conflicts")
	jiraSyncCmd.Flags().Bool("create-only", false, "Only create new issues, don't update existing")
	jiraSyncCmd.Flags().String("state", "all", "Issue state to sync: open, closed, all")
	jiraSyncCmd.Flags().StringSlice("project", nil, "Project key(s) to sync (overrides configured project/projects)")
	registerSelectiveSyncFlags(jiraSyncCmd)

	jiraCmd.AddCommand(jiraSyncCmd)
	jiraCmd.AddCommand(jiraStatusCmd)
	rootCmd.AddCommand(jiraCmd)
}

func runJiraSync(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("jira-sync")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	pull, _ := cmd.Flags().GetBool("pull")
	push, _ := cmd.Flags().GetBool("push")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	preferLocal, _ := cmd.Flags().GetBool("prefer-local")
	preferJira, _ := cmd.Flags().GetBool("prefer-jira")
	createOnly, _ := cmd.Flags().GetBool("create-only")
	state, _ := cmd.Flags().GetString("state")

	if !dryRun {
		CheckReadonly("jira sync")
	}

	if preferLocal && preferJira {
		return HandleErrorRespectJSON("cannot use both --prefer-local and --prefer-jira")
	}

	trackerStore, err := trackerStoreForCommand(rootCtx)
	if err != nil {
		return HandleErrorRespectJSON("database not available: %v", err)
	}

	if err := validateJiraConfigForStore(trackerStore); err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	ctx := rootCtx

	jt := &jira.Tracker{}
	cliProjects, _ := cmd.Flags().GetStringSlice("project")
	if len(cliProjects) > 0 {
		jt.SetProjectKeys(tracker.DeduplicateStrings(cliProjects))
	}
	if err := jt.Init(ctx, trackerStore); err != nil {
		return HandleErrorRespectJSON("initializing Jira tracker: %v", err)
	}

	engine := tracker.NewEngine(jt, trackerStore, currentActor())
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	engine.PushHooks = buildJiraPushHooksForStore(ctx, trackerStore)

	opts := tracker.SyncOptions{
		Pull:       pull,
		Push:       push,
		DryRun:     dryRun,
		CreateOnly: createOnly,
		State:      state,
	}

	if err := applySelectiveSyncFlags(cmd, &opts, push); err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	if preferLocal {
		opts.ConflictResolution = tracker.ConflictLocal
	} else if preferJira {
		opts.ConflictResolution = tracker.ConflictExternal
	} else {
		opts.ConflictResolution = tracker.ConflictTimestamp
	}

	effectivePush := push || (!push && !pull)
	var preLinkedIDs map[string]bool
	if effectivePush && opts.CreateOnly && !dryRun {
		preLinkedIDs = snapshotJiraLinkedIssueIDs(ctx, trackerStore, jt)
	}

	result, err := engine.Sync(ctx, opts)
	if err != nil {
		if jsonOutput {
			if jerr := outputJSON(result); jerr != nil {
				return jerr
			}
			return SilentExit()
		}
		return HandleError("%v", err)
	}

	if effectivePush && result.Success {
		reconcileJiraParentsForStore(ctx, trackerStore, jt, engine.PushHooks, opts, preLinkedIDs, dryRun, jsonOutput, result)
	}

	if jsonOutput {
		return outputJSON(result)
	}
	if dryRun {
		if len(result.Warnings) > 0 {
			fmt.Println("\nWarnings:")
			for _, w := range result.Warnings {
				fmt.Printf("  - %s\n", w)
			}
		}
		fmt.Println("\n✓ Dry run complete (no changes made)")
		return nil
	}
	if result.Stats.Pulled > 0 {
		fmt.Printf("✓ Pulled %d issues (%d created, %d updated)\n",
			result.Stats.Pulled, result.Stats.Created, result.Stats.Updated)
	}
	if result.Stats.Pushed > 0 {
		fmt.Printf("✓ Pushed %d issues\n", result.Stats.Pushed)
	}
	if result.Stats.Conflicts > 0 {
		fmt.Printf("→ Resolved %d conflicts\n", result.Stats.Conflicts)
	}
	fmt.Println("\n✓ Jira sync complete")
	if len(result.Warnings) > 0 {
		fmt.Println("\nWarnings:")
		for _, w := range result.Warnings {
			fmt.Printf("  - %s\n", w)
		}
	}
	return nil
}

// buildJiraPushHooks creates PushHooks for Jira-specific push behavior.
func buildJiraPushHooks(ctx context.Context) *tracker.PushHooks {
	return buildJiraPushHooksForStore(ctx, tracker.NewStore(store))
}

func buildJiraPushHooksForStore(ctx context.Context, st tracker.Store) *tracker.PushHooks {
	return &tracker.PushHooks{
		ShouldPush: func(issue *types.Issue) bool {
			pushPrefix, _ := st.GetConfig(ctx, "jira.push_prefix")
			if pushPrefix == "" {
				return true
			}
			for _, prefix := range strings.Split(pushPrefix, ",") {
				prefix = strings.TrimSpace(prefix)
				prefix = strings.TrimSuffix(prefix, "-")
				if prefix != "" && strings.HasPrefix(issue.ID, prefix+"-") {
					return true
				}
			}
			return false
		},
	}
}

func snapshotJiraLinkedIssueIDs(ctx context.Context, st tracker.Store, jt *jira.Tracker) map[string]bool {
	if st == nil || jt == nil {
		return nil
	}
	// jira sync is a round-trip path — opt out of BEADS_MAX_ROWS (designer §4.1).
	issues, err := st.SearchIssues(ctx, "", types.IssueFilter{
		MaxRows:       0,
		MaxRowsSource: "",
	})
	if err != nil {
		return nil
	}
	linked := make(map[string]bool)
	for _, issue := range issues {
		if issue == nil || issue.ExternalRef == nil {
			continue
		}
		ref := strings.TrimSpace(*issue.ExternalRef)
		if ref != "" && jt.IsExternalRef(ref) && jt.ExtractIdentifier(ref) != "" {
			linked[issue.ID] = true
		}
	}
	return linked
}

func reconcileJiraParentsForStore(
	ctx context.Context,
	st tracker.Store,
	jt *jira.Tracker,
	pushHooks *tracker.PushHooks,
	opts tracker.SyncOptions,
	preLinkedIDs map[string]bool,
	dryRun bool,
	isJSON bool,
	result *tracker.SyncResult,
) {
	if jt == nil || st == nil || result == nil {
		return
	}

	links, unresolvedWarnings, hierarchyErrors, err := buildJiraParentLinksForStore(ctx, st, jt, pushHooks, opts, preLinkedIDs, dryRun)
	if err != nil {
		msg := fmt.Sprintf("parent reconcile: building link set failed: %v", err)
		result.Warnings = append(result.Warnings, msg)
		result.Stats.Errors++
		if !isJSON {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		}
		return
	}

	for _, w := range unresolvedWarnings {
		result.Warnings = append(result.Warnings, w)
		if !isJSON {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
	}
	for _, herr := range hierarchyErrors {
		msg := fmt.Sprintf("parent reconcile: %v", herr)
		result.Warnings = append(result.Warnings, msg)
		result.Stats.Errors++
		if !isJSON {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		}
	}

	if len(links) == 0 {
		return
	}

	stats, err := jt.ReconcileParents(ctx, links, dryRun)
	if stats != nil && !isJSON {
		if dryRun {
			if stats.WouldUpdate > 0 {
				fmt.Printf("[dry-run] Would reconcile %d Jira parent link%s\n",
					stats.WouldUpdate, plural(stats.WouldUpdate))
				for _, m := range stats.Mutations {
					childDisplay := m.ChildIdentifier
					if childDisplay == "" {
						childDisplay = m.ChildBeadID + " (new)"
					}
					parentDisplay := m.ParentIdentifier
					if parentDisplay == "" {
						parentDisplay = m.ParentBeadID + " (new)"
					}
					if m.Action == "reparent" && m.CurrentParentKey != "" {
						fmt.Printf("  [dry-run] Would reparent %s (%s → %s)\n",
							childDisplay, m.CurrentParentKey, parentDisplay)
					} else {
						fmt.Printf("  [dry-run] Would set parent of %s → %s\n",
							childDisplay, parentDisplay)
					}
				}
			}
		} else if stats.Updated > 0 {
			fmt.Printf("✓ Reconciled %d Jira parent link%s\n",
				stats.Updated, plural(stats.Updated))
		}
	}
	if err != nil {
		msg := fmt.Sprintf("parent reconcile: %v", err)
		result.Warnings = append(result.Warnings, msg)
		result.Stats.Errors++
		if !isJSON {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		}
		return
	}
	if stats != nil {
		for _, missing := range stats.NotFound {
			msg := fmt.Sprintf("parent reconcile: Jira issue %s not found; parent link skipped", missing)
			result.Warnings = append(result.Warnings, msg)
			if !isJSON {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
			}
		}
		for _, e := range stats.Errors {
			msg := fmt.Sprintf("parent reconcile: %v", e)
			result.Warnings = append(result.Warnings, msg)
			result.Stats.Errors++
			if !isJSON {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
			}
		}
	}
}

func buildJiraParentLinksForStore(
	ctx context.Context,
	st tracker.Store,
	jt *jira.Tracker,
	pushHooks *tracker.PushHooks,
	opts tracker.SyncOptions,
	preLinkedIDs map[string]bool,
	dryRun bool,
) ([]jira.ParentLink, []string, []error, error) {
	if st == nil {
		return nil, nil, nil, fmt.Errorf("database not available")
	}

	// jira sync is a round-trip path — opt out of BEADS_MAX_ROWS (designer §4.1).
	issues, err := st.SearchIssues(ctx, "", types.IssueFilter{
		MaxRows:       0,
		MaxRowsSource: "",
	})
	if err != nil {
		return nil, nil, nil, err
	}

	byID := make(map[string]*types.Issue, len(issues))
	for _, issue := range issues {
		if issue != nil && issue.ID != "" {
			byID[issue.ID] = issue
		}
	}

	var issueIDSet map[string]bool
	if len(opts.IssueIDs) > 0 {
		issueIDSet = make(map[string]bool, len(opts.IssueIDs))
		for _, id := range opts.IssueIDs {
			issueIDSet[id] = true
		}
	}

	var descendantSet map[string]bool
	if opts.ParentID != "" {
		descendantSet, err = buildJiraDescendantSet(ctx, st, opts.ParentID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("resolving parent %s: %w", opts.ParentID, err)
		}
	}

	scopedIssues := make([]*types.Issue, 0, len(issues))
	scopedForCreate := make(map[string]bool, len(issues))
	for _, issue := range issues {
		if issue == nil || issue.ID == "" {
			continue
		}
		if issueIDSet != nil && !issueIDSet[issue.ID] {
			continue
		}
		if descendantSet != nil && !descendantSet[issue.ID] {
			continue
		}
		if !jiraIssueAllowedByPushFilters(issue, opts, pushHooks) {
			continue
		}
		scopedForCreate[issue.ID] = true
		if opts.CreateOnly {
			if dryRun {
				if issue.ExternalRef != nil && jt.IsExternalRef(strings.TrimSpace(*issue.ExternalRef)) {
					continue
				}
			} else if preLinkedIDs[issue.ID] {
				continue
			}
		}
		scopedIssues = append(scopedIssues, issue)
	}
	sort.Slice(scopedIssues, func(i, j int) bool {
		return scopedIssues[i].ID < scopedIssues[j].ID
	})

	mapper := jt.FieldMapper()
	var links []jira.ParentLink
	var unresolvedWarnings []string
	var hierarchyErrors []error

	for _, child := range scopedIssues {
		deps, err := st.GetDependenciesWithMetadata(ctx, child.ID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("loading deps for %s: %w", child.ID, err)
		}

		seenParents := make(map[string]bool)
		var parentDeps []*types.IssueWithDependencyMetadata
		var parentIDs []string
		for _, d := range deps {
			if d == nil || d.DependencyType != types.DepParentChild {
				continue
			}
			pid := strings.TrimSpace(d.Issue.ID)
			if pid == "" || seenParents[pid] {
				continue
			}
			seenParents[pid] = true
			parentDeps = append(parentDeps, d)
			parentIDs = append(parentIDs, pid)
		}

		if len(parentDeps) == 0 {
			continue
		}
		if len(parentDeps) > 1 {
			hierarchyErrors = append(hierarchyErrors, fmt.Errorf(
				"unsupported Jira hierarchy: %s has multiple parent-child dependencies (%s); Jira supports only one parent per issue",
				child.ID, strings.Join(parentIDs, ", ")))
			continue
		}

		parentID := parentIDs[0]
		if parentID == child.ID {
			hierarchyErrors = append(hierarchyErrors, fmt.Errorf(
				"unsupported Jira hierarchy: cannot set %s as its own parent", child.ID))
			continue
		}

		parent := byID[parentID]
		if parent == nil {
			p := parentDeps[0].Issue
			parent = &p
		}

		childRef := ""
		if child.ExternalRef != nil {
			childRef = strings.TrimSpace(*child.ExternalRef)
		}
		childKey := ""
		if childRef != "" {
			if !jt.IsExternalRef(childRef) {
				unresolvedWarnings = append(unresolvedWarnings, fmt.Sprintf(
					"parent link for %s → %s skipped: child external_ref %q is not a Jira reference",
					child.ID, parent.ID, childRef))
				continue
			}
			childKey = strings.TrimSpace(jt.ExtractIdentifier(childRef))
			if childKey == "" {
				unresolvedWarnings = append(unresolvedWarnings, fmt.Sprintf(
					"parent link for %s → %s skipped: could not extract Jira key from child external_ref %q",
					child.ID, parent.ID, childRef))
				continue
			}
		} else if !dryRun {
			// Child was not created in Jira during this push (already reported by engine).
			continue
		}

		parentRef := ""
		if parent.ExternalRef != nil {
			parentRef = strings.TrimSpace(*parent.ExternalRef)
		}
		parentKey := ""
		if parentRef != "" {
			if !jt.IsExternalRef(parentRef) {
				unresolvedWarnings = append(unresolvedWarnings, fmt.Sprintf(
					"parent link for %s → %s skipped: parent external_ref %q is not a Jira reference",
					child.ID, parent.ID, parentRef))
				continue
			}
			parentKey = strings.TrimSpace(jt.ExtractIdentifier(parentRef))
			if parentKey == "" {
				unresolvedWarnings = append(unresolvedWarnings, fmt.Sprintf(
					"parent link for %s → %s skipped: could not extract Jira key from parent external_ref %q",
					child.ID, parent.ID, parentRef))
				continue
			}
		} else if !dryRun || !scopedForCreate[parent.ID] {
			unresolvedWarnings = append(unresolvedWarnings, fmt.Sprintf(
				"parent link for %s → %s skipped: parent %s has no Jira external_ref (include parent in push scope or link it to a Jira issue)",
				child.ID, parent.ID, parent.ID))
			continue
		}

		links = append(links, jira.ParentLink{
			ChildBeadID:      child.ID,
			ChildIdentifier:  childKey,
			ChildJiraType:    jira.ResolveJiraTypeForBead(child, mapper),
			ParentBeadID:     parent.ID,
			ParentIdentifier: parentKey,
			ParentJiraType:   jira.ResolveJiraTypeForBead(parent, mapper),
		})
	}

	return links, unresolvedWarnings, hierarchyErrors, nil
}

func buildJiraDescendantSet(ctx context.Context, st tracker.Store, parentID string) (map[string]bool, error) {
	result := map[string]bool{parentID: true}
	queue := []string{parentID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		dependents, err := st.GetDependentsWithMetadata(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("getting dependents of %s: %w", current, err)
		}
		for _, dep := range dependents {
			if dep.DependencyType == types.DepParentChild && !result[dep.Issue.ID] {
				result[dep.Issue.ID] = true
				queue = append(queue, dep.Issue.ID)
			}
		}
	}
	return result, nil
}

func jiraIssueAllowedByPushFilters(issue *types.Issue, opts tracker.SyncOptions, pushHooks *tracker.PushHooks) bool {
	if issue == nil {
		return false
	}
	if opts.ExcludeEphemeral && issue.Ephemeral {
		return false
	}
	if len(opts.TypeFilter) > 0 {
		matched := false
		for _, t := range opts.TypeFilter {
			if issue.IssueType == t {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, t := range opts.ExcludeTypes {
		if issue.IssueType == t {
			return false
		}
	}
	if opts.ExcludeIDPrefix != "" && strings.HasPrefix(issue.ID, opts.ExcludeIDPrefix) {
		return false
	}
	for _, p := range opts.ExcludeIDPatterns {
		if p != "" && strings.Contains(issue.ID, p) {
			return false
		}
	}
	if opts.State == "open" && issue.Status == types.StatusClosed {
		return false
	}
	if pushHooks != nil && pushHooks.ShouldPush != nil && !pushHooks.ShouldPush(issue) {
		return false
	}
	return true
}

func runJiraStatus(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("jira-status")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	trackerStore, err := trackerStoreForCommand(rootCtx)
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	jiraURL, _ := trackerStore.GetConfig(ctx, "jira.url")
	lastSync, _ := trackerStore.GetConfig(ctx, "jira.last_sync")

	pluralProjects, _ := trackerStore.GetConfig(ctx, "jira.projects")
	singularProject, _ := trackerStore.GetConfig(ctx, "jira.project")
	projectKeys := tracker.ResolveProjectIDs(nil, pluralProjects, singularProject)

	configured := jiraURL != "" && len(projectKeys) > 0

	// jira sync is a round-trip path — opt out of BEADS_MAX_ROWS
	// (designer §4.1) so a misconfigured env doesn't abort partway.
	allIssues, err := trackerStore.SearchIssues(ctx, "", types.IssueFilter{
		MaxRows:       0,
		MaxRowsSource: "",
	})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}

	withJiraRef := 0
	pendingPush := 0
	for _, issue := range allIssues {
		if issue.ExternalRef != nil && jira.IsJiraExternalRef(*issue.ExternalRef, jiraURL) {
			withJiraRef++
		} else if issue.ExternalRef == nil {
			pendingPush++
		}
	}

	if jsonOutput {
		primaryProject := ""
		if len(projectKeys) > 0 {
			primaryProject = projectKeys[0]
		}
		return outputJSON(map[string]interface{}{
			"configured":    configured,
			"jira_url":      jiraURL,
			"jira_project":  primaryProject,
			"jira_projects": projectKeys,
			"last_sync":     lastSync,
			"total_issues":  len(allIssues),
			"with_jira_ref": withJiraRef,
			"pending_push":  pendingPush,
		})
	}

	fmt.Println("Jira Sync Status")
	fmt.Println("================")
	fmt.Println()

	if !configured {
		fmt.Println("Status: Not configured")
		fmt.Println()
		fmt.Println("To configure Jira integration:")
		fmt.Println("  bd config set jira.url \"https://company.atlassian.net\"")
		fmt.Println("  bd config set jira.project \"PROJ\"")
		fmt.Println("  bd config set jira.projects \"PROJ1,PROJ2\"  # multiple projects")
		fmt.Println("  bd config set jira.api_token \"YOUR_TOKEN\"")
		fmt.Println("  bd config set jira.username \"your@email.com\"")
		return nil
	}

	fmt.Printf("Jira URL:     %s\n", jiraURL)
	if len(projectKeys) == 1 {
		fmt.Printf("Project:      %s\n", projectKeys[0])
	} else {
		fmt.Printf("Projects:     %s (%d projects)\n", strings.Join(projectKeys, ", "), len(projectKeys))
	}
	if lastSync != "" {
		fmt.Printf("Last Sync:    %s\n", lastSync)
	} else {
		fmt.Println("Last Sync:    Never")
	}
	fmt.Println()
	fmt.Printf("Total Issues: %d\n", len(allIssues))
	fmt.Printf("With Jira:    %d\n", withJiraRef)
	fmt.Printf("Local Only:   %d\n", pendingPush)

	if pendingPush > 0 {
		fmt.Println()
		fmt.Printf("Run 'bd jira sync --push' to push %d local issue(s) to Jira\n", pendingPush)
	}
	return nil
}

// validateJiraConfig checks that required Jira configuration is present.
func validateJiraConfig() error {
	return validateJiraConfigForStore(tracker.NewStore(store))
}

func validateJiraConfigForStore(st tracker.Store) error {
	ctx := rootCtx
	if st == nil {
		return fmt.Errorf("database not available")
	}
	jiraURL, _ := st.GetConfig(ctx, "jira.url")

	if jiraURL == "" {
		return fmt.Errorf("jira.url not configured\nRun: bd config set jira.url \"https://company.atlassian.net\"")
	}

	// Check for project configuration (singular or plural).
	pluralProjects, _ := st.GetConfig(ctx, "jira.projects")
	singularProject, _ := st.GetConfig(ctx, "jira.project")
	projectKeys := tracker.ResolveProjectIDs(nil, pluralProjects, singularProject)
	if len(projectKeys) == 0 {
		return fmt.Errorf("no Jira project configured\nRun: bd config set jira.project \"PROJ\"\nOr:  bd config set jira.projects \"PROJ1,PROJ2\"")
	}

	apiToken, _ := st.GetConfig(ctx, "jira.api_token")
	if apiToken == "" && os.Getenv("JIRA_API_TOKEN") == "" {
		return fmt.Errorf("Jira API token not configured\nRun: bd config set jira.api_token \"YOUR_TOKEN\"\nOr: export JIRA_API_TOKEN=YOUR_TOKEN")
	}

	return nil
}
