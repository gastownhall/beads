package schema

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// GH#5269, AC-3: on a database whose recorded marks outran its DDL — the
// reporter's v53-marks/v49-DDL state — the explicit repair corrects exactly
// the drifted shapes, stages them by name into one repair commit, and leaves
// cursors, rows and healthy columns alone. sqlmock pins the statement shape;
// the embedded and CLI tiers pin the durable behaviour.
func TestRepairDroppedDefaultDriftRepairsRecordedDrift(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Both cursors record their series' latest: every obligation is covered.
	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(74))
	mock.ExpectQuery(`SELECT MAX\(version\) FROM ignored_schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(31))
	// Probes, sorted by table: comments (drift), compaction_snapshots,
	// dependencies (drift), events, issue_snapshots, wisp_comments,
	// wisp_dependencies, wisp_events.
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM comments LIKE 'id'$`).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM compaction_snapshots LIKE 'id'$`).WillReturnRows(idDefault(nil))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM dependencies LIKE 'id'$`).WillReturnRows(idDefault("(uuid())"))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM events LIKE 'id'$`).WillReturnRows(idDefault(nil))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM issue_snapshots LIKE 'id'$`).WillReturnRows(idDefault(nil))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM wisp_comments LIKE 'id'$`).WillReturnRows(idDefault(nil))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM wisp_dependencies LIKE 'id'$`).WillReturnRows(idDefault(nil))
	mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(showTables("comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events"))
	mock.ExpectQuery(`^SHOW COLUMNS FROM wisp_events LIKE 'id'$`).WillReturnRows(idDefault(nil))

	// Session hygiene: transaction commit already off; working set clean.
	mock.ExpectQuery(`SELECT @@SESSION\.dolt_transaction_commit`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(0))
	mock.ExpectQuery(`FROM dolt_status`).WillReturnRows(
		sqlmock.NewRows([]string{"table_name", "staged"}))
	// Commit eligibility: the wisp tables are under dolt_ignore.
	mock.ExpectQuery(`FROM INFORMATION_SCHEMA\.TABLES`).
		WillReturnRows(sqlmock.NewRows([]string{"TABLE_NAME"}).
			AddRow("comments").AddRow("compaction_snapshots").AddRow("dependencies").
			AddRow("events").AddRow("issue_snapshots"))

	// Corrections, in probe order: comments, then dependencies.
	mock.ExpectExec("ALTER TABLE `comments` ALTER COLUMN `id` DROP DEFAULT").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`^SHOW COLUMNS FROM comments LIKE 'id'$`).WillReturnRows(idDefault(nil))
	mock.ExpectExec("ALTER TABLE `dependencies` ALTER COLUMN `id` DROP DEFAULT").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`^SHOW COLUMNS FROM dependencies LIKE 'id'$`).WillReturnRows(idDefault(nil))

	// Exactly the two repaired committable tables are staged, by name, under
	// the distinct repair message.
	mock.ExpectQuery(`CALL DOLT_ADD\('-f', \?\)`).WithArgs("comments").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(`CALL DOLT_ADD\('-f', \?\)`).WithArgs("dependencies").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(`CALL DOLT_COMMIT\('-m', \?\)`).WithArgs("schema: repair dropped-default post-conditions").
		WillReturnRows(sqlmock.NewRows([]string{"status"}))

	repairs, err := RepairDroppedDefaultDrift(context.Background(), db)
	if err != nil {
		t.Fatalf("RepairDroppedDefaultDrift: %v", err)
	}
	if strings.Join(repairs, ",") != "comments.id,dependencies.id" {
		t.Fatalf("repairs = %v, want [comments.id dependencies.id]", repairs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// A healthy database records no repairs and issues no DDL at all — a repeat
// run after a repair is a no-op with no commit (AC-3).
func TestRepairDroppedDefaultDriftHealthyStoreWritesNothing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(74))
	mock.ExpectQuery(`SELECT MAX\(version\) FROM ignored_schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(31))
	allTables := showTables("comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events")
	for _, col := range []string{"comments", "compaction_snapshots", "dependencies", "events", "issue_snapshots", "wisp_comments", "wisp_dependencies", "wisp_events"} {
		mock.ExpectQuery(`^SHOW TABLES$`).WillReturnRows(allTables)
		mock.ExpectQuery(fmt.Sprintf(`^SHOW COLUMNS FROM %s LIKE 'id'$`, col)).WillReturnRows(idDefault(nil))
	}

	repairs, err := RepairDroppedDefaultDrift(context.Background(), db)
	if err != nil {
		t.Fatalf("RepairDroppedDefaultDrift on a healthy store: %v", err)
	}
	if repairs != nil {
		t.Fatalf("repairs = %v, want none", repairs)
	}
	// No session write at all: not even the transaction-commit read is
	// reached, and the mock would flag any ALTER/ADD/COMMIT.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// Obligations only apply to versions the cursors actually record: a store
// migrated to v49 owes nothing for 0050/0051 (their marks are absent), and a
// missing cursor table records nothing.
func TestRecordedDroppedDefaultObligationsFollowTheRecordedCursors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT MAX\(version\) FROM schema_migrations`).
		WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(49))
	mock.ExpectQuery(`SELECT MAX\(version\) FROM ignored_schema_migrations`).
		WillReturnError(errors.New("table not found"))

	got := recordedDroppedDefaultObligations(context.Background(), db)
	// v43's dependencies.id obligation is covered (43 <= 49); 0050's duplicate
	// deduplicates into it; everything 51+ is not recorded; the ignored cursor
	// is absent, so no wisp obligation applies.
	if len(got) != 1 || got[0].table != "dependencies" || got[0].column != "id" || got[0].version != 43 {
		t.Fatalf("obligations = %+v, want exactly the v43 dependencies.id obligation", got)
	}
}

