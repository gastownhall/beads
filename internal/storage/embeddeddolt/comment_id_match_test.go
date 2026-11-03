//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
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

	// PR #7114's scenarios (Ezra Quemuel), with the newer-row and allow-stale
	// expectations turned to keep the stored comment: a re-import whose
	// comment is identical is a no-op in both guard modes, and a repeated
	// --allow-stale import of an edited comment never rewrites it.
	t.Run("same_issue_id_keeps_stored/identical_comment_is_noop", func(t *testing.T) {
		te := newEnv(t, "cim")
		ctx := t.Context()
		if err := create(t, te, ctx, plain, issueWith("cim-1", false, base, original("cim-1"))); err != nil {
			t.Fatalf("seed: %v", err)
		}
		journalBefore := commentJournal(t, te, ctx, "cim-1")
		for _, opts := range []storage.BatchCreateOptions{
			{SkipPrefixValidation: true, RejectStaleUpserts: true},
			plain,
		} {
			if err := create(t, te, ctx, opts, issueWith("cim-1", false, base.Add(time.Hour), original("cim-1"))); err != nil {
				t.Fatalf("identical re-import: %v", err)
			}
		}
		assertStoredComment(t, te, ctx, "comments", commentID, original("cim-1"))
		if n := commentCount(t, te, ctx, "comments", "cim-1"); n != 1 {
			t.Fatalf("comment rows = %d, want 1", n)
		}
		if d := commentJournal(t, te, ctx, "cim-1") - journalBefore; d != 0 {
			t.Fatalf("comment journal events = %d, want 0", d)
		}
	})
	t.Run("same_issue_id_keeps_stored/allow_stale_repeated", func(t *testing.T) {
		te := newEnv(t, "cim")
		ctx := t.Context()
		if err := create(t, te, ctx, plain, issueWith("cim-1", false, base, original("cim-1"))); err != nil {
			t.Fatalf("seed: %v", err)
		}
		journalBefore := commentJournal(t, te, ctx, "cim-1")
		incoming := original("cim-1")
		incoming.Text = "edited text"
		for i := range 2 {
			if err := create(t, te, ctx, plain, issueWith("cim-1", false, base, incoming)); err != nil {
				t.Fatalf("--allow-stale import %d: %v", i+1, err)
			}
			assertStoredComment(t, te, ctx, "comments", commentID, original("cim-1"))
		}
		if d := commentJournal(t, te, ctx, "cim-1") - journalBefore; d != 0 {
			t.Fatalf("comment journal events = %d, want 0", d)
		}
	})
	t.Run("stale_issue_row_skips_comment_merge", func(t *testing.T) {
		te := newEnv(t, "cim")
		ctx := t.Context()
		if err := create(t, te, ctx, plain, issueWith("cim-1", false, base.Add(2*time.Hour), original("cim-1"))); err != nil {
			t.Fatalf("seed: %v", err)
		}
		journalBefore := commentJournal(t, te, ctx, "cim-1")
		// A strictly older snapshot is rejected before aux persistence, so
		// neither its edited comment nor its new one merges.
		edited := original("cim-1")
		edited.Text = "edited text"
		added := &types.Comment{ID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5d", IssueID: "cim-1", Author: "dave", Text: "second", CreatedAt: base}
		opts := storage.BatchCreateOptions{SkipPrefixValidation: true, RejectStaleUpserts: true}
		if err := create(t, te, ctx, opts, issueWith("cim-1", false, base, edited, added)); err != nil {
			t.Fatalf("stale re-import: %v", err)
		}
		assertStoredComment(t, te, ctx, "comments", commentID, original("cim-1"))
		if n := commentCount(t, te, ctx, "comments", "cim-1"); n != 1 {
			t.Fatalf("comment rows = %d, want 1", n)
		}
		if d := commentJournal(t, te, ctx, "cim-1") - journalBefore; d != 0 {
			t.Fatalf("comment journal events = %d, want 0", d)
		}
	})
}
