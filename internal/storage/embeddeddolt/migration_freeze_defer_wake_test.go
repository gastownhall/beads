//go:build cgo

package embeddeddolt_test

import (
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedWakeExpiredDefersSkipsDuringMigrationFreeze pins GH#7634: a
// classified-read open (bd ready / bd list) opened with frozen=true must
// answer from current state without flipping an expired dated defer back to
// open. The sweep is a write, and dc-6jaq's contract is that a read may run
// during an active MIGRATION-FREEZE marker but no write may follow it. The
// same sweep DOES wake the defer once frozen is false, pinning that the new
// parameter disqualifies the sweep rather than disabling it outright.
func TestEmbeddedWakeExpiredDefersSkipsDuringMigrationFreeze(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	const database = "fz"
	const issueID = database + "-1"

	bootstrap, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("Open (bootstrap): %v", err)
	}
	if err := bootstrap.SetConfig(ctx, "issue_prefix", database); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := bootstrap.CreateIssue(ctx, &types.Issue{
		ID: issueID, Title: "deferred issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
	}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	// An expired DATED defer: status='deferred' with defer_until in the past
	// is exactly what WakeExpiredDefersInTx looks for.
	if _, err := db.ExecContext(ctx,
		`UPDATE issues SET status = 'deferred', defer_until = DATE_SUB(UTC_TIMESTAMP(), INTERVAL 1 MINUTE) WHERE id = ?`,
		issueID); err != nil {
		cleanup()
		t.Fatalf("seed expired defer: %v", err)
	}
	cleanup()
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap store: %v", err)
	}

	// A frozen classified-read open must not wake the defer.
	frozen, err := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, database, "main", true)
	if err != nil {
		t.Fatalf("OpenForReadOnlyCommand (frozen): %v", err)
	}
	if _, err := frozen.GetReadyWork(ctx, types.WorkFilter{}); err != nil {
		frozen.Close()
		t.Fatalf("GetReadyWork (frozen): %v", err)
	}
	issue, err := frozen.GetIssue(ctx, issueID)
	if err != nil {
		frozen.Close()
		t.Fatalf("GetIssue (frozen): %v", err)
	}
	if issue.Status != types.StatusDeferred || issue.DeferUntil == nil {
		frozen.Close()
		t.Fatalf("a frozen classified-read open woke the defer: status=%q deferUntil=%v, want it to stay deferred", issue.Status, issue.DeferUntil)
	}
	if err := frozen.Close(); err != nil {
		t.Fatalf("close frozen store: %v", err)
	}

	// The same sweep, unfrozen, must still wake it — frozen only disqualifies
	// the sweep for this open, it does not disable the sweep outright.
	unfrozen, err := embeddeddolt.OpenForReadOnlyCommand(ctx, beadsDir, database, "main", false)
	if err != nil {
		t.Fatalf("OpenForReadOnlyCommand (unfrozen): %v", err)
	}
	defer func() {
		if err := unfrozen.Close(); err != nil {
			t.Fatalf("close unfrozen store: %v", err)
		}
	}()
	if _, err := unfrozen.GetReadyWork(ctx, types.WorkFilter{}); err != nil {
		t.Fatalf("GetReadyWork (unfrozen): %v", err)
	}
	issue, err = unfrozen.GetIssue(ctx, issueID)
	if err != nil {
		t.Fatalf("GetIssue (unfrozen): %v", err)
	}
	if issue.Status != types.StatusOpen || issue.DeferUntil != nil {
		t.Fatalf("an unfrozen classified-read open did not wake the defer: status=%q deferUntil=%v", issue.Status, issue.DeferUntil)
	}
}
