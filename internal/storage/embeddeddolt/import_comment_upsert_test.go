//go:build cgo

package embeddeddolt_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// An imported comment whose id already exists must not fail on the primary key;
// a changed comment follows the issue row's stale-guard verdict.
func TestCreateIssuesUpsertsCommentsByID(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	localUpdated := base.Add(time.Hour)
	commentAt := base.Add(10 * time.Minute)
	const commentID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

	issueWith := func(id string, updatedAt time.Time, comments ...*types.Comment) *types.Issue {
		return &types.Issue{
			ID: id, Title: "title", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: base, UpdatedAt: updatedAt,
			Comments: comments,
		}
	}
	comment := func(id, text string) *types.Comment {
		return &types.Comment{ID: id, Author: "alice", Text: text, CreatedAt: commentAt}
	}

	seed := func(t *testing.T, te *testEnv, ctx context.Context, id string) {
		t.Helper()
		err := te.store.CreateIssuesWithFullOptions(ctx,
			[]*types.Issue{issueWith(id, localUpdated, comment(commentID, "original"))},
			"tester", storage.BatchCreateOptions{SkipPrefixValidation: true})
		if err != nil {
			t.Fatalf("seed issue: %v", err)
		}
	}

	importRow := func(ctx context.Context, te *testEnv, issue *types.Issue, opts storage.BatchCreateOptions) error {
		opts.SkipPrefixValidation = true
		opts.SkipDependencyValidationErrors = true
		return te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{issue}, "tester", opts)
	}

	commentState := func(t *testing.T, te *testEnv, ctx context.Context, issueID string) (count int, text string, createdAt time.Time) {
		t.Helper()
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", []any{issueID}, &count)
		te.queryScalar(t, ctx, "SELECT text FROM comments WHERE id = ?", []any{commentID}, &text)
		te.queryScalar(t, ctx, "SELECT created_at FROM comments WHERE id = ?", []any{commentID}, &createdAt)
		return count, text, createdAt
	}

	wantComment := func(t *testing.T, te *testEnv, ctx context.Context, issueID string, wantCount int, wantText string) {
		t.Helper()
		count, text, createdAt := commentState(t, te, ctx, issueID)
		if count != wantCount {
			t.Fatalf("comment count = %d, want %d", count, wantCount)
		}
		if text != wantText {
			t.Fatalf("comment %s text = %q, want %q", commentID, text, wantText)
		}
		if !createdAt.UTC().Equal(commentAt) {
			t.Fatalf("comment %s created_at = %v, want JSONL timestamp %v preserved", commentID, createdAt.UTC(), commentAt)
		}
	}

	stale := storage.BatchCreateOptions{RejectStaleUpserts: true}
	allowStale := storage.BatchCreateOptions{}

	t.Run("identical_comment_reimport_is_noop", func(t *testing.T) {
		te := newTestEnv(t, "cua")
		ctx := t.Context()
		seed(t, te, ctx, "cua-1")
		for _, opts := range []storage.BatchCreateOptions{stale, allowStale} {
			if err := importRow(ctx, te, issueWith("cua-1", localUpdated.Add(time.Hour), comment(commentID, "original")), opts); err != nil {
				t.Fatalf("re-import identical comment: %v", err)
			}
		}
		wantComment(t, te, ctx, "cua-1", 1, "original")
	})

	t.Run("changed_text_equal_timestamp_keeps_local", func(t *testing.T) {
		te := newTestEnv(t, "cub")
		ctx := t.Context()
		seed(t, te, ctx, "cub-1")
		if err := importRow(ctx, te, issueWith("cub-1", localUpdated, comment(commentID, "edited")), stale); err != nil {
			t.Fatalf("import changed comment on tie: %v", err)
		}
		wantComment(t, te, ctx, "cub-1", 1, "original")
	})

	t.Run("changed_text_newer_row_updates", func(t *testing.T) {
		te := newTestEnv(t, "cuc")
		ctx := t.Context()
		seed(t, te, ctx, "cuc-1")
		if err := importRow(ctx, te, issueWith("cuc-1", localUpdated.Add(time.Hour), comment(commentID, "edited")), stale); err != nil {
			t.Fatalf("import changed comment on newer row: %v", err)
		}
		wantComment(t, te, ctx, "cuc-1", 1, "edited")
	})

	t.Run("changed_text_stale_row_keeps_local", func(t *testing.T) {
		te := newTestEnv(t, "cud")
		ctx := t.Context()
		seed(t, te, ctx, "cud-1")
		if err := importRow(ctx, te, issueWith("cud-1", base, comment(commentID, "edited")), stale); err != nil {
			t.Fatalf("import changed comment on stale row: %v", err)
		}
		wantComment(t, te, ctx, "cud-1", 1, "original")
	})

	t.Run("changed_text_allow_stale_updates", func(t *testing.T) {
		te := newTestEnv(t, "cue")
		ctx := t.Context()
		seed(t, te, ctx, "cue-1")
		if err := importRow(ctx, te, issueWith("cue-1", localUpdated, comment(commentID, "edited")), allowStale); err != nil {
			t.Fatalf("import --allow-stale changed comment: %v", err)
		}
		wantComment(t, te, ctx, "cue-1", 1, "edited")
		if err := importRow(ctx, te, issueWith("cue-1", localUpdated, comment(commentID, "edited")), allowStale); err != nil {
			t.Fatalf("re-import --allow-stale: %v", err)
		}
		wantComment(t, te, ctx, "cue-1", 1, "edited")
	})

	t.Run("changed_text_conflict_skip_keeps_local", func(t *testing.T) {
		te := newTestEnv(t, "cuf")
		ctx := t.Context()
		seed(t, te, ctx, "cuf-1")
		if err := importRow(ctx, te, issueWith("cuf-1", localUpdated.Add(time.Hour), comment(commentID, "edited")), storage.BatchCreateOptions{ConflictSkip: true}); err != nil {
			t.Fatalf("ConflictSkip import changed comment: %v", err)
		}
		wantComment(t, te, ctx, "cuf-1", 1, "original")
	})

	t.Run("new_comment_id_is_inserted", func(t *testing.T) {
		te := newTestEnv(t, "cug")
		ctx := t.Context()
		seed(t, te, ctx, "cug-1")
		const newID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"
		if err := importRow(ctx, te, issueWith("cug-1", localUpdated,
			comment(commentID, "original"), comment(newID, "second")), stale); err != nil {
			t.Fatalf("import new comment id: %v", err)
		}
		wantComment(t, te, ctx, "cug-1", 2, "original")
		var text string
		te.queryScalar(t, ctx, "SELECT text FROM comments WHERE id = ?", []any{newID}, &text)
		if text != "second" {
			t.Fatalf("new comment text = %q, want %q", text, "second")
		}
	})

	t.Run("comment_id_owned_by_other_issue_errors", func(t *testing.T) {
		te := newTestEnv(t, "cuh")
		ctx := t.Context()
		seed(t, te, ctx, "cuh-1")
		err := importRow(ctx, te, issueWith("cuh-2", localUpdated, comment(commentID, "original")), allowStale)
		if err == nil || !strings.Contains(err.Error(), "already belongs to cuh-1") {
			t.Fatalf("import comment id owned by another issue: err = %v, want 'already belongs to cuh-1'", err)
		}
	})
}
