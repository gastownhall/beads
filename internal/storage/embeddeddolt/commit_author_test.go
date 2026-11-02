//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// GH#7628: embedded mode authored every Dolt commit beads <beads@local> (or
// root <root@%> for whole-working-set commits), whoever ran the command.

type headCommit struct {
	hash, author, authorEmail, committer, committerEmail, message string
}

// readHead reads HEAD's commit row through a short-lived connection.
func (te *testEnv) readHead(t *testing.T, ctx context.Context) headCommit {
	t.Helper()
	var h headCommit
	te.queryScalar(t, ctx,
		"SELECT commit_hash, author, author_email, committer, email, message FROM dolt_log LIMIT 1", nil,
		&h.hash, &h.author, &h.authorEmail, &h.committer, &h.committerEmail, &h.message)
	return h
}

// requireNewCommitBy asserts HEAD moved past before and records name <email>
// as both author and committer.
func requireNewCommitBy(t *testing.T, label string, before, head headCommit, name, email string) {
	t.Helper()
	if head.hash == before.hash {
		t.Fatalf("%s: HEAD did not advance from %s (%q); no commit to inspect", label, before.hash, before.message)
	}
	if head.author != name || head.authorEmail != email {
		t.Errorf("%s: commit %q author = %q <%s>, want %q <%s>", label, head.message, head.author, head.authorEmail, name, email)
	}
	if head.committer != name || head.committerEmail != email {
		t.Errorf("%s: commit %q committer = %q <%s>, want %q <%s>", label, head.message, head.committer, head.committerEmail, name, email)
	}
}

// logHashes returns every commit hash in HEAD's log.
func (te *testEnv) logHashes(t *testing.T, ctx context.Context) map[string]bool {
	t.Helper()
	hashes := map[string]bool{}
	for _, c := range te.logCommits(t, ctx) {
		hashes[c.hash] = true
	}
	return hashes
}

// commitsSince returns HEAD's log entries missing from known, skipping the
// "peer:" commits a test makes itself.
func (te *testEnv) commitsSince(t *testing.T, ctx context.Context, known map[string]bool) []headCommit {
	t.Helper()
	var fresh []headCommit
	for _, c := range te.logCommits(t, ctx) {
		if !known[c.hash] && !strings.HasPrefix(c.message, "peer:") {
			fresh = append(fresh, c)
		}
	}
	return fresh
}

func (te *testEnv) logCommits(t *testing.T, ctx context.Context) []headCommit {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	rows, err := db.QueryContext(ctx, "SELECT commit_hash, author, author_email, committer, email, message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	defer rows.Close()
	var commits []headCommit
	for rows.Next() {
		var c headCommit
		if err := rows.Scan(&c.hash, &c.author, &c.authorEmail, &c.committer, &c.committerEmail, &c.message); err != nil {
			t.Fatalf("scan dolt_log: %v", err)
		}
		commits = append(commits, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	return commits
}

// openPinnedConn opens one session on the database. Close it before the store
// next opens its engine: the store waits for an open engine to close.
func (te *testEnv) openPinnedConn(t *testing.T, ctx context.Context) (*sql.Conn, func()) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = cleanup()
		t.Fatalf("pin connection: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		_ = cleanup()
	}
}

// peerCommit commits conn's working set as someone other than the store.
func peerCommit(t *testing.T, ctx context.Context, conn *sql.Conn, message string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?, '--author', 'Peer <peer@example.com>')", "peer: "+message); err != nil {
		t.Fatalf("peer commit %q: %v", message, err)
	}
}

func execOn(t *testing.T, ctx context.Context, conn *sql.Conn, stmt string, args ...any) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, stmt, args...); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

