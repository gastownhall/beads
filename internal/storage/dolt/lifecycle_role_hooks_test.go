package dolt

import (
	"context"
	"errors"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// This file holds the server-backed store's answers to the OUT-OF-BAND hooks the
// two accessor-reachable Lifecycle contracts declare — the observations a
// backend publishes that the frozen role fixture kit does not carry, built here
// at the wiring site the way CycleDetectorFixture.Exec is.
//
// None of them is raw SQL. That is the point of the contracts they serve: the
// staging contract next door reads its post-state with `SELECT`, so a backend
// with no SQL connection can run none of it, while everything below is a method
// the store already publishes to ordinary callers and an HTTP-client leg answers
// from its own read surface.

// newDoltContractEventLister answers one issue's whole event journal. The limit
// is 0, which GetEvents reads as "no limit": the contracts take a DELTA around
// the operation under test, so a truncated journal would make an assertion about
// what a refusal wrote unfalsifiable.
func newDoltContractEventLister(store *DoltStore) func(context.Context, string) ([]*types.Event, error) {
	return func(ctx context.Context, issueID string) ([]*types.Event, error) {
		return store.GetEvents(ctx, issueID, 0)
	}
}

// newDoltContractDependencyLister answers one issue's outgoing edges as records.
//
// It reads through the store's dependency-with-metadata surface, which resolves
// each target to the issue behind it, so an edge onto an id no plane holds is
// dropped. The contracts that use this hook assert a PARENT set whose every
// member is a row they seeded, so the resolution is invisible to them; a case
// that needed dangling edges would need a different seam.
func newDoltContractDependencyLister(store *DoltStore) func(context.Context, string) ([]*types.Dependency, error) {
	return func(ctx context.Context, issueID string) ([]*types.Dependency, error) {
		records, err := store.GetDependenciesWithMetadata(ctx, issueID)
		if err != nil {
			return nil, err
		}
		edges := make([]*types.Dependency, 0, len(records))
		for _, record := range records {
			if record == nil {
				continue
			}
			edges = append(edges, &types.Dependency{
				IssueID:     issueID,
				DependsOnID: record.ID,
				Type:        record.DependencyType,
			})
		}
		return edges, nil
	}
}

// newDoltContractWispProbe reports whether the EPHEMERAL plane holds a row at
// id. It reads the wisps table alone, which is the whole reason the hook exists:
// the store's both-plane read resolves the durable row first, so a wisp sharing
// an occupied durable id never reaches it.
//
// An absent wisp arrives as ErrNotFound rather than a nil issue, and that is the
// answer the hook exists to give — "no row" is not a failed read.
func newDoltContractWispProbe(store *DoltStore) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, id string) (bool, error) {
		wisp, err := store.getWisp(ctx, id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return false, nil
			}
			return false, err
		}
		return wisp != nil, nil
	}
}
