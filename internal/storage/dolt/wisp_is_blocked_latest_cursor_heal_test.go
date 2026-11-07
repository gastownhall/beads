package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// The wisps.is_blocked repairs at ignored 7 and 15 fire only while one of
// those versions is pending, so a clone past ignored 15 whose wisps table lost
// is_blocked never healed: `bd ready` and `bd create --ephemeral` failed with
// "table does not have column is_blocked" (bd-eggelc, PR #8111, refs
// #4468/#5257). Ignored 0032's pre-migration repair is the heal. These tests
// build that clone through the production store: a store migrated to latest,
// then the column and its index dropped and the ignored cursor rewound to 31
// (a no-op where 31 is still latest), then reopened.

const (
	wispIsBlockedColumnProbe = `SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`
	wispIsBlockedIndexProbe = `SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND INDEX_NAME = 'idx_wisps_is_blocked'`
)

// wispIsBlockedClone is one test's own database, opened through New.
type wispIsBlockedClone struct {
	t     *testing.T
	ctx   context.Context
	cfg   *Config
	store *DoltStore
}

func newWispIsBlockedClone(t *testing.T) *wispIsBlockedClone {
	t.Helper()
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	c := &wispIsBlockedClone{t: t, ctx: ctx, cfg: &Config{
		Path:            t.TempDir(),
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        uniqueTestDBName(t),
		CreateIfMissing: true,
		MaxOpenConns:    1,
	}}
	store, err := openStoreWithOwnBudget(t, c.cfg)
	if err != nil {
		t.Fatalf("New (create): %v", err)
	}
	c.store = store
	if err := setConfigWithOwnBudget(t, store, "issue_prefix", "wib"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), testTimeout)
		defer dropCancel()
		_, _ = c.store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", c.cfg.Database))
		c.store.Close()
	})
	return c
}

func (c *wispIsBlockedClone) exec(stage, query string, args ...any) {
	c.t.Helper()
	if _, err := c.store.db.ExecContext(c.ctx, query, args...); err != nil {
		c.t.Fatalf("%s: %v", stage, err)
	}
}

func (c *wispIsBlockedClone) count(stage, query string, args ...any) int {
	c.t.Helper()
	var n int
	if err := c.store.db.QueryRowContext(c.ctx, query, args...).Scan(&n); err != nil {
		c.t.Fatalf("%s: %v", stage, err)
	}
	return n
}

func (c *wispIsBlockedClone) scalar(stage, query string, args ...any) string {
	c.t.Helper()
	var s sql.NullString
	if err := c.store.db.QueryRowContext(c.ctx, query, args...).Scan(&s); err != nil {
		c.t.Fatalf("%s: %v", stage, err)
	}
	return s.String
}

func (c *wispIsBlockedClone) cursor() int {
	c.t.Helper()
	return c.count("ignored cursor", "SELECT COALESCE(MAX(version), 0) FROM ignored_schema_migrations")
}

// breakShape drops the index BEFORE the column (the column cannot go while
// an index names it) and rewinds the ignored cursor to 31, so 0032 is pending
// on a build that ships it and the cursor is unchanged on one that does not.
func (c *wispIsBlockedClone) breakShape() {
	c.t.Helper()
	if c.count("fresh column probe", wispIsBlockedColumnProbe) != 1 {
		c.t.Fatal("fresh workspace: wisps.is_blocked missing after full migration run")
	}
	c.exec("drop idx_wisps_is_blocked", "DROP INDEX idx_wisps_is_blocked ON wisps")
	c.exec("drop wisps.is_blocked", "ALTER TABLE wisps DROP COLUMN is_blocked")
	c.exec("rewind ignored cursor", "DELETE FROM ignored_schema_migrations WHERE version >= 32")
	if c.count("after break", wispIsBlockedColumnProbe) != 0 {
		c.t.Fatal("test setup failed to remove wisps.is_blocked")
	}
	if got := c.cursor(); got != 31 {
		c.t.Fatalf("ignored cursor after rewind = %d, want 31", got)
	}
}

// reopen closes the store and opens it again through New, which runs the
// production migration pass.
func (c *wispIsBlockedClone) reopen() {
	c.t.Helper()
	c.store.Close()
	store, err := openStoreWithOwnBudget(c.t, c.cfg)
	if err != nil {
		c.t.Fatalf("New (reopen): %v", err)
	}
	c.store = store
}

func (c *wispIsBlockedClone) create(issue *types.Issue) {
	c.t.Helper()
	if err := c.store.CreateIssue(c.ctx, issue, "test"); err != nil {
		c.t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
	}
}

func (c *wispIsBlockedClone) depend(issueID, dependsOnID string, kind types.DependencyType) {
	c.t.Helper()
	dep := &types.Dependency{IssueID: issueID, DependsOnID: dependsOnID, Type: kind}
	if err := c.store.AddDependency(c.ctx, dep, "test"); err != nil {
		c.t.Fatalf("AddDependency(%s -> %s): %v", issueID, dependsOnID, err)
	}
}

