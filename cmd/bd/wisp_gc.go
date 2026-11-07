package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

var wispGCCmd = &cobra.Command{
	Use:   "gc",
	Short: "Garbage collect old/abandoned wisps",
	Long: `Garbage collect old or abandoned wisps from the database.

A wisp is considered abandoned if:
  - It hasn't been updated in --age duration and is not closed
  - AND it is not live work: blocked steps (waiting on a dependency), pinned
    beads, and any step whose status category is wip (in_progress, blocked,
    hooked) or frozen (deferred, pinned) are never reclaimed by age, no matter
    how long they have been waiting (GH#4394). Custom statuses count by their
    configured category, so only plain open (active) and closed (done) steps
    are age-reclaimable. If the blocked set or the custom-status list cannot be
    read, the GC aborts rather than risk reclaiming live steps.

Wisps that depend on an abandoned wisp are deleted with it, closed ones
included even without --all, but only if they too haven't been updated in
--age duration and are not live work. A dependent that is too young is
spared and loses its edge to the deleted wisp: an open one is collected once
it ages out, a closed one only by --all, --closed or 'bd purge'.

Abandoned wisps are deleted without creating a digest. Use 'bd mol squash'
if you want to preserve a summary before garbage collection.

Use --closed to purge ALL closed wisps (regardless of age). This is the
fastest way to reclaim space from accumulated wisp bloat. Safe by default:
requires --force to actually delete.
Without --force, closed wisps that still gate live work outside the purge
candidate set are skipped in previews; JSON output includes their IDs and
blocking dependents.

Note: This uses time-based cleanup, appropriate for ephemeral wisps.
For graph-pressure staleness detection (blocking other work), see 'bd mol stale'.

Examples:
  bd mol wisp gc                                    # Clean abandoned wisps (default: 1h threshold)
  bd mol wisp gc --dry-run                          # Preview what would be cleaned
  bd mol wisp gc --age 24h                          # Custom age threshold
  bd mol wisp gc --all                              # Also clean closed wisps older than threshold
  bd mol wisp gc --closed                           # Preview closed wisp deletion
  bd mol wisp gc --closed --force                   # Delete all closed wisps
  bd mol wisp gc --closed --dry-run                 # Explicit dry-run (same as no --force)
  bd mol wisp gc --exclude-type agent,rig           # Protect agent and rig wisps from GC
  bd mol wisp gc --closed --force --exclude-type mol # Delete closed wisps except mol type`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runWispGC,
}

// WispGCResult is the JSON output for wisp gc
type WispGCResult struct {
	CleanedIDs   []string `json:"cleaned_ids"`
	CleanedCount int      `json:"cleaned_count"`
	Candidates   int      `json:"candidates,omitempty"`
	DryRun       bool     `json:"dry_run,omitempty"`
}

const (
	noDeletableClosedWispsMessage = "No deletable closed wisps — all have live dependents outside the batch"
	wispGCSkippedJSONKey          = "skipped"
	wispGCSkippedCountJSONKey     = "skipped_count"
)

type wispGCSkippedIssue struct {
	ID                 string   `json:"id"`
	BlockingDependents []string `json:"blocking_dependents"`
}

// protectedWispStatuses returns the statuses whose category means a wisp is
// live work rather than abandoned, so age-based GC must never reclaim it.
//
// Protection is derived from the status *category* rather than a hand-written
// list: CategoryWIP (in_progress/blocked/hooked) is work in flight, and
// CategoryFrozen (deferred/pinned) is work deliberately put on ice — reclaiming
// something a user explicitly deferred defeats the point of deferring it. Only
// CategoryActive (plain open) and CategoryDone (closed) are age-reclaimable.
//
// Custom statuses (status.custom) participate on the same footing, matching the
// sibling destructive command in purge.go. Reading them is required, not
// best-effort: if we cannot enumerate them we must not under-protect and risk
// deleting live molecule steps, so the error propagates and aborts the GC.
func protectedWispStatuses(ctx context.Context, r molReader) (map[types.Status]bool, error) {
	protected := make(map[types.Status]bool)
	for _, s := range []types.Status{
		types.StatusOpen,
		types.StatusInProgress,
		types.StatusBlocked,
		types.StatusClosed,
		types.StatusDeferred,
		types.StatusPinned,
		types.StatusHooked,
	} {
		switch types.BuiltInStatusCategory(s) {
		case types.CategoryWIP, types.CategoryFrozen:
			protected[s] = true
		}
	}

	customStatuses, err := r.GetCustomStatusesDetailed(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading custom statuses for wisp age GC: %w", err)
	}
	for _, cs := range customStatuses {
		switch cs.Category {
		case types.CategoryWIP, types.CategoryFrozen:
			protected[types.Status(cs.Name)] = true
		}
	}
	return protected, nil
}

