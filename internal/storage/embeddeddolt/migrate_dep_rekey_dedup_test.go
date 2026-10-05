//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// Real-Dolt coverage for the merge-aware dependency-id re-key
// (rekeyDependencyIDs, internal/storage/schema/dep_id_backfill.go) and its
// completion marker, ignored migration 0026 (gastownhall/beads#5268).
//
// Why this cannot be a sqlmock test. The bug IS Dolt's primary-key enforcement:
// the old row-at-a-time rewrite issued statements a statement-echo mock happily
// accepts, and only a real engine answers the second one with "duplicate primary
// key given" and leaves the table half-re-keyed. The same reason
// migrate_wisp_dep_forward_repair_test.go exists. These tests also exercise
// properties of MigrateUp's step ORDER rather than of any single statement: that
// a pending 0026 forces a pass on a database whose main cursor is already at
// latest, that an aborted re-key leaves 0026 unrecorded so the next open retries,
// and that the ON UPDATE CASCADE on the typed target columns is what makes a
// plain rename leave a stale primary key behind.

// depRekeyMarkerVersion is ignored migration 0026, the clone-local marker whose
// pending state forces the one repair pass. It is pinned here permanently: the
// marker shipped inside the v1.3.0 tag, so field stores have already recorded it
// at ordinal 26 and it can never move. Ignored migrations now sit above it --
// 0027 is main's add_wisps_current_revision, which the v1.3.0 forward-port
// renumbered out of the way after both branches independently claimed 0026. Do
// not chase the newest ordinal with this constant; it names one specific marker.
const depRekeyMarkerVersion = 26

// TestDepRekeyMarkerStillInIgnoredSeries keeps the fixtures in this file honest.
// They force the repair pass by unrecording from the marker, which only works
// while the pass is genuinely pending afterwards.
//
// The marker does not have to be the NEWEST ignored migration for that to hold,
// and since the forward-port it is not. unrecordIgnoredVersionsFrom deletes the
// cursor rows from the marker upward, so the cursor -- COALESCE(MAX(version), 0)
// -- falls below 26 and migrate re-applies the whole tail: 0026 comes back
// pending no matter how many versions sit above it. The cost of the range delete
// is that those later versions re-run too, so they have to stay re-runnable;
// ignored 0027 guards its ALTER on INFORMATION_SCHEMA for exactly that reason.
//
// What the fixtures cannot survive is the marker leaving the series. Renumbered
// or dropped, the unrecord would target an ordinal nothing owns, no pass would be
// forced, and the marker-driven tests below would fail with assertions that
// indict the repair code. This fails first, and says what actually broke.
func TestDepRekeyMarkerStillInIgnoredSeries(t *testing.T) {
	if got := schema.LatestIgnoredVersion(); got < depRekeyMarkerVersion {
		t.Fatalf("ignored series ends at %d, below the dep-rekey marker %d; the marker shipped "+
			"in the v1.3.0 tag and cannot move, so a series that no longer reaches it means 0026 "+
			"was renumbered or dropped -- restore it, or unrecordIgnoredVersionsFrom leaves no "+
			"marker pending and the fixtures below stop exercising the repair",
			got, depRekeyMarkerVersion)
	}
}

