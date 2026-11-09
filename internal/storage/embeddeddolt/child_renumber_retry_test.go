//go:build cgo

package embeddeddolt_test

import (
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
// retry against the same remote revision reports it again and fails the same
// way, without renaming anything twice or adding an empty rename commit.
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
	if first == nil || first.RenameCommit == "" || len(first.Renames) != 2 || first.Merged {
		t.Fatalf("first report = %+v, want two renames in a kept rename commit and no merge", first)
	}

	second, err := te.store.PullRenumberChildren(ctx, "", "tester")
	if !errors.As(err, &mce) {
		t.Fatalf("retry = %v, want the same MergeConflictsError", err)
	}
	if second == nil {
		t.Fatal("retry returned no report")
	}
	if len(second.Renames) != 0 || second.RenameCommit != "" {
		t.Errorf("retry renamed again: %+v", second)
	}
	if want := []string{first.RenameCommit}; !reflect.DeepEqual(second.PriorRenameCommits, want) {
		t.Errorf("retry reports prior rename commits %v, want %v", second.PriorRenameCommits, want)
	}
	if second.RemoteHead != first.RemoteHead {
		t.Errorf("retry merged %s, want the same remote revision %s", second.RemoteHead, first.RemoteHead)
	}

	withRawConn(t, te.dataDir, te.database, func(conn *sql.Conn) {
		if got := headHash(t, ctx, conn); got != first.RenameCommit {
			t.Errorf("HEAD = %s, want the kept rename commit %s", got, first.RenameCommit)
		}
		rows, err := conn.QueryContext(ctx, "SELECT message FROM dolt_log")
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		renameCommits := 0
		for rows.Next() {
			var msg string
			if err := rows.Scan(&msg); err != nil {
				t.Fatalf("scan log: %v", err)
			}
			if strings.HasPrefix(msg, "bd: renumber colliding children") {
				renameCommits++
			}
		}
		_ = rows.Close()
		if renameCommits != 1 {
			t.Errorf("%d rename commits in history, want 1", renameCommits)
		}
		wantTitles := map[string]string{p + ".73": "local 73", p + ".74": "local 71", p + ".75": "local 72"}
		if got := issueTitles(t, ctx, conn); !reflect.DeepEqual(got, wantTitles) {
			t.Errorf("children = %v, want %v", got, wantTitles)
		}
	})
}