// isProtectedWisp reports whether a wisp is live work that age-based GC must
// never reclaim. A wisp is protected if it is explicitly pinned, if it is
// blocked on an open dependency (blockedSet, derived from is_blocked), or if
// its status falls in a protected category. Reclaiming any of these
// mid-execution destroys active molecules (GH#4394).
//
// Named isProtectedWisp rather than isActiveWisp to avoid confusion with
// (*DoltStore).isActiveWisp in internal/storage/dolt, which is in this same
// delete path but means only "a row for this ID exists in the wisps table".
func isProtectedWisp(issue *types.Issue, blockedSet map[string]bool, protectedStatuses map[types.Status]bool) bool {
	// The pinned flag is independent of the pinned status; the closed-purge
	// branch of this same command already honors it (see runWispPurgeClosed).
	if issue.Pinned {
		return true
	}
	if blockedSet[issue.ID] {
		return true
	}
	return protectedStatuses[issue.Status]
}

func runWispGC(cmd *cobra.Command, args []string) error {
	CheckReadonly("wisp gc")

	evt := metrics.NewCommandEvent("wisp-gc")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := rootCtx

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	ageStr, _ := cmd.Flags().GetString("age")
	cleanAll, _ := cmd.Flags().GetBool("all")
	closedMode, _ := cmd.Flags().GetBool("closed")
	force, _ := cmd.Flags().GetBool("force")
	excludeTypeStrs, _ := cmd.Flags().GetStringSlice("exclude-type")

	ageThreshold := time.Hour
	if ageStr != "" {
		var err error
		ageThreshold, err = time.ParseDuration(ageStr)
		if err != nil {
			return HandleError("invalid --age duration: %v", err)
		}
	}

	var excludeTypes []types.IssueType
	for _, t := range excludeTypeStrs {
		excludeTypes = append(excludeTypes, types.IssueType(t))
	}

	if usesProxiedServer() {
		return runWispGCProxiedServer(rootCtx, dryRun, ageThreshold, cleanAll, closedMode, force, excludeTypes)
	}

	if store == nil {
		return HandleErrorWithHint("no database connection", diagHint())
	}

	if closedMode {
		return runWispPurgeClosed(ctx, dryRun, force, excludeTypes)
	}

	abandoned, err := findAbandonedWisps(ctx, store, cleanAll, ageThreshold, excludeTypes)
	if err != nil && abandoned == nil {
		return HandleError("%v", err)
	}
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: cascade expansion incomplete: %v\n", err)
	}

	if len(abandoned) == 0 {
		if jsonOutput {
			return outputJSON(WispGCResult{
				CleanedIDs:   []string{},
				CleanedCount: 0,
				DryRun:       dryRun,
			})
		}
		fmt.Println("No abandoned wisps found")
		return nil
	}

	if dryRun {
		if jsonOutput {
			ids := make([]string, len(abandoned))
			for i, o := range abandoned {
				ids[i] = o.ID
			}
			return outputJSON(WispGCResult{
				CleanedIDs:   ids,
				Candidates:   len(abandoned),
				CleanedCount: 0,
				DryRun:       true,
			})
		}
		fmt.Printf("Dry run: would clean %d abandoned wisp(s):\n\n", len(abandoned))
		for _, issue := range abandoned {
			age := formatTimeAgo(issue.UpdatedAt)
			fmt.Printf("  %s: %s (last updated: %s)\n", issue.ID, issue.Title, age)
		}
		fmt.Printf("\nRun without --dry-run to delete these wisps.\n")
		return nil
	}

	ids := make([]string, len(abandoned))
	for i, issue := range abandoned {
		ids[i] = issue.ID
	}
	// Cascade must stay OFF here too. findAbandonedWisps has already expanded
	// dependents and kept only the unprotected ones that meet --age, so a
	// deleteBatch cascade would re-expand from that set to ALL transitive
	// dependents — including blocked/in-progress steps that isProtectedWisp
	// just excluded (GH#4394's protection is only enforced in the pre-filter;
	// cascade bypasses it) and fresh steps the --age check just spared.
	// Without cascade the list is deleted exactly as filtered and live
	// dependents are orphaned (edges dropped, is_blocked recomputed).
	if err := deleteBatch(nil, ids, true, false, false, jsonOutput, false, nil, "wisp gc"); err != nil {
		return HandleError("%v", err)
	}
	return nil
}