// TestEmbeddedDepRekeyRepairsHalfRekeyedDuplicateEdge is the victim repair: a
// database that a pre-1.3.0 binary left half-re-keyed and then declared
// migrated, because the per-step commits had already carried the main cursor to
// latest before the re-key aborted.
//
// The seeded shape is the reporter's, minus the 5,336-row haystack: one logical
// edge recorded twice, once as an issue ref and once as an external ref -- legal,
// since the unique keys are per typed column -- with the external twin already
// moved onto the deterministic id by the aborted pass.
func TestEmbeddedDepRekeyRepairsHalfRekeyedDuplicateEdge(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMigratedStore(t, ctx)
	conn, closeConn := openDepConn(t, ctx, dataDir)

	const source, target = "hq-cv-vvnmq", "ops-w05"
	want := depid.New(source, target)
	seedIssue(t, ctx, conn, source)
	seedIssue(t, ctx, conn, target)
	// The surviving orientation, still carrying its 0043-minted random id.
	seedDependency(t, ctx, conn, "random-issue-row", source, strptr(target), nil, nil)
	// The twin the aborted pass already moved onto the deterministic id. It is
	// the loser anyway: the survivor rule is the typed column, not who got there
	// first, so the repair has to free this id before reusing it.
	seedDependency(t, ctx, conn, want, source, nil, nil, strptr(target))
	commitSeed(t, ctx, conn, "test: reproduce the #5268 half-re-keyed duplicate edge")

	// What makes this database a class-2b victim: the main cursor is at latest
	// and every numbered migration is recorded, so without a pending marker
	// migrationWorkNeeded short-circuits and the re-key never runs again.
	unrecordIgnoredVersionsFrom(t, ctx, conn, depRekeyMarkerVersion)
	closeConn()

	conn2, _ := openDepConn(t, ctx, dataDir)

	// Pre-fix this returns "rekey dependency ids: dependencies: re-key id
	// random-issue-row -> ...: duplicate primary key given".
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp over a half-re-keyed duplicate edge: %v", err)
	}

	wantRows := []depRow{{id: want, issueID: source, dependsOnIssueID: target}}
	if got := readDeps(t, ctx, conn2); !slices.Equal(got, wantRows) {
		t.Fatalf("dependencies = %v, want %v", got, wantRows)
	}
	if !ignoredVersionRecorded(t, ctx, conn2, depRekeyMarkerVersion) {
		t.Fatalf("ignored migration %d not recorded after a successful pass", depRekeyMarkerVersion)
	}

	// With the marker recorded and both cursors at latest, migrationWorkNeeded
	// is false and MigrateUp short-circuits before the re-key. Prove that
	// directly rather than by "nothing changed": seed a row whose id IS
	// divergent, and assert the next open leaves it alone. If the pass still
	// ran, this row would be re-keyed.
	seedIssue(t, ctx, conn2, "steady-src")
	seedDependency(t, ctx, conn2, "steady-random-id", "steady-src", nil, nil, strptr("external:e1"))
	commitSeed(t, ctx, conn2, "test: a divergent row written after the repair pass")

	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp on the converged store: %v", err)
	}
	wantSteady := []depRow{
		{id: want, issueID: source, dependsOnIssueID: target},
		{id: "steady-random-id", issueID: "steady-src", dependsOnExternal: "external:e1"},
	}
	if got := readDeps(t, ctx, conn2); !slices.Equal(got, wantSteady) {
		t.Fatalf("after the steady-state open dependencies = %v, want %v", got, wantSteady)
	}
}

