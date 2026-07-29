package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var deleteCmd = &cobra.Command{
	Use:     "delete <issue-id> [issue-id...]",
	GroupID: "issues",
	Short:   "Delete one or more issues and clean up references",
	Long: `Delete one or more issues and clean up all references to them.
This command will:
1. Remove all dependency links (any type, both directions) involving the issues
2. Update text references to "[deleted:ID]" in directly connected issues
3. Permanently delete the issues from the database

This is a destructive operation that cannot be undone. Use with caution.

BATCH DELETION:
Delete multiple issues at once:
  bd delete bd-1 bd-2 bd-3 --force

Delete from file (one ID per line):
  bd delete --from-file deletions.txt --force

Preview before deleting:
  bd delete --from-file deletions.txt --dry-run

DEPENDENCY HANDLING:
Default: Fails if any issue has dependents not in deletion set
  bd delete bd-1 bd-2

Cascade: Recursively delete all dependents
  bd delete bd-1 --cascade --force

Force: Delete and orphan dependents
  bd delete bd-1 --force`,
	Args:          cobra.MinimumNArgs(0),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("delete")

		evt := metrics.NewCommandEvent("delete")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runDeleteProxiedServer(cmd, rootCtx, args)
		}

		fromFile, _ := cmd.Flags().GetString("from-file")
		force, _ := cmd.Flags().GetBool("force")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		cascade, _ := cmd.Flags().GetBool("cascade")
		issueIDs := make([]string, 0, len(args))
		issueIDs = append(issueIDs, args...)
		if fromFile != "" {
			fileIDs, err := readIssueIDsFromFile(fromFile)
			if err != nil {
				return HandleError("reading file: %v", err)
			}
			issueIDs = append(issueIDs, fileIDs...)
		}
		if len(issueIDs) == 0 {
			_ = cmd.Usage()
			return HandleError("no issue IDs provided")
		}
		issueIDs = uniqueStrings(issueIDs)

		if store == nil {
			if err := ensureStoreActive(); err != nil {
				return HandleError("%v", err)
			}
		}

		if len(issueIDs) > 1 || cascade {
			if err := deleteBatch(cmd, issueIDs, force, dryRun, cascade, jsonOutput, false); err != nil {
				return HandleError("%v", err)
			}
			return nil
		}

		issueID := issueIDs[0]
		ctx := rootCtx
		// Get the issue to be deleted, using prefix-based routing
		routedResult, err := resolveAndGetIssueForMutation(ctx, store, issueID)
		if err != nil {
			if isNotFoundErr(err) {
				return HandleError("issue %s not found", issueID)
			}
			return HandleError("%v", err)
		}
		defer routedResult.Close()
		issue := routedResult.Issue
		issueID = routedResult.ResolvedID
		activeStore := routedResult.Store
		connectedIssues := make(map[string]*types.Issue)
		deps, err := activeStore.GetDependencies(ctx, issueID)
		if err != nil {
			return HandleError("getting dependencies: %v", err)
		}
		for _, dep := range deps {
			connectedIssues[dep.ID] = dep
		}
		dependents, err := activeStore.GetDependents(ctx, issueID)
		if err != nil {
			return HandleError("getting dependents: %v", err)
		}
		for _, dependent := range dependents {
			connectedIssues[dependent.ID] = dependent
		}
		depRecords, err := activeStore.GetDependencyRecords(ctx, issueID)
		if err != nil {
			return HandleError("getting dependency records: %v", err)
		}
		// Build the regex pattern for matching issue IDs (handles hyphenated IDs properly)
		// Pattern: (^|non-word-char)(issueID)($|non-word-char) where word-char includes hyphen
		idPattern := `(^|[^A-Za-z0-9_-])(` + regexp.QuoteMeta(issueID) + `)($|[^A-Za-z0-9_-])`
		re := regexp.MustCompile(idPattern)
		replacementText := `$1[deleted:` + issueID + `]$3`
		if !force {
			fmt.Printf("\n%s\n", ui.RenderFail("⚠️  DELETE PREVIEW"))
			fmt.Printf("\nIssue to delete:\n")
			fmt.Printf("  %s: %s\n", issueID, issue.Title)
			totalDeps := len(depRecords) + len(dependents)
			if totalDeps > 0 {
				fmt.Printf("\nDependency links to remove: %d\n", totalDeps)
				for _, dep := range depRecords {
					fmt.Printf("  %s → %s (%s)\n", dep.IssueID, dep.DependsOnID, dep.Type)
				}
				for _, dep := range dependents {
					fmt.Printf("  %s → %s (inbound)\n", dep.ID, issueID)
				}
			}
			if len(connectedIssues) > 0 {
				fmt.Printf("\nConnected issues where text references will be updated:\n")
				issuesWithRefs := 0
				for id, connIssue := range connectedIssues {
					hasRefs := re.MatchString(connIssue.Description) ||
						(connIssue.Notes != "" && re.MatchString(connIssue.Notes)) ||
						(connIssue.Design != "" && re.MatchString(connIssue.Design)) ||
						(connIssue.AcceptanceCriteria != "" && re.MatchString(connIssue.AcceptanceCriteria))
					if hasRefs {
						fmt.Printf("  %s: %s\n", id, connIssue.Title)
						issuesWithRefs++
					}
				}
				if issuesWithRefs == 0 {
					fmt.Printf("  (none have text references)\n")
				}
			}
			fmt.Printf("\n%s\n", ui.RenderWarn("This operation cannot be undone!"))
			fmt.Printf("To proceed, run: %s\n\n", ui.RenderWarn("bd delete "+issueID+" --force"))
			return nil
		}
		updatedIssueCount := 0
		totalDepsRemoved := 0
		deleteErr := transactHonoringAutoCommit(ctx, activeStore, fmt.Sprintf("bd: delete %s", issueID), func(tx storage.Transaction) error {
			for id, connIssue := range connectedIssues {
				updates := make(map[string]interface{})
				if re.MatchString(connIssue.Description) {
					updates["description"] = re.ReplaceAllString(connIssue.Description, replacementText)
				}
				if connIssue.Notes != "" && re.MatchString(connIssue.Notes) {
					updates["notes"] = re.ReplaceAllString(connIssue.Notes, replacementText)
				}
				if connIssue.Design != "" && re.MatchString(connIssue.Design) {
					updates["design"] = re.ReplaceAllString(connIssue.Design, replacementText)
				}
				if connIssue.AcceptanceCriteria != "" && re.MatchString(connIssue.AcceptanceCriteria) {
					updates["acceptance_criteria"] = re.ReplaceAllString(connIssue.AcceptanceCriteria, replacementText)
				}
				if len(updates) > 0 {
					if err := tx.UpdateIssue(ctx, id, updates, actor); err != nil {
						return fmt.Errorf("update references in %s: %w", id, err)
					}
					updatedIssueCount++
				}
			}
			for _, dep := range depRecords {
				if err := tx.RemoveDependency(ctx, dep.IssueID, dep.DependsOnID, actor); err != nil {
					return fmt.Errorf("remove dependency %s → %s: %w", dep.IssueID, dep.DependsOnID, err)
				}
				totalDepsRemoved++
			}
			for _, dep := range dependents {
				if err := tx.RemoveDependency(ctx, dep.ID, issueID, actor); err != nil {
					return fmt.Errorf("remove dependency %s → %s: %w", dep.ID, issueID, err)
				}
				totalDepsRemoved++
			}
			if err := tx.DeleteIssue(ctx, issueID); err != nil {
				return fmt.Errorf("delete %s: %w", issueID, err)
			}
			return nil
		})
		if deleteErr != nil {
			return HandleError("deleting issue: %v", deleteErr)
		}

		commandDidWrite.Store(true)

		if jsonOutput {
			if err := outputJSON(map[string]interface{}{
				"deleted":              issueID,
				"dependencies_removed": totalDepsRemoved,
				"references_updated":   updatedIssueCount,
			}); err != nil {
				return err
			}
		} else {
			fmt.Printf("%s Deleted %s\n", ui.RenderPass("✓"), issueID)
			fmt.Printf("  Removed %d dependency link(s)\n", totalDepsRemoved)
			fmt.Printf("  Updated text references in %d issue(s)\n", updatedIssueCount)
		}
		return nil
	},
}

