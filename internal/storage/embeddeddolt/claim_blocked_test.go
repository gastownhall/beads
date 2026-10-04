//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// Every claim door ends in issueops.claimIssueInTx, and that is where the
// open-blocker refusal lives. The two doors below never pass through
// ExecuteUpdate, so a guard sitting only there (where this fix first put it)
// lets both of them claim a blocked issue; each case here fails against that
// placement and passes against the primitive.
func TestEmbeddedClaimRefusesAnOpenBlockerOnEveryDoor(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "clblk")
	ctx := t.Context()

	issueRow := func(t *testing.T, id string) (string, string) {
		t.Helper()
		var status, assignee string
		te.queryScalar(t, ctx, "SELECT status, COALESCE(assignee, '') FROM issues WHERE id = ?", []any{id}, &status, &assignee)
		return status, assignee
	}
	create := func(t *testing.T, id string) {
		t.Helper()
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	block := func(t *testing.T, blocked, blocker string) {
		t.Helper()
		if err := te.store.AddDependency(ctx, &types.Dependency{
			IssueID: blocked, DependsOnID: blocker, Type: types.DepBlocks,
		}, "tester"); err != nil {
			t.Fatalf("block %s by %s: %v", blocked, blocker, err)
		}
	}
	assertUntouched := func(t *testing.T, id string) {
		t.Helper()
		if status, assignee := issueRow(t, id); status != string(types.StatusOpen) || assignee != "" {
			t.Errorf("refused claim wrote to %s: status=%q assignee=%q, want open and unassigned", id, status, assignee)
		}
	}

	t.Run("StoreClaimIssue", func(t *testing.T) {
		create(t, "clblk-blocker-1")
		create(t, "clblk-blocked-1")
		block(t, "clblk-blocked-1", "clblk-blocker-1")

		err := te.store.ClaimIssue(ctx, "clblk-blocked-1", "worker")
		if !errors.Is(err, storage.ErrClaimBlocked) {
			t.Fatalf("ClaimIssue of a blocked issue: err = %v, want ErrClaimBlocked", err)
		}
		if !strings.Contains(err.Error(), "clblk-blocker-1") {
			t.Errorf("refusal does not name the blocker: %v", err)
		}
		assertUntouched(t, "clblk-blocked-1")
	})

	t.Run("ClaimerRole", func(t *testing.T) {
		create(t, "clblk-blocker-2")
		create(t, "clblk-blocked-2")
		block(t, "clblk-blocked-2", "clblk-blocker-2")

		claimer, err := te.store.IssueClaimer()
		if err != nil {
			t.Fatalf("IssueClaimer(): %v", err)
		}
		_, err = claimer.Claim(ctx, issueops.ClaimRequest{Actor: "worker", IssueID: "clblk-blocked-2"})
		if !errors.Is(err, issueops.ErrClaimBlocked) {
			t.Fatalf("Claimer.Claim of a blocked issue: err = %v, want ErrClaimBlocked", err)
		}
		var blocked *issueops.BlockedError
		if !errors.As(err, &blocked) {
			t.Fatalf("refusal is %T, want *issueops.BlockedError", err)
		}
		if len(blocked.Blockers) != 1 || blocked.Blockers[0].ID != "clblk-blocker-2" {
			t.Errorf("Blockers = %v, want the one open blocker", blocked.Blockers)
		}
		assertUntouched(t, "clblk-blocked-2")
	})

	t.Run("InheritedBlockWithNoLiveEdge", func(t *testing.T) {
		create(t, "clblk-inherited")
		te.exec(t, ctx, "UPDATE issues SET is_blocked = 1 WHERE id = ?", "clblk-inherited")

		err := te.store.ClaimIssue(ctx, "clblk-inherited", "worker")
		if !errors.Is(err, storage.ErrClaimBlocked) {
			t.Fatalf("ClaimIssue of an inherited block: err = %v, want ErrClaimBlocked", err)
		}
		if msg := err.Error(); !strings.Contains(msg, "inherited from an ancestor") || strings.Contains(msg, "[]") {
			t.Errorf("inherited block message = %q, want the ancestor sentence and no empty list", msg)
		}
		assertUntouched(t, "clblk-inherited")
	})

	t.Run("ClosedBlockerDoesNotBlock", func(t *testing.T) {
		create(t, "clblk-blocker-3")
		create(t, "clblk-blocked-3")
		block(t, "clblk-blocked-3", "clblk-blocker-3")
		if err := te.store.CloseIssue(ctx, "clblk-blocker-3", "done", "tester", ""); err != nil {
			t.Fatalf("close blocker: %v", err)
		}

		if err := te.store.ClaimIssue(ctx, "clblk-blocked-3", "worker"); err != nil {
			t.Fatalf("ClaimIssue after the blocker closed: %v", err)
		}
		if status, assignee := issueRow(t, "clblk-blocked-3"); status != string(types.StatusInProgress) || assignee != "worker" {
			t.Errorf("claim did not land: status=%q assignee=%q", status, assignee)
		}
	})
}
