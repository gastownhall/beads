//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// Real-Dolt coverage for ignored 0032's pre-migration repair
// (repairCrossPrefixExternalDeps, internal/storage/schema): the prefix-era
// dependency classifier filed same-database cross-prefix targets as external
// (gastownhall/beads#8299), so `bd ready` ignored edges this database could
// have resolved. The repair runs inside one driver transaction on the pinned
// session (the rollback half is pinned by the fault-hook tests in the schema
// package); this file pins the end-to-end population semantics only real Dolt
// can answer: which rows convert, which stay, what the conversion preserves,
// and what the blocked-state recompute does to sources and descendants.

// seedParentChildEdge inserts the issue parent-child edge the blocked-state
// walk treats as the hierarchy: child -> parent, type 'parent-child'. The
// shared seedDepTable hardcodes 'blocks', and the child edge must not be one.
func seedParentChildEdge(t *testing.T, ctx context.Context, conn *sql.Conn, child, parent string) {
	t.Helper()
	mustExecConn(t, ctx, conn, `
INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by, metadata)
VALUES (?, ?, ?, 'parent-child', NOW(), 'tester', JSON_OBJECT())`,
		depid.New(child, parent), child, parent)
}

// indexDepRows keys dep rows by id so assertions do not depend on Dolt's
// string ordering of the derived ids.
func indexDepRows(t *testing.T, rows []depRow) map[string]depRow {
	t.Helper()
	out := make(map[string]depRow, len(rows))
	for _, r := range rows {
		if _, dup := out[r.id]; dup {
			t.Fatalf("duplicate dependency id %s", r.id)
		}
		out[r.id] = r
	}
	return out
}

// scalarStringArg is scalarString for a one-argument query.
func scalarStringArg(t *testing.T, ctx context.Context, conn *sql.Conn, query, arg string) string {
	t.Helper()
	var v string
	if err := conn.QueryRowContext(ctx, query, arg).Scan(&v); err != nil {
		t.Fatalf("scalar %q: %v", query, err)
	}
	return v
}

