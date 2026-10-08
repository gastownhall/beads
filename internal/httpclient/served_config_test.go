//go:build cgo

// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/served_config_test.go@49d1df2f6)
// to OSS beads under the MIT license.

package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The WorkspaceConfig contract against the served surface — the WHOLE tier,
// reads and writes, now that #5596's two verbs are wired.
//
// IT USED TO BE HAND-WRITTEN, and the reason it was is worth recording rather
// than deleting: every case in the shared suite writes through the role, this
// backend's writes refused, so adopting the tier would have parked twenty-one
// cases to run four. What stood in its place asserted the subset a refusing
// backend could reach. Client wave ga-jpywb wires SetSetting and UnsetSetting,
// so the suite is adoptable whole and the stand-in is gone — it asserted a
// strict subset of what runs below, which is the retirement condition a
// stand-in should have.
//
// ONE ENVIRONMENT FOR THE WHOLE SUITE, and here that is a correctness
// requirement rather than a saving, exactly as it is on the two store legs:
// config keys are global to a workspace, the two projected keys are written by
// NAME (their whole point is that those exact names project), and the
// refused-write case takes a history delta. All three need the subtests
// sequential over one plane.
func TestServedWorkspaceConfigContract(t *testing.T) {
	e := newServedEnv(t, "hwcfg")
	ctx := t.Context()
	fixture := newServedWorkspaceConfigFixture(t, e)

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

// newServedWorkspaceConfigFixture binds the ROLE to the client and everything
// else to the reference store, which is this harness's standing rule.
//
// THE VOCABULARY READER IS THE ONE EXCEPTION, and it is the same exception the
// ReadyClaimer fixture makes for its Reader: the three reads it holds are the
// SUBJECT's own (vocabulary.go), so the projection cases compare two of this
// client's surfaces — a write through the role, and a vocabulary read that goes
// back over the wire for the key it wrote — against each other. Binding the
// server's would compare the server to itself and prove nothing about the
// client.
func newServedWorkspaceConfigFixture(t *testing.T, e *servedEnv) conformance.WorkspaceConfigFixture {
	t.Helper()
	settings, err := e.subject.WorkspaceConfig()
	if err != nil {
		t.Fatalf("WorkspaceConfig(): %v", err)
	}
	// The protected-key cases need an issue_prefix to remove and restore, and
	// they need it written PAST the role, which refuses to write it. newServedEnv
	// already seeds one through the reference store; this restates it so the
	// fixture does not depend on the harness's own seeding order.
	if err := e.setConfig(t.Context(), "issue_prefix", e.prefix); err != nil {
		t.Fatalf("seed issue_prefix: %v", err)
	}
	return conformance.WorkspaceConfigFixture{
		IssuePrefix:     e.prefix,
		WorkspaceConfig: settings,
		SetConfig:       e.setConfig,
		QueryScalar:     e.queryScalar,
		CountHistory:    e.countHistory,
		Vocabulary: &conformance.WorkspaceVocabularyReader{
			CustomStatuses: e.subject.GetCustomStatusesDetailed,
			CustomTypes:    e.subject.GetCustomTypes,
			InfraTypes: func(ctx context.Context) (map[string]bool, error) {
				return e.subject.GetInfraTypes(ctx), nil
			},
		},
	}
}
