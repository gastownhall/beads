//go:build cgo

package embeddeddolt_test

import (
	"context"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// This file holds the embedded store's answers to the OUT-OF-BAND hooks the two
// accessor-reachable Lifecycle contracts declare — the observations a backend
// publishes that the frozen role fixture kit does not carry, built here at the
// wiring site the way CycleDetectorFixture.Exec is.
//
// Two of the three go through the store's own published reads. The wisp probe
// does not: this package's tests are EXTERNAL (package embeddeddolt_test), so
// the store's plane-pinned read is not reachable from here, and the probe opens
// the same short-lived SQL connection the fixture kit's QueryScalar opens. That
// is a property of this leg's test packaging rather than of the contract — the
// hook asks one yes/no question about one id, and an HTTP-client leg answers it
// from its own wisp read.

// newEmbeddedContractEventLister answers one issue's whole event journal. The
// limit is 0, which GetEvents reads as "no limit": the contracts take a DELTA
// around the operation under test, so a truncated journal would make an
// assertion about what a refusal wrote unfalsifiable.
func newEmbeddedContractEventLister(store *embeddeddolt.EmbeddedDoltStore) func(context.Context, string) ([]*types.Event, error) {
	return func(ctx context.Context, issueID string) ([]*types.Event, error) {
		return store.GetEvents(ctx, issueID, 0)
	}
}

// newEmbeddedContractDependencyLister answers one issue's outgoing edges as
// records.
//
// It reads through the store's dependency-with-metadata surface, which resolves
// each target to the issue behind it, so an edge onto an id no plane holds is
// dropped. The contracts that use this hook assert a PARENT set whose every
// member is a row they seeded, so the resolution is invisible to them.
func newEmbeddedContractDependencyLister(store *embeddeddolt.EmbeddedDoltStore) func(context.Context, string) ([]*types.Dependency, error) {
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

// newEmbeddedContractWispProbe reports whether the EPHEMERAL plane holds a row
// at id. It reads the wisps table alone, which is the whole reason the hook
// exists: the store's both-plane read resolves the durable row first, so a wisp
// sharing an occupied durable id never reaches it.
func newEmbeddedContractWispProbe(te *testEnv) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, id string) (bool, error) {
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
		if err != nil {
			return false, err
		}
		defer func() { _ = cleanup() }()
		var rows int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&rows); err != nil {
			return false, err
		}
		return rows > 0, nil
	}
}
