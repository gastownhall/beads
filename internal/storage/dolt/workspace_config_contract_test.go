package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestWorkspaceConfigContract runs the WorkspaceConfig contract against the
// server-backed store.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch. Config keys are GLOBAL to a workspace, so the
// cases namespace their probe keys under the fixture prefix and the two
// projected keys (status.custom, types.custom) are written by name and asserted
// exactly. The refused-write case takes a history delta, which is only
// meaningful while the subtests run sequentially. setupTestStore already marks
// the PARENT parallel; no subtest here calls t.Parallel.
func TestWorkspaceConfigContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltWorkspaceConfigFixture(t, "wcfg")
	defer cleanup()

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
}

func newDoltWorkspaceConfigFixture(t *testing.T, prefix string) (conformance.WorkspaceConfigFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	settings, err := store.WorkspaceConfig()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	// The protected-key cases need an issue_prefix to remove and restore,
	// written PAST the role, which refuses to write it.
	if err := kit.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("seed issue_prefix: %v", err)
	}
	fixture := conformance.WorkspaceConfigFixture{
		IssuePrefix:     kit.IssuePrefix,
		WorkspaceConfig: settings,
		SetConfig:       kit.SetConfig,
		QueryScalar:     kit.QueryScalar,
		CountHistory:    kit.CountHistory,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
