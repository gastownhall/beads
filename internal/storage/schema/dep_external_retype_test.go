package schema

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// TestLatestIgnoredVersionIncludesMigration0032 pins the ignored slot the
// cross-prefix retype repair claims (gastownhall/beads#8299), directly above
// the pre-upgrade events restore's (ignored 0031), and that the slot carries
// its repair.
func TestLatestIgnoredVersionIncludesMigration0032(t *testing.T) {
	const wantIgnored = 32
	if got := LatestIgnoredVersion(); got != wantIgnored {
		t.Fatalf("LatestIgnoredVersion() = %d, want %d (same-database cross-prefix retype repair)", got, wantIgnored)
	}
	if _, ok := preMigrationRepairs[repairKey{ignoredSource.cursorTable, wantIgnored}]; !ok {
		t.Fatalf("ignored 0032 has no pre-migration repair registered; the marker alone retypes nothing")
	}
}

// TestRetypeRepairTransactionGateIsMarker32Only pins the boundary's scoping:
// only the ignored source's marker 32 runs its repair inside a driver
// transaction — every other repair, in either source, keeps running directly
// on the pass's connection, and a main-source migration that someday claims
// the same number does not silently pick the boundary up.
func TestRetypeRepairTransactionGateIsMarker32Only(t *testing.T) {
	if !ignoredSource.repairRunsInTransaction(32) {
		t.Fatal("ignored 0032 must run its repair inside the driver transaction")
	}
	for _, version := range []int{0, 7, 15, 26, 31, 33} {
		if ignoredSource.repairRunsInTransaction(version) {
			t.Fatalf("ignored version %d must not claim the repair-transaction boundary", version)
		}
	}
	for _, version := range []int{32, 40, 41, 47, 53} {
		if mainSource.repairRunsInTransaction(version) {
			t.Fatalf("main version %d must not claim the repair-transaction boundary", version)
		}
	}
}

// TestMigration0032MarkerIsSeenTouchingItsTables pins the raw-text
// declarations the pending-migration dirty-table guard relies on: the
// marker's repair writes both edge tables and both issue tables, so a
// tracked, dirty one of them must refuse the pass before the repair
// mutates anything.
func TestMigration0032MarkerIsSeenTouchingItsTables(t *testing.T) {
	markerSQL, err := IgnoredMigrationSQL("0032_dep_external_retype_marker.up.sql")
	if err != nil {
		t.Fatalf("IgnoredMigrationSQL(0032) = %v", err)
	}
	for _, table := range []string{"dependencies", "wisp_dependencies", "issues", "wisps"} {
		if !migrationSQLTouchesTable(markerSQL, table) {
			t.Fatalf("ignored 0032 does not declare its %s write; the dirty-table guard would not see it", table)
		}
	}
	if migrationSQLTouchesTable(markerSQL, "events") || migrationSQLTouchesTable(markerSQL, "bd_events_journal") {
		t.Fatal("ignored 0032 declares a write to a table its repair never touches")
	}
}

// TestRetypeRepairRefusesNonPinnedSession pins the boundary's refusal
// contract: a caller that cannot supply MigrateUp's pinned single session
// fails BEFORE the repair runs any statement — a pooled *sql.DB and an
// already-open *sql.Tx both implement enough of database/sql to be
// dangerous, and neither may run the repair without the atomic boundary.
func TestRetypeRepairRefusesNonPinnedSession(t *testing.T) {
	mf := migrationFile{version: depExternalRetypeMarkerVersion, name: "0032_dep_external_retype_marker.up.sql"}
	ctx := context.Background()

	t.Run("plain mock", func(t *testing.T) {
		err := ignoredSource.preMigrationRepairInDriverTx(ctx, &mockDB{}, mf)
		if err == nil {
			t.Fatal("expected a refusal for a DBConn with no pinned session")
		}
		if !strings.Contains(err.Error(), "pinned single session") {
			t.Fatalf("refusal should name the pinned-session contract, got: %v", err)
		}
	})

	t.Run("pooled handle", func(t *testing.T) {
		db, _, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer func() { _ = db.Close() }()
		err = ignoredSource.preMigrationRepairInDriverTx(ctx, db, mf)
		if err == nil {
			t.Fatal("expected a refusal for a pooled *sql.DB despite its BeginTx")
		}
	})

	t.Run("supplied transaction", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer func() { _ = db.Close() }()
		mock.ExpectBegin()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		err = ignoredSource.preMigrationRepairInDriverTx(ctx, tx, mf)
		if err == nil {
			t.Fatal("expected a refusal for a supplied *sql.Tx: the boundary must not nest")
		}
	})
}

// TestRetypePlanKeepsHierarchyViolationsExternal pins the conflict half of
// the 0032 plan: a legacy blocking row whose conversion would create an edge
// the write path refuses — the target is the source's own ancestor — stays
// external and is named in a removal notice, while an eligible row in the
// same plan is still converted. planDepExternalRetype writes nothing, so the
// whole population decision is observable against a statement-echo mock: the
// Dolt semantics of the walk are AC-6's embedded test's business.
func TestRetypePlanKeepsHierarchyViolationsExternal(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	const source = "hq-kid"
	const ancestor = "hq-anc" // present as an issue AND the source's own ancestor
	const eligibleSource = "hq-ok"
	const eligibleTarget = "ops-t2"

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WithArgs("dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT id, issue_id, depends_on_external, type FROM dependencies`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "depends_on_external", "type"}).
			AddRow(depid.New(source, ancestor), source, ancestor, "blocks").
			AddRow(depid.New(eligibleSource, eligibleTarget), eligibleSource, eligibleTarget, "blocks"))
	// The violating row: a present target, then the blocker-ancestor probe
	// fires once and settles it — the descendant probe never runs.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM wisps`).WithArgs(ancestor).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM issues`).WithArgs(ancestor).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM ancestors WHERE node = \?`).WithArgs(source, ancestor).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	// The eligible row: a present target, both probes negative.
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM wisps`).WithArgs(eligibleTarget).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM issues`).WithArgs(eligibleTarget).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM ancestors WHERE node = \?`).WithArgs(eligibleSource, eligibleTarget).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM ancestors WHERE node = \?`).WithArgs(eligibleTarget, eligibleSource).
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM INFORMATION_SCHEMA\.TABLES`).
		WithArgs("wisp_dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	var notice bytes.Buffer
	origStderr := stderr
	stderr = &notice
	t.Cleanup(func() { stderr = origStderr })

	rows, err := planDepExternalRetype(context.Background(), db)
	if err != nil {
		t.Fatalf("planDepExternalRetype: %v", err)
	}
	if len(rows) != 1 || rows[0].id != depid.New(eligibleSource, eligibleTarget) ||
		rows[0].kind != issueops.DepTargetIssue || rows[0].table != "dependencies" {
		t.Fatalf("plan = %+v, want only the eligible row re-typed to its issue target", rows)
	}
	if got := notice.String(); !strings.Contains(got, "keeping cross-prefix edge "+source+" -> "+ancestor+" external") ||
		!strings.Contains(got, "bd dep remove "+source+" "+ancestor) {
		t.Fatalf("removal notice = %q, want it to name the source, the target and the removal command", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}
