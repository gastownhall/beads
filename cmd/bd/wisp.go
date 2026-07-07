package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// Wisp commands - manage ephemeral molecules
//
// Wisps are ephemeral issues with Ephemeral=true in the main database.
// They're used for patrol cycles and operational loops that shouldn't
// be synced via git.
//
// Commands:
//   bd mol wisp list    - List all wisps in current context
//   bd mol wisp gc      - Garbage collect orphaned wisps

var wispCmd = &cobra.Command{
	Use:   "wisp [proto-id]",
	Short: "Create or manage wisps (ephemeral molecules)",
	Long: `Create or manage wisps - EPHEMERAL molecules for operational workflows.

When called with a proto-id argument, creates a wisp from that proto.
When called with a subcommand (list, gc), manages existing wisps.

Wisps are issues with Ephemeral=true in the main database. They're stored
locally but NOT synced via git.

WHEN TO USE WISP vs POUR:
  wisp (vapor): Ephemeral work that auto-cleans up
    - Release workflows (one-time execution)
    - Operational loops and recurring cycles
    - Health checks and diagnostics
    - Any operational workflow without audit value

  pour (liquid): Persistent work that needs audit trail
    - Feature implementations spanning multiple sessions
    - Work you may need to reference later
    - Anything worth preserving in git history

TIP: Formulas can specify phase:"vapor" to recommend wisp usage.
     If you use pour on a vapor-phase formula, you'll get a warning.

The wisp lifecycle:
  1. Create: bd mol wisp <proto> or bd create --ephemeral
  2. Execute: Normal bd operations work on wisp issues
  3. Squash: bd mol squash <id> (clears Ephemeral flag, promotes to persistent)
  4. Or burn: bd mol burn <id> (deletes without creating digest)

Examples:
  bd mol wisp beads-release --var version=1.0  # Release workflow
  bd mol wisp mol-my-workflow                  # Ephemeral operational cycle
  bd mol wisp list                             # List all wisps
  bd mol wisp gc                               # Garbage collect old wisps

Subcommands:
  list  List all wisps in current context
  gc    Garbage collect orphaned wisps`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWisp,
}

// WispListItem represents a wisp in list output
type WispListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Priority  int       `json:"priority"`
	Type      string    `json:"type"`
	Labels    []string  `json:"labels,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Old       bool      `json:"old,omitempty"` // Not updated in 24+ hours
}

// WispListResult is the JSON output for wisp list
type WispListResult struct {
	Wisps    []WispListItem `json:"wisps"`
	Count    int            `json:"count"`
	OldCount int            `json:"old_count,omitempty"`
}

// OldThreshold is how old a wisp must be to be flagged as old (time-based, for ephemeral cleanup)
const OldThreshold = 24 * time.Hour

func runWisp(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("wisp")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	if len(args) == 0 {
		_ = cmd.Help()
		return nil
	}
	// Delegate to the non-emitting core so `bd wisp <name>` records exactly one
	// cli_command event ("wisp"), not also "wisp-create".
	return runWispCreateCore(cmd, args)
}

// wispCreateCmd instantiates a proto as an ephemeral wisp (kept for backwards compat)
var wispCreateCmd = &cobra.Command{
	Use:   "create <proto-id>",
	Short: "Instantiate a proto as a wisp (solid -> vapor)",
	Long: `Create a wisp from a proto - sublimation from solid to vapor.

This is the chemistry-inspired command for creating ephemeral work from templates.
The resulting wisp is stored in the main database with Ephemeral=true and NOT synced via git.

Phase transition: Proto (solid) -> Wisp (vapor)

Use wisp for:
  - Operational loops and recurring cycles
  - Health checks and monitoring
  - One-shot orchestration runs
  - Routine operations with no audit value

The wisp will:
  - Be stored in main database with Ephemeral=true flag
  - NOT be synced via git
  - Either evaporate (burn) or condense to digest (squash)

Examples:
  bd mol wisp create mol-patrol                    # Ephemeral patrol cycle
  bd mol wisp create mol-health-check              # One-time health check
  bd mol wisp create mol-diagnostics --var target=db  # Diagnostic run`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWispCreate,
}

func runWispCreate(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("wisp-create")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	return runWispCreateCore(cmd, args)
}

