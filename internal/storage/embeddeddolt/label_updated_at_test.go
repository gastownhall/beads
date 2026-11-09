//go:build cgo

package embeddeddolt_test

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// embeddedAgedUpdatedAt is decades before any clock these tests run under, so
// a stamp that did not move cannot pass for one that did.
var embeddedAgedUpdatedAt = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// TestEmbeddedLabelTransactionCommitsUpdatedAt (GH#5442): the embedded
// transaction wrapper stamps the owning row's updated_at through the same
// issueops body as every other write stack and marks the routed owning table
// dirty, so a RunInTransaction with a commit message publishes the new
// timestamp to HEAD, leaving no unstaged issues diff behind. On unfixed main
// the label and event rows commit while the owning row's timestamp never
// moves, so HEAD keeps the backdated value.
func TestEmbeddedLabelTransactionCommitsUpdatedAt(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "lblua")
	ctx := t.Context()

	id := "emb-lbl-ua"
	if err := te.store.CreateIssue(ctx, &types.Issue{
		ID: id, Title: "embedded label clock probe",
		Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// commitAged backdates the owning row and publishes the backdate, so HEAD
	// starts at the aged instant and a later HEAD read can only move if the
	// transaction's commit really carried the stamp.
	commitAged := func() {
		t.Helper()
		te.exec(t, ctx, "UPDATE issues SET updated_at = ? WHERE id = ?", embeddedAgedUpdatedAt, id)
		if err := te.store.Commit(ctx, "test: backdate label clock fixture"); err != nil {
			t.Fatalf("commit backdate: %v", err)
		}
	}
	assertPublished := func(op string) {
		t.Helper()
		var working, head time.Time
		te.queryScalar(t, ctx, "SELECT updated_at FROM issues WHERE id = ?", []any{id}, &working)
		te.queryScalar(t, ctx, "SELECT updated_at FROM issues AS OF 'HEAD' WHERE id = ?", []any{id}, &head)
		if !working.UTC().After(embeddedAgedUpdatedAt) {
			t.Errorf("%s: working updated_at = %v, want a stamp after %v", op, working.UTC(), embeddedAgedUpdatedAt)
		}
		if !head.UTC().After(embeddedAgedUpdatedAt) {
			t.Errorf("%s: updated_at at HEAD = %v, want a stamp after %v (the transaction's Dolt commit must publish the stamp)",
				op, head.UTC(), embeddedAgedUpdatedAt)
		}
		var dirty int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'", nil, &dirty)
		if dirty != 0 {
			t.Errorf("%s: issues is unstaged in dolt_status after the transaction commit; the stamp was left in the working set", op)
		}
	}

	commitAged()
	if err := te.store.RunInTransaction(ctx, "test: label add in transaction", func(tx storage.Transaction) error {
		return tx.AddLabel(ctx, id, "txlabel", "tester")
	}); err != nil {
		t.Fatalf("transaction AddLabel: %v", err)
	}
	assertPublished("transaction AddLabel")

	commitAged()
	if err := te.store.RunInTransaction(ctx, "test: label remove in transaction", func(tx storage.Transaction) error {
		return tx.RemoveLabel(ctx, id, "txlabel", "tester")
	}); err != nil {
		t.Fatalf("transaction RemoveLabel: %v", err)
	}
	assertPublished("transaction RemoveLabel")
}