// The pinning rule: every default a frozen migration body drops must be
// pinned in droppedDefaultObligations, and no migration after a pinned version
// may re-introduce that default — retiring an obligation is an explicit,
// reviewed edit of the pin table (a superseding migration, not an accident).
func TestDroppedDefaultObligationsArePinnedAndRetiredExplicitly(t *testing.T) {
	// Lowest pinned version per obligation (0043 and 0050 both pin
	// dependencies.id; the earliest retains the provenance).
	pinned := map[string]int{}
	for _, o := range droppedDefaultObligations {
		key := o.cursorTable + "|" + o.table + "." + o.column
		if v, ok := pinned[key]; !ok || o.version < v {
			pinned[key] = o.version
		}
	}
	for _, src := range []migrationSource{mainSource, ignoredSource} {
		for _, mf := range src.list() {
			data, err := src.files.ReadFile(src.dir + "/" + mf.name)
			if err != nil {
				t.Fatalf("reading %s: %v", mf.name, err)
			}
			for _, d := range droppedDefaults(string(data)) {
				key := src.cursorTable + "|" + d.table + "." + d.column
				if _, ok := pinned[key]; !ok {
					t.Errorf("%s drops the default on %s.%s, but no obligation is pinned in droppedDefaultObligations "+
						"(GH#5269): pin it so the explicit repair and doctor can see it", mf.name, d.table, d.column)
				}
			}
			for _, m := range setDefaultRe.FindAllStringSubmatch(string(data), -1) {
				table, column := strings.ToLower(m[1]), strings.ToLower(m[2])
				key := src.cursorTable + "|" + table + "." + column
				if v, ok := pinned[key]; ok && mf.version > v {
					t.Errorf("%s re-introduces a DEFAULT on %s.%s, whose drop %s v%04d pinned (GH#5269): "+
						"retire the obligation in droppedDefaultObligations explicitly", mf.name, table, column, src.cursorTable, v)
				}
			}
		}
	}
}