// runWispCreateCore instantiates a proto as a wisp without emitting a metrics
// event, so the caller owns emission: the standalone `bd mol wisp create`
// entrypoint records "wisp-create", while the bare `bd wisp <name>` alias records
// "wisp". This keeps each invocation to exactly one cli_command event.
func runWispCreateCore(cmd *cobra.Command, args []string) error {
	CheckReadonly("wisp create")

	ctx := rootCtx

	if store == nil {
		return HandleErrorWithHint("no database connection", diagHint())
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	rootOnly, _ := cmd.Flags().GetBool("root-only")
	varFlags, _ := cmd.Flags().GetStringArray("var")

	vars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			return HandleError("invalid variable format '%s', expected 'key=value'", v)
		}
		vars[parts[0]] = parts[1]
	}

	// Try to load as formula first (ephemeral proto)
	// If that fails, fall back to loading from DB (legacy proto beads)
	var subgraph *TemplateSubgraph
	var protoID string

	// Try to cook formula inline (ephemeral protos)
	// This works for any valid formula name, not just "mol-" prefixed ones
	// Pass vars for step condition filtering (bd-7zka.1)
	sg, err := resolveAndCookFormulaWithVars(args[0], nil, vars)
	if err == nil {
		subgraph = sg
		protoID = sg.Root.ID
	}

	if subgraph == nil {
		// Resolve proto ID (legacy path)
		protoID = args[0]
		// Try to resolve partial ID if it doesn't look like a full ID
		if !strings.HasPrefix(protoID, "bd-") && !strings.HasPrefix(protoID, "gt-") && !strings.HasPrefix(protoID, "mol-") {
			// Might be a partial ID, try to resolve
			if resolved, err := resolvePartialIDDirect(ctx, protoID); err == nil {
				protoID = resolved
			}
		}

		if strings.HasPrefix(protoID, "mol-") {
			issues, err := store.SearchIssues(ctx, "", types.IssueFilter{
				Labels: []string{MoleculeLabel},
			})
			if err != nil {
				return HandleError("searching for proto: %v", err)
			}
			found := false
			for _, issue := range issues {
				if strings.Contains(issue.Title, protoID) || issue.ID == protoID {
					protoID = issue.ID
					found = true
					break
				}
			}
			if !found {
				return HandleErrorWithHint(fmt.Sprintf("'%s' not found as formula or proto", args[0]), "run 'bd formula list' to see available formulas")
			}
		}

		protoIssue, err := store.GetIssue(ctx, protoID)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return HandleError("proto not found: %s", protoID)
			}
			return HandleError("loading proto %s: %v", protoID, err)
		}
		if !isProtoIssue(protoIssue) {
			return HandleError("%s is not a proto (missing '%s' label)", protoID, MoleculeLabel)
		}

		subgraph, err = loadTemplateSubgraph(ctx, store, protoID)
		if err != nil {
			return HandleError("loading proto: %v", err)
		}
	}

	// Apply variable defaults from formula
	vars = applyVariableDefaults(vars, subgraph)

	// Check for missing required variables (those without defaults)
	requiredVars := extractRequiredVariables(subgraph)
	var missingVars []string
	for _, v := range requiredVars {
		if _, ok := vars[v]; !ok {
			missingVars = append(missingVars, v)
		}
	}
	if len(missingVars) > 0 {
		return HandleErrorWithHint(
			fmt.Sprintf("missing required variables: %s", strings.Join(missingVars, ", ")),
			fmt.Sprintf("Provide them with: --var %s=<value>", missingVars[0]),
		)
	}

	// By default, wisps materialize the same child DAG as pour, just marked
	// Ephemeral=true so they don't sync via git. Use --root-only to opt out
	// of fanout (e.g. for patrol wisps whose steps are inlined at prime time
	// rather than tracked as beads). GH#3872.
	if dryRun {
		if rootOnly {
			skipped := len(subgraph.Issues) - 1
			fmt.Printf("\nDry run: would create wisp with 1 issue (root only) from proto %s\n", protoID)
			if skipped > 0 {
				fmt.Printf("  Note: %d child step(s) skipped (--root-only)\n", skipped)
			}
		} else {
			fmt.Printf("\nDry run: would create wisp with %d issues from proto %s\n\n", len(subgraph.Issues), protoID)
		}
		fmt.Printf("Storage: main database (ephemeral=true, not synced via git)\n\n")
		issuesToShow := subgraph.Issues
		if rootOnly && len(issuesToShow) > 0 {
			issuesToShow = issuesToShow[:1]
		}
		for _, issue := range issuesToShow {
			newTitle := substituteVariables(issue.Title, vars)
			fmt.Printf("  - %s (from %s)\n", newTitle, issue.ID)
		}
		return nil
	}

	result, err := spawnMoleculeWithOptions(ctx, store, subgraph, CloneOptions{
		Vars:      vars,
		Actor:     actor,
		Ephemeral: true,
		Prefix:    types.IDPrefixWisp,
		RootOnly:  rootOnly,
	})
	if err != nil {
		return HandleError("creating wisp: %v", err)
	}

	if jsonOutput {
		type wispCreateResult struct {
			*InstantiateResult
			Phase string `json:"phase"`
		}
		return outputJSON(wispCreateResult{result, "vapor"})
	}

	fmt.Printf("%s Created wisp: %d issues\n", ui.RenderPass("✓"), result.Created)
	fmt.Printf("  Root issue: %s\n", result.NewEpicID)
	fmt.Printf("  Phase: vapor (ephemeral, not synced via git)\n")
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  bd close %s.<step>       # Complete steps\n", result.NewEpicID)
	fmt.Printf("  bd mol squash %s         # Condense to digest (promotes to persistent)\n", result.NewEpicID)
	fmt.Printf("  bd mol burn %s           # Discard without creating digest\n", result.NewEpicID)
	return nil
}