// wispIsBlockedGraph is AC-2's graph: open durable blocker A, wisp B blocked
// by A, child wisp C of B, independent wisp D, plus an independent durable
// issue E for the "durable rows unchanged" half of AC-5.
type wispIsBlockedGraph struct{ a, b, c, d, e string }

func (c *wispIsBlockedClone) seedGraph() wispIsBlockedGraph {
	c.t.Helper()
	g := wispIsBlockedGraph{a: "wib-a", b: "wib-b", c: "wib-c", d: "wib-d", e: "wib-e"}
	issue := func(id string, wisp bool) *types.Issue {
		return &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: wisp}
	}
	c.create(issue(g.a, false))
	c.create(issue(g.e, false))
	c.create(issue(g.b, true))
	c.create(issue(g.c, true))
	c.create(issue(g.d, true))
	c.depend(g.b, g.a, types.DepBlocks)
	c.depend(g.c, g.b, types.DepParentChild)
	// The live derivation is the expected value, not a hand-written one.
	if got := c.blockedFlags("wisps", g.b, g.c, g.d); !slices.Equal(got, []int{1, 1, 0}) {
		c.t.Fatalf("seeded wisp is_blocked (b, c, d) = %v, want [1 1 0]", got)
	}
	return g
}

func (c *wispIsBlockedClone) blockedFlags(table string, ids ...string) []int {
	c.t.Helper()
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		//nolint:gosec // G201: table is a test constant.
		out = append(out, c.count("is_blocked of "+id, fmt.Sprintf("SELECT is_blocked FROM %s WHERE id = ?", table), id))
	}
	return out
}

func (c *wispIsBlockedClone) readyIDs() map[string]bool {
	c.t.Helper()
	ready, err := c.store.GetReadyWork(c.ctx, types.WorkFilter{IncludeEphemeral: true})
	if err != nil {
		c.t.Fatalf("GetReadyWork(IncludeEphemeral): %v", err)
	}
	ids := make(map[string]bool, len(ready))
	for _, issue := range ready {
		ids[issue.ID] = true
	}
	return ids
}

// TestDoltNew_WispIsBlockedSelfHealAtLatestIgnoredCursor_RealDolt is AC-1:
// a clone at ignored cursor 31 whose wisps lost is_blocked and its index gets
// both back, at 0006's shape, and records 0032.
func TestDoltNew_WispIsBlockedSelfHealAtLatestIgnoredCursor_RealDolt(t *testing.T) {
	c := newWispIsBlockedClone(t)
	c.breakShape()
	c.reopen()

	if c.count("after reopen column probe", wispIsBlockedColumnProbe) != 1 {
		t.Fatalf("wisps.is_blocked not restored on reopen at ignored cursor 31: clones past ignored 0015 stay broken")
	}
	columnType := c.scalar("column type", `SELECT COLUMN_TYPE FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`)
	nullable := c.scalar("column nullability", `SELECT IS_NULLABLE FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`)
	columnDefault := c.scalar("column default", `SELECT COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND COLUMN_NAME = 'is_blocked'`)
	if !strings.EqualFold(columnType, "tinyint(1)") || nullable != "NO" || columnDefault != "0" {
		t.Fatalf("restored wisps.is_blocked = %s nullable=%s default=%q, want tinyint(1) NOT NULL DEFAULT 0",
			columnType, nullable, columnDefault)
	}
	indexColumns := c.scalar("index columns", `SELECT GROUP_CONCAT(COLUMN_NAME ORDER BY SEQ_IN_INDEX)
		FROM INFORMATION_SCHEMA.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'wisps' AND INDEX_NAME = 'idx_wisps_is_blocked'`)
	if indexColumns != "is_blocked,status" {
		t.Fatalf("idx_wisps_is_blocked columns = %q, want is_blocked,status", indexColumns)
	}
	if got := c.cursor(); got != 32 {
		t.Fatalf("ignored cursor after the heal = %d, want 32", got)
	}
}

// TestDoltNew_WispIsBlockedRestoreRecomputesFlags_RealDolt is AC-2: the
// restored column is not left at its DEFAULT 0. Ready work reads around stale
// flags set to 1, never around zeros, so a shape-only heal would show the
// blocked wisps B and C as ready.
func TestDoltNew_WispIsBlockedRestoreRecomputesFlags_RealDolt(t *testing.T) {
	c := newWispIsBlockedClone(t)
	g := c.seedGraph()
	c.breakShape()
	c.reopen()

	if got := c.blockedFlags("wisps", g.b, g.c, g.d); !slices.Equal(got, []int{1, 1, 0}) {
		t.Fatalf("restored wisp is_blocked (b, c, d) = %v, want [1 1 0]", got)
	}
	if got := c.blockedFlags("issues", g.a); !slices.Equal(got, []int{0}) {
		t.Fatalf("blocker a is_blocked = %v, want [0]", got)
	}
	ready := c.readyIDs()
	for _, id := range []string{g.a, g.d} {
		if !ready[id] {
			t.Errorf("ready work misses %s after the heal", id)
		}
	}
	for _, id := range []string{g.b, g.c} {
		if ready[id] {
			t.Errorf("ready work includes blocked wisp %s after the heal", id)
		}
	}
}

