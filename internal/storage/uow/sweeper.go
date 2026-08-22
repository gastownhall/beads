package uow

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// SweeperSource is the capability accessor a unit-of-work provider offers for
// the bulk-clearance role.
type SweeperSource interface {
	Sweeper() (publicops.Sweeper, error)
}

// sweeper clears closed rows from one tier through a unit of work.
type sweeper struct {
	provider UnitOfWorkProvider
}

// Sweeper returns the guarded bulk-clearance surface for this provider.
func (p *doltSQLProvider) Sweeper() (publicops.Sweeper, error) {
	return NewSweeper(p)
}

// NewSweeper constructs a public sweeper backed by provider.
func NewSweeper(provider UnitOfWorkProvider) (publicops.Sweeper, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new sweeper: unit-of-work provider must not be nil")
	}
	return &sweeper{provider: provider}, nil
}

var _ publicops.Sweeper = (*sweeper)(nil)

// Sweep clears the request's tier inside ONE unit of work.
//
// This is the genuinely separate body: the two store backends share
// issueops.SweepInTx, and this one reaches the same questions through the domain
// use cases. What it must NOT do differently is WHICH ROWS GO — the selection,
// the pattern, the pinned and closed_at rechecks and the citation rule all run
// through the same internal/workapi functions the shared body runs.
//
// ONE UNIT OF WORK matters more here than it does for a read. The candidate
// query and the delete are on the same transaction, so a row closed, unpinned
// or cited between them cannot change what is deleted — the promise
// issueops.Sweeper.Sweep makes.
//
// A DRY RUN TAKES A READ-ONLY UNIT OF WORK: it writes nothing, so it must not
// take the committing path and leave a history entry describing a preview.
func (s *sweeper) Sweep(ctx context.Context, req publicops.SweepRequest) (publicops.SweepResult, error) {
	if err := workapi.ValidateSweepRequest(req); err != nil {
		return publicops.SweepResult{}, err
	}
	if req.DryRun {
		return RunTxRead(ctx, s.provider, func(ctx context.Context, uw UnitOfWork) (publicops.SweepResult, error) {
			return sweepInUOW(ctx, uw, req)
		})
	}
	return RunTxResult(ctx, s.provider, func(ctx context.Context, uw UnitOfWork) (publicops.SweepResult, string, error) {
		result, err := sweepInUOW(ctx, uw, req)
		if err != nil || result.Swept == 0 {
			// A sweep that deleted nothing labels nothing: the role promises
			// at most one history entry per call and none for a no-op.
			return result, "", err
		}
		return result, fmt.Sprintf("bd: sweep %d %s bead(s)", result.Swept, req.Tier), nil
	})
}

// sweepInUOW is the whole sweep on one unit of work, shared by the preview
// path and the committing one so the two cannot answer differently.
func sweepInUOW(ctx context.Context, uw UnitOfWork, req publicops.SweepRequest) (publicops.SweepResult, error) {
	result := publicops.SweepResult{DryRun: req.DryRun}

	page, err := uw.IssueUseCase().SearchIssues(ctx, "", workapi.BuildSweepCandidateFilter(req))
	if err != nil {
		return publicops.SweepResult{}, fmt.Errorf("listing sweep candidates: %w", err)
	}

	kept, skips := workapi.FilterSweepCandidates(page.Items, req.IDPattern, req.ClosedBefore)
	result.Skipped = skips

	if req.ProtectReferenced {
		referenced, err := sweepReferencedInUOW(ctx, uw, kept)
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
	deleted, err := uw.IssueUseCase().DeleteIssues(ctx, domain.DeleteIssuesParams{
		IDs:    ids,
		DryRun: req.DryRun,
	}, req.Actor)
	if err != nil {
		return publicops.SweepResult{}, err
	}
	result.Swept = deleted.DeletedCount
	result.Dependencies = deleted.DependenciesCount
	result.Labels = deleted.LabelsCount
	result.Events = deleted.EventsCount
	return result, nil
}

// sweepReferencedInUOW returns which of the candidates a not-done row cites,
// reading the not-done set and its comments on the same unit of work the
// candidates came off. Comments are read for the whole not-done set in ONE
// batch rather than one query per row.
func sweepReferencedInUOW(ctx context.Context, uw UnitOfWork, candidates []*types.Issue) (map[string]bool, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	candidateIDs := make(map[string]bool, len(candidates))
	for _, issue := range candidates {
		candidateIDs[issue.ID] = true
	}
	matcher := workapi.NewCandidateIDMatcher(candidateIDs)

	custom, err := uw.ConfigUseCase().GetCustomStatuses(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading custom statuses for reference scan: %w", err)
	}
	page, err := uw.IssueUseCase().SearchIssues(ctx, "", workapi.BuildSweepReferenceScanFilter(custom))
	if err != nil {
		return nil, fmt.Errorf("scanning open beads for references: %w", err)
	}
	notDone := page.Items

	notDoneIDs := make([]string, 0, len(notDone))
	for _, issue := range notDone {
		if issue != nil {
			notDoneIDs = append(notDoneIDs, issue.ID)
		}
	}
	// BOTH PLANES, and this is not an optimization detail. The not-done set
	// comes from SearchIssues, which merges the durable and wisp planes, so it
	// contains wisps — and their comments live in wisp_comments, which
	// GetCommentsForIssues does not read. Scanning only the durable table left
	// a closed bead cited solely by a comment on an open WISP unprotected, so
	// `bd prune` deleted it on this route and kept it on the other. The
	// contract says an implementation that cannot read the full set must fail
	// the sweep rather than under-scan it.
	//
	// The full id list goes to both reads rather than being partitioned by a
	// plane flag: an id lives in exactly one plane, so at most one side answers
	// for it, and a mis-partition here would silently under-scan again.
	comments, err := uw.CommentUseCase().GetCommentsForIssues(ctx, notDoneIDs)
	if err != nil {
		return nil, fmt.Errorf("scanning open beads for references: %w", err)
	}
	wispComments, err := uw.CommentUseCase().GetCommentsForWisps(ctx, notDoneIDs)
	if err != nil {
		return nil, fmt.Errorf("scanning open wisps for references: %w", err)
	}
	for id, cs := range wispComments {
		comments[id] = append(comments[id], cs...)
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
