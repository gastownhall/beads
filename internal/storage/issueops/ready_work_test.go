package issueops

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// deferredParentExistsRegex matches sqlbuild.AnyDeferredParentSQL for one
// table: an EXISTS per branch of the deferred-parent rule, status deferred
// (plain `bd defer`, GH#7300) or a future defer_until.
func deferredParentExistsRegex(issueTable string) string {
	return `\(EXISTS \(SELECT 1 FROM ` + issueTable + ` WHERE status = 'deferred'\) OR ` +
		`EXISTS \(SELECT 1 FROM ` + issueTable + ` WHERE defer_until IS NOT NULL AND defer_until > UTC_TIMESTAMP\(\)\)\)`
}

// deferredParentProbeRegex matches the per-table fallback probe.
func deferredParentProbeRegex(issueTable string) string {
	return `^SELECT ` + deferredParentExistsRegex(issueTable) + `$`
}

// staleBlockedQueryRegex matches sqlbuild.StaleBlockedIDsSQL for one table
// family: the read of its stale-flagged rows every ready path makes.
func staleBlockedQueryRegex(table string) string {
	return `^SELECT t\.id FROM ` + table + ` t WHERE t\.is_blocked = 1 AND NOT EXISTS`
}

func deferredChildrenQueryRegex(depTable, issueTable string) string {
	targetCol := "depends_on_issue_id"
	if issueTable == "wisps" {
		targetCol = "depends_on_wisp_id"
	}
	return `SELECT dep\.issue_id\s+FROM ` + depTable + ` dep\s+JOIN ` + issueTable + ` parent ON parent\.id = dep\.` + targetCol +
		`\s+WHERE dep\.type = 'parent-child'\s+AND \(parent\.status = 'deferred' OR \(parent\.defer_until IS NOT NULL AND parent\.defer_until > UTC_TIMESTAMP\(\)\)\)`
}

func deferredProbeAnswer(found bool) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"deferred"}).AddRow(found)
}

func beginMockTx(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *sql.Tx) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })

	return db, mock, tx
}