// seedBlockerPair creates <prefix>-a and <prefix>-b through the store and
// commits them.
func seedBlockerPair(t *testing.T, ctx context.Context, te *testEnv, prefix string) (a, b string) {
	t.Helper()
	a, b = prefix+"-a", prefix+"-b"
	for _, id := range []string{a, b} {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	if err := te.store.Commit(ctx, "seed issues"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return a, b
}

// divergeFromPeer branches peer at HEAD, commits an unrelated config row on
// main, and runs onPeer on the peer branch before committing it, so a merge
// of peer cannot fast-forward.
func divergeFromPeer(t *testing.T, ctx context.Context, te *testEnv, peer string, onPeer func(conn *sql.Conn)) {
	t.Helper()
	conn, closeConn := te.openPinnedConn(t, ctx)
	defer closeConn()
	execOn(t, ctx, conn, "CALL DOLT_BRANCH(?, 'HEAD')", peer)
	execOn(t, ctx, conn, "INSERT INTO config (`key`, value) VALUES (?, 'main')", "test.main."+peer)
	peerCommit(t, ctx, conn, "main side")
	execOn(t, ctx, conn, "CALL DOLT_CHECKOUT(?)", peer)
	execOn(t, ctx, conn, "INSERT INTO config (`key`, value) VALUES (?, 'peer')", "test.peer."+peer)
	if onPeer != nil {
		onPeer(conn)
	}
	peerCommit(t, ctx, conn, "peer side")
	execOn(t, ctx, conn, "CALL DOLT_CHECKOUT('main')")
}

func setGitAuthorEnv(t *testing.T, name, email string) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", name)
	t.Setenv("GIT_AUTHOR_EMAIL", email)
}

// TestEmbeddedCommitAuthorFollowsGitAuthorEnv pins the author of each commit
// an embedded store creates to GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL, the way
// server mode authors them.
func TestEmbeddedCommitAuthorFollowsGitAuthorEnv(t *testing.T) {
	const name, email = "Alice Author", "alice@example.com"

	t.Run("transactions", func(t *testing.T) {
		setGitAuthorEnv(t, name, email)
		ctx := t.Context()
		te := newTestEnv(t, "auth")

		issue := &types.Issue{Title: "authored", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		before := te.readHead(t, ctx)
		if err := te.store.RunInTransaction(ctx, "bd: create issue", func(tx storage.Transaction) error {
			return tx.CreateIssue(ctx, issue, "alice")
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
		head := te.readHead(t, ctx)
		requireNewCommitBy(t, "create", before, head, name, email)
		if head.message != "bd: create issue" {
			t.Errorf("create commit message = %q, want %q", head.message, "bd: create issue")
		}

		before = head
		updateMsg := "bd: update " + issue.ID
		if err := te.store.RunInTransaction(ctx, updateMsg, func(tx storage.Transaction) error {
			return tx.UpdateIssue(ctx, issue.ID, map[string]interface{}{"priority": 0}, "alice")
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		head = te.readHead(t, ctx)
		requireNewCommitBy(t, "update", before, head, name, email)
		if head.message != updateMsg {
			t.Errorf("update commit message = %q, want %q", head.message, updateMsg)
		}

		before = head
		closeMsg := "bd: close " + issue.ID
		if err := te.store.RunInTransaction(ctx, closeMsg, func(tx storage.Transaction) error {
			return tx.CloseIssue(ctx, issue.ID, "done", "alice", "")
		}); err != nil {
			t.Fatalf("close: %v", err)
		}
		head = te.readHead(t, ctx)
		requireNewCommitBy(t, "close", before, head, name, email)
		if !strings.HasPrefix(head.message, closeMsg) {
			t.Errorf("close commit message = %q, want %q", head.message, closeMsg)
		}
	})

	// The whole-working-set commits ran an unauthored DOLT_COMMIT('-Am'),
	// which records the embedded driver's SQL client, root <root@%>.
	t.Run("working set", func(t *testing.T) {
		setGitAuthorEnv(t, name, email)
		ctx := t.Context()
		te := newTestEnv(t, "authws")

		commits := []struct {
			label  string
			commit func() error
		}{
			{"Commit", func() error { return te.store.Commit(ctx, "test: Commit") }},
			{"CommitAll", func() error {
				committed, err := te.store.CommitAll(ctx, "test: CommitAll")
				if err == nil && !committed {
					t.Errorf("CommitAll reported nothing to commit")
				}
				return err
			}},
			{"CommitPending", func() error {
				committed, err := te.store.CommitPending(ctx, "alice")
				if err == nil && !committed {
					t.Errorf("CommitPending reported nothing to commit")
				}
				return err
			}},
		}
		for _, c := range commits {
			te.exec(t, ctx, "INSERT INTO config (`key`, value) VALUES (?, 'v')", "test.author."+c.label)
			before := te.readHead(t, ctx)
			if err := c.commit(); err != nil {
				t.Fatalf("%s: %v", c.label, err)
			}
			requireNewCommitBy(t, c.label, before, te.readHead(t, ctx), name, email)
		}
	})

	// Plain Merge: a non-fast-forward merge whose peer side adds a blocking
	// dependency without maintaining is_blocked, so the post-pull recompute
	// commits a repair too.
	t.Run("merge and post-pull recompute", func(t *testing.T) {
		setGitAuthorEnv(t, name, email)
		ctx := t.Context()
		te := newTestEnv(t, "authm")
		a, b := seedBlockerPair(t, ctx, te, "authm")
		divergeFromPeer(t, ctx, te, "authmpeer", func(conn *sql.Conn) {
			execOn(t, ctx, conn,
				"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, 'blocks', NOW(), 'peer')",
				a, b)
		})

		known := te.logHashes(t, ctx)
		conflicts, err := te.store.Merge(ctx, "authmpeer")
		if err != nil || len(conflicts) != 0 {
			t.Fatalf("Merge = %v, %v; want a clean merge", conflicts, err)
		}
		fresh := te.commitsSince(t, ctx, known)
		sawRecompute := false
		for _, c := range fresh {
			sawRecompute = sawRecompute || c.message == "bd: recompute is_blocked after pull"
			requireNewCommitBy(t, "Merge", headCommit{}, c, name, email)
		}
		if len(fresh) < 2 || !sawRecompute {
			t.Fatalf("Merge created commits %+v; want the merge commit and the post-pull recompute", fresh)
		}
	})

	t.Run("merge with strategy", func(t *testing.T) {
		setGitAuthorEnv(t, name, email)
		ctx := t.Context()
		te := newTestEnv(t, "authms")
		divergeFromPeer(t, ctx, te, "authmspeer", nil)

		known := te.logHashes(t, ctx)
		if _, err := te.store.MergeWithStrategy(ctx, "authmspeer", "theirs"); err != nil {
			t.Fatalf("MergeWithStrategy: %v", err)
		}
		fresh := te.commitsSince(t, ctx, known)
		if len(fresh) == 0 {
			t.Fatal("MergeWithStrategy created no commit")
		}
		for _, c := range fresh {
			requireNewCommitBy(t, "MergeWithStrategy", headCommit{}, c, name, email)
		}
	})

	t.Run("full recompute repair", func(t *testing.T) {
		setGitAuthorEnv(t, name, email)
		ctx := t.Context()
		te := newTestEnv(t, "authr")
		a, b := seedBlockerPair(t, ctx, te, "authr")
		if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: a, DependsOnID: b, Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("add dependency: %v", err)
		}
		if err := te.store.Commit(ctx, "seed blocker"); err != nil {
			t.Fatalf("commit blocker: %v", err)
		}
		conn, closeConn := te.openPinnedConn(t, ctx)
		execOn(t, ctx, conn, "UPDATE issues SET is_blocked = 0 WHERE id = ?", a)
		peerCommit(t, ctx, conn, "stale is_blocked")
		closeConn()

		rc, ok := storage.UnwrapStore(te.store).(storage.BlockedRecomputer)
		if !ok {
			t.Fatal("embedded store must implement storage.BlockedRecomputer")
		}
		before := te.readHead(t, ctx)
		changed, err := rc.RecomputeAllBlocked(ctx)
		if err != nil || changed == 0 {
			t.Fatalf("RecomputeAllBlocked = %d, %v; want a repair", changed, err)
		}
		head := te.readHead(t, ctx)
		requireNewCommitBy(t, "RecomputeAllBlocked", before, head, name, email)
		if head.message != versioncontrolops.BlockedRecomputeCommitMsg {
			t.Errorf("recompute commit message = %q, want %q", head.message, versioncontrolops.BlockedRecomputeCommitMsg)
		}
	})
}
