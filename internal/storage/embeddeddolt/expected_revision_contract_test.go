//go:build cgo

package embeddeddolt_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// TestExpectedRevisionContract runs the R16/R17 expected-revision contract
// against the embedded-Dolt-backed store, which reaches
// internal/storage/issueops.CompareAndSetVersionInTx through this leg's own
// issue-operation transaction (EmbeddedDoltStore.CompareAndSetVersion) or a
// read-only connection (EmbeddedDoltStore.CurrentVersion) — see
// internal/storage/embeddeddolt/expected_revision_cas.go.
//
// All three legs run that one shared body, so this is not an independent
// vote on the design — it is the check on THIS leg's wrapper and this file's
// own "_attribution"/error-sentinel translation. See the contract file's
// header comment.
func TestExpectedRevisionContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "erev")
	ctx := t.Context()
	fixture := conformance.ExpectedRevisionFixture{
		IssuePrefix:                   "erev",
		CompareAndSetVersion:          expectedRevisionEmbeddedCompareAndSetVersion(te.store),
		CurrentVersion:                expectedRevisionEmbeddedCurrentVersion(te.store),
		MutateOutsideExpectedRevision: expectedRevisionEmbeddedMutateOutside(te),
	}

	t.Run("AcceptsAWriteNamingTheCurrentVersion", func(t *testing.T) {
		conformance.RunExpectedRevisionAcceptsAWriteNamingTheCurrentVersion(t, ctx, fixture)
	})
	t.Run("AcceptsAWriteNamingNoVersion", func(t *testing.T) {
		conformance.RunExpectedRevisionAcceptsAWriteNamingNoVersion(t, ctx, fixture)
	})
	t.Run("CoversFieldsOutsideAnyWatchedSubset", func(t *testing.T) {
		conformance.RunExpectedRevisionCoversFieldsOutsideAnyWatchedSubset(t, ctx, fixture)
	})
	t.Run("RefusalReportsTheRefusingVersionAddress", func(t *testing.T) {
		conformance.RunRefusalReportsTheRefusingVersionAddress(t, ctx, fixture)
	})
	t.Run("RefusalReportsTheRefusingVersionsChangeAttribution", func(t *testing.T) {
		conformance.RunRefusalReportsTheRefusingVersionsChangeAttribution(t, ctx, fixture)
	})
	t.Run("RefusalIsATypedOutcomeNotAGenericError", func(t *testing.T) {
		conformance.RunRefusalIsATypedOutcomeNotAGenericError(t, ctx, fixture)
	})
	t.Run("RefusalIsDistinguishableFromAnAcceptedWrite", func(t *testing.T) {
		conformance.RunRefusalIsDistinguishableFromAnAcceptedWrite(t, ctx, fixture)
	})
	t.Run("RefusalIsDistinguishableFromNotFound", func(t *testing.T) {
		conformance.RunRefusalIsDistinguishableFromNotFound(t, ctx, fixture)
	})
	t.Run("RefusalIsDistinguishableFromValidationFailure", func(t *testing.T) {
		conformance.RunRefusalIsDistinguishableFromValidationFailure(t, ctx, fixture)
	})
	t.Run("RefusalNeverSilentlyPicksAWinner", func(t *testing.T) {
		conformance.RunRefusalNeverSilentlyPicksAWinner(t, ctx, fixture)
	})
	t.Run("FixtureIsAnHonestSkipPendingPartB", func(t *testing.T) {
		if fixture.CompareAndSetVersion != nil {
			t.Error("CompareAndSetVersion is wired, want nil: be-80f4a.1 removes R16's per-record CAS backing (architect-ruled design departure from gastownhall/beads#5898) and this leg's fixture must honestly skip pending Part B, not fake green")
		}
		if fixture.CurrentVersion != nil {
			t.Error("CurrentVersion is wired, want nil: see CompareAndSetVersion above")
		}
		if fixture.MutateOutsideExpectedRevision != nil {
			t.Error("MutateOutsideExpectedRevision is wired, want nil: see CompareAndSetVersion above")
		}
	})
}

func expectedRevisionEmbeddedCompareAndSetVersion(store *embeddeddolt.EmbeddedDoltStore) conformance.PerRecordCASWrite {
	return func(ctx context.Context, id string, expected *conformance.Address, patch map[string]any) (conformance.ExpectedRevisionResult, error) {
		applicationPatch, attribution := expectedRevisionSplitAttribution(patch)
		plan := storage.CompareAndSetVersionPlan{
			ID:          id,
			Expected:    expectedRevisionAddressPtr(expected),
			Patch:       applicationPatch,
			Attribution: attribution,
		}
		result, err := store.CompareAndSetVersion(ctx, plan)
		if err != nil {
			return conformance.ExpectedRevisionResult{}, expectedRevisionTranslateErr(err)
		}
		return expectedRevisionResultToConformance(result), nil
	}
}

