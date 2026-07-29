//go:build cgo

package embeddeddolt_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// testEnv bundles a store with the paths needed to open raw SQL connections.
type testEnv struct {
	store    *embeddeddolt.EmbeddedDoltStore
	dataDir  string
	database string
}

// newTestEnv creates an initialized EmbeddedDoltStore in a temp directory,
// sets the issue_prefix config, and returns a testEnv with raw SQL access.
func newTestEnv(t *testing.T, prefix string) *testEnv {
	t.Helper()
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, prefix, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return &testEnv{
		store:    store,
		dataDir:  filepath.Join(beadsDir, "embeddeddolt"),
		database: prefix,
	}
}

// queryScalar opens a short-lived raw SQL connection, runs a single-row query,
// scans the results into dest, and closes the connection immediately.
func (te *testEnv) queryScalar(t *testing.T, ctx context.Context, query string, args []any, dest ...any) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	if err := db.QueryRowContext(ctx, query, args...).Scan(dest...); err != nil {
		t.Fatalf("queryScalar %q: %v", query, err)
	}
}

// exec opens a short-lived raw SQL connection, executes a statement, and closes immediately.
func (te *testEnv) exec(t *testing.T, ctx context.Context, stmt string, args ...any) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

// assertRowExists verifies a row exists in the given table with the given ID.
func (te *testEnv) assertRowExists(t *testing.T, ctx context.Context, table, id string) {
	t.Helper()
	var count int
	te.queryScalar(t, ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", table), []any{id}, &count)
	if count == 0 {
		t.Errorf("expected row in %s with id=%q, found none", table, id)
	}
}

// assertRowNotExists verifies no row exists in the given table with the given ID.
func (te *testEnv) assertRowNotExists(t *testing.T, ctx context.Context, table, id string) {
	t.Helper()
	var count int
	te.queryScalar(t, ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", table), []any{id}, &count)
	if count != 0 {
		t.Errorf("expected no row in %s with id=%q, found %d", table, id, count)
	}
}

// assertIssueTitle verifies the title of an issue in the given table.
func (te *testEnv) assertIssueTitle(t *testing.T, ctx context.Context, table, id, wantTitle string) {
	t.Helper()
	var got string
	te.queryScalar(t, ctx, fmt.Sprintf("SELECT title FROM %s WHERE id = ?", table), []any{id}, &got)
	if got != wantTitle {
		t.Errorf("title for %s: got %q, want %q", id, got, wantTitle)
	}
}

// assertEventCount verifies the number of events for an issue.
func (te *testEnv) assertEventCount(t *testing.T, ctx context.Context, table, issueID, eventType string, wantCount int) {
	t.Helper()
	var count int
	te.queryScalar(t, ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE issue_id = ? AND event_type = ?", table),
		[]any{issueID, eventType}, &count)
	if count != wantCount {
		t.Errorf("event count for %s/%s: got %d, want %d", issueID, eventType, count, wantCount)
	}
}

// assertLabelCount verifies the number of labels for an issue.
func (te *testEnv) assertLabelCount(t *testing.T, ctx context.Context, table, issueID string, wantCount int) {
	t.Helper()
	var count int
	te.queryScalar(t, ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE issue_id = ?", table),
		[]any{issueID}, &count)
	if count != wantCount {
		t.Errorf("label count for %s: got %d, want %d", issueID, count, wantCount)
	}
}

// assertChildCounter verifies the last_child value in child_counters.
func (te *testEnv) assertChildCounter(t *testing.T, ctx context.Context, parentID string, wantLastChild int) {
	t.Helper()
	var lastChild int
	te.queryScalar(t, ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = ?",
		[]any{parentID}, &lastChild)
	if lastChild != wantLastChild {
		t.Errorf("last_child for %s: got %d, want %d", parentID, lastChild, wantLastChild)
	}
}

// assertWispChildCounter verifies the last_child value in wisp_child_counters.
func (te *testEnv) assertWispChildCounter(t *testing.T, ctx context.Context, parentID string, wantLastChild int) {
	t.Helper()
	var lastChild int
	te.queryScalar(t, ctx,
		"SELECT last_child FROM wisp_child_counters WHERE parent_id = ?",
		[]any{parentID}, &lastChild)
	if lastChild != wantLastChild {
		t.Errorf("wisp last_child for %s: got %d, want %d", parentID, lastChild, wantLastChild)
	}
}

// assertNoChildCounterRow verifies the row is absent from the given counter table.
func (te *testEnv) assertNoChildCounterRow(t *testing.T, ctx context.Context, table, parentID string) {
	t.Helper()
	var count int
	te.queryScalar(t, ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE parent_id = ?", table),
		[]any{parentID}, &count)
	if count != 0 {
		t.Errorf("expected no row in %s for parent %s, found %d", table, parentID, count)
	}
}

func skipUnlessEmbeddedDolt(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
}

func TestCreateIssue(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("basic", func(t *testing.T) {
		te := newTestEnv(t, "ci")
		ctx := t.Context()

		issue := &types.Issue{
			Title:     "Test issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}

		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if issue.ID == "" {
			t.Fatal("expected issue ID to be generated")
		}
		if issue.ContentHash == "" {
			t.Error("expected content hash to be computed")
		}
		if issue.CreatedAt.IsZero() {
			t.Error("expected created_at to be set")
		}

		te.assertRowExists(t, ctx, "issues", issue.ID)
	})

	t.Run("nil_issue_errors", func(t *testing.T) {
		te := newTestEnv(t, "ni")
		ctx := t.Context()

		err := te.store.CreateIssue(ctx, nil, "tester")
		if err == nil {
			t.Fatal("expected error for nil issue")
		}
	})

	t.Run("with_explicit_id", func(t *testing.T) {
		te := newTestEnv(t, "ex")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "ex-manual1",
			Title:     "Explicit ID issue",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeBug,
		}

		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if issue.ID != "ex-manual1" {
			t.Errorf("ID: got %q, want %q", issue.ID, "ex-manual1")
		}

		te.assertRowExists(t, ctx, "issues", "ex-manual1")
	})

	t.Run("missing_prefix_errors", func(t *testing.T) {
		ctx := t.Context()
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		store, err := embeddeddolt.Open(ctx, beadsDir, "noprefix", "main")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { store.Close() })

		issue := &types.Issue{
			Title:     "Should fail",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}

		err = store.CreateIssue(ctx, issue, "tester")
		if err == nil {
			t.Fatal("expected error for missing issue_prefix")
		}
		if !strings.Contains(err.Error(), "issue_prefix config is missing") {
			t.Errorf("expected ErrNotInitialized, got: %v", err)
		}
	})

	t.Run("closed_gets_closed_at", func(t *testing.T) {
		te := newTestEnv(t, "cc")
		ctx := t.Context()

		issue := &types.Issue{
			Title:     "Closed issue",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
		}

		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if issue.ClosedAt == nil {
			t.Error("expected closed_at to be set for closed issue")
		}
	})

	t.Run("timestamps_utc", func(t *testing.T) {
		te := newTestEnv(t, "tz")
		ctx := t.Context()

		loc := time.FixedZone("Test", 5*3600)
		nonUTC := time.Date(2025, 1, 15, 12, 0, 0, 0, loc)

		issue := &types.Issue{
			Title:     "Timezone test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: nonUTC,
		}

		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if issue.CreatedAt.Location() != time.UTC {
			t.Errorf("expected UTC, got %v", issue.CreatedAt.Location())
		}
	})

	t.Run("upsert_on_duplicate", func(t *testing.T) {
		te := newTestEnv(t, "up")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "up-dup1",
			Title:     "Original title",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}

		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("first CreateIssue: %v", err)
		}

		issue2 := &types.Issue{
			ID:        "up-dup1",
			Title:     "Updated title",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}

		if err := te.store.CreateIssue(ctx, issue2, "tester"); err != nil {
			t.Fatalf("second CreateIssue (upsert): %v", err)
		}

		te.assertIssueTitle(t, ctx, "issues", "up-dup1", "Updated title")
	})

	t.Run("event_recorded", func(t *testing.T) {
		te := newTestEnv(t, "ev")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "ev-evt1",
			Title:     "Event test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}

		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		te.assertEventCount(t, ctx, "events", "ev-evt1", "created", 1)
	})

	t.Run("ephemeral_routes_to_wisps", func(t *testing.T) {
		te := newTestEnv(t, "ew")
		ctx := t.Context()

		issue := &types.Issue{
			ID:        "ew-wisp1",
			Title:     "Ephemeral issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}

		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		te.assertRowExists(t, ctx, "wisps", "ew-wisp1")
		te.assertRowNotExists(t, ctx, "issues", "ew-wisp1")
	})

	t.Run("ephemeral_auto_id", func(t *testing.T) {
		te := newTestEnv(t, "ea")
		ctx := t.Context()

		issue := &types.Issue{
			Title:     "Ephemeral auto ID test",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}

		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if issue.ID == "" {
			t.Fatal("ephemeral issue got empty ID")
		}
		if !strings.Contains(issue.ID, "-wisp-") {
			t.Errorf("expected wisp-prefixed ID, got %q", issue.ID)
		}

		te.assertRowExists(t, ctx, "wisps", issue.ID)
	})

	t.Run("multiple_ephemeral_auto_ids_unique", func(t *testing.T) {
		te := newTestEnv(t, "mu")
		ctx := t.Context()

		ids := make(map[string]bool)
		for i := 0; i < 5; i++ {
			issue := &types.Issue{
				Title:     fmt.Sprintf("Ephemeral #%d", i),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				Ephemeral: true,
			}
			if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue #%d: %v", i, err)
			}
			if issue.ID == "" {
				t.Fatalf("ephemeral issue #%d got empty ID", i)
			}
			if ids[issue.ID] {
				t.Fatalf("duplicate ID %q on issue #%d", issue.ID, i)
			}
			ids[issue.ID] = true
		}
	})

	t.Run("counter_mode", func(t *testing.T) {
		te := newTestEnv(t, "cm")
		ctx := t.Context()

		if err := te.store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("SetConfig(issue_id_mode): %v", err)
		}
		if err := te.store.Commit(ctx, "enable counter mode"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		issue1 := &types.Issue{
			Title:     "Counter issue 1",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue1, "tester"); err != nil {
			t.Fatalf("CreateIssue 1: %v", err)
		}
		if issue1.ID != "cm-1" {
			t.Errorf("first counter ID: got %q, want %q", issue1.ID, "cm-1")
		}

		issue2 := &types.Issue{
			Title:     "Counter issue 2",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue2, "tester"); err != nil {
			t.Fatalf("CreateIssue 2: %v", err)
		}
		if issue2.ID != "cm-2" {
			t.Errorf("second counter ID: got %q, want %q", issue2.ID, "cm-2")
		}
	})

	t.Run("counter_explicit_id_overrides", func(t *testing.T) {
		te := newTestEnv(t, "co")
		ctx := t.Context()

		if err := te.store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		if err := te.store.Commit(ctx, "enable counter mode"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		issue := &types.Issue{
			ID:        "co-explicit",
			Title:     "Explicit ID issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if issue.ID != "co-explicit" {
			t.Errorf("expected co-explicit, got %q", issue.ID)
		}
	})

	t.Run("counter_seeds_from_existing", func(t *testing.T) {
		te := newTestEnv(t, "cs")
		ctx := t.Context()

		// Create issues with explicit sequential IDs before enabling counter mode.
		for _, id := range []string{"cs-5", "cs-10", "cs-3"} {
			issue := &types.Issue{
				ID:        id,
				Title:     "Pre-existing " + id,
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", id, err)
			}
		}

		if err := te.store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		if err := te.store.Commit(ctx, "enable counter mode"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		next := &types.Issue{
			Title:     "First counter-mode issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, next, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if next.ID != "cs-11" {
			t.Errorf("expected cs-11 (seeded from max existing id 10), got %q", next.ID)
		}
	})

	t.Run("counter_seeds_from_mixed", func(t *testing.T) {
		te := newTestEnv(t, "sm")
		ctx := t.Context()

		// Create a mix: one hash-based ID and one numeric ID.
		for _, iss := range []*types.Issue{
			{ID: "sm-a3f2", Title: "Hash-based", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "sm-7", Title: "Numeric", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		} {
			if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
				t.Fatalf("CreateIssue %s: %v", iss.ID, err)
			}
		}

		if err := te.store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		if err := te.store.Commit(ctx, "enable counter mode"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		next := &types.Issue{
			Title:     "First counter-mode issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, next, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if next.ID != "sm-8" {
			t.Errorf("expected sm-8 (seeded from max numeric id 7, ignoring hash id), got %q", next.ID)
		}
	})

	t.Run("counter_already_seeded", func(t *testing.T) {
		te := newTestEnv(t, "as")
		ctx := t.Context()

		// Manually seed counter at 20.
		te.exec(t, ctx, "INSERT INTO issue_counter (prefix, last_id) VALUES (?, ?)", "as", 20)

		// Create a manually-specified issue with a higher ID.
		high := &types.Issue{
			ID:        "as-99",
			Title:     "High manual ID",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, high, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if err := te.store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}
		if err := te.store.Commit(ctx, "enable counter mode"); err != nil {
			t.Fatalf("Commit: %v", err)
		}

		// Counter was at 20; seeding must NOT override existing row.
		next := &types.Issue{
			Title:     "Next counter issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, next, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if next.ID != "as-21" {
			t.Errorf("expected as-21 (counter must not re-seed over existing row), got %q", next.ID)
		}
	})

	t.Run("hash_mode_default", func(t *testing.T) {
		te := newTestEnv(t, "hm")
		ctx := t.Context()

		issue := &types.Issue{
			Title:     "Hash ID issue",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if issue.ID == "" {
			t.Error("expected non-empty ID in hash mode")
		}
		// Hash mode IDs should NOT be sequential integers.
		if issue.ID == "hm-1" || issue.ID == "hm-2" {
			t.Errorf("hash mode should not generate sequential IDs, got %q", issue.ID)
		}
	})

	t.Run("no_double_hyphen", func(t *testing.T) {
		te := newTestEnv(t, "gt")
		ctx := t.Context()

		// Write trailing-hyphen prefix directly to DB to bypass normalization.
		te.exec(t, ctx, "UPDATE config SET value = ? WHERE `key` = ?", "gt-", "issue_prefix")

		issue := &types.Issue{
			Title:     "test double hyphen",
			Status:    types.StatusOpen,
			Priority:  3,
			IssueType: types.TypeBug,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		if strings.Contains(issue.ID, "--") {
			t.Errorf("issue ID contains double hyphen: %q", issue.ID)
		}
		if !strings.HasPrefix(issue.ID, "gt-") {
			t.Errorf("issue ID should start with 'gt-', got %q", issue.ID)
		}
	})

	t.Run("event_type_without_custom_config", func(t *testing.T) {
		te := newTestEnv(t, "et")
		ctx := t.Context()

		issue := &types.Issue{
			Title:     "state change audit trail",
			Status:    types.StatusClosed,
			Priority:  4,
			IssueType: types.TypeEvent,
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue with event type should succeed, got: %v", err)
		}

		te.assertRowExists(t, ctx, "issues", issue.ID)
	})

	t.Run("validation_fails_for_bad_issue", func(t *testing.T) {
		te := newTestEnv(t, "vf")
		ctx := t.Context()

		issue := &types.Issue{
			Title:     "",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}

		err := te.store.CreateIssue(ctx, issue, "tester")
		if err == nil {
			t.Fatal("expected validation error for empty title")
		}
	})

	t.Run("wisp_validation_parity", func(t *testing.T) {
		te := newTestEnv(t, "wv")
		ctx := t.Context()

		tests := []struct {
			name    string
			issue   *types.Issue
			wantErr string
		}{
			{
				name: "valid wisp",
				issue: &types.Issue{
					Title: "a valid wisp", Status: types.StatusOpen,
					Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
				},
			},
			{
				name: "empty title rejected",
				issue: &types.Issue{
					Title: "", Status: types.StatusOpen,
					Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
				},
				wantErr: "title is required",
			},
			{
				name: "invalid status rejected",
				issue: &types.Issue{
					Title: "bad status", Status: types.Status("bogus"),
					Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
				},
				wantErr: "invalid status",
			},
			{
				name: "invalid type rejected",
				issue: &types.Issue{
					Title: "bad type", Status: types.StatusOpen,
					Priority: 2, IssueType: types.IssueType("nonexistent"), Ephemeral: true,
				},
				wantErr: "invalid issue type",
			},
			{
				name: "event type accepted",
				issue: &types.Issue{
					Title: "wisp event", Status: types.StatusOpen,
					Priority: 4, IssueType: types.TypeEvent, Ephemeral: true,
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := te.store.CreateIssue(ctx, tt.issue, "tester")
				if tt.wantErr == "" {
					if err != nil {
						t.Fatalf("expected success, got: %v", err)
					}
				} else {
					if err == nil {
						t.Fatalf("expected error containing %q, got nil", tt.wantErr)
					}
					if !strings.Contains(err.Error(), tt.wantErr) {
						t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
					}
				}
			})
		}
	})

	t.Run("infra_type_routes_to_wisps", func(t *testing.T) {
		te := newTestEnv(t, "ir")
		ctx := t.Context()

		// "agent" is an infra type but not a built-in issue type,
		// so it must be configured as a custom type to pass validation.
		if err := te.store.SetConfig(ctx, "types.custom", "agent"); err != nil {
			t.Fatalf("SetConfig: %v", err)
		}

		issue := &types.Issue{
			Title:     "Agent wisp",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.IssueType("agent"),
		}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}

		// Infra type should have been marked ephemeral and routed to wisps table.
		if !issue.Ephemeral {
			t.Fatal("expected issue.Ephemeral to be set for infra type")
		}
		te.assertRowExists(t, ctx, "wisps", issue.ID)
		te.assertRowNotExists(t, ctx, "issues", issue.ID)
	})
}

func TestCreateIssues(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("empty_slice", func(t *testing.T) {
		te := newTestEnv(t, "es")
		ctx := t.Context()

		if err := te.store.CreateIssues(ctx, nil, "tester"); err != nil {
			t.Fatalf("CreateIssues(nil): %v", err)
		}
	})

	t.Run("multiple_issues", func(t *testing.T) {
		te := newTestEnv(t, "mi")
		ctx := t.Context()

		issues := []*types.Issue{
			{ID: "mi-aaa", Title: "First", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "mi-bbb", Title: "Second", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug},
			{ID: "mi-ccc", Title: "Third", Status: types.StatusClosed, Priority: 3, IssueType: types.TypeTask},
		}

		if err := te.store.CreateIssues(ctx, issues, "tester"); err != nil {
			t.Fatalf("CreateIssues: %v", err)
		}

		for _, iss := range issues {
			te.assertRowExists(t, ctx, "issues", iss.ID)
		}

		stats, err := te.store.GetStatistics(ctx)
		if err != nil {
			t.Fatalf("GetStatistics: %v", err)
		}
		if stats.TotalIssues != 3 {
			t.Errorf("TotalIssues: got %d, want 3", stats.TotalIssues)
		}
		if stats.ClosedIssues != 1 {
			t.Errorf("ClosedIssues: got %d, want 1", stats.ClosedIssues)
		}
	})

	t.Run("upsert_skips_duplicate_events", func(t *testing.T) {
		te := newTestEnv(t, "ud")
		ctx := t.Context()

		issues := []*types.Issue{
			{ID: "ud-dup", Title: "Original", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		}
		if err := te.store.CreateIssues(ctx, issues, "tester"); err != nil {
			t.Fatalf("first CreateIssues: %v", err)
		}

		te.assertEventCount(t, ctx, "events", "ud-dup", "created", 1)

		// Re-import same ID — should upsert without extra event.
		issues2 := []*types.Issue{
			{ID: "ud-dup", Title: "Updated", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		}
		if err := te.store.CreateIssues(ctx, issues2, "tester"); err != nil {
			t.Fatalf("second CreateIssues: %v", err)
		}

		te.assertIssueTitle(t, ctx, "issues", "ud-dup", "Updated")
		te.assertEventCount(t, ctx, "events", "ud-dup", "created", 1) // still just 1
	})

	t.Run("upsert_records_events_for_new_labels", func(t *testing.T) {
		te := newTestEnv(t, "ul")
		ctx := t.Context()

		issues := []*types.Issue{
			{
				ID:        "ul-dup",
				Title:     "Original",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				Labels:    []string{"existing"},
			},
		}
		if err := te.store.CreateIssues(ctx, issues, "tester"); err != nil {
			t.Fatalf("first CreateIssues: %v", err)
		}
		te.assertEventCount(t, ctx, "events", "ul-dup", string(types.EventLabelAdded), 1)

		issues2 := []*types.Issue{
			{
				ID:        "ul-dup",
				Title:     "Updated",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
				Labels:    []string{"existing", "new"},
			},
		}
		if err := te.store.CreateIssues(ctx, issues2, "tester"); err != nil {
			t.Fatalf("second CreateIssues: %v", err)
		}

		te.assertLabelCount(t, ctx, "labels", "ul-dup", 2)
		te.assertEventCount(t, ctx, "events", "ul-dup", string(types.EventLabelAdded), 2)
	})

	t.Run("all_ephemeral", func(t *testing.T) {
		te := newTestEnv(t, "ae")
		ctx := t.Context()

		issues := []*types.Issue{
			{ID: "ae-wisp-1", Title: "Wisp 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
			{ID: "ae-wisp-2", Title: "Wisp 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		}

		if err := te.store.CreateIssues(ctx, issues, "tester"); err != nil {
			t.Fatalf("CreateIssues: %v", err)
		}

		for _, iss := range issues {
			te.assertRowExists(t, ctx, "wisps", iss.ID)
			te.assertRowNotExists(t, ctx, "issues", iss.ID)
		}
	})

	t.Run("mixed_ephemeral_and_regular", func(t *testing.T) {
		te := newTestEnv(t, "mx")
		ctx := t.Context()

		issues := []*types.Issue{
			{ID: "mx-reg1", Title: "Regular", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "mx-wisp-1", Title: "Wisp", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		}

		if err := te.store.CreateIssues(ctx, issues, "tester"); err != nil {
			t.Fatalf("CreateIssues: %v", err)
		}

		te.assertRowExists(t, ctx, "issues", "mx-reg1")
		te.assertRowExists(t, ctx, "wisps", "mx-wisp-1")
		te.assertRowNotExists(t, ctx, "issues", "mx-wisp-1")
	})

	t.Run("rejects_regular_to_wisp_batch_dependency", func(t *testing.T) {
		te := newTestEnv(t, "rw")
		ctx := t.Context()

		regular := &types.Issue{
			ID:        "rw-regular-source",
			Title:     "Regular source",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{
				DependsOnID: "rw-wisp-target",
				Type:        types.DepBlocks,
			}},
		}
		wisp := &types.Issue{
			ID:        "rw-wisp-target",
			Title:     "Wisp target",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}

		err := te.store.CreateIssues(ctx, []*types.Issue{regular, wisp}, "tester")
		if err == nil || !strings.Contains(err.Error(), "cross-bucket dependency") {
			t.Fatalf("CreateIssues error = %v, want cross-bucket dependency", err)
		}
		te.assertRowNotExists(t, ctx, "issues", regular.ID)
		te.assertRowNotExists(t, ctx, "wisps", wisp.ID)
	})

	t.Run("rejects_wisp_to_regular_batch_dependency", func(t *testing.T) {
		te := newTestEnv(t, "wr")
		ctx := t.Context()

		regular := &types.Issue{
			ID:        "wr-regular-target",
			Title:     "Regular target",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		wisp := &types.Issue{
			ID:        "wr-wisp-source",
			Title:     "Wisp source",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
			Dependencies: []*types.Dependency{{
				DependsOnID: regular.ID,
				Type:        types.DepBlocks,
			}},
		}

		err := te.store.CreateIssues(ctx, []*types.Issue{regular, wisp}, "tester")
		if err == nil || !strings.Contains(err.Error(), "cross-bucket dependency") {
			t.Fatalf("CreateIssues error = %v, want cross-bucket dependency", err)
		}
		te.assertRowNotExists(t, ctx, "issues", regular.ID)
		te.assertRowNotExists(t, ctx, "wisps", wisp.ID)
	})

	t.Run("skips_mixed_batch_dependency_when_validation_errors_are_tolerated", func(t *testing.T) {
		te := newTestEnv(t, "sk")
		ctx := t.Context()

		regular := &types.Issue{
			ID:        "sk-regular-source",
			Title:     "Regular source",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{
				DependsOnID: "sk-wisp-target",
				Type:        types.DepBlocks,
			}},
		}
		wisp := &types.Issue{
			ID:        "sk-wisp-target",
			Title:     "Wisp target",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		var skipped []string

		err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{regular, wisp}, "tester", storage.BatchCreateOptions{
			OrphanHandling:                 storage.OrphanAllow,
			SkipPrefixValidation:           true,
			SkipDependencyValidationErrors: true,
			OnSkippedDependency: func(issueID, dependsOnID, reason string) {
				skipped = append(skipped, fmt.Sprintf("%s -> %s: %s", issueID, dependsOnID, reason))
			},
		})
		if err != nil {
			t.Fatalf("CreateIssuesWithFullOptions: %v", err)
		}
		te.assertRowExists(t, ctx, "issues", regular.ID)
		te.assertRowExists(t, ctx, "wisps", wisp.ID)
		if len(skipped) != 1 ||
			!strings.Contains(skipped[0], "sk-regular-source -> sk-wisp-target") ||
			!strings.Contains(skipped[0], "cross-bucket dependency") {
			t.Fatalf("skipped = %#v, want cross-bucket dependency detail", skipped)
		}

		var regularDeps, wispDeps int
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", []any{regular.ID}, &regularDeps)
		te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", []any{regular.ID}, &wispDeps)
		if regularDeps != 0 || wispDeps != 0 {
			t.Fatalf("persisted dependency counts = regular:%d wisp:%d, want none", regularDeps, wispDeps)
		}
	})

	t.Run("rejects_wisp_dependency_cycle", func(t *testing.T) {
		te := newTestEnv(t, "wc")
		ctx := t.Context()

		wispA := &types.Issue{
			ID:        "wc-wisp-a",
			Title:     "Wisp A",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
			Dependencies: []*types.Dependency{{
				DependsOnID: "wc-wisp-b",
				Type:        types.DepBlocks,
			}},
		}
		wispB := &types.Issue{
			ID:        "wc-wisp-b",
			Title:     "Wisp B",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
			Dependencies: []*types.Dependency{{
				DependsOnID: "wc-wisp-a",
				Type:        types.DepBlocks,
			}},
		}

		err := te.store.RunInTransaction(ctx, "test: reject wisp dependency cycle", func(tx storage.Transaction) error {
			return tx.CreateIssues(ctx, []*types.Issue{wispA, wispB}, "tester")
		})
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("CreateIssues error = %v, want cycle rejection", err)
		}
		te.assertRowNotExists(t, ctx, "wisps", wispA.ID)
		te.assertRowNotExists(t, ctx, "wisps", wispB.ID)
	})

	t.Run("transaction_wisp_dependency_does_not_stage_regular_dependency_rows", func(t *testing.T) {
		te := newTestEnv(t, "ws")
		ctx := t.Context()

		dirtyOwner := &types.Issue{
			ID:        "ws-dirty-owner",
			Title:     "Dirty dependency owner",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		dirtyTarget := &types.Issue{
			ID:        "ws-dirty-target",
			Title:     "Dirty dependency target",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := te.store.CreateIssues(ctx, []*types.Issue{dirtyOwner, dirtyTarget}, "tester"); err != nil {
			t.Fatalf("CreateIssues seed regular issues: %v", err)
		}
		if err := te.store.Commit(ctx, "test: seed regular issues"); err != nil {
			t.Fatalf("Commit seed regular issues: %v", err)
		}

		te.exec(t, ctx,
			"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, ?, ?)",
			dirtyOwner.ID, dirtyTarget.ID, types.DepBlocks, time.Now().UTC(), "tester")

		wispSource := &types.Issue{
			ID:        "ws-wisp-source",
			Title:     "Wisp dependency source",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		wispTarget := &types.Issue{
			ID:        "ws-wisp-target",
			Title:     "Wisp dependency target",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		err := te.store.RunInTransaction(ctx, "test: add wisp dependency", func(tx storage.Transaction) error {
			if err := tx.CreateIssues(ctx, []*types.Issue{wispSource, wispTarget}, "tester"); err != nil {
				return err
			}
			return tx.AddDependency(ctx, &types.Dependency{
				IssueID:     wispSource.ID,
				DependsOnID: wispTarget.ID,
				Type:        types.DepBlocks,
			}, "tester")
		})
		if err != nil {
			t.Fatalf("RunInTransaction add wisp dependency: %v", err)
		}

		var committedDirtyDependencies int
		te.queryScalar(t, ctx,
			"SELECT COUNT(*) FROM dependencies AS OF 'HEAD' WHERE issue_id = ? AND depends_on_issue_id = ?",
			[]any{dirtyOwner.ID, dirtyTarget.ID}, &committedDirtyDependencies)
		if committedDirtyDependencies != 0 {
			t.Fatalf("committed dirty dependencies = %d, want 0", committedDirtyDependencies)
		}

		var workingDirtyDependencies int
		te.queryScalar(t, ctx,
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
			[]any{dirtyOwner.ID, dirtyTarget.ID}, &workingDirtyDependencies)
		if workingDirtyDependencies != 1 {
			t.Fatalf("working dirty dependencies = %d, want 1", workingDirtyDependencies)
		}
	})

	t.Run("with_labels", func(t *testing.T) {
		te := newTestEnv(t, "lb")
		ctx := t.Context()

		issues := []*types.Issue{
			{
				ID: "lb-lbl1", Title: "Labeled", Status: types.StatusOpen,
				Priority: 2, IssueType: types.TypeTask,
				Labels: []string{"bug", "urgent"},
			},
		}

		if err := te.store.CreateIssues(ctx, issues, "tester"); err != nil {
			t.Fatalf("CreateIssues: %v", err)
		}

		te.assertRowExists(t, ctx, "issues", "lb-lbl1")
		te.assertLabelCount(t, ctx, "labels", "lb-lbl1", 2)
	})

	t.Run("hierarchical_child_counters", func(t *testing.T) {
		te := newTestEnv(t, "hc")
		ctx := t.Context()

		issues := []*types.Issue{
			{ID: "hc-parent", Title: "Parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "hc-parent.1", Title: "Child 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "hc-parent.2", Title: "Child 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "hc-parent.5", Title: "Child 5", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		}

		if err := te.store.CreateIssues(ctx, issues, "tester"); err != nil {
			t.Fatalf("CreateIssues: %v", err)
		}

		for _, iss := range issues {
			te.assertRowExists(t, ctx, "issues", iss.ID)
		}
		te.assertChildCounter(t, ctx, "hc-parent", 5)
	})

	t.Run("hierarchical_child_counters_mixed_wisp_and_issue", func(t *testing.T) {
		te := newTestEnv(t, "mh")
		ctx := t.Context()

		issues := []*types.Issue{
			{ID: "mh-issue-parent", Title: "Issue parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "mh-issue-parent.1", Title: "Issue child 1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "mh-issue-parent.4", Title: "Issue child 4", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
			{ID: "mh-wisp-parent", Title: "Wisp parent", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
			{ID: "mh-wisp-parent.2", Title: "Wisp child 2", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
			{ID: "mh-wisp-parent.7", Title: "Wisp child 7", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true},
		}

		if err := te.store.CreateIssues(ctx, issues, "tester"); err != nil {
			t.Fatalf("CreateIssues: %v", err)
		}

		te.assertChildCounter(t, ctx, "mh-issue-parent", 4)
		te.assertWispChildCounter(t, ctx, "mh-wisp-parent", 7)
		te.assertNoChildCounterRow(t, ctx, "wisp_child_counters", "mh-issue-parent")
		te.assertNoChildCounterRow(t, ctx, "child_counters", "mh-wisp-parent")
	})

	t.Run("wisp_delete_clears_child_counter", func(t *testing.T) {
		te := newTestEnv(t, "wd")
		ctx := t.Context()

		parent := &types.Issue{
			ID: "wd-wisp-parent", Title: "Wisp parent", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
		}
		if err := te.store.CreateIssue(ctx, parent, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if _, err := te.store.GetNextChildID(ctx, "wd-wisp-parent"); err != nil {
			t.Fatalf("GetNextChildID: %v", err)
		}
		te.assertWispChildCounter(t, ctx, "wd-wisp-parent", 1)

		if err := te.store.DeleteIssue(ctx, "wd-wisp-parent"); err != nil {
			t.Fatalf("DeleteIssue: %v", err)
		}
		te.assertNoChildCounterRow(t, ctx, "wisp_child_counters", "wd-wisp-parent")
	})

	t.Run("prefix_validation_rejects_mismatch", func(t *testing.T) {
		te := newTestEnv(t, "pv")
		ctx := t.Context()

		issues := []*types.Issue{
			{ID: "wrong-prefix-1", Title: "Bad prefix", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		}

		err := te.store.CreateIssues(ctx, issues, "tester")
		if err == nil {
			t.Fatal("expected prefix validation error")
		}
		if !strings.Contains(err.Error(), "prefix") {
			t.Errorf("expected prefix error, got: %v", err)
		}
	})
}

func TestHookFiringStoreCreateIssuesFiresDependencyUpdatesFromEmbeddedStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The hook runner on Windows executes hook files directly via CreateProcess,
		// which has no shebang dispatch. The extensionless #!/bin/sh hook written by
		// newEmbeddedHookStore cannot be executed as a shell script, so no payload is
		// ever logged and the assertions fail. Same limitation already skipped in
		// internal/hooks/hooks_test.go. See: https://github.com/gastownhall/beads/issues/3800
		t.Skip("hook script execution not supported on Windows - see GH#3800")
	}
	t.Run("non_transactional", func(t *testing.T) {
		te := newTestEnv(t, "hk")
		ctx := t.Context()
		store, logPath := newEmbeddedHookStore(t, te)

		source := &types.Issue{
			ID:        "hk-source",
			Title:     "Source",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{DependsOnID: "hk-target-a", Type: types.DepBlocks},
				{DependsOnID: "hk-target-b", Type: types.DepBlocks},
			},
		}
		targetA := &types.Issue{ID: "hk-target-a", Title: "Target A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		targetB := &types.Issue{ID: "hk-target-b", Title: "Target B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

		if err := store.CreateIssues(ctx, []*types.Issue{source, targetA, targetB}, "tester"); err != nil {
			t.Fatalf("CreateIssues: %v", err)
		}

		assertDependencyHookPayloads(t, logPath, []string{"hk-target-a", "hk-target-b"})
	})

	t.Run("transactional", func(t *testing.T) {
		te := newTestEnv(t, "txh")
		ctx := t.Context()
		store, logPath := newEmbeddedHookStore(t, te)

		source := &types.Issue{
			ID:        "txh-source",
			Title:     "Source",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{DependsOnID: "txh-target-a", Type: types.DepBlocks},
				{DependsOnID: "txh-target-b", Type: types.DepBlocks},
			},
		}
		targetA := &types.Issue{ID: "txh-target-a", Title: "Target A", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		targetB := &types.Issue{ID: "txh-target-b", Title: "Target B", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}

		err := store.RunInTransaction(ctx, "test: hook batch deps", func(tx storage.Transaction) error {
			return tx.CreateIssues(ctx, []*types.Issue{source, targetA, targetB}, "tester")
		})
		if err != nil {
			t.Fatalf("RunInTransaction: %v", err)
		}

		assertDependencyHookPayloads(t, logPath, []string{"txh-target-a", "txh-target-b"})
	})
}

func newEmbeddedHookStore(t *testing.T, te *testEnv) (storage.DoltStorage, string) {
	t.Helper()
	hooksDir := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("MkdirAll hooks: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "updates.jsonl")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$2" = "update" ]; then
  payload="$(cat)"
  printf '%%s\n' "$payload" >> %q
else
  cat >/dev/null
fi
`, logPath)
	if err := os.WriteFile(filepath.Join(hooksDir, hooks.HookOnUpdate), []byte(script), 0o755); err != nil {
		t.Fatalf("write update hook: %v", err)
	}
	return storage.NewHookFiringStore(te.store, hooks.NewRunner(hooksDir)), logPath
}

func assertDependencyHookPayloads(t *testing.T, logPath string, targets []string) {
	t.Helper()
	var payloads []types.Issue
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		payloads = readHookPayloads(t, logPath)
		if len(payloads) >= len(targets) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(payloads) != len(targets) {
		t.Fatalf("dependency update hook count = %d, want %d", len(payloads), len(targets))
	}
	sort.Slice(payloads, func(i, j int) bool {
		return len(payloads[i].Dependencies) < len(payloads[j].Dependencies)
	})
	for i, target := range targets {
		if got := len(payloads[i].Dependencies); got != i+1 {
			t.Fatalf("payload %d dependency count = %d, want %d", i, got, i+1)
		}
		if got := payloads[i].Dependencies[i].DependsOnID; got != target {
			t.Fatalf("payload %d dependency target = %q, want %q", i, got, target)
		}
	}
}

func readHookPayloads(t *testing.T, logPath string) []types.Issue {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read hook log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var payloads []types.Issue
	for _, line := range lines {
		if line == "" {
			continue
		}
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			t.Fatalf("decode hook payload %q: %v", line, err)
		}
		payloads = append(payloads, issue)
	}
	return payloads
}
