//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestEmbeddedPullRenumberChildrenRetryKeepsRenameCommit is bd-28sb25 AC-6:
// on top of the report fixture both clones retitled the same issue, which the
// auto-resolver declines. The renumbering pull commits its renames, then the
// merge fails on that conflict. The rename commit stays and is reported; a
// retry against the same remote revision finds it by the marker naming that
// revision, reports its renames and parent (Retained) and fails the same
// way, without renaming anything twice or adding an empty rename commit.
// Once the operator settles the conflict, the retry's merge completes with
// the retained result, and a further pull has nothing to retain (glm's
// retained-result case).
func TestEmbeddedPullRenumberChildrenRetryKeepsRenameCommit(t *testing.T) {
	te := newTestEnv(t, "rn")
	ctx := t.Context()
	p := childCollisionParent
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		base := seedChildCollision(t, ctx, conn)
		local := headHash(t, ctx, conn)
		exec := func(q string, args ...any) {
			t.Helper()
			if _, err := conn.ExecContext(ctx, q, args...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		// Rebuild both sides with one more commit each that retitles P.
		exec("CALL DOLT_CHECKOUT('-b', 'remote-side', 'origin/main')")
		// Both retitles carry the same updated_at, so the auto-resolver
		// cannot settle the title last-write-wins and declines it.
		exec("UPDATE issues SET title = 'remote epic', updated_at = '2026-01-02 03:04:05' WHERE id = ?", p)
		exec("CALL DOLT_COMMIT('-Am', 'remote clone: retitle P')")
		exec("CALL DOLT_PUSH('origin', 'remote-side:main')")
		exec("CALL DOLT_CHECKOUT('main')")
		exec("CALL DOLT_BRANCH('-D', 'remote-side')")
		if got := headHash(t, ctx, conn); got != local {
			t.Fatalf("main moved from %s to %s while building the remote side (base %s)", local, got, base)
		}
		exec("UPDATE issues SET title = 'local epic', updated_at = '2026-01-02 03:04:05' WHERE id = ?", p)
		exec("CALL DOLT_COMMIT('-Am', 'local clone: retitle P')")
	})

	first, err := te.store.PullRenumberChildren(ctx, "", "tester")
	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(err, &mce) {
		t.Fatalf("first pull = %v, want a MergeConflictsError", err)
	}
	if first == nil || first.RenameCommit == "" || len(first.Renames) != 2 || first.Merged || first.Retained {
		t.Fatalf("first report = %+v, want two fresh renames in a kept rename commit and no merge", first)
	}
	if first.RenameParent != first.LocalHead {
		t.Errorf("first rename parent = %s, want the pre-pull head %s", first.RenameParent, first.LocalHead)
	}

	second, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if !errors.As(err, &mce) {
		t.Fatalf("retry = %v, want the same MergeConflictsError", err)
	}
	if second == nil {
		t.Fatal("retry returned no report")
	}
	if !second.Retained || second.RenameCommit != first.RenameCommit || second.RenameParent != first.RenameParent {
		t.Errorf("retry = retained %v, commit %s, parent %s; want the kept commit %s on %s",
			second.Retained, second.RenameCommit, second.RenameParent, first.RenameCommit, first.RenameParent)
	}
	if !reflect.DeepEqual(second.Renames, first.Renames) {
		t.Errorf("retry renames = %v, want the kept commit's %v", second.Renames, first.Renames)
	}
	if second.LocalHead != first.RenameCommit || second.Merged || len(second.Mentions) != 0 {
		t.Errorf("retry report = %+v, want it to start from the kept commit and not merge", second)
	}
	if second.RemoteHead != first.RemoteHead {
		t.Errorf("retry merged %s, want the same remote revision %s", second.RemoteHead, first.RemoteHead)
	}

	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		if got := headHash(t, ctx, conn); got != first.RenameCommit {
			t.Errorf("HEAD = %s, want the kept rename commit %s", got, first.RenameCommit)
		}
		if got := renameCommitMessages(t, ctx, conn); len(got) != 1 {
			t.Errorf("%d rename commits in history, want 1", len(got))
		} else if want := "bd: renumber colliding children\n\n" +
			"renumber: " + p + ".71 -> " + p + ".74\n" +
			"renumber: " + p + ".72 -> " + p + ".75\n" +
			"bd-renumber: remote_head=" + first.RemoteHead; strings.TrimSpace(got[first.RenameCommit]) != want {
			t.Errorf("rename commit message = %q, want %q", got[first.RenameCommit], want)
		}
		wantTitles := map[string]string{p + ".73": "local 73", p + ".74": "local 71", p + ".75": "local 72"}
		if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantTitles) {
			t.Errorf("children = %v, want %v", got, wantTitles)
		}
	})

	// The operator settles the residual conflict; the retry's merge completes
	// and still reports the retained renames, with the head it ended on.
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		if _, err := conn.ExecContext(ctx, "UPDATE issues SET title = 'remote epic' WHERE id = ?", p); err != nil {
			t.Fatalf("adopt remote title: %v", err)
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'adopt the remote title')"); err != nil {
			t.Fatalf("commit resolution: %v", err)
		}
	})
	third, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("pull after resolving: %v", err)
	}
	if !third.Retained || third.RenameCommit != first.RenameCommit || !reflect.DeepEqual(third.Renames, first.Renames) ||
		!third.Merged || third.Head == "" {
		t.Errorf("completing pull = %+v, want the retained renames merged", third)
	}

	// The repair is spent: the remote head is in history, so nothing is
	// retained and nothing is renamed.
	fourth, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if err != nil {
		t.Fatalf("further pull: %v", err)
	}
	if fourth.Retained || fourth.RenameCommit != "" || len(fourth.Renames) != 0 || fourth.Head != third.Head {
		t.Errorf("further pull = %+v, want nothing retained or renamed and head still %s", fourth, third.Head)
	}
	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		if got := renameCommitMessages(t, ctx, conn); len(got) != 1 {
			t.Errorf("%d rename commits in history after completing, want 1", len(got))
		}
		wantTitles := map[string]string{
			p + ".71": "remote 71", p + ".72": "remote 72", p + ".73": "local 73",
			p + ".74": "local 71", p + ".75": "local 72",
		}
		if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantTitles) {
			t.Errorf("children after completing = %v, want %v", got, wantTitles)
		}
	})
}

// renameCommitMessages maps each rename commit in HEAD's history to its
// message.
func renameCommitMessages(t *testing.T, ctx context.Context, conn *sql.Conn) map[string]string {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var hash, msg string
		if err := rows.Scan(&hash, &msg); err != nil {
			t.Fatalf("scan log: %v", err)
		}
		if strings.HasPrefix(msg, "bd: renumber colliding children") {
			out[hash] = msg
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read log: %v", err)
	}
	return out
}
