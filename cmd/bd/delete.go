package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
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

DEPENDENCY HANDLING (the same on a local database and against a team server):
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
				if _, ok := exitCodeFromError(err); ok {
					return err
				}
				return HandleError("%v", err)
			}
			return nil
		}

		issueID := issueIDs[0]
		ctx := rootCtx
		// Get the issue to be deleted, using prefix-based routing. Resolution
		// stays in the front door: issueops.DeleteRequest.IDs are exact, because
		// resolving an ambiguous prefix and then deleting the row it hit is the
		// one place a convenience is not.
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

		deleter, err := activeStore.Deleter()
		if err != nil {
			return HandleError("%v", err)
		}
		// --force is this command's CONFIRMATION as well as its orphan mode, so an
		// unconfirmed run asks the role the same question a --dry-run does. The
		// rewrite runs INSIDE the transaction that deletes, because it is the
		// role's.
		request := issueops.DeleteRequest{
			Actor:  actor,
			IDs:    []string{issueID},
			Force:  force,
			DryRun: dryRun || !force,
		}
		opsCtx, err := issueOpsContext(ctx)
		if err != nil {
			return HandleError("%v", err)
		}
		result, err := deleter.Delete(opsCtx, request)
		if request.DryRun {
			if err != nil {
				if previewErr := outputDeletionPreview([]string{issueID}, map[string]*types.Issue{issueID: issue}, false, dryRun, nil, err, jsonOutput); previewErr != nil {
					return previewErr
				}
				if jsonOutput {
					return outputJSONError(err, "")
				}
				return HandleError("previewing deletion: %v", err)
			}
			if jsonOutput || isQuiet() {
				return outputDeletionPreview([]string{issueID}, map[string]*types.Issue{issueID: issue}, false, dryRun, &result, nil, jsonOutput)
			}
			return renderSingleDeletePreview(ctx, activeStore, issueID, issue, dryRun, result)
		}
		if err != nil {
			return HandleError("deleting issue: %v", err)
		}

		commandDidWrite.Store(true)

		// NO COMMIT COMPENSATION HERE. The role versions its own deletion on
		// every backend now, including the embedded one, which publishes the
		// entry after its SQL commit — and it does so on the store the rows
		// were actually deleted from, which for a prefix-routed id is the
		// TARGET repository rather than this workspace. This route once had to
		// mint that commit itself, because the port onto the role dropped the
		// version commit embedded deletes used to get; a compensation here
		// would now find a clean working set and add nothing but a second
		// spelling of the same event. Batch and off modes are still honored:
		// issueOpsContext above defers the role's commit in either.

		if jsonOutput {
			// The single-issue keys, unchanged: `deleted` is the id rather than a
			// list here, which is what every `bd delete <one-id> --json` parses.
			if err := outputJSON(map[string]interface{}{
				"deleted":              issueID,
				"dependencies_removed": result.Dependencies,
				"references_updated":   result.ReferencesUpdated,
			}); err != nil {
				return err
			}
		} else {
			fmt.Printf("%s Deleted %s\n", ui.RenderPass("✓"), issueID)
			fmt.Printf("  Removed %d dependency link(s)\n", result.Dependencies)
			fmt.Printf("  Updated text references in %d issue(s)\n", result.ReferencesUpdated)
		}
		return nil
	},
}

// renderSingleDeletePreview prints the human preview for a one-issue delete. The
// counts come from the role's dry run; the edge listing and the "which neighbors
// cite this id" lines are reads this handler makes, because a delete answers
// with an effect rather than with rows.
func renderSingleDeletePreview(
	ctx context.Context, activeStore storage.DoltStorage,
	issueID string, issue *types.Issue, dryRun bool, result issueops.DeleteResult,
) error {
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
	// The role's own citation rule, not a second copy: a preview naming a
	// different set of neighbors than the deletion rewrites is worse than none.
	re := storeissueops.DeletedReferencePattern(issueID)

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
	if dryRun {
		fmt.Printf("\nWould delete: %d issues\n", result.Deleted)
		fmt.Printf("Would remove: %d dependencies, %d labels, %d events\n", result.Dependencies, result.Labels, result.Events)
		if len(result.Orphaned) > 0 {
			fmt.Printf("Would orphan: %d issues\n", len(result.Orphaned))
		}
		fmt.Printf("\n(Dry-run mode - no changes made)\n")
	} else {
		fmt.Printf("\n%s\n", ui.RenderWarn("This operation cannot be undone!"))
		fmt.Printf("To proceed, run: %s\n\n", ui.RenderWarn("bd delete "+issueID+" --force"))
	}
	return nil
}

// deleteIssue removes an issue from the database.
func deleteIssue(ctx context.Context, issueID string) error {
	return store.DeleteIssue(ctx, issueID)
}

