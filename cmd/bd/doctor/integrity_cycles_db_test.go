//go:build cgo

package doctor

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

func execCycleFixture(t *testing.T, db *sql.DB, stmt string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), stmt, args...); err != nil {
		t.Fatalf("fixture %q: %v", stmt, err)
	}
}

// createCycleIssues creates open issues with the given explicit ids so raw
// dependency inserts (which bypass the add-time cycle guard on purpose)
// satisfy the issues foreign key.
func createCycleIssues(t *testing.T, store *dolt.DoltStore, ids ...string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		if err := store.CreateIssue(ctx, newTestIssue(id), "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
}

// createCycleWisps creates wisps (ephemeral issues route to the wisps table)
// with the given explicit ids.
func createCycleWisps(t *testing.T, store *dolt.DoltStore, ids ...string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		issue := newTestIssue(id)
		issue.Ephemeral = true
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(wisp %s): %v", id, err)
		}
	}
}

// TestCheckDependencyCycles_WispOnlyCycle: a cycle that exists only in
// wisp_dependencies must be reported, matching issueops.DetectCyclesInTx and
// 'bd dep cycles', which read dependencies UNION wisp_dependencies.
func TestCheckDependencyCycles_WispOnlyCycle(t *testing.T) {
	store := newTestDoltStore(t, "test")
	db := store.UnderlyingDB()

	createCycleWisps(t, store, "test-w1", "test-w2")
	execCycleFixture(t, db, `INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type)
		VALUES (UUID(), 'test-w1', 'test-w2', 'blocks'), (UUID(), 'test-w2', 'test-w1', 'blocks')`)

	check := checkDependencyCyclesWithStore(store)
	if check.Status != StatusError {
		t.Fatalf("Status = %q (%s: %s), want %q for a wisp-only cycle", check.Status, check.Message, check.Detail, StatusError)
	}
}

// TestCheckDependencyCycles_CrossTableCycle: half the cycle in dependencies,
// half in wisp_dependencies. Reading either table alone finds no cycle.
func TestCheckDependencyCycles_CrossTableCycle(t *testing.T) {
	store := newTestDoltStore(t, "test")
	db := store.UnderlyingDB()

	createCycleIssues(t, store, "test-ci1")
	createCycleWisps(t, store, "test-cw1")
	execCycleFixture(t, db, `INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_by)
		VALUES ('cycdep-cross', 'test-ci1', 'test-cw1', 'blocks', 'test')`)
	execCycleFixture(t, db, `INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type)
		VALUES (UUID(), 'test-cw1', 'test-ci1', 'blocks')`)

	check := checkDependencyCyclesWithStore(store)
	if check.Status != StatusError {
		t.Fatalf("Status = %q (%s: %s), want %q for a cross-table cycle", check.Status, check.Message, check.Detail, StatusError)
	}
}

// TestCheckDependencyCycles_NonBlockingLoopsIgnored: non-blocking loops are
// legitimate in both tables and must not be reported.
func TestCheckDependencyCycles_NonBlockingLoopsIgnored(t *testing.T) {
	store := newTestDoltStore(t, "test")
	db := store.UnderlyingDB()

	createCycleIssues(t, store, "test-ri1", "test-ri2")
	createCycleWisps(t, store, "test-rw1", "test-rw2")
	execCycleFixture(t, db, `INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by)
		VALUES ('cycdep-r1', 'test-ri1', 'test-ri2', 'related', 'test'),
		       ('cycdep-r2', 'test-ri2', 'test-ri1', 'related', 'test')`)
	execCycleFixture(t, db, `INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type)
		VALUES (UUID(), 'test-rw1', 'test-rw2', 'related'), (UUID(), 'test-rw2', 'test-rw1', 'related')`)

	check := checkDependencyCyclesWithStore(store)
	if check.Status != StatusOK {
		t.Fatalf("Status = %q (%s: %s), want %q for non-blocking loops", check.Status, check.Message, check.Detail, StatusOK)
	}
}