// isProtoIssue checks if an issue is a proto (has the template label)
func isProtoIssue(issue *types.Issue) bool {
	for _, label := range issue.Labels {
		if label == MoleculeLabel {
			return true
		}
	}
	return false
}

// resolvePartialIDDirect resolves a partial ID directly from store
func resolvePartialIDDirect(ctx context.Context, partial string) (string, error) {
	// Try direct lookup first
	if issue, err := store.GetIssue(ctx, partial); err == nil {
		return issue.ID, nil
	}
	// Search by prefix
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{
		IDs: []string{partial + "*"},
	})
	if err != nil {
		return "", err
	}
	if len(issues) == 1 {
		return issues[0].ID, nil
	}
	if len(issues) > 1 {
		return "", fmt.Errorf("ambiguous ID: %s matches %d issues", partial, len(issues))
	}
	return "", fmt.Errorf("not found: %s", partial)
}

var wispListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all wisps in current context",
	Long: `List all wisps (ephemeral molecules) in the current context.

Wisps are issues with Ephemeral=true in the main database. They are stored
locally but not synced via git.

The list shows:
  - ID: Issue ID of the wisp
  - Title: Wisp title
  - Status: Current status (open, in_progress, closed)
  - Started: When the wisp was created
  - Updated: Last modification time

Old wisp detection:
  - Old wisps haven't been updated in 24+ hours
  - Use 'bd mol wisp gc' to clean up old/abandoned wisps

Examples:
  bd mol wisp list              # List all wisps
  bd mol wisp list --json       # JSON output for programmatic use
  bd mol wisp list --all        # Include closed wisps`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWispList,
}

