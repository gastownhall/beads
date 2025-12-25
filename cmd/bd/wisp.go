package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// Wisp commands - manage ephemeral molecules
//
// Wisps are ephemeral issues with Wisp=true in the main database.
// They're used for patrol cycles and operational loops that shouldn't
// be exported to JSONL (and thus not synced via git).
//
// Commands:
//   bd wisp list    - List all wisps in current context
//   bd wisp gc      - Garbage collect orphaned wisps

var wispCmd = &cobra.Command{
	Use:   "wisp",
	Short: "Manage ephemeral molecules (wisps)",
	Long: `Manage wisps - ephemeral molecules for operational workflows.

Wisps are issues with Wisp=true in the main database. They're stored
locally but NOT exported to JSONL (and thus not synced via git).
They're used for patrol cycles, operational loops, and other workflows
that shouldn't accumulate in the shared issue database.

The wisp lifecycle:
  1. Create: bd wisp create <proto> or bd create --wisp
  2. Execute: Normal bd operations work on wisps
  3. Squash: bd mol squash <id> (clears Wisp flag, promotes to persistent)
  4. Or burn: bd mol burn <id> (deletes wisp without creating digest)

Commands:
  list  List all wisps in current context
  gc    Garbage collect orphaned wisps`,
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

// wispCreateCmd instantiates a proto as an ephemeral wisp
var wispCreateCmd = &cobra.Command{
	Use:   "create <proto-id>",
	Short: "Instantiate a proto as an ephemeral wisp (solid -> vapor)",
	Long: `Create a wisp from a proto - sublimation from solid to vapor.

This is the chemistry-inspired command for creating ephemeral work from templates.
The resulting wisp is stored in the main database with Wisp=true and NOT exported to JSONL.

Phase transition: Proto (solid) -> Wisp (vapor)

Use wisp create for:
  - Patrol cycles (deacon, witness)
  - Health checks and monitoring
  - One-shot orchestration runs
  - Routine operations with no audit value

The wisp will:
  - Be stored in main database with Wisp=true flag
  - NOT be exported to JSONL (and thus not synced via git)
  - Either evaporate (burn) or condense to digest (squash)

Examples:
  bd wisp create mol-patrol                    # Ephemeral patrol cycle
  bd wisp create mol-health-check              # One-time health check
  bd wisp create mol-diagnostics --var target=db  # Diagnostic run`,
	Args: cobra.ExactArgs(1),
	Run:  runWispCreate,
}

func runWispCreate(cmd *cobra.Command, args []string) {
	CheckReadonly("wisp create")

	ctx := rootCtx

	// Wisp create requires direct store access
	if store == nil {
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: wisp create requires direct database access\n")
			fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon wisp create %s ...\n", args[0])
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		}
		os.Exit(1)
	}

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	varFlags, _ := cmd.Flags().GetStringSlice("var")

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

	// Resolve proto ID
	protoID := args[0]
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
			fmt.Fprintf(os.Stderr, "Error: proto '%s' not found in catalog\n", args[0])
			fmt.Fprintf(os.Stderr, "Hint: run 'bd mol catalog' to see available protos\n")
			os.Exit(1)
		}
	}

	// Load the proto
	protoIssue, err := store.GetIssue(ctx, protoID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading proto %s: %v\n", protoID, err)
		os.Exit(1)
	}
	if !isProtoIssue(protoIssue) {
		fmt.Fprintf(os.Stderr, "Error: %s is not a proto (missing '%s' label)\n", protoID, MoleculeLabel)
		os.Exit(1)
	}

	// Load the proto subgraph
	subgraph, err := loadTemplateSubgraph(ctx, store, protoID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading proto: %v\n", err)
		os.Exit(1)
	}

	// Check for missing variables
	requiredVars := extractAllVariables(subgraph)
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
		fmt.Printf("Storage: main database (wisp=true, not exported to JSONL)\n\n")
		for _, issue := range subgraph.Issues {
			newTitle := substituteVariables(issue.Title, vars)
			fmt.Printf("  - %s (from %s)\n", newTitle, issue.ID)
		}
		return
	}

	// Spawn as wisp in main database (ephemeral=true sets Wisp flag, skips JSONL export)
	// bd-hobo: Use "wisp" prefix for distinct visual recognition
	result, err := spawnMolecule(ctx, store, subgraph, vars, "", actor, true, "wisp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating wisp: %v\n", err)
		os.Exit(1)
	}

	// Wisps are in main db but don't trigger JSONL export (Wisp flag excludes them)

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
	Long: `List all ephemeral molecules (wisps) in the current context.

Wisps are issues with Wisp=true in the main database. They are stored
locally but not exported to JSONL (and thus not synced via git).

The list shows:
  - ID: Issue ID of the wisp
  - Title: Wisp title
  - Status: Current status (open, in_progress, closed)
  - Started: When the wisp was created
  - Updated: Last modification time

Old wisp detection:
  - Old wisps haven't been updated in 24+ hours
  - Use 'bd wisp gc' to clean up old/abandoned wisps

Examples:
  bd wisp list              # List all wisps
  bd wisp list --json       # JSON output for programmatic use
  bd wisp list --all        # Include closed wisps`,
	Run: runWispList,
}