// deleteIssue removes an issue from the database.
func deleteIssue(ctx context.Context, issueID string) error {
	return store.DeleteIssue(ctx, issueID)
}

//nolint:unparam // cmd parameter required for potential future use
func deleteBatch(_ *cobra.Command, issueIDs []string, force bool, dryRun bool, cascade bool, jsonOutput bool, _ bool, _ ...string) error {
	if store == nil {
		if err := ensureStoreActive(); err != nil {
			return err
		}
	}
	ctx := rootCtx
	issues := make(map[string]*types.Issue)
	notFound := []string{}
	var routedStore storage.DoltStorage
	for _, id := range issueIDs {
		result, err := resolveAndGetIssueForMutation(ctx, store, id)
		if err != nil {
			if isNotFoundErr(err) {
				notFound = append(notFound, id)
			} else {
				return fmt.Errorf("getting issue %s: %v", id, err)
			}
		} else {
			issues[result.ResolvedID] = result.Issue
			if result.Routed && routedStore == nil {
				routedStore = result.Store
			} else {
				result.Close()
			}
		}
	}
	if routedStore != nil {
		defer func() { _ = routedStore.Close() }()
	}
	if len(notFound) > 0 {
		return fmt.Errorf("issues not found: %s", strings.Join(notFound, ", "))
	}
	batchStore := store
	if routedStore != nil {
		batchStore = routedStore
	}
	if dryRun || !force {
		result, err := batchStore.DeleteIssues(ctx, issueIDs, cascade, false, true)
		if err != nil {
			showDeletionPreview(issueIDs, issues, cascade, err)
			return err
		}
		showDeletionPreview(issueIDs, issues, cascade, nil)
		fmt.Printf("\nWould delete: %d issues\n", result.DeletedCount)
		fmt.Printf("Would remove: %d dependencies, %d labels, %d events\n",
			result.DependenciesCount, result.LabelsCount, result.EventsCount)
		if len(result.OrphanedIssues) > 0 {
			fmt.Printf("Would orphan: %d issues\n", len(result.OrphanedIssues))
		}
		if dryRun {
			fmt.Printf("\n(Dry-run mode - no changes made)\n")
		} else {
			fmt.Printf("\n%s\n", ui.RenderWarn("This operation cannot be undone!"))
			if cascade {
				fmt.Printf("To proceed with cascade deletion, run: %s\n",
					ui.RenderWarn("bd delete "+strings.Join(issueIDs, " ")+" --cascade --force"))
			} else {
				fmt.Printf("To proceed, run: %s\n",
					ui.RenderWarn("bd delete "+strings.Join(issueIDs, " ")+" --force"))
			}
		}
		return nil
	}
	connectedIssues := make(map[string]*types.Issue)
	idSet := make(map[string]bool)
	for _, id := range issueIDs {
		idSet[id] = true
	}
	for _, id := range issueIDs {
		deps, err := batchStore.GetDependencies(ctx, id)
		if err == nil {
			for _, dep := range deps {
				if !idSet[dep.ID] {
					connectedIssues[dep.ID] = dep
				}
			}
		}
		dependents, err := batchStore.GetDependents(ctx, id)
		if err == nil {
			for _, dep := range dependents {
				if !idSet[dep.ID] {
					connectedIssues[dep.ID] = dep
				}
			}
		}
	}
	result, err := batchStore.DeleteIssues(ctx, issueIDs, cascade, force, false)
	if err != nil {
		return err
	}

	updatedCount := updateTextReferencesInIssues(ctx, issueIDs, connectedIssues)

	commandDidWrite.Store(true)

	if jsonOutput {
		if err := outputJSON(map[string]interface{}{
			"deleted":              issueIDs,
			"deleted_count":        result.DeletedCount,
			"dependencies_removed": result.DependenciesCount,
			"labels_removed":       result.LabelsCount,
			"events_removed":       result.EventsCount,
			"references_updated":   updatedCount,
			"orphaned_issues":      result.OrphanedIssues,
		}); err != nil {
			return err
		}
	} else {
		fmt.Printf("%s Deleted %d issue(s)\n", ui.RenderPass("✓"), result.DeletedCount)
		fmt.Printf("  Removed %d dependency link(s)\n", result.DependenciesCount)
		fmt.Printf("  Removed %d label(s)\n", result.LabelsCount)
		fmt.Printf("  Removed %d event(s)\n", result.EventsCount)
		fmt.Printf("  Updated text references in %d issue(s)\n", updatedCount)
		if len(result.OrphanedIssues) > 0 {
			fmt.Printf("  %s Orphaned %d issue(s): %s\n",
				ui.RenderWarn("⚠"), len(result.OrphanedIssues), strings.Join(result.OrphanedIssues, ", "))
		}
	}
	return nil
}

