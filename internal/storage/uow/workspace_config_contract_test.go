package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestWorkspaceConfigContract runs the WorkspaceConfig contract against the
// unit-of-work provider — the one implementation that does not hand back
// internal/workapi/storeworkspaceconfig. The two store backends share that
// body, which makes this the SECOND of two votes rather than the third.
//
// It is also the wiring the projection cases were written for: this backend
// stored status.custom and types.custom without rewriting the tables that reads
// consult first, so a proxied `bd config set types.custom` reported success and
// never took effect.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a real
// Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so the config plane and dolt_log are database-global.
func TestWorkspaceConfigContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWWorkspaceConfigFixture(t, ctx, "wcfg")

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

func newUOWWorkspaceConfigFixture(t *testing.T, ctx context.Context, prefix string) conformance.WorkspaceConfigFixture {
	t.Helper()
	// newUOWRoleFixtureProvider already seeds issue_prefix past the role, which
	// is what the protected-key cases need to remove and restore.
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	// Through the capability accessor, not NewWorkspaceConfig: a provider that
	// stopped offering the role is the regression a constructor call would hide.
	source, ok := provider.(WorkspaceConfigSource)
	if !ok {
		t.Fatalf("provider %T does not offer the WorkspaceConfig accessor", provider)
	}
	settings, err := source.WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.WorkspaceConfigFixture{
		IssuePrefix:     kit.IssuePrefix,
		WorkspaceConfig: settings,
		SetConfig:       kit.SetConfig,
		QueryScalar:     kit.QueryScalar,
		CountHistory:    kit.CountHistory,
	}
}
