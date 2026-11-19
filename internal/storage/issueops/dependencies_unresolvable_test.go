package issueops

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// unresolvableRecordsQueryRegex is the stored-record read the combined
// outgoing reader runs for the far ends the hydration missed.
func unresolvableRecordsQueryRegex(table string) string {
	return `(?s)` + dependencyProjectionRegex + regexp.QuoteMeta(table) +
		` WHERE issue_id = \? AND ` + regexp.QuoteMeta(DepTargetExpr) + ` IN \(`
}

// expectOutgoingEdgesAndHydration queues the reads every outgoing metadata
// reader runs: both edge planes, then one issue batch that answers only the
// local ids.
func expectOutgoingEdgesAndHydration(mock sqlmock.Sqlmock, source string, edges [][2]string, local []string) {
	durable := sqlmock.NewRows([]string{"depends_on_id", "type"})
	for _, e := range edges {
		durable.AddRow(e[0], e[1])
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + DepTargetExpr + " AS depends_on_id, type FROM dependencies WHERE issue_id = ?")).
		WithArgs(source).WillReturnRows(durable)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + DepTargetExpr + " AS depends_on_id, type FROM wisp_dependencies WHERE issue_id = ?")).
		WithArgs(source).WillReturnRows(sqlmock.NewRows([]string{"depends_on_id", "type"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnError(sql.ErrNoRows)
	rows := issueRows()
	for _, id := range local {
		rows.AddRow(issueRowValues(id, "title "+id)...)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id IN (")).
		WillReturnRows(rows)
	if len(local) > 0 {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM labels WHERE issue_id IN (")).
			WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
	}
}

// AC-1 (bd-uschhs, #5850): the combined outgoing reader separates the real
// issues from the exact stored records of the edges whose far end has no row
// here, and the old issue-only reader answers exactly the resolved half.
func TestGetDependenciesWithUnresolvableInTxSplitsResolvedFromStoredRecords(t *testing.T) {
	t.Parallel()

	const (
		source   = "ud-1"
		local    = "ud-2"
		external = "external:liveop:liveop-kmf"
		foreign  = "liveop-abc"
	)
	created := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	edges := [][2]string{{local, "blocks"}, {external, "tracks"}, {foreign, "related"}}

	_, mock, tx := beginMockTx(t)
	expectOutgoingEdgesAndHydration(mock, source, edges, []string{local})
	mock.ExpectQuery(unresolvableRecordsQueryRegex("dependencies")).
		WithArgs(source, external, foreign).
		WillReturnRows(dependencyRows().
			AddRow(source, foreign, "related", created, "alice", `{"why":"x"}`, "").
			AddRow(source, external, "tracks", created, "bob", `{"k":"v"}`, "thr-1"))
	mock.ExpectQuery(unresolvableRecordsQueryRegex("wisp_dependencies")).
		WithArgs(source, external, foreign).
		WillReturnRows(dependencyRows())
	// The old reader on the same fixture.
	expectOutgoingEdgesAndHydration(mock, source, edges, []string{local})

	ctx := context.Background()
	resolved, unresolvable, err := GetDependenciesWithUnresolvableInTx(ctx, tx, source)
	if err != nil {
		t.Fatalf("GetDependenciesWithUnresolvableInTx: %v", err)
	}
	old, err := GetDependenciesWithMetadataInTx(ctx, tx, source)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadataInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}

	if len(resolved) != 1 || resolved[0].ID != local || resolved[0].DependencyType != types.DepBlocks {
		t.Fatalf("resolved = %+v, want the one local blocks target %q", resolved, local)
	}
	if !reflect.DeepEqual(old, resolved) {
		t.Fatalf("old metadata reader = %+v, want exactly the resolved portion %+v", old, resolved)
	}
	want := []*types.Dependency{
		{IssueID: source, DependsOnID: external, Type: "tracks", CreatedAt: created, CreatedBy: "bob", Metadata: `{"k":"v"}`, ThreadID: "thr-1"},
		{IssueID: source, DependsOnID: foreign, Type: "related", CreatedAt: created, CreatedBy: "alice", Metadata: `{"why":"x"}`},
	}
	if !reflect.DeepEqual(unresolvable, want) {
		t.Fatalf("unresolvable = %+v, want the stored records sorted by target %+v", unresolvable, want)
	}
}

// A fully local issue runs no record query (sqlmock refuses an unexpected one).
func TestGetDependenciesWithUnresolvableInTxRunsNoRecordQueryWithoutMisses(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	expectOutgoingEdgesAndHydration(mock, "ud-1", [][2]string{{"ud-2", "blocks"}}, []string{"ud-2"})

	resolved, unresolvable, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, "ud-1")
	if err != nil {
		t.Fatalf("GetDependenciesWithUnresolvableInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	if len(resolved) != 1 || unresolvable != nil {
		t.Fatalf("got resolved=%d unresolvable=%+v, want 1 and nil", len(resolved), unresolvable)
	}
}

// A dropped wisp_dependencies table is tolerated by the record read exactly as
// by the edge read; any other record-query failure is returned, never an
// empty "complete" answer.
func TestGetDependenciesWithUnresolvableInTxRecordQueryErrors(t *testing.T) {
	t.Parallel()

	const target = "external:x:y"
	t.Run("missing wisp table tolerated", func(t *testing.T) {
		t.Parallel()
		_, mock, tx := beginMockTx(t)
		expectOutgoingEdgesAndHydration(mock, "ud-1", [][2]string{{target, "tracks"}}, nil)
		mock.ExpectQuery(unresolvableRecordsQueryRegex("dependencies")).
			WillReturnRows(dependencyRows().AddRow("ud-1", target, "tracks", time.Now(), "a", "{}", ""))
		mock.ExpectQuery(unresolvableRecordsQueryRegex("wisp_dependencies")).
			WillReturnError(errors.New("Error 1146: Table 'db.wisp_dependencies' doesn't exist"))

		_, unresolvable, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, "ud-1")
		if err != nil {
			t.Fatalf("missing wisp_dependencies must be tolerated, got %v", err)
		}
		if len(unresolvable) != 1 || unresolvable[0].DependsOnID != target {
			t.Fatalf("unresolvable = %+v, want the durable record for %q", unresolvable, target)
		}
	})
	t.Run("durable query failure returned", func(t *testing.T) {
		t.Parallel()
		_, mock, tx := beginMockTx(t)
		expectOutgoingEdgesAndHydration(mock, "ud-1", [][2]string{{target, "tracks"}}, nil)
		boom := errors.New("boom")
		mock.ExpectQuery(unresolvableRecordsQueryRegex("dependencies")).WillReturnError(boom)

		resolved, unresolvable, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, "ud-1")
		if !errors.Is(err, boom) || resolved != nil || unresolvable != nil {
			t.Fatalf("got (%v, %v, %v), want (nil, nil, boom)", resolved, unresolvable, err)
		}
	})
}
