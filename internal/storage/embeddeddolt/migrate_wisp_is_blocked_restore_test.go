//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedWispIsBlockedRestoreAtIgnored32 is the embedded twin of the dolt
// package's ignored-0032 tests (bd-eggelc, refs #4468/#5257): a clone past
// ignored 15 whose wisps lost is_blocked and its index, rewound to cursor 31,
// gets both back on the next Open, with the wisp flags derived rather than
// left at the column's DEFAULT 0, and nothing durable or committed touched.
func TestEmbeddedWispIsBlockedRestoreAtIgnored32(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	fx := newPristineEmbeddedDoltFixture(t, "wib")
	store := fx.store
	t.Cleanup(func() {
		if store != nil {
			closeEmbeddedDoltStore(t, store)
		}
	})

	// A: open durable blocker. B: wisp blocked by A. C: child wisp of B.
	// D: independent wisp.
	for _, seed := range []struct {
		id   string
		wisp bool
	}{{"wib-a", false}, {"wib-b", true}, {"wib-c", true}, {"wib-d", true}} {
		issue := &types.Issue{ID: seed.id, Title: seed.id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: seed.wisp}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", seed.id, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "wib-b", DependsOnID: "wib-a", Type: types.DepBlocks},
		{IssueID: "wib-c", DependsOnID: "wib-b", Type: types.DepParentChild},
	} {
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency(%s -> %s): %v", dep.IssueID, dep.DependsOnID, err)
		}
	}
	if err := store.Commit(ctx, "seed wisp is_blocked graph"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	if got := wispBlockedFlags(t, ctx, fx, "wib-b", "wib-c", "wib-d"); !slices.Equal(got, []int{1, 1, 0}) {
		t.Fatalf("seeded wisp is_blocked (b, c, d) = %v, want [1 1 0]", got)
	}
	closeEmbeddedDoltStore(t, store)
	store = nil

	// The drift, on the closed clone: established wisp timestamps, the index
	// gone before the column it names, and the cursor rewound to 31 so 0032 is
	// pending.
	withWispSQL(t, ctx, fx, func(db *sql.DB) {
		for _, stmt := range []string{
			"UPDATE wisps SET created_at = '2001-02-03 04:05:06', updated_at = '2001-02-03 04:05:06'",
			"DROP INDEX idx_wisps_is_blocked ON wisps",
			"ALTER TABLE wisps DROP COLUMN is_blocked",
			"DELETE FROM ignored_schema_migrations WHERE version >= 32",
		} {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	})

	// What the heal must leave alone: HEAD, durable rows, the dirty table set
	// and the wisp timestamps.
	untouched := func() string {
		return wispText(t, ctx, fx, `SELECT CONCAT(
			'head=', HASHOF('HEAD'),
			' issues=', DOLT_HASHOF_TABLE('issues'),
			' dependencies=', DOLT_HASHOF_TABLE('dependencies'),
			' dirty=', COALESCE((SELECT GROUP_CONCAT(table_name ORDER BY table_name) FROM dolt_status), ''),
			' wisps=', (SELECT GROUP_CONCAT(CONCAT(id, '@', created_at, '@', updated_at) ORDER BY id) FROM wisps))`)
	}
	before := untouched()

	reopened, err := embeddeddolt.Open(ctx, fx.beadsDir, fx.database, "main")
	if err != nil {
		t.Fatalf("Open (heal): %v", err)
	}
	store = reopened

	if got := wispScalar(t, ctx, fx, `SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND INDEX_NAME = 'idx_wisps_is_blocked'`); got == 0 {
		t.Fatal("idx_wisps_is_blocked not restored on Open at ignored cursor 31")
	}
	if got := wispScalar(t, ctx, fx, "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations"); got != 32 {
		t.Fatalf("ignored cursor after the heal = %d, want 32", got)
	}
	if got := wispBlockedFlags(t, ctx, fx, "wib-b", "wib-c", "wib-d"); !slices.Equal(got, []int{1, 1, 0}) {
		t.Fatalf("restored wisp is_blocked (b, c, d) = %v, want [1 1 0]", got)
	}
	if after := untouched(); after != before {
		t.Errorf("the heal changed history, durable rows, dirty tables or wisp timestamps:\n got %s\nwant %s", after, before)
	}
	ready, err := store.GetReadyWork(ctx, types.WorkFilter{IncludeEphemeral: true})
	if err != nil {
		t.Fatalf("GetReadyWork(IncludeEphemeral): %v", err)
	}
	readyIDs := map[string]bool{}
	for _, issue := range ready {
		readyIDs[issue.ID] = true
	}
	for id, want := range map[string]bool{"wib-a": true, "wib-d": true, "wib-b": false, "wib-c": false} {
		if readyIDs[id] != want {
			t.Errorf("ready work has %s = %v after the heal, want %v", id, readyIDs[id], want)
		}
	}
}

// withWispSQL runs fn on a short-lived raw connection to the fixture.
func withWispSQL(t *testing.T, ctx context.Context, fx *pristineEmbeddedDoltFixture, fn func(*sql.DB)) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, fx.dataDir, fx.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	fn(db)
}

func wispScalar(t *testing.T, ctx context.Context, fx *pristineEmbeddedDoltFixture, query string, args ...any) int {
	t.Helper()
	var n int
	withWispSQL(t, ctx, fx, func(db *sql.DB) {
		if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	})
	return n
}

func wispText(t *testing.T, ctx context.Context, fx *pristineEmbeddedDoltFixture, query string) string {
	t.Helper()
	var s sql.NullString
	withWispSQL(t, ctx, fx, func(db *sql.DB) {
		if err := db.QueryRowContext(ctx, query).Scan(&s); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	})
	return s.String
}

func wispBlockedFlags(t *testing.T, ctx context.Context, fx *pristineEmbeddedDoltFixture, ids ...string) []int {
	t.Helper()
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		out = append(out, wispScalar(t, ctx, fx, "SELECT is_blocked FROM wisps WHERE id = ?", id))
	}
	return out
}
