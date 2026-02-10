package main

import (
	"context"
	"fmt"
	"regexp"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var renameCmd = &cobra.Command{
	Use:   "rename <old-id> <new-id>",
	Short: "Rename an issue ID",
	Long: `Rename an issue from one ID to another.

This updates:
- The issue's primary ID
- All references in other issues (descriptions, titles, notes, etc.)
- Dependencies pointing to/from this issue
- Labels, comments, and events

Examples:
  bd rename bd-w382l bd-dolt     # Rename to memorable ID
  bd rename gt-abc123 gt-auth    # Use descriptive ID

Note: The new ID must use a valid prefix for this database.`,
	Args: cobra.ExactArgs(2),
	RunE: runRename,
}

func init() {
	rootCmd.AddCommand(renameCmd)
}

func runRename(cmd *cobra.Command, args []string) error {
	oldID := args[0]
	newID := args[1]

	// Validate IDs
	if oldID == newID {
		return fmt.Errorf("old and new IDs are the same")
	}

	// Basic ID format validation
	idPattern := regexp.MustCompile(`^[a-z]+-[a-zA-Z0-9._-]+$`)
	if !idPattern.MatchString(newID) {
		return fmt.Errorf("invalid new ID format %q: must be prefix-suffix (e.g., bd-dolt)", newID)
	}

	ctx := context.Background()
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("failed to get storage: %w", err)
	}

	// Check if old issue exists
	oldIssue, err := store.GetIssue(ctx, oldID)
	if err != nil {
		return fmt.Errorf("failed to get issue %s: %w", oldID, err)
	}
	if oldIssue == nil {
		return fmt.Errorf("issue %s not found", oldID)
	}

	// Check if new ID already exists
	existing, err := store.GetIssue(ctx, newID)
	if err != nil {
		return fmt.Errorf("failed to check for existing issue: %w", err)
	}
	if existing != nil {
		return fmt.Errorf("issue %s already exists", newID)
	}

	// Update the issue ID
	oldIssue.ID = newID
	actor := getActorWithGit()
	if err := store.UpdateIssueID(ctx, oldID, newID, oldIssue, actor); err != nil {
		return fmt.Errorf("failed to rename issue: %w", err)
	}

	// Update references in other issues
	if err := updateReferencesInAllIssues(ctx, store, oldID, newID, actor); err != nil {
		// Non-fatal - the primary rename succeeded
		fmt.Printf("Warning: failed to update some references: %v\n", err)
	}

	fmt.Printf("Renamed %s -> %s\n", ui.RenderWarn(oldID), ui.RenderAccent(newID))

	return nil
}

// updateReferencesInAllIssues updates text references to the old ID in all issues
func updateReferencesInAllIssues(ctx context.Context, store storage.Storage, oldID, newID, actor string) error {
	// Get all issues
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to list issues: %w", err)
	}

	// Pattern to match the old ID as a word boundary
	oldPattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(oldID) + `\b`)

	for _, issue := range issues {
		if issue.ID == newID {
			continue // Skip the renamed issue itself
		}

		updated := false
		updates := make(map[string]interface{})

		// Check and update each text field
		if oldPattern.MatchString(issue.Title) {
			updates["title"] = oldPattern.ReplaceAllString(issue.Title, newID)
			updated = true
		}
		if oldPattern.MatchString(issue.Description) {
			updates["description"] = oldPattern.ReplaceAllString(issue.Description, newID)
			updated = true
		}
		if oldPattern.MatchString(issue.Design) {
			updates["design"] = oldPattern.ReplaceAllString(issue.Design, newID)
			updated = true
		}
		if oldPattern.MatchString(issue.Notes) {
			updates["notes"] = oldPattern.ReplaceAllString(issue.Notes, newID)
			updated = true
		}
		if oldPattern.MatchString(issue.AcceptanceCriteria) {
			updates["acceptance_criteria"] = oldPattern.ReplaceAllString(issue.AcceptanceCriteria, newID)
			updated = true
		}

		if updated {
			if err := store.UpdateIssue(ctx, issue.ID, updates, actor); err != nil {
				return fmt.Errorf("failed to update references in %s: %w", issue.ID, err)
			}
		}
	}

	return nil
}
