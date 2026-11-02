//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedCommitAuthorDefaults pins GH#7628 AC-5: with no identity source
// at all, both embedded write paths keep the anonymous beads <beads@local>
// pair — the previous, and only, embedded identity — instead of leaking the
// SQL client's root <root@%>. The transactional path exercised it even
// before (its constant author), but the whole-working-set path committed
// unauthored, which is where root <root@%> came from.
func TestEmbeddedCommitAuthorDefaults(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")

	issue := &types.Issue{
		Title:     "anonymous write",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	t.Run("transactional_write", func(t *testing.T) {
		te := newTestEnv(t, "cad")
		ctx := t.Context()
		if err := te.store.RunInTransaction(ctx, "bd: create issue", func(tx storage.Transaction) error {
			return tx.CreateIssue(ctx, issue, "tester")
		}); err != nil {
			t.Fatalf("create issue: %v", err)
		}
		assertNewCommitAuthored(t, te, "beads", "beads@local", "bd: create issue")
	})

	t.Run("whole_working_set_write", func(t *testing.T) {
		te := newTestEnv(t, "caw")
		ctx := t.Context()
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("create issue: %v", err)
		}
		if err := te.store.Commit(ctx, "bd: publish working set"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		assertNewCommitAuthored(t, te, "beads", "beads@local", "bd: publish working set")
	})
}