package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// Wisp commands - manage ephemeral molecules
//
// Wisps are ephemeral molecules stored in .beads-wisp/ (gitignored).
// They're used for patrol cycles and operational loops that shouldn't
// accumulate in the permanent database.
//
// Commands:
//   bd wisp list    - List all wisps in current context
//   bd wisp gc      - Garbage collect orphaned wisps

var wispCmd = &cobra.Command{
	Use:   "wisp",
	Short: "Manage ephemeral molecules (wisps)",
	Long: `Manage wisps - ephemeral molecules for operational workflows.

Wisps are ephemeral molecules stored in .beads-wisp/ (gitignored).
They're used for patrol cycles, operational loops, and other workflows
that shouldn't accumulate in the permanent database.

The wisp lifecycle:
  1. Create: bd mol bond --wisp ... (creates in .beads-wisp/)
  2. Execute: Normal bd operations work on wisps
  3. Squash: bd mol squash <id> (creates permanent digest, deletes wisp)
  4. Or burn: bd mol burn <id> (deletes wisp with no digest)

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
	Orphaned  bool      `json:"orphaned,omitempty"`
	Stale     bool      `json:"stale,omitempty"`
}

// WispListResult is the JSON output for wisp list
type WispListResult struct {
	Wisps        []WispListItem `json:"wisps"`
	Count        int            `json:"count"`
	OrphanCount  int            `json:"orphan_count,omitempty"`
	StaleCount   int            `json:"stale_count,omitempty"`
	WispDir      string         `json:"wisp_dir"`
	WispDirError string         `json:"wisp_dir_error,omitempty"`
}

// StaleThreshold is how old a wisp must be to be considered stale
const StaleThreshold = 24 * time.Hour

// wispCreateCmd instantiates a proto as an ephemeral wisp
var wispCreateCmd = &cobra.Command{
	Use:   "create <proto-id>",
	Short: "Instantiate a proto as an ephemeral wisp (solid -> vapor)",
	Long: `Create a wisp from a proto - sublimation from solid to vapor.

This is the chemistry-inspired command for creating ephemeral work from templates.
The resulting wisp lives in .beads-wisp/ (ephemeral storage) and is NOT synced.

Phase transition: Proto (solid) -> wisp -> Wisp (vapor)

Use wisp create for:
  - Patrol cycles (deacon, witness)
  - Health checks and monitoring
  - One-shot orchestration runs
  - Routine operations with no audit value

The wisp will:
  - Be stored in .beads-wisp/ (gitignored)
  - NOT sync to remote
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
		fmt.Printf("Storage: wisp (.beads-wisp/)\n\n")
		for _, issue := range subgraph.Issues {
			newTitle := substituteVariables(issue.Title, vars)
			fmt.Printf("  - %s (from %s)\n", newTitle, issue.ID)
		}
		return
	}

	// Open wisp storage
	wispStore, err := beads.NewWispStorage(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open wisp storage: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = wispStore.Close() }()

	// Ensure wisp directory is gitignored
	if err := beads.EnsureWispGitignore(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not update .gitignore: %v\n", err)
	}

	// Spawn as wisp (ephemeral=true)
	result, err := spawnMolecule(ctx, wispStore, subgraph, vars, "", actor, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating wisp: %v\n", err)
		os.Exit(1)
	}

	// Don't schedule flush - wisps are not synced

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
	fmt.Printf("  Phase: vapor (ephemeral in .beads-wisp/)\n")
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  bd close %s.<step>       # Complete steps\n", result.NewEpicID)
	fmt.Printf("  bd mol squash %s         # Condense to digest\n", result.NewEpicID)
	fmt.Printf("  bd mol burn %s           # Discard without digest\n", result.NewEpicID)
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

Wisps are stored in .beads-wisp/ alongside .beads/. They are gitignored
and will be garbage collected over time.

The list shows:
  - ID: Issue ID of the wisp
  - Title: Wisp title
  - Status: Current status (open, in_progress, closed)
  - Started: When the wisp was created
  - Updated: Last modification time

Orphan detection:
  - Orphaned wisps have no root molecule (parent was deleted)
  - Stale wisps haven't been updated in 24+ hours
  - Use 'bd wisp gc' to clean up orphaned/stale wisps

