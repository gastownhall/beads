package issueops

import (
	"context"
	"database/sql"
	"fmt"

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
func SweepInTx(ctx context.Context, tx *sql.Tx, req publicops.SweepRequest) (publicops.SweepResult, error) {
	result := publicops.SweepResult{DryRun: req.DryRun}

	candidates, err := SearchIssuesInTx(ctx, tx, "", workapi.BuildSweepCandidateFilter(req))
	if err != nil {
		return publicops.SweepResult{}, fmt.Errorf("listing sweep candidates: %w", err)
	}

	kept, skips := workapi.FilterSweepCandidates(candidates, req.IDPattern, req.ClosedBefore)
	result.Skipped = skips

	if req.ProtectReferenced {
		referenced, err := sweepReferencedInTx(ctx, tx, kept)
		if err != nil {
			return publicops.SweepResult{}, err
		}
		var count int
		kept, count, result.ReferencedIDs = workapi.PartitionSweepReferenced(kept, referenced)
		result.Skipped.Referenced = count
	}

	if len(kept) == 0 {
		return result, nil
	}

	ids := make([]string, len(kept))
	for i, issue := range kept {
		ids[i] = issue.ID
	}

	// cascade=false, force=true: a sweep deletes what it selected and ORPHANS
	// dependents outside the selection. The DRY RUN passes the same flags rather
	// than the force=false the direct route used to pass, so a preview cannot
	// fail where the real run would succeed.
	deleted, err := DeleteIssuesInTx(ctx, tx, ids, false, true, req.DryRun)
	if err != nil {
		return publicops.SweepResult{}, err
	}
	result.Swept = deleted.DeletedCount
	result.Dependencies = deleted.DependenciesCount
	result.Labels = deleted.LabelsCount
	result.Events = deleted.EventsCount
	return result, nil
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
