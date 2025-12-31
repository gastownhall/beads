package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// deleteViaDaemon uses the RPC daemon to delete issues
func deleteViaDaemon(issueIDs []string, force, dryRun, cascade bool, jsonOutput bool, reason string) {
	// NOTE: The daemon's delete handler implements the core deletion logic.
	// cascade and detailed dependency handling are not yet implemented in the RPC layer.
	// For now, we pass force=true to the daemon and rely on its simpler deletion logic.
	
	deleteArgs := &rpc.DeleteArgs{
		IDs:     issueIDs,
		Force:   force,
		DryRun:  dryRun,
		Cascade: cascade,
		Reason:  reason,
	}
	
	resp, err := daemonClient.Delete(deleteArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	
	if !resp.Success {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}
	
	// Parse response
	var result map[string]interface{}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}
	
	if jsonOutput {
		outputJSON(result)
		return
	}
	
	// Pretty print for human output
	if dryRun {
		fmt.Printf("Dry run - would delete %v issue(s)\n", result["issue_count"])
		return
	}
	
	deletedCount := int(result["deleted_count"].(float64))
	totalCount := int(result["total_count"].(float64))

	if deletedCount > 0 {
		if deletedCount == 1 {
			fmt.Printf("%s Deleted %s\n", ui.RenderPass("✓"), issueIDs[0])
		} else {
			fmt.Printf("%s Deleted %d issue(s)\n", ui.RenderPass("✓"), deletedCount)
		}
	}

	if errors, ok := result["errors"].([]interface{}); ok && len(errors) > 0 {
		fmt.Printf("\n%s Warnings:\n", ui.RenderWarn("⚠"))
		for _, e := range errors {
			fmt.Printf("  %s\n", e)
		}
		if deletedCount < totalCount {
			os.Exit(1)
		}
	}
}

