//go:build cgo

package embeddeddolt_test

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// bd-413ovu (PR #7114): the historical content dedup — an incoming comment
// whose normalized content (author, created_at, text) is already stored on the
// same issue under a different id creates no duplicate comment and emits no
// comment-write event, whatever id shapes the two rows carry. The id-by-id
// resolution added for the same-id edit case must not regress it.
func TestCreateIssuesCommentContentDedupAcrossIDs(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	commentAt := base.Add(-30 * time.Minute)

	type fixture struct {
		storedID string
		incoming *types.Comment
	}
	run := func(t *testing.T, name string, f fixture) {
		t.Run(name, func(t *testing.T) {
			te := newTestEnv(t, "cda")
			te.store.SetEventsJournalEnabled(true)
			ctx := t.Context()
			seed := &types.Issue{
				ID: "cda-1", Title: "title", Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask,
				CreatedAt: base, UpdatedAt: base,
				Comments: []*types.Comment{{
					ID: f.storedID, IssueID: "cda-1", Author: "alice",
					Text: "shared text", CreatedAt: commentAt,
				}},
			}
			if err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{seed}, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			f.incoming.IssueID = "cda-1"
			incoming := &types.Issue{
				ID: "cda-1", Title: "title", Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask,
				CreatedAt: base, UpdatedAt: base,
				Comments: []*types.Comment{f.incoming},
			}
			if err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{incoming}, "tester", storage.BatchCreateOptions{
				SkipPrefixValidation: true, RejectStaleUpserts: true,
			}); err != nil {
				t.Fatalf("re-import: %v", err)
			}
			var count int
			te.queryScalar(t, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", []any{"cda-1"}, &count)
			if count != 1 {
				t.Fatalf("comment count on cda-1 = %d, want 1 (no duplicate)", count)
			}
			var journal int
			te.queryScalar(t, ctx,
				"SELECT COUNT(*) FROM bd_events_journal WHERE issue_id = ? AND op = 'comment'",
				[]any{"cda-1"}, &journal)
			if journal != 1 {
				t.Fatalf("comment journal events = %d, want 1 (the seed's; no comment write on the dedup)", journal)
			}
		})
	}

	run(t, "uuid_stored_uuid_incoming", fixture{
		storedID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b",
		incoming: &types.Comment{ID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c", Author: "alice", Text: "shared text", CreatedAt: commentAt},
	})
	run(t, "numeric_stored_uuid_incoming", fixture{
		storedID: "12345",
		incoming: &types.Comment{ID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c", Author: "alice", Text: "shared text", CreatedAt: commentAt},
	})
	run(t, "uuid_stored_derived_incoming", fixture{
		storedID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b",
		incoming: &types.Comment{ID: "cda-derived-9f2a", Author: "alice", Text: "shared text", CreatedAt: commentAt},
	})
	run(t, "uuid_stored_idless_incoming", fixture{
		storedID: "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b",
		incoming: &types.Comment{Author: "alice", Text: "shared text", CreatedAt: commentAt},
	})
}