// showDeletionPreview shows what would be deleted
func showDeletionPreview(issueIDs []string, issues map[string]*types.Issue, cascade bool, depError error) {
	fmt.Printf("\n%s\n", ui.RenderFail("⚠️  DELETE PREVIEW"))
	fmt.Printf("\nIssues to delete (%d):\n", len(issueIDs))
	for _, id := range issueIDs {
		if issue := issues[id]; issue != nil {
			fmt.Printf("  %s: %s\n", id, issue.Title)
		}
	}
	if cascade {
		fmt.Printf("\n%s Cascade mode enabled - will also delete all dependent issues\n", ui.RenderWarn("⚠"))
	}
	if depError != nil {
		fmt.Printf("\n%s\n", ui.RenderFail(depError.Error()))
	}
}

// updateTextReferencesInIssues updates text references to deleted issues in pre-collected connected issues
func updateTextReferencesInIssues(ctx context.Context, deletedIDs []string, connectedIssues map[string]*types.Issue) int {
	updatedCount := 0
	// For each deleted issue, update references in all connected issues
	for _, id := range deletedIDs {
		// Build regex pattern
		idPattern := `(^|[^A-Za-z0-9_-])(` + regexp.QuoteMeta(id) + `)($|[^A-Za-z0-9_-])`
		re := regexp.MustCompile(idPattern)
		replacementText := `$1[deleted:` + id + `]$3`
		for connID, connIssue := range connectedIssues {
			updates := make(map[string]interface{})
			if re.MatchString(connIssue.Description) {
				updates["description"] = re.ReplaceAllString(connIssue.Description, replacementText)
			}
			if connIssue.Notes != "" && re.MatchString(connIssue.Notes) {
				updates["notes"] = re.ReplaceAllString(connIssue.Notes, replacementText)
			}
			if connIssue.Design != "" && re.MatchString(connIssue.Design) {
				updates["design"] = re.ReplaceAllString(connIssue.Design, replacementText)
			}
			if connIssue.AcceptanceCriteria != "" && re.MatchString(connIssue.AcceptanceCriteria) {
				updates["acceptance_criteria"] = re.ReplaceAllString(connIssue.AcceptanceCriteria, replacementText)
			}
			if len(updates) > 0 {
				if err := store.UpdateIssue(ctx, connID, updates, actor); err == nil {
					updatedCount++
					// Update the in-memory issue to avoid double-replacing
					if desc, ok := updates["description"].(string); ok {
						connIssue.Description = desc
					}
					if notes, ok := updates["notes"].(string); ok {
						connIssue.Notes = notes
					}
					if design, ok := updates["design"].(string); ok {
						connIssue.Design = design
					}
					if ac, ok := updates["acceptance_criteria"].(string); ok {
						connIssue.AcceptanceCriteria = ac
					}
				}
			}
		}
	}
	return updatedCount
}

// readIssueIDsFromFile reads issue IDs from a file (one per line)
func readIssueIDsFromFile(filename string) ([]string, error) {
	// #nosec G304 - user-provided file path is intentional
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var ids []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ids = append(ids, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// uniqueStrings removes duplicates from a slice of strings
func uniqueStrings(slice []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

func init() {
	deleteCmd.Flags().BoolP("force", "f", false, "Actually delete (without this flag, shows preview)")
	deleteCmd.Flags().String("from-file", "", "Read issue IDs from file (one per line)")
	deleteCmd.Flags().Bool("dry-run", false, "Preview what would be deleted without making changes")
	deleteCmd.Flags().Bool("cascade", false, "Recursively delete all dependent issues")
	deleteCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(deleteCmd)
}
