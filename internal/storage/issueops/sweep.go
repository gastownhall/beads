package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// SweepInTx is the store-backed body behind issueops.Sweeper: the whole of
// `bd purge` and `bd prune` from candidate query to deletion, inside ONE
// transaction.
//
// It lives here rather than in an importable internal/workapi/store<role>
// package because the work is several reads and a write that must see one
// snapshot, and storage.DoltStorage publishes methods, not transactions. The
// two Dolt-backed stores share this body, so they are ONE vote and the
// unit-of-work provider is the second.
//
// It assumes a request already refused by workapi.ValidateSweepRequest. The
// accessors validate BEFORE opening a transaction, so a refusal costs no
// database work.
//
// WHAT ONE TRANSACTION GIVES UP: the server-backed store's own DeleteIssues
// splits large wisp deletions into batched transactions to stay inside Dolt's
// write timeout, and a sweep cannot both do that and promise that the set it
// counted is the set it deleted. It promises the latter.
//
// WITH RetainIDs it retires what it deletes: the kept candidates are split by
// storage plane BEFORE the delete (afterwards there is no row left to say which
// plane held it), and one tombstone per deleted row is written into that
// plane's tombstone table after the delete, on the same transaction — so a
// tombstone that cannot be written takes the delete back with it.
func SweepInTx(ctx context.Context, tx *sql.Tx, req publicops.SweepRequest) (publicops.SweepResult, error) {
	result := publicops.SweepResult{DryRun: req.DryRun, RetainIDs: req.RetainIDs}

	search := SearchIssuesInTx
	if workapi.SweepSearchesWispsPlaneOnly(req) {
		search = SearchWispsPlaneInTx
	}
	candidates, err := search(ctx, tx, "", workapi.BuildSweepCandidateFilter(req))
	if err != nil {
		return publicops.SweepResult{}, fmt.Errorf("listing sweep candidates: %w", err)
	}

	kept, skips := workapi.FilterSweepCandidates(candidates, req.IDPattern, req.ClosedBefore)
	result.Skipped = skips

	if !req.RetainIDs {
		retained, err := ResolveRetainedTypesInTx(ctx, tx)
		if err != nil {
			return publicops.SweepResult{}, err
		}
		kept, result.Skipped.RetentionRequired = workapi.PartitionSweepRetainedTypes(kept, retained)
	}

	if req.ProtectLiveDependents {
		protected, err := sweepLiveDependentsInTx(ctx, tx, kept)
		if err != nil {
			return publicops.SweepResult{}, err
		}
		kept, result.Skipped.LiveDependent = workapi.PartitionSweepLiveDependents(kept, protected)
	}

	if req.ProtectReferenced {
		referenced, err := sweepReferencedInTx(ctx, tx, kept)
		if err != nil {
			return publicops.SweepResult{}, err
		}
		var count int
		kept, count, result.ReferencedIDs = workapi.PartitionSweepReferenced(kept, referenced)
		result.Skipped.Referenced = count
	}

	kept, result.Remaining = workapi.LimitSweepCandidates(kept, req.Limit)

	if len(kept) == 0 {
		return result, nil
	}

	ids := make([]string, len(kept))
	for i, issue := range kept {
		ids[i] = issue.ID
	}

	var retireWisp, retireDurable []RetiredID
	if req.RetainIDs && !req.DryRun {
		retireWisp, retireDurable, err = sweepRetiredIDsInTx(ctx, tx, kept)
		if err != nil {
			return publicops.SweepResult{}, err
		}
	}

	// cascade=false, force=true: a sweep deletes what it selected and ORPHANS
	// dependents outside the selection. The DRY RUN passes the same flags rather
	// than the force=false the direct route used to pass, so a preview cannot
	// fail where the real run would succeed.
	deleted, err := DeleteIssuesInTx(ctx, tx, ids, false, true, req.DryRun, req.Actor)
	if err != nil {
		return publicops.SweepResult{}, err
	}
	if req.RetainIDs && !req.DryRun {
		if err := RetireIDsInTx(ctx, tx, retireWisp, retireDurable); err != nil {
			return publicops.SweepResult{}, err
		}
	}
	result.Swept = deleted.DeletedCount
	result.Dependencies = deleted.DependenciesCount
	result.Labels = deleted.LabelsCount
	result.Events = deleted.EventsCount
	return result, nil
}

// ResolveRetainedTypesInTx reads the workspace's retained issue types
// (issueops.SettingKeyTypesRetained) on the caller's transaction. It reads the
// config row only — no config.yaml layer, no cache — so a sweep sees exactly
// what the workspace database holds at its own snapshot. A read error is
// returned, never treated as "nothing retained": a sweep that could not read
// the set must fail rather than delete rows it should have held back.
func ResolveRetainedTypesInTx(ctx context.Context, tx DBTX) (map[string]bool, error) {
	value, err := GetConfigInTx(ctx, tx, publicops.SettingKeyTypesRetained)
	if err != nil {
		return nil, fmt.Errorf("reading %s for the retained-type protection: %w", publicops.SettingKeyTypesRetained, err)
	}
	return workapi.RetainedTypesFromSetting(value), nil
}

