package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// Wisp commands - manage ephemeral molecules
//
// Wisps are ephemeral issues with Ephemeral=true in the main database.
// They're used for patrol cycles and operational loops that shouldn't
// be exported to JSONL (and thus not synced via git).
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
locally but NOT exported to JSONL (and thus not synced via git).

WHEN TO USE WISP vs POUR:
  wisp (vapor): Ephemeral work that auto-cleans up
    - Release workflows (one-time execution)
    - Patrol cycles (deacon, witness, refinery)
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
  bd mol wisp mol-patrol                       # Ephemeral patrol cycle
  bd mol wisp list                             # List all wisps
  bd mol wisp gc                               # Garbage collect old wisps

Subcommands:
  list  List all wisps in current context
  gc    Garbage collect orphaned wisps`,
	Args: cobra.MaximumNArgs(1),
	Run:  runWisp,
}

// WispListItem represents a wisp in list output
type WispListItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Priority  int       `json:"priority"`
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

// runWisp handles the wisp command when called directly with a proto-id
// It delegates to runWispCreate for the actual work
func runWisp(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		// No proto-id provided, show help
		_ = cmd.Help() // Help() always returns nil for cobra commands
		return
	}
	// Delegate to the create logic
	runWispCreate(cmd, args)
}

// wispCreateCmd instantiates a proto as an ephemeral wisp (kept for backwards compat)
var wispCreateCmd = &cobra.Command{
	Use:   "create <proto-id>",
	Short: "Instantiate a proto as a wisp (solid -> vapor)",
	Long: `Create a wisp from a proto - sublimation from solid to vapor.

This is the chemistry-inspired command for creating ephemeral work from templates.
The resulting wisp is stored in the main database with Ephemeral=true and NOT exported to JSONL.

Phase transition: Proto (solid) -> Wisp (vapor)

Use wisp for:
  - Patrol cycles (deacon, witness)
  - Health checks and monitoring
  - One-shot orchestration runs
  - Routine operations with no audit value

The wisp will:
  - Be stored in main database with Ephemeral=true flag
  - NOT be exported to JSONL (and thus not synced via git)
  - Either evaporate (burn) or condense to digest (squash)

Examples:
  bd mol wisp create mol-patrol                    # Ephemeral patrol cycle
  bd mol wisp create mol-health-check              # One-time health check
  bd mol wisp create mol-diagnostics --var target=db  # Diagnostic run`,
	Args: cobra.ExactArgs(1),
	Run:  runWispCreate,
}

