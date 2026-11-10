//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// commitIdentity reads dolt_log's (author, author_email) for the one commit
// that carries the given message. Pinned by message because dolt_log's
// commit_order has ties (the engine's session commits and explicit-author
// commits can share an order value), which makes any ORDER BY ... LIMIT n
// read arbitrary within the tie.
func (te *testEnv) commitIdentity(t *testing.T, message string) (string, string) {
	t.Helper()
	var author, email string
	te.queryScalar(t, t.Context(),
		"SELECT author, author_email FROM dolt_log WHERE message = ?",
		[]any{message}, &author, &email)
	return author, email
}

// commitIdentityProbe writes an issue through the store's transaction path
// (CreateIssue in RunInTransaction, the StageAndCommit/--author channel) and
// returns the issue.
func (te *testEnv) commitIdentityProbe(t *testing.T, title string) *types.Issue {
	t.Helper()
	issue := &types.Issue{Title: title, Status: types.StatusOpen, IssueType: types.TypeTask}
	err := te.store.RunInTransaction(t.Context(), "bd: create "+title, func(tx storage.Transaction) error {
		return tx.CreateIssue(t.Context(), issue, "probe")
	})
	if err != nil {
		t.Fatalf("RunInTransaction create %q: %v", title, err)
	}
	return issue
}

// TestEmbeddedCommitAuthorFollowsGitAuthorEnv pins AC-6: an embedded store
// constructed with GIT_AUTHOR_NAME/GIT_AUTHOR_EMAIL set commits as that
// identity on every author channel — the transaction path's explicit --author
// (CreateIssue), the session-only DOLT_COMMIT('-Am') path (CommitPending), and
// the merge path's DOLT_MERGE('--author') (non-fast-forward Merge).
func TestEmbeddedCommitAuthorFollowsGitAuthorEnv(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	// The store captures its committer at construction, so the environment
	// must be set before the fixture opens. Env-mutating test: no t.Parallel.
	t.Setenv("GIT_AUTHOR_NAME", "Embedded Probe")
	t.Setenv("GIT_AUTHOR_EMAIL", "probe@embedded.test")

	const wantName, wantEmail = "Embedded Probe", "probe@embedded.test"

	te := newTestEnv(t, "gitauth")
	ctx := t.Context()

	// Explicit-author channel: CreateIssue inside a transaction commits via
	// StageAndCommit with --author.
	issue := te.commitIdentityProbe(t, "author probe")
	author, email := te.commitIdentity(t, "bd: create author probe")
	if author != wantName || email != wantEmail {
		t.Errorf("create commit (author, email) = (%q, %q), want (%q, %q)", author, email, wantName, wantEmail)
	}

	// Session-author channel: a pending tracked change committed by
	// CommitPending. Its DOLT_COMMIT('-Am', ..., '--author', ...) is the
	// channel the fix added (#7628): at base it carried no --author and
	// leaked the engine's root@% session identity.
	te.exec(t, ctx, "UPDATE issues SET priority = 2 WHERE id = ?", issue.ID)
	committed, err := te.store.CommitPending(ctx, "probe")
	if err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
	if !committed {
		t.Fatalf("CommitPending reported nothing committed; the working set update did not land")
	}
	author, email = te.commitIdentity(t, "bd: commit pending changes by probe")
	if author != wantName || email != wantEmail {
		t.Errorf("CommitPending commit (author, email) = (%q, %q), want (%q, %q)", author, email, wantName, wantEmail)
	}

	// Explicit-author merge channel: divergent nonconflicting branches force a
	// real merge commit, whose author is DOLT_MERGE's --author. Divergence is
	// seeded on one pinned raw session — DOLT_BRANCH/DOLT_CHECKOUT are
	// session-scoped, so the store's own fresh handles stay on main and the
	// te.store.Merge below merges probe-branch into main (the same seeding
	// shape as merge_strategy_test.go).
	rawDB, cleanupRaw, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL for divergence seeding: %v", err)
	}
	// The raw connector owns a SECOND engine over the same directory; it must
	// be closed before te.store.Merge opens the store's connections, or the
	// two engines contend on the journal lock (the driver disables its
	// singleton cache precisely because concurrent engines are unsupported).
	// No defer: closed explicitly below, right after the seeding.
	rawConn, err := rawDB.Conn(ctx)
	if err != nil {
		t.Fatalf("pin raw connection: %v", err)
	}
	execRaw := func(stmt string) {
		t.Helper()
		if _, err := rawConn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	execRaw("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
		"VALUES ('gitauth-div', 'divergence seed', '', '', '', '', 'open', 2, 'task')")
	execRaw("CALL DOLT_COMMIT('-Am', 'seed divergence')")
	execRaw("CALL DOLT_BRANCH('probe-branch', 'HEAD')")
	execRaw("UPDATE issues SET priority = 3 WHERE id = 'gitauth-div'")
	execRaw("CALL DOLT_COMMIT('-Am', 'main diverges')")
	execRaw("CALL DOLT_CHECKOUT('probe-branch')")
	execRaw("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
		"VALUES ('gitauth-br', 'branch side', '', '', '', '', 'open', 2, 'task')")
	execRaw("CALL DOLT_COMMIT('-Am', 'branch diverges')")
	execRaw("CALL DOLT_CHECKOUT('main')")
	if err := rawConn.Close(); err != nil {
		t.Fatalf("close raw connection: %v", err)
	}
	if err := cleanupRaw(); err != nil {
		t.Fatalf("close raw engine: %v", err)
	}

	var commitsBefore int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_log", nil, &commitsBefore)
	conflicts, err := te.store.Merge(ctx, "probe-branch")
	if err != nil {
		t.Fatalf("Merge probe-branch: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Merge produced conflicts: %v", conflicts)
	}
	// The branches diverged around one branch-side commit, so the merge adds
	// exactly two entries to main's dolt_log: the merged-in "branch diverges"
	// commit (authored by the raw seeding session's engine identity, root@% —
	// a merge must not rewrite existing history — and the reason counting
	// root-authored commits across a merge is not a valid assertion here) and
	// the merge commit itself.
	var commitsAfter int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dolt_log", nil, &commitsAfter)
	if commitsAfter != commitsBefore+2 {
		t.Fatalf("merge added %d commits to main's log, want exactly 2 (merged-in branch tip + merge commit; branches must diverge so the merge is non-fast-forward)", commitsAfter-commitsBefore)
	}
	// dolt_log's commit_order has ties, so ORDER BY commit_order LIMIT n row
	// order is arbitrary: pin the merge commit by the message the engine
	// gave it instead. The merge path is Merge(ctx, db, branch, --author) —
	// the explicit-author site the fix added (#7628) — so the merge commit
	// must carry the identity captured at construction, not the engine's
	// root@% session default.
	var mergeAuthor, mergeEmail string
	te.queryScalar(t, ctx,
		"SELECT author, author_email FROM dolt_log WHERE message LIKE 'Merge branch%'",
		nil, &mergeAuthor, &mergeEmail)
	if mergeAuthor != wantName || mergeEmail != wantEmail {
		t.Errorf("merge commit (author, email) = (%q, %q), want (%q, %q)", mergeAuthor, mergeEmail, wantName, wantEmail)
	}
	// The merge must have brought the branch-side issue back to main.
	if _, err := te.store.GetIssue(ctx, "gitauth-br"); err != nil {
		t.Fatalf("branch issue gitauth-br missing after merge: %v", err)
	}

	// Retention: the identity is an immutable property of the store, captured
	// at construction. Changing the environment afterwards must not redirect
	// any later commit of this store…
	t.Setenv("GIT_AUTHOR_NAME", "Late Change")
	t.Setenv("GIT_AUTHOR_EMAIL", "late@embedded.test")
	te.commitIdentityProbe(t, "post-construction probe")
	author, email = te.commitIdentity(t, "bd: create post-construction probe")
	if author != wantName || email != wantEmail {
		t.Errorf("commit after env change (author, email) = (%q, %q), want the identity captured at construction (%q, %q)",
			author, email, wantName, wantEmail)
	}
	// …while a distinct store constructed under the new environment captures
	// the new values: capture, not per-commit resolution.
	te2 := newTestEnv(t, "gitauth2")
	te2.commitIdentityProbe(t, "second store probe")
	author, email = te2.commitIdentity(t, "bd: create second store probe")
	if author != "Late Change" || email != "late@embedded.test" {
		t.Errorf("second store commit (author, email) = (%q, %q), want (Late Change, late@embedded.test)", author, email)
	}
}