func runWispList(cmd *cobra.Command, args []string) error {
	evt := metrics.NewCommandEvent("wisp-list")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	showAll, _ := cmd.Flags().GetBool("all")
	typeFilter, _ := cmd.Flags().GetString("type")

	if store == nil {
		if jsonOutput {
			return outputJSON(WispListResult{
				Wisps: []WispListItem{},
				Count: 0,
			})
		}
		fmt.Println("No database connection")
		return nil
	}

	ephemeralFlag := true
	filter := types.IssueFilter{
		Ephemeral: &ephemeralFlag,
		Limit:     5000,
	}
	if typeFilter != "" {
		it := types.IssueType(typeFilter)
		filter.IssueType = &it
	}
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleError("listing wisps: %v", err)
	}

	// Filter closed issues unless --all is specified
	if !showAll {
		var filtered []*types.Issue
		for _, issue := range issues {
			if issue.Status != types.StatusClosed {
				filtered = append(filtered, issue)
			}
		}
		issues = filtered
	}

	// Convert to list items and detect old wisps
	now := time.Now()
	items := make([]WispListItem, 0, len(issues))
	oldCount := 0

	for _, issue := range issues {
		item := WispListItem{
			ID:        issue.ID,
			Title:     issue.Title,
			Status:    string(issue.Status),
			Priority:  issue.Priority,
			Type:      string(issue.IssueType),
			Labels:    issue.Labels,
			CreatedAt: issue.CreatedAt,
			UpdatedAt: issue.UpdatedAt,
		}

		// Check if old (not updated in 24+ hours)
		if now.Sub(issue.UpdatedAt) > OldThreshold {
			item.Old = true
			oldCount++
		}

		items = append(items, item)
	}

	// Sort by updated_at descending (most recent first)
	slices.SortFunc(items, func(a, b WispListItem) int {
		return b.UpdatedAt.Compare(a.UpdatedAt) // descending order
	})

	result := WispListResult{
		Wisps:    items,
		Count:    len(items),
		OldCount: oldCount,
	}

	if jsonOutput {
		return outputJSON(result)
	}

	if len(items) == 0 {
		fmt.Println("No wisps found")
		return nil
	}

	fmt.Printf("Wisps (%d):\n\n", len(items))

	// Print header
	fmt.Printf("%-12s %-10s %-4s %-10s %-46s %s\n",
		"ID", "STATUS", "PRI", "TYPE", "TITLE", "UPDATED")
	fmt.Println(strings.Repeat("-", 100))

	for _, item := range items {
		// Truncate title if too long
		title := item.Title
		if len(title) > 44 {
			title = title[:41] + "..."
		}

		// Format status with color
		status := ui.RenderStatus(item.Status)

		// Format updated time
		updated := formatTimeAgo(item.UpdatedAt)
		if item.Old {
			updated = ui.RenderWarn(updated + " ⚠")
		}

		fmt.Printf("%-12s %-10s P%-3d %-10s %-46s %s\n",
			item.ID, status, item.Priority, item.Type, title, updated)
	}

	if oldCount > 0 {
		fmt.Printf("\n%s %d old wisp(s) (not updated in 24+ hours)\n",
			ui.RenderWarn("⚠"), oldCount)
		fmt.Println("  Hint: Use 'bd mol wisp gc' to clean up old wisps")
	}
	return nil
}

// formatTimeAgo returns a human-readable relative time
func formatTimeAgo(t time.Time) string {
	d := time.Since(t)

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d mins ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("2006-01-02")
	}
}

// formatTimeUntil returns a human-readable relative time for a future instant,
// the forward-looking mirror of formatTimeAgo. Used for lease expiry in bd show.
// A past (or present) instant renders as "expired".
func formatTimeUntil(t time.Time) string {
	d := time.Until(t)
	if d <= 0 {
		return "expired"
	}
	switch {
	case d < time.Minute:
		return "in <1 min"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "in 1 min"
		}
		return fmt.Sprintf("in %d mins", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "in 1 hour"
		}
		return fmt.Sprintf("in %d hours", hours)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "in 1 day"
		}
		return fmt.Sprintf("in %d days", days)
	}
}

var wispGCCmd = &cobra.Command{
	Use:   "gc",
	Short: "Garbage collect old/abandoned wisps",
	Long: `Garbage collect old or abandoned wisps from the database.

A wisp is considered abandoned if:
  - It hasn't been updated in --age duration and is not closed

Abandoned wisps are deleted without creating a digest. Use 'bd mol squash'
if you want to preserve a summary before garbage collection.

Use --closed to purge ALL closed wisps (regardless of age). This is the
fastest way to reclaim space from accumulated wisp bloat. Safe by default:
requires --force to actually delete.

Note: This uses time-based cleanup, appropriate for ephemeral wisps.
For graph-pressure staleness detection (blocking other work), see 'bd mol stale'.

Examples:
  bd mol wisp gc                                    # Clean abandoned wisps (default: 1h threshold)
  bd mol wisp gc --dry-run                          # Preview what would be cleaned
  bd mol wisp gc --age 24h                          # Custom age threshold
  bd mol wisp gc --all                              # Also clean closed wisps older than threshold
  bd mol wisp gc --closed                           # Preview closed wisp deletion
  bd mol wisp gc --closed --force                   # Delete all closed wisps
  bd mol wisp gc --closed --dry-run                 # Explicit dry-run (same as no --force)
  bd mol wisp gc --exclude-type agent,rig           # Protect agent and rig wisps from GC
  bd mol wisp gc --closed --force --exclude-type mol # Delete closed wisps except mol type`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWispGC,
}

// WispGCResult is the JSON output for wisp gc
type WispGCResult struct {
	CleanedIDs   []string `json:"cleaned_ids"`
	CleanedCount int      `json:"cleaned_count"`
	Candidates   int      `json:"candidates,omitempty"`
	DryRun       bool     `json:"dry_run,omitempty"`
}