// TestEmbeddedDepRekeyMergesFreshDuplicateEdges is the class-1 victim: a
// database with duplicate typed edges and pending migrations, i.e. any rig that
// upgrades across the #4259 fix carrying an old wisp-to-issue conversion. The
// re-key runs because migrations are pending, and must merge rather than abort.
func TestEmbeddedDepRekeyMergesFreshDuplicateEdges(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	t.Run("dependencies, main cursor behind", func(t *testing.T) {
		dataDir := seedMainSchemaAt(t, ctx, 53)
		conn, closeConn := openDepConn(t, ctx, dataDir)

		seedIssue(t, ctx, conn, "src")
		seedIssue(t, ctx, conn, "tgt")
		seedIssue(t, ctx, conn, "other")
		// The duplicate pair, both still random, plus an unrelated edge that
		// must survive the pass re-keyed and intact.
		seedDependency(t, ctx, conn, "rand-a", "src", strptr("tgt"), nil, nil)
		seedDependency(t, ctx, conn, "rand-b", "src", nil, nil, strptr("tgt"))
		seedDependency(t, ctx, conn, "rand-c", "other", strptr("tgt"), nil, nil)
		commitSeed(t, ctx, conn, "test: duplicate typed edges before the 54..latest jump")
		closeConn()

		conn2, _ := openDepConn(t, ctx, dataDir)
		if _, err := schema.MigrateUp(ctx, conn2); err != nil {
			t.Fatalf("MigrateUp over duplicate typed edges: %v", err)
		}
		if v := currentMainVersion(t, ctx, conn2); v != schema.LatestVersion() {
			t.Fatalf("main schema version = %d, want latest %d", v, schema.LatestVersion())
		}

		want := []depRow{
			{id: depid.New("other", "tgt"), issueID: "other", dependsOnIssueID: "tgt"},
			{id: depid.New("src", "tgt"), issueID: "src", dependsOnIssueID: "tgt"},
		}
		if got := readDeps(t, ctx, conn2); !slices.Equal(got, want) {
			t.Fatalf("dependencies = %v, want %v", got, want)
		}
	})

	t.Run("wisp_dependencies", func(t *testing.T) {
		// The clone-local twin of the same table. Its re-key never syncs, but it
		// aborts the whole pass just as loudly, so it needs the same merge.
		dataDir := seedMigratedStore(t, ctx)
		conn, closeConn := openDepConn(t, ctx, dataDir)

		seedWisp(t, ctx, conn, "w1")
		seedIssue(t, ctx, conn, "wt")
		seedWispDependency(t, ctx, conn, "rand-w-a", "w1", strptr("wt"), nil, nil)
		seedWispDependency(t, ctx, conn, "rand-w-b", "w1", nil, nil, strptr("wt"))
		commitSeed(t, ctx, conn, "test: duplicate typed wisp edges")

		unrecordIgnoredVersionsFrom(t, ctx, conn, depRekeyMarkerVersion)
		closeConn()

		conn2, _ := openDepConn(t, ctx, dataDir)
		if _, err := schema.MigrateUp(ctx, conn2); err != nil {
			t.Fatalf("MigrateUp over duplicate typed wisp edges: %v", err)
		}
		want := []depRow{{id: depid.New("w1", "wt"), issueID: "w1", dependsOnIssueID: "wt"}}
		if got := readDepTable(t, ctx, conn2, "wisp_dependencies"); !slices.Equal(got, want) {
			t.Fatalf("wisp_dependencies = %v, want %v", got, want)
		}
	})
}

// TestEmbeddedDepRekeyConvergesRenameChain is the state a rename leaves behind
// on any binary that predates the issueops fix in this change: the FK's ON UPDATE
// CASCADE retargets depends_on_issue_id before the rewrite can match the old
// value, so the row keeps an id derived from the pre-rename target. Rename a
// second issue INTO the freed name and two rows contend for one deterministic
// id, with neither row corrupt.
//
// This is mechanically convergible -- vacate the occupant first -- and refusing
// it would fail every open of an ordinary workspace, so the planner orders the
// chain instead.
func TestEmbeddedDepRekeyConvergesRenameChain(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMigratedStore(t, ctx)
	conn, closeConn := openDepConn(t, ctx, dataDir)

	for _, id := range []string{"src", "name-a", "name-b"} {
		seedIssue(t, ctx, conn, id)
	}
	// src -> name-b, keyed as if its target had been renamed a -> b.
	seedDependency(t, ctx, conn, depid.New("src", "name-a"), "src", strptr("name-b"), nil, nil)
	// src -> name-a, keyed as if its target had been renamed c -> a. It wants
	// the id the row above is currently sitting on.
	seedDependency(t, ctx, conn, depid.New("src", "name-c"), "src", strptr("name-a"), nil, nil)
	commitSeed(t, ctx, conn, "test: two renames leaving a re-key chain")

	unrecordIgnoredVersionsFrom(t, ctx, conn, depRekeyMarkerVersion)
	closeConn()

	conn2, _ := openDepConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn2); err != nil {
		t.Fatalf("MigrateUp over a re-key chain: %v", err)
	}
	want := []depRow{
		{id: depid.New("src", "name-a"), issueID: "src", dependsOnIssueID: "name-a"},
		{id: depid.New("src", "name-b"), issueID: "src", dependsOnIssueID: "name-b"},
	}
	got := readDeps(t, ctx, conn2)
	slices.SortFunc(got, func(a, b depRow) int { return strings.Compare(a.id, b.id) })
	slices.SortFunc(want, func(a, b depRow) int { return strings.Compare(a.id, b.id) })
	if !slices.Equal(got, want) {
		t.Fatalf("dependencies = %v, want %v", got, want)
	}
}

