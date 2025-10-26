package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

var renumberCmd = &cobra.Command{
	Use:   "renumber",
	Short: "Renumber all issues to compact the ID space",
	Long: `Renumber all issues sequentially to eliminate gaps in the ID space.

This command will:
- Renumber all issues starting from 1 (keeping chronological order)
- Update all dependency links (blocks, related, parent-child, discovered-from)
- Update all text references in descriptions, notes, acceptance criteria
- Show a mapping report of old ID -> new ID
- Export the updated database to JSONL

Example:
  bd renumber --dry-run    # Preview changes
  bd renumber --force      # Actually renumber

Risks:
- May break external references in GitHub issues, docs, commits
- Git history may become confusing
- Operation cannot be undone (backup recommended)`,
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		force, _ := cmd.Flags().GetBool("force")

		if !dryRun && !force {
			fmt.Fprintf(os.Stderr, "Error: must specify --dry-run or --force\n")
			os.Exit(1)
		}

		// Renumber command needs direct access to storage
		// Ensure we have a direct store connection
		if store == nil {
			var err error
			if dbPath == "" {
				fmt.Fprintf(os.Stderr, "Error: no database path found\n")
				os.Exit(1)
			}
			store, err = sqlite.New(dbPath)
			if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
			os.Exit(1)
			}
			defer func() { _ = store.Close() }()
			}

			ctx := context.Background()

		// Get prefix from config, or derive from first issue if not set
		prefix, err := store.GetConfig(ctx, "issue_prefix")
		if err != nil || prefix == "" {
			// Get any issue to derive prefix
			issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil || len(issues) == 0 {
				fmt.Fprintf(os.Stderr, "Error: failed to determine issue prefix\n")
				os.Exit(1)
			}
			// Extract prefix from first issue (e.g., "bd-123" -> "bd")
			parts := strings.Split(issues[0].ID, "-")
			if len(parts) < 2 {
				fmt.Fprintf(os.Stderr, "Error: invalid issue ID format: %s\n", issues[0].ID)
				os.Exit(1)
			}
			prefix = parts[0]
		}

		// Get all issues sorted by creation time
		issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to list issues: %v\n", err)
			os.Exit(1)
		}

		if len(issues) == 0 {
			fmt.Println("No issues to renumber")
			return
		}

		// Sort by creation time to preserve chronological order
		sort.Slice(issues, func(i, j int) bool {
			return issues[i].CreatedAt.Before(issues[j].CreatedAt)
		})

		// Build mapping from old ID to new ID
		idMapping := make(map[string]string)
		for i, issue := range issues {
			newNum := i + 1
			newID := fmt.Sprintf("%s-%d", prefix, newNum)
			idMapping[issue.ID] = newID
		}

		if dryRun {
			cyan := color.New(color.FgCyan).SprintFunc()
			fmt.Printf("DRY RUN: Would renumber %d issues\n\n", len(issues))
			fmt.Printf("Sample changes:\n")
			changesShown := 0
			for _, issue := range issues {
				oldID := issue.ID
				newID := idMapping[oldID]
				if oldID != newID {
					fmt.Printf("  %s -> %s (%s)\n", cyan(oldID), cyan(newID), issue.Title)
					changesShown++
					if changesShown >= 10 {
						skipped := 0
						for _, iss := range issues {
							if iss.ID != idMapping[iss.ID] {
								skipped++
							}
						}
						skipped -= changesShown
						if skipped > 0 {
							fmt.Printf("... and %d more changes\n", skipped)
						}
						break
					}
				}
			}
			return
		}

		green := color.New(color.FgGreen).SprintFunc()

		fmt.Printf("Renumbering %d issues...\n", len(issues))

		if err := renumberIssuesInDB(ctx, prefix, idMapping, issues); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to renumber issues: %v\n", err)
			os.Exit(1)
		}

		// Schedule full export (IDs changed, incremental won't work)
		markDirtyAndScheduleFullExport()

		fmt.Printf("%s Successfully renumbered %d issues\n", green("✓"), len(issues))

		// Count actual changes
		changed := 0
		for oldID, newID := range idMapping {
			if oldID != newID {
				changed++
			}
		}
		fmt.Printf("  %d issues renumbered, %d unchanged\n", changed, len(issues)-changed)

		if jsonOutput {
			result := map[string]interface{}{
				"total_issues": len(issues),
				"changed":      changed,
				"unchanged":    len(issues) - changed,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(result)
		}
	},
}

