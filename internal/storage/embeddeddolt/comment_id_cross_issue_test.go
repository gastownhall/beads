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

// bd-413ovu (PR #7114): a comment id owned by a different issue is a
// conflict, not a primary-key collision. The import fails with an error
// naming the comment, its stored owner and the incoming issue, the failing
// transaction's writes roll back, and the owning issue keeps its comment —
// in every mode, including ConflictSkip. A rename (or a legacy numeric id)
// can leave a comment behind on its new owner; importing the old issue with
// the retained id surfaces the same explicit error. Content already stored
// on the incoming issue still deduplicates, whichever id the incoming record
// carries, so no error fires and no duplicate is created for it.
func TestCreateIssuesCommentIDCrossIssueOwnership(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	commentAt := base.Add(-30 * time.Minute)
	const commentID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

	issue := func(id, commentID, commentText string, updatedAt time.Time, ephemeral bool) *types.Issue {
		issue := &types.Issue{
			ID: id, Title: "title " + id, Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral,
			CreatedAt: base, UpdatedAt: updatedAt,
			Comments: []*types.Comment{{
				ID: commentID, IssueID: id, Author: "alice",
				Text: commentText, CreatedAt: commentAt,
			}},
		}
		return issue
	}

	create := func(t *testing.T, te *testEnv, ctx context.Context, mode storage.BatchCreateOptions, issues ...*types.Issue) error {
		t.Helper()
		mode.SkipPrefixValidation = true
		return te.store.CreateIssuesWithFullOptions(ctx, issues, "tester", mode)
	}

	commentTextByID := func(t *testing.T, te *testEnv, ctx context.Context, table, id string) (string, bool) {
		t.Helper()
		var text string
		var count int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", []any{id}, &count)
		if count == 0 {
			return "", false
		}
		te.queryScalar(t, ctx, "SELECT text FROM "+table+" WHERE id = ?", []any{id}, &text)
		return text, true
	}

	issueExists := func(t *testing.T, te *testEnv, ctx context.Context, table, id string) bool {
		t.Helper()
		var count int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", []any{id}, &count)
		return count > 0
	}

	assertOwnershipError := func(t *testing.T, err error, wantComment, wantOwner, wantIncoming string) {
		t.Helper()
		if err == nil {
			t.Fatalf("import succeeded, want the cross-issue comment ownership error")
		}
		for _, want := range []struct{ name, value string }{
			{"comment id", wantComment},
			{"stored owner", wantOwner},
			{"incoming issue", wantIncoming},
		} {
			if !strings.Contains(err.Error(), want.value) {
				t.Fatalf("error %q does not name the %s (%q)", err.Error(), want.name, want.value)
			}
		}
	}

	t.Run("comment_id_owned_by_other_issue_errors", func(t *testing.T) {
		te := newTestEnv(t, "cio")
		ctx := t.Context()
		if err := create(t, te, ctx, storage.BatchCreateOptions{}, issue("cio-owner", commentID, "owner text", base, false)); err != nil {
			t.Fatalf("seed owner: %v", err)
		}
		err := create(t, te, ctx, storage.BatchCreateOptions{RejectStaleUpserts: true}, issue("cio-incoming", commentID, "incoming text", base, false))
		assertOwnershipError(t, err, commentID, "cio-owner", "cio-incoming")
		if text, ok := commentTextByID(t, te, ctx, "comments", commentID); !ok || text != "owner text" {
			t.Fatalf("stored comment = (%q, %v), want the owner's row kept", text, ok)
		}
		if issueExists(t, te, ctx, "issues", "cio-incoming") {
			t.Fatalf("incoming issue exists, want the failing transaction rolled back")
		}
	})

	t.Run("ownership_error_rolls_back_the_whole_chunk", func(t *testing.T) {
		te := newTestEnv(t, "cir")
		ctx := t.Context()
		if err := create(t, te, ctx, storage.BatchCreateOptions{}, issue("cir-owner", commentID, "owner text", base, false)); err != nil {
			t.Fatalf("seed owner: %v", err)
		}
		// A second fresh issue rides in the same transaction: it must roll
		// back with the conflicting one, not survive the chunk's failure.
		err := create(t, te, ctx, storage.BatchCreateOptions{},
			issue("cir-incoming", commentID, "incoming text", base, false),
			issue("cir-bystander", "", "clean issue", base, false))
		assertOwnershipError(t, err, commentID, "cir-owner", "cir-incoming")
		if issueExists(t, te, ctx, "issues", "cir-bystander") {
			t.Fatalf("bystander issue exists, want the whole failing transaction rolled back")
		}
	})

	t.Run("ownership_error_under_conflict_skip", func(t *testing.T) {
		te := newTestEnv(t, "cis")
		ctx := t.Context()
		if err := create(t, te, ctx, storage.BatchCreateOptions{}, issue("cis-owner", commentID, "owner text", base, false)); err != nil {
			t.Fatalf("seed owner: %v", err)
		}
		if err := create(t, te, ctx, storage.BatchCreateOptions{}, issue("cis-incoming", "", "incoming text", base, false)); err != nil {
			t.Fatalf("seed incoming: %v", err)
		}
		// ConflictSkip tolerates a cross-plane collision on the issue row; a
		// comment id owned by another issue is still a conflict.
		err := create(t, te, ctx, storage.BatchCreateOptions{ConflictSkip: true},
			issue("cis-incoming", commentID, "edited text", base, false))
		assertOwnershipError(t, err, commentID, "cis-owner", "cis-incoming")
		if text, ok := commentTextByID(t, te, ctx, "comments", commentID); !ok || text != "owner text" {
			t.Fatalf("stored comment = (%q, %v), want the owner's row kept", text, ok)
		}
	})

	t.Run("renamed_issue_retaining_comment_id_errors_on_import", func(t *testing.T) {
		te := newTestEnv(t, "crn")
		ctx := t.Context()
		// The issue was renamed: the new id holds the comment now, and a
		// later import of the old id still presenting it must not silently
		// remap, duplicate or move it.
		if err := create(t, te, ctx, storage.BatchCreateOptions{}, issue("crn-new", commentID, "kept text", base, false)); err != nil {
			t.Fatalf("seed renamed owner: %v", err)
		}
		err := create(t, te, ctx, storage.BatchCreateOptions{RejectStaleUpserts: true}, issue("crn-old", commentID, "old text", base, false))
		assertOwnershipError(t, err, commentID, "crn-new", "crn-old")
		if text, ok := commentTextByID(t, te, ctx, "comments", commentID); !ok || text != "kept text" {
			t.Fatalf("stored comment = (%q, %v), want the renamed owner's row kept", text, ok)
		}
	})

	t.Run("legacy_numeric_id_owned_by_other_issue_errors", func(t *testing.T) {
		te := newTestEnv(t, "cln")
		ctx := t.Context()
		if err := create(t, te, ctx, storage.BatchCreateOptions{}, issue("cln-owner", "12345", "owner text", base, false)); err != nil {
			t.Fatalf("seed owner: %v", err)
		}
		err := create(t, te, ctx, storage.BatchCreateOptions{RejectStaleUpserts: true}, issue("cln-incoming", "12345", "incoming text", base, false))
		assertOwnershipError(t, err, "12345", "cln-owner", "cln-incoming")
		if text, ok := commentTextByID(t, te, ctx, "comments", "12345"); !ok || text != "owner text" {
			t.Fatalf("stored comment = (%q, %v), want the owner's row kept", text, ok)
		}
	})

	t.Run("content_equivalent_on_destination_dedups_despite_foreign_owner", func(t *testing.T) {
		te := newTestEnv(t, "cde")
		ctx := t.Context()
		// The same content is already stored on the incoming issue under
		// another id: the historical content dedup wins and the import
		// neither errors nor duplicates, whichever id the incoming record
		// carried (here the foreign-owned one).
		if err := create(t, te, ctx, storage.BatchCreateOptions{}, issue("cde-owner", commentID, "shared text", base, false)); err != nil {
			t.Fatalf("seed owner: %v", err)
		}
		if err := create(t, te, ctx, storage.BatchCreateOptions{}, issue("cde-incoming", "cde-local", "shared text", base, false)); err != nil {
			t.Fatalf("seed incoming: %v", err)
		}
		if err := create(t, te, ctx, storage.BatchCreateOptions{RejectStaleUpserts: true},
			issue("cde-incoming", commentID, "shared text", base, false)); err != nil {
			t.Fatalf("re-import with a foreign-owned id whose content is already stored: %v", err)
		}
		var count int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", []any{"cde-incoming"}, &count)
		if count != 1 {
			t.Fatalf("comment count on cde-incoming = %d, want 1 (no duplicate)", count)
		}
		if text, ok := commentTextByID(t, te, ctx, "comments", commentID); !ok || text != "shared text" {
			t.Fatalf("stored comment = (%q, %v), want the owner's row kept", text, ok)
		}
	})

	t.Run("wisp_plane_ownership_error", func(t *testing.T) {
		te := newTestEnv(t, "cwo")
		ctx := t.Context()
		if err := create(t, te, ctx, storage.BatchCreateOptions{}, issue("cwo-owner", commentID, "owner text", base, true)); err != nil {
			t.Fatalf("seed owner: %v", err)
		}
		err := create(t, te, ctx, storage.BatchCreateOptions{RejectStaleUpserts: true}, issue("cwo-incoming", commentID, "incoming text", base, true))
		assertOwnershipError(t, err, commentID, "cwo-owner", "cwo-incoming")
		if text, ok := commentTextByID(t, te, ctx, "wisp_comments", commentID); !ok || text != "owner text" {
			t.Fatalf("stored wisp comment = (%q, %v), want the owner's row kept", text, ok)
		}
	})
}