// deleteBatch is the multi-id and cascade path, shared by `bd delete`,
// `bd cleanup`, `bd wisp gc` and `bd mol burn`.
//
// It resolves the ids the way this front door always has - prefix matching and
// cross-repository routing, which issueops.DeleteRequest deliberately does not
// do - and hands the RESOLVED ids to the role.
//
//nolint:unparam // cmd parameter required for potential future use
func deleteBatch(_ *cobra.Command, issueIDs []string, force bool, dryRun bool, cascade bool, jsonOutput bool, _ bool, _ ...string) error {
	if store == nil {
		if err := ensureStoreActive(); err != nil {
			return err
		}
	}
	ctx := rootCtx
	issues := make(map[string]*types.Issue)
	resolvedIDs := make([]string, 0, len(issueIDs))
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
			resolvedIDs = append(resolvedIDs, result.ResolvedID)
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

	deleter, err := batchStore.Deleter()
	if err != nil {
		return err
	}
	// --force is the confirmation as well as the orphan mode, so an unconfirmed
	// run asks the role what it WOULD do; see the single-id path.
	request := issueops.DeleteRequest{
		Actor:   actor,
		IDs:     resolvedIDs,
		Cascade: cascade,
		Force:   force,
		DryRun:  dryRun || !force,
	}
	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	result, err := deleter.Delete(opsCtx, request)
	if request.DryRun {
		if err != nil {
			if previewErr := outputDeletionPreview(resolvedIDs, issues, cascade, dryRun, nil, err, jsonOutput); previewErr != nil {
				return previewErr
			}
			if jsonOutput {
				return outputJSONError(err, "")
			}
			return err
		}
		if previewErr := outputDeletionPreview(resolvedIDs, issues, cascade, dryRun, &result, nil, jsonOutput); previewErr != nil {
			return previewErr
		}
		if !dryRun && !jsonOutput && !isQuiet() {
			fmt.Printf("\n%s\n", ui.RenderWarn("This operation cannot be undone!"))
			if cascade {
				fmt.Printf("To proceed with cascade deletion, run: %s\n",
					ui.RenderWarn("bd delete "+strings.Join(resolvedIDs, " ")+" --cascade --force"))
			} else {
				fmt.Printf("To proceed, run: %s\n",
					ui.RenderWarn("bd delete "+strings.Join(resolvedIDs, " ")+" --force"))
			}
		}
		return nil
	}
	if err != nil {
		return err
	}

	commandDidWrite.Store(true)

	// NO COMMIT COMPENSATION HERE, for the reason the single-id path gives:
	// the role versions the deletion itself, on the store the rows were
	// deleted from, and defers it in batch and off modes.

	if jsonOutput {
		if err := outputJSON(map[string]interface{}{
			"deleted":              resolvedIDs,
			"deleted_count":        result.Deleted,
			"dependencies_removed": result.Dependencies,
			"labels_removed":       result.Labels,
			"events_removed":       result.Events,
			"references_updated":   result.ReferencesUpdated,
			"orphaned_issues":      result.Orphaned,
		}); err != nil {
			return err
		}
	} else {
		fmt.Printf("%s Deleted %d issue(s)\n", ui.RenderPass("✓"), result.Deleted)
		fmt.Printf("  Removed %d dependency link(s)\n", result.Dependencies)
		fmt.Printf("  Removed %d label(s)\n", result.Labels)
		fmt.Printf("  Removed %d event(s)\n", result.Events)
		fmt.Printf("  Updated text references in %d issue(s)\n", result.ReferencesUpdated)
		if len(result.Orphaned) > 0 {
			fmt.Printf("  %s Orphaned %d issue(s): %s\n",
				ui.RenderWarn("⚠"), len(result.Orphaned), strings.Join(result.Orphaned, ", "))
		}
	}
	return nil
}

// outputDeletionPreview renders a deletion preview without exposing issue
// payloads in machine-readable or quiet output.
//
// result is the ROLE's dry run, or nil when the role refused: printing zeros
// beside a refusal would read as "nothing would have been deleted" rather than
// "we did not get that far".
func outputDeletionPreview(issueIDs []string, issues map[string]*types.Issue, cascade bool, dryRun bool, result *issueops.DeleteResult, depError error, jsonOutput bool) error {
	if jsonOutput {
		preview := map[string]interface{}{
			"preview":   true,
			"dry_run":   dryRun,
			"issue_ids": issueIDs,
			"cascade":   cascade,
		}
		if result != nil {
			preview["would_delete"] = result.Deleted
			preview["would_remove_dependencies"] = result.Dependencies
			preview["would_remove_labels"] = result.Labels
			preview["would_remove_events"] = result.Events
			preview["would_orphan"] = len(result.Orphaned)
		}
		if depError != nil {
			preview["error"] = depError.Error()
		}
		return outputJSON(preview)
	}
	if isQuiet() {
		return nil
	}

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
	if result != nil {
		fmt.Printf("\nWould delete: %d issues\n", result.Deleted)
		fmt.Printf("Would remove: %d dependencies, %d labels, %d events\n",
			result.Dependencies, result.Labels, result.Events)
		if len(result.Orphaned) > 0 {
			fmt.Printf("Would orphan: %d issues\n", len(result.Orphaned))
		}
		if dryRun {
			fmt.Printf("\n(Dry-run mode - no changes made)\n")
		}
	}
	return nil
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
