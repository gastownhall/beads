package db

import (
	"errors"
	"reflect"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

func deferredParentProbeRegex(issueTable string) string {
	return `^SELECT ` + regexp.QuoteMeta(sqlbuild.DeferredParentExistsSQL(issueTable)) + `$`
}

func deferredEdgeRegex(e deferredParentEdge) string {
	return `SELECT dep\.issue_id\s+FROM ` + e.depTable + ` dep\s+JOIN ` + e.issueTable +
		` parent ON parent\.id = dep\.` + e.targetCol +
		`\s+WHERE dep\.type = 'parent-child'\s+AND ` + regexp.QuoteMeta(sqlbuild.DeferredParentPredicate("parent"))
}

func noDeferredChildren() *sqlmock.Rows { return sqlmock.NewRows([]string{"issue_id"}) }

// TestDeferredParentEdgesBrokenDurablePlaneIsAnError is the domain/db twin of
// the issueops guard on the same join. `childrenOfDeferredParents`
// walks four (dependency table, issue table) pairs, and three of the four name
// a table every beads database must have. Its gate classified the error class
// alone with no plane guard at all, and it continues rather than aborting, so
// the edges naming a missing `dependencies` or `issues` were swallowed one by
// one while the remaining two answered: a nil error over an incomplete set of
// deferred children. For a missing `dependencies` that reaches the user --
// ready work hands back the child of a deferred parent, pinned end-to-end in
// embeddeddolt/ready_work_missing_table_test.go. For a missing `issues` the
// walk swallows it the same way, but the ready-work union reads `issues`
// downstream and fails there anyway, so nothing is handed back on that route.
//
// Each case below scripts the surviving edges empty, so the answer here is no
// children at all -- that is this test's setup, not the shape of the defect.
//
// The two stacks must agree on what a missing table means, or the same query
// answers differently depending on which one served it.
//
// The assertion is errors.Is against the primed driver error, not a substring
// of the message: the query text names both joined tables, so a substring
// check would pass on any error that merely echoes the query.
func TestDeferredParentEdgesBrokenDurablePlaneIsAnError(t *testing.T) {
	for _, missing := range []string{"dependencies", "issues"} {
		t.Run(missing, func(t *testing.T) {
			mock, repo := newMockRepo(t)
			gone := missingTable(missing)
			mock.ExpectQuery(deferredParentProbeRegex("issues")).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			for _, e := range deferredParentEdges {
				if e.depTable == missing || e.issueTable == missing {
					mock.ExpectQuery(deferredEdgeRegex(e)).WillReturnError(gone)
					continue
				}
				mock.ExpectQuery(deferredEdgeRegex(e)).WillReturnRows(noDeferredChildren())
			}

			got, err := repo.getChildrenOfDeferredParents(t.Context())
			if err == nil {
				t.Fatalf("a missing %s table was excused as an absent wisp plane, answering %v", missing, got)
			}
			if !errors.Is(err, gone) {
				t.Fatalf("error is not the missing-%s failure: %v", missing, err)
			}
		})
	}
}

// TestDeferredParentEdgesMissingWispPlaneIsNotAnError is the control the
// narrowed gate must keep green: `wisps` and `wisp_dependencies` are the two
// tables a database may legitimately not have, and the surviving edges still
// carry deferred children. Over-tightening to "never tolerate" passes the
// assertions above and fails here.
func TestDeferredParentEdgesMissingWispPlaneIsNotAnError(t *testing.T) {
	for _, missing := range []string{"wisps", "wisp_dependencies"} {
		t.Run(missing, func(t *testing.T) {
			mock, repo := newMockRepo(t)
			mock.ExpectQuery(deferredParentProbeRegex("issues")).
				WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
			for _, e := range deferredParentEdges {
				if e.depTable == missing || e.issueTable == missing {
					mock.ExpectQuery(deferredEdgeRegex(e)).WillReturnError(missingTable(missing))
					continue
				}
				mock.ExpectQuery(deferredEdgeRegex(e)).WillReturnRows(
					sqlmock.NewRows([]string{"issue_id"}).AddRow(e.depTable + "/" + e.issueTable))
			}

			got, err := repo.getChildrenOfDeferredParents(t.Context())
			if err != nil {
				t.Fatalf("deferred-parent walk errored on a database with no %s: %v", missing, err)
			}
			var want []string
			for _, e := range deferredParentEdges {
				if e.depTable != missing && e.issueTable != missing {
					want = append(want, e.depTable+"/"+e.issueTable)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("children = %v, want %v", got, want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// TestDeferredParentEdgesUnrelatedErrorPropagates is the second control: the
// tolerance must not be a blanket catch in either direction.
func TestDeferredParentEdgesUnrelatedErrorPropagates(t *testing.T) {
	mock, repo := newMockRepo(t)
	boom := errors.New("connection refused")
	mock.ExpectQuery(deferredParentProbeRegex("issues")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(deferredEdgeRegex(deferredParentEdges[0])).WillReturnError(boom)

	if _, err := repo.getChildrenOfDeferredParents(t.Context()); !errors.Is(err, boom) {
		t.Fatalf("a failed deferred-parent edge did not propagate: %v", err)
	}
}

// TestDeferredParentGateFalseIssuesNoChildScans: the gate is a SELECT of an
// EXISTS expression, which always returns one row. A false value in both
// tables must answer "no deferred parent" without walking the four edges; read
// as "a row came back", it would scan them on every ready read.
func TestDeferredParentGateFalseIssuesNoChildScans(t *testing.T) {
	mock, repo := newMockRepo(t)
	for _, table := range []string{"issues", "wisps"} {
		mock.ExpectQuery(deferredParentProbeRegex(table)).
			WillReturnRows(sqlmock.NewRows([]string{"deferred"}).AddRow(false))
	}

	got, err := repo.getChildrenOfDeferredParents(t.Context())
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParents: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("children = %v, want none", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestDeferredParentGateWispTableOpensTheGate: a deferred parent only in wisps
// still sends out the edge walk.
func TestDeferredParentGateWispTableOpensTheGate(t *testing.T) {
	mock, repo := newMockRepo(t)
	mock.ExpectQuery(deferredParentProbeRegex("issues")).
		WillReturnRows(sqlmock.NewRows([]string{"deferred"}).AddRow(false))
	mock.ExpectQuery(deferredParentProbeRegex("wisps")).
		WillReturnRows(sqlmock.NewRows([]string{"deferred"}).AddRow(true))
	for _, e := range deferredParentEdges {
		rows := noDeferredChildren()
		if e.depTable == "wisp_dependencies" && e.issueTable == "wisps" {
			rows.AddRow("wisp-child")
		}
		mock.ExpectQuery(deferredEdgeRegex(e)).WillReturnRows(rows)
	}

	got, err := repo.getChildrenOfDeferredParents(t.Context())
	if err != nil {
		t.Fatalf("getChildrenOfDeferredParents: %v", err)
	}
	if want := []string{"wisp-child"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