func renumberIssuesInDB(ctx context.Context, prefix string, idMapping map[string]string, issues []*types.Issue) error {
	// Step 0: Get all dependencies BEFORE renaming (while IDs still match)
	allDepsByIssue, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dependency records: %w", err)
	}

	// Step 1: Rename all issues to temporary UUIDs to avoid collisions
	tempMapping := make(map[string]string)

	for _, issue := range issues {
		oldID := issue.ID
		// Use UUID to guarantee uniqueness (no collision possible)
		tempID := fmt.Sprintf("temp-%s", uuid.New().String())
		tempMapping[oldID] = tempID

		// Rename to temp ID (don't update text yet)
		issue.ID = tempID
		if err := store.UpdateIssueID(ctx, oldID, tempID, issue, actor); err != nil {
			return fmt.Errorf("failed to rename %s to temp ID: %w", oldID, err)
		}
	}

	// Step 2: Rename from temp IDs to final IDs (still don't update text)
	for _, issue := range issues {
		tempID := issue.ID // Currently has temp ID

		// Find original ID
		var oldOriginalID string
		for origID, tID := range tempMapping {
			if tID == tempID {
				oldOriginalID = origID
				break
			}
		}
		finalID := idMapping[oldOriginalID]

		// Just update the ID, not text yet
		issue.ID = finalID
		if err := store.UpdateIssueID(ctx, tempID, finalID, issue, actor); err != nil {
			return fmt.Errorf("failed to update issue %s: %w", tempID, err)
		}
	}

	// Step 3: Now update all text references using the original old->new mapping
	// Build regex to match any OLD issue ID (before renumbering)
	oldIDs := make([]string, 0, len(idMapping))
	for oldID := range idMapping {
		oldIDs = append(oldIDs, regexp.QuoteMeta(oldID))
	}
	oldPattern := regexp.MustCompile(`\b(` + strings.Join(oldIDs, "|") + `)\b`)

	replaceFunc := func(match string) string {
		if newID, ok := idMapping[match]; ok {
			return newID
		}
		return match
	}

	// Update text references in all issues
	for _, issue := range issues {
		changed := false

		newTitle := oldPattern.ReplaceAllStringFunc(issue.Title, replaceFunc)
		if newTitle != issue.Title {
			issue.Title = newTitle
			changed = true
		}

		newDesc := oldPattern.ReplaceAllStringFunc(issue.Description, replaceFunc)
		if newDesc != issue.Description {
			issue.Description = newDesc
			changed = true
		}

		if issue.Design != "" {
			newDesign := oldPattern.ReplaceAllStringFunc(issue.Design, replaceFunc)
			if newDesign != issue.Design {
				issue.Design = newDesign
				changed = true
			}
		}

		if issue.AcceptanceCriteria != "" {
			newAC := oldPattern.ReplaceAllStringFunc(issue.AcceptanceCriteria, replaceFunc)
			if newAC != issue.AcceptanceCriteria {
				issue.AcceptanceCriteria = newAC
				changed = true
			}
		}

		if issue.Notes != "" {
			newNotes := oldPattern.ReplaceAllStringFunc(issue.Notes, replaceFunc)
			if newNotes != issue.Notes {
				issue.Notes = newNotes
				changed = true
			}
		}

		// Only update if text changed
		if changed {
			if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
				"title":               issue.Title,
				"description":         issue.Description,
				"design":              issue.Design,
				"acceptance_criteria": issue.AcceptanceCriteria,
				"notes":               issue.Notes,
			}, actor); err != nil {
				return fmt.Errorf("failed to update text references in %s: %w", issue.ID, err)
			}
		}
	}

	// Update all dependency links (use the deps we fetched before renaming)
	if err := renumberDependencies(ctx, idMapping, allDepsByIssue); err != nil {
		return fmt.Errorf("failed to update dependencies: %w", err)
	}

	// Update the counter to the highest renumbered ID so next issue gets correct number
	// After renumbering to bd-1..bd-N, set counter to N so next issue is bd-(N+1)
	// We need to FORCE set it (not MAX) because counter may be higher from deleted issues
	// Strategy: Reset (delete) the counter row, then SyncAllCounters recreates it from actual max ID
	sqliteStore, _ := store.(*sqlite.SQLiteStorage)
	if err := sqliteStore.ResetCounter(ctx, prefix); err != nil {
		return fmt.Errorf("failed to reset counter: %w", err)
	}
	// Now sync will recreate it from the actual max ID in the database
	if err := sqliteStore.SyncAllCounters(ctx); err != nil {
		return fmt.Errorf("failed to sync counter: %w", err)
	}

	return nil
}

func renumberDependencies(ctx context.Context, idMapping map[string]string, allDepsByIssue map[string][]*types.Dependency) error {
	// Collect all dependencies to update
	oldDeps := make([]*types.Dependency, 0)
	newDeps := make([]*types.Dependency, 0)

	for issueID, deps := range allDepsByIssue {
		newIssueID, issueRenamed := idMapping[issueID]
		if !issueRenamed {
			newIssueID = issueID
		}

		for _, dep := range deps {
			newDependsOnID, depRenamed := idMapping[dep.DependsOnID]
			if !depRenamed {
				newDependsOnID = dep.DependsOnID
			}

			// If either ID changed, we need to update
			if issueRenamed || depRenamed {
				oldDeps = append(oldDeps, dep)
				newDep := &types.Dependency{
					IssueID:     newIssueID,
					DependsOnID: newDependsOnID,
					Type:        dep.Type,
				}
				newDeps = append(newDeps, newDep)
			}
		}
	}

	// First remove all old dependencies
	for _, oldDep := range oldDeps {
		// Remove old dependency (may not exist if IDs already updated)
		_ = store.RemoveDependency(ctx, oldDep.IssueID, oldDep.DependsOnID, "renumber")
	}

	// Then add all new dependencies
	for _, newDep := range newDeps {
		// Add new dependency
		if err := store.AddDependency(ctx, newDep, "renumber"); err != nil {
			// Ignore duplicate and validation errors (parent-child direction might be swapped)
			if !strings.Contains(err.Error(), "UNIQUE constraint failed") &&
				!strings.Contains(err.Error(), "duplicate") &&
				!strings.Contains(err.Error(), "invalid parent-child") {
				return fmt.Errorf("failed to add dependency %s -> %s: %w", newDep.IssueID, newDep.DependsOnID, err)
			}
		}
	}

	return nil
}

func init() {
	renumberCmd.Flags().Bool("dry-run", false, "Preview changes without applying them")
	renumberCmd.Flags().Bool("force", false, "Actually perform the renumbering")
	rootCmd.AddCommand(renumberCmd)
}
