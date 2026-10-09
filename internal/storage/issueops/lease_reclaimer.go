package issueops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

// ValidateReclaimRequest applies the request rules every LeaseReclaimer
// implementation shares.
//
// THE CAP IS CHECKED AGAINST Filter.IDs AS SENT, before deduplication, for
// ValidateGetManyRequest's reason: the obligation it bounds is reading the
// request apart, not reading the rows it resolves to.
func ValidateReclaimRequest(request publicops.ReclaimRequest) error {
	if strings.TrimSpace(request.Actor) == "" {
		return fmt.Errorf("%w: reclaim actor is required", storage.ErrValidation)
	}
	if request.OlderThan < 0 {
		return fmt.Errorf("%w: reclaim older_than must not be negative", storage.ErrValidation)
	}
	if len(request.Filter.IDs) > publicops.MaxReclaimIDs {
		return &publicops.TooManyReclaimIDsError{Requested: len(request.Filter.IDs), Cap: publicops.MaxReclaimIDs}
	}
	for i, id := range request.Filter.IDs {
		if id == "" {
			return fmt.Errorf("%w: reclaim scope id at position %d is empty", storage.ErrValidation, i)
		}
	}
	return nil
}

// ExecuteReclaimInTx sweeps stale leases from tx, in ONE transaction. It is
// the body behind the LeaseReclaimer accessor on ALL THREE legs: the two
// stores wrap it in their own retrying write transaction, and the
// unit-of-work provider reaches it through the domain repository, whose
// runner publishes exactly the DBTX method set ReclaimExpiredLeasesInTx
// takes.
//
// VALIDATION HAPPENS HERE, for ExecuteGetMany's reason: this body is the only
// body, so a leg that forgot to validate would be answering a different
// contract. It runs before the cutoff is computed and before the sweep's
// query, so a request that fails validation never reaches storage.
//
// THE CUTOFF IS COMPUTED HERE, once, from time.Now().UTC().Add(-OlderThan), so
// every leg sweeps against the same clock read rather than each leg taking its
// own — the dolt and embedded-dolt wrappers and the unit-of-work repository
// all call this function with the request, never with a precomputed cutoff.
func ExecuteReclaimInTx(ctx context.Context, tx DBTX, request publicops.ReclaimRequest) (publicops.ReclaimResult, error) {
	if err := ValidateReclaimRequest(request); err != nil {
		return publicops.ReclaimResult{}, err
	}
	cutoff := time.Now().UTC().Add(-request.OlderThan)
	reclaimed, err := ReclaimExpiredLeasesInTx(ctx, tx, cutoff, request.Filter, request.Actor)
	if err != nil {
		return publicops.ReclaimResult{}, fmt.Errorf("reclaim: %w", err)
	}
	if reclaimed == nil {
		reclaimed = []publicops.ReclaimedLease{}
	}
	return publicops.ReclaimResult{Reclaimed: reclaimed}, nil
}

// ReclaimVersionCommit composes the version-control entry for a finished sweep:
// the durable tables it changed and the message to record them under. A sweep
// that reverted nothing composes neither, which every leg's commit funnel reads
// as "write nothing to history", so a no-op reaper tick leaves no entry
// describing a no-op.
//
// It is ONE helper the three legs share so the message cannot drift between
// them: the same "bd: reclaim N expired lease(s)" the raw ReclaimExpiredLeases
// has always recorded. The staged set is the one the release records, issues
// and events, because a reclaim is the same revert applied by a clock.
func ReclaimVersionCommit(result publicops.ReclaimResult) (ChangedTables, string) {
	if len(result.Reclaimed) == 0 {
		return nil, ""
	}
	tables := ChangedTables{}
	tables.Add("issues", "events")
	return tables, fmt.Sprintf("bd: reclaim %d expired lease(s)", len(result.Reclaimed))
}
