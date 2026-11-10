//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// Real-Dolt coverage for the cross-prefix dependency-target repair
// (repairCrossPrefixDepTargets, internal/storage/schema/cross_prefix_dep_repair.go)
// and its completion marker, ignored migration 0032 (gastownhall/beads#8299).
//
// Why this cannot be a sqlmock test: the repair's contract is Dolt's — the
// generated depends_on_id column recomputes over the moved typed column, the
// uk_dep_* unique keys arbitrate the move, and the blocked-state recompute is
// several correlated EXISTS queries over the real schema. The pass-order
// properties (marker pending forces the pass, refusal leaves the marker
// pending, converged reopen short-circuits) are MigrateUp's, not any single
// statement's — the same reason migrate_dep_rekey_dedup_test.go exists.

// crossPrefixDepRepairMarkerVersion is ignored migration 0032, the clone-local
// marker whose pending state forces the repair pass. Like
// depRekeyMarkerVersion in the re-key file, it names one specific slot: the
// fixtures unrecord from it, which only forces a pass while 0032 stays in the
// ignored series.
const crossPrefixDepRepairMarkerVersion = 32

// depRowPayload is the edge payload the repair must carry across the move:
// everything about the row except which typed column holds the target.
type depRowPayload struct {
	depType   string
	createdAt string
	createdBy string
	metadata  string
}

func readDepRowPayload(t *testing.T, ctx context.Context, conn *sql.Conn, id string) depRowPayload {
	t.Helper()
	var p depRowPayload
	err := conn.QueryRowContext(ctx,
		`SELECT COALESCE(type, ''), COALESCE(created_at, ''), COALESCE(created_by, ''), COALESCE(metadata, '')
		 FROM dependencies WHERE id = ?`, id).
		Scan(&p.depType, &p.createdAt, &p.createdBy, &p.metadata)
	if err != nil {
		t.Fatalf("read payload for edge %s: %v", id, err)
	}
	return p
}

// issueStamp is the blocked-state pair the repair recomputes: the derived
// is_blocked and the updated_at the mark/unmark templates must pin (bd-578h9),
// rendered as text so a pre/post comparison is byte-exact.
type issueStamp struct {
	isBlocked int
	updatedAt string
}

func readIssueStamp(t *testing.T, ctx context.Context, conn *sql.Conn, id string) issueStamp {
	t.Helper()
	var s issueStamp
	err := conn.QueryRowContext(ctx,
		`SELECT is_blocked, COALESCE(DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s'), '') FROM issues WHERE id = ?`, id).
		Scan(&s.isBlocked, &s.updatedAt)
	if err != nil {
		t.Fatalf("read issues stamp for %s: %v", id, err)
	}
	return s
}

// depRowsByTarget orders edge rows by (source, resolved target) so expected
// and actual sets compare order-independently: the primary keys are UUIDv5
// derivations whose sort order is an implementation detail of depid.
func depRowsByTarget(rows []depRow) []depRow {
	out := slices.Clone(rows)
	slices.SortFunc(out, func(a, b depRow) int {
		if c := strings.Compare(a.issueID, b.issueID); c != 0 {
			return c
		}
		return strings.Compare(a.dependsOnIssueID+a.dependsOnWispID+a.dependsOnExternal,
			b.dependsOnIssueID+b.dependsOnWispID+b.dependsOnExternal)
	})
	return out
}

