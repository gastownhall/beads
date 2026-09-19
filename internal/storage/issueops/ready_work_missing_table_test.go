package issueops

import (
	"context"
	"errors"
	"reflect"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func noChildren() *sqlmock.Rows { return sqlmock.NewRows([]string{"issue_id"}) }

func childRows(ids ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"issue_id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	return rows
}

func deferredParentFound() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"1"}).AddRow(1)
}

// TestGetChildrenOfDeferredParentsInTx_MissingIssuesIsNotExcusedByWispDependencies
// is the ready-work twin of #5942's counts guards, on the one join this
// package walks whose FROM names two tables at once:
//
//	FROM <dependencies|wisp_dependencies> dep
//	JOIN <issues|wisps> parent ON parent.id = dep.<target>
//
// The tolerance is written for the wisp plane, the two tables a database may
// legitimately not have. Keyed on the error class alone it also fires on the
// leg's other table, so a missing `issues` -- a table no beads database is
// allowed to be without -- surfacing on a wisp_dependencies leg was read as
// "this database has no wisp_dependencies", which breaks the inner loop and
// so skips the one leg left in the walk, behind a nil error.
//
// On a statically corrupt schema neither arm is ever in range, so what this
// pins is the gate's contract rather than a reachable product bug: a missing
// `issues` fails at the `:530` probe before the walk starts, and a missing
// `dependencies` fails on leg 1 (dependencies/issues). Both confirmed by
// executing them against embedded Dolt -- identical errors on base and on the
// fix. The domain/db twin has no such shield.
//
// The assertion is errors.Is against the primed driver error rather than a
// substring of the message: the query text names both joined tables, so a
// substring check would pass on any error that merely echoes the query.
func TestGetChildrenOfDeferredParentsInTx_MissingIssuesIsNotExcusedByWispDependencies(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	gone := tableNotFound("issues")
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnRows(deferredParentFound())
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "issues")).WillReturnRows(noChildren())
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "wisps")).WillReturnRows(noChildren())
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "issues")).WillReturnError(gone)

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err == nil {
		t.Fatalf("a missing issues table was excused as an absent wisp plane, answering %v", got)
	}
	if !errors.Is(err, gone) {
		t.Fatalf("error is not the missing-issues failure: %v", err)
	}
}

// TestGetChildrenOfDeferredParentsInTx_MissingDependenciesIsNotExcusedByWisps
// is the same defect reached through the other half of the gate. Here the leg
// is dependencies/wisps, so the wisps tolerance is the one in range, and a
// missing `dependencies` was skipped as an absent wisp plane -- the run then
// walked the remaining legs and returned whatever they held, again behind a
// nil error.
func TestGetChildrenOfDeferredParentsInTx_MissingDependenciesIsNotExcusedByWisps(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	gone := tableNotFound("dependencies")
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnRows(deferredParentFound())
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "issues")).WillReturnRows(noChildren())
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "wisps")).WillReturnError(gone)
	// Only the untightened path reaches these. Priming them keeps the old
	// behaviour a clean nil-error answer rather than an unexpected-query
	// failure, which would read like a pass for the wrong reason.
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "issues")).WillReturnRows(noChildren())
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "wisps")).WillReturnRows(noChildren())

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err == nil {
		t.Fatalf("a missing dependencies table was excused as an absent wisp plane, answering %v", got)
	}
	if !errors.Is(err, gone) {
		t.Fatalf("error is not the missing-dependencies failure: %v", err)
	}
}

// TestGetChildrenOfDeferredParentsInTx_IgnoresMissingWispsTable is the control
// the narrowed gate must keep green: `wisps` is one of the two tables the
// tolerance was written for, and a database that never migrated the wisp plane
// still has deferred children on the surviving legs -- here dependencies/issues
// and wisp_dependencies/issues, one durable and one mixed. Over-tightening to
// "never tolerate" passes both assertions above and fails here.
func TestGetChildrenOfDeferredParentsInTx_IgnoresMissingWispsTable(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnRows(deferredParentFound())
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "issues")).
		WillReturnRows(childRows("child-from-dependencies-issues"))
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "wisps")).
		WillReturnError(tableNotFound("wisps"))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "issues")).
		WillReturnRows(childRows("child-from-wisp-dependencies-issues"))
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "wisps")).
		WillReturnError(tableNotFound("wisps"))

	got, err := getChildrenOfDeferredParentsInTx(context.Background(), tx)
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParentsInTx errored on a database with no wisps table: %v", err)
	}
	want := []string{"child-from-dependencies-issues", "child-from-wisp-dependencies-issues"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestGetChildrenOfDeferredParentsInTx_UnrelatedErrorPropagates is the second
// control: the tolerance must not become a blanket catch in either direction.
// A dropped connection was never a missing table and must still reach the
// caller.
func TestGetChildrenOfDeferredParentsInTx_UnrelatedErrorPropagates(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	boom := errors.New("connection refused")
	mock.ExpectQuery(deferredParentProbeRegex("issues")).WillReturnRows(deferredParentFound())
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "issues")).WillReturnRows(noChildren())
	mock.ExpectQuery(deferredChildrenQueryRegex("dependencies", "wisps")).WillReturnRows(noChildren())
	mock.ExpectQuery(deferredChildrenQueryRegex("wisp_dependencies", "issues")).WillReturnError(boom)

	if _, err := getChildrenOfDeferredParentsInTx(context.Background(), tx); !errors.Is(err, boom) {
		t.Fatalf("a failed deferred-parent join did not propagate: %v", err)
	}
}
