//go:build cgo

package embeddeddolt_test

import (
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

type commitAuthor struct{ name, email string }

// storeCommitAuthors builds a fresh embedded store under the current
// environment, then has it write two commits, each passing the store's
// identity as --author: CommitPending over a pending tracked change, and a
// non-fast-forward Merge of a divergent, non-conflicting branch. It returns
// the author of each (#7628).
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
// GIT_AUTHOR_* identity, issue transaction, session and merge commits stay
// beads <beads@local>.
func TestEmbeddedCommitAuthorDefaultsWithoutGitAuthor(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")

	want := commitAuthor{"beads", "beads@local"}
	te := newTestEnv(t, "cadc")
	te.commitIdentityProbe(t, "default identity probe")
	if got := te.commitIdentity(t, "bd: create default identity probe"); got != want {
		t.Errorf("CreateIssue authored %+v, want %+v", got, want)
	}
	pending, merge := storeCommitAuthors(t, "cad")
	if pending != want {
		t.Errorf("CommitPending authored %+v, want %+v", pending, want)
	}
	if merge != want {
		t.Errorf("Merge authored %+v, want %+v", merge, want)
	}
}

// TestEmbeddedCommitAuthorSkipsAnUnparseableGitAuthor pins the fallback for an
// identity Dolt's --author parser cannot carry. Dolt refuses a name ending in
// ')' outright, which failed every commit, the fixture's own "bd init" commit
// first; now the name falls back to beads while the usable GIT_AUTHOR_EMAIL is
// kept, on issue transaction, pending-changes and merge commits alike.
func TestEmbeddedCommitAuthorSkipsAnUnparseableGitAuthor(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv("GIT_AUTHOR_NAME", "Store Author (CI)")
	t.Setenv("GIT_AUTHOR_EMAIL", "store@example.com")

	want := commitAuthor{"beads", "store@example.com"}
	te := newTestEnv(t, "cau")
	te.commitIdentityProbe(t, "unparseable identity probe")
	if got := te.commitIdentity(t, "bd: create unparseable identity probe"); got != want {
		t.Errorf("CreateIssue authored %+v, want %+v", got, want)
	}
	pending, merge := storeCommitAuthors(t, "cau2")
	if pending != want {
		t.Errorf("CommitPending authored %+v, want %+v", pending, want)
	}
	if merge != want {
		t.Errorf("Merge authored %+v, want %+v", merge, want)
	}
}

// commitIdentity reads dolt_log's (author, author_email) for the one commit
// that carries the given message. Pinned by message because dolt_log's
// commit_order has ties, which makes any ORDER BY ... LIMIT n read arbitrary
// within the tie.
func (te *testEnv) commitIdentity(t *testing.T, message string) commitAuthor {
	t.Helper()
	var got commitAuthor
	te.queryScalar(t, t.Context(),
		"SELECT author, author_email FROM dolt_log WHERE message = ?",
		[]any{message}, &got.name, &got.email)
	return got
}

// commitIdentityProbe writes an issue through the store's transaction path
// (CreateIssue in RunInTransaction, the explicit --author channel) under the
// commit message "bd: create <title>".
func (te *testEnv) commitIdentityProbe(t *testing.T, title string) {
	t.Helper()
	issue := &types.Issue{Title: title, Status: types.StatusOpen, IssueType: types.TypeTask}
	err := te.store.RunInTransaction(t.Context(), "bd: create "+title, func(tx storage.Transaction) error {
		return tx.CreateIssue(t.Context(), issue, "probe")
	})
	if err != nil {
		t.Fatalf("RunInTransaction create %q: %v", title, err)
	}
}

// TestEmbeddedCommitAuthorRetainedAfterEnvChange pins that the identity is
// captured when a store is constructed (#7628): its CreateIssue commits keep
// that identity after GIT_AUTHOR_* changes, while a store constructed under
// the new environment captures the new values. Env-mutating: no t.Parallel.
func TestEmbeddedCommitAuthorRetainedAfterEnvChange(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	t.Setenv("GIT_AUTHOR_NAME", "Store Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "store@example.com")
	want := commitAuthor{"Store Author", "store@example.com"}

	te := newTestEnv(t, "car")
	te.commitIdentityProbe(t, "author probe")
	if got := te.commitIdentity(t, "bd: create author probe"); got != want {
		t.Errorf("CreateIssue authored %+v, want %+v", got, want)
	}

	t.Setenv("GIT_AUTHOR_NAME", "Late Change")
	t.Setenv("GIT_AUTHOR_EMAIL", "late@example.com")
	te.commitIdentityProbe(t, "post-construction probe")
	if got := te.commitIdentity(t, "bd: create post-construction probe"); got != want {
		t.Errorf("CreateIssue after the env change authored %+v, want the identity captured at construction %+v", got, want)
	}

	te2 := newTestEnv(t, "car2")
	te2.commitIdentityProbe(t, "second store probe")
	if got, want2 := te2.commitIdentity(t, "bd: create second store probe"), (commitAuthor{"Late Change", "late@example.com"}); got != want2 {
		t.Errorf("second store's CreateIssue authored %+v, want %+v", got, want2)
	}
}
