//go:build cgo

package embeddeddolt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
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
}
