package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestImporterContract runs the Importer contract against the unit-of-work
// provider, which is the ONLY implementation there is: ImporterSource is the
// one capability accessor in this package with no storage.DoltStorage
// counterpart, so the two store backends serve `bd import` through the raw
// CreateIssuesWithFullOptions seam and have no role for the contract to hold.
// That is stated at length in the contract file's header.
//
// What this wiring is worth is therefore not a third vote on a shared body but
// the ONLY vote on this one. The engine underneath
// (issueops.CreateIssuesInTxWithResult) is shared with the store legs and the
// audit tier already observes it there; the mapping on top — StaleRejectedIDs,
// its dedup, the Created arithmetic, SkippedDependencies — is written here in
// uow/importer.go and nothing anywhere ran it.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a
// real Dolt sql-server) and NO t.Parallel, for the reason the sibling role
// suites give: this backend has no per-test copy-on-write branch.
func TestImporterContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWImporterFixture(t, ctx, "imp")

	t.Run("RejectsAStaleRowAndNamesIt", func(t *testing.T) {
		conformance.RunImporterRejectsAStaleRowAndNamesIt(t, ctx, fixture)
	})
	t.Run("ReportsTheAbsentTargetItDroppedOnce", func(t *testing.T) {
		conformance.RunImporterReportsTheAbsentTargetItDroppedOnce(t, ctx, fixture)
	})
	t.Run("ReportsTheCrossPlaneEdgeItDropped", func(t *testing.T) {
		conformance.RunImporterReportsTheCrossPlaneEdgeItDropped(t, ctx, fixture)
	})
	t.Run("ReportsTheCycleEdgeItDropped", func(t *testing.T) {
		conformance.RunImporterReportsTheCycleEdgeItDropped(t, ctx, fixture)
	})
}

func newUOWImporterFixture(t *testing.T, ctx context.Context, prefix string) conformance.ImporterFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewImporter: a provider that stopped
	// offering the role is the regression, and a constructor call would hide it.
	source, ok := provider.(ImporterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Importer accessor", provider)
	}
	importer, err := source.Importer()
	if err != nil {
		t.Fatalf("Importer(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.ImporterFixture{
		IssuePrefix: kit.IssuePrefix,
		Importer:    importer,
		QueryScalar: kit.QueryScalar,
	}
}
