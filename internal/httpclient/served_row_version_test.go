//go:build cgo

package httpclient

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The revision-token matrix, run through client → in-process bd serve →
// reference store.
//
// Every SUBJECT is the client's own role, so each mutation is the wire
// operation a v0 caller sends. Every SEED goes through the reference store,
// per the harness's binding rule.
//
// THE READ IS THE ONE DEPARTURE, AND IT IS DELIBERATE: the token is read back
// through the CLIENT's Reader.Get, so what the matrix compares is the wire's
// `revision` — the value a v0 caller composes expected_version from — not the
// reference store's row. That is not the client agreeing with itself: the
// ruled answers are constants in the table, not anything the client derived,
// and the reader is a different operation from every write under test. And so
// a wire that misreported the token cannot hide behind a correct store, every
// read is cross-checked against the reference store's own RowVersion.
//
// Nothing is parked: every member these rows send is on the v0 wire.
func newServedRowVersionFixture(t *testing.T, prefix string) conformance.RowVersionFixture {
	t.Helper()
	env := newServedEnv(t, prefix)
	subject := env.subject
	lifecycle, err := subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	cas, err := subject.MetadataCAS()
	if err != nil {
		t.Fatalf("MetadataCAS(): %v", err)
	}
	claimer, err := subject.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	releaser, err := subject.Releaser()
	if err != nil {
		t.Fatalf("Releaser(): %v", err)
	}
	heartbeater, err := subject.Heartbeater()
	if err != nil {
		t.Fatalf("Heartbeater(): %v", err)
	}
	editor, err := subject.DependencyEditor()
	if err != nil {
		t.Fatalf("DependencyEditor(): %v", err)
	}
	commenter, err := subject.Commenter()
	if err != nil {
		t.Fatalf("Commenter(): %v", err)
	}
	applier, err := subject.BatchApplier()
	if err != nil {
		t.Fatalf("BatchApplier(): %v", err)
	}
	reader, err := subject.IssueReader()
	if err != nil {
		t.Fatalf("IssueReader(): %v", err)
	}
	seedClaimer, err := env.reference.IssueClaimer()
	if err != nil {
		t.Fatalf("reference IssueClaimer(): %v", err)
	}
	return conformance.RowVersionFixture{
		IssuePrefix:      env.prefix,
		Lifecycle:        lifecycle,
		MetadataCAS:      cas,
		Claimer:          claimer,
		Releaser:         releaser,
		Heartbeater:      heartbeater,
		DependencyEditor: editor,
		Commenter:        commenter,
		BatchApplier:     applier,
		CreateIssue:      env.createIssue,
		AddDependency:    env.addDependency,
		SeedClaimer:      seedClaimer,
		GetIssue:         servedWireRevisionRead(env, reader),
		QueryScalar:      env.queryScalar,
	}
}

// servedWireRevisionRead answers the row as the client's Reader.Get decodes it,
// RowVersion stitched from the wire's `revision`, and refuses an answer whose
// token disagrees with the reference store's.
func servedWireRevisionRead(env *servedEnv, reader issueops.Reader) func(context.Context, string) (*types.Issue, error) {
	return func(ctx context.Context, id string) (*types.Issue, error) {
		details, err := reader.Get(ctx, issueops.GetRequest{ID: id})
		if err != nil {
			return nil, err
		}
		stored, err := env.reference.GetIssue(ctx, id)
		if err != nil {
			return nil, err
		}
		if details.Issue.RowVersion != stored.RowVersion {
			return nil, fmt.Errorf("the wire's revision for %s decodes to %d but the reference store's row_lock is %d",
				id, details.Issue.RowVersion, stored.RowVersion)
		}
		issue := details.Issue
		return &issue, nil
	}
}

func TestServedRowVersionAdvancesPerMutationKind(t *testing.T) {
	conformance.RunLifecycleRowVersionAdvancesPerMutationKind(t, t.Context(), newServedRowVersionFixture(t, "hrv1"))
}

func TestServedLifecycleUpdateLabelOnlyPatchLeavesRowVersion(t *testing.T) {
	conformance.RunLifecycleUpdateLabelOnlyPatchLeavesRowVersion(t, t.Context(), newServedRowVersionFixture(t, "hrv2"))
}
