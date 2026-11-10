//go:build cgo

package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The DualWriteFixture contracts, run through client → in-process bd serve →
// reference store, the same shape as the other served fixtures in this
// package.
//
// Mutate and MutateAsNoOp are bound to the CLIENT'S Lifecycle role (Create and
// Update over the wire), not to the reference store: the contract is a claim
// about what a client-driven mutation causes the SERVER to record, and a
// fixture that seeded through the reference store would only prove the
// storage engine versions its own direct writes, which the dolt/embeddeddolt/
// uow legs already cover. CurrentRevision, VersionRowCount and
// LatestVersionAttribution read the REFERENCE store's raw columns, the same
// as every other served fixture's postcondition hooks (served_issue_operations_test.go's
// doc comment states the rule this follows: a postcondition reads the row
// directly, never through a role, so a column the read side does not project
// still gets checked).
//
// Each fixture gets its OWN env (newServedEnv), never the shared
// composition: storage.VersionedHistoryConfigurer is a knob on the reference
// store instance the server's roles are bound to, and flipping it on a store
// any other test shares would start minting issue_versions rows underneath
// cases that never asked for them.

func newServedDualWriteFixture(t *testing.T, prefix string, enabled bool) conformance.DualWriteFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	configurer, ok := any(env.reference).(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", env.reference)
	}
	configurer.SetVersionedHistoryEnabled(enabled)

	lifecycle, err := env.subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	return conformance.DualWriteFixture{
		IssuePrefix: env.prefix,
		Mutate: func(ctx context.Context, id string) error {
			_, err := lifecycle.Create(ctx, issueops.CreateRequest{
				Actor:         "writer",
				ForceIDPrefix: true,
				Issue: &types.Issue{
					ID: id, Title: "t-" + id, Status: types.StatusOpen,
					Priority: 1, IssueType: types.TypeTask,
				},
			})
			return err
		},
		// Re-sets Title to the exact value Mutate already gave it, so
		// issueops.DiscardNoopIssueUpdates discards it before it ever reaches
		// RecordVersionInTx — the same shape the uow leg's own
		// newUOWDualWriteFixture uses for the identical reason.
		MutateAsNoOp: func(ctx context.Context, id string) error {
			_, err := lifecycle.Update(ctx, issueops.UpdateRequest{
				Actor:   "writer",
				IssueID: id,
				Patch:   issueops.IssuePatch{Title: set("t-" + id)},
			})
			return err
		},
		CurrentRevision: func(ctx context.Context, id string) (int64, error) {
			var revision int64
			err := env.queryScalar(ctx, "SELECT current_revision FROM issues WHERE id = ?", []any{id}, &revision)
			return revision, err
		},
		VersionRowCount: func(ctx context.Context, id string) (int, error) {
			var count int
			err := env.queryScalar(ctx, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", []any{id}, &count)
			return count, err
		},
		LatestVersionAttribution: func(ctx context.Context, id string) (actor, agent, message string, err error) {
			err = env.queryScalar(ctx, `
				SELECT COALESCE(change_actor, ''), COALESCE(change_agent, ''), COALESCE(change_message, '')
				FROM issue_versions
				WHERE issue_id = ?
				ORDER BY revision DESC
				LIMIT 1`, []any{id}, &actor, &agent, &message)
			return actor, agent, message, err
		},
	}
}

func TestServedDualWriteContract(t *testing.T) {
	ctx := t.Context()
	fixture := newServedDualWriteFixture(t, "hdw", true)

	t.Run("MintsOneVersionRowPerAcceptedMutation", func(t *testing.T) {
		conformance.RunDualWriteMintsOneVersionRowPerAcceptedMutation(t, ctx, fixture)
	})
	t.Run("NoOpMutationMintsNoRow", func(t *testing.T) {
		conformance.RunDualWriteNoOpMutationMintsNoRow(t, ctx, fixture)
	})
	t.Run("AttributionIsRecordedWithTheMutation", func(t *testing.T) {
		conformance.RunDualWriteAttributionIsRecordedWithTheMutation(t, ctx, fixture)

		// The shared contract above only asserts change_actor is non-empty,
		// since each leg's Mutate closure supplies a different literal. This
		// fixture's Mutate always sends Actor: "writer" (see
		// newServedDualWriteFixture below), so the http leg can assert the
		// exact value rather than merely "something was recorded".
		id := fixture.IssuePrefix + "-attribution-exact"
		if err := fixture.Mutate(ctx, id); err != nil {
			t.Fatalf("mutating %s: %v", id, err)
		}
		actor, _, _, err := fixture.LatestVersionAttribution(ctx, id)
		if err != nil {
			t.Fatalf("reading attribution for %s: %v", id, err)
		}
		if actor != "writer" {
			t.Errorf("change_actor = %q, want exactly %q: this fixture's Mutate always sends Actor: %q", actor, "writer", "writer")
		}
	})
	t.Run("CurrentRevisionMatchesTheNewVersionRow", func(t *testing.T) {
		conformance.RunDualWriteCurrentRevisionMatchesTheNewVersionRow(t, ctx, fixture)
	})
	t.Run("NoOpMutationLeavesThePriorVersionRowUnperturbed", func(t *testing.T) {
		conformance.RunDualWriteNoOpMutationLeavesThePriorVersionRowUnperturbed(t, ctx, fixture)
	})
}

// TestServedDualWriteContractFlagOff runs FR-7 against an env whose reference
// store has the flag OFF from the start — its own env, not the one above, so
// "flag off" means what FR-7 promises rather than "not yet turned on this
// session".
func TestServedDualWriteContractFlagOff(t *testing.T) {
	ctx := t.Context()
	fixture := newServedDualWriteFixture(t, "hdwoff", false)

	t.Run("FlagOffProducesNoVersionRows", func(t *testing.T) {
		conformance.RunDualWriteFlagOffProducesNoVersionRows(t, ctx, fixture)
	})
}