var deleteCmd = &cobra.Command{
	Use:     "delete <issue-id> [issue-id...]",
	GroupID: "issues",
	Short:   "Delete one or more issues and clean up references",
	Long: `Delete one or more issues and clean up all references to them.
This command will:
1. Remove all dependency links (any type, both directions) involving the issues
2. Update text references to "[deleted:ID]" in directly connected issues
3. Delete the issues from the database (creates tombstones by default)

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
  bd delete bd-1 --force

PERMANENT DELETION:
Use --hard to permanently delete (bypass tombstones):
  bd delete bd-1 bd-2 --hard --force

WARNING: --hard bypasses sync safety. Use only when you are certain
the issues will not resurrect from remote branches.`,
	Args: cobra.MinimumNArgs(0),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("delete")
		fromFile, _ := cmd.Flags().GetString("from-file")
		force, _ := cmd.Flags().GetBool("force")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		cascade, _ := cmd.Flags().GetBool("cascade")
		hardDelete, _ := cmd.Flags().GetBool("hard")
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "" {
			reason = "delete"
		}
		// Use global jsonOutput set by PersistentPreRun
		// Collect issue IDs from args and/or file
		issueIDs := make([]string, 0, len(args))
		issueIDs = append(issueIDs, args...)
		if fromFile != "" {
			fileIDs, err := readIssueIDsFromFile(fromFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
				os.Exit(1)
			}
			issueIDs = append(issueIDs, fileIDs...)
		}
		if len(issueIDs) == 0 {
			fmt.Fprintf(os.Stderr, "Error: no issue IDs provided\n")
			_ = cmd.Usage()
			os.Exit(1)
		}
		// Remove duplicates
		issueIDs = uniqueStrings(issueIDs)
		
		// Use daemon if available, otherwise use direct mode
		if daemonClient != nil {
			deleteViaDaemon(issueIDs, force, dryRun, cascade, jsonOutput, reason)
			return
		}
		
		// Direct mode - ensure store is available
		if store == nil {
			if err := ensureStoreActive(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
		
		// Handle batch deletion in direct mode
		// Also use batch path for cascade (which needs to expand dependents)
		if len(issueIDs) > 1 || cascade {
			deleteBatch(cmd, issueIDs, force, dryRun, cascade, jsonOutput, hardDelete, reason)
			return
		}
		
		// Single issue deletion (legacy behavior)
		issueID := issueIDs[0]
		ctx := rootCtx
		// Get the issue to be deleted
		issue, err := store.GetIssue(ctx, issueID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if issue == nil {
			fmt.Fprintf(os.Stderr, "Error: issue %s not found\n", issueID)
			os.Exit(1)
		}
		// Find all connected issues (dependencies in both directions)
		connectedIssues := make(map[string]*types.Issue)
		// Get dependencies (issues this one depends on)
		deps, err := store.GetDependencies(ctx, issueID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting dependencies: %v\n", err)
			os.Exit(1)
		}
		for _, dep := range deps {
			connectedIssues[dep.ID] = dep
		}
		// Get dependents (issues that depend on this one)
		dependents, err := store.GetDependents(ctx, issueID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting dependents: %v\n", err)
			os.Exit(1)
		}
		for _, dependent := range dependents {
			connectedIssues[dependent.ID] = dependent
		}
		// Get dependency records (outgoing) to count how many we'll remove
		depRecords, err := store.GetDependencyRecords(ctx, issueID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting dependency records: %v\n", err)
			os.Exit(1)
		}
		// Build the regex pattern for matching issue IDs (handles hyphenated IDs properly)
		// Pattern: (^|non-word-char)(issueID)($|non-word-char) where word-char includes hyphen
		idPattern := `(^|[^A-Za-z0-9_-])(` + regexp.QuoteMeta(issueID) + `)($|[^A-Za-z0-9_-])`
		re := regexp.MustCompile(idPattern)
		replacementText := `$1[deleted:` + issueID + `]$3`
		// Preview mode
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
					// Check if there are actually text references using the fixed regex
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
			return
		}
		// Actually delete
		deleteActor := getActorWithGit()
		// 1. Update text references in connected issues (all text fields)
		updatedIssueCount := 0
		for id, connIssue := range connectedIssues {
			updates := make(map[string]interface{})
			// Replace in description
			if re.MatchString(connIssue.Description) {
				newDesc := re.ReplaceAllString(connIssue.Description, replacementText)
				updates["description"] = newDesc
			}
			// Replace in notes
			if connIssue.Notes != "" && re.MatchString(connIssue.Notes) {
				newNotes := re.ReplaceAllString(connIssue.Notes, replacementText)
				updates["notes"] = newNotes
			}
			// Replace in design
			if connIssue.Design != "" && re.MatchString(connIssue.Design) {
				newDesign := re.ReplaceAllString(connIssue.Design, replacementText)
				updates["design"] = newDesign
			}
			// Replace in acceptance_criteria
			if connIssue.AcceptanceCriteria != "" && re.MatchString(connIssue.AcceptanceCriteria) {
				newAC := re.ReplaceAllString(connIssue.AcceptanceCriteria, replacementText)
				updates["acceptance_criteria"] = newAC
			}
			if len(updates) > 0 {
				if err := store.UpdateIssue(ctx, id, updates, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: Failed to update references in %s: %v\n", id, err)
				} else {
					updatedIssueCount++
				}
			}
		}
		// 2. Remove all dependency links (outgoing)
		outgoingRemoved := 0
		for _, dep := range depRecords {
			if err := store.RemoveDependency(ctx, dep.IssueID, dep.DependsOnID, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to remove dependency %s → %s: %v\n",
					dep.IssueID, dep.DependsOnID, err)
			} else {
				outgoingRemoved++
			}
		}
		// 3. Remove inbound dependency links (issues that depend on this one)
		inboundRemoved := 0
		for _, dep := range dependents {
			if err := store.RemoveDependency(ctx, dep.ID, issueID, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to remove dependency %s → %s: %v\n",
					dep.ID, issueID, err)
			} else {
				inboundRemoved++
			}
		}
		// 4. Create tombstone (instead of deleting from database)
		// Phase 1 dual-write: still writes to deletions.jsonl (step 0), now also creates tombstone
		if err := createTombstone(ctx, issueID, deleteActor, reason); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating tombstone: %v\n", err)
			os.Exit(1)
		}
		// Note: No longer call removeIssueFromJSONL - tombstone will be exported to JSONL
		// Schedule auto-flush to update neighbors
		markDirtyAndScheduleFlush()
		totalDepsRemoved := outgoingRemoved + inboundRemoved
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"deleted":              issueID,
				"dependencies_removed": totalDepsRemoved,
				"references_updated":   updatedIssueCount,
			})
		} else {
			fmt.Printf("%s Deleted %s\n", ui.RenderPass("✓"), issueID)
			fmt.Printf("  Removed %d dependency link(s)\n", totalDepsRemoved)
			fmt.Printf("  Updated text references in %d issue(s)\n", updatedIssueCount)
		}
	},
}
// createTombstone converts an issue to a tombstone record
// Note: This is a direct database operation since Storage interface doesn't have CreateTombstone
func createTombstone(ctx context.Context, issueID string, actor string, reason string) error {
	// We need to access the SQLite storage directly
	// Check if store is SQLite storage
	type tombstoner interface {
		CreateTombstone(ctx context.Context, id string, actor string, reason string) error
	}
	if t, ok := store.(tombstoner); ok {
		return t.CreateTombstone(ctx, issueID, actor, reason)
	}
	return fmt.Errorf("tombstone operation not supported by this storage backend")
}