func runWispGC(cmd *cobra.Command, args []string) error {
	CheckReadonly("wisp gc")

	evt := metrics.NewCommandEvent("wisp-gc")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	ageStr, _ := cmd.Flags().GetString("age")
	cleanAll, _ := cmd.Flags().GetBool("all")
	closedMode, _ := cmd.Flags().GetBool("closed")
	force, _ := cmd.Flags().GetBool("force")
	excludeTypeStrs, _ := cmd.Flags().GetStringSlice("exclude-type")

	ageThreshold := time.Hour
	if ageStr != "" {
		var err error
		ageThreshold, err = time.ParseDuration(ageStr)
		if err != nil {
			return HandleError("invalid --age duration: %v", err)
		}
	}

	if store == nil {
		return HandleErrorWithHint("no database connection", diagHint())
	}

	var excludeTypes []types.IssueType
	for _, t := range excludeTypeStrs {
		excludeTypes = append(excludeTypes, types.IssueType(t))
	}

	if closedMode {
		return runWispPurgeClosed(ctx, dryRun, force, excludeTypes)
	}

	ephemeralFlag := true
	filter := types.IssueFilter{
		Ephemeral:    &ephemeralFlag,
		ExcludeTypes: excludeTypes,
		Limit:        5000,
	}
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleError("listing wisps: %v", err)
	}

	// Find old/abandoned wisps
	now := time.Now()
	var abandoned []*types.Issue
	for _, issue := range issues {
		// Never GC infrastructure beads (configured via types.infra)
		if store.IsInfraTypeCtx(ctx, issue.IssueType) {
			continue
		}

		// Skip closed issues unless --all is specified
		if issue.Status == types.StatusClosed && !cleanAll {
			continue
		}

		// Check if old (not updated within age threshold)
		if now.Sub(issue.UpdatedAt) > ageThreshold {
			abandoned = append(abandoned, issue)
		}
	}

	// Cascade: expand to include blocked step children of abandoned wisps.
	// Without this, deleting a parent formula wisp leaves its dependent step
	// wisps as permanent orphans (they have no other references keeping them alive).
	if len(abandoned) > 0 {
		parentIDs := make([]string, len(abandoned))
		for i, issue := range abandoned {
			parentIDs[i] = issue.ID
		}
		childIDs, err := store.FindWispDependentsRecursive(ctx, parentIDs)
		if err != nil {
			// Log but don't fail the GC — partial cascade is better than none
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: cascade expansion incomplete: %v\n", err)
		}
		if len(childIDs) > 0 {
			// Fetch the child wisps and add them to the abandoned set
			childIDSlice := make([]string, 0, len(childIDs))
			for id := range childIDs {
				childIDSlice = append(childIDSlice, id)
			}
			childIssues, fetchErr := store.GetIssuesByIDs(ctx, childIDSlice)
			if fetchErr == nil {
				abandonedSet := make(map[string]bool, len(abandoned))
				for _, issue := range abandoned {
					abandonedSet[issue.ID] = true
				}
				for _, child := range childIssues {
					if abandonedSet[child.ID] {
						continue
					}
					// Never cascade to infra types
					if store.IsInfraTypeCtx(ctx, child.IssueType) {
						continue
					}
					abandoned = append(abandoned, child)
				}
			}
		}
	}

	if len(abandoned) == 0 {
		if jsonOutput {
			return outputJSON(WispGCResult{
				CleanedIDs:   []string{},
				CleanedCount: 0,
				DryRun:       dryRun,
			})
		}
		fmt.Println("No abandoned wisps found")
		return nil
	}

	if dryRun {
		if jsonOutput {
			ids := make([]string, len(abandoned))
			for i, o := range abandoned {
				ids[i] = o.ID
			}
			return outputJSON(WispGCResult{
				CleanedIDs:   ids,
				Candidates:   len(abandoned),
				CleanedCount: 0,
				DryRun:       true,
			})
		}
		fmt.Printf("Dry run: would clean %d abandoned wisp(s):\n\n", len(abandoned))
		for _, issue := range abandoned {
			age := formatTimeAgo(issue.UpdatedAt)
			fmt.Printf("  %s: %s (last updated: %s)\n", issue.ID, issue.Title, age)
		}
		fmt.Printf("\nRun without --dry-run to delete these wisps.\n")
		return nil
	}

	ids := make([]string, len(abandoned))
	for i, issue := range abandoned {
		ids[i] = issue.ID
	}
	if err := deleteBatch(nil, ids, true, false, true, jsonOutput, false, "wisp gc"); err != nil {
		return HandleError("%v", err)
	}
	return nil
}