func runWispCreate(cmd *cobra.Command, args []string) {
	CheckReadonly("wisp create")

	ctx := rootCtx

	// Wisp create requires direct store access (daemon auto-bypassed for wisp ops)
	if store == nil {
		fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		fmt.Fprintf(os.Stderr, "Hint: run 'bd init' or 'bd import' to initialize the database\n")
		os.Exit(1)
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	varFlags, _ := cmd.Flags().GetStringArray("var")

	// Parse variables
	vars := make(map[string]string)
	for _, v := range varFlags {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: invalid variable format '%s', expected 'key=value'\n", v)
			os.Exit(1)
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

		// Check if it's a named molecule (mol-xxx) - look up in catalog
		if strings.HasPrefix(protoID, "mol-") {
			// Find the proto by name
			issues, err := store.SearchIssues(ctx, "", types.IssueFilter{
				Labels: []string{MoleculeLabel},
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error searching for proto: %v\n", err)
				os.Exit(1)
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
				fmt.Fprintf(os.Stderr, "Error: '%s' not found as formula or proto\n", args[0])
				fmt.Fprintf(os.Stderr, "Hint: run 'bd formula list' to see available formulas\n")
				os.Exit(1)
			}
		}

		// Load the proto
		// Note: GetIssue returns (nil, nil) for not-found, so check both
		protoIssue, err := store.GetIssue(ctx, protoID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading proto %s: %v\n", protoID, err)
			os.Exit(1)
		}
		if protoIssue == nil {
			fmt.Fprintf(os.Stderr, "Error: proto not found: %s\n", protoID)
			os.Exit(1)
		}
		if !isProtoIssue(protoIssue) {
			fmt.Fprintf(os.Stderr, "Error: %s is not a proto (missing '%s' label)\n", protoID, MoleculeLabel)
			os.Exit(1)
		}

		// Load the proto subgraph from DB
		subgraph, err = loadTemplateSubgraph(ctx, store, protoID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading proto: %v\n", err)
			os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "Error: missing required variables: %s\n", strings.Join(missingVars, ", "))
		fmt.Fprintf(os.Stderr, "Provide them with: --var %s=<value>\n", missingVars[0])
		os.Exit(1)
	}

	if dryRun {
		fmt.Printf("\nDry run: would create wisp with %d issues from proto %s\n\n", len(subgraph.Issues), protoID)
		fmt.Printf("Storage: main database (ephemeral=true, not exported to JSONL)\n\n")
		for _, issue := range subgraph.Issues {
			newTitle := substituteVariables(issue.Title, vars)
			fmt.Printf("  - %s (from %s)\n", newTitle, issue.ID)
		}
		return
	}

	// Spawn as ephemeral in main database (Ephemeral=true, skips JSONL export)
	// Use wisp prefix for distinct visual recognition (see types.IDPrefixWisp)
	result, err := spawnMolecule(ctx, store, subgraph, vars, "", actor, true, types.IDPrefixWisp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating wisp: %v\n", err)
		os.Exit(1)
	}

	// Wisp issues are in main db but don't trigger JSONL export (Ephemeral flag excludes them)

	if jsonOutput {
		type wispCreateResult struct {
			*InstantiateResult
			Phase string `json:"phase"`
		}
		outputJSON(wispCreateResult{result, "vapor"})
		return
	}

	fmt.Printf("%s Created wisp: %d issues\n", ui.RenderPass("✓"), result.Created)
	fmt.Printf("  Root issue: %s\n", result.NewEpicID)
	fmt.Printf("  Phase: vapor (ephemeral, not exported to JSONL)\n")
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  bd close %s.<step>       # Complete steps\n", result.NewEpicID)
	fmt.Printf("  bd mol squash %s         # Condense to digest (promotes to persistent)\n", result.NewEpicID)
	fmt.Printf("  bd mol burn %s           # Discard without creating digest\n", result.NewEpicID)
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
	// Note: GetIssue returns (nil, nil) for not-found, so check both
	if issue, err := store.GetIssue(ctx, partial); err == nil && issue != nil {
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
locally but not exported to JSONL (and thus not synced via git).

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
	Run: runWispList,
}

func runWispList(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	showAll, _ := cmd.Flags().GetBool("all")

	// Check for database connection
	if store == nil {
		if jsonOutput {
			outputJSON(WispListResult{
				Wisps: []WispListItem{},
				Count: 0,
			})
		} else {
			fmt.Println("No database connection")
		}
		return
	}

	// Query wisps from main database using Ephemeral filter
	ephemeralFlag := true
	filter := types.IssueFilter{
		Ephemeral: &ephemeralFlag,
		Limit:     5000,
	}
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing wisps: %v\n", err)
		os.Exit(1)
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
		outputJSON(result)
		return
	}

	// Human-readable output
	if len(items) == 0 {
		fmt.Println("No wisps found")
		return
	}

	fmt.Printf("Wisps (%d):\n\n", len(items))

	// Print header
	fmt.Printf("%-12s %-10s %-4s %-46s %s\n",
		"ID", "STATUS", "PRI", "TITLE", "UPDATED")
	fmt.Println(strings.Repeat("-", 90))

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

		fmt.Printf("%-12s %-10s P%-3d %-46s %s\n",
			item.ID, status, item.Priority, title, updated)
	}

	// Print warnings
	if oldCount > 0 {
		fmt.Printf("\n%s %d old wisp(s) (not updated in 24+ hours)\n",
			ui.RenderWarn("⚠"), oldCount)
		fmt.Println("  Hint: Use 'bd mol wisp gc' to clean up old wisps")
	}
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
  bd mol wisp gc                       # Clean abandoned wisps (default: 1h threshold)
  bd mol wisp gc --dry-run             # Preview what would be cleaned
  bd mol wisp gc --age 24h             # Custom age threshold
  bd mol wisp gc --all                 # Also clean closed wisps older than threshold
  bd mol wisp gc --closed              # Preview closed wisp deletion
  bd mol wisp gc --closed --force      # Delete all closed wisps
  bd mol wisp gc --closed --dry-run    # Explicit dry-run (same as no --force)`,
	Run: runWispGC,
}

// WispGCResult is the JSON output for wisp gc
type WispGCResult struct {
	CleanedIDs   []string `json:"cleaned_ids"`
	CleanedCount int      `json:"cleaned_count"`
	Candidates   int      `json:"candidates,omitempty"`
	DryRun       bool     `json:"dry_run,omitempty"`
}

func runWispGC(cmd *cobra.Command, args []string) {
	CheckReadonly("wisp gc")

	ctx := rootCtx

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	ageStr, _ := cmd.Flags().GetString("age")
	cleanAll, _ := cmd.Flags().GetBool("all")
	closedMode, _ := cmd.Flags().GetBool("closed")
	force, _ := cmd.Flags().GetBool("force")

	// Parse age threshold
	ageThreshold := time.Hour // Default 1 hour
	if ageStr != "" {
		var err error
		ageThreshold, err = time.ParseDuration(ageStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --age duration: %v\n", err)
			os.Exit(1)
		}
	}

	// Wisp gc requires direct store access for deletion (daemon auto-bypassed for wisp ops)
	if store == nil {
		fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		fmt.Fprintf(os.Stderr, "Hint: run 'bd init' or 'bd import' to initialize the database\n")
		os.Exit(1)
	}

	// --closed mode: purge all closed wisps (batch deletion)
	if closedMode {
		runWispPurgeClosed(ctx, dryRun, force)
		return
	}

	// Query wisps from main database using Ephemeral filter
	ephemeralFlag := true
	filter := types.IssueFilter{
		Ephemeral: &ephemeralFlag,
		Limit:     5000,
	}
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing wisps: %v\n", err)
		os.Exit(1)
	}

	// Find old/abandoned wisps
	now := time.Now()
	var abandoned []*types.Issue
	for _, issue := range issues {
		// Skip closed issues unless --all is specified
		if issue.Status == types.StatusClosed && !cleanAll {
			continue
		}

		// Check if old (not updated within age threshold)
		if now.Sub(issue.UpdatedAt) > ageThreshold {
			abandoned = append(abandoned, issue)
		}
	}

	if len(abandoned) == 0 {
		if jsonOutput {
			outputJSON(WispGCResult{
				CleanedIDs:   []string{},
				CleanedCount: 0,
				DryRun:       dryRun,
			})
		} else {
			fmt.Println("No abandoned wisps found")
		}
		return
	}

	if dryRun {
		if jsonOutput {
			ids := make([]string, len(abandoned))
			for i, o := range abandoned {
				ids[i] = o.ID
			}
			outputJSON(WispGCResult{
				CleanedIDs:   ids,
				Candidates:   len(abandoned),
				CleanedCount: 0,
				DryRun:       true,
			})
		} else {
			fmt.Printf("Dry run: would clean %d abandoned wisp(s):\n\n", len(abandoned))
			for _, issue := range abandoned {
				age := formatTimeAgo(issue.UpdatedAt)
				fmt.Printf("  %s: %s (last updated: %s)\n", issue.ID, issue.Title, age)
			}
			fmt.Printf("\nRun without --dry-run to delete these wisps.\n")
		}
		return
	}

	// Use batch deletion for efficiency (cascade=true, wisps reference each other)
	ids := make([]string, len(abandoned))
	for i, issue := range abandoned {
		ids[i] = issue.ID
	}
	deleteBatch(nil, ids, true, false, true, jsonOutput, false, "wisp gc")
}