// deleteIssue removes an issue from the database
// Note: This is a direct database operation since Storage interface doesn't have Delete
func deleteIssue(ctx context.Context, issueID string) error {
	// We need to access the SQLite storage directly
	// Check if store is SQLite storage
	type deleter interface {
		DeleteIssue(ctx context.Context, id string) error
	}
	if d, ok := store.(deleter); ok {
		return d.DeleteIssue(ctx, issueID)
	}
	return fmt.Errorf("delete operation not supported by this storage backend")
}
// removeIssueFromJSONL removes a deleted issue from the JSONL file
// Auto-flush cannot see deletions because the dirty_issues row is deleted with the issue
func removeIssueFromJSONL(issueID string) error {
	path := findJSONLPath()
	if path == "" {
		return nil // No JSONL file yet
	}
	// Read all issues except the deleted one
	// #nosec G304 - controlled path from config
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No file, nothing to clean
		}
		return fmt.Errorf("failed to open JSONL: %w", err)
	}
	var issues []*types.Issue
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var iss types.Issue
		if err := json.Unmarshal([]byte(line), &iss); err != nil {
			// Skip malformed lines
			continue
		}
		if iss.ID != issueID {
			issues = append(issues, &iss)
		}
	}
	if err := scanner.Err(); err != nil {
		_ = f.Close()
		return fmt.Errorf("failed to read JSONL: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close JSONL: %w", err)
	}
	// Write to temp file atomically
	temp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	// #nosec G304 - controlled path from config
	out, err := os.OpenFile(temp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	enc := json.NewEncoder(out)
	for _, iss := range issues {
		if err := enc.Encode(iss); err != nil {
			_ = out.Close()
			_ = os.Remove(temp)
			return fmt.Errorf("failed to write issue: %w", err)
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	// Atomic rename
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	return nil
}
// deleteBatch handles deletion of multiple issues
//nolint:unparam // cmd parameter required for potential future use
func deleteBatch(_ *cobra.Command, issueIDs []string, force bool, dryRun bool, cascade bool, jsonOutput bool, hardDelete bool, reason string) {
	// Ensure we have a direct store
	if store == nil {
		if err := ensureStoreActive(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}
	ctx := rootCtx
	// Type assert to SQLite storage
	d, ok := store.(*sqlite.SQLiteStorage)
	if !ok {
		// Fallback for non-SQLite storage (e.g., MemoryStorage in --no-db mode)
		deleteBatchFallback(issueIDs, force, dryRun, cascade, jsonOutput, hardDelete, reason)
		return
	}
	// Verify all issues exist
	issues := make(map[string]*types.Issue)
	notFound := []string{}
	for _, id := range issueIDs {
		issue, err := d.GetIssue(ctx, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting issue %s: %v\n", id, err)
			os.Exit(1)
		}
		if issue == nil {
			notFound = append(notFound, id)
		} else {
			issues[id] = issue
		}
	}
	if len(notFound) > 0 {
		fmt.Fprintf(os.Stderr, "Error: issues not found: %s\n", strings.Join(notFound, ", "))
		os.Exit(1)
	}
	// Dry-run or preview mode
	if dryRun || !force {
		result, err := d.DeleteIssues(ctx, issueIDs, cascade, false, true)
		if err != nil {
			// Try to show preview even if there are dependency issues
			showDeletionPreview(issueIDs, issues, cascade, err)
			os.Exit(1)
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
		return
	}
	// Pre-collect connected issues before deletion (so we can update their text references)
	connectedIssues := make(map[string]*types.Issue)
	idSet := make(map[string]bool)
	for _, id := range issueIDs {
		idSet[id] = true
	}
	for _, id := range issueIDs {
		// Get dependencies (issues this one depends on)
		deps, err := store.GetDependencies(ctx, id)
		if err == nil {
			for _, dep := range deps {
				if !idSet[dep.ID] {
					connectedIssues[dep.ID] = dep
				}
			}
		}
		// Get dependents (issues that depend on this one)
		dependents, err := store.GetDependents(ctx, id)
		if err == nil {
			for _, dep := range dependents {
				if !idSet[dep.ID] {
					connectedIssues[dep.ID] = dep
				}
			}
		}
	}
	// Actually delete (creates tombstones)
	result, err := d.DeleteIssues(ctx, issueIDs, cascade, force, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Hard delete: immediately prune tombstones from JSONL
	// Note: We keep tombstones in DB to prevent resurrection during sync.
	// The tombstones will be exported and synced to remote, blocking resurrection.
	// Use 'bd cleanup --hard' after syncing to fully purge old tombstones.
	if hardDelete {
		if !jsonOutput {
			fmt.Println(ui.RenderWarn("⚠️  HARD DELETE MODE: Pruning tombstones from JSONL"))
			fmt.Println("  Note: Tombstones kept in DB to prevent resurrection. Run 'bd sync' then 'bd cleanup --hard' to fully purge.")
		}
		// Prune tombstones from JSONL using negative TTL (immediate expiration)
		if pruneResult, err := pruneExpiredTombstones(-1); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to prune tombstones from JSONL: %v\n", err)
		} else if pruneResult != nil && pruneResult.PrunedCount > 0 && !jsonOutput {
			fmt.Printf("  Pruned %d tombstone(s) from JSONL\n", pruneResult.PrunedCount)
		}
	}

	// Update text references in connected issues (using pre-collected issues)
	updatedCount := updateTextReferencesInIssues(ctx, issueIDs, connectedIssues)
	// Note: No longer remove from JSONL - tombstones will be exported to JSONL
	// Schedule auto-flush
	markDirtyAndScheduleFlush()
	// Output results
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"deleted":              issueIDs,
			"deleted_count":        result.DeletedCount,
			"dependencies_removed": result.DependenciesCount,
			"labels_removed":       result.LabelsCount,
			"events_removed":       result.EventsCount,
			"references_updated":   updatedCount,
			"orphaned_issues":      result.OrphanedIssues,
		})
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
}

// deleteBatchFallback handles batch deletion for non-SQLite storage (e.g., MemoryStorage in --no-db mode)
// It iterates through issues one by one, creating tombstones for each.
func deleteBatchFallback(issueIDs []string, force bool, dryRun bool, cascade bool, jsonOutput bool, hardDelete bool, reason string) {
	ctx := rootCtx

	// Cascade not supported in fallback mode
	if cascade {
		fmt.Fprintf(os.Stderr, "Error: --cascade not supported in --no-db mode\n")
		os.Exit(1)
	}

	// Verify all issues exist first
	issues := make(map[string]*types.Issue)
	notFound := []string{}
	for _, id := range issueIDs {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting issue %s: %v\n", id, err)
			os.Exit(1)
		}
		if issue == nil {
			notFound = append(notFound, id)
		} else {
			issues[id] = issue
		}
	}
	if len(notFound) > 0 {
		fmt.Fprintf(os.Stderr, "Error: issues not found: %s\n", strings.Join(notFound, ", "))
		os.Exit(1)
	}

	// Preview mode
	if dryRun || !force {
		fmt.Printf("\n%s\n", ui.RenderFail("⚠️  DELETE PREVIEW"))
		fmt.Printf("\nIssues to delete (%d):\n", len(issueIDs))
		for _, id := range issueIDs {
			if issue := issues[id]; issue != nil {
				fmt.Printf("  %s: %s\n", id, issue.Title)
			}
		}
		if dryRun {
			fmt.Printf("\n(Dry-run mode - no changes made)\n")
		} else {
			fmt.Printf("\n%s\n", ui.RenderWarn("This operation cannot be undone!"))
			fmt.Printf("To proceed, run: %s\n",
				ui.RenderWarn("bd delete "+strings.Join(issueIDs, " ")+" --force"))
		}
		return
	}

	// Pre-collect connected issues before deletion (for text reference updates)
	connectedIssues := make(map[string]*types.Issue)
	idSet := make(map[string]bool)
	for _, id := range issueIDs {
		idSet[id] = true
	}
	for _, id := range issueIDs {
		deps, err := store.GetDependencies(ctx, id)
		if err == nil {
			for _, dep := range deps {
				if !idSet[dep.ID] {
					connectedIssues[dep.ID] = dep
				}
			}
		}
		dependents, err := store.GetDependents(ctx, id)
		if err == nil {
			for _, dep := range dependents {
				if !idSet[dep.ID] {
					connectedIssues[dep.ID] = dep
				}
			}
		}
	}

	// Delete each issue
	deleteActor := getActorWithGit()
	deletedCount := 0
	depsRemoved := 0

	for _, issueID := range issueIDs {
		// Remove dependencies (outgoing)
		depRecords, err := store.GetDependencyRecords(ctx, issueID)
		if err == nil {
			for _, dep := range depRecords {
				if err := store.RemoveDependency(ctx, dep.IssueID, dep.DependsOnID, deleteActor); err == nil {
					depsRemoved++
				}
			}
		}

		// Remove dependencies (inbound)
		dependents, err := store.GetDependents(ctx, issueID)
		if err == nil {
			for _, dep := range dependents {
				if err := store.RemoveDependency(ctx, dep.ID, issueID, deleteActor); err == nil {
					depsRemoved++
				}
			}
		}

		// Create tombstone
		if err := createTombstone(ctx, issueID, deleteActor, reason); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating tombstone for %s: %v\n", issueID, err)
			continue
		}
		deletedCount++
	}

	// Update text references in connected issues
	updatedCount := updateTextReferencesInIssues(ctx, issueIDs, connectedIssues)

	// Hard delete: remove from JSONL immediately
	if hardDelete {
		for _, id := range issueIDs {
			_ = removeIssueFromJSONL(id)
		}
	}

	// Schedule auto-flush
	markDirtyAndScheduleFlush()

	// Output results
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"deleted":              issueIDs,
			"deleted_count":        deletedCount,
			"dependencies_removed": depsRemoved,
			"references_updated":   updatedCount,
		})
	} else {
		fmt.Printf("%s Deleted %d issue(s)\n", ui.RenderPass("✓"), deletedCount)
		fmt.Printf("  Removed %d dependency link(s)\n", depsRemoved)
		fmt.Printf("  Updated text references in %d issue(s)\n", updatedCount)
	}
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

