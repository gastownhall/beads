//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// bd-413ovu (PR #7114): PersistComments deduplicated an incoming-id comment
// by content only (issue_id, author, created_at, text) and then INSERTed with
// the incoming id as the primary key. A stored comment with the same id but
// edited text, author or time missed the probe and collided with Error 1062,
// rolling back the whole chunk, so a re-import could never converge. The id
// resolves before the insert: a comment already stored on the same issue
// keeps its stored fields (comment merge is additive — no rewrite, no
// journal event, in every mode), and the historical content probe still
// deduplicates identical content under a different id.
func TestCreateIssuesCommentIDMatch(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	// newJournaledEnv is a fresh store with the events journal activated, the
	// way a workspace's own open does, so the comment-write assertions read
	// real journal rows.
	newJournaledEnv := func(t *testing.T) *testEnv {
		t.Helper()
		te := newTestEnv(t, "cim")
		te.store.SetEventsJournalEnabled(true)
		return te
	}

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	commentAt := base.Add(-30 * time.Minute)
	const commentID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

	// issue builds one durable issue (cim-1) carrying a single comment. Each
	// subtest runs in its own store, so the ids may repeat across subtests.
	issue := func(commentText string, updatedAt time.Time) *types.Issue {
		return &types.Issue{
			ID: "cim-1", Title: "title", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: base, UpdatedAt: updatedAt,
			Comments: []*types.Comment{{
				ID: commentID, IssueID: "cim-1", Author: "alice",
				Text: commentText, CreatedAt: commentAt,
			}},
		}
	}

	commentJournalCount := func(t *testing.T, te *testEnv, ctx context.Context, issueID string) int {
		t.Helper()
		var count int
		te.queryScalar(t, ctx,
			"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ? AND op = 'comment'",
			[]any{issueID}, &count)
		return count
	}

	storedComment := func(t *testing.T, te *testEnv, ctx context.Context, table string) (text, author string, createdAt time.Time) {
		t.Helper()
		te.queryScalar(t, ctx,
			"SELECT text, author, created_at FROM "+table+" WHERE id = ?",
			[]any{commentID}, &text, &author, &createdAt)
		return text, author, createdAt
	}

	assertCommentCount := func(t *testing.T, te *testEnv, ctx context.Context, table, issueID string, want int) {
		t.Helper()
		var count int
		te.queryScalar(t, ctx,
			"SELECT COUNT(*) FROM "+table+" WHERE issue_id = ?",
			[]any{issueID}, &count)
		if count != want {
			t.Fatalf("comment count on %s = %d, want %d", issueID, count, want)
		}
	}

	// reimport runs one upsert of the issue over the seeded row, in the given
	// mode, and returns the error so red cases can report it.
	reimport := func(t *testing.T, te *testEnv, ctx context.Context, in *types.Issue, mode storage.BatchCreateOptions) error {
		t.Helper()
		mode.SkipPrefixValidation = true
		return te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{in}, "tester", mode)
	}

	t.Run("same_id_edited_text_on_tie_keeps_stored", func(t *testing.T) {
		te := newJournaledEnv(t)
		ctx := t.Context()
		if err := reimport(t, te, ctx, issue("original text", base), storage.BatchCreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// Default import (RejectStaleUpserts): an equal-timestamp row keeps
		// every stored column but still merges its aux data, which is exactly
		// where the content probe used to miss and the insert collide.
		err := reimport(t, te, ctx, issue("edited text", base), storage.BatchCreateOptions{RejectStaleUpserts: true})
		if err != nil {
			t.Fatalf("re-import with edited comment text: %v", err)
		}
		text, author, createdAt := storedComment(t, te, ctx, "comments")
		if text != "original text" || author != "alice" || !createdAt.UTC().Equal(commentAt) {
			t.Fatalf("stored comment = (%q, %q, %v), want the seeded row kept", text, author, createdAt)
		}
		assertCommentCount(t, te, ctx, "comments", "cim-1", 1)
		if got := commentJournalCount(t, te, ctx, "cim-1"); got != 1 {
			t.Fatalf("comment journal events = %d, want 1 (the seed's; no comment write on the id match)", got)
		}
	})

	t.Run("same_id_edited_text_author_time_newer_row_keeps_stored", func(t *testing.T) {
		te := newJournaledEnv(t)
		ctx := t.Context()
		seed := &types.Issue{
			ID: "cim-1", Title: "title", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: base, UpdatedAt: base,
			Comments: []*types.Comment{
				{ID: "cim-c1", IssueID: "cim-1", Author: "alice", Text: "one", CreatedAt: commentAt},
				{ID: "cim-c2", IssueID: "cim-1", Author: "bob", Text: "two", CreatedAt: commentAt},
				{ID: "cim-c3", IssueID: "cim-1", Author: "carol", Text: "three", CreatedAt: commentAt},
			},
		}
		if err := reimport(t, te, ctx, seed, storage.BatchCreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// The issue row is strictly newer and IS rewritten; its comments are
		// additive data and keep their stored fields: one sub-case per edited
		// axis (text, author, time).
		incoming := &types.Issue{
			ID: "cim-1", Title: "title", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: base, UpdatedAt: base.Add(time.Hour),
			Comments: []*types.Comment{
				{ID: "cim-c1", IssueID: "cim-1", Author: "alice", Text: "one edited", CreatedAt: commentAt},
				{ID: "cim-c2", IssueID: "cim-1", Author: "bob edited", Text: "two", CreatedAt: commentAt},
				{ID: "cim-c3", IssueID: "cim-1", Author: "carol", Text: "three", CreatedAt: commentAt.Add(5 * time.Minute)},
			},
		}
		err := reimport(t, te, ctx, incoming, storage.BatchCreateOptions{RejectStaleUpserts: true})
		if err != nil {
			t.Fatalf("re-import with newer row and edited comments: %v", err)
		}
		want := []struct {
			id, text, author string
			createdAt        time.Time
		}{
			{"cim-c1", "one", "alice", commentAt},
			{"cim-c2", "two", "bob", commentAt},
			{"cim-c3", "three", "carol", commentAt},
		}
		for _, w := range want {
			var text, author string
			var createdAt time.Time
			te.queryScalar(t, ctx,
				"SELECT text, author, created_at FROM comments WHERE id = ?",
				[]any{w.id}, &text, &author, &createdAt)
			if text != w.text || author != w.author || !createdAt.UTC().Equal(w.createdAt) {
				t.Fatalf("stored comment %s = (%q, %q, %v), want the seeded row kept", w.id, text, author, createdAt)
			}
		}
		assertCommentCount(t, te, ctx, "comments", "cim-1", 3)
		if got := commentJournalCount(t, te, ctx, "cim-1"); got != 3 {
			t.Fatalf("comment journal events = %d, want 3 (the seed's; no comment write on the id match)", got)
		}
	})

	t.Run("same_id_edited_text_allow_stale_keeps_stored", func(t *testing.T) {
		te := newJournaledEnv(t)
		ctx := t.Context()
		if err := reimport(t, te, ctx, issue("original text", base), storage.BatchCreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// Plain upsert (--allow-stale): the issue row is rewritten, the
		// comment keeps its stored fields.
		err := reimport(t, te, ctx, issue("edited text", base.Add(time.Hour)), storage.BatchCreateOptions{})
		if err != nil {
			t.Fatalf("re-import with --allow-stale and edited comment text: %v", err)
		}
		if text, _, _ := storedComment(t, te, ctx, "comments"); text != "original text" {
			t.Fatalf("comment text = %q, want the stored %q kept", text, "original text")
		}
		assertCommentCount(t, te, ctx, "comments", "cim-1", 1)
		if got := commentJournalCount(t, te, ctx, "cim-1"); got != 1 {
			t.Fatalf("comment journal events = %d, want 1 (the seed's; no comment write on the id match)", got)
		}
	})

	t.Run("same_id_edited_text_conflict_skip_keeps_stored", func(t *testing.T) {
		te := newJournaledEnv(t)
		ctx := t.Context()
		if err := reimport(t, te, ctx, issue("original text", base), storage.BatchCreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// ConflictSkip leaves the stored row untouched, and the comment's
		// stored fields stay untouched with it.
		err := reimport(t, te, ctx, issue("edited text", base), storage.BatchCreateOptions{ConflictSkip: true})
		if err != nil {
			t.Fatalf("re-import with ConflictSkip and edited comment text: %v", err)
		}
		if text, _, _ := storedComment(t, te, ctx, "comments"); text != "original text" {
			t.Fatalf("comment text = %q, want the stored %q kept", text, "original text")
		}
		assertCommentCount(t, te, ctx, "comments", "cim-1", 1)
		if got := commentJournalCount(t, te, ctx, "cim-1"); got != 1 {
			t.Fatalf("comment journal events = %d, want 1 (the seed's; no comment write on the id match)", got)
		}
	})

	t.Run("same_id_missing_timestamp_keeps_stored", func(t *testing.T) {
		te := newJournaledEnv(t)
		ctx := t.Context()
		if err := reimport(t, te, ctx, issue("original text", base), storage.BatchCreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// No incoming timestamp: the prospective live stamp used to miss the
		// content probe and collide. The id lookup still resolves it to the
		// stored comment, a no-op.
		incoming := issue("edited text", base)
		incoming.Comments[0].CreatedAt = time.Time{}
		err := reimport(t, te, ctx, incoming, storage.BatchCreateOptions{RejectStaleUpserts: true})
		if err != nil {
			t.Fatalf("re-import with edited comment text and no comment timestamp: %v", err)
		}
		if text, _, createdAt := storedComment(t, te, ctx, "comments"); text != "original text" || !createdAt.UTC().Equal(commentAt) {
			t.Fatalf("stored comment = (%q, _, %v), want the seeded row kept", text, createdAt)
		}
		assertCommentCount(t, te, ctx, "comments", "cim-1", 1)
	})

	t.Run("identical_comment_is_noop", func(t *testing.T) {
		te := newJournaledEnv(t)
		ctx := t.Context()
		if err := reimport(t, te, ctx, issue("original text", base), storage.BatchCreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		err := reimport(t, te, ctx, issue("original text", base), storage.BatchCreateOptions{RejectStaleUpserts: true})
		if err != nil {
			t.Fatalf("identical re-import: %v", err)
		}
		assertCommentCount(t, te, ctx, "comments", "cim-1", 1)
		if got := commentJournalCount(t, te, ctx, "cim-1"); got != 1 {
			t.Fatalf("comment journal events = %d, want 1", got)
		}
	})

	t.Run("new_comment_id_inserts", func(t *testing.T) {
		te := newJournaledEnv(t)
		ctx := t.Context()
		if err := reimport(t, te, ctx, issue("original text", base), storage.BatchCreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		incoming := issue("original text", base)
		incoming.Comments = append(incoming.Comments, &types.Comment{
			ID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c", IssueID: "cim-1",
			Author: "dave", Text: "second", CreatedAt: commentAt,
		})
		if err := reimport(t, te, ctx, incoming, storage.BatchCreateOptions{RejectStaleUpserts: true}); err != nil {
			t.Fatalf("re-import with an added comment: %v", err)
		}
		assertCommentCount(t, te, ctx, "comments", "cim-1", 2)
		if got := commentJournalCount(t, te, ctx, "cim-1"); got != 2 {
			t.Fatalf("comment journal events = %d, want 2 (seed's one plus the inserted comment's)", got)
		}
	})

	t.Run("idless_comment_derives_and_dedups", func(t *testing.T) {
		te := newJournaledEnv(t)
		ctx := t.Context()
		seed := issue("", base)
		seed.Comments = []*types.Comment{{IssueID: "cim-1", Author: "alice", Text: "live comment", CreatedAt: commentAt}}
		if err := reimport(t, te, ctx, seed, storage.BatchCreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		assertCommentCount(t, te, ctx, "comments", "cim-1", 1)
		if err := reimport(t, te, ctx, seed, storage.BatchCreateOptions{RejectStaleUpserts: true}); err != nil {
			t.Fatalf("id-less re-import: %v", err)
		}
		assertCommentCount(t, te, ctx, "comments", "cim-1", 1)
	})

	t.Run("stale_issue_row_skips_comment_merge", func(t *testing.T) {
		te := newJournaledEnv(t)
		ctx := t.Context()
		if err := reimport(t, te, ctx, issue("original text", base.Add(2*time.Hour)), storage.BatchCreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// A strictly older snapshot is rejected before aux persistence, so
		// its comments merge in no mode.
		incoming := issue("edited text", base)
		incoming.Comments = append(incoming.Comments, &types.Comment{
			ID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5d", IssueID: "cim-1",
			Author: "dave", Text: "second", CreatedAt: commentAt,
		})
		err := reimport(t, te, ctx, incoming, storage.BatchCreateOptions{RejectStaleUpserts: true})
		if err != nil {
			t.Fatalf("stale re-import: %v", err)
		}
		assertCommentCount(t, te, ctx, "comments", "cim-1", 1)
		if text, _, _ := storedComment(t, te, ctx, "comments"); text != "original text" {
			t.Fatalf("comment text = %q, want the stored row kept", text)
		}
	})

	t.Run("wisp_plane_same_id_keeps_stored", func(t *testing.T) {
		te := newJournaledEnv(t)
		ctx := t.Context()
		wisp := func(commentText string, updatedAt time.Time) *types.Issue {
			in := issue(commentText, updatedAt)
			in.Ephemeral = true
			return in
		}
		if err := reimport(t, te, ctx, wisp("original text", base), storage.BatchCreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		err := reimport(t, te, ctx, wisp("edited text", base), storage.BatchCreateOptions{RejectStaleUpserts: true})
		if err != nil {
			t.Fatalf("wisp re-import with edited comment text: %v", err)
		}
		if text, _, _ := storedComment(t, te, ctx, "wisp_comments"); text != "original text" {
			t.Fatalf("wisp comment text = %q, want the stored %q kept", text, "original text")
		}
		assertCommentCount(t, te, ctx, "wisp_comments", "cim-1", 1)
	})
}