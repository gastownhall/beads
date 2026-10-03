//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
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

// updated_at is DATETIME(0), and the engine ROUNDS a sub-second value into it
// (half a second rounds up); it does not truncate. A comparison against a
// bound parameter sees the parameter's full precision instead. Together these
// are why issueops.NormalizeUpdatedAt exists and must round.
func TestIssueUpdatedAtColumnRoundsSubSecond(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "dtr")
	ctx := t.Context()
	stored := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)
	if err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
		ID: "dtr-1", Title: "title", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		CreatedAt: stored.Add(-time.Hour), UpdatedAt: stored,
	}}, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("seed issue: %v", err)
	}

	var olderThanParam int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id = ? AND updated_at < ?",
		[]any{"dtr-1", stored.Add(300 * time.Millisecond)}, &olderThanParam)
	if olderThanParam != 1 {
		t.Fatalf("stored %v < bound %v: count = %d, want 1 (a bound parameter keeps its fraction)",
			stored, stored.Add(300*time.Millisecond), olderThanParam)
	}

	for _, tc := range []struct {
		frac time.Duration
		want time.Time
	}{
		{0, stored},
		{300 * time.Millisecond, stored},
		{499 * time.Millisecond, stored},
		{500 * time.Millisecond, stored.Add(time.Second)},
		{700 * time.Millisecond, stored.Add(time.Second)},
		{999 * time.Millisecond, stored.Add(time.Second)},
	} {
		written := stored.Add(tc.frac)
		te.exec(t, ctx, "UPDATE issues SET updated_at = ? WHERE id = ?", written, "dtr-1")
		var got time.Time
		te.queryScalar(t, ctx, "SELECT updated_at FROM issues WHERE id = ?", []any{"dtr-1"}, &got)
		if !got.UTC().Equal(tc.want) {
			t.Fatalf("updated_at written as %v stored as %v, want %v", written, got.UTC(), tc.want)
		}
		if normalized := issueops.NormalizeUpdatedAt(written); !normalized.Equal(got.UTC()) {
			t.Fatalf("NormalizeUpdatedAt(%v) = %v, but the column stored %v", written, normalized, got.UTC())
		}
	}
}

// A sub-second incoming updated_at must reach one verdict for the issue row
// and its comments: both overwritten or both kept. The stored row sits on a
// whole second; the incoming row carries a new title and an edited comment.
func TestCreateIssuesSubSecondUpdatedAtMovesRowAndCommentTogether(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	stored := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)
	createdAt := stored.Add(-time.Hour)
	commentAt := createdAt.Add(10 * time.Minute)
	const commentID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a6a"
	const newID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a6b"

	row := func(id, title string, updatedAt time.Time, comments ...*types.Comment) *types.Issue {
		return &types.Issue{
			ID: id, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			CreatedAt: createdAt, UpdatedAt: updatedAt, Comments: comments,
		}
	}
	comment := func(id, text string) *types.Comment {
		return &types.Comment{ID: id, Author: "alice", Text: text, CreatedAt: commentAt}
	}
	setup := func(t *testing.T, prefix string) (*testEnv, string) {
		t.Helper()
		te := newTestEnv(t, prefix)
		id := prefix + "-1"
		if err := te.store.CreateIssuesWithFullOptions(t.Context(),
			[]*types.Issue{row(id, "old", stored, comment(commentID, "original"))},
			"tester", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
			t.Fatalf("seed issue: %v", err)
		}
		return te, id
	}
	importRow := func(t *testing.T, te *testEnv, issue *types.Issue, opts storage.BatchCreateOptions) {
		t.Helper()
		opts.SkipPrefixValidation = true
		opts.SkipDependencyValidationErrors = true
		if err := te.store.CreateIssuesWithFullOptions(t.Context(), []*types.Issue{issue}, "tester", opts); err != nil {
			t.Fatalf("import at %v: %v", issue.UpdatedAt, err)
		}
	}

	stale := storage.BatchCreateOptions{RejectStaleUpserts: true}
	allowStale := storage.BatchCreateOptions{}

	for i, tc := range []struct {
		name          string
		frac          time.Duration
		opts          storage.BatchCreateOptions
		wantOverwrite bool
	}{
		{"default_plus_300ms_is_a_tie", 300 * time.Millisecond, stale, false},
		{"default_plus_500ms_rounds_up_to_newer", 500 * time.Millisecond, stale, true},
		{"default_plus_700ms_rounds_up_to_newer", 700 * time.Millisecond, stale, true},
		{"allow_stale_plus_300ms", 300 * time.Millisecond, allowStale, true},
		{"allow_stale_plus_500ms", 500 * time.Millisecond, allowStale, true},
		{"allow_stale_plus_700ms", 700 * time.Millisecond, allowStale, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te, id := setup(t, fmt.Sprintf("ss%c", 'a'+i))
			ctx := t.Context()
			importRow(t, te, row(id, "new", stored.Add(tc.frac), comment(commentID, "edited")), tc.opts)

			var title, text string
			var updatedAt time.Time
			te.queryScalar(t, ctx, "SELECT title, updated_at FROM issues WHERE id = ?", []any{id}, &title, &updatedAt)
			te.queryScalar(t, ctx, "SELECT text FROM comments WHERE id = ?", []any{commentID}, &text)
			rowOverwritten, commentOverwritten := title == "new", text == "edited"
			if rowOverwritten != commentOverwritten {
				t.Fatalf("issue row and comment diverged at +%v: title=%q updated_at=%v comment=%q",
					tc.frac, title, updatedAt.UTC(), text)
			}
			if rowOverwritten != tc.wantOverwrite {
				t.Fatalf("at +%v: overwritten = %v, want %v (title=%q updated_at=%v comment=%q)",
					tc.frac, rowOverwritten, tc.wantOverwrite, title, updatedAt.UTC(), text)
			}
		})
	}

	// The stale check rounds the same way: -300ms lands on the stored second
	// (a tie, whose new aux rows still merge), -700ms on the second before it
	// (stale, rejected whole).
	for i, tc := range []struct {
		name           string
		frac           time.Duration
		wantNewComment bool
	}{
		{"default_minus_300ms_is_a_tie_not_stale", -300 * time.Millisecond, true},
		{"default_minus_700ms_is_stale", -700 * time.Millisecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			te, id := setup(t, fmt.Sprintf("st%c", 'a'+i))
			ctx := t.Context()
			importRow(t, te, row(id, "new", stored.Add(tc.frac), comment(commentID, "edited"), comment(newID, "added")), stale)

			var title, text string
			var newCount int
			te.queryScalar(t, ctx, "SELECT title FROM issues WHERE id = ?", []any{id}, &title)
			te.queryScalar(t, ctx, "SELECT text FROM comments WHERE id = ?", []any{commentID}, &text)
			te.queryScalar(t, ctx, "SELECT COUNT(*) FROM comments WHERE id = ?", []any{newID}, &newCount)
			if title != "old" || text != "original" {
				t.Fatalf("at %v: title=%q comment=%q, want the stored row and comment kept", tc.frac, title, text)
			}
			if (newCount == 1) != tc.wantNewComment {
				t.Fatalf("at %v: new comment stored = %v, want %v", tc.frac, newCount == 1, tc.wantNewComment)
			}
		})
	}
}
