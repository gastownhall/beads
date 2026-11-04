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

func deferredParentProbeRegex(issueTable string) string {
	return `SELECT 1 FROM ` + issueTable + `\s+WHERE defer_until IS NOT NULL\s+AND defer_until > UTC_TIMESTAMP\(\)\s+LIMIT 1`
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
	return `SELECT dep\.issue_id\s+FROM ` + depTable + ` dep\s+JOIN ` + issueTable + ` parent ON parent\.id = dep\.` + targetCol + `\s+WHERE dep\.type = 'parent-child'\s+AND parent\.defer_until IS NOT NULL\s+AND parent\.defer_until > UTC_TIMESTAMP\(\)`
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
	mock.ExpectQuery(deferredParentProbeRegex("issues")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))

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

func TestGetReadyWorkInTxOpenParameterizesCustomActive(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(staleBlockedQueryRegex("issues")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT name FROM custom_statuses WHERE category = 'active' ORDER BY name`).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("triaged"))
	mock.ExpectQuery(`SELECT id FROM issues\s+WHERE status IN \(\?,\?\)`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)

	got, err := GetReadyWorkInTx(context.Background(), tx, types.WorkFilter{Status: types.StatusOpen})
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

func TestGetReadyWorkInTxOpenNoCustomActiveSingleSeek(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(staleBlockedQueryRegex("issues")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT name FROM custom_statuses WHERE category = 'active' ORDER BY name`).
		WillReturnRows(sqlmock.NewRows([]string{"name"}))
	mock.ExpectQuery(`SELECT id FROM issues\s+WHERE status = \?`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)

	got, err := GetReadyWorkInTx(context.Background(), tx, types.WorkFilter{Status: types.StatusOpen})
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

const readyActiveStatusProbeSQL = readyProbeSQL + `.*EXISTS \(SELECT name FROM custom_statuses WHERE category = 'active'\)$`

func readyOpenProbeFilter() types.WorkFilter {
	return types.WorkFilter{
		Status: types.StatusOpen, Type: "task", SortPolicy: types.SortPolicyOldest,
		IncludeDeferred: true, IncludeEphemeral: true,
	}
}

func TestReadyCountsHydratesActiveStatusesOnce(t *testing.T) {
	t.Parallel()
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(readyActiveStatusProbeSQL).
		WillReturnRows(readyProbeRows(true, true, false, false, true))
	mock.ExpectQuery(`^SELECT name FROM custom_statuses WHERE category = 'active' ORDER BY name$`).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("review")).RowsWillBeClosed()
	mock.ExpectQuery(`(?s)FROM issues i\s+WHERE status IN \(\?,\?\)`).
		WithArgs("open", "review", "task").WillReturnRows(emptyCountsRows())
	mock.ExpectQuery(`(?s)FROM wisps i\s+WHERE status IN \(\?,\?\)`).
		WithArgs("open", "review", "task").WillReturnRows(emptyCountsRows())
	got, err := GetReadyWorkWithCountsInTx(context.Background(), tx, readyOpenProbeFilter())
	if err != nil {
		t.Fatalf("GetReadyWorkWithCountsInTx: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d rows, want empty SQL-reachability fixture", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReadyCountsTotalResolvedEmptyStatuses(t *testing.T) {
	t.Parallel()
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(readyActiveStatusProbeSQL).
		WillReturnRows(readyProbeRows(true, true, false, false, false, false))
	// No names query: the successful combined probe resolved an empty set.
	mock.ExpectQuery(`^SELECT id, COUNT\(\*\) OVER \(\) FROM issues WHERE status = \?`).
		WithArgs("open", "task").WillReturnRows(sqlmock.NewRows([]string{"id", "total"}))
	mock.ExpectQuery(`^SELECT id, COUNT\(\*\) OVER \(\) FROM wisps WHERE status = \?`).
		WithArgs("open", "task").WillReturnRows(sqlmock.NewRows([]string{"id", "total"}))
	filter := readyOpenProbeFilter()
	filter.Limit = 1
	got, total, err := GetReadyWorkWithCountsAndTotalInTx(context.Background(), tx, filter)
	if err != nil {
		t.Fatalf("GetReadyWorkWithCountsAndTotalInTx: %v", err)
	}
	if len(got) != 0 || total != 0 {
		t.Fatalf("got %d rows/total %d, want empty SQL-reachability fixture", len(got), total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReadyCounterActiveStatusesIncludesOverlap(t *testing.T) {
	t.Parallel()
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(readyActiveStatusProbeSQL).
		WillReturnRows(readyProbeRows(true, true, false, false, true, true))
	mock.ExpectQuery(`^SELECT name FROM custom_statuses WHERE category = 'active' ORDER BY name$`).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("review")).RowsWillBeClosed()
	mock.ExpectQuery(`(?s)^SELECT \(SELECT COUNT\(\*\) FROM issues WHERE status IN \(\?,\?\).*FROM wisps WHERE status IN \(\?,\?\)`).
		WithArgs("open", "review", "task", "open", "review", "task").
		WillReturnRows(sqlmock.NewRows([]string{"issues", "wisps"}).AddRow(2, 2))
	mock.ExpectQuery(`(?s)^SELECT COUNT\(\*\) FROM issues WHERE status IN \(\?,\?\).*AND id IN \(SELECT id FROM wisps WHERE status IN \(\?,\?\)`).
		WithArgs("open", "review", "task", "open", "review", "task").WillReturnRows(scalarCountRows(1))
	got, err := CountReadyWorkInTx(context.Background(), tx, readyOpenProbeFilter())
	if err != nil {
		t.Fatalf("CountReadyWorkInTx: %v", err)
	}
	if got != 3 {
		t.Fatalf("count = %d, want 2 + 2 - 1 overlap", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReadyProbeActiveStatusesAfterOptionalSchemaFallback(t *testing.T) {
	t.Parallel()
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(readyActiveStatusProbeSQL).WillReturnError(tableNotFound("wisps"))
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).WillReturnError(tableNotFound("wisp_dependencies"))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(tableNotFound("wisps"))
	mock.ExpectQuery(staleBlockedQueryRegex("issues")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	// A failed optional-schema probe did not establish that active names are absent.
	mock.ExpectQuery(`^SELECT name FROM custom_statuses WHERE category = 'active' ORDER BY name$`).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("review")).RowsWillBeClosed()
	mock.ExpectQuery(`^SELECT COUNT\(\*\) FROM issues WHERE status IN \(\?,\?\)`).
		WithArgs("open", "review", "task").WillReturnRows(scalarCountRows(4))
	got, err := CountReadyWorkInTx(context.Background(), tx, readyOpenProbeFilter())
	if err != nil {
		t.Fatalf("CountReadyWorkInTx after optional-schema fallback: %v", err)
	}
	if got != 4 {
		t.Fatalf("count = %d, want 4", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReadyProbeNonOpenSkipsCustomStatuses(t *testing.T) {
	t.Parallel()
	_, mock, tx := beginMockTx(t)
	// Collision is the last fact; this query must not depend on custom_statuses.
	mock.ExpectQuery(readyProbeSQL + `.*EXISTS \(SELECT 1 FROM issues JOIN wisps ON wisps.id = issues.id\)$`).
		WillReturnRows(readyProbeRows(false, true, false, false, false))
	mock.ExpectQuery(`^SELECT COUNT\(\*\) FROM issues WHERE status = \?`).
		WithArgs("in_progress", "task").WillReturnRows(scalarCountRows(1))
	filter := readyOpenProbeFilter()
	filter.Status = types.StatusInProgress
	got, err := CountReadyWorkInTx(context.Background(), tx, filter)
	if err != nil {
		t.Fatalf("CountReadyWorkInTx non-open control: %v", err)
	}
	if got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReadyProbeActiveStatusReadFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"query", "iteration", "scan"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			_, mock, tx := beginMockTx(t)
			boom := errors.New("injected active-status read failure")
			mock.ExpectQuery(readyActiveStatusProbeSQL).
				WillReturnRows(readyProbeRows(false, true, false, false, false, true))
			lookup := mock.ExpectQuery(`^SELECT name FROM custom_statuses WHERE category = 'active' ORDER BY name$`)
			switch mode {
			case "query":
				lookup.WillReturnError(boom)
			case "iteration":
				lookup.WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("review").RowError(0, boom)).RowsWillBeClosed()
			case "scan":
				lookup.WillReturnRows(sqlmock.NewRows([]string{"name", "extra"}).AddRow("review", "invalid projection")).RowsWillBeClosed()
			}
			_, err := CountReadyWorkInTx(context.Background(), tx, readyOpenProbeFilter())
			if mode == "scan" {
				if err == nil || !strings.Contains(err.Error(), "Scan") || !strings.Contains(err.Error(), "active custom statuses") {
					t.Fatalf("got %v, want contextual active-status scan failure", err)
				}
			} else if !errors.Is(err, boom) {
				t.Fatalf("got %v, want injected active-status error %v", err, boom)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}
}

func TestReadyProbeRequiredCustomStatusTableMissing(t *testing.T) {
	t.Parallel()
	_, mock, tx := beginMockTx(t)
	gone := tableNotFound("custom_statuses")
	mock.ExpectQuery(readyActiveStatusProbeSQL).WillReturnError(gone)
	// Existing fallback probes establish a healthy but empty wisp plane; they
	// cannot turn the missing required custom-status table into an empty set.
	mock.ExpectQuery(`SELECT 1 FROM wisp_dependencies LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(staleBlockedQueryRegex("issues")).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery(`^SELECT name FROM custom_statuses WHERE category = 'active' ORDER BY name$`).WillReturnError(gone)
	_, err := CountReadyWorkInTx(context.Background(), tx, readyOpenProbeFilter())
	if !errors.Is(err, gone) {
		t.Fatalf("got %v, want required-table failure %v", err, gone)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReadyProbeActiveStatusFactError(t *testing.T) {
	t.Parallel()
	_, mock, tx := beginMockTx(t)
	boom := errors.New("permission denied on ready status fact")
	mock.ExpectQuery(readyActiveStatusProbeSQL).WillReturnError(boom)
	_, err := CountReadyWorkInTx(context.Background(), tx, readyOpenProbeFilter())
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want probe fact failure %v", err, boom)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

type cancelReadyStatusReadTx struct {
	DBTX
	cancel context.CancelFunc
	reads  int
}

func (c *cancelReadyStatusReadTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.HasPrefix(query, "SELECT name FROM custom_statuses ") {
		c.reads++
		c.cancel()
	}
	return c.DBTX.QueryContext(ctx, query, args...)
}

func TestReadyProbeActiveStatusReadCancellation(t *testing.T) {
	t.Parallel()
	_, mock, tx := beginMockTx(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mock.ExpectQuery(readyActiveStatusProbeSQL).
		WillReturnRows(readyProbeRows(false, true, false, false, false, true))
	guarded := &cancelReadyStatusReadTx{DBTX: tx, cancel: cancel}
	_, err := CountReadyWorkInTx(ctx, guarded, readyOpenProbeFilter())
	if !errors.Is(err, context.Canceled) || guarded.reads != 1 {
		t.Fatalf("got error %v and %d status reads, want cancellation at the status lookup", err, guarded.reads)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
