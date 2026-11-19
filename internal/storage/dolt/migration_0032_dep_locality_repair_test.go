package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// These tests pin the #6066 legacy-edge upgrade repair on the server backend:
// ignored migration 0032's pre-repair moves edges between LOCAL rows out of
// depends_on_external — where prefix-only classification stored them, invisible
// to blocked/ready computation — into the typed local columns, and recomputes
// blocked state in one ordinary SQL transaction on the migration's pinned
// connection. They are the server-mode twins of the embedded tests in
// migrate_dep_locality_repair_test.go: same repair, different SQL engine.

// depLocalityMarkerVersion is the ignored migration slot the repair rides.
const depLocalityMarkerVersion = 32

// depLocalityDoltSeed commits the legacy shape onto a migrated store: one
// foreign-prefix local blocker, its dependent, and the blocks edge prefix-only
// classification stored in depends_on_external. It returns the two issue ids
// and the edge's deterministic id.
func depLocalityDoltSeed(t *testing.T, ctx context.Context, store *DoltStore) (blocker, dependent, depID string) {
	t.Helper()
	blocker, dependent = "treehouse-lease-d1", "firstmate-lease-d1"
	for _, issue := range []*types.Issue{
		{ID: blocker, Title: "foreign-prefix local blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: dependent, Title: "dependent of the foreign-prefix blocker", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	depID = depid.New(dependent, blocker)
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id)
		VALUES (?, ?, NULL, NULL, ?, 'blocks', '2026-01-15 10:30:00', 'legacy-importer', '{"origin": "misclassified-blocks"}', 'thread-legacy-d1')`,
		depID, dependent, blocker); err != nil {
		t.Fatalf("seed the legacy mis-stored edge: %v", err)
	}
	if err := store.doltAddAndCommit(ctx, []string{"issues", "dependencies"}, "test: seed legacy mis-stored edge"); err != nil {
		t.Fatalf("commit the legacy seed: %v", err)
	}
	return blocker, dependent, depID
}

// depLocalityReopenMarker makes ignored 0032 pending again.
func depLocalityReopenMarker(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	if _, err := store.db.ExecContext(ctx,
		"DELETE FROM ignored_schema_migrations WHERE version = ?", depLocalityMarkerVersion); err != nil {
		t.Fatalf("reopen the 0032 marker: %v", err)
	}
}

// depLocalityCursorHas reports whether the ignored cursor records the marker.
func depLocalityCursorHas(t *testing.T, ctx context.Context, store *DoltStore) bool {
	t.Helper()
	return queryInt64(ctx, t, store,
		fmt.Sprintf("SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = %d", depLocalityMarkerVersion)) == 1
}

// depLocalityTargets reads the seeded edge's three target columns.
func depLocalityTargets(t *testing.T, ctx context.Context, store *DoltStore, depID string) (issueID, external sql.NullString) {
	t.Helper()
	if err := store.db.QueryRowContext(ctx,
		"SELECT depends_on_issue_id, depends_on_external FROM dependencies WHERE id = ?", depID,
	).Scan(&issueID, &external); err != nil {
		t.Fatalf("read the seeded edge %s: %v", depID, err)
	}
	return issueID, external
}

// depLocalityRequireTyped asserts the edge was reclassified to the local issue
// column.
func depLocalityRequireTyped(t *testing.T, ctx context.Context, store *DoltStore, depID, blocker string) {
	t.Helper()
	issueID, external := depLocalityTargets(t, ctx, store, depID)
	if !issueID.Valid || issueID.String != blocker || external.Valid {
		t.Fatalf("edge %s targets = (issue %v, external %v), want the local issue %q and no external value", depID, issueID, external, blocker)
	}
}

// depLocalityRequireExternal asserts the edge still holds its legacy shape.
func depLocalityRequireExternal(t *testing.T, ctx context.Context, store *DoltStore, depID, blocker string) {
	t.Helper()
	issueID, external := depLocalityTargets(t, ctx, store, depID)
	if issueID.Valid || !external.Valid || external.String != blocker {
		t.Fatalf("edge %s targets = (issue %v, external %v), want the untouched legacy external %q", depID, issueID, external, blocker)
	}
}

func depLocalityBlocked(t *testing.T, ctx context.Context, store *DoltStore, id string) int64 {
	t.Helper()
	return queryInt64(ctx, t, store, fmt.Sprintf("SELECT is_blocked FROM issues WHERE id = '%s'", id))
}

// depLocalityRequireProvenance asserts the moved edge kept everything but its
// target columns.
func depLocalityRequireProvenance(t *testing.T, ctx context.Context, store *DoltStore, depID string) {
	t.Helper()
	var createdAt, createdBy, thread string
	if err := store.db.QueryRowContext(ctx,
		"SELECT DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%s'), created_by, thread_id FROM dependencies WHERE id = ?",
		depID).Scan(&createdAt, &createdBy, &thread); err != nil {
		t.Fatalf("read the moved edge's provenance: %v", err)
	}
	if createdAt != "2026-01-15 10:30:00" || createdBy != "legacy-importer" || thread != "thread-legacy-d1" {
		t.Fatalf("moved edge provenance = (%q, %q, %q), want the seeded values", createdAt, createdBy, thread)
	}
	if got := queryInt64(ctx, t, store, fmt.Sprintf(
		"SELECT COUNT(*) FROM dependencies WHERE id = '%s' AND JSON_UNQUOTE(JSON_EXTRACT(metadata, '$.origin')) = 'misclassified-blocks'", depID)); got != 1 {
		t.Fatalf("moved edge metadata origin count = %d, want 1: identity and provenance must be preserved", got)
	}
}

// TestMigration0032DepLocalityRepairOnDoltBackend is AC-7 on the server
// backend: a migration pass over a legacy mis-stored edge moves it to the
// typed local column, recomputes the dependent's blocked flag, preserves
// identity and provenance, records the marker, and a reopened marker writes
// nothing.
func TestMigration0032DepLocalityRepairOnDoltBackend(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocker, dependent, depID := depLocalityDoltSeed(t, ctx, store)
	depLocalityReopenMarker(t, ctx, store)
	depLocalityRequireExternal(t, ctx, store, depID, blocker)

	if _, err := rerunSchemaInitOnTestStore(ctx, store); err != nil {
		t.Fatalf("migration pass with the repair: %v", err)
	}
	depLocalityRequireTyped(t, ctx, store, depID, blocker)
	depLocalityRequireProvenance(t, ctx, store, depID)
	if blocked := depLocalityBlocked(t, ctx, store, dependent); blocked != 1 {
		t.Fatalf("dependent %s is_blocked = %d after the repair, want 1: the local blocker must gate it", dependent, blocked)
	}
	if !depLocalityCursorHas(t, ctx, store) {
		t.Fatalf("ignored cursor does not record %d after the repair pass", depLocalityMarkerVersion)
	}

	// Reopening the marker writes no repair changes.
	var headBefore string
	if err := store.db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&headBefore); err != nil {
		t.Fatalf("read HEAD before the reopen pass: %v", err)
	}
	depLocalityReopenMarker(t, ctx, store)
	if _, err := rerunSchemaInitOnTestStore(ctx, store); err != nil {
		t.Fatalf("reopen migration pass: %v", err)
	}
	var headAfter string
	if err := store.db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&headAfter); err != nil {
		t.Fatalf("read HEAD after the reopen pass: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("HEAD = %q after reopening the marker, want %q: the reopen pass must write nothing", headAfter, headBefore)
	}
	depLocalityRequireTyped(t, ctx, store, depID, blocker)
	if blocked := depLocalityBlocked(t, ctx, store, dependent); blocked != 1 {
		t.Fatalf("dependent %s is_blocked = %d after the reopen pass, want 1 (unchanged)", dependent, blocked)
	}
	if !depLocalityCursorHas(t, ctx, store) {
		t.Fatalf("ignored cursor does not record %d after the reopen pass", depLocalityMarkerVersion)
	}
}

// depLocalityFaultConn injects one failure into the repair's recompute on the
// server backend: the first is_blocked UPDATE after a move has executed
// returns an error, so the ordinary transaction must roll the moves back and
// leave the marker pending. It wraps the schema.DBConn interface MigrateUp
// takes, the same pattern as the embedded tests.
type depLocalityFaultConn struct {
	schema.DBConn
	sawMove bool
	failed  bool
}

func (c *depLocalityFaultConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "depends_on_external = NULL") {
		c.sawMove = true
	}
	if c.sawMove && !c.failed &&
		strings.HasPrefix(strings.TrimSpace(query), "UPDATE") &&
		strings.Contains(query, "is_blocked") {
		c.failed = true
		return nil, errors.New("injected recompute failure (test)")
	}
	return c.DBConn.ExecContext(ctx, query, args...)
}

// TestMigration0032DepLocalityRepairRollsBackOnDoltBackend pins the repair's
// atomicity on the server backend: a failure after the moves but before the
// recompute finishes rolls BOTH back — the edge keeps its legacy shape, the
// dependent stays unblocked, the marker stays pending — and a plain retry
// converges.
func TestMigration0032DepLocalityRepairRollsBackOnDoltBackend(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocker, dependent, depID := depLocalityDoltSeed(t, ctx, store)
	depLocalityReopenMarker(t, ctx, store)

	func() {
		conn, err := store.db.Conn(ctx)
		if err != nil {
			t.Fatalf("pin a connection: %v", err)
		}
		defer conn.Close()
		fault := &depLocalityFaultConn{DBConn: conn}
		_, migrateErr := schema.MigrateUp(ctx, fault)
		if migrateErr == nil {
			t.Fatal("MigrateUp with the injected recompute failure = nil, want the failure")
		}
		if !strings.Contains(migrateErr.Error(), "injected recompute failure") {
			t.Fatalf("MigrateUp error = %v, want the injected recompute failure to travel", migrateErr)
		}
		if !fault.failed {
			t.Fatal("the fault was never reached: the pass completed without a recompute write")
		}
	}()

	depLocalityRequireExternal(t, ctx, store, depID, blocker)
	if blocked := depLocalityBlocked(t, ctx, store, dependent); blocked != 0 {
		t.Fatalf("dependent %s is_blocked = %d after the rolled-back pass, want 0: the moves must be undone", dependent, blocked)
	}
	if depLocalityCursorHas(t, ctx, store) {
		t.Fatalf("the failed repair recorded marker %d; it must stay pending for the retry", depLocalityMarkerVersion)
	}

	// A plain retry converges.
	if _, err := rerunSchemaInitOnTestStore(ctx, store); err != nil {
		t.Fatalf("retry migration pass: %v", err)
	}
	depLocalityRequireTyped(t, ctx, store, depID, blocker)
	if blocked := depLocalityBlocked(t, ctx, store, dependent); blocked != 1 {
		t.Fatalf("dependent %s is_blocked = %d after the retry, want 1", dependent, blocked)
	}
	if !depLocalityCursorHas(t, ctx, store) {
		t.Fatalf("ignored cursor does not record %d after the retry", depLocalityMarkerVersion)
	}
}

// TestMigration0032DepLocalityRepairRefusesDirtyGraphOnDoltBackend is AC-8 on
// the server backend: with the repair needed and a pre-existing dirty issues
// table, the schema pass refuses BEFORE any repair write with
// *schema.DirtyTablesError naming the table, leaves the marker pending and
// the rows untouched; after the documented reconcile commit the pass
// succeeds.
func TestMigration0032DepLocalityRepairRefusesDirtyGraphOnDoltBackend(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	blocker, dependent, depID := depLocalityDoltSeed(t, ctx, store)
	if _, err := store.db.ExecContext(ctx,
		"UPDATE issues SET title = 'user edit in flight' WHERE id = ?", dependent); err != nil {
		t.Fatalf("dirty the issues table: %v", err)
	}
	depLocalityReopenMarker(t, ctx, store)

	_, migrateErr := rerunSchemaInitOnTestStore(ctx, store)
	if migrateErr == nil {
		t.Fatal("schema init over the dirty graph = nil, want the typed refusal")
	}
	var dirtyErr *schema.DirtyTablesError
	if !errors.As(migrateErr, &dirtyErr) {
		t.Fatalf("schema init error = %T (%v), want *schema.DirtyTablesError so the reconcile opens can skip and let the commit through", migrateErr, migrateErr)
	}
	if len(dirtyErr.Tables) != 1 || dirtyErr.Tables[0] != "issues" {
		t.Fatalf("DirtyTablesError.Tables = %v, want [issues]: only the dirty graph must be named", dirtyErr.Tables)
	}
	depLocalityRequireExternal(t, ctx, store, depID, blocker)
	if blocked := depLocalityBlocked(t, ctx, store, dependent); blocked != 0 {
		t.Fatalf("dependent %s is_blocked = %d after the refused pass, want 0: no repair write may land", dependent, blocked)
	}
	if depLocalityCursorHas(t, ctx, store) {
		t.Fatalf("the refused repair recorded marker %d; it must stay pending", depLocalityMarkerVersion)
	}

	// The documented reconcile: commit the working set at the current schema,
	// then the pass goes through.
	if err := store.doltAddAndCommit(ctx, []string{"issues"}, "test: reconcile the dirty graph"); err != nil {
		t.Fatalf("reconcile commit: %v", err)
	}
	if _, err := rerunSchemaInitOnTestStore(ctx, store); err != nil {
		t.Fatalf("migration after the reconcile: %v", err)
	}
	depLocalityRequireTyped(t, ctx, store, depID, blocker)
	if blocked := depLocalityBlocked(t, ctx, store, dependent); blocked != 1 {
		t.Fatalf("dependent %s is_blocked = %d after the reconcile pass, want 1", dependent, blocked)
	}
	if !depLocalityCursorHas(t, ctx, store) {
		t.Fatalf("ignored cursor does not record %d after the reconcile pass", depLocalityMarkerVersion)
	}
}