// TestLoadDependencyEdges_MultiPage: with a page size smaller than the edge
// count, pagination must still assemble the exact same graph a single page
// would, and pages must chain across both tables.
func TestLoadDependencyEdges_MultiPage(t *testing.T) {
	store := newTestDoltStore(t, "test")
	db := store.UnderlyingDB()

	createCycleIssues(t, store, "test-p1", "test-p2", "test-p3", "test-p4", "test-p5")
	createCycleWisps(t, store, "test-pw1", "test-pw2")
	// Five durable blocking edges (cycle p1→p2→p3→p1 plus chain p4→p5, p5→p1)
	// and a wisp edge, so page size 2 forces multiple pages per table.
	execCycleFixture(t, db, `INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES
		('cycdep-p1', 'test-p1', 'test-p2', 'blocks', 'test'),
		('cycdep-p2', 'test-p2', 'test-p3', 'blocks', 'test'),
		('cycdep-p3', 'test-p3', 'test-p1', 'blocks', 'test'),
		('cycdep-p4', 'test-p4', 'test-p5', 'conditional-blocks', 'test'),
		('cycdep-p5', 'test-p5', 'test-p1', 'blocks', 'test')`)
	execCycleFixture(t, db, `INSERT INTO wisp_dependencies (id, issue_id, depends_on_wisp_id, type)
		VALUES (UUID(), 'test-pw1', 'test-pw2', 'blocks')`)

	want := map[string][]string{
		"test-p1":  {"test-p2"},
		"test-p2":  {"test-p3"},
		"test-p3":  {"test-p1"},
		"test-p4":  {"test-p5"},
		"test-p5":  {"test-p1"},
		"test-pw1": {"test-pw2"},
	}

	for _, pageSize := range []int{1, 2, 1000} {
		edges, check := loadDependencyEdges(db, pageSize, dependencyCycleMaxEdges)
		if check != nil {
			t.Fatalf("pageSize %d: unexpected check %s: %s", pageSize, check.Message, check.Detail)
		}
		if !reflect.DeepEqual(edges, want) {
			t.Errorf("pageSize %d: edges = %v, want %v", pageSize, edges, want)
		}
	}

	cycle := dependencyCycleNodes(mustLoadEdges(t, db, 2))
	wantCycle := []string{"test-p1", "test-p2", "test-p3"}
	if !reflect.DeepEqual(cycle, wantCycle) {
		t.Errorf("cycle nodes = %v, want %v", cycle, wantCycle)
	}
}

func mustLoadEdges(t *testing.T, db *sql.DB, pageSize int) map[string][]string {
	t.Helper()
	edges, check := loadDependencyEdges(db, pageSize, dependencyCycleMaxEdges)
	if check != nil {
		t.Fatalf("loadDependencyEdges: %s: %s", check.Message, check.Detail)
	}
	return edges
}

// TestLoadDependencyEdges_TooLargeGraphWarns: past maxEdges the check must
// degrade to the documented warning instead of growing without bound.
func TestLoadDependencyEdges_TooLargeGraphWarns(t *testing.T) {
	store := newTestDoltStore(t, "test")
	db := store.UnderlyingDB()

	createCycleIssues(t, store, "test-l1", "test-l2", "test-l3", "test-l4")
	execCycleFixture(t, db, `INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES
		('cycdep-l1', 'test-l1', 'test-l2', 'blocks', 'test'),
		('cycdep-l2', 'test-l2', 'test-l3', 'blocks', 'test'),
		('cycdep-l3', 'test-l3', 'test-l4', 'blocks', 'test')`)

	edges, check := loadDependencyEdges(db, 2, 2)
	if edges != nil || check == nil {
		t.Fatalf("edges = %v, check = %v; want nil edges and a warning check", edges, check)
	}
	if check.Status != StatusWarning || !strings.Contains(check.Detail, "more than 2 edges") {
		t.Fatalf("check = %q (%s: %s), want %q with too-large detail", check.Status, check.Message, check.Detail, StatusWarning)
	}
}

// TestCheckDependencyCycles_DegradedIDSchemas: doctor opens the store
// read-only and skips the migration chain, so it must still work on databases
// where dependencies.id never materialized (#4690) or is NULL mid-backfill
// (ensureDependenciesIDColumn) — shapes where id keyset pagination would
// silently drop rows. The loader must detect them and fall back to a plain
// scan.
func TestCheckDependencyCycles_DegradedIDSchemas(t *testing.T) {
	t.Run("null ids", func(t *testing.T) {
		store := newTestDoltStore(t, "test")
		db := store.UnderlyingDB()

		createCycleIssues(t, store, "test-n1", "test-n2")
		execCycleFixture(t, db, "ALTER TABLE dependencies DROP PRIMARY KEY")
		execCycleFixture(t, db, "ALTER TABLE dependencies MODIFY COLUMN id CHAR(36) NULL")
		execCycleFixture(t, db, `INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by)
			VALUES (NULL, 'test-n1', 'test-n2', 'blocks', 'test'), (NULL, 'test-n2', 'test-n1', 'blocks', 'test')`)

		check := checkDependencyCyclesWithStore(store)
		if check.Status != StatusError {
			t.Fatalf("Status = %q (%s: %s), want %q despite NULL ids", check.Status, check.Message, check.Detail, StatusError)
		}
	})

	t.Run("missing id column", func(t *testing.T) {
		store := newTestDoltStore(t, "test")
		db := store.UnderlyingDB()

		createCycleIssues(t, store, "test-m1", "test-m2")
		execCycleFixture(t, db, "ALTER TABLE dependencies DROP PRIMARY KEY")
		execCycleFixture(t, db, "ALTER TABLE dependencies DROP COLUMN id")
		execCycleFixture(t, db, `INSERT INTO dependencies (issue_id, depends_on_issue_id, type, created_by)
			VALUES ('test-m1', 'test-m2', 'blocks', 'test'), ('test-m2', 'test-m1', 'blocks', 'test')`)

		check := checkDependencyCyclesWithStore(store)
		if check.Status != StatusError {
			t.Fatalf("Status = %q (%s: %s), want %q despite missing id column", check.Status, check.Message, check.Detail, StatusError)
		}
	})
}
