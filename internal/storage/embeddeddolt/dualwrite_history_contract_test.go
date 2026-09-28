//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestDualWriteContract runs the dual-write history contract against the
// embedded store, which reaches issueops mutation entrypoints through its own
// per-operation connection. All three legs share RecordVersionInTx's one
// body, so this is an ENGINE CHECK rather than an independent vote — what it
// can actually catch here is a wrapper that loses the transaction the
// version row must land in, or a backend that stops implementing the seam
// at all.
func TestDualWriteContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "dwc")
	ctx := t.Context()
	fixture := newEmbeddedDualWriteFixture(t, te, "dwc", true)

	t.Run("MintsOneVersionRowPerAcceptedMutation", func(t *testing.T) {
		conformance.RunDualWriteMintsOneVersionRowPerAcceptedMutation(t, ctx, fixture)
	})
	t.Run("NoOpMutationMintsNoRow", func(t *testing.T) {
		conformance.RunDualWriteNoOpMutationMintsNoRow(t, ctx, fixture)
	})
	t.Run("AttributionIsRecordedWithTheMutation", func(t *testing.T) {
		conformance.RunDualWriteAttributionIsRecordedWithTheMutation(t, ctx, fixture)
	})
	t.Run("CurrentRevisionMatchesTheNewVersionRow", func(t *testing.T) {
		conformance.RunDualWriteCurrentRevisionMatchesTheNewVersionRow(t, ctx, fixture)
	})
	t.Run("NoOpMutationLeavesThePriorVersionRowUnperturbed", func(t *testing.T) {
		conformance.RunDualWriteNoOpMutationLeavesThePriorVersionRowUnperturbed(t, ctx, fixture)
	})
}

// TestDualWriteContractFlagOff runs FR-7 against an environment constructed
// with the flag OFF from the start — its own environment, not a shared one,
// so "flag off" means what FR-7 promises rather than "not yet turned on this
// session".
func TestDualWriteContractFlagOff(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "dwcoff")
	ctx := t.Context()
	fixture := newEmbeddedDualWriteFixture(t, te, "dwcoff", false)

	t.Run("FlagOffProducesNoVersionRows", func(t *testing.T) {
		conformance.RunDualWriteFlagOffProducesNoVersionRows(t, ctx, fixture)
	})
}

// TestDualWriteFixtureKitIsWired is this leg's half of the explicit per-leg
// guardrail design §8.5 calls for in place of AST auto-discovery — see the
// dolt leg's TestDualWriteFixtureKitIsWired for why the guardrail is needed
// even though TestEveryLegWiresEveryRoleContract's scan already enumerates
// and confirms wiring for all six RunDualWriteXxx entrypoints.
func TestDualWriteFixtureKitIsWired(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "dwk")
	fixture := newEmbeddedDualWriteFixture(t, te, "dwk", true)

	if fixture.Mutate == nil {
		t.Error("DualWriteFixture.Mutate is nil")
	}
	if fixture.MutateAsNoOp == nil {
		t.Error("DualWriteFixture.MutateAsNoOp is nil")
	}
	if fixture.CurrentRevision == nil {
		t.Error("DualWriteFixture.CurrentRevision is nil")
	}
	if fixture.VersionRowCount == nil {
		t.Error("DualWriteFixture.VersionRowCount is nil")
	}
	if fixture.LatestVersionAttribution == nil {
		t.Error("DualWriteFixture.LatestVersionAttribution is nil")
	}
}

func newEmbeddedDualWriteFixture(t *testing.T, te *testEnv, prefix string, enabled bool) conformance.DualWriteFixture {
	t.Helper()
	store := te.store
	// Through the type assertion `bd serve` makes, never the concrete method
	// set: dual-write history is not on storage.DoltStorage, so publishing it
	// IS implementing this interface, matching newEmbeddedJournalFixture's own
	// discipline for storage.EventsJournalCursor above.
	configurer, ok := any(store).(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", store)
	}
	configurer.SetVersionedHistoryEnabled(enabled)

	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.DualWriteFixture{
		IssuePrefix: prefix,
		Mutate: func(ctx context.Context, id string) error {
			return store.CreateIssue(ctx, &types.Issue{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
			}, "actor")
		},
		MutateAsNoOp: func(ctx context.Context, id string) error {
			// Re-sets title to the exact value Mutate already gave it, so
			// issueops.DiscardNoopIssueUpdates discards it before it ever
			// reaches RecordVersionInTx.
			return store.UpdateIssue(ctx, id, map[string]any{"title": "t-" + id}, "actor")
		},
		CurrentRevision: func(ctx context.Context, id string) (int64, error) {
			var revision int64
			err := kit.QueryScalar(ctx, "SELECT current_revision FROM issues WHERE id = ?", []any{id}, &revision)
			return revision, err
		},
		VersionRowCount: func(ctx context.Context, id string) (int, error) {
			var count int
			err := kit.QueryScalar(ctx, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", []any{id}, &count)
			return count, err
		},
		LatestVersionAttribution: func(ctx context.Context, id string) (actor, agent, message string, err error) {
			err = kit.QueryScalar(ctx, `
				SELECT COALESCE(change_actor, ''), COALESCE(change_agent, ''), COALESCE(change_message, '')
				FROM issue_versions
				WHERE issue_id = ?
				ORDER BY revision DESC
				LIMIT 1`, []any{id}, &actor, &agent, &message)
			return actor, agent, message, err
		},
	}
}