// TestEmbeddedRenameKeepsDependencyIDsDeterministic closes the chain state at
// its source. updateIssueIDInTx renames the issues row first, so
// fk_dep_issue_target's ON UPDATE CASCADE has already moved depends_on_issue_id
// by the time replaceDependencyTargetInTx looks for the old value -- it matched
// nothing, and the row kept a primary key derived from the old name. After the
// fix the id is re-derived, so no rename mints a stale key for the migration
// pass to untangle later.
func TestEmbeddedRenameKeepsDependencyIDsDeterministic(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMigratedStore(t, ctx)
	conn, _ := openDepConn(t, ctx, dataDir)

	for _, id := range []string{"src", "old-target"} {
		seedIssue(t, ctx, conn, id)
	}
	seedDependency(t, ctx, conn, depid.New("src", "old-target"), "src", strptr("old-target"), nil, nil)
	commitSeed(t, ctx, conn, "test: an edge whose target is about to be renamed")

	renameIssue(t, ctx, conn, "old-target", "new-target")

	want := []depRow{{id: depid.New("src", "new-target"), issueID: "src", dependsOnIssueID: "new-target"}}
	if got := readDeps(t, ctx, conn); !slices.Equal(got, want) {
		t.Fatalf("after renaming the target, dependencies = %v, want %v", got, want)
	}

	// The source side already had this property (rekeyDependencySourceInTx);
	// assert it too so the two halves cannot drift apart.
	renameIssue(t, ctx, conn, "src", "new-src")
	want = []depRow{{id: depid.New("new-src", "new-target"), issueID: "new-src", dependsOnIssueID: "new-target"}}
	if got := readDeps(t, ctx, conn); !slices.Equal(got, want) {
		t.Fatalf("after renaming the source, dependencies = %v, want %v", got, want)
	}
}

