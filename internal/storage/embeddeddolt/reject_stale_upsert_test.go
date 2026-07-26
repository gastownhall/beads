//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// bd-pkim8: RejectStaleUpserts is the transactional half of the import stale
// guard. cmd/bd's filterStaleImportIssues reads local updated_at before the
// batch write, so a local update committing in between would be silently
// overwritten; with the option set, the upsert itself keeps the stored row
// when it is strictly newer than the incoming one.
func TestCreateIssuesRejectStaleUpserts(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	seed := func(t *testing.T, te *testEnv, ctx context.Context, id string) {
		t.Helper()
		err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
			ID: id, Title: "local title", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: base, UpdatedAt: base.Add(time.Hour),
		}}, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true})
		if err != nil {
			t.Fatalf("seed issue: %v", err)
		}
	}

	upsert := func(t *testing.T, te *testEnv, ctx context.Context, id, title string, updatedAt time.Time, rejectStale bool) {
		t.Helper()
		err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
			ID: id, Title: title, Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: base, UpdatedAt: updatedAt,
		}}, "tester", storage.BatchCreateOptions{
			SkipPrefixValidation: true,
			RejectStaleUpserts:   rejectStale,
		})
		if err != nil {
			t.Fatalf("upsert issue: %v", err)
		}
	}

	title := func(t *testing.T, te *testEnv, ctx context.Context, id string) string {
		t.Helper()
		var got string
		te.queryScalar(t, ctx, "SELECT title FROM issues WHERE id = ?", []any{id}, &got)
		return got
	}

	t.Run("stale_incoming_keeps_local_row", func(t *testing.T) {
		te := newTestEnv(t, "rsa")
		ctx := t.Context()
		seed(t, te, ctx, "rsa-1")

		upsert(t, te, ctx, "rsa-1", "stale snapshot title", base, true)

		if got := title(t, te, ctx, "rsa-1"); got != "local title" {
			t.Fatalf("title = %q, want local row preserved", got)
		}
		var gotUpdated time.Time
		te.queryScalar(t, ctx, "SELECT updated_at FROM issues WHERE id = ?", []any{"rsa-1"}, &gotUpdated)
		if !gotUpdated.UTC().Equal(base.Add(time.Hour)) {
			t.Fatalf("updated_at = %v, want local %v preserved", gotUpdated.UTC(), base.Add(time.Hour))
		}
	})

	t.Run("stale_incoming_does_not_reopen_closed_issue", func(t *testing.T) {
		// bd-hj85c incident shape (wyvern wy-78o): an issue closed locally
		// must not be reopened by importing an older snapshot in which it
		// was still open.
		te := newTestEnv(t, "rsg")
		ctx := t.Context()
		err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
			ID: "rsg-1", Title: "local title", Status: types.StatusClosed,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: base, UpdatedAt: base.Add(time.Hour),
		}}, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true})
		if err != nil {
			t.Fatalf("seed issue: %v", err)
		}

		err = te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
			ID: "rsg-1", Title: "local title", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: base, UpdatedAt: base,
		}}, "tester", storage.BatchCreateOptions{
			SkipPrefixValidation: true,
			RejectStaleUpserts:   true,
		})
		if err != nil {
			t.Fatalf("stale upsert: %v", err)
		}

		var gotStatus string
		te.queryScalar(t, ctx, "SELECT status FROM issues WHERE id = ?", []any{"rsg-1"}, &gotStatus)
		if gotStatus != string(types.StatusClosed) {
			t.Fatalf("status = %q, want closed status preserved against stale snapshot", gotStatus)
		}
	})

	t.Run("equal_timestamp_keeps_local_row", func(t *testing.T) {
		// bd-hj85c: updated_at has second granularity, so two distinct
		// same-second updates tie. The local row must win the tie — an
		// incoming tie row must not rewrite columns (re-importing an
		// identical snapshot is idempotent either way, since the rewrite
		// would have written identical values).
		te := newTestEnv(t, "rsb")
		ctx := t.Context()
		seed(t, te, ctx, "rsb-1")

		upsert(t, te, ctx, "rsb-1", "equal-time title", base.Add(time.Hour), true)

		if got := title(t, te, ctx, "rsb-1"); got != "local title" {
			t.Fatalf("title = %q, want local row preserved on equal timestamp", got)
		}
	})

	t.Run("equal_timestamp_empty_notes_does_not_wipe_local_notes", func(t *testing.T) {
		// bd-hj85c incident shape: local `bd update --notes` lands in the
		// same second as the snapshot being imported. The incoming row has
		// no notes; the populated local notes must survive, while the tie
		// row's aux data (which never bumps updated_at) still merges.
		te := newTestEnv(t, "rsf")
		ctx := t.Context()
		err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
			ID: "rsf-1", Title: "local title", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Notes: "local notes",
			CreatedAt: base, UpdatedAt: base.Add(time.Hour),
		}}, "tester", storage.BatchCreateOptions{SkipPrefixValidation: true})
		if err != nil {
			t.Fatalf("seed issue: %v", err)
		}

		err = te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
			ID: "rsf-1", Title: "local title", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, // Notes deliberately empty
			CreatedAt: base, UpdatedAt: base.Add(time.Hour),
			Labels: []string{"tie-label"},
		}}, "tester", storage.BatchCreateOptions{
			SkipPrefixValidation: true,
			RejectStaleUpserts:   true,
		})
		if err != nil {
			t.Fatalf("tie upsert: %v", err)
		}

		var gotNotes string
		te.queryScalar(t, ctx, "SELECT notes FROM issues WHERE id = ?", []any{"rsf-1"}, &gotNotes)
		if gotNotes != "local notes" {
			t.Fatalf("notes = %q, want local notes preserved on equal timestamp", gotNotes)
		}
		var labelCount int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = ?", []any{"rsf-1", "tie-label"}, &labelCount)
		if labelCount != 1 {
			t.Fatalf("labelCount = %d, want tie row's aux data merged", labelCount)
		}
	})

	t.Run("newer_incoming_applies", func(t *testing.T) {
		te := newTestEnv(t, "rsc")
		ctx := t.Context()
		seed(t, te, ctx, "rsc-1")

		upsert(t, te, ctx, "rsc-1", "newer title", base.Add(2*time.Hour), true)

		if got := title(t, te, ctx, "rsc-1"); got != "newer title" {
			t.Fatalf("title = %q, want newer upsert applied", got)
		}
		var gotUpdated time.Time
		te.queryScalar(t, ctx, "SELECT updated_at FROM issues WHERE id = ?", []any{"rsc-1"}, &gotUpdated)
		if !gotUpdated.UTC().Equal(base.Add(2 * time.Hour)) {
			t.Fatalf("updated_at = %v, want incoming %v", gotUpdated.UTC(), base.Add(2*time.Hour))
		}
	})

	t.Run("stale_incoming_skips_aux_and_reports", func(t *testing.T) {
		// bd-578h9.8: a rejected row must not merge in the stale snapshot's
		// labels/comments/dependencies, and OnStaleRejected must fire so the
		// import can count it as skipped instead of created.
		te := newTestEnv(t, "rse")
		ctx := t.Context()
		seed(t, te, ctx, "rse-1")
		seed(t, te, ctx, "rse-2")

		var rejected []string
		err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
			ID: "rse-1", Title: "stale snapshot title", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			CreatedAt: base, UpdatedAt: base,
			Labels:       []string{"stale-label"},
			Comments:     []*types.Comment{{Author: "tester", Text: "stale comment", CreatedAt: base}},
			Dependencies: []*types.Dependency{{IssueID: "rse-1", DependsOnID: "rse-2", Type: types.DepBlocks}},
		}}, "tester", storage.BatchCreateOptions{
			SkipPrefixValidation: true,
			RejectStaleUpserts:   true,
			OnStaleRejected:      func(id string) { rejected = append(rejected, id) },
		})
		if err != nil {
			t.Fatalf("upsert issue: %v", err)
		}

		if len(rejected) != 1 || rejected[0] != "rse-1" {
			t.Fatalf("OnStaleRejected = %v, want [rse-1]", rejected)
		}
		if got := title(t, te, ctx, "rse-1"); got != "local title" {
			t.Fatalf("title = %q, want local row preserved", got)
		}
		var labelCount, commentCount, depCount int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", []any{"rse-1"}, &labelCount)
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", []any{"rse-1"}, &commentCount)
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", []any{"rse-1"}, &depCount)
		if labelCount != 0 || commentCount != 0 || depCount != 0 {
			t.Fatalf("aux rows persisted for rejected issue: labels=%d comments=%d deps=%d, want 0/0/0",
				labelCount, commentCount, depCount)
		}
	})

	t.Run("without_flag_stale_overwrites", func(t *testing.T) {
		// --allow-stale path: plain UPSERT semantics, older snapshot wins.
		te := newTestEnv(t, "rsd")
		ctx := t.Context()
		seed(t, te, ctx, "rsd-1")

		upsert(t, te, ctx, "rsd-1", "stale snapshot title", base, false)

		if got := title(t, te, ctx, "rsd-1"); got != "stale snapshot title" {
			t.Fatalf("title = %q, want unguarded upsert to overwrite", got)
		}
	})
}