// TestEmbeddedCrossPrefixDepRepairRestoresLegacyRows is the victim repair
// (AC-5): rows the old prefix-comparing classifier stored in
// depends_on_external move into the typed column this database's evidence
// names, the blocked state they froze recomputes, and everything else about
// the row — id included, because depid keys on the resolved target, not the
// column — rides across unchanged. Explicit external: references and
// missing foreign-prefix targets ("add now, route later") are left exactly as
// they are.
func TestEmbeddedCrossPrefixDepRepairRestoresLegacyRows(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMigratedStore(t, ctx)
	conn, closeConn := openDepConn(t, ctx, dataDir)

	const srcBlocked, srcUnblocked = "bd-x-src", "bd-x-src2"
	const heldOpen, heldClosed = "hq-x-open-target", "hq-x-closed-target"
	seedIssue(t, ctx, conn, srcBlocked)
	seedIssue(t, ctx, conn, srcUnblocked)
	seedIssue(t, ctx, conn, heldOpen)
	seedIssue(t, ctx, conn, heldClosed)
	mustExecConn(t, ctx, conn,
		"UPDATE issues SET status = 'closed', closed_at = NOW() WHERE id = ?", heldClosed)

	// Ids are already the deterministic derivation over (source, resolved
	// target), so the re-key that runs before the repair leaves them alone and
	// the test sees the repair's own no-id-rewrite property: moving the target
	// between columns keeps the id, because depid keys on the resolved value.
	seedDependency(t, ctx, conn, depid.New(srcBlocked, heldOpen), srcBlocked, nil, nil, strptr(heldOpen))
	seedDependency(t, ctx, conn, depid.New(srcUnblocked, heldClosed), srcUnblocked, nil, nil, strptr(heldClosed))
	seedDependency(t, ctx, conn, depid.New(srcBlocked, "external:JIRA-9"), srcBlocked, nil, nil, strptr("external:JIRA-9"))
	seedDependency(t, ctx, conn, depid.New(srcBlocked, "hq-x-ghost"), srcBlocked, nil, nil, strptr("hq-x-ghost"))
	commitSeed(t, ctx, conn, "test: legacy cross-prefix rows before the repair pass")

	movedID := depid.New(srcBlocked, heldOpen)
	payloadBefore := readDepRowPayload(t, ctx, conn, movedID)
	stampBefore := readIssueStamp(t, ctx, conn, srcBlocked)
	if stampBefore.isBlocked != 0 {
		t.Fatalf("pre-repair is_blocked for %s = %d, want 0: an unresolvable external never blocked it", srcBlocked, stampBefore.isBlocked)
	}

	unrecordIgnoredVersionsFrom(t, ctx, conn, crossPrefixDepRepairMarkerVersion)
	closeConn()

	conn2, _ := openDepConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp over legacy cross-prefix rows: %v", err)
	}

	// The two held targets moved into depends_on_issue_id; the explicit
	// external reference and the missing foreign-prefix target stay in
	// depends_on_external.
	want := []depRow{
		{id: depid.New(srcBlocked, "external:JIRA-9"), issueID: srcBlocked, dependsOnExternal: "external:JIRA-9"},
		{id: movedID, issueID: srcBlocked, dependsOnIssueID: heldOpen},
		{id: depid.New(srcBlocked, "hq-x-ghost"), issueID: srcBlocked, dependsOnExternal: "hq-x-ghost"},
		{id: depid.New(srcUnblocked, heldClosed), issueID: srcUnblocked, dependsOnIssueID: heldClosed},
	}
	if got := depRowsByTarget(readDeps(t, ctx, conn2)); !slices.Equal(got, depRowsByTarget(want)) {
		t.Fatalf("dependencies after the repair = %v, want %v", got, want)
	}

	// The blocked state the misclassification froze: an open same-database
	// cross-prefix blocker now gates its source, a closed one does not, and
	// neither flips updated_at (the mark/unmark templates pin it).
	stampAfter := readIssueStamp(t, ctx, conn2, srcBlocked)
	if stampAfter.isBlocked != 1 {
		t.Fatalf("post-repair is_blocked for %s = %d, want 1: the repaired edge gates readiness", srcBlocked, stampAfter.isBlocked)
	}
	if stampAfter.updatedAt != stampBefore.updatedAt {
		t.Fatalf("updated_at for %s churned across the repair: got %q, want %q", srcBlocked, stampAfter.updatedAt, stampBefore.updatedAt)
	}
	if got := scalarInt(t, ctx, conn2, "SELECT is_blocked FROM issues WHERE id = ?", srcUnblocked); got != 0 {
		t.Fatalf("post-repair is_blocked for %s = %d, want 0: its blocker is closed", srcUnblocked, got)
	}

	// Identity and payload ride across the column move unchanged.
	if got := readDepRowPayload(t, ctx, conn2, movedID); got != payloadBefore {
		t.Fatalf("payload for %s after the repair = %+v, want %+v", movedID, got, payloadBefore)
	}

	if !ignoredVersionRecorded(t, ctx, conn2, crossPrefixDepRepairMarkerVersion) {
		t.Fatalf("ignored migration %d not recorded after a successful pass", crossPrefixDepRepairMarkerVersion)
	}
}