// wispIsAbandoned reports whether a wisp counts as abandoned for GC
// purposes: it has not been updated within ageThreshold of now.
//
// ageThreshold <= 0 (--age 0s and below) always reports true, bypassing
// the clock comparison entirely — this matches the GC's own documented
// contract ("hasn't been updated in --age duration"): nothing has been
// updated in the last zero seconds. It also matters for a reason the
// comparison alone does not handle: Dolt's DATETIME(0) column rounds
// fractional seconds half-up on write rather than truncating (#7236), so
// a wisp created in the second half of a wall-clock second can read back
// with updatedAt up to 0.5s in the future. now.Sub(updatedAt) is then
// negative, failing a strict > comparison for a wisp that is, by
// definition, not yet one second old — exactly what --age 0s's "every
// wisp qualifies" contract exists to avoid depending on in the first
// place.
func wispIsAbandoned(now, updatedAt time.Time, ageThreshold time.Duration) bool {
	return ageThreshold <= 0 || now.Sub(updatedAt) > ageThreshold
}

// wispGCNow is the clock seam for findAbandonedWisps' age check, swappable in
// tests. Both gc routes read it, so a test that swaps it must not call
// t.Parallel().
var wispGCNow = time.Now

func findAbandonedWisps(ctx context.Context, r molReader, cleanAll bool, ageThreshold time.Duration, excludeTypes []types.IssueType) ([]*types.Issue, error) {
	ephemeralFlag := true
	filter := types.IssueFilter{
		Ephemeral:    &ephemeralFlag,
		ExcludeTypes: excludeTypes,
		Limit:        5000,
	}
	issues, err := r.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}

	blocked, err := r.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		return nil, fmt.Errorf("determining blocked wisps for age GC: %w", err)
	}
	blockedSet := make(map[string]bool, len(blocked))
	for _, b := range blocked {
		blockedSet[b.ID] = true
	}

	protectedStatuses, err := protectedWispStatuses(ctx, r)
	if err != nil {
		return nil, err
	}

	now := wispGCNow()
	var abandoned []*types.Issue
	for _, issue := range issues {
		if r.IsInfraTypeCtx(ctx, issue.IssueType) {
			continue
		}
		if issue.Status == types.StatusClosed && !cleanAll {
			continue
		}
		if isProtectedWisp(issue, blockedSet, protectedStatuses) {
			continue
		}
		if wispIsAbandoned(now, issue.UpdatedAt, ageThreshold) {
			abandoned = append(abandoned, issue)
		}
	}

	if len(abandoned) == 0 {
		return abandoned, nil
	}

	parentIDs := make([]string, len(abandoned))
	for i, issue := range abandoned {
		parentIDs[i] = issue.ID
	}
	childIDs, cascadeErr := r.FindWispDependentsRecursive(ctx, parentIDs)
	if len(childIDs) > 0 {
		childIDSlice := make([]string, 0, len(childIDs))
		for id := range childIDs {
			childIDSlice = append(childIDSlice, id)
		}
		// TODO(batchgetter): unbounded id list; see issueops.BatchGetter's doc
		// and the importIssueLookup TODO in import_shared.go for why this is
		// not yet routed through the role.
		childIssues, fetchErr := r.GetIssuesByIDs(ctx, childIDSlice)
		if fetchErr == nil {
			abandonedSet := make(map[string]bool, len(abandoned))
			for _, issue := range abandoned {
				abandonedSet[issue.ID] = true
			}
			// Dependents skip the parent scan's not-closed clause: an
			// abandoned molecule's finished steps go with it, even without
			// --all.
			for _, child := range childIssues {
				if abandonedSet[child.ID] {
					continue
				}
				if r.IsInfraTypeCtx(ctx, child.IssueType) {
					continue
				}
				if isProtectedWisp(child, blockedSet, protectedStatuses) {
					continue
				}
				// Children are held to the same --age contract as parents. One
				// spared here is detached when its parent is deleted, so a
				// closed one is left for --all, --closed or bd purge.
				if !wispIsAbandoned(now, child.UpdatedAt, ageThreshold) {
					continue
				}
				abandoned = append(abandoned, child)
			}
		}
	}
	return abandoned, cascadeErr
}

