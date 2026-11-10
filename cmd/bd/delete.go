package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	storeissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
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

Cascade: Also delete parent-child descendants; other dependents survive
(orphaned, as with --force). Preview first: --dry-run lists every id it deletes.
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

		// A8 (beads#4682): issueops.DeleteRequest.ExpectedVersion already refuses
		// (ErrValidation) a non-nil token beside more than one distinct id, but
		// that refusal reads differently from the other three verbs' —
		// requireSingleIfRevisionID gives this one the same wording and the same
		// "before any write" timing as update/close/assign.
		ifRevision, err := parseIfRevisionFlag(cmd)
		if err != nil {
			return err
		}
		if err := requireSingleIfRevisionID(ifRevision, issueIDs); err != nil {
			return err
		}

		if store == nil {
			if err := ensureStoreActive(); err != nil {
				return HandleError("%v", err)
			}
		}

		if len(issueIDs) > 1 || cascade {
			if err := deleteBatch(cmd, issueIDs, force, dryRun, cascade, jsonOutput, false, ifRevision); err != nil {
				if ifRevision != nil {
					if reported, ok := reportIfRevisionFailure("deleting", issueIDs[0], err, ifRevision); ok {
						return reported
					}
				}
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
		//
		// Exact here too, never a leading-prefix abbreviation: an id that is
		// gone (already deleted, a re-run script) would otherwise resolve to
		// whichever surviving issue's id it begins, and the preview would
		// print `bd delete <that issue> --force` as the next step.
		routedResult, err := resolveAndGetIssueForMutationExact(ctx, store, issueID)
		if err != nil {
			// mc-zndi7.81: this pre-flight existence check runs before
			// deleter.Delete() and the per-id lock fence #7244 added, so a
			// same-token --if-revision racer that loses the fence sees the
			// row disappear right here instead of inside the guarded write
			// below — see reportIfRevisionTargetGone.
			if reported, ok := reportIfRevisionTargetGone("deleting", issueID, err, ifRevision); ok {
				return reported
			}
			if isNotFoundErr(err) {
				return HandleError("issue %s not found", issueID)
			}
			if refusal := deleteAbbreviationRefusal(err); refusal != nil {
				return HandleError("%v", refusal)
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
			Actor:           currentActor(),
			IDs:             []string{issueID},
			Force:           force,
			DryRun:          dryRun || !force,
			ExpectedVersion: ifRevision,
		}
		opsCtx, err := issueOpsContext(ctx)
		if err != nil {
			return HandleError("%v", err)
		}
		result, err := deleter.Delete(opsCtx, request)
		if request.DryRun {
			if err != nil {
				// A8 (beads#4682): a guard mismatch is reported through the
				// dedicated conditional-write envelope instead of the generic
				// preview-with-error below — same split delete's real run makes,
				// so --dry-run and the real run answer a stale token identically.
				if ifRevision != nil {
					if reported, ok := reportIfRevisionFailure("deleting", issueID, err, ifRevision); ok {
						return reported
					}
				}
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
			if ifRevision != nil {
				if reported, ok := reportIfRevisionFailure("deleting", issueID, err, ifRevision); ok {
					return reported
				}
			}
			return HandleError("deleting issue: %v", err)
		}

		commandDidWrite.Store(true)

		if routedResult.RedirectedCopy {
			printRetainedCopyNote(issueID, routedResult.RedirectedCopyOwner)
		}

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

// deleteAbbreviationRefusal is the refusal of an id that names no issue
// exactly but is the start of one or more: delete needs the full id, so say
// that and name the ids it begins. It is nil for any other error.
func deleteAbbreviationRefusal(err error) error {
	var abbrev *utils.AbbreviatedIDError
	if !errors.As(err, &abbrev) {
		return nil
	}
	return &deleteAbbreviationError{abbrev: abbrev}
}

// deleteAbbreviationError is that refusal. To a delete, an id that is only the
// start of other ids names no row, so the text leads with "not found" — the
// words gascity's isBdNotFound reads as an already-gone bead — and the error
// unwraps to storage.ErrNotFound, which lets a guarded `bd delete --cascade`
// report it as the same lost race as any other gone target (see
// reportIfRevisionTargetGone).
type deleteAbbreviationError struct {
	abbrev *utils.AbbreviatedIDError
}

func (e *deleteAbbreviationError) Error() string {
	return fmt.Sprintf("issue %q not found: bd delete needs the full issue id, and %q is only the start of %s",
		e.abbrev.Input, e.abbrev.Input, strings.Join(e.abbrev.Matches, ", "))
}

func (e *deleteAbbreviationError) Unwrap() []error { return []error{storage.ErrNotFound, e.abbrev} }

// deleteIssue removes an issue from the database.
func deleteIssue(ctx context.Context, issueID string) error {
	return store.DeleteIssue(ctx, issueID)
}

// deleteBatch is the multi-id and cascade path, shared by `bd delete`,
// `bd cleanup`, `bd wisp gc` and `bd mol burn`.
//
// It resolves the ids in the front door - exact-id matching and
// cross-repository routing, which issueops.DeleteRequest deliberately does not
// do - and hands the RESOLVED ids to the role. Never a leading-prefix
// abbreviation: a re-run of a finished --from-file list would otherwise
// delete every surviving issue whose id one of its ids begins.
//
// ifRevision is non-nil only via `bd delete`'s own --if-revision (beads#4682);
// its other three callers always pass nil, and requireSingleIfRevisionID has
// already refused this path if it were asked to guard more than one id.
//
//nolint:unparam // cmd parameter required for potential future use
func deleteBatch(_ *cobra.Command, issueIDs []string, force bool, dryRun bool, cascade bool, jsonOutput bool, _ bool, ifRevision *int64, _ ...string) error {
	return deleteBatchReporting(issueIDs, force, dryRun, cascade, jsonOutput, ifRevision, nil)
}

// deleteBatchReporting is deleteBatch for a bulk command that reports on its
// own selection: report's members (`bd admin cleanup`'s retention_required) are added to the --json preview and result. A nil report
// adds nothing, which is deleteBatch.
func deleteBatchReporting(issueIDs []string, force, dryRun, cascade, jsonOutput bool, ifRevision *int64, report map[string]interface{}) error {
	if store == nil {
		if err := ensureStoreActive(); err != nil {
			return err
		}
	}
	ctx := rootCtx
	issues := make(map[string]*types.Issue)
	resolvedIDs := make([]string, 0, len(issueIDs))
	notFound := []string{}
	retainedCopies := []retainedCopy{}
	// The destination each resolved id would be deleted from, keyed by
	// storeDestinationKey, in first-seen order for a stable refusal message.
	destinations := map[string]*batchDestination{}
	var destinationOrder []string
	var routedStore storage.DoltStorage
	for _, id := range issueIDs {
		result, err := resolveAndGetIssueForMutationExact(ctx, store, id)
		if err != nil {
			if isNotFoundErr(err) {
				notFound = append(notFound, id)
			} else if refusal := deleteAbbreviationRefusal(err); refusal != nil {
				return refusal
			} else {
				return fmt.Errorf("getting issue %s: %v", id, err)
			}
		} else {
			issues[result.ResolvedID] = result.Issue
			resolvedIDs = append(resolvedIDs, result.ResolvedID)
			if result.RedirectedCopy {
				retainedCopies = append(retainedCopies, retainedCopy{id: result.ResolvedID, owner: result.RedirectedCopyOwner})
			}
			key := storeDestinationKey(result.Store)
			if d, ok := destinations[key]; ok {
				d.ids = append(d.ids, result.ResolvedID)
			} else {
				destinations[key] = &batchDestination{display: deleteDestinationDisplay(result), ids: []string{result.ResolvedID}}
				destinationOrder = append(destinationOrder, key)
			}
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
	// The batch deletes through ONE store: this one when no id routed,
	// otherwise the first routed handle. Ids whose resolutions reached a
	// different destination would be deleted from whichever store that handle
	// happens to serve — the copy from its owner, or worse, the owner's row
	// through a handle that cannot see it. Refuse before any delete and ask
	// for one command per store.
	if len(destinationOrder) > 1 {
		parts := make([]string, 0, len(destinationOrder))
		for _, key := range destinationOrder {
			d := destinations[key]
			parts = append(parts, fmt.Sprintf("%s (%s)", d.display, strings.Join(d.ids, ", ")))
		}
		return fmt.Errorf("refusing to delete a batch that spans %d stores: %s; run a separate bd delete per store",
			len(destinationOrder), strings.Join(parts, "; "))
	}
	if len(notFound) > 0 {
		// The role's own existence probe reports a missing id with this same
		// error, and its text is the flat list every caller has always
		// printed. What it adds is the storage.ErrNotFound sentinel, which
		// lets the guarded `bd delete --cascade` caller's
		// reportIfRevisionFailure report a target that a concurrent `bd
		// delete` removed before this resolution as the same lost race as one
		// removed inside deleter.Delete below: precondition_failed, not exit 1
		// (see reportIfRevisionTargetGone).
		return &issueops.NotFoundError{IDs: notFound}
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
		Actor:           currentActor(),
		IDs:             resolvedIDs,
		Cascade:         cascade,
		Force:           force,
		DryRun:          dryRun || !force,
		ExpectedVersion: ifRevision,
	}
	opsCtx, err := issueOpsContext(ctx)
	if err != nil {
		return HandleError("%v", err)
	}
	result, err := deleter.Delete(opsCtx, request)
	if request.DryRun {
		if err != nil {
			// A8 (beads#4682): let the caller's dedicated conditional-write
			// envelope report a guard mismatch instead of the generic
			// preview-with-error below -- see the single-id path's identical
			// split. classifyIfRevisionFailure, not a bare ifRevision!=nil check,
			// because an unrelated dry-run failure (not-found, bad request) on a
			// guarded call still belongs to the generic preview path.
			if ifRevision != nil {
				if _, _, _, _, ok := classifyIfRevisionFailure(err, ifRevision); ok {
					return err
				}
			}
			if previewErr := outputDeletionPreviewReporting(resolvedIDs, issues, cascade, dryRun, nil, err, jsonOutput, report); previewErr != nil {
				return previewErr
			}
			if jsonOutput {
				return outputJSONError(err, "")
			}
			return err
		}
		// The front door hydrated only the REQUESTED rows; the preview below
		// enumerates every row the role resolved — under --cascade the whole
		// containment closure, plus the orphans it leaves (GH#6301). JSON
		// and quiet never print the enumeration, so they skip the read.
		if !jsonOutput && !isQuiet() {
			hydrateDeletePreviewTitles(ctx, batchStore, issues, result)
		}
		if previewErr := outputDeletionPreviewReporting(resolvedIDs, issues, cascade, dryRun, &result, nil, jsonOutput, report); previewErr != nil {
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

	// A delete redirected from a hydrated copy removed the owner's row only;
	// tell the user which copy that leaves behind, one note per deleted copy.
	for _, c := range retainedCopies {
		printRetainedCopyNote(c.id, c.owner)
	}

	// Tell this command's own auto-export that these ids are supposed to be
	// gone. Without it the export's orphan guard sees them as JSONL-only
	// records absent from the store, refuses to overwrite, and never saves
	// state — which is the permanent wedge of GH#5896. Recorded for the
	// requested ids; anything cascade removed alongside them is proven later
	// by the store's own history instead.
	commandDeletedIssueIDs.add(resolvedIDs...)

	// NO COMMIT COMPENSATION HERE, for the reason the single-id path gives:
	// the role versions the deletion itself, on the store the rows were
	// deleted from, and defers it in batch and off modes.

	if jsonOutput {
		answer := map[string]interface{}{
			"deleted":              resolvedIDs,
			"deleted_count":        result.Deleted,
			"dependencies_removed": result.Dependencies,
			"labels_removed":       result.Labels,
			"events_removed":       result.Events,
			"references_updated":   result.ReferencesUpdated,
			"orphaned_issues":      result.Orphaned,
			// deleted stays the requested ids; deleted_ids names every row
			// deleted_count counts, the cascade closure included (GH#6301).
			"deleted_ids": result.DeletedIDs,
		}
		for key, value := range report {
			answer[key] = value
		}
		if err := outputJSON(answer); err != nil {
			return err
		}
	} else {
		fmt.Printf("%s Deleted %d issue(s)\n", ui.RenderPass("✓"), result.Deleted)
		if cascade && len(result.DeletedIDs) > 0 {
			// The cascade's reach named, not just counted (GH#6301): the
			// confirmation is the last place a caller can still see which rows
			// went with the request.
			fmt.Printf("  Deleted: %s\n", strings.Join(result.DeletedIDs, ", "))
		}
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

// retainedCopy records one id whose delete was redirected from a hydrated
// project copy to the auto-route owner: the id, and the owner store whose row
// the delete actually removed. The note about the copy this leaves behind is
// printed after the delete commits, one per id.
type retainedCopy struct {
	id    string
	owner string
}

// batchDestination groups the ids a delete batch resolved into ONE store:
// display names that destination for the mixed-batch refusal message, and the
// ids that landed there. Insertion order (kept by the caller) makes the
// message stable across runs.
type batchDestination struct {
	display string
	ids     []string
}

// storeDestinationKey is the identity of the store a resolved id would be
// deleted from, so a batch that spans two stores can refuse before deleting
// anything. Locatable stores (storage.StoreLocator, via the outermost
// decorator) key on their canonical CLI directory — the same directory the
// CLI opens, so two handles onto one database share a key. A store that
// locates no directory (remote backends) keys on its unwrapped pointer, which
// cannot be confused with a path and stays stable for one handle.
func storeDestinationKey(s storage.DoltStorage) string {
	unwrapped := storage.UnwrapStore(s)
	if locator, ok := unwrapped.(storage.StoreLocator); ok {
		if cli := locator.CLIDir(); cli != "" {
			return "store:" + utils.CanonicalizePath(cli)
		}
	}
	return fmt.Sprintf("handle:%p", unwrapped)
}

// deleteDestinationDisplay names the store a delete resolved to, for the
// mixed-batch refusal. A redirected hydrated copy names the owner path the
// resolver already recorded; anything else names its CLI directory; a store
// that names neither is "an unidentified store" — still distinct in the
// message via its key, but honestly unnamed.
func deleteDestinationDisplay(result *RoutedResult) string {
	if result.RedirectedCopyOwner != "" {
		return result.RedirectedCopyOwner
	}
	if locator, ok := storage.UnwrapStore(result.Store).(storage.StoreLocator); ok {
		if cli := locator.CLIDir(); cli != "" {
			return cli
		}
	}
	return "an unidentified store"
}

// printRetainedCopyNote tells the user that a delete redirected from a
// hydrated copy removed the owner's row only, and names the copy it leaves
// behind. Without it the copy looks deleted from this command while `bd list`
// (which stays local) keeps showing it — the same silent-split confusion
// GH#6058 is about, one delete later.
//
// Suppressed by --quiet like every other non-error stderr note in this
// package (see printContributorRoutingNotice), and never in --json, where
// callers parse stdout and the delete result already carries the truth.
func printRetainedCopyNote(issueID, ownerPath string) {
	if isQuiet() || jsonOutput {
		return
	}
	fmt.Fprintf(os.Stderr, "note: deleted %s from the auto-routed store at %s; the copy this project holds of it remains\n",
		printedIssueID(issueID), remedy.PrintedValue(ownerPath))
	fmt.Fprintf(os.Stderr, "  The copy is local data now: bd delete %s --force here deletes it\n", printedIssueID(issueID))
}

// outputDeletionPreview renders a deletion preview without exposing issue
// payloads in machine-readable or quiet output.
//
// result is the ROLE's dry run, or nil when the role refused: printing zeros
// beside a refusal would read as "nothing would have been deleted" rather than
// "we did not get that far".
func outputDeletionPreview(issueIDs []string, issues map[string]*types.Issue, cascade bool, dryRun bool, result *issueops.DeleteResult, depError error, jsonOutput bool) error {
	return outputDeletionPreviewReporting(issueIDs, issues, cascade, dryRun, result, depError, jsonOutput, nil)
}

// outputDeletionPreviewReporting is outputDeletionPreview with report's
// members added to the --json preview (see deleteBatchReporting).
func outputDeletionPreviewReporting(issueIDs []string, issues map[string]*types.Issue, cascade bool, dryRun bool, result *issueops.DeleteResult, depError error, jsonOutput bool, report map[string]interface{}) error {
	if jsonOutput {
		preview := map[string]interface{}{
			"preview":   true,
			"dry_run":   dryRun,
			"issue_ids": issueIDs,
			"cascade":   cascade,
		}
		for key, value := range report {
			preview[key] = value
		}
		if result != nil {
			preview["would_delete"] = result.Deleted
			preview["would_remove_dependencies"] = result.Dependencies
			preview["would_remove_labels"] = result.Labels
			preview["would_remove_events"] = result.Events
			preview["would_orphan"] = len(result.Orphaned)
			// The ids beside the counts, named as the deletion will report
			// them (deleted_ids, orphaned_issues), so a caller can check it
			// got what it previewed (GH#6301).
			preview["would_delete_ids"] = result.DeletedIDs
			preview["would_orphan_ids"] = result.Orphaned
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
	// The role's answer names every row the request would take (GH#6301):
	// under --cascade that is the whole containment closure, and a preview
	// listing only the requested ids with a count beside them is what let a
	// 48-row deletion look like a bounded one. When the role refused
	// (result == nil) there is no closure to name, so the requested ids stand
	// in as before. A row whose title did not load is still listed, by id.
	previewIDs := issueIDs
	if result != nil && len(result.DeletedIDs) > 0 {
		previewIDs = result.DeletedIDs
	}
	fmt.Printf("\nIssues to delete (%d):\n", len(previewIDs))
	for _, id := range previewIDs {
		title := ""
		if issue := issues[id]; issue != nil {
			title = issue.Title
		}
		fmt.Printf("  %s: %s\n", id, title)
	}
	if cascade {
		fmt.Printf("\n%s Cascade mode enabled - will also delete parent-child descendants; other dependents survive\n", ui.RenderWarn("⚠"))
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
			for _, id := range result.Orphaned {
				title := ""
				if issue := issues[id]; issue != nil {
					title = issue.Title
				}
				fmt.Printf("  %s: %s\n", id, title)
			}
		}
		if dryRun {
			fmt.Printf("\n(Dry-run mode - no changes made)\n")
		}
	}
	return nil
}

// hydrateDeletePreviewTitles batch-loads the rows the preview enumerates but
// the front door did not hydrate: the role's answer names the whole resolved
// set — under --cascade the containment closure — and the orphans it leaves,
// none of which the per-id resolution read (GH#6301).
//
// Titles are presentation, so a load that fails lists those rows by id alone
// — the error is dropped on purpose. It never replaces a row the caller
// already handed in: issues keeps the front door's rows.
func hydrateDeletePreviewTitles(ctx context.Context, store storage.DoltStorage, issues map[string]*types.Issue, result issueops.DeleteResult) {
	extra := make([]string, 0, len(result.DeletedIDs)+len(result.Orphaned))
	for _, id := range result.DeletedIDs {
		if issues[id] == nil {
			extra = append(extra, id)
		}
	}
	for _, id := range result.Orphaned {
		if issues[id] == nil {
			extra = append(extra, id)
		}
	}
	if len(extra) == 0 {
		return
	}
	rows, err := store.GetIssuesByIDs(ctx, extra)
	if err != nil {
		return
	}
	for _, row := range rows {
		if row != nil {
			issues[row.ID] = row
		}
	}
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
	deleteCmd.Flags().Bool("cascade", false, "Also delete parent-child descendants; other dependents survive")
	// A8 (beads#4682)
	deleteCmd.Flags().String("if-revision", "", ifRevisionFlagHelp)
	deleteCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(deleteCmd)
}
