//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedUpdateIssueCheckedFieldGuards proves the bd-wsqvw
// ExpectedAssignee/ExpectedStatus guards wire through the EmbeddedDoltStore's
// withConn wrapper: a matching guard applies, a stale guard refuses atomically
// with the typed mismatch error and the row untouched, and a pointer to "" is
// a real "expected unassigned" guard. The compare-and-swap core is the shared
// issueops.CheckExpectedFieldsInTx already covered against a real engine by
// the dolt package; this test proves the embedded wrapper threads it and rolls
// back.
func TestEmbeddedUpdateIssueCheckedFieldGuards(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "ugd")
	ctx := t.Context()
	sptr := func(v string) *string { return &v }

	create := func(id string) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	get := func(id string) *types.Issue {
		iss, err := te.store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s): %v", id, err)
		}
		return iss
	}

	// Reassign X→Y with a matching assignee guard (the park transition).
	create("ugd-park")
	if err := te.store.ClaimIssue(ctx, "ugd-park", "worker"); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := te.store.UpdateIssueChecked(ctx, "ugd-park",
		map[string]interface{}{"assignee": "mayor"}, "tester",
		storage.UpdateIssueOptions{ExpectedAssignee: sptr("worker")}); err != nil {
		t.Fatalf("guarded reassign err = %v, want nil", err)
	}
	if got := get("ugd-park").Assignee; got != "mayor" {
		t.Fatalf("assignee = %q after guarded reassign, want %q", got, "mayor")
	}

	// Stale assignee guard refuses atomically: typed error, row untouched.
	create("ugd-stale")
	if err := te.store.ClaimIssue(ctx, "ugd-stale", "thief"); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	err := te.store.UpdateIssueChecked(ctx, "ugd-stale",
		map[string]interface{}{"assignee": "mayor", "title": "should-not-apply"}, "tester",
		storage.UpdateIssueOptions{ExpectedAssignee: sptr("worker")})
	if !errors.Is(err, storage.ErrAssigneeMismatch) {
		t.Fatalf("err = %v, want errors.Is(_, ErrAssigneeMismatch)", err)
	}
	if iss := get("ugd-stale"); iss.Assignee != "thief" || iss.Title == "should-not-apply" {
		t.Fatalf("row changed after refused guard (assignee=%q title=%q); tx must roll back", iss.Assignee, iss.Title)
	}

	// Claim-on-behalf: --if-assignee '' --if-status open (the restore transition).
	create("ugd-restore")
	if err := te.store.UpdateIssueChecked(ctx, "ugd-restore",
		map[string]interface{}{"assignee": "owner", "status": "in_progress"}, "tester",
		storage.UpdateIssueOptions{ExpectedAssignee: sptr(""), ExpectedStatus: sptr("open")}); err != nil {
		t.Fatalf("claim-on-behalf err = %v, want nil", err)
	}
	if iss := get("ugd-restore"); iss.Assignee != "owner" || iss.Status != types.StatusInProgress {
		t.Fatalf("got assignee=%q status=%q, want owner/in_progress", iss.Assignee, iss.Status)
	}

	// Status guard mismatch is typed.
	err = te.store.UpdateIssueChecked(ctx, "ugd-restore",
		map[string]interface{}{"assignee": "other"}, "tester",
		storage.UpdateIssueOptions{ExpectedAssignee: sptr("owner"), ExpectedStatus: sptr("open")})
	if !errors.Is(err, storage.ErrStatusMismatch) {
		t.Fatalf("err = %v, want errors.Is(_, ErrStatusMismatch)", err)
	}
	if got := get("ugd-restore").Assignee; got != "owner" {
		t.Fatalf("assignee = %q after refused update, want unchanged %q", got, "owner")
	}
}
