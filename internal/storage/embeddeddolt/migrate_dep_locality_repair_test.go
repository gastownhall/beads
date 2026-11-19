//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// These tests pin the upgrade repair of gastownhall/beads#6066's legacy rows:
// edges between LOCAL rows whose ids carry a foreign prefix (created via
// explicit-id creation) that prefix-only classification stored in
// depends_on_external, where blocked/ready computation cannot see them.
// The pre-repair of ignored migration 0032 moves such rows of both edge
// tables to the local target columns — wisp target first — and recomputes
// blocked state in one ordinary SQL transaction, preserving identity,
// provenance and timestamps. Like the rest of the migration tests they need
// BEADS_TEST_EMBEDDED_DOLT=1.

// depLocalityMarkerVersion is the ignored migration slot the repair rides.
const depLocalityMarkerVersion = 32

// depLocalitySeedIDs are the local rows the fixture seeds: foreign-prefix and
// same-prefix issues, wisps, and the one id deliberately present in BOTH the
// wisps and issues tables, which pins the wisp-first order the resolver and
// the repair share.
const (
	depLocBlockedTarget = "treehouse-lease-b1" // local issue, open, foreign prefix
	depLocWispTarget    = "treehouse-lease-w9" // local wisp, open, foreign prefix
	depLocShared        = "shared-local-target"
	depLocDependent     = "firstmate-lease-d1"
	depLocRelated       = "firstmate-lease-rel"
	depLocPreserved     = "firstmate-lease-pres"
	depLocWispFirst     = "firstmate-lease-wfs"
	depLocOnWisp        = "demo-durable-on-wisp"
	depLocEpic          = "demo-epic"
	depLocChild         = "demo-child"
	depLocWispSource    = "demo-wisp-src"
)

