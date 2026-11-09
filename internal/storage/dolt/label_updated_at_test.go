package dolt

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// labelClockAged is the updated_at the label-clock fixtures are imported
// with: whole-second and long before any clock this runs under.
var labelClockAged = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// seedAgedLabelIssue imports id with updated_at labelClockAged and labels,
// and requires the import to have left the issues table committed.
func seedAgedLabelIssue(t *testing.T, ctx context.Context, store *DoltStore, id string, wisp bool, labels ...string) {
	t.Helper()
	if err := store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
		ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Ephemeral: wisp, CreatedAt: labelClockAged, UpdatedAt: labelClockAged, Labels: labels,
	}}, "seed", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// headUpdatedAt reads id's updated_at as committed at HEAD.
func headUpdatedAt(t *testing.T, ctx context.Context, store *DoltStore, id string) time.Time {
	t.Helper()
	var at time.Time
	if err := store.db.QueryRowContext(ctx, "SELECT updated_at FROM issues AS OF 'HEAD' WHERE id = ?", id).Scan(&at); err != nil {
		t.Fatalf("read HEAD updated_at for %s: %v", id, err)
	}
	return at.UTC()
}

// TestLabelEditsCommitUpdatedAt pins the commit half of GH#5442 on the server
// leg with versioned history off: a label edit that advances updated_at has to
// put the advanced timestamp in the Dolt commit it makes, not leave the issues
// row dirty in the working set, or a clone pulling HEAD still sees the label
// change as older than it is. A wisp edit must not sweep an unrelated dirty
// durable row into its commit.
func TestLabelEditsCommitUpdatedAt(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	store.SetVersionedHistoryEnabled(false)

	cases := []struct {
		name   string
		labels []string
		run    func(id string) error
	}{
		{"AddLabel", nil, func(id string) error { return store.AddLabel(ctx, id, "x", "a") }},
		{"RemoveLabel", []string{"x"}, func(id string) error { return store.RemoveLabel(ctx, id, "x", "a") }},
		{"tx AddLabel", nil, func(id string) error {
			return store.RunInTransaction(ctx, "bd: tx label add", func(tx storage.Transaction) error {
				return tx.AddLabel(ctx, id, "x", "a")
			})
		}},
		{"tx RemoveLabel", []string{"x"}, func(id string) error {
			return store.RunInTransaction(ctx, "bd: tx label remove", func(tx storage.Transaction) error {
				return tx.RemoveLabel(ctx, id, "x", "a")
			})
		}},
	}
	for i, tc := range cases {
		id := "lua-" + string(rune('a'+i))
		seedAgedLabelIssue(t, ctx, store, id, false, tc.labels...)
		if dirtyTables(t, store)["issues"] {
			t.Fatalf("%s: precondition: issues dirty after seeding %s", tc.name, id)
		}
		if err := tc.run(id); err != nil {
			t.Fatalf("%s on %s: %v", tc.name, id, err)
		}
		if at := headUpdatedAt(t, ctx, store, id); !at.After(labelClockAged) {
			t.Errorf("%s on %s: HEAD updated_at = %v, want it advanced past %v", tc.name, id, at, labelClockAged)
		}
		if dirtyTables(t, store)["issues"] {
			t.Errorf("%s on %s: issues left dirty after the label commit", tc.name, id)
		}
	}

	t.Run("wisp transaction leaves unrelated durable row uncommitted", func(t *testing.T) {
		seedAgedLabelIssue(t, ctx, store, "lua-bystander", false)
		seedAgedLabelIssue(t, ctx, store, "lua-wisp-w", true)
		if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = ? WHERE id = ?", "uncommitted edit", "lua-bystander"); err != nil {
			t.Fatalf("dirty bystander: %v", err)
		}
		if err := store.RunInTransaction(ctx, "bd: wisp label add", func(tx storage.Transaction) error {
			return tx.AddLabel(ctx, "lua-wisp-w", "x", "a")
		}); err != nil {
			t.Fatalf("wisp tx AddLabel: %v", err)
		}
		var headTitle string
		if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues AS OF 'HEAD' WHERE id = ?", "lua-bystander").Scan(&headTitle); err != nil {
			t.Fatalf("read HEAD title: %v", err)
		}
		if headTitle != "lua-bystander" {
			t.Errorf("HEAD title = %q: the wisp label transaction committed an unrelated durable edit", headTitle)
		}
		got, err := store.GetIssue(ctx, "lua-wisp-w")
		if err != nil {
			t.Fatalf("get wisp: %v", err)
		}
		if !got.UpdatedAt.After(labelClockAged) {
			t.Errorf("wisp updated_at = %v, want it advanced past %v", got.UpdatedAt.UTC(), labelClockAged)
		}
	})
}

// TestExecuteUpdateLabelsReportOwningTable pins that a labels-only guarded
// update names the routed owning table among its ChangedTables, since the
// label edit now writes that row's updated_at and the caller stages exactly
// the tables reported. A wisp routes to wisps, which ChangedTables never
// records (the wisp tables are dolt-ignored), so the wisp case pins only that
// the edit is not misrouted onto issues.
func TestExecuteUpdateLabelsReportOwningTable(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	for _, plane := range []struct {
		id   string
		wisp bool
	}{{"lux-durable", false}, {"lux-wisp-w", true}} {
		seedAgedLabelIssue(t, ctx, store, plane.id, plane.wisp)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		_, tables, err := storageissueops.ExecuteUpdate(ctx, tx, publicops.UpdateRequest{
			Actor: "a", IssueID: plane.id,
			Patch: publicops.IssuePatch{Labels: publicops.LabelPatch{Add: []string{"x"}}},
		})
		_ = tx.Rollback()
		if err != nil {
			t.Fatalf("ExecuteUpdate %s: %v", plane.id, err)
		}
		if tables["issues"] == plane.wisp {
			t.Errorf("ExecuteUpdate %s labels-only (wisp=%v): ChangedTables = %v, want issues among them exactly for the durable row",
				plane.id, plane.wisp, tables.Sorted())
		}
	}
}

// TestLabelRenameCommitsUpdatedAt is the rename leg of
// TestLabelEditsCommitUpdatedAt: RenameLabel advances each carrier's
// updated_at, and its commit has to carry that, not leave issues dirty.
func TestLabelRenameCommitsUpdatedAt(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	store.SetVersionedHistoryEnabled(false)

	seedAgedLabelIssue(t, ctx, store, "lur-a", false, "rename-me")
	if dirtyTables(t, store)["issues"] {
		t.Fatalf("precondition: issues dirty after seeding")
	}
	if _, _, _, err := store.RenameLabel(ctx, "rename-me", "renamed", "a"); err != nil {
		t.Fatalf("RenameLabel: %v", err)
	}
	if at := headUpdatedAt(t, ctx, store, "lur-a"); !at.After(labelClockAged) {
		t.Errorf("HEAD updated_at = %v, want it advanced past %v", at, labelClockAged)
	}
	if dirtyTables(t, store)["issues"] {
		t.Errorf("issues left dirty after the rename commit")
	}
}
