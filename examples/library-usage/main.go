// Package main demonstrates using Beads as a Go library
//
// This example shows how an external project (like VC) can import and use
// Beads programmatically instead of spawning CLI processes.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/steveyegge/beads/internal/beads"
)

func main() {
	ctx := context.Background()

	// Find the Beads database (looks for .beads/*.db in current/parent dirs)
	dbPath := beads.FindDatabasePath()
	if dbPath == "" {
		log.Fatal("No Beads database found. Run 'bd init' first.")
	}

	fmt.Printf("Using database: %s\n\n", dbPath)

	// Open the database
	store, err := beads.NewSQLiteStorage(dbPath)
	if err != nil {
		log.Fatalf("Failed to open storage: %v", err)
	}
	defer store.Close()

	// Example 1: Get ready work
	fmt.Println("=== Ready Work ===")
	ready, err := store.GetReadyWork(ctx, beads.WorkFilter{
		Status: beads.StatusOpen,
		Limit:  5,
	})
	if err != nil {
		log.Fatalf("Failed to get ready work: %v", err)
	}

	for _, issue := range ready {
		fmt.Printf("- %s: %s (priority %d)\n", issue.ID, issue.Title, issue.Priority)
	}

	// Example 2: Create an issue
	fmt.Println("\n=== Creating Issue ===")
	newIssue := &beads.Issue{
		ID:          "", // Empty = auto-generate
		Title:       "Example library-created issue",
		Description: "This issue was created programmatically using Beads as a library",
		Status:      beads.StatusOpen,
		Priority:    2,
		IssueType:   beads.TypeTask,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := store.CreateIssue(ctx, newIssue, "library-example"); err != nil {
		log.Fatalf("Failed to create issue: %v", err)
	}

	fmt.Printf("Created issue: %s\n", newIssue.ID)

	// Example 3: Add a dependency
	fmt.Println("\n=== Adding Dependency ===")
	dep := &beads.Dependency{
		IssueID:     newIssue.ID,
		DependsOnID: "bd-1", // Assumes bd-1 exists
		Type:        beads.DepDiscoveredFrom,
		CreatedAt:   time.Now(),
		CreatedBy:   "library-example",
	}

	if err := store.AddDependency(ctx, dep, "library-example"); err != nil {
		// Don't fail if bd-1 doesn't exist
		fmt.Printf("Note: Could not add dependency (bd-1 may not exist): %v\n", err)
	} else {
		fmt.Printf("Added dependency: %s discovered-from bd-1\n", newIssue.ID)
	}

	// Example 4: Add a label
	fmt.Println("\n=== Adding Label ===")
	if err := store.AddLabel(ctx, newIssue.ID, "library-usage", "library-example"); err != nil {
		log.Fatalf("Failed to add label: %v", err)
	}
	fmt.Printf("Added label 'library-usage' to %s\n", newIssue.ID)

	// Example 5: Add a comment
	fmt.Println("\n=== Adding Comment ===")
	comment, err := store.AddIssueComment(ctx, newIssue.ID, "library-example", "This is a programmatic comment")
	if err != nil {
		log.Fatalf("Failed to add comment: %v", err)
	}
	fmt.Printf("Added comment #%d\n", comment.ID)

	// Example 6: Update issue status
	fmt.Println("\n=== Updating Status ===")
	updates := map[string]interface{}{
		"status": beads.StatusInProgress,
	}
	if err := store.UpdateIssue(ctx, newIssue.ID, updates, "library-example"); err != nil {
		log.Fatalf("Failed to update issue: %v", err)
	}
	fmt.Printf("Updated %s status to in_progress\n", newIssue.ID)

	// Example 7: Get statistics
	fmt.Println("\n=== Statistics ===")
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		log.Fatalf("Failed to get statistics: %v", err)
	}

	fmt.Printf("Total issues: %d\n", stats.TotalIssues)
	fmt.Printf("Open: %d | In Progress: %d | Closed: %d | Blocked: %d | Ready: %d\n",
		stats.OpenIssues, stats.InProgressIssues, stats.ClosedIssues,
		stats.BlockedIssues, stats.ReadyIssues)

	// Example 8: Close the issue
	fmt.Println("\n=== Closing Issue ===")
	if err := store.CloseIssue(ctx, newIssue.ID, "Completed demo", "library-example"); err != nil {
		log.Fatalf("Failed to close issue: %v", err)
	}
	fmt.Printf("Closed issue %s\n", newIssue.ID)

	fmt.Println("\n✅ Library usage demo complete!")
}