func runWispPurgeClosed(ctx context.Context, dryRun bool, force bool, excludeTypes []types.IssueType) error {
	statusClosed := types.StatusClosed
	ephemeralTrue := true
	filter := types.IssueFilter{
		Status:       &statusClosed,
		Ephemeral:    &ephemeralTrue,
		ExcludeTypes: excludeTypes,
		Limit:        5000,
	}

	closedIssues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleError("listing closed wisps: %v", err)
	}

	// Filter out pinned and infra issues (protected from cleanup)
	pinnedCount := 0
	infraCount := 0
	filtered := make([]*types.Issue, 0, len(closedIssues))
	for _, issue := range closedIssues {
		if issue.Pinned {
			pinnedCount++
			continue
		}
		if store.IsInfraTypeCtx(ctx, issue.IssueType) {
			infraCount++
			continue
		}
		filtered = append(filtered, issue)
	}
	closedIssues = filtered

	if pinnedCount > 0 && !jsonOutput {
		fmt.Printf("Skipping %d pinned issue(s) (protected from cleanup)\n", pinnedCount)
	}
	if infraCount > 0 && !jsonOutput {
		fmt.Printf("Skipping %d configured infra issue(s) protected from GC\n", infraCount)
	}

	if len(closedIssues) == 0 {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"deleted_count": 0,
				"message":       "No closed wisps to delete",
			})
		}
		fmt.Println("No closed wisps to delete")
		return nil
	}

	ids := make([]string, len(closedIssues))
	for i, issue := range closedIssues {
		ids[i] = issue.ID
	}

	if !force && !dryRun {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"candidates": len(ids),
				"dry_run":    true,
			})
		}
		fmt.Printf("Found %d closed wisp(s) to delete\n", len(ids))
		fmt.Printf("\nUse --force to proceed, or --dry-run for detailed preview.\n")
		return nil
	}

	if !jsonOutput {
		fmt.Printf("Found %d closed wisp(s)\n", len(ids))
		if dryRun {
			fmt.Println(ui.RenderWarn("DRY RUN - no changes will be made"))
		}
		fmt.Println()
	}

	if err := deleteBatch(nil, ids, force, dryRun, true, jsonOutput, false, "wisp gc --closed"); err != nil {
		return HandleError("%v", err)
	}

	if !dryRun && force && !jsonOutput {
		fmt.Printf("\nHint: Run 'bd compact --dolt' to reclaim disk space\n")
	}
	return nil
}

func init() {
	// Wisp command flags (for direct create: bd mol wisp <proto>)
	wispCmd.Flags().StringArray("var", []string{}, "Variable substitution (key=value)")
	wispCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	wispCmd.Flags().Bool("root-only", false, "Create only the root issue (no child step issues)")

	// Wisp create command flags (kept for backwards compat: bd mol wisp create <proto>)
	wispCreateCmd.Flags().StringArray("var", []string{}, "Variable substitution (key=value)")
	wispCreateCmd.Flags().Bool("dry-run", false, "Preview what would be created")
	wispCreateCmd.Flags().Bool("root-only", false, "Create only the root issue (no child step issues)")

	wispListCmd.Flags().Bool("all", false, "Include closed wisps")
	wispListCmd.Flags().String("type", "", "Filter by issue type (e.g., agent, task, patrol)")

	wispGCCmd.Flags().Bool("dry-run", false, "Preview what would be cleaned")
	wispGCCmd.Flags().String("age", "1h", "Age threshold for abandoned wisp detection")
	wispGCCmd.Flags().Bool("all", false, "Also clean closed wisps older than threshold")
	wispGCCmd.Flags().Bool("closed", false, "Delete all closed wisps (ignores --age threshold)")
	wispGCCmd.Flags().BoolP("force", "f", false, "Actually delete (default: preview only)")
	wispGCCmd.Flags().StringSlice("exclude-type", nil, "Exclude wisps of these types from GC (comma-separated, e.g., agent,rig)")

	wispCmd.AddCommand(wispCreateCmd)
	wispCmd.AddCommand(wispListCmd)
	wispCmd.AddCommand(wispGCCmd)
	molCmd.AddCommand(wispCmd)
}