// seedDepLocalityRows commits the legacy shape onto a fully migrated
// database: mis-stored external-column edges naming local targets in both
// dependency tables, alongside the three shapes that must stay exactly as
// they are — an explicit external: reference, an absent cross-prefix target,
// and an already-typed local edge (with a parent-child child whose blocked
// state must follow its parent's once the parent's own blocker becomes
// visible to the computation).
func seedDepLocalityRows(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	dep := func(source, target string) string { return depid.New(source, target) }
	mustExecOn(t, ctx, conn, `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES
  ('`+depLocBlockedTarget+`', 'local blocker under a foreign prefix', '', '', '', '', 'open', 2, 'task'),
  ('`+depLocShared+`', 'an issue that shares its id with a wisp', '', '', '', '', 'open', 2, 'task'),
  ('`+depLocDependent+`', 'dependent of the foreign-prefix blocker', '', '', '', '', 'open', 2, 'task'),
  ('`+depLocRelated+`', 'source of related, explicit and absent edges', '', '', '', '', 'open', 2, 'task'),
  ('`+depLocPreserved+`', 'source of an already-typed edge', '', '', '', '', 'open', 2, 'task'),
  ('`+depLocWispFirst+`', 'source whose target exists as both wisp and issue', '', '', '', '', 'open', 2, 'task'),
  ('`+depLocOnWisp+`', 'durable issue blocked on a foreign-prefix wisp', '', '', '', '', 'open', 2, 'task'),
  ('`+depLocEpic+`', 'epic blocked by the foreign-prefix issue', '', '', '', '', 'open', 2, 'epic'),
  ('`+depLocChild+`', 'child of the epic', '', '', '', '', 'open', 2, 'task');
INSERT INTO wisps (id, title, status, priority, issue_type) VALUES
  ('`+depLocWispTarget+`', 'local wisp under a foreign prefix', 'open', 2, 'task'),
  ('`+depLocShared+`', 'a wisp that shares its id with an issue', 'open', 2, 'task'),
  ('`+depLocWispSource+`', 'wisp source of a mis-stored edge', 'open', 2, 'task');
INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) VALUES
  ('`+dep(depLocDependent, depLocBlockedTarget)+`', '`+depLocDependent+`', NULL, NULL, '`+depLocBlockedTarget+`', 'blocks', '2026-01-15 10:30:00', 'legacy-importer', '{"origin": "misclassified-blocks"}', 'thread-legacy-d1'),
  ('`+dep(depLocRelated, depLocBlockedTarget)+`', '`+depLocRelated+`', NULL, NULL, '`+depLocBlockedTarget+`', 'related', '2026-02-20 08:15:00', 'carol', '{"origin": "misclassified-related"}', 'thread-legacy-rel'),
  ('`+dep(depLocRelated, "external:da:da-7eo")+`', '`+depLocRelated+`', NULL, NULL, 'external:da:da-7eo', 'related', '2026-02-20 08:15:00', 'carol', '{}', ''),
  ('`+dep(depLocRelated, "other-nonexistent")+`', '`+depLocRelated+`', NULL, NULL, 'other-nonexistent', 'related', '2026-02-20 08:15:00', 'carol', '{}', ''),
  ('`+dep(depLocOnWisp, depLocWispTarget)+`', '`+depLocOnWisp+`', NULL, NULL, '`+depLocWispTarget+`', 'blocks', '2026-01-15 10:30:00', 'legacy-importer', '{}', ''),
  ('`+dep(depLocWispFirst, depLocShared)+`', '`+depLocWispFirst+`', NULL, NULL, '`+depLocShared+`', 'blocks', '2026-01-15 10:30:00', 'legacy-importer', '{}', ''),
  ('`+dep(depLocEpic, depLocBlockedTarget)+`', '`+depLocEpic+`', NULL, NULL, '`+depLocBlockedTarget+`', 'blocks', '2026-01-15 10:30:00', 'legacy-importer', '{}', ''),
  ('`+dep(depLocPreserved, depLocBlockedTarget)+`', '`+depLocPreserved+`', '`+depLocBlockedTarget+`', NULL, NULL, 'related', '2026-01-15 10:30:00', 'legacy-importer', '{"origin": "already-typed"}', 'thread-kept'),
  ('`+dep(depLocChild, depLocEpic)+`', '`+depLocChild+`', '`+depLocEpic+`', NULL, NULL, 'parent-child', '2026-01-15 10:30:00', 'legacy-importer', '{}', '');
INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id) VALUES
  ('`+dep(depLocWispSource, depLocBlockedTarget)+`', '`+depLocWispSource+`', NULL, NULL, '`+depLocBlockedTarget+`', 'blocks', '2026-03-01 09:00:00', 'legacy-importer', '{"origin": "wisp-misclassified"}', 'thread-legacy-wisp'),
  ('`+dep(depLocWispSource, "external:da:da-7x")+`', '`+depLocWispSource+`', NULL, NULL, 'external:da:da-7x', 'related', '2026-03-01 09:00:00', 'legacy-importer', '{}', '');
`)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: seed legacy mis-stored dependency rows')")
}

// reopenDepLocalityMarker makes ignored 0032 pending again, so the next
// MigrateUp runs the repair on the seeded rows.
func reopenDepLocalityMarker(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	mustExecOn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version = ?", depLocalityMarkerVersion)
}

// depLocalityTargets reads one edge row's three target columns.
func depLocalityTargets(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string) (issueID, wispID, external sql.NullString) {
	t.Helper()
	err := conn.QueryRowContext(ctx, fmt.Sprintf(
		"SELECT depends_on_issue_id, depends_on_wisp_id, depends_on_external FROM %s WHERE id = ?", table),
		id).Scan(&issueID, &wispID, &external)
	if err != nil {
		t.Fatalf("read %s row %s: %v", table, id, err)
	}
	return issueID, wispID, external
}

