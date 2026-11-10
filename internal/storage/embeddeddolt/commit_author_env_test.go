//go:build cgo

package embeddeddolt_test

import (
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

type commitAuthor struct{ name, email string }

// storeCommitAuthors builds a fresh embedded store under the current
// environment, then has it write two commits: CommitPending over a pending
// tracked change (the session committer), and a non-fast-forward Merge of a
// divergent, non-conflicting branch (an explicit --author). It returns the
// author of each (#7628).
func storeCommitAuthors(t *testing.T, prefix string) (pending, merge commitAuthor) {
	t.Helper()
	te := newTestEnv(t, prefix)
	ctx := t.Context()

	te.exec(t, ctx, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'pending', '', '', '', '', 'open', 2, 'task')", prefix+"-p")
	committed, err := te.store.CommitPending(ctx, "tester")
	if err != nil || !committed {
		t.Fatalf("CommitPending = %v, %v; want a commit", committed, err)
	}
	te.queryScalar(t, ctx, "SELECT author, author_email FROM dolt_log WHERE message = 'bd: commit pending changes by tester'", nil, &pending.name, &pending.email)

	peer := prefix + "_peer"
	func() {
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("pin connection: %v", err)
		}
		defer func() { _ = conn.Close() }()
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{"CALL DOLT_BRANCH(?, 'HEAD')", []any{peer}},
			{"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'ours', '', '', '', '', 'open', 2, 'task')", []any{prefix + "-ours"}},
			{"CALL DOLT_COMMIT('-Am', 'ours')", nil},
			{"CALL DOLT_CHECKOUT(?)", []any{peer}},
			{"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES (?, 'theirs', '', '', '', '', 'open', 2, 'task')", []any{prefix + "-theirs"}},
			{"CALL DOLT_COMMIT('-Am', 'theirs')", nil},
			{"CALL DOLT_CHECKOUT('main')", nil},
		} {
			if _, err := conn.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
				t.Fatalf("seed divergent branch: %s: %v", stmt.sql, err)
			}
		}
	}()

	conflicts, err := te.store.Merge(ctx, peer)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("Merge(%s) = %v, %v; want a clean merge", peer, conflicts, err)
	}
	// The merge commit is the newest commit on main with a second parent.
	te.queryScalar(t, ctx, "SELECT l.author, l.author_email FROM dolt_log l JOIN dolt_commit_ancestors a ON a.commit_hash = l.commit_hash WHERE a.parent_index = 1 ORDER BY l.commit_order DESC LIMIT 1", nil, &merge.name, &merge.email)
	return pending, merge
}

// TestEmbeddedCommitAuthorFollowsGitAuthorEnv pins that an embedded store's
// session commits and explicit-author merge commits both carry the
// GIT_AUTHOR_* identity it was constructed under (#7628).
func TestEmbeddedCommitAuthorFollowsGitAuthorEnv(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv("GIT_AUTHOR_NAME", "Store Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "store@example.com")

	want := commitAuthor{"Store Author", "store@example.com"}
	pending, merge := storeCommitAuthors(t, "cae")
	if pending != want {
		t.Errorf("CommitPending authored %+v, want %+v", pending, want)
	}
	if merge != want {
		t.Errorf("Merge authored %+v, want %+v", merge, want)
	}
}

// TestEmbeddedCommitAuthorDefaultsWithoutGitAuthor pins the fallback: with no
// GIT_AUTHOR_* identity, session and merge commits stay beads <beads@local>.
func TestEmbeddedCommitAuthorDefaultsWithoutGitAuthor(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")

	want := commitAuthor{"beads", "beads@local"}
	pending, merge := storeCommitAuthors(t, "cad")
	if pending != want {
		t.Errorf("CommitPending authored %+v, want %+v", pending, want)
	}
	if merge != want {
		t.Errorf("Merge authored %+v, want %+v", merge, want)
	}
}