func runWispList(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	showAll, _ := cmd.Flags().GetBool("all")

	// Check for database connection
	if store == nil && daemonClient == nil {
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

	// Query wisps from main database using Wisp filter
	wispFlag := true
	var issues []*types.Issue
	var err error

	if daemonClient != nil {
		// Use daemon RPC
		resp, rpcErr := daemonClient.List(&rpc.ListArgs{
			Wisp: &wispFlag,
		})
		if rpcErr != nil {
			err = rpcErr
		} else {
			if jsonErr := json.Unmarshal(resp.Data, &issues); jsonErr != nil {
				err = jsonErr
			}
		}
	} else {
		// Direct database access
		filter := types.IssueFilter{
			Wisp: &wispFlag,
		}
		issues, err = store.SearchIssues(ctx, "", filter)
	}
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
		fmt.Println("  Hint: Use 'bd wisp gc' to clean up old wisps")
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

Note: This uses time-based cleanup, appropriate for ephemeral wisps.
For graph-pressure staleness detection (blocking other work), see 'bd mol stale'.

Examples:
  bd wisp gc                # Clean abandoned wisps (default: 1h threshold)
  bd wisp gc --dry-run      # Preview what would be cleaned
  bd wisp gc --age 24h      # Custom age threshold
  bd wisp gc --all          # Also clean closed wisps older than threshold`,
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

	// Wisp gc requires direct store access for deletion
	if store == nil {
		if daemonClient != nil {
			fmt.Fprintf(os.Stderr, "Error: wisp gc requires direct database access\n")
			fmt.Fprintf(os.Stderr, "Hint: use --no-daemon flag: bd --no-daemon wisp gc\n")
		} else {
			fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		}
		os.Exit(1)
	}

	// Query wisps from main database using Wisp filter
	wispFlag := true
	filter := types.IssueFilter{
		Wisp: &wispFlag,
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

	// Delete abandoned wisps
	var cleanedIDs []string
	sqliteStore, ok := store.(*sqlite.SQLiteStorage)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: wisp gc requires SQLite storage backend\n")
		os.Exit(1)
	}

	for _, issue := range abandoned {
		if err := sqliteStore.DeleteIssue(ctx, issue.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to delete %s: %v\n", issue.ID, err)
			continue
		}
		cleanedIDs = append(cleanedIDs, issue.ID)
	}

	result := WispGCResult{
		CleanedIDs:   cleanedIDs,
		CleanedCount: len(cleanedIDs),
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Cleaned %d abandoned wisp(s)\n", ui.RenderPass("✓"), result.CleanedCount)
	for _, id := range cleanedIDs {
		fmt.Printf("  - %s\n", id)
	}
}

func init() {
	// Wisp create command flags
	wispCreateCmd.Flags().StringSlice("var", []string{}, "Variable substitution (key=value)")
	wispCreateCmd.Flags().Bool("dry-run", false, "Preview what would be created")

	wispListCmd.Flags().Bool("all", false, "Include closed wisps")

	wispGCCmd.Flags().Bool("dry-run", false, "Preview what would be cleaned")
	wispGCCmd.Flags().String("age", "1h", "Age threshold for abandoned wisp detection")
	wispGCCmd.Flags().Bool("all", false, "Also clean closed wisps older than threshold")

	wispCmd.AddCommand(wispCreateCmd)
	wispCmd.AddCommand(wispListCmd)
	wispCmd.AddCommand(wispGCCmd)
	rootCmd.AddCommand(wispCmd)
}
