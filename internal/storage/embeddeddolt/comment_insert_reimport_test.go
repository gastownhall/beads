//go:build cgo

package embeddeddolt_test

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// bd-413ovu (PR #7114): a comment absent by both id and content inserts and
// journals exactly once (supplied id or the existing id-less derivation), and
// a re-import with the stored timestamp adds no comment row or comment-write
// event.
func TestCreateIssuesCommentInsertThenReimport(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	commentAt := base.Add(-30 * time.Minute)
	const commentID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

	run := func(t *testing.T, name string, first, second *types.Comment) {
		t.Run(name, func(t *testing.T) {
			te := newTestEnv(t, "cir")
			te.store.SetEventsJournalEnabled(true)
			ctx := t.Context()
			build := func(c *types.Comment) *types.Issue {
				c.IssueID = "cir-1"
				return &types.Issue{
					ID: "cir-1", Title: "title", Status: types.StatusOpen,
					Priority: 2, IssueType: types.TypeTask,
					CreatedAt: base, UpdatedAt: base,
					Comments: []*types.Comment{c},
				}
			}
			opts := storage.BatchCreateOptions{SkipPrefixValidation: true, RejectStaleUpserts: true}
			if err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{build(first)}, "tester", opts); err != nil {
				t.Fatalf("first import: %v", err)
			}
			var journal int
			te.queryScalar(t, ctx,
				"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ? AND op = 'comment'",
				[]any{"cir-1"}, &journal)
			if journal != 1 {
				t.Fatalf("comment journal events after first import = %d, want 1", journal)
			}
			// The re-import presents the comment exactly as stored.
			if err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{build(second)}, "tester", opts); err != nil {
				t.Fatalf("re-import: %v", err)
			}
			var count int
			te.queryScalar(t, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", []any{"cir-1"}, &count)
			if count != 1 {
				t.Fatalf("comment count on cir-1 = %d, want 1 (no second row)", count)
			}
			te.queryScalar(t, ctx,
				"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ? AND op = 'comment'",
				[]any{"cir-1"}, &journal)
			if journal != 1 {
				t.Fatalf("comment journal events after re-import = %d, want 1 (no comment write)", journal)
			}
		})
	}

	run(t, "supplied_id",
		&types.Comment{ID: commentID, Author: "alice", Text: "fresh", CreatedAt: commentAt},
		&types.Comment{ID: commentID, Author: "alice", Text: "fresh", CreatedAt: commentAt})
	// The id-less first import derives an id; the re-import presents the
	// same content and is deduplicated onto it.
	run(t, "idless_derivation",
		&types.Comment{Author: "alice", Text: "fresh", CreatedAt: commentAt},
		&types.Comment{Author: "alice", Text: "fresh", CreatedAt: commentAt})
}