// TestEmbeddedDepExternalRetypeRepairsLegacyCrossPrefixRows is the AC-6 end
// to end: legacy external rows whose targets this database holds are
// re-typed in BOTH dependency tables, the affected sources AND their
// descendants are recomputed, provenance and timestamps survive untouched,
// and a second open changes nothing. Absent targets and explicit
// "external:" references stay external — the multi-rig "add now, route
// later" population the issue explicitly keeps.
//
// PRE-fix (before ignored 0032 existed) this same test fails at the first
// readback: LatestIgnoredVersion is 31, unrecording from 32 is a no-op, the
// pass applies nothing, and every misfiled row stays external with the
// sources still unblocked.
func TestEmbeddedDepExternalRetypeRepairsLegacyCrossPrefixRows(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMigratedStore(t, ctx)
	conn, closeConn := openDepConn(t, ctx, dataDir)

	// Cross-prefix is the point: every id deliberately mixes project
	// prefixes, the shape the prefix-era classifier mis-filed.
	const source = "hq-src"
	const issueTarget = "ops-issue-tgt" // an issue THIS database holds
	const wispTarget = "ops-wisp-tgt"   // a wisp THIS database holds
	const child = "hq-src-kid"          // the source's child, a recompute descendant
	const wispSource = "hq-wisp-src"
	seedIssue(t, ctx, conn, source)
	seedIssue(t, ctx, conn, issueTarget)
	seedWisp(t, ctx, conn, wispTarget)
	seedIssue(t, ctx, conn, child)
	seedWisp(t, ctx, conn, wispSource)

	// dependencies: three legacy-external rows plus the two populations that
	// must stay external. Ids are the depid.New values, so the re-key that
	// runs earlier in the same pass is a no-op and the repair's id
	// preservation is observable against exactly these strings.
	const ghostTarget = "ops-ghost"       // absent everywhere: stays external
	const explicitTarget = "external:ticket-77" // explicit reference: external by definition
	seedDependency(t, ctx, conn, depid.New(source, issueTarget), source, nil, nil, strptr(issueTarget))
	seedDependency(t, ctx, conn, depid.New(source, wispTarget), source, nil, nil, strptr(wispTarget))
	seedDependency(t, ctx, conn, depid.New(source, ghostTarget), source, nil, nil, strptr(ghostTarget))
	seedDependency(t, ctx, conn, depid.New(source, explicitTarget), source, nil, nil, strptr(explicitTarget))
	// The child's hierarchy edge: a descendant the recompute seed must
	// reach through the same AffectedBy* walk a dep write uses.
	seedParentChildEdge(t, ctx, conn, child, source)
	// wisp_dependencies: the same misfiled shape from a wisp source.
	seedWispDependency(t, ctx, conn, depid.New(wispSource, issueTarget), wispSource, nil, nil, strptr(issueTarget))

	commitSeed(t, ctx, conn, "seed legacy external cross-prefix dep rows")

	// Timestamps and provenance the repair must not touch.
	beforeUpdatedAt := scalarStringArg(t, ctx, conn, "SELECT updated_at FROM issues WHERE id = ?", source)
	beforeChildUpdatedAt := scalarStringArg(t, ctx, conn, "SELECT updated_at FROM issues WHERE id = ?", child)
	beforeWispUpdatedAt := scalarStringArg(t, ctx, conn, "SELECT updated_at FROM wisps WHERE id = ?", wispSource)
	convertedID := depid.New(source, issueTarget)
	beforeCreatedAt := scalarStringArg(t, ctx, conn, "SELECT created_at FROM dependencies WHERE id = ?", convertedID)
	if beforeCreatedAt == "" {
		t.Fatal("no created_at on the converted row; the fixture is wrong")
	}

	// The state a clone is in before the marker's pass: ignored 0032 pending.
	unrecordIgnoredVersionsFrom(t, ctx, conn, 32)
	closeConn()

	conn2, _ := openDepConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp with ignored 0032 pending: %v", err)
	}
	if !ignoredVersionRecorded(t, ctx, conn2, 32) {
		t.Fatal("ignored 0032 was not recorded; the repair must not strand the marker")
	}

	// --- both tables re-typed, populations preserved ---
	got := indexDepRows(t, readDeps(t, ctx, conn2))
	wantDeps := map[string]depRow{
		depid.New(source, issueTarget):   {id: depid.New(source, issueTarget), issueID: source, dependsOnIssueID: issueTarget},
		depid.New(source, wispTarget):    {id: depid.New(source, wispTarget), issueID: source, dependsOnWispID: wispTarget},
		depid.New(source, ghostTarget):   {id: depid.New(source, ghostTarget), issueID: source, dependsOnExternal: ghostTarget},
		depid.New(source, explicitTarget): {id: depid.New(source, explicitTarget), issueID: source, dependsOnExternal: explicitTarget},
		depid.New(child, source):         {id: depid.New(child, source), issueID: child, dependsOnIssueID: source},
	}
	if len(got) != len(wantDeps) {
		t.Fatalf("dependencies now hold %d rows, want %d: %+v", len(got), len(wantDeps), got)
	}
	for id, want := range wantDeps {
		if row, ok := got[id]; !ok {
			t.Fatalf("dependencies lost row %s (id %s), want %+v", id, id, want)
		} else if row != want {
			t.Fatalf("dependencies row %s = %+v, want %+v", id, row, want)
		}
	}
	gotWispDeps := indexDepRows(t, readDepTable(t, ctx, conn2, "wisp_dependencies"))
	wantWispID := depid.New(wispSource, issueTarget)
	if len(gotWispDeps) != 1 {
		t.Fatalf("wisp_dependencies now hold %d rows, want the one seeded: %+v", len(gotWispDeps), gotWispDeps)
	}
	if row := gotWispDeps[wantWispID]; row != (depRow{id: wantWispID, issueID: wispSource, dependsOnIssueID: issueTarget}) {
		t.Fatalf("wisp_dependencies row %s = %+v, want the external target re-typed into depends_on_issue_id", wantWispID, row)
	}

	// --- provenance and timestamps unchanged ---
	if got := scalarStringArg(t, ctx, conn2, "SELECT created_at FROM dependencies WHERE id = ?", convertedID); got != beforeCreatedAt {
		t.Fatalf("converted row's created_at changed: %q -> %q", beforeCreatedAt, got)
	}
	if got := scalarStringArg(t, ctx, conn2, "SELECT created_by FROM dependencies WHERE id = ?", convertedID); got != "tester" {
		t.Fatalf("converted row's created_by changed: want \"tester\", got %q", got)
	}
	if got := scalarStringArg(t, ctx, conn2, "SELECT type FROM dependencies WHERE id = ?", convertedID); got != "blocks" {
		t.Fatalf("converted row's type changed: want \"blocks\", got %q", got)
	}
	for _, probe := range []struct{ table, id, before string }{
		{"issues", source, beforeUpdatedAt},
		{"issues", child, beforeChildUpdatedAt},
		{"wisps", wispSource, beforeWispUpdatedAt},
	} {
		if got := scalarStringArg(t, ctx, conn2, "SELECT updated_at FROM "+probe.table+" WHERE id = ?", probe.id); got != probe.before {
			t.Fatalf("%s %s updated_at changed across the recompute: %q -> %q", probe.table, probe.id, probe.before, got)
		}
	}

	// --- affected sources and descendants recomputed ---
	for _, blocked := range []struct{ table, id string }{
		{"issues", source},
		{"issues", child},
		{"wisps", wispSource},
	} {
		if got := scalarInt(t, ctx, conn2, "SELECT is_blocked FROM "+blocked.table+" WHERE id = ?", blocked.id); got != 1 {
			t.Fatalf("%s %s is_blocked = %d after the repair, want 1: the re-typed edge must block readiness again", blocked.table, blocked.id, got)
		}
	}
	// The absent target still blocks nothing.
	if got := scalarInt(t, ctx, conn2, "SELECT is_blocked FROM issues WHERE id = ?", issueTarget); got != 0 {
		t.Fatalf("issue target %s is_blocked = %d, want 0: nothing blocks a target", issueTarget, got)
	}

	// --- a second open changes nothing ---
	beforeSecond := readDeps(t, ctx, conn2)
	beforeSecondWispDeps := readDepTable(t, ctx, conn2, "wisp_dependencies")
	applied, err := schema.MigrateUp(ctx, conn2)
	if err != nil {
		t.Fatalf("second MigrateUp: %v", err)
	}
	if applied != 0 {
		t.Fatalf("second MigrateUp applied %d migrations, want 0: the recorded marker must make the pass a no-op", applied)
	}
	afterSecond := readDeps(t, ctx, conn2)
	afterSecondWispDeps := readDepTable(t, ctx, conn2, "wisp_dependencies")
	if len(beforeSecond) != len(afterSecond) || len(beforeSecondWispDeps) != len(afterSecondWispDeps) {
		t.Fatalf("second open changed row counts: deps %d -> %d, wisp deps %d -> %d",
			len(beforeSecond), len(afterSecond), len(beforeSecondWispDeps), len(afterSecondWispDeps))
	}
	for i := range beforeSecond {
		if beforeSecond[i] != afterSecond[i] {
			t.Fatalf("second open changed dependencies row %d: %+v -> %+v", i, beforeSecond[i], afterSecond[i])
		}
	}
	for i := range beforeSecondWispDeps {
		if beforeSecondWispDeps[i] != afterSecondWispDeps[i] {
			t.Fatalf("second open changed wisp_dependencies row %d: %+v -> %+v", i, beforeSecondWispDeps[i], afterSecondWispDeps[i])
		}
	}
}