// TestEmbeddedCrossPrefixDepRepairRefusesDirtyWorkingSet pins the dirty-table
// contract (AC-6): a repair that would write refuses at plan time, before any
// write and before the 0032 marker records, with the same DirtyTablesError the
// lenient open intents already tolerate; the working set the user commits
// clears the refusal and the retry converges.
func TestEmbeddedCrossPrefixDepRepairRefusesDirtyWorkingSet(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMigratedStore(t, ctx)
	conn, closeConn := openDepConn(t, ctx, dataDir)

	const src, held, other = "bd-x-src", "hq-x-held", "bd-x-other"
	seedIssue(t, ctx, conn, src)
	seedIssue(t, ctx, conn, held)
	seedIssue(t, ctx, conn, other)
	seedDependency(t, ctx, conn, depid.New(src, held), src, nil, nil, strptr(held))
	commitSeed(t, ctx, conn, "test: legacy cross-prefix row before a dirty working set")

	unrecordIgnoredVersionsFrom(t, ctx, conn, crossPrefixDepRepairMarkerVersion)
	// An uncommitted user row leaves dependencies dirty in the working set —
	// the state the plan-time refusal exists to keep the repair out of.
	seedDependency(t, ctx, conn, depid.New(src, other), src, strptr(other), nil, nil)
	closeConn()

	conn2, _ := openDepConn(t, ctx, dataDir)
	_, err := schema.MigrateUp(ctx, conn2)
	var dirty *schema.DirtyTablesError
	if !errors.As(err, &dirty) {
		t.Fatalf("MigrateUp over a dirty dependencies table = %v, want *schema.DirtyTablesError", err)
	}
	if !slices.Contains(dirty.Tables, "dependencies") {
		t.Fatalf("DirtyTablesError.Tables = %v, want it to name dependencies", dirty.Tables)
	}

	// Nothing was written and the marker stayed pending: the refusal fired at
	// plan time, ahead of every write and ahead of ignoredSource.migrate.
	wantUnchanged := []depRow{
		{id: depid.New(src, held), issueID: src, dependsOnExternal: held},
		{id: depid.New(src, other), issueID: src, dependsOnIssueID: other},
	}
	if got := depRowsByTarget(readDeps(t, ctx, conn2)); !slices.Equal(got, depRowsByTarget(wantUnchanged)) {
		t.Fatalf("dependencies after the refused pass = %v, want %v: the refusal must write nothing", got, wantUnchanged)
	}
	if ignoredVersionRecorded(t, ctx, conn2, crossPrefixDepRepairMarkerVersion) {
		t.Fatalf("ignored migration %d recorded by the refused pass, want it pending for the retry", crossPrefixDepRepairMarkerVersion)
	}

	// The documented recovery: commit the working set at the current schema,
	// and the next open retries the pass and converges.
	mustExecConn(t, ctx, conn2, "CALL DOLT_ADD('-A')")
	mustExecConn(t, ctx, conn2, "CALL DOLT_COMMIT('-m', 'test: clear the dirty working set', '--skip-empty')")
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp after the working set was committed: %v", err)
	}
	wantRepaired := []depRow{
		{id: depid.New(src, held), issueID: src, dependsOnIssueID: held},
		{id: depid.New(src, other), issueID: src, dependsOnIssueID: other},
	}
	if got := depRowsByTarget(readDeps(t, ctx, conn2)); !slices.Equal(got, depRowsByTarget(wantRepaired)) {
		t.Fatalf("dependencies after the retry = %v, want %v", got, wantRepaired)
	}
	if !ignoredVersionRecorded(t, ctx, conn2, crossPrefixDepRepairMarkerVersion) {
		t.Fatalf("ignored migration %d not recorded after the retry converged", crossPrefixDepRepairMarkerVersion)
	}
}

// TestEmbeddedCrossPrefixDepRepairConvergedReopenIsNoOp pins the steady state
// (AC-7): once the marker records, a converged reopen short-circuits in
// migrationWorkNeeded before the repair, so a legacy-shaped row written after
// convergence is left alone rather than churned on every open.
func TestEmbeddedCrossPrefixDepRepairConvergedReopenIsNoOp(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMigratedStore(t, ctx)
	conn, closeConn := openDepConn(t, ctx, dataDir)

	const src, held = "bd-x-src", "hq-x-held"
	seedIssue(t, ctx, conn, src)
	seedIssue(t, ctx, conn, held)
	seedDependency(t, ctx, conn, depid.New(src, held), src, nil, nil, strptr(held))
	commitSeed(t, ctx, conn, "test: legacy cross-prefix row before the repair pass")

	unrecordIgnoredVersionsFrom(t, ctx, conn, crossPrefixDepRepairMarkerVersion)
	closeConn()

	conn2, closeConn2 := openDepConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp over the legacy row: %v", err)
	}
	if got := readDeps(t, ctx, conn2); !slices.Equal(got, []depRow{
		{id: depid.New(src, held), issueID: src, dependsOnIssueID: held},
	}) {
		t.Fatalf("dependencies after the repair pass = %v, want the row moved", got)
	}

	// A legacy-shaped row written after convergence: artificial by
	// construction now that the write path classifies by evidence, which is
	// exactly why the converged open must not touch it — the marker, not a
	// re-scan, is what says the repair is done.
	seedIssue(t, ctx, conn2, "hq-x-post")
	seedDependency(t, ctx, conn2, depid.New(src, "hq-x-post"), src, nil, nil, strptr("hq-x-post"))
	commitSeed(t, ctx, conn2, "test: a legacy-shaped row written after convergence")
	closeConn2()

	conn3, _ := openDepConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn3); err != nil {
		t.Fatalf("MigrateUp on the converged store: %v", err)
	}
	want := []depRow{
		{id: depid.New(src, held), issueID: src, dependsOnIssueID: held},
		{id: depid.New(src, "hq-x-post"), issueID: src, dependsOnExternal: "hq-x-post"},
	}
	if got := depRowsByTarget(readDeps(t, ctx, conn3)); !slices.Equal(got, depRowsByTarget(want)) {
		t.Fatalf("dependencies after the converged reopen = %v, want %v: the reopen must be a no-op", got, want)
	}
	if !ignoredVersionRecorded(t, ctx, conn3, crossPrefixDepRepairMarkerVersion) {
		t.Fatalf("ignored migration %d lost by the converged reopen", crossPrefixDepRepairMarkerVersion)
	}
}