// requireTargets asserts one edge row's three target columns at once.
func requireDepLocalityTargets(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string, wantIssue, wantWisp, wantExternal sql.NullString) {
	t.Helper()
	issueID, wispID, external := depLocalityTargets(t, ctx, conn, table, id)
	if issueID != wantIssue || wispID != wantWisp || external != wantExternal {
		t.Fatalf("%s row %s targets = (%v, %v, %v), want (%v, %v, %v)",
			table, id, issueID, wispID, external, wantIssue, wantWisp, wantExternal)
	}
}

func nullString() sql.NullString { return sql.NullString{} }

func valueString(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

// requireDepLocalityBlocked asserts the stored blocked flag of one issue or
// wisp row.
func requireDepLocalityBlocked(t *testing.T, ctx context.Context, conn *sql.Conn, table, id string, want int) {
	t.Helper()
	got := scalarInt(t, ctx, conn, fmt.Sprintf("SELECT is_blocked FROM %s WHERE id = ?", table), id)
	if got != want {
		t.Fatalf("%s.%s.is_blocked = %d, want %d", table, id, got, want)
	}
}

// requireDepLocalityLegacyShape asserts the fixture's rows still hold their
// legacy external shape — the before-state of every repair assertion.
func requireDepLocalityLegacyShape(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	dep := func(source, target string) string { return depid.New(source, target) }
	requireDepLocalityTargets(t, ctx, conn, "dependencies", dep(depLocDependent, depLocBlockedTarget), nullString(), nullString(), valueString(depLocBlockedTarget))
	requireDepLocalityTargets(t, ctx, conn, "wisp_dependencies", dep(depLocWispSource, depLocBlockedTarget), nullString(), nullString(), valueString(depLocBlockedTarget))
	requireDepLocalityBlocked(t, ctx, conn, "issues", depLocDependent, 0)
	requireDepLocalityBlocked(t, ctx, conn, "issues", depLocEpic, 0)
	requireDepLocalityBlocked(t, ctx, conn, "wisps", depLocWispSource, 0)
}

// requireDepLocalityRepairedShape asserts the after-state AC-7 names: both
// edge tables carry the reclassified rows in their typed columns, the
// explicit external: reference, the absent target and the already-typed edge
// are untouched, the blocked flags agree with local semantics (including the
// parent-child child of a newly blocked epic), identity, provenance and
// timestamps are preserved, and the marker is durably recorded.
func requireDepLocalityRepairedShape(t *testing.T, ctx context.Context, conn *sql.Conn) {
	t.Helper()
	dep := func(source, target string) string { return depid.New(source, target) }

	// Reclassified to the issue column: the blocks edge, the related edge,
	// the epic's edge, and the wisp-source edge. The wisp-target and the
	// wisp-first edges land in the wisp column.
	requireDepLocalityTargets(t, ctx, conn, "dependencies", dep(depLocDependent, depLocBlockedTarget), valueString(depLocBlockedTarget), nullString(), nullString())
	requireDepLocalityTargets(t, ctx, conn, "dependencies", dep(depLocRelated, depLocBlockedTarget), valueString(depLocBlockedTarget), nullString(), nullString())
	requireDepLocalityTargets(t, ctx, conn, "dependencies", dep(depLocEpic, depLocBlockedTarget), valueString(depLocBlockedTarget), nullString(), nullString())
	requireDepLocalityTargets(t, ctx, conn, "dependencies", dep(depLocOnWisp, depLocWispTarget), nullString(), valueString(depLocWispTarget), nullString())
	requireDepLocalityTargets(t, ctx, conn, "dependencies", dep(depLocWispFirst, depLocShared), nullString(), valueString(depLocShared), nullString())
	requireDepLocalityTargets(t, ctx, conn, "wisp_dependencies", dep(depLocWispSource, depLocBlockedTarget), valueString(depLocBlockedTarget), nullString(), nullString())

	// The three shapes that must not move.
	requireDepLocalityTargets(t, ctx, conn, "dependencies", dep(depLocRelated, "external:da:da-7eo"), nullString(), nullString(), valueString("external:da:da-7eo"))
	requireDepLocalityTargets(t, ctx, conn, "dependencies", dep(depLocRelated, "other-nonexistent"), nullString(), nullString(), valueString("other-nonexistent"))
	requireDepLocalityTargets(t, ctx, conn, "wisp_dependencies", dep(depLocWispSource, "external:da:da-7x"), nullString(), nullString(), valueString("external:da:da-7x"))
	requireDepLocalityTargets(t, ctx, conn, "dependencies", dep(depLocPreserved, depLocBlockedTarget), valueString(depLocBlockedTarget), nullString(), nullString())
	requireDepLocalityTargets(t, ctx, conn, "dependencies", dep(depLocChild, depLocEpic), valueString(depLocEpic), nullString(), nullString())

	// Blocked flags under local semantics.
	requireDepLocalityBlocked(t, ctx, conn, "issues", depLocDependent, 1) // blocks edge, open blocker
	requireDepLocalityBlocked(t, ctx, conn, "issues", depLocRelated, 0)   // related never blocks
	requireDepLocalityBlocked(t, ctx, conn, "issues", depLocOnWisp, 1)    // open wisp blocker
	requireDepLocalityBlocked(t, ctx, conn, "issues", depLocWispFirst, 1) // shared id resolves wisp-first
	requireDepLocalityBlocked(t, ctx, conn, "issues", depLocEpic, 1)      // epic blocked by the issue
	requireDepLocalityBlocked(t, ctx, conn, "issues", depLocChild, 1)     // child inherits the blocked parent
	requireDepLocalityBlocked(t, ctx, conn, "issues", depLocPreserved, 0) // related edge stays related
	requireDepLocalityBlocked(t, ctx, conn, "wisps", depLocWispSource, 1) // wisp blocked on a local issue

	// Identity, provenance and timestamps are preserved on the moved rows.
	requireDepLocalityProvenance(t, ctx, conn, "dependencies", dep(depLocDependent, depLocBlockedTarget), "2026-01-15 10:30:00", "legacy-importer", "misclassified-blocks", "thread-legacy-d1")
	requireDepLocalityProvenance(t, ctx, conn, "dependencies", dep(depLocRelated, depLocBlockedTarget), "2026-02-20 08:15:00", "carol", "misclassified-related", "thread-legacy-rel")
	requireDepLocalityProvenance(t, ctx, conn, "wisp_dependencies", dep(depLocWispSource, depLocBlockedTarget), "2026-03-01 09:00:00", "legacy-importer", "wisp-misclassified", "thread-legacy-wisp")

	// The repair emits no events or journal rows on the migration connection.
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM events"); got != 0 {
		t.Fatalf("events rows = %d, want 0: the repair must not record events", got)
	}
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM bd_events_journal"); got != 0 {
		t.Fatalf("bd_events_journal rows = %d, want 0: the repair must not journal", got)
	}

	if !ignoredCursorHas(t, ctx, conn, depLocalityMarkerVersion) {
		t.Fatalf("ignored cursor does not record %d after the repair pass", depLocalityMarkerVersion)
	}
}