func runWispPurgeClosed(ctx context.Context, dryRun bool, force bool, excludeTypes []types.IssueType) error {
	statusClosed := types.StatusClosed
	ephemeralTrue := true
	filter := types.IssueFilter{
		Status:       &statusClosed,
		Ephemeral:    &ephemeralTrue,
		ExcludeTypes: excludeTypes,
		Limit:        5000,
	}

	closedIssues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return HandleError("listing closed wisps: %v", err)
	}

	// Filter out pinned and infra issues (protected from cleanup)
	pinnedCount := 0
	infraCount := 0
	filtered := make([]*types.Issue, 0, len(closedIssues))
	for _, issue := range closedIssues {
		if issue.Pinned {
			pinnedCount++
			continue
		}
		if store.IsInfraTypeCtx(ctx, issue.IssueType) {
			infraCount++
			continue
		}
		filtered = append(filtered, issue)
	}
	closedIssues = filtered

	if pinnedCount > 0 && !jsonOutput {
		fmt.Printf("Skipping %d pinned issue(s) (protected from cleanup)\n", pinnedCount)
	}
	if infraCount > 0 && !jsonOutput {
		fmt.Printf("Skipping %d configured infra issue(s) protected from GC\n", infraCount)
	}

	if len(closedIssues) == 0 {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"deleted_count": 0,
				"message":       "No closed wisps to delete",
			})
		}
		fmt.Println("No closed wisps to delete")
		return nil
	}

	ids := make([]string, len(closedIssues))
	for i, issue := range closedIssues {
		ids[i] = issue.ID
	}

	// Non-force paths (count preview and --dry-run) skip-with-notice candidates
	// that still gate live work instead of failing the whole batch; --force is
	// unchanged (purges every closed wisp, orphans the live dependents).
	deletable := ids
	var protected map[string][]string
	if !force {
		safe, protectedCandidates, err := partitionClosedWispsByLiveDependents(ctx, ids)
		if err != nil {
			return HandleError("checking live dependents of closed wisps: %v", err)
		}
		protected = protectedCandidates
		reportSkippedProtectedWisps(protected)
		deletable = safe
	}

	if !force && !dryRun {
		if jsonOutput {
			payload := map[string]interface{}{
				"candidates": len(deletable),
				"dry_run":    true,
			}
			addSkippedProtectedWispsJSON(payload, protected)
			return outputJSON(payload)
		}
		if len(deletable) == 0 {
			fmt.Println(noDeletableClosedWispsMessage)
			return nil
		}
		fmt.Printf("Found %d closed wisp(s) to delete\n", len(deletable))
		fmt.Printf("\nUse --force to proceed, or --dry-run for detailed preview.\n")
		return nil
	}

	if len(deletable) == 0 {
		if jsonOutput {
			if dryRun {
				return outputDeletionPreviewReporting([]string{}, nil, false, true, &issueops.DeleteResult{}, nil, true, skippedProtectedWispsJSONFields(protected))
			}
			payload := map[string]interface{}{
				"candidates": 0,
				"dry_run":    dryRun,
			}
			addSkippedProtectedWispsJSON(payload, protected)
			return outputJSON(payload)
		}
		fmt.Println(noDeletableClosedWispsMessage)
		return nil
	}

	if !jsonOutput {
		fmt.Printf("Found %d closed wisp(s)\n", len(deletable))
		if dryRun {
			fmt.Println(ui.RenderWarn("DRY RUN - no changes will be made"))
		}
		fmt.Println()
	}

	// Cascade must stay OFF here. The closed set above is already the complete
	// purge candidate list, so cascade can only ever add NON-closed dependents
	// to the batch. In a linear molecule DAG a closed step's transitive
	// dependents are every other (live) step — cascading swept whole active
	// molecules into deletion, steps, dependency links and events all at once
	// (wisp gc --closed --force self-destructed the deacon patrol).
	// Without cascade, closed wisps are deleted and live dependents are
	// orphaned (edges dropped, is_blocked recomputed) — the same semantics as
	// a plain `bd delete`.
	if err := deleteBatchReporting(deletable, force, dryRun, false, jsonOutput, nil, skippedProtectedWispsJSONFields(protected)); err != nil {
		return HandleError("%v", err)
	}

	if !dryRun && force && !jsonOutput {
		fmt.Printf("\nHint: Run 'bd compact --dolt' to reclaim disk space\n")
	}
	return nil
}