func expectedRevisionEmbeddedCurrentVersion(store *embeddeddolt.EmbeddedDoltStore) func(ctx context.Context, id string) (conformance.Address, error) {
	return func(ctx context.Context, id string) (conformance.Address, error) {
		address, err := store.CurrentVersion(ctx, id)
		if err != nil {
			return "", expectedRevisionTranslateErr(err)
		}
		return conformance.Address(address), nil
	}
}

// expectedRevisionEmbeddedMutateOutside writes through storeops directly
// against a raw connection, the same "reach past the store's own API" idiom
// testEnv's own exec/queryScalar helpers already use from this external test
// package: R16-c's whole-of-state probe needs a path that is NOT gated by
// the compare-expected check (backend/conformance's own doc comment), no
// such path belongs on the production API surface, and this package cannot
// reach the store's unexported withConn/transaction machinery from outside
// it. embeddeddolt.OpenSQL's *sql.DB satisfies issueops.DBTX directly, so no
// explicit transaction is needed; the write is autocommitted per statement,
// same as testEnv's own helpers, and durably visible to the next OpenSQL
// connection.
func expectedRevisionEmbeddedMutateOutside(te *testEnv) func(ctx context.Context, id string, field, value string) error {
	return func(ctx context.Context, id string, field, value string) error {
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
		if err != nil {
			return err
		}
		defer func() { _ = cleanup() }()
		return storeops.MutateExpectedRevisionFieldInTx(ctx, db, id, field, value)
	}
}

// expectedRevisionSplitAttribution separates the "_attribution" convention
// key (expectedRevisionAttributionPatch, backend/conformance) from the
// application fields a whole-of-state patch actually persists: attribution
// is tracked in expected_revision_records' own change_actor/change_agent/
// change_message/change_at_nanos columns, never as a literal durable_state
// field. The caller's map is never mutated.
func expectedRevisionSplitAttribution(patch map[string]any) (map[string]any, storage.ExpectedRevisionAttribution) {
	applicationPatch := make(map[string]any, len(patch))
	var attribution storage.ExpectedRevisionAttribution
	for k, v := range patch {
		if k == "_attribution" {
			if a, ok := v.(conformance.ChangeAttribution); ok {
				attribution = storage.ExpectedRevisionAttribution{
					Actor:   a.Actor,
					Agent:   a.Agent,
					Message: a.Message,
					At:      a.At,
				}
			}
			continue
		}
		applicationPatch[k] = v
	}
	return applicationPatch, attribution
}

func expectedRevisionAddressPtr(a *conformance.Address) *string {
	if a == nil {
		return nil
	}
	s := string(*a)
	return &s
}

func expectedRevisionResultToConformance(r storage.CompareAndSetVersionResult) conformance.ExpectedRevisionResult {
	result := conformance.ExpectedRevisionResult{
		Accepted:   r.Accepted,
		NewVersion: conformance.Address(r.NewVersion),
	}
	if r.Refusal != nil {
		result.Refusal = &conformance.Refusal{
			RefusingVersion: conformance.Address(r.Refusal.RefusingVersion),
			Attribution: conformance.ChangeAttribution{
				Actor:   r.Refusal.Attribution.Actor,
				Agent:   r.Refusal.Attribution.Agent,
				Message: r.Refusal.Attribution.Message,
				At:      r.Refusal.Attribution.At,
			},
		}
	}
	return result
}

// expectedRevisionTranslateErr re-wraps this leg's storeops sentinels as the
// conformance package's — structurally distinct errors.New values with
// matching semantics, so a bare pass-through would fail the suite's
// errors.Is checks (RunRefusalIsDistinguishableFromNotFound,
// RunRefusalIsDistinguishableFromValidationFailure).
func expectedRevisionTranslateErr(err error) error {
	switch {
	case errors.Is(err, storeops.ErrExpectedRevisionNotFound):
		return fmt.Errorf("%w: %v", conformance.ErrRevisionNotFound, err)
	case errors.Is(err, storeops.ErrExpectedRevisionValidation):
		return fmt.Errorf("%w: %v", conformance.ErrRevisionValidation, err)
	default:
		return err
	}
}
