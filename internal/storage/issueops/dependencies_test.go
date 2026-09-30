package issueops

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestCheckDependencyCycleInTxSelfDependencyIsWrapPreserving proves the
// self-dependency guard now returns a typed sentinel (errors.Is-able) while its
// user-facing message text stays byte-identical to the pre-taxonomy string.
func TestCheckDependencyCycleInTxSelfDependencyIsWrapPreserving(t *testing.T) {
	t.Parallel()

	_, _, tx := beginMockTx(t)
	dep := &types.Dependency{IssueID: "dep-a", DependsOnID: "dep-a", Type: types.DepBlocks}

	err := CheckDependencyCycleInTx(context.Background(), tx, dep, nil)
	if err == nil {
		t.Fatal("CheckDependencyCycleInTx(self-dep) = nil, want error")
	}
	if !errors.Is(err, domain.ErrSelfDependency) {
		t.Errorf("errors.Is(err, domain.ErrSelfDependency) = false, want true; err = %v", err)
	}
	// Byte-identical to the pre-taxonomy message: the sentinel is the static
	// prefix rendered by %w, the rest is unchanged.
	const want = "cannot add self-dependency: dep-a cannot depend on itself"
	if err.Error() != want {
		t.Errorf("message = %q, want byte-identical %q", err.Error(), want)
	}
}