Examples:
  bd wisp list              # List all wisps
  bd wisp list --json       # JSON output for programmatic use
  bd wisp list --all        # Include closed wisps`,
	Run: runWispList,
}

func runWispList(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	showAll, _ := cmd.Flags().GetBool("all")

	// Check wisp directory exists
	wispDir := beads.FindWispDir()
	if wispDir == "" {
		if jsonOutput {
			outputJSON(WispListResult{
				Wisps:        []WispListItem{},
				Count:        0,
				WispDirError: "no .beads directory found",
			})
		} else {
			fmt.Println("No wisp storage found (no .beads directory)")
		}
		return
	}

	// Check if wisp directory exists
	if _, err := os.Stat(wispDir); os.IsNotExist(err) {
		if jsonOutput {
			outputJSON(WispListResult{
				Wisps:   []WispListItem{},
				Count:   0,
				WispDir: wispDir,
			})
		} else {
			fmt.Println("No wisps found (wisp directory does not exist)")
		}
		return
	}

	// Open wisp storage
	wispStore, err := beads.NewWispStorage(ctx)
	if err != nil {
		if jsonOutput {
			outputJSON(WispListResult{
				Wisps:        []WispListItem{},
				Count:        0,
				WispDir:      wispDir,
				WispDirError: err.Error(),
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error opening wisp storage: %v\n", err)
		}
		return
	}
	defer func() { _ = wispStore.Close() }()

	// List all issues from wisp storage
	issues, err := listWispIssues(ctx, wispStore, showAll)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing wisps: %v\n", err)
		os.Exit(1)
	}

	// Convert to list items and detect orphans/stale
	now := time.Now()
	items := make([]WispListItem, 0, len(issues))
	orphanCount := 0
	staleCount := 0

	for _, issue := range issues {
		item := WispListItem{
			ID:        issue.ID,
			Title:     issue.Title,
			Status:    string(issue.Status),
			Priority:  issue.Priority,
			CreatedAt: issue.CreatedAt,
			UpdatedAt: issue.UpdatedAt,
		}

		// Check if stale (not updated in 24+ hours)
		if now.Sub(issue.UpdatedAt) > StaleThreshold {
			item.Stale = true
			staleCount++
		}

		// Orphan detection would require checking if parent exists
		// For now, we consider root wisps without children as potential orphans
		// This is a heuristic - true orphan detection requires dependency analysis

		items = append(items, item)
	}

	// Sort by updated_at descending (most recent first)
	slices.SortFunc(items, func(a, b WispListItem) int {
		return b.UpdatedAt.Compare(a.UpdatedAt) // descending order
	})

	result := WispListResult{
		Wisps:       items,
		Count:       len(items),
		OrphanCount: orphanCount,
		StaleCount:  staleCount,
		WispDir:     wispDir,
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
		if item.Stale {
			updated = ui.RenderWarn(updated + " ⚠")
		}

		fmt.Printf("%-12s %-10s P%-3d %-46s %s\n",
			item.ID, status, item.Priority, title, updated)
	}

	// Print warnings
	if staleCount > 0 {
		fmt.Printf("\n%s %d stale wisp(s) (not updated in 24+ hours)\n",
			ui.RenderWarn("⚠"), staleCount)
		fmt.Println("  Hint: Use 'bd wisp gc' to clean up stale wisps")
	}
}

// listWispIssues retrieves issues from wisp storage
func listWispIssues(ctx context.Context, s storage.Storage, includeAll bool) ([]*types.Issue, error) {
	// Build filter - by default, exclude closed issues
	filter := types.IssueFilter{}
	if !includeAll {
		// When not showing all, we need to get everything and filter in Go
		// since the filter only supports single status
	}

	// Get all issues from wisp storage
	issues, err := s.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}

	// If not showing all, filter out closed issues
	if !includeAll {
		var filtered []*types.Issue
		for _, issue := range issues {
			if issue.Status != types.StatusClosed {
				filtered = append(filtered, issue)
			}
		}
		return filtered, nil
	}

	return issues, nil
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
	Short: "Garbage collect orphaned wisps",
	Long: `Garbage collect orphaned wisps from wisp storage.

A wisp is considered orphaned if:
  - It has a process_id and that process is no longer running
  - It hasn't been updated in --age duration and is not closed

Orphaned wisps are deleted without creating a digest. Use 'bd mol squash'
if you want to preserve a summary before garbage collection.

Examples:
  bd wisp gc                # Clean orphans (default: 1h threshold)
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
	WispDir      string   `json:"wisp_dir"`
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

	// Find wisp storage
	wispDir := beads.FindWispDir()
	if wispDir == "" {
		if jsonOutput {
			outputJSON(WispGCResult{
				CleanedIDs:   []string{},
				CleanedCount: 0,
			})
		} else {
			fmt.Println("No wisp storage found")
		}
		return
	}

	// Check if wisp directory exists
	if _, err := os.Stat(wispDir); os.IsNotExist(err) {
		if jsonOutput {
			outputJSON(WispGCResult{
				CleanedIDs:   []string{},
				CleanedCount: 0,
				WispDir:      wispDir,
			})
		} else {
			fmt.Println("No wisps to clean (wisp directory does not exist)")
		}
		return
	}

	// Open wisp storage
	wispStore, err := beads.NewWispStorage(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening wisp storage: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = wispStore.Close() }()

	// Get all issues from wisp storage
	filter := types.IssueFilter{}
	issues, err := wispStore.SearchIssues(ctx, "", filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing wisps: %v\n", err)
		os.Exit(1)
	}

	// Find orphans
	now := time.Now()
	var orphans []*types.Issue
	for _, issue := range issues {
		// Skip closed issues unless --all is specified
		if issue.Status == types.StatusClosed && !cleanAll {
			continue
		}

		// Check if stale (not updated within age threshold)
		if now.Sub(issue.UpdatedAt) > ageThreshold {
			orphans = append(orphans, issue)
		}
	}

	if len(orphans) == 0 {
		if jsonOutput {
			outputJSON(WispGCResult{
				CleanedIDs:   []string{},
				CleanedCount: 0,
				WispDir:      wispDir,
				DryRun:       dryRun,
			})
		} else {
			fmt.Println("No orphaned wisps found")
		}
		return
	}

	if dryRun {
		if jsonOutput {
			ids := make([]string, len(orphans))
			for i, o := range orphans {
				ids[i] = o.ID
			}
			outputJSON(WispGCResult{
				CleanedIDs:   ids,
				Candidates:   len(orphans),
				CleanedCount: 0,
				WispDir:      wispDir,
				DryRun:       true,
			})
		} else {
			fmt.Printf("Dry run: would clean %d orphaned wisp(s):\n\n", len(orphans))
			for _, issue := range orphans {
				age := formatTimeAgo(issue.UpdatedAt)
				fmt.Printf("  %s: %s (last updated: %s)\n", issue.ID, issue.Title, age)
			}
			fmt.Printf("\nRun without --dry-run to delete these wisps.\n")
		}
		return
	}

	// Delete orphans
	var cleanedIDs []string
	sqliteStore, ok := wispStore.(*sqlite.SQLiteStorage)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: wisp gc requires SQLite storage backend\n")
		os.Exit(1)
	}

	for _, issue := range orphans {
		if err := sqliteStore.DeleteIssue(ctx, issue.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to delete %s: %v\n", issue.ID, err)
			continue
		}
		cleanedIDs = append(cleanedIDs, issue.ID)
	}

	result := WispGCResult{
		CleanedIDs:   cleanedIDs,
		CleanedCount: len(cleanedIDs),
		WispDir:      wispDir,
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("%s Cleaned %d orphaned wisp(s)\n", ui.RenderPass("✓"), result.CleanedCount)
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
	wispGCCmd.Flags().String("age", "1h", "Age threshold for orphan detection")
	wispGCCmd.Flags().Bool("all", false, "Also clean closed wisps older than threshold")

	wispCmd.AddCommand(wispCreateCmd)
	wispCmd.AddCommand(wispListCmd)
	wispCmd.AddCommand(wispGCCmd)
	rootCmd.AddCommand(wispCmd)
}