// getActorWithGit returns the actor for audit trail with git config fallback.
// Priority: global actor var (from --actor flag or BD_ACTOR env) > git config user.name > $USER > "unknown"
func getActorWithGit() string {
	// If actor is already set (from flag or env), use it
	if actor != "" && actor != "unknown" {
		return actor
	}

	// Try git config user.name
	cmd := exec.Command("git", "config", "user.name")
	if output, err := cmd.Output(); err == nil {
		if gitUser := strings.TrimSpace(string(output)); gitUser != "" {
			return gitUser
		}
	}

	// Fall back to USER env
	if user := os.Getenv("USER"); user != "" {
		return user
	}

	return "unknown"
}

func init() {
	deleteCmd.Flags().BoolP("force", "f", false, "Actually delete (without this flag, shows preview)")
	deleteCmd.Flags().String("from-file", "", "Read issue IDs from file (one per line)")
	deleteCmd.Flags().Bool("dry-run", false, "Preview what would be deleted without making changes")
	deleteCmd.Flags().Bool("cascade", false, "Recursively delete all dependent issues")
	deleteCmd.Flags().Bool("hard", false, "Permanently delete (skip tombstone, cannot be recovered via sync)")
	deleteCmd.Flags().String("reason", "", "Reason for deletion (stored in tombstone for audit trail)")
	rootCmd.AddCommand(deleteCmd)
}