// TestCheckDependencyCycleInTxCycleIsWrapPreserving proves the cycle guard now
// returns a typed sentinel while its user-facing message text stays
// byte-identical to the pre-taxonomy string.
func TestCheckDependencyCycleInTxCycleIsWrapPreserving(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	dep := &types.Dependency{IssueID: "dep-a", DependsOnID: "dep-b", Type: types.DepBlocks}

	// WouldCreateSchedulingCycleInTx queries reachability with (dependsOnID, issueID).
	mock.ExpectQuery("WITH RECURSIVE reachable").
		WithArgs("dep-b", "dep-a").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	err := CheckDependencyCycleInTx(context.Background(), tx, dep, nil)
	if err == nil {
		t.Fatal("CheckDependencyCycleInTx(cycle) = nil, want error")
	}
	if !errors.Is(err, domain.ErrDependencyCycle) {
		t.Errorf("errors.Is(err, domain.ErrDependencyCycle) = false, want true; err = %v", err)
	}
	// Byte-identical to the pre-taxonomy message (the bare sentinel text).
	const want = "adding dependency would create a cycle"
	if err.Error() != want {
		t.Errorf("message = %q, want byte-identical %q", err.Error(), want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestReplaceDependencyTargetNormalizesTargetColumns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		targetCol    string
		rowIssue     any
		rowWisp      any
		wantIssue    any
		wantWisp     any
		wantExternal any
	}{
		{
			name:         "issue target clears stale wisp target",
			targetCol:    "depends_on_issue_id",
			rowIssue:     nil,
			rowWisp:      "old-target",
			wantIssue:    "new-target",
			wantWisp:     nil,
			wantExternal: nil,
		},
		{
			name:         "wisp target clears stale issue target",
			targetCol:    "depends_on_wisp_id",
			rowIssue:     "old-target",
			rowWisp:      nil,
			wantIssue:    nil,
			wantWisp:     "new-target",
			wantExternal: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer db.Close()

			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM dependencies a")).
				WithArgs("new-target", "new-target", "new-target").
				WillReturnRows(sqlmock.NewRows([]string{"found"}))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id")).
				WithArgs("old-target", "old-target").
				WillReturnRows(sqlmock.NewRows([]string{
					"issue_id",
					"depends_on_issue_id",
					"depends_on_wisp_id",
					"depends_on_external",
					"type",
					"created_at",
					"created_by",
					"metadata",
					"thread_id",
				}).AddRow("source", tt.rowIssue, tt.rowWisp, nil, "blocks", nil, "tester", "{}", "thread-1"))
			mock.ExpectExec(regexp.QuoteMeta("DELETE FROM dependencies")).
				WithArgs("old-target", "old-target").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id)")).
				WithArgs(depid.New("source", "new-target"), "source", tt.wantIssue, tt.wantWisp, tt.wantExternal, "blocks", nil, "tester", "{}", "thread-1").
				WillReturnResult(sqlmock.NewResult(0, 1))
			// The post-cascade sweep: rows whose typed column already carries
			// newID because fk_dep_*_target's ON UPDATE CASCADE beat the rewrite
			// here get their stale id re-derived. Nothing stale in this fixture.
			mock.ExpectQuery(regexp.QuoteMeta("SELECT id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external")).
				WithArgs("new-target").
				WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}))
			mock.ExpectCommit()

			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			if err := replaceDependencyTargetInTx(context.Background(), tx, "dependencies", tt.targetCol, "old-target", "new-target"); err != nil {
				_ = tx.Rollback()
				t.Fatalf("replaceDependencyTargetInTx: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

func TestCycleDetectionTablesUseBothTablesByDefault(t *testing.T) {
	got := cycleDetectionTables()
	want := []string{"dependencies", "wisp_dependencies"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCycleReachabilityQuerySingleTableJoinsDirectly(t *testing.T) {
	query := cycleReachabilityQuery([]string{"wisp_dependencies"})
	if !strings.Contains(query, "JOIN wisp_dependencies d ON d.issue_id = r.node") {
		t.Fatalf("query does not join wisp_dependencies directly:\n%s", query)
	}
	if strings.Contains(query, "JOIN (SELECT") {
		t.Fatalf("single-table cycle query should not materialize a derived dependency table:\n%s", query)
	}
	if !strings.Contains(query, "d.type IN ('blocks', 'conditional-blocks', 'parent-child')") {
		t.Fatalf("query does not filter scheduling-relevant dependency types at the direct join:\n%s", query)
	}
	if strings.Contains(query, "UNION ALL") || strings.Contains(query, "depth") {
		t.Fatalf("cycle query should traverse unique nodes, not enumerate paths:\n%s", query)
	}
}

func TestCycleReachabilityQueryMultipleTablesTraversesUniqueNodes(t *testing.T) {
	query := cycleReachabilityQuery([]string{"dependencies", "wisp_dependencies"})
	if strings.Contains(query, "UNION ALL") || strings.Contains(query, "depth") {
		t.Fatalf("multi-table cycle query should traverse unique nodes, not enumerate paths:\n%s", query)
	}
	if !strings.Contains(query, "FROM dependencies") {
		t.Fatalf("query does not include dependencies table:\n%s", query)
	}
	if !strings.Contains(query, "FROM wisp_dependencies") {
		t.Fatalf("query does not include wisp_dependencies table:\n%s", query)
	}
	if !strings.Contains(query, DepTargetExpr) {
		t.Fatalf("query does not resolve depends_on_id via DepTargetExpr:\n%s", query)
	}
}

// TestReplaceDependencyTargetRekeysCascadedRows pins the half of a rename the
// rewrite above cannot see. fk_dep_issue_target carries ON UPDATE CASCADE and
// updateIssueIDInTx renames the issues row first, so depends_on_issue_id already
// reads newID by the time replaceDependencyTargetInTx runs: its
// `WHERE ... = oldID` matches nothing and the row keeps id = depid(issue, oldID).
// That stale primary key re-forks across clones (#4259) and, once a later rename
// hands oldID to another issue, becomes the migration-time re-key chain of
// gastownhall/beads#5268.
func TestReplaceDependencyTargetRekeysCascadedRows(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM dependencies a")).
		WithArgs("new-target", "new-target", "new-target").
		WillReturnRows(sqlmock.NewRows([]string{"found"}))
	// The cascade already moved the row, so the oldID rewrite finds nothing.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external, type, created_at, created_by, metadata, thread_id")).
		WithArgs("old-target", "old-target").
		WillReturnRows(sqlmock.NewRows([]string{
			"issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external",
			"type", "created_at", "created_by", "metadata", "thread_id",
		}))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM dependencies")).
		WithArgs("old-target", "old-target").
		WillReturnResult(sqlmock.NewResult(0, 0))
	// The sweep finds the cascaded row: right target, id still derived from the
	// pre-rename name.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external")).
		WithArgs("new-target").
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "depends_on_issue_id", "depends_on_wisp_id", "depends_on_external"}).
			AddRow(depid.New("source", "old-target"), "source", "new-target", nil, nil).
			// An already-correct sibling must not be touched.
			AddRow(depid.New("other", "new-target"), "other", "new-target", nil, nil))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE dependencies SET id = ? WHERE id = ?")).
		WithArgs(depid.New("source", "new-target"), depid.New("source", "old-target")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := replaceDependencyTargetInTx(context.Background(), tx, "dependencies", "depends_on_issue_id", "old-target", "new-target"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("replaceDependencyTargetInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestDependencyMetadataEqualComparesValuesNotBytes pins the change-free
// re-add gate's comparison rule. The gate decides between "nothing happened"
// and "a real mutation of the source issue" by comparing metadata the CALLER
// supplied against metadata read back out of a native JSON column, and a
// native JSON column is free to re-canonicalize what it persists — Dolt's
// inserts a space after ':', sorts object keys, and narrows a bare integral
// float to an int. A byte compare therefore reports a change on values that
// never changed. Each same-value row here is a spelling difference a byte
// compare fails and a JCS (RFC 8785) compare survives (#6650).
func TestDependencyMetadataEqualComparesValuesNotBytes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "IdenticalBytes", a: `{"gate":"any-children"}`, b: `{"gate":"any-children"}`, want: true},
		{name: "EmptyObjects", a: `{}`, b: `{}`, want: true},
		{name: "StorageInsertedSpacing", a: `{"gate":"any-children"}`, b: `{"gate": "any-children"}`, want: true},
		{name: "StorageSortedKeys", a: `{"spawner_id":"src-1","gate":"any-children"}`, b: `{"gate":"any-children","spawner_id":"src-1"}`, want: true},
		{name: "StorageNarrowedIntegralFloat", a: `{"weight":1.0}`, b: `{"weight":1}`, want: true},
		{name: "DifferentValue", a: `{"note":"v1"}`, b: `{"note":"v2"}`, want: false},
		{name: "DifferentKey", a: `{"gate":"any-children"}`, b: `{"mode":"any-children"}`, want: false},
		{name: "EmptyVersusPopulated", a: `{}`, b: `{"gate":"any-children"}`, want: false},
		// Neither side parses, so the rule falls back to the byte compare it
		// replaced rather than calling two unparseable strings equal.
		{name: "UnparseableFallsBackToBytesEqual", a: `not json`, b: `not json`, want: true},
		{name: "UnparseableFallsBackToBytesUnequal", a: `not json`, b: `also not json`, want: false},
		{name: "OneSideUnparseableFallsBackToBytes", a: `{"gate":"any-children"}`, b: `{"gate":`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DependencyMetadataEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("DependencyMetadataEqual(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// The rule is symmetric: which side came from the column and
			// which from the caller must not change the answer.
			if got := DependencyMetadataEqual(tc.b, tc.a); got != tc.want {
				t.Errorf("DependencyMetadataEqual(%q, %q) = %v, want %v (asymmetric)", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// TestAddDependencyInTxReadsNullStoredMetadataAsEmptyObject pins the widened
// SELECT's handling of a SQL-NULL metadata column. dependencies.metadata is
// nullable (`JSON DEFAULT (JSON_OBJECT())`, no NOT NULL), so a row written out
// of band — bd sql, an external tool, hand SQL — can hold NULL. Before the
// re-add gate read metadata at all this path scanned only `type` and was
// idempotently happy; scanning NULL into a plain string would hard-fail it
// with "converting NULL to string is unsupported", turning every subsequent
// re-add of that edge into an error. NULL is the absent-metadata state, so it
// reads as `{}` and the change-free re-add of a metadata-free edge stays the
// no-op it was.
func TestAddDependencyInTxReadsNullStoredMetadataAsEmptyObject(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_type FROM issues WHERE id = ?")).
		WithArgs("dep-a").
		WillReturnRows(sqlmock.NewRows([]string{"issue_type"}).AddRow("task"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT type, metadata FROM dependencies")).
		WithArgs("dep-a", "dep-b").
		WillReturnRows(sqlmock.NewRows([]string{"type", "metadata"}).AddRow(string(types.DepRelated), nil))

	kind := DepTargetIssue
	dep := &types.Dependency{IssueID: "dep-a", DependsOnID: "dep-b", Type: types.DepRelated}
	created, err := AddDependencyInTx(context.Background(), tx, dep, "writer", AddDependencyOpts{
		SourceTable:      "issues",
		TargetTable:      "issues",
		WriteTable:       "dependencies",
		SkipCycleCheck:   true,
		TargetKind:       &kind,
		PrecheckedTarget: &DepTargetPrecheck{IssueType: "task"},
	})
	if err != nil {
		t.Fatalf("AddDependencyInTx(existing edge with NULL metadata) = %v, want nil: a change-free re-add is a no-op, not an error", err)
	}
	if created {
		t.Error("AddDependencyInTx reported the edge as created; the edge already existed with the requested type")
	}
	// No UPDATE and no journal write are expected: scripting only the two
	// reads means any write this path attempted would fail the run.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