// TestEmbeddedDepRekeyRefusesToWriteIntoDirtyDependencies covers the
// marker-versus-guard hazard. MigrateUp's changed-signature guard runs AFTER
// ignoredSource.migrate has durably recorded 0026, so a re-key that wrote into a
// pre-existing dirty table would fail the open once and then never retry: the
// heal would sit uncommitted and ride an unrelated user commit. The re-key
// refuses at plan time instead -- nothing written, marker unrecorded -- and the
// documented DirtyTablesError recovery converges it.
func TestEmbeddedDepRekeyRefusesToWriteIntoDirtyDependencies(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMigratedStore(t, ctx)
	conn, closeConn := openDepConn(t, ctx, dataDir)

	seedIssue(t, ctx, conn, "d-src")
	seedIssue(t, ctx, conn, "d-tgt")
	commitSeed(t, ctx, conn, "test: issues for the dirty-dependencies victim")
	// Deliberately NOT committed: this is the working-set state a batch-mode
	// session, or a pre-1.3.0 aborted re-key, leaves behind.
	seedDependency(t, ctx, conn, "dirty-random-id", "d-src", strptr("d-tgt"), nil, nil)
	unrecordIgnoredVersionsFrom(t, ctx, conn, depRekeyMarkerVersion)
	closeConn()

	conn2, closeConn2 := openDepConn(t, ctx, dataDir)
	before := readDeps(t, ctx, conn2)

	_, err := schema.MigrateUp(ctx, conn2)
	if err == nil {
		t.Fatal("MigrateUp re-keyed a pre-existing dirty dependencies table")
	}
	var dirtyErr *schema.DirtyTablesError
	if !errors.As(err, &dirtyErr) {
		t.Fatalf("error %v is not a *schema.DirtyTablesError", err)
	}
	if !slices.Contains(dirtyErr.Tables, "dependencies") {
		t.Errorf("dirtyErr.Tables = %v, want it to name dependencies", dirtyErr.Tables)
	}
	if got := readDeps(t, ctx, conn2); !slices.Equal(got, before) {
		t.Fatalf("dependencies = %v, want the pre-pass rows %v untouched", got, before)
	}
	if ignoredVersionRecorded(t, ctx, conn2, depRekeyMarkerVersion) {
		t.Fatalf("ignored migration %d recorded despite the refusal", depRekeyMarkerVersion)
	}

	// The documented recovery: commit the working set, re-open, converge.
	commitSeed(t, ctx, conn2, "test: commit the dirty dependency row")
	closeConn2()

	conn3, _ := openDepConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn3); err != nil {
		t.Fatalf("MigrateUp after committing the working set: %v", err)
	}
	want := []depRow{{id: depid.New("d-src", "d-tgt"), issueID: "d-src", dependsOnIssueID: "d-tgt"}}
	if got := readDeps(t, ctx, conn3); !slices.Equal(got, want) {
		t.Fatalf("dependencies = %v, want %v", got, want)
	}
	if !ignoredVersionRecorded(t, ctx, conn3, depRekeyMarkerVersion) {
		t.Fatalf("ignored migration %d not recorded after the retried pass", depRekeyMarkerVersion)
	}
}