// runWispPurgeClosed deletes all closed wisps using batch deletion.
// Safe by default: preview-only without --force.
func runWispPurgeClosed(ctx context.Context, dryRun bool, force bool) {
	// Query closed ephemeral issues
	statusClosed := types.StatusClosed
	ephemeralTrue := true
	filter := types.IssueFilter{
		Status:    &statusClosed,
		Ephemeral: &ephemeralTrue,
		Limit:     5000,
	}

	closedIssues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing closed wisps: %v\n", err)
		os.Exit(1)
	}

	// Filter out pinned issues (protected from cleanup)
	pinnedCount := 0
	filtered := make([]*types.Issue, 0, len(closedIssues))
	for _, issue := range closedIssues {
		if issue.Pinned {
			pinnedCount++
			continue
		}
		filtered = append(filtered, issue)
	}
	closedIssues = filtered

	if pinnedCount > 0 && !jsonOutput {
		fmt.Printf("Skipping %d pinned issue(s) (protected from cleanup)\n", pinnedCount)
	}

	if len(closedIssues) == 0 {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"deleted_count": 0,
				"message":       "No closed wisps to delete",
			})
		} else {
			fmt.Println("No closed wisps to delete")
		}
		return
	}

	// Extract IDs
	ids := make([]string, len(closedIssues))
	for i, issue := range closedIssues {
		ids[i] = issue.ID
	}

	// Preview mode (no --force and no --dry-run)
	if !force && !dryRun {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"candidates": len(ids),
				"dry_run":    true,
			})
		} else {
			fmt.Printf("Found %d closed wisp(s) to delete\n", len(ids))
			fmt.Printf("\nUse --force to proceed, or --dry-run for detailed preview.\n")
		}
		return
	}

	if !jsonOutput {
		fmt.Printf("Found %d closed wisp(s)\n", len(ids))
		if dryRun {
			fmt.Println(ui.RenderWarn("DRY RUN - no changes will be made"))
		}
		fmt.Println()
	}

	// Use batch deletion with cascade (wisps mostly reference other wisps)
	deleteBatch(nil, ids, force, dryRun, true, jsonOutput, false, "wisp gc --closed")

	if !dryRun && force && !jsonOutput {
		fmt.Printf("\nHint: Run 'bd compact --dolt' to reclaim disk space\n")
	}
}

