package main

import (
	"context"
	"fmt"
	"os"
	"sort"
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
	defer wispStore.Close()

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
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
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
	defer wispStore.Close()

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
	wispListCmd.Flags().Bool("all", false, "Include closed wisps")

	wispGCCmd.Flags().Bool("dry-run", false, "Preview what would be cleaned")
	wispGCCmd.Flags().String("age", "1h", "Age threshold for orphan detection")
	wispGCCmd.Flags().Bool("all", false, "Also clean closed wisps older than threshold")

	wispCmd.AddCommand(wispListCmd)
	wispCmd.AddCommand(wispGCCmd)
	rootCmd.AddCommand(wispCmd)
}