// depLocalityUpdatedAt reads one issue's updated_at, the timestamp the
// recompute must preserve.
func depLocalityUpdatedAt(t *testing.T, ctx context.Context, conn *sql.Conn, id string) string {
	t.Helper()
	var got string
	if err := conn.QueryRowContext(ctx,
		"SELECT DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s') FROM issues WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("read updated_at for %s: %v", id, err)
	}
	return got
}

// requireDepLocalityProvenance asserts one moved edge row kept everything but
// its target columns.
func requireDepLocalityProvenance(t *testing.T, ctx context.Context, conn *sql.Conn, table, id, wantCreatedAt, wantCreatedBy, wantMetaOrigin, wantThread string) {
	t.Helper()
	var createdAt, createdBy, thread string
	err := conn.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT DATE_FORMAT(created_at, '%%Y-%%m-%%d %%H:%%i:%%s'), created_by, thread_id FROM %s WHERE id = ?`, table),
		id).Scan(&createdAt, &createdBy, &thread)
	if err != nil {
		t.Fatalf("read %s provenance for %s: %v", table, id, err)
	}
	if createdAt != wantCreatedAt || createdBy != wantCreatedBy || thread != wantThread {
		t.Fatalf("%s %s provenance = (%q, %q, %q), want (%q, %q, %q)",
			table, id, createdAt, createdBy, thread, wantCreatedAt, wantCreatedBy, wantThread)
	}
	if got := scalarInt(t, ctx, conn,
		fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ? AND JSON_UNQUOTE(JSON_EXTRACT(metadata, '$.origin')) = ?", table),
		id, wantMetaOrigin); got != 1 {
		t.Fatalf("%s %s metadata origin = %q, want %q", table, id, got, wantMetaOrigin)
	}
}

// TestEmbeddedDepLocalityRepairMovesLegacyExternalRows is AC-7's main leg: a
// fully migrated database with legacy external-column edges naming local
// targets in both dependency tables repairs them to the typed columns on the
// pending-marker pass, blocked flags agree with local semantics (including
// the parent-child child of a newly blocked epic, and the wisp-first order
// for an id that exists as both wisp and issue), identity, provenance and
// timestamps are preserved, and reopening the marker writes nothing.
//
// PRE-fix (base without the repair) the mis-stored rows stay external and the
// dependent, the epic and the wisp source all read ready: the repair does not
// exist and this test fails on its first blocked-flag assertion.
func TestEmbeddedDepLocalityRepairMovesLegacyExternalRows(t *testing.T) {
	requireEmbedded(t)

	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("initial MigrateUp: %v", err)
	}
	seedDepLocalityRows(t, ctx, conn)
	dependentUpdatedAt := depLocalityUpdatedAt(t, ctx, conn, depLocDependent)
	reopenDepLocalityMarker(t, ctx, conn)
	requireDepLocalityLegacyShape(t, ctx, conn)

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("repair MigrateUp: %v", err)
	}
	requireDepLocalityRepairedShape(t, ctx, conn)

	// The recompute preserved the dependent's updated_at.
	if got := depLocalityUpdatedAt(t, ctx, conn, depLocDependent); got != dependentUpdatedAt {
		t.Fatalf("dependent updated_at = %q after the repair, want %q: the recompute must preserve timestamps", got, dependentUpdatedAt)
	}

	// Reopening writes no repair changes: the second pass records the marker
	// but neither commits anything nor touches a row.
	headBefore := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')")
	reopenDepLocalityMarker(t, ctx, conn)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("reopen MigrateUp: %v", err)
	}
	if headAfter := scalarString(t, ctx, conn, "SELECT HASHOF('HEAD')"); headAfter != headBefore {
		t.Fatalf("HEAD = %q after reopening, want %q: the reopen pass must write nothing", headAfter, headBefore)
	}
	requireDepLocalityRepairedShape(t, ctx, conn)
}

// depLocalityFaultConn injects one failure into the repair's recompute: the
// first is_blocked UPDATE after a move has executed returns an error, so the
// pass must roll the moves back and leave the marker pending. It wraps the
// schema.DBConn interface the migrator takes (the preflightStatementCounter
// pattern) and injects through SQL text only — no production fault hook —
// because the failure point is exactly the seam the design demands be
// atomic: moves and recompute in one ordinary transaction.
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

// TestEmbeddedDepLocalityRepairRollsBackMovesAndRecomputeTogether pins the
// repair's atomicity: a failure after the moves but before the recompute
// finishes must roll BOTH back — the rows keep their legacy external shape,
// the blocked flags stay as they were, the working set stays clean, and the
// marker stays pending, so the next open retries the whole repair rather
// than resuming half-applied (autocommitted moves would have stranded the
// moved-source seed; the ordinary transaction undoes both or neither).
func TestEmbeddedDepLocalityRepairRollsBackMovesAndRecomputeTogether(t *testing.T) {
	requireEmbedded(t)

	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("initial MigrateUp: %v", err)
	}
	seedDepLocalityRows(t, ctx, conn)
	reopenDepLocalityMarker(t, ctx, conn)
	statusBefore, err := statusTables(ctx, conn)
	if err != nil {
		t.Fatalf("read dolt_status before the failed pass: %v", err)
	}

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
	requireDepLocalityLegacyShape(t, ctx, conn)
	if ignoredCursorHas(t, ctx, conn, depLocalityMarkerVersion) {
		t.Fatal("the failed repair recorded its marker; it must stay pending for the retry")
	}
	statusAfter, err := statusTables(ctx, conn)
	if err != nil {
		t.Fatalf("read dolt_status after the failed pass: %v", err)
	}
	if !slices.Equal(statusBefore, statusAfter) {
		t.Fatalf("dolt_status = %v after the failed pass, want %v: the rollback left no debris", statusAfter, statusBefore)
	}

	// A plain retry without the fault converges.
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("retry MigrateUp: %v", err)
	}
	requireDepLocalityRepairedShape(t, ctx, conn)
}

// TestEmbeddedDepLocalityRepairRefusesDirtyGraphThenRecovers is AC-8: with
// the repair needed and a pre-existing dirty issues table, the migration
// refuses BEFORE any repair write with *schema.DirtyTablesError naming the
// table, leaves the marker pending and the rows untouched; after the
// documented reconcile commit the migration succeeds, and ordinary
// clone-local wisp dirt never blocks it.
func TestEmbeddedDepLocalityRepairRefusesDirtyGraphThenRecovers(t *testing.T) {
	requireEmbedded(t)

	ctx := t.Context()
	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openPinnedConn(t, ctx, dataDir)
	defer closeConn()

	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("initial MigrateUp: %v", err)
	}
	seedDepLocalityRows(t, ctx, conn)
	// A user edit in flight on the issues table — the dirty graph the repair
	// must not entangle its writes with.
	mustExecOn(t, ctx, conn, "UPDATE issues SET title = 'user edit in flight' WHERE id = ?", depLocDependent)
	reopenDepLocalityMarker(t, ctx, conn)

	_, migrateErr := schema.MigrateUp(ctx, conn)
	if migrateErr == nil {
		t.Fatal("MigrateUp over the dirty graph = nil, want the typed refusal")
	}
	var dirtyErr *schema.DirtyTablesError
	if !errors.As(migrateErr, &dirtyErr) {
		t.Fatalf("MigrateUp error = %T (%v), want *schema.DirtyTablesError so the reconcile opens can skip and let the commit through", migrateErr, migrateErr)
	}
	if len(dirtyErr.Tables) != 1 || dirtyErr.Tables[0] != "issues" {
		t.Fatalf("DirtyTablesError.Tables = %v, want [issues]: only the dirty graph must be named", dirtyErr.Tables)
	}
	if !strings.Contains(migrateErr.Error(), "bd dolt commit") {
		t.Fatalf("refusal = %q, want the documented commit-and-retry recovery text", migrateErr.Error())
	}
	requireDepLocalityLegacyShape(t, ctx, conn)
	if ignoredCursorHas(t, ctx, conn, depLocalityMarkerVersion) {
		t.Fatal("the refused repair recorded its marker; it must stay pending for the post-reconcile pass")
	}

	// The reconcile: an ordinary clone-local wisp edit that must never block
	// the repair, then the working-set commit `bd dolt commit` performs.
	mustExecOn(t, ctx, conn, "UPDATE wisps SET title = 'wisp edit in flight' WHERE id = ?", depLocWispSource)
	mustDrain(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'test: reconcile the dirty graph')")
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("migration after the reconcile: %v", err)
	}
	requireDepLocalityRepairedShape(t, ctx, conn)
	if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM wisps WHERE id = ? AND title = ?", depLocWispSource, "wisp edit in flight"); got != 1 {
		t.Fatalf("wisp edit rows = %d, want 1: ordinary clone-local wisp dirt is exempt and must survive the pass", got)
	}
}