// TestDoltNew_WispIsBlockedRestoreAllowsEphemeralCreate_RealDolt is AC-4:
// creating a wisp recomputes is_blocked, which failed on the missing column.
func TestDoltNew_WispIsBlockedRestoreAllowsEphemeralCreate_RealDolt(t *testing.T) {
	c := newWispIsBlockedClone(t)
	c.seedGraph()
	c.breakShape()
	c.reopen()

	const id = "wib-new"
	c.create(&types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: true})
	if got := c.blockedFlags("wisps", id); !slices.Equal(got, []int{0}) {
		t.Fatalf("new independent wisp is_blocked = %v, want [0]", got)
	}
}

// TestDoltNew_WispIsBlockedRestoreHealthyCloneConverges_RealDolt is AC-6's
// store half: a healthy clone with 0032 pending records it, keeps its flags,
// and a converged reopen leaves it recorded. The probe count is pinned in the
// schema package (TestIgnored32HealthyCloneOnlyProbes).
func TestDoltNew_WispIsBlockedRestoreHealthyCloneConverges_RealDolt(t *testing.T) {
	c := newWispIsBlockedClone(t)
	g := c.seedGraph()
	c.exec("rewind ignored cursor", "DELETE FROM ignored_schema_migrations WHERE version >= 32")
	c.reopen()

	if got := c.cursor(); got != 32 {
		t.Fatalf("ignored cursor after migrating a healthy clone = %d, want 32", got)
	}
	if got := c.blockedFlags("wisps", g.b, g.c, g.d); !slices.Equal(got, []int{1, 1, 0}) {
		t.Fatalf("healthy wisp is_blocked (b, c, d) = %v, want [1 1 0]", got)
	}
	c.reopen()
	if got := c.count("cursor rows", "SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = 32"); got != 1 {
		t.Fatalf("ignored cursor rows for 32 after a converged reopen = %d, want 1", got)
	}
}

// TestDoltNew_WispIsBlockedRestoreKeepsHistoryAndTimestamps_RealDolt is AC-5:
// the heal writes only the clone-local wisps flags. Wisp timestamps, durable
// rows, HEAD and the dirty table set stay as they were.
func TestDoltNew_WispIsBlockedRestoreKeepsHistoryAndTimestamps_RealDolt(t *testing.T) {
	c := newWispIsBlockedClone(t)
	c.seedGraph()
	// Established timestamps: far from "now", so a restamp cannot hide.
	c.exec("pin wisp timestamps", "UPDATE wisps SET updated_at = '2001-02-03 04:05:06', created_at = '2001-02-03 04:05:06'")

	wispStamps := func() string {
		return c.scalar("wisp timestamps", `SELECT GROUP_CONCAT(CONCAT(id, '@', created_at, '@', updated_at) ORDER BY id) FROM wisps`)
	}
	durable := func() string {
		return c.scalar("durable rows", `SELECT CONCAT(DOLT_HASHOF_TABLE('issues'), '/', DOLT_HASHOF_TABLE('dependencies'))`)
	}
	head := func() string { return c.scalar("HEAD", "SELECT HASHOF('HEAD')") }
	dirty := func() string {
		return c.scalar("dirty tables", "SELECT COALESCE(GROUP_CONCAT(table_name ORDER BY table_name), '') FROM dolt_status")
	}

	c.breakShape()
	stampsBefore, durableBefore, headBefore, dirtyBefore := wispStamps(), durable(), head(), dirty()
	c.reopen()

	if c.count("after reopen column probe", wispIsBlockedColumnProbe) != 1 {
		t.Fatal("wisps.is_blocked not restored on reopen")
	}
	if got := wispStamps(); got != stampsBefore {
		t.Errorf("wisp timestamps changed by the heal:\n got %s\nwant %s", got, stampsBefore)
	}
	if got := durable(); got != durableBefore {
		t.Errorf("durable issues/dependencies changed by the heal: %s -> %s", durableBefore, got)
	}
	if got := head(); got != headBefore {
		t.Errorf("HEAD moved during the heal: %s -> %s", headBefore, got)
	}
	if got := dirty(); got != dirtyBefore {
		t.Errorf("dirty tables after the heal = %q, want %q", got, dirtyBefore)
	}
}