// sweepRetiredIDsInTx builds the tombstones a retaining sweep writes for the
// candidates it is about to delete, split by the plane each row is stored in.
// It must run before the delete: afterwards no row is left to read the plane
// from. Every kept candidate carries a closed_at — FilterSweepCandidates drops
// the ones that do not — so a missing one is a broken invariant, not a row to
// retire without it.
//
// It first coordinates every id it will retire (CoordinateIDCreatesInTx), so
// the lock that orders the sweep against a racing create of one of its ids is
// taken before the delete, not after it: taken after, a create that probed
// before the delete would wait on the deleted row while the sweep waited on
// the create's cell.
func sweepRetiredIDsInTx(ctx context.Context, tx DBTX, kept []*types.Issue) (wisp, durable []RetiredID, err error) {
	ids := make([]string, len(kept))
	closedAt := make(map[string]time.Time, len(kept))
	for i, issue := range kept {
		if issue.ClosedAt == nil {
			return nil, nil, fmt.Errorf("retain ids: sweep candidate %s has no closed_at", issue.ID)
		}
		ids[i] = issue.ID
		closedAt[issue.ID] = *issue.ClosedAt
	}
	if err := CoordinateIDCreatesInTx(ctx, tx, ids); err != nil {
		return nil, nil, fmt.Errorf("retain ids: %w", err)
	}
	wispIDs, durableIDs, err := PartitionWispIDsInTx(ctx, tx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("retain ids: %w", err)
	}
	for _, id := range wispIDs {
		wisp = append(wisp, RetiredID{ID: id, ClosedAt: closedAt[id]})
	}
	for _, id := range durableIDs {
		durable = append(durable, RetiredID{ID: id, ClosedAt: closedAt[id]})
	}
	return wisp, durable, nil
}

// sweepLiveDependentsInTx returns which of the candidates a live row depends
// on through a protecting edge (issueops.SweepRequest.ProtectLiveDependents),
// on the SAME transaction the candidates came off. Edges are read from both
// dependency tables, and the sources' statuses from both planes.
//
// Reading the custom statuses is required for the reason sweepReferencedInTx
// gives: a missed custom active status would under-protect.
func sweepLiveDependentsInTx(ctx context.Context, tx *sql.Tx, candidates []*types.Issue) (map[string]bool, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	ids := make([]string, len(candidates))
	for i, issue := range candidates {
		ids[i] = issue.ID
	}
	incoming, err := GetDependentRecordsForIssuesInTx(ctx, tx, ids)
	if err != nil {
		return nil, fmt.Errorf("reading dependents for live-dependent protection: %w", err)
	}
	sources := workapi.SweepLiveDependentSources(incoming)
	if len(sources) == 0 {
		return nil, nil
	}
	custom, err := ResolveCustomStatusesDetailedInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("reading custom statuses for live-dependent protection: %w", err)
	}
	liveRows, err := SearchIssuesInTx(ctx, tx, "", workapi.BuildSweepLiveDependentScanFilter(sources, custom))
	if err != nil {
		return nil, fmt.Errorf("reading dependent statuses for live-dependent protection: %w", err)
	}
	live := make(map[string]bool, len(liveRows))
	for _, issue := range liveRows {
		if issue != nil {
			live[issue.ID] = true
		}
	}
	return workapi.SweepLiveDependentTargets(incoming, live), nil
}

// sweepReferencedInTx returns which of the candidates are cited by a row that
// is not done, reading the not-done set and its comments on the SAME
// transaction the candidates came off.
//
// Reading the workspace's custom statuses is required rather than best-effort:
// an error aborts the sweep, because a scan that silently missed a custom active
// status would under-protect and delete a bead a live bead still cites
// (issueops.SweepRequest.ProtectReferenced).
func sweepReferencedInTx(ctx context.Context, tx *sql.Tx, candidates []*types.Issue) (map[string]bool, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	candidateIDs := make(map[string]bool, len(candidates))
	for _, issue := range candidates {
		candidateIDs[issue.ID] = true
	}
	matcher := workapi.NewCandidateIDMatcher(candidateIDs)

	custom, err := ResolveCustomStatusesDetailedInTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("reading custom statuses for reference scan: %w", err)
	}
	notDone, err := SearchIssuesInTx(ctx, tx, "", workapi.BuildSweepReferenceScanFilter(custom))
	if err != nil {
		return nil, fmt.Errorf("scanning open beads for references: %w", err)
	}

	notDoneIDs := make([]string, 0, len(notDone))
	for _, issue := range notDone {
		if issue != nil {
			notDoneIDs = append(notDoneIDs, issue.ID)
		}
	}
	comments, err := GetCommentsForIssuesInTx(ctx, tx, notDoneIDs)
	if err != nil {
		return nil, fmt.Errorf("scanning open beads for references: %w", err)
	}

	referenced := make(map[string]bool)
	for _, issue := range notDone {
		if issue == nil {
			continue
		}
		matcher.FindAll(issue.Description, referenced)
		matcher.FindAll(issue.Notes, referenced)
		for _, c := range comments[issue.ID] {
			matcher.FindAll(c.Text, referenced)
		}
	}
	return referenced, nil
}