func init() {
	// Wisp command flags (for direct create: bd mol wisp <proto>)
	wispCmd.Flags().StringArray("var", []string{}, "Variable substitution (key=value)")
	wispCmd.Flags().Bool("dry-run", false, "Preview what would be created")

	// Wisp create command flags (kept for backwards compat: bd mol wisp create <proto>)
	wispCreateCmd.Flags().StringArray("var", []string{}, "Variable substitution (key=value)")
	wispCreateCmd.Flags().Bool("dry-run", false, "Preview what would be created")

	wispListCmd.Flags().Bool("all", false, "Include closed wisps")

	wispGCCmd.Flags().Bool("dry-run", false, "Preview what would be cleaned")
	wispGCCmd.Flags().String("age", "1h", "Age threshold for abandoned wisp detection")
	wispGCCmd.Flags().Bool("all", false, "Also clean closed wisps older than threshold")
	wispGCCmd.Flags().Bool("closed", false, "Delete all closed wisps (ignores --age threshold)")
	wispGCCmd.Flags().BoolP("force", "f", false, "Actually delete (default: preview only)")

	wispCmd.AddCommand(wispCreateCmd)
	wispCmd.AddCommand(wispListCmd)
	wispCmd.AddCommand(wispGCCmd)
	molCmd.AddCommand(wispCmd)
}
