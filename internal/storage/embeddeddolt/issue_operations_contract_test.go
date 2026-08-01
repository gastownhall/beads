//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/conformance"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

func TestEmbeddedIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "infra")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateRoutesInfraTypesToWisps(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "infra"))
}

func TestEmbeddedIssueOperationsCreateRejectsMissingDependencyTargets(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "skipdep")
	ctx := t.Context()
	conformance.RunIssueOperationsCreateRejectsMissingDependencyTargets(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "skipdep"))
}

func TestEmbeddedIssueOperationsUpdateFoldsMetadataIntoOneEvent(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "metaevent")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateFoldsMetadataIntoOneEvent(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "metaevent"))
}

func TestEmbeddedIssueOperationsUpdateClosePolicy(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "closepol")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateClosePolicy(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "closepol"))
}

func TestEmbeddedIssueOperationsUpdateAssigneeTransferFence(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "xferfence")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateAssigneeTransferFence(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "xferfence"))
}

func TestEmbeddedIssueOperationsUpdateClosedFieldsMatchClose(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "closedfields")
	ctx := t.Context()
	conformance.RunIssueOperationsUpdateClosedFieldsMatchClose(t, ctx, newEmbeddedIssueOperationsFixture(t, ctx, te, "closedfields"))
}

func newEmbeddedIssueOperationsFixture(t *testing.T, ctx context.Context, te *testEnv, prefix string) conformance.IssueOperationsStagingFixture {
	t.Helper()
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	return conformance.IssueOperationsStagingFixture{
		IssuePrefix: prefix,
		Operations:  operations,
		CreateIssue: te.store.CreateIssue,
		SetConfig:   te.store.SetConfig,
		UpdateRaw:   te.store.UpdateIssue,
		QueryScalar: func(ctx context.Context, query string, args []any, dest ...any) error {
			te.queryScalar(t, ctx, query, args, dest...)
			return nil
		},
	}
}