// TestEmbeddedDepRekeyAbortLeavesMarkerUnrecorded pins the retry guarantee that
// the marker's position in MigrateUp buys: a re-key that cannot converge fails
// the pass BEFORE ignoredSource.migrate records 0026, so the database does not
// claim it migrated and the next open tries again. This is the property whose
// absence turned #5268 from a loud error into silent half-applied state.
//
// The unconvergible shape is a rotation: two rows each holding exactly the id
// the other needs, with no free id to move through. Unlike the chain above there
// is no ordering that works, and unlike a malformed squatter it is insertable
// under every constraint the live schema carries.
func TestEmbeddedDepRekeyAbortLeavesMarkerUnrecorded(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()

	dataDir := seedMigratedStore(t, ctx)
	conn, closeConn := openDepConn(t, ctx, dataDir)

	for _, id := range []string{"c-src", "c1", "c2"} {
		seedIssue(t, ctx, conn, id)
	}
	seedDependency(t, ctx, conn, depid.New("c-src", "c2"), "c-src", strptr("c1"), nil, nil)
	seedDependency(t, ctx, conn, depid.New("c-src", "c1"), "c-src", strptr("c2"), nil, nil)
	commitSeed(t, ctx, conn, "test: two dependency rows holding each other's deterministic id")

	unrecordIgnoredVersionsFrom(t, ctx, conn, depRekeyMarkerVersion)
	closeConn()

	conn2, closeConn2 := openDepConn(t, ctx, dataDir)
	before := readDeps(t, ctx, conn2)

	_, err := schema.MigrateUp(ctx, conn2)
	if err == nil {
		t.Fatal("MigrateUp succeeded over an unconvergible rotation")
	}
	var conflict *schema.DependencyRekeyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error %v is not a *schema.DependencyRekeyConflictError", err)
	}
	if !strings.Contains(err.Error(), "bd doctor") {
		t.Errorf("error %q does not point at bd doctor", err)
	}
	if ignoredVersionRecorded(t, ctx, conn2, depRekeyMarkerVersion) {
		t.Fatalf("ignored migration %d recorded despite the failed re-key", depRekeyMarkerVersion)
	}
	// Nothing was written: the plan is validated before the first statement.
	// Compared by value, since a row COUNT cannot see a pre-validation write.
	if got := readDeps(t, ctx, conn2); !slices.Equal(got, before) {
		t.Fatalf("dependencies = %v, want the seeded rows %v untouched", got, before)
	}

	// A read-only command must still open. The re-key is a convergence repair,
	// not a precondition for reading, and before it existed this clone opened
	// fine; bricking `bd list` on latent id corruption the user cannot inspect
	// without it would be the worse outcome. Nothing is lost: the marker is
	// still unrecorded, so the next strict open retries.
	closeConn2()
	store, err := embeddeddolt.OpenForReadOnlyCommand(ctx, filepath.Dir(dataDir), "testdb", "main")
	if err != nil {
		t.Fatalf("OpenForReadOnlyCommand over an unconvergible re-key: %v", err)
	}
	store.Close()

	// Repair the way `bd doctor` would -- break the rotation by giving one row
	// a free id -- and prove the retried pass converges.
	conn3, closeConn3 := openDepConn(t, ctx, dataDir)
	mustExecConn(t, ctx, conn3, "UPDATE dependencies SET id = ? WHERE id = ?",
		"interim-free-id", depid.New("c-src", "c2"))
	commitSeed(t, ctx, conn3, "test: break the re-key rotation")
	closeConn3()

	conn4, _ := openDepConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn4); err != nil {
		t.Fatalf("MigrateUp after breaking the rotation: %v", err)
	}
	want := []depRow{
		{id: depid.New("c-src", "c1"), issueID: "c-src", dependsOnIssueID: "c1"},
		{id: depid.New("c-src", "c2"), issueID: "c-src", dependsOnIssueID: "c2"},
	}
	got := readDeps(t, ctx, conn4)
	slices.SortFunc(got, func(a, b depRow) int { return strings.Compare(a.id, b.id) })
	slices.SortFunc(want, func(a, b depRow) int { return strings.Compare(a.id, b.id) })
	if !slices.Equal(got, want) {
		t.Fatalf("dependencies = %v, want %v", got, want)
	}
	if !ignoredVersionRecorded(t, ctx, conn4, depRekeyMarkerVersion) {
		t.Fatalf("ignored migration %d not recorded after the retried pass succeeded", depRekeyMarkerVersion)
	}
}

// --- helpers ---

type depRow struct {
	id                string
	issueID           string
	dependsOnIssueID  string
	dependsOnWispID   string
	dependsOnExternal string
}

// seedMigratedStore returns the dataDir of a store that has completed a full
// MigrateUp, i.e. one whose main AND ignored cursors are both at latest -- the
// state every marker-driven fixture below starts from.
func seedMigratedStore(t *testing.T, ctx context.Context) string {
	t.Helper()
	dataDir := seedMainSchemaAt(t, ctx, schema.LatestVersion())
	conn, closeConn := openDepConn(t, ctx, dataDir)
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp on the seed store: %v", err)
	}
	closeConn()
	return dataDir
}

// openDepConn is openPinnedConn with a guarded t.Cleanup. These tests reopen the
// store mid-test to force a fresh migration pass, so the close has to be
// callable explicitly AND be idempotent: without the cleanup, any t.Fatal
// between open and close leaks the pinned connection and the embedded Dolt
// engine behind it for the rest of the test binary.
func openDepConn(t *testing.T, ctx context.Context, dataDir string) (*sql.Conn, func()) {
	t.Helper()
	conn, cleanup := openPinnedConn(t, ctx, dataDir)
	var once sync.Once
	closeOnce := func() { once.Do(cleanup) }
	t.Cleanup(closeOnce)
	return conn, closeOnce
}