// TestEmbeddedCommitAuthorDefaults pins AC-7's control: with neither
// GIT_AUTHOR_* variable resolving to a nonempty value, embedded commits on
// both the explicit-author and the session-author channels are
// `beads <beads@local>`. The explicit channel already carries that default
// at base; the session channel does not (it leaks the engine's OS user), so
// this control is red there too.
func TestEmbeddedCommitAuthorDefaults(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	// Pin both to empty rather than unset: empty is what ResolveCommitter
	// treats as unset, and t.Setenv restores the ambient value afterwards.
	// Env-mutating test: no t.Parallel.
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")

	te := newTestEnv(t, "gitdef")
	ctx := t.Context()

	// Explicit-author channel.
	issue := te.commitIdentityProbe(t, "default identity probe")
	author, email := te.commitIdentity(t, "bd: create default identity probe")
	if author != "beads" || email != "beads@local" {
		t.Errorf("create commit (author, email) = (%q, %q), want (beads, beads@local)", author, email)
	}

	// Session-author channel.
	te.exec(t, ctx, "UPDATE issues SET priority = 2 WHERE id = ?", issue.ID)
	committed, err := te.store.CommitPending(ctx, "probe")
	if err != nil || !committed {
		t.Fatalf("CommitPending: committed=%v err=%v", committed, err)
	}
	author, email = te.commitIdentity(t, "bd: commit pending changes by probe")
	if author != "beads" || email != "beads@local" {
		t.Errorf("CommitPending commit (author, email) = (%q, %q), want (beads, beads@local)", author, email)
	}
}