func TestBuildSQLInClause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		ids              []string
		wantPlaceholders string
		wantArgs         []interface{}
	}{
		{
			name:             "single ID",
			ids:              []string{"42"},
			wantPlaceholders: "?",
			wantArgs:         []interface{}{"42"},
		},
		{
			name:             "multiple IDs",
			ids:              []string{"1", "2", "3"},
			wantPlaceholders: "?,?,?",
			wantArgs:         []interface{}{"1", "2", "3"},
		},
		{
			name:             "empty slice",
			ids:              []string{},
			wantPlaceholders: "",
			wantArgs:         []interface{}{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotPlaceholders, gotArgs := buildSQLInClause(tt.ids)

			if gotPlaceholders != tt.wantPlaceholders {
				t.Errorf("placeholders = %q, want %q", gotPlaceholders, tt.wantPlaceholders)
			}

			if len(gotArgs) != len(tt.wantArgs) {
				t.Fatalf("args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}

			for i := range gotArgs {
				if gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("args[%d] = %v, want %v", i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestGetReadyWorkInTx_PropagatesDeferredParentChildError(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	childErr := errors.New("dolt transient dependency read failure")
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnError(childErr)

	_, err := GetReadyWorkInTx(
		context.Background(),
		tx,
		types.WorkFilter{},
	)
	if err == nil {
		t.Fatal("expected deferred parent child error")
	}
	if !errors.Is(err, childErr) {
		t.Fatalf("expected wrapped deferred parent child error, got %v", err)
	}
	if !strings.Contains(err.Error(), "compute deferred parent children") {
		t.Fatalf("expected deferred parent child context, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestLoadStatusByIDInTxPrefersWispOnCollision(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery("SELECT id, status FROM issues").
		WithArgs("dup-id").
		WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow("dup-id", types.StatusOpen))
	mock.ExpectQuery("SELECT id, status FROM wisps").
		WithArgs("dup-id").
		WillReturnRows(sqlmock.NewRows([]string{"id", "status"}).AddRow("dup-id", types.StatusClosed))

	got, err := loadStatusByIDInTx(context.Background(), tx, []string{"dup-id"})
	if err != nil {
		t.Fatalf("loadStatusByIDInTx error = %v, want no error on cross-table dup", err)
	}
	if got["dup-id"] != types.StatusClosed {
		t.Errorf("status = %v, want %v (wisp canonical preferred over issues)", got["dup-id"], types.StatusClosed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestMergeReadyWispsPrefersWispOnCollision(t *testing.T) {
	t.Parallel()

	issuesCopy := &types.Issue{ID: "dup-id", Status: types.StatusOpen, Title: "issues copy"}
	wispCopy := &types.Issue{ID: "dup-id", Status: types.StatusClosed, Title: "wisp canonical"}

	got := mergeReadyWisps(
		[]*types.Issue{issuesCopy},
		[]*types.Issue{wispCopy},
		types.WorkFilter{},
	)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (deduped)", len(got))
	}
	if got[0].Title != "wisp canonical" {
		t.Errorf("title = %q, want %q (wisp preferred over issues copy)", got[0].Title, "wisp canonical")
	}
}

func TestGetChildrenOfDeferredParentsInTx_ReturnsChildrenFromBothDependencyTables(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "issues")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-dependencies-issues"))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-dependencies-wisps"))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "issues")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-wisp-dependencies-issues"))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-wisp-dependencies-wisps"))

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx: %v", err)
	}
	want := []string{
		"child-from-dependencies-issues",
		"child-from-dependencies-wisps",
		"child-from-wisp-dependencies-issues",
		"child-from-wisp-dependencies-wisps",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetChildrenOfDeferredParentsInTx_NoDeferredParentsExitsAfterProbe(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	// SELECT (EXISTS ...) always answers one row; no deferred parent is a
	// false value, which must not enter the child scan.
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnRows(deferredProbeAnswer(false))
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnRows(deferredProbeAnswer(false))

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("children = %v, want empty", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestGetChildrenOfDeferredParentsInTx_NoRowsProbeStillSkipsTheScan keeps the
// old ErrNoRows answer meaning "no deferred parent" for a driver that answers
// that way.
func TestGetChildrenOfDeferredParentsInTx_NoRowsProbeStillSkipsTheScan(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnError(sql.ErrNoRows)

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("children = %v, want empty", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestGetChildrenOfDeferredParentsInTx_FalseIssuesProbeFallsThroughToWisps: a
// false answer for issues moves on to wisps, and only a true answer there
// starts the child scan.
func TestGetChildrenOfDeferredParentsInTx_FalseIssuesProbeFallsThroughToWisps(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnRows(deferredProbeAnswer(false))
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnRows(deferredProbeAnswer(true))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "issues")).WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-of-wisp-parent"))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "issues")).WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "wisps")).WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx: %v", err)
	}
	if want := []string{"child-of-wisp-parent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestGetChildrenOfDeferredParentsInTx_MissingWispsProbeIsTolerated: a
// database without the wisp plane answers the issues probe false and fails the
// wisps one with a missing table, which means no deferred parent.
func TestGetChildrenOfDeferredParentsInTx_MissingWispsProbeIsTolerated(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnRows(deferredProbeAnswer(false))
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnError(tableNotFound("wisps"))

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("children = %v, want empty", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestProbeReadyWorkInTx_DeferredFactsMatchStatusOrFutureDate pins the
// one-statement probe: each table's deferred fact asks both branches of the
// rule, and a true fact runs the child scan whose four legs each match a
// parent by status or by future date (GH#7300).
func TestProbeReadyWorkInTx_DeferredFactsMatchStatusOrFutureDate(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(`^SELECT EXISTS \(SELECT 1 FROM wisps\), EXISTS \(SELECT 1 FROM wisp_dependencies\), ` +
		`EXISTS \(.+\), EXISTS \(.+\), ` +
		deferredParentExistsRegex("issues") + `, ` + deferredParentExistsRegex("wisps") + `$`).
		WillReturnRows(sqlmock.NewRows([]string{"wp", "wd", "is", "ws", "id", "wdf"}).
			AddRow(true, true, false, false, true, false))
	legs := make([]string, 0, 4)
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		for _, issueTable := range []string{"issues", "wisps"} {
			legs = append(legs, deferredChildrenQueryRegex(depTable, issueTable))
		}
	}
	mock.ExpectQuery(strings.Join(legs, `\s+UNION ALL\s+`)).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-of-undated-parent"))

	p, err := probeReadyWorkInTx(context.Background(), tx, types.WorkFilter{}, false)
	if err != nil {
		t.Fatalf("probeReadyWorkInTx: %v", err)
	}
	if want := []string{"child-of-undated-parent"}; !reflect.DeepEqual(p.inputs.DeferredChildIDs, want) {
		t.Fatalf("DeferredChildIDs = %v, want %v", p.inputs.DeferredChildIDs, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetChildrenOfDeferredParentsInTx_IgnoresMissingWispDependenciesTable(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "issues")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-dependencies-issues"))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-from-dependencies-wisps"))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "issues")).
		WillReturnError(errors.New("table wisp_dependencies does not exist"))

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx: %v", err)
	}
	want := []string{"child-from-dependencies-issues", "child-from-dependencies-wisps"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetReadyWorkInTxOpenIncludesCustomActive(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(staleBlockedQueryRegex("issues")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT id FROM issues\s+WHERE \(status = \? OR status IN \(SELECT name FROM custom_statuses WHERE category = 'active'\)\)`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)

	got, err := GetReadyWorkInTx(
		context.Background(),
		tx,
		types.WorkFilter{Status: types.StatusOpen},
	)
	if err != nil {
		t.Fatalf("GetReadyWorkInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no rows, got %d", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestGetReadyWorkInTxStatusesSingleQuery(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(staleBlockedQueryRegex("issues")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT id FROM issues\s+WHERE status IN \(\?,\?\)`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)

	got, err := GetReadyWorkInTx(
		context.Background(),
		tx,
		types.WorkFilter{Statuses: []types.Status{"open", "blocked"}},
	)
	if err != nil {
		t.Fatalf("GetReadyWorkInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no rows, got %d", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReadyWorkWispIssueFilterPropagatesStatuses(t *testing.T) {
	t.Parallel()

	filter := readyWorkWispIssueFilter(types.WorkFilter{Statuses: []types.Status{"open", "blocked"}})
	if filter.Status != nil {
		t.Fatalf("wisp filter Status = %v, want nil", *filter.Status)
	}
	want := []types.Status{"open", "blocked"}
	if !reflect.DeepEqual(filter.Statuses, want) {
		t.Fatalf("wisp filter Statuses = %v, want %v", filter.Statuses, want)
	}
}

func TestGetChildrenOfDeferredParentsInTx_UndatedDeferredParentHidesChildren(t *testing.T) {
	t.Parallel()

	// An undated deferred parent (status = 'deferred', defer_until NULL) must
	// pass the probe and have its children excluded from ready work.
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnRows(deferredProbeAnswer(true))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "issues")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-of-undated-deferred"))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "issues")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}))

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx: %v", err)
	}
	if want := []string{"child-of-undated-deferred"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestProbeReadyWorkInTx_UndatedDeferredParentHidesChildren(t *testing.T) {
	t.Parallel()

	// The one-statement probe is the path GetReadyWork, the counts readers and
	// CountReadyWork all take. Its deferred-parent facts must be true for a
	// status = 'deferred' row with no defer_until, and a true fact must send the
	// child scan out as one UNION ALL statement whose IDs become the exclusions.
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(readyProbeSQL + `, EXISTS \(.+\), EXISTS \(.+\), ` +
		deferredParentExistsRegex("issues") + `, ` + deferredParentExistsRegex("wisps") + `$`).
		WillReturnRows(readyProbeRows(true, true, false, false, true, false))
	var legs []string
	for _, depTable := range deferredChildDepTables {
		for _, issueTable := range deferredChildIssueTables {
			legs = append(legs, deferredChildrenQueryRegex(depTable, issueTable))
		}
	}
	mock.ExpectQuery(strings.Join(legs, `\s+UNION ALL\s+`)).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id"}).AddRow("child-of-undated-deferred"))

	p, err := probeReadyWorkInTx(context.Background(), tx, types.WorkFilter{}, false)
	if err != nil {
		t.Fatalf("probeReadyWorkInTx: %v", err)
	}
	if want := []string{"child-of-undated-deferred"}; !reflect.DeepEqual(p.inputs.DeferredChildIDs, want) {
		t.Fatalf("DeferredChildIDs = %v, want %v", p.inputs.DeferredChildIDs, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
