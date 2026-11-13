//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
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

	seedWispIsBlockedGraph(t, ctx, fx, store)
	closeEmbeddedDoltStore(t, store)
	store = nil
	driftWispIsBlocked(t, ctx, fx)

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
	// The default filter goes through the same wisp stale-flag probe and
	// blocked-wisp filter, which read wisps.is_blocked.
	defaultReady, err := store.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork(default): %v", err)
	}
	defaultIDs := map[string]bool{}
	for _, issue := range defaultReady {
		defaultIDs[issue.ID] = true
	}
	for id, want := range map[string]bool{"wib-a": true, "wib-b": false, "wib-c": false} {
		if defaultIDs[id] != want {
			t.Errorf("default ready work has %s = %v after the heal, want %v", id, defaultIDs[id], want)
		}
	}
}

// TestEmbeddedWispIsBlockedRestoreUnblocksClose is the embedded twin of
// AC-3: closing a blocker recomputes its dependents through a mark statement
// whose union leg joins wisps and reads p.is_blocked, so on the drifted clone
// the close failed even for a durable dependent. After the heal the close
// succeeds and unblocks the durable dependent X and the wisps B and C.
func TestEmbeddedWispIsBlockedRestoreUnblocksClose(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := t.Context()
	fx := newPristineEmbeddedDoltFixture(t, "wib")
	store := fx.store
	t.Cleanup(func() {
		if store != nil {
			closeEmbeddedDoltStore(t, store)
		}
	})

	x := &types.Issue{ID: "wib-x", Title: "wib-x", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, x, "test"); err != nil {
		t.Fatalf("CreateIssue(wib-x): %v", err)
	}
	seedWispIsBlockedGraph(t, ctx, fx, store)
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: "wib-x", DependsOnID: "wib-a", Type: types.DepBlocks}, "test"); err != nil {
		t.Fatalf("AddDependency(wib-x -> wib-a): %v", err)
	}
	if err := store.Commit(ctx, "seed durable dependent"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	closeEmbeddedDoltStore(t, store)
	store = nil
	driftWispIsBlocked(t, ctx, fx)

	reopened, err := embeddeddolt.Open(ctx, fx.beadsDir, fx.database, "main")
	if err != nil {
		t.Fatalf("Open (heal): %v", err)
	}
	store = reopened
	if err := store.CloseIssue(ctx, "wib-a", "done", "test", ""); err != nil {
		t.Fatalf("CloseIssue(wib-a) on the reopened clone: %v", err)
	}
	if got := wispScalar(t, ctx, fx, "SELECT is_blocked FROM issues WHERE id = ?", "wib-x"); got != 0 {
		t.Errorf("durable dependent is_blocked after closing its blocker = %d, want 0", got)
	}
	if got := wispBlockedFlags(t, ctx, fx, "wib-b", "wib-c"); !slices.Equal(got, []int{0, 0}) {
		t.Errorf("wisp dependents is_blocked (b, c) after closing their blocker = %v, want [0 0]", got)
	}
}

// TestEmbeddedWispIsBlockedRepairFailureRetries is AC-7 on the embedded open
// path: a fault at any stage of the ignored 0032 restore must fail Open with
// the fault surfaced and record no ignored 0032 row, and the next Open with
// the fault cleared must resume the repair to completion. The stage strings
// mirror schema's wispIsBlockedRestoreAfter* constants as literals, so a
// rename there fails these tests rather than silently disarming them.
func TestEmbeddedWispIsBlockedRepairFailureRetries(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, stage := range []string{"after_column", "after_recompute", "after_index"} {
		t.Run(stage, func(t *testing.T) {
			ctx := t.Context()
			fx := newPristineEmbeddedDoltFixture(t, "wib")
			seedWispIsBlockedGraph(t, ctx, fx, fx.store)
			closeEmbeddedDoltStore(t, fx.store)
			driftWispIsBlocked(t, ctx, fx)

			fault := errors.New("injected fault")
			restore := schema.SetWispIsBlockedRestoreFaultHookForTest(func(at string) error {
				if at == stage {
					return fault
				}
				return nil
			})
			broken, err := embeddeddolt.Open(ctx, fx.beadsDir, fx.database, "main")
			restore()
			if err == nil {
				closeEmbeddedDoltStore(t, broken)
				t.Fatalf("Open with a fault pinned at %s must fail and leave 0032 pending", stage)
			}
			if !strings.Contains(err.Error(), "wisp is_blocked restore fault at "+stage) {
				t.Fatalf("Open error = %v, want the %s fault surfaced", err, stage)
			}
			if got := wispScalar(t, ctx, fx, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); got != 0 {
				t.Fatalf("failed pass recorded ignored 0032 %d times, want 0 (the repair must stay pending for retry)", got)
			}

			store, err := embeddeddolt.Open(ctx, fx.beadsDir, fx.database, "main")
			if err != nil {
				t.Fatalf("Open after the fault cleared: %v", err)
			}
			closeEmbeddedDoltStore(t, store)
			if got := wispScalar(t, ctx, fx, `SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
				WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND INDEX_NAME = 'idx_wisps_is_blocked'`); got == 0 {
				t.Fatal("idx_wisps_is_blocked not restored by the retried Open")
			}
			if got := wispBlockedFlags(t, ctx, fx, "wib-b", "wib-c", "wib-d"); !slices.Equal(got, []int{1, 1, 0}) {
				t.Fatalf("retried wisp is_blocked (b, c, d) = %v, want [1 1 0]: the retry must resume through the recompute", got)
			}
			if got := wispScalar(t, ctx, fx, "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); got != 1 {
				t.Fatalf("retried Open recorded ignored 0032 %d times, want exactly 1", got)
			}
		})
	}
}

// seedWispIsBlockedGraph commits AC-2's graph on store: A, an open durable
// blocker; B, a wisp blocked by A; C, a child wisp of B; D, an independent
// wisp.
func seedWispIsBlockedGraph(t *testing.T, ctx context.Context, fx *pristineEmbeddedDoltFixture, store *embeddeddolt.EmbeddedDoltStore) {
	t.Helper()
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
}

// driftWispIsBlocked applies the drift to the closed clone: established wisp
// timestamps, the index gone before the column it names, and the cursor
// rewound to 31 so 0032 is pending.
func driftWispIsBlocked(t *testing.T, ctx context.Context, fx *pristineEmbeddedDoltFixture) {
	t.Helper()
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