func seedDependency(t *testing.T, ctx context.Context, conn *sql.Conn, id, issueID string, issueTarget, wispTarget, externalTarget *string) {
	t.Helper()
	seedDepTable(t, ctx, conn, "dependencies", id, issueID, issueTarget, wispTarget, externalTarget)
}

func seedWispDependency(t *testing.T, ctx context.Context, conn *sql.Conn, id, issueID string, issueTarget, wispTarget, externalTarget *string) {
	t.Helper()
	seedDepTable(t, ctx, conn, "wisp_dependencies", id, issueID, issueTarget, wispTarget, externalTarget)
}

// seedDepTable inserts an edge with an EXPLICIT id, which is the whole point:
// the affected rows predate the deterministic derivation, so they cannot be
// created through AddDependency. It deliberately does not replace
// migrate_wisp_dep_forward_repair_test.go's seedWispDep, which seeds the LEGACY
// wisp_dependencies shape -- that table has no id column at all, so the two
// helpers cannot share an INSERT.
func seedDepTable(t *testing.T, ctx context.Context, conn *sql.Conn, table, id, issueID string, issueTarget, wispTarget, externalTarget *string) {
	t.Helper()
	mustExecConn(t, ctx, conn, `
INSERT INTO `+table+` (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata)
VALUES (?, ?, ?, ?, ?, 'blocks', NOW(), 'tester', JSON_OBJECT())`,
		id, issueID, nullable(issueTarget), nullable(wispTarget), nullable(externalTarget))
}

// renameIssue drives the real rename path (issueops.UpdateIssueIDInTx), so the
// FK's ON UPDATE CASCADE fires exactly as it does for `bd rename`.
func renameIssue(t *testing.T, ctx context.Context, conn *sql.Conn, oldID, newID string) {
	t.Helper()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin rename tx: %v", err)
	}
	issue := &types.Issue{ID: newID, Title: "i", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := issueops.UpdateIssueIDInTx(ctx, tx, oldID, newID, issue, "tester"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("rename %s -> %s: %v", oldID, newID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit rename %s -> %s: %v", oldID, newID, err)
	}
}

func readDeps(t *testing.T, ctx context.Context, conn *sql.Conn) []depRow {
	t.Helper()
	return readDepTable(t, ctx, conn, "dependencies")
}

func readDepTable(t *testing.T, ctx context.Context, conn *sql.Conn, table string) []depRow {
	t.Helper()
	rows, err := conn.QueryContext(ctx, `
SELECT id, issue_id,
       COALESCE(depends_on_issue_id, ''),
       COALESCE(depends_on_wisp_id, ''),
       COALESCE(depends_on_external, '')
FROM `+table+`
ORDER BY issue_id, id`)
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()

	var out []depRow
	for rows.Next() {
		var r depRow
		if err := rows.Scan(&r.id, &r.issueID, &r.dependsOnIssueID, &r.dependsOnWispID, &r.dependsOnExternal); err != nil {
			t.Fatalf("scan %s row: %v", table, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", table, err)
	}
	return out
}

// unrecordIgnoredVersionsFrom removes the ignored-series cursor rows from
// version upward, reproducing the state a clone is in before it has run the
// marker's pass. It deletes a RANGE rather than the single row because the
// cursor is COALESCE(MAX(version), 0) and migrate only applies versions above
// it: with a later ignored migration present, dropping only 0026 would leave the
// cursor at latest and the pass would silently never run. The cursor table is
// dolt-ignored, so this needs no commit.
func unrecordIgnoredVersionsFrom(t *testing.T, ctx context.Context, conn *sql.Conn, version int) {
	t.Helper()
	mustExecConn(t, ctx, conn, "DELETE FROM ignored_schema_migrations WHERE version >= ?", version)
}

func ignoredVersionRecorded(t *testing.T, ctx context.Context, conn *sql.Conn, version int) bool {
	t.Helper()
	return scalarInt(t, ctx, conn,
		"SELECT COUNT(*) FROM ignored_schema_migrations WHERE version = ?", version) > 0
}
