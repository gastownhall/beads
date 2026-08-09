//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestWorkspaceConfigContract runs the WorkspaceConfig contract against the
// embedded store, which hands back the SAME body the server-backed store does
// (internal/workapi/storeworkspaceconfig), differing only in the engine
// underneath. It is not an independent vote on the body.
//
// One environment for the whole suite, and here that is a CORRECTNESS
// requirement: config keys are global to a workspace, the two projected keys
// are written by name, and the refused-write case takes a history delta — all
// of which need the subtests sequential over one plane.
func TestWorkspaceConfigContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "wcfg")
	ctx := t.Context()
	fixture := newEmbeddedWorkspaceConfigFixture(t, te, "wcfg")

	t.Run("StoresAValueVerbatim", func(t *testing.T) {
		conformance.RunWorkspaceConfigStoresAValueVerbatim(t, ctx, fixture)
	})
	t.Run("ReplacesAnExistingValue", func(t *testing.T) {
		conformance.RunWorkspaceConfigReplacesAnExistingValue(t, ctx, fixture)
	})
	t.Run("ConflatesAnUnsetKeyWithAnEmptyValue", func(t *testing.T) {
		conformance.RunWorkspaceConfigConflatesAnUnsetKeyWithAnEmptyValue(t, ctx, fixture)
	})
	t.Run("ListsEveryStoredSetting", func(t *testing.T) {
		conformance.RunWorkspaceConfigListsEveryStoredSetting(t, ctx, fixture)
	})
	t.Run("ListExcludesTheKVPlane", func(t *testing.T) {
		conformance.RunWorkspaceConfigListExcludesTheKVPlane(t, ctx, fixture)
	})
	t.Run("PointReadRefusesTheKVPlane", func(t *testing.T) {
		conformance.RunWorkspaceConfigPointReadRefusesTheKVPlane(t, ctx, fixture)
	})
	t.Run("UnsetRemovesTheSetting", func(t *testing.T) {
		conformance.RunWorkspaceConfigUnsetRemovesTheSetting(t, ctx, fixture)
	})
	t.Run("UnsetOfAnAbsentKeySucceeds", func(t *testing.T) {
		conformance.RunWorkspaceConfigUnsetOfAnAbsentKeySucceeds(t, ctx, fixture)
	})
	t.Run("RefusesAnEmptyKey", func(t *testing.T) {
		conformance.RunWorkspaceConfigRefusesAnEmptyKey(t, ctx, fixture)
	})
	t.Run("RefusesTheProtectedKeyOnSet", func(t *testing.T) {
		conformance.RunWorkspaceConfigRefusesTheProtectedKeyOnSet(t, ctx, fixture)
	})
	t.Run("UnsetDoesNotRefuseTheProtectedKey", func(t *testing.T) {
		conformance.RunWorkspaceConfigUnsetDoesNotRefuseTheProtectedKey(t, ctx, fixture)
	})
	t.Run("RefusesAnUnparseableCustomStatus", func(t *testing.T) {
		conformance.RunWorkspaceConfigRefusesAnUnparseableCustomStatus(t, ctx, fixture)
	})
	t.Run("ProjectsCustomStatuses", func(t *testing.T) {
		conformance.RunWorkspaceConfigProjectsCustomStatuses(t, ctx, fixture)
	})
	t.Run("ProjectsCustomTypes", func(t *testing.T) {
		conformance.RunWorkspaceConfigProjectsCustomTypes(t, ctx, fixture)
	})
	t.Run("UnsetLeavesTheProjectionBehind", func(t *testing.T) {
		conformance.RunWorkspaceConfigUnsetLeavesTheProjectionBehind(t, ctx, fixture)
	})
	t.Run("ARefusedWriteRecordsNoHistory", func(t *testing.T) {
		conformance.RunWorkspaceConfigARefusedWriteRecordsNoHistory(t, ctx, fixture)
	})
	t.Run("KeysAreCaseSensitive", func(t *testing.T) {
		conformance.RunWorkspaceConfigKeysAreCaseSensitive(t, ctx, fixture)
	})
	t.Run("CustomStatusReadsAreOrderedByName", func(t *testing.T) {
		conformance.RunWorkspaceConfigCustomStatusReadsAreOrderedByName(t, ctx, fixture)
	})
	t.Run("CustomTypeReadsAreOrderedByName", func(t *testing.T) {
		conformance.RunWorkspaceConfigCustomTypeReadsAreOrderedByName(t, ctx, fixture)
	})
	t.Run("ConfiguredInfraTypesReplaceTheDefaultSet", func(t *testing.T) {
		conformance.RunWorkspaceConfigConfiguredInfraTypesReplaceTheDefaultSet(t, ctx, fixture)
	})
	t.Run("UnconfiguredVocabularyReadsAreEmptyNotErrors", func(t *testing.T) {
		conformance.RunWorkspaceConfigUnconfiguredVocabularyReadsAreEmptyNotErrors(t, ctx, fixture)
	})
}

func newEmbeddedWorkspaceConfigFixture(t *testing.T, te *testEnv, prefix string) conformance.WorkspaceConfigFixture {
	t.Helper()
	settings, err := te.store.WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	// The protected-key cases need an issue_prefix to remove and restore, and
	// they need it written PAST the role, which refuses to write it.
	if err := kit.SetConfig(t.Context(), "issue_prefix", prefix); err != nil {
		t.Fatalf("seed issue_prefix: %v", err)
	}
	return conformance.WorkspaceConfigFixture{
		IssuePrefix:     kit.IssuePrefix,
		WorkspaceConfig: settings,
		SetConfig:       kit.SetConfig,
		QueryScalar:     kit.QueryScalar,
		CountHistory:    kit.CountHistory,
		Vocabulary: &conformance.WorkspaceVocabularyReader{
			// The same three reads the server-backed store answers, and the
			// same body behind them: the difference between the two stores is
			// below storage.DoltStorage, not above it.
			CustomStatuses: te.store.GetCustomStatusesDetailed,
			CustomTypes:    te.store.GetCustomTypes,
			InfraTypes: func(ctx context.Context) (map[string]bool, error) {
				return te.store.GetInfraTypes(ctx), nil
			},
		},
	}
}