func addSkippedProtectedWispsJSON(payload map[string]interface{}, protected map[string][]string) {
	for key, value := range skippedProtectedWispsJSONFields(protected) {
		payload[key] = value
	}
}

func skippedProtectedWispsJSONFields(protected map[string][]string) map[string]interface{} {
	fields := map[string]interface{}{
		wispGCSkippedCountJSONKey: len(protected),
	}
	if len(protected) > 0 {
		fields[wispGCSkippedJSONKey] = skippedProtectedWispsJSON(protected)
	}
	return fields
}

func skippedProtectedWispsJSON(protected map[string][]string) []wispGCSkippedIssue {
	ids := make([]string, 0, len(protected))
	for id := range protected {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	skipped := make([]wispGCSkippedIssue, 0, len(ids))
	for _, id := range ids {
		skipped = append(skipped, wispGCSkippedIssue{
			ID:                 id,
			BlockingDependents: protected[id],
		})
	}
	return skipped
}

// partitionClosedWispsByLiveDependents splits closed-wisp GC candidates into
// the set that is safe to purge and the set that still gates work outside the
// deletion. A candidate is safe only when every issue that depends on it is
// also being deleted; purging one whose dependent survives would orphan that
// dependent, which is exactly what the non-force DeleteIssues guard rejects.
//
// Dropping one candidate can make an earlier one unsafe — a chained closed
// molecule prefix (step 1 closed, step 2 closed but gating a live step 3) has
// step 2 protected, which in turn leaves step 1 gating the surviving step 2 —
// so the safe set is computed as a fixed point over the candidate set, not a
// single pass. The protected map is keyed by candidate id and holds its sorted
// dependents that are not in the final safe set. Computing it against that
// final set is what keeps the returned safe set from tripping
// DependentsOutsideRequestError.
func partitionClosedWispsByLiveDependents(ctx context.Context, ids []string) (safe []string, protected map[string][]string, err error) {
	records, err := store.GetDependentRecordsForIssues(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	dependents := make(map[string][]string, len(ids))
	for _, id := range ids {
		seen := make(map[string]bool)
		for _, dep := range records[id] {
			if seen[dep.IssueID] {
				continue
			}
			seen[dep.IssueID] = true
			dependents[id] = append(dependents[id], dep.IssueID)
		}
	}

	safeSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		safeSet[id] = true
	}
	for {
		changed := false
		for _, id := range ids {
			if !safeSet[id] {
				continue
			}
			for _, dep := range dependents[id] {
				if !safeSet[dep] {
					delete(safeSet, id)
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}

	protected = make(map[string][]string)
	for _, id := range ids {
		if safeSet[id] {
			safe = append(safe, id)
			continue
		}
		var blockers []string
		for _, dep := range dependents[id] {
			if !safeSet[dep] {
				blockers = append(blockers, dep)
			}
		}
		slices.Sort(blockers)
		protected[id] = blockers
	}
	return safe, protected, nil
}

// reportSkippedProtectedWisps prints the skip-with-notice for closed wisps that
// were withheld because a dependent survives outside the deletion, naming those
// dependents and how to override.
func reportSkippedProtectedWisps(protected map[string][]string) {
	if len(protected) == 0 || jsonOutput {
		return
	}
	ids := make([]string, 0, len(protected))
	for id := range protected {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	fmt.Printf("Skipping %d closed wisp(s) that still gate work outside the batch:\n", len(ids))
	for _, id := range ids {
		fmt.Printf("  %s (blocking dependents: %s)\n", id, strings.Join(protected[id], ", "))
	}
	fmt.Println("Use --force to delete them anyway (orphans the dependents: edges dropped, is_blocked recomputed).")
}
