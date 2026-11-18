//go:build cgo

package embeddeddolt

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestDeferWakePreflightStaleAnswers runs the defer-wake sweep against a real
// engine with a row changed between the preflight and the write transaction it
// decides on. The two run in separate transactions, so the preflight's answer
// can be out of date by the time the write would start; each case pins what the
// user sees then.
func TestDeferWakePreflightStaleAnswers(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), ".beads"), "wakepre", "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "wp"); err != nil {
		t.Fatal(err)
	}

	exec := func(t *testing.T, stmt string, args ...any) {
		t.Helper()
		db, cleanup, err := OpenSQL(ctx, store.dataDir, store.database, store.branch)
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	// state reports the issue's status and how many wake events it has.
	state := func(t *testing.T, id string) (string, int) {
		t.Helper()
		db, cleanup, err := OpenSQL(ctx, store.dataDir, store.database, store.branch)
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		var status string
		var wakes int
		if err := db.QueryRowContext(ctx, "SELECT status FROM issues WHERE id = ?", id).Scan(&status); err != nil {
			t.Fatalf("read status of %s: %v", id, err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE issue_id = ? AND actor = ?",
			id, issueops.DeferWakeActor).Scan(&wakes); err != nil {
			t.Fatalf("count wake events of %s: %v", id, err)
		}
		return status, wakes
	}
	deferredIssue := func(t *testing.T, until time.Time) string {
		t.Helper()
		issue := &types.Issue{Title: t.Name(), Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		exec(t, "UPDATE issues SET status = 'deferred', defer_until = ? WHERE id = ?", until.UTC(), issue.ID)
		return issue.ID
	}
	// between runs change in the window after the preflight, with its answer.
	between := func(t *testing.T, change func(due bool)) {
		saved := afterDeferWakePreflight
		afterDeferWakePreflight = change
		t.Cleanup(func() { afterDeferWakePreflight = saved })
	}

	t.Run("a row that becomes due after a negative preflight is woken by the next sweep", func(t *testing.T) {
		id := deferredIssue(t, time.Now().Add(time.Hour))
		var answered []bool
		between(t, func(due bool) {
			answered = append(answered, due)
			if len(answered) == 1 {
				exec(t, "UPDATE issues SET defer_until = ? WHERE id = ?", time.Now().Add(-time.Hour).UTC(), id)
			}
		})

		store.wakeExpiredDefers(ctx)
		if status, wakes := state(t, id); status != string(types.StatusDeferred) || wakes != 0 {
			t.Fatalf("after a negative preflight: status %q, %d wake events; want still deferred, none", status, wakes)
		}
		store.wakeExpiredDefers(ctx)
		if status, wakes := state(t, id); status != string(types.StatusOpen) || wakes != 1 {
			t.Fatalf("after the next sweep: status %q, %d wake events; want open, one", status, wakes)
		}
		if len(answered) != 2 || answered[0] || !answered[1] {
			t.Fatalf("preflight answers = %v, want [false true]", answered)
		}
	})

	t.Run("a row re-deferred after a positive preflight is left alone", func(t *testing.T) {
		id := deferredIssue(t, time.Now().Add(-time.Hour))
		later := time.Now().Add(24 * time.Hour)
		var answered []bool
		between(t, func(due bool) {
			answered = append(answered, due)
			exec(t, "UPDATE issues SET defer_until = ? WHERE id = ?", later.UTC(), id)
		})

		store.wakeExpiredDefers(ctx)
		if len(answered) != 1 || !answered[0] {
			t.Fatalf("preflight answers = %v, want [true]", answered)
		}
		if status, wakes := state(t, id); status != string(types.StatusDeferred) || wakes != 0 {
			t.Fatalf("after a positive preflight and a re-defer: status %q, %d wake events; want still deferred, none", status, wakes)
		}
	})

	t.Run("a row undeferred after a positive preflight is left alone", func(t *testing.T) {
		id := deferredIssue(t, time.Now().Add(-time.Hour))
		between(t, func(bool) {
			exec(t, "UPDATE issues SET status = 'in_progress', defer_until = NULL WHERE id = ?", id)
		})

		store.wakeExpiredDefers(ctx)
		if status, wakes := state(t, id); status != string(types.StatusInProgress) || wakes != 0 {
			t.Fatalf("after a positive preflight and an undefer: status %q, %d wake events; want in_progress, none", status, wakes)
		}
	})
}
