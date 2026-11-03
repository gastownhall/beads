//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// bd-413ovu: import matches an incoming comment by its id before inserting it.
// Import is additive for comments: an id already stored on the same issue
// keeps its stored author, text and time whatever the issue row's stale-guard
// mode, instead of colliding on the comments primary key (Error 1062).
func TestCreateIssuesCommentIDMatch(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	// The comment-write journal is default-off; turn it on so the journal
	// assertions below can see what a write emits.
	newEnv := func(t *testing.T, prefix string) *testEnv {
		t.Helper()
		te := newTestEnv(t, prefix)
		te.store.SetEventsJournalEnabled(true)
		return te
	}

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	const commentID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

	issueWith := func(id string, wisp bool, updatedAt time.Time, comments ...*types.Comment) *types.Issue {
		return &types.Issue{
			ID: id, Title: "title", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp,
			CreatedAt: base, UpdatedAt: updatedAt,
			Comments: comments,
		}
	}
	original := func(issueID string) *types.Comment {
		return &types.Comment{ID: commentID, IssueID: issueID, Author: "alice", Text: "original text", CreatedAt: base}
	}
	plain := storage.BatchCreateOptions{SkipPrefixValidation: true}
	create := func(t *testing.T, te *testEnv, ctx context.Context, opts storage.BatchCreateOptions, issues ...*types.Issue) error {
		t.Helper()
		return te.store.CreateIssuesWithFullOptions(ctx, issues, "tester", opts)
	}
	commentTable := func(wisp bool) string {
		if wisp {
			return "wisp_comments"
		}
		return "comments"
	}
	commentCount := func(t *testing.T, te *testEnv, ctx context.Context, table, issueID string) int {
		t.Helper()
		var n int
		te.queryScalar(t, ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE issue_id = ?", table), []any{issueID}, &n)
		return n
	}
	commentJournal := func(t *testing.T, te *testEnv, ctx context.Context, issueID string) int {
		t.Helper()
		var n int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM bd_events_journal WHERE op = 'comment' AND issue_id = ?", []any{issueID}, &n)
		return n
	}
	assertStoredComment := func(t *testing.T, te *testEnv, ctx context.Context, table, id string, want *types.Comment) {
		t.Helper()
		var issueID, author, text string
		var createdAt time.Time
		te.queryScalar(t, ctx, fmt.Sprintf("SELECT issue_id, author, text, created_at FROM %s WHERE id = ?", table),
			[]any{id}, &issueID, &author, &text, &createdAt)
		if issueID != want.IssueID || author != want.Author || text != want.Text || !createdAt.UTC().Equal(want.CreatedAt) {
			t.Fatalf("stored comment %s = (%s, %s, %q, %v), want (%s, %s, %q, %v)", id,
				issueID, author, text, createdAt.UTC(), want.IssueID, want.Author, want.Text, want.CreatedAt)
		}
	}

	// AC-1: a same-issue id match keeps the stored comment in every mode.
	edits := []struct {
		name string
		edit func(c *types.Comment)
	}{
		{"text", func(c *types.Comment) { c.Text = "edited text" }},
		{"author", func(c *types.Comment) { c.Author = "bob" }},
		{"time", func(c *types.Comment) { c.CreatedAt = base.Add(time.Minute) }},
		{"missing_time", func(c *types.Comment) { c.CreatedAt = time.Time{} }},
	}
	modes := []struct {
		name      string
		opts      storage.BatchCreateOptions
		updatedAt time.Time
	}{
		{"plain_upsert_equal_row", plain, base},
		{"plain_upsert_newer_row", plain, base.Add(time.Hour)},
		{"reject_stale_equal_row", storage.BatchCreateOptions{SkipPrefixValidation: true, RejectStaleUpserts: true}, base},
		{"reject_stale_newer_row", storage.BatchCreateOptions{SkipPrefixValidation: true, RejectStaleUpserts: true}, base.Add(time.Hour)},
		{"conflict_skip", storage.BatchCreateOptions{SkipPrefixValidation: true, ConflictSkip: true}, base.Add(time.Hour)},
	}
	for _, plane := range []struct {
		name string
		wisp bool
	}{{"issues", false}, {"wisps", true}} {
		for _, mode := range modes {
			for _, e := range edits {
				if plane.wisp && e.name != "text" {
					continue // the wisp plane shares the code path; one edit proves its routing
				}
				t.Run(fmt.Sprintf("same_issue_id_keeps_stored/%s/%s/%s", plane.name, mode.name, e.name), func(t *testing.T) {
					te := newEnv(t, "cim")
					ctx := t.Context()
					table := commentTable(plane.wisp)
					if err := create(t, te, ctx, plain, issueWith("cim-1", plane.wisp, base, original("cim-1"))); err != nil {
						t.Fatalf("seed: %v", err)
					}
					journalBefore := commentJournal(t, te, ctx, "cim-1")

					incoming := original("cim-1")
					e.edit(incoming)
					if err := create(t, te, ctx, mode.opts, issueWith("cim-1", plane.wisp, mode.updatedAt, incoming)); err != nil {
						t.Fatalf("import with changed comment %s: %v", e.name, err)
					}

					assertStoredComment(t, te, ctx, table, commentID, original("cim-1"))
					if n := commentCount(t, te, ctx, table, "cim-1"); n != 1 {
						t.Fatalf("comment rows = %d, want 1", n)
					}
					if d := commentJournal(t, te, ctx, "cim-1") - journalBefore; d != 0 {
						t.Fatalf("comment journal events = %d, want 0", d)
					}
				})
			}
		}
	}

	// AC-3: an id stored on another issue is an ownership error naming the
	// comment and both issues; the failing transaction writes nothing.
	assertOwnershipError := func(t *testing.T, err error, commentID, owner, incoming string) {
		t.Helper()
		if err == nil {
			t.Fatalf("import of comment %s on %s succeeded, want ownership error", commentID, incoming)
		}
		msg := err.Error()
		for _, want := range []string{commentID, owner, incoming} {
			if !strings.Contains(msg, want) {
				t.Fatalf("error %q does not name %q", msg, want)
			}
		}
		if strings.Contains(msg, "1062") || strings.Contains(msg, "duplicate primary key") {
			t.Fatalf("error %q is the opaque primary-key collision", msg)
		}
	}
	issueRows := func(t *testing.T, te *testEnv, ctx context.Context, id string) int {
		t.Helper()
		var n int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", []any{id}, &n)
		return n
	}
	for _, tc := range []struct {
		name      string
		commentID string
		opts      storage.BatchCreateOptions
	}{
		{"uuid_id_new_issue", commentID, plain},
		{"uuid_id_new_issue_conflict_skip", commentID, storage.BatchCreateOptions{SkipPrefixValidation: true, ConflictSkip: true}},
		{"uuid_id_new_issue_reject_stale", commentID, storage.BatchCreateOptions{SkipPrefixValidation: true, RejectStaleUpserts: true}},
		{"legacy_numeric_id_new_issue", "42", plain},
	} {
		t.Run("other_issue_id_errors/"+tc.name, func(t *testing.T) {
			te := newEnv(t, "cio")
			ctx := t.Context()
			owned := original("cio-j")
			owned.ID = tc.commentID
			if err := create(t, te, ctx, plain, issueWith("cio-j", false, base, owned)); err != nil {
				t.Fatalf("seed: %v", err)
			}
			journalBefore := commentJournal(t, te, ctx, "cio-i")

			incoming := original("cio-i")
			incoming.ID = tc.commentID
			incoming.Text = "text on the other issue"
			err := create(t, te, ctx, tc.opts, issueWith("cio-i", false, base, incoming))
			assertOwnershipError(t, err, tc.commentID, "cio-j", "cio-i")

			assertStoredComment(t, te, ctx, "comments", tc.commentID, owned)
			if n := issueRows(t, te, ctx, "cio-i"); n != 0 {
				t.Fatalf("issue cio-i rows = %d, want the failing create rolled back", n)
			}
			if d := commentJournal(t, te, ctx, "cio-i") - journalBefore; d != 0 {
				t.Fatalf("comment journal events on cio-i = %d, want 0", d)
			}
		})
	}
	for _, opts := range []struct {
		name string
		opts storage.BatchCreateOptions
	}{
		{"plain_upsert", plain},
		{"conflict_skip", storage.BatchCreateOptions{SkipPrefixValidation: true, ConflictSkip: true}},
	} {
		t.Run("other_issue_id_errors/existing_issue_rolls_back_aux/"+opts.name, func(t *testing.T) {
			te := newEnv(t, "cio")
			ctx := t.Context()
			if err := create(t, te, ctx, plain,
				issueWith("cio-j", false, base, original("cio-j")),
				issueWith("cio-i", false, base)); err != nil {
				t.Fatalf("seed: %v", err)
			}

			incoming := issueWith("cio-i", false, base, original("cio-i"))
			incoming.Labels = []string{"from-import"}
			err := create(t, te, ctx, opts.opts, incoming)
			assertOwnershipError(t, err, commentID, "cio-j", "cio-i")

			assertStoredComment(t, te, ctx, "comments", commentID, original("cio-j"))
			var labels int
			te.queryScalar(t, ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", []any{"cio-i"}, &labels)
			if labels != 0 {
				t.Fatalf("labels on cio-i = %d, want the failing transaction's label rolled back", labels)
			}
			if n := commentCount(t, te, ctx, "comments", "cio-i"); n != 0 {
				t.Fatalf("comment rows on cio-i = %d, want 0", n)
			}
		})
	}
	t.Run("other_issue_id_errors/after_rename", func(t *testing.T) {
		// A rename moves the comment to the new id; importing the old id
		// with that comment is reported, not remapped or silently accepted.
		te := newEnv(t, "cio")
		ctx := t.Context()
		if err := create(t, te, ctx, plain, issueWith("cio-a", false, base, original("cio-a"))); err != nil {
			t.Fatalf("seed: %v", err)
		}
		renamed, err := te.store.GetIssue(ctx, "cio-a")
		if err != nil {
			t.Fatalf("get cio-a: %v", err)
		}
		renamed.ID = "cio-b"
		if err := te.store.UpdateIssueID(ctx, "cio-a", "cio-b", renamed, "tester"); err != nil {
			t.Fatalf("rename cio-a -> cio-b: %v", err)
		}

		// Another actor: the same actor would collide first on cio-a's
		// derived created-event id, a separate rename gap.
		err = te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{issueWith("cio-a", false, base, original("cio-a"))}, "importer", plain)
		assertOwnershipError(t, err, commentID, "cio-b", "cio-a")
		assertStoredComment(t, te, ctx, "comments", commentID, original("cio-b"))
	})
}
