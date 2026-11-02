//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// newestDoltLogRow reads the newest commit of dolt_log — the row a write that
// just landed owns. commit_order, not date, orders the rows: several commits
// can share a date at its truncation, and the wrong row would turn an
// authorship assertion into a coin flip.
func newestDoltLogRow(t *testing.T, te *testEnv) (author, email, committer, message string) {
	t.Helper()
	te.queryScalar(t, t.Context(),
		"SELECT author, author_email, committer, message FROM dolt_log ORDER BY commit_order DESC LIMIT 1",
		nil, &author, &email, &committer, &message)
	return
}

// assertNewCommitAuthored pins the newest dolt_log row: authorship, committer
// parity (DOLT_COMMIT('--author') stamps the committer too unless separately
// configured, which beads never does), and — when wantMessage is set — that the
// caller's commit message survived unchanged.
func assertNewCommitAuthored(t *testing.T, te *testEnv, wantName, wantEmail, wantMessage string) {
	t.Helper()
	author, email, committer, message := newestDoltLogRow(t, te)
	if author != wantName || email != wantEmail {
		t.Fatalf("newest commit author = %q <%q>, want %q <%q>", author, email, wantName, wantEmail)
	}
	if committer != author {
		t.Errorf("newest commit committer = %q, want parity with author %q", committer, author)
	}
	if wantMessage != "" && message != wantMessage {
		t.Errorf("commit message = %q, want %q", message, wantMessage)
	}
}

// TestEmbeddedCommitAuthorFollowsGitAuthorEnv pins GH#7628 AC-1: embedded
// transactional writes (the create/update/close lifecycle transactions) author
// their Dolt commits from GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL, server mode's
// existing rule, instead of the hard-coded beads <beads@local>, and the commit
// message format is unchanged.
func TestEmbeddedCommitAuthorFollowsGitAuthorEnv(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	t.Setenv("GIT_AUTHOR_NAME", "Alice Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "alice@example.com")

	te := newTestEnv(t, "cae")
	ctx := t.Context()

	issue := &types.Issue{Title: "authored create", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := te.store.RunInTransaction(ctx, "bd: create issue", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, issue, "tester")
	}); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	assertNewCommitAuthored(t, te, "Alice Author", "alice@example.com", "bd: create issue")

	if err := te.store.RunInTransaction(ctx, "bd: update "+issue.ID, func(tx storage.Transaction) error {
		return tx.UpdateIssue(ctx, issue.ID, map[string]interface{}{"title": "authored update"}, "tester")
	}); err != nil {
		t.Fatalf("update issue: %v", err)
	}
	assertNewCommitAuthored(t, te, "Alice Author", "alice@example.com", "bd: update "+issue.ID)

	if err := te.store.RunInTransaction(ctx, "bd: close "+issue.ID, func(tx storage.Transaction) error {
		return tx.CloseIssue(ctx, issue.ID, "done", "tester", "session")
	}); err != nil {
		t.Fatalf("close issue: %v", err)
	}
	assertNewCommitAuthored(t, te, "Alice Author", "alice@example.com", "bd: close "+issue.ID)
}

// TestEmbeddedCommitAllAuthorFollowsGitAuthorEnv pins GH#7628 AC-2: the
// whole-working-set commit points — the explicit Commit/CommitAll and the
// CommitPending auto-commit net that every embedded pull/sync runs — author
// their DOLT_COMMIT('-Am') commits from GIT_AUTHOR_NAME / GIT_AUTHOR_EMAIL
// instead of the SQL client's root <root@%>. Each round dirties the working
// set with a plain CreateIssue (no lifecycle transaction) so commitAll, not
// the transactional path, is what publishes it.
func TestEmbeddedCommitAllAuthorFollowsGitAuthorEnv(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	t.Setenv("GIT_AUTHOR_NAME", "Alice Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "alice@example.com")

	te := newTestEnv(t, "cbc")
	ctx := t.Context()

	dirty := func(n int) {
		issue := &types.Issue{
			Title:     fmt.Sprintf("authored working set %d", n),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue round %d: %v", n, err)
		}
	}

	dirty(1)
	if err := te.store.Commit(ctx, "bd: explicit commit"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	assertNewCommitAuthored(t, te, "Alice Author", "alice@example.com", "bd: explicit commit")

	dirty(2)
	committed, err := te.store.CommitAll(ctx, "bd: commit all")
	if err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if !committed {
		t.Fatalf("CommitAll reported nothing committed despite a dirty working set")
	}
	assertNewCommitAuthored(t, te, "Alice Author", "alice@example.com", "bd: commit all")

	dirty(3)
	committed, err = te.store.CommitPending(ctx, "tester")
	if err != nil {
		t.Fatalf("CommitPending: %v", err)
	}
	if !committed {
		t.Fatalf("CommitPending reported nothing committed despite a dirty working set")
	}
	assertNewCommitAuthored(t, te, "Alice Author", "alice@example.com", "bd: commit pending changes by tester")
}