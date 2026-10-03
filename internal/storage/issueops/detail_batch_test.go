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
	publicops "github.com/steveyegge/beads/issueops"
)

type detailBatchRecordingDBTX struct{ statements int }

func (r *detailBatchRecordingDBTX) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	r.statements++
	return nil, errors.New("unexpected exec")
}

func (r *detailBatchRecordingDBTX) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	r.statements++
	return nil, errors.New("unexpected query")
}

func (r *detailBatchRecordingDBTX) QueryRowContext(context.Context, string, ...any) *sql.Row {
	r.statements++
	panic("unexpected query row")
}

func TestDetailBatchValidationRunsNoSQL(t *testing.T) {
	for _, ids := range [][]string{nil, {}, {""}, {" "}, {"\t\n"}, {"bd-a", ""}} {
		request := publicops.DetailBatchRequest{IDs: ids, IncludeDependents: true, IncludeComments: true, BriefDeps: true}
		before := append([]string(nil), ids...)
		tx := &detailBatchRecordingDBTX{}
		out, err := ExecuteDetailBatch(context.Background(), tx, request, DetailBatchEmbedded)
		if len(ids) == 0 {
			if err != nil || out.Items == nil || len(out.Items) != 0 {
				t.Fatalf("empty request = %+v, %v; want non-nil empty items", out, err)
			}
		} else if !errors.Is(err, publicops.ErrValidation) || out.Items != nil {
			t.Fatalf("request %q = %+v, %v; want validation error and no items", ids, out, err)
		}
		if tx.statements != 0 {
			t.Fatalf("request %q ran %d statements", ids, tx.statements)
		}
		if len(ids) > 0 && !reflect.DeepEqual(ids, before) {
			t.Fatalf("request mutated: got %q, want %q", ids, before)
		}
	}
}

func newDetailBatchMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sql expectations: %v", err)
		}
		_ = db.Close()
	})
	return db, mock
}

func detailBatchSubjectQuery(table, placeholders string) string {
	return regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM " + table + " " + sqlbuild.LeaseJoin(table) + " WHERE id IN (" + placeholders + ")")
}

func detailBatchEdgeQuery(table, placeholders string, incoming bool) string {
	if incoming {
		return regexp.QuoteMeta("SELECT " + DepTargetExpr + ", issue_id, type FROM " + table + " WHERE " + DepTargetExpr + " IN (" + placeholders + ") ORDER BY id")
	}
	return regexp.QuoteMeta("SELECT issue_id, " + DepTargetExpr + ", type FROM " + table + " WHERE issue_id IN (" + placeholders + ") ORDER BY issue_id, depends_on_external, id")
}

func TestDetailBatchReadFailuresReturnNoItems(t *testing.T) {
	for _, failure := range []string{"subject", "subject scan", "subject rows", "wisp probe", "edge", "dependent", "out tally", "in tally", "labels", "comment count", "comment"} {
		t.Run(failure, func(t *testing.T) {
			db, mock := newDetailBatchMock(t)
			boom := errors.New("injected read failure")
			subjectRows := issueRows().AddRow(issueRowValues("bd-a", "A")...)
			if failure == "subject scan" {
				subjectRows = sqlmock.NewRows([]string{"id"}).AddRow("bd-a")
			} else if failure == "subject rows" {
				subjectRows.RowError(0, boom)
			}
			steps := []struct {
				name, query string
				rows        *sqlmock.Rows
			}{
				{"subject", detailBatchSubjectQuery("issues", "?"), subjectRows},
				{"wisp probe", regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1"), sqlmock.NewRows([]string{"1"})},
				{"edge", detailBatchEdgeQuery("dependencies", "?", false), sqlmock.NewRows([]string{"anchor", "far", "type"})},
				{"wisp edge", detailBatchEdgeQuery("wisp_dependencies", "?", false), sqlmock.NewRows([]string{"anchor", "far", "type"})},
				{"dependent", detailBatchEdgeQuery("dependencies", "?", true), sqlmock.NewRows([]string{"anchor", "far", "type"})},
				{"wisp dependent", detailBatchEdgeQuery("wisp_dependencies", "?", true), sqlmock.NewRows([]string{"anchor", "far", "type"})},
				{"out tally", `SELECT d.issue_id AS anchor, COUNT\(\*\) AS n FROM dependencies`, sqlmock.NewRows([]string{"anchor", "n"})},
				{"wisp out tally", `SELECT d.issue_id AS anchor, COUNT\(\*\) AS n FROM wisp_dependencies`, sqlmock.NewRows([]string{"anchor", "n"})},
				{"in tally", `SELECT COALESCE\(.*AS anchor, COUNT\(\*\) AS n FROM dependencies`, sqlmock.NewRows([]string{"anchor", "n"})},
				{"wisp in tally", `SELECT COALESCE\(.*AS anchor, COUNT\(\*\) AS n FROM wisp_dependencies`, sqlmock.NewRows([]string{"anchor", "n"})},
				{"labels", `SELECT issue_id, label FROM labels`, sqlmock.NewRows([]string{"issue_id", "label"})},
				{"comment count", `SELECT issue_id, COUNT\(\*\) as cnt FROM comments`, sqlmock.NewRows([]string{"issue_id", "cnt"})},
				{"comment", `SELECT id, issue_id, author, text, created_at FROM comments`, sqlmock.NewRows([]string{"id", "issue_id", "author", "text", "created_at"})},
			}
			for _, step := range steps {
				expect := mock.ExpectQuery(step.query)
				if failure == step.name {
					expect.WillReturnError(boom)
					break
				}
				expect.WillReturnRows(step.rows).RowsWillBeClosed()
				if step.name == "subject" && (failure == "subject scan" || failure == "subject rows") {
					break
				}
			}
			out, err := ExecuteDetailBatch(context.Background(), db, publicops.DetailBatchRequest{
				IDs: []string{"bd-a"}, IncludeDependents: true, IncludeComments: true,
			}, DetailBatchEmbedded)
			if err == nil || out.Items != nil {
				t.Fatalf("%s = %+v, %v; want error and no items", failure, out, err)
			}
			if failure != "subject scan" && !errors.Is(err, boom) {
				t.Fatalf("%s lost injected cause: %v", failure, err)
			}
		})
	}
}

func TestDetailBatchServerDependentFailure(t *testing.T) {
	db, mock := newDetailBatchMock(t)
	mock.ExpectQuery(detailBatchSubjectQuery("issues", "?")).WithArgs("bd-a").
		WillReturnRows(issueRows().AddRow(issueRowValues("bd-a", "A")...))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery(detailBatchEdgeQuery("dependencies", "?", false)).WithArgs("bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"anchor", "far", "type"}))
	boom := errors.New("injected server dependent failure")
	mock.ExpectQuery(`(?s)FROM issues i JOIN dependencies d.*UNION ALL.*FROM wisps w JOIN wisp_dependencies d`).
		WithArgs("bd-a", "bd-a").WillReturnError(boom)
	out, err := ExecuteDetailBatch(context.Background(), db, publicops.DetailBatchRequest{
		IDs: []string{"bd-a"}, IncludeDependents: true,
	}, DetailBatchDirectServer)
	if !errors.Is(err, boom) || out.Items != nil {
		t.Fatalf("server dependent failure = %+v, %v; want injected error and no items", out, err)
	}
}

func TestDetailBatchOrderMissingRepeats(t *testing.T) {
	db, mock := newDetailBatchMock(t)
	ids := []string{"bd-b", "missing", "bd-a", "bd-b"}
	before := append([]string(nil), ids...)
	mock.ExpectQuery(detailBatchSubjectQuery("issues", "?,?,?")).WithArgs("bd-b", "missing", "bd-a").
		WillReturnRows(issueRows().AddRow(issueRowValues("bd-a", "A")...).AddRow(issueRowValues("bd-b", "B")...))
	mock.ExpectQuery(detailBatchSubjectQuery("wisps", "?")).WithArgs("missing").WillReturnRows(issueRows())
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery(detailBatchEdgeQuery("dependencies", "?,?", false)).WithArgs("bd-b", "bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"anchor", "far", "type"}).AddRow("bd-b", "missing", types.DepRelated))
	mock.ExpectQuery(detailBatchEdgeQuery("wisp_dependencies", "?,?", false)).WithArgs("bd-b", "bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"anchor", "far", "type"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery(detailBatchSubjectQuery("issues", "?")).WithArgs("missing").WillReturnRows(issueRows())
	for _, direction := range []publicops.EdgeDirection{publicops.EdgeDirectionOut, publicops.EdgeDirectionIn} {
		for _, plane := range edgeCountPlanes {
			mock.ExpectQuery(regexp.QuoteMeta(buildEdgeCountQuery(plane.dependencies, plane.sources, 2, publicops.EdgeCountRequest{Direction: direction}))).
				WithArgs("bd-b", "bd-a").WillReturnRows(sqlmock.NewRows([]string{"anchor", "n"}).AddRow("bd-b", 2))
		}
	}
	mock.ExpectQuery(`SELECT issue_id, label FROM labels`).WithArgs("bd-b", "bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}).AddRow("bd-b", "alpha").AddRow("bd-b", "beta").AddRow("bd-b", "gamma"))
	mock.ExpectQuery(`SELECT issue_id, COUNT\(\*\) as cnt FROM comments`).WithArgs("bd-b", "bd-a").
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "cnt"}).AddRow("bd-b", 1))
	out, err := ExecuteDetailBatch(context.Background(), db, publicops.DetailBatchRequest{IDs: ids}, DetailBatchEmbedded)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != len(ids) {
		t.Fatalf("got %d items, want %d", len(out.Items), len(ids))
	}
	for i, item := range out.Items {
		if item.ID != ids[i] || item.Found != (ids[i] != "missing") || (item.Issue != nil) != item.Found {
			t.Fatalf("item %d = %+v", i, item)
		}
	}
	b := out.Items[0].Issue
	if b.Dependencies != nil || *b.DependencyCount != 4 || *b.DependentCount != 4 || *b.CommentCount != 1 || b.CommentsOmitted == nil || !*b.CommentsOmitted {
		t.Fatalf("B counts/omission = %+v", b)
	}
	if !reflect.DeepEqual(b.Labels, []string{"alpha", "beta", "gamma"}) || !reflect.DeepEqual(b, out.Items[3].Issue) {
		t.Fatalf("labels or repeated details differ: %+v", out.Items)
	}
	if !reflect.DeepEqual(ids, before) {
		t.Fatalf("request mutated: %q", ids)
	}
}

// A database may lack the optional wisp tables. Reader.Get then reports the
// missing ID as not found, so the batch must not fail the call (R6).
func TestDetailBatchToleratesMissingOptionalWispTables(t *testing.T) {
	for _, missing := range []string{"wisps", "wisp_dependencies"} {
		t.Run(missing, func(t *testing.T) {
			db, mock := newDetailBatchMock(t)
			notExist := errors.New("Error 1146 (42S02): Table 'beads." + missing + "' doesn't exist")
			mock.ExpectQuery(detailBatchSubjectQuery("issues", "?,?")).WithArgs("bd-a", "missing").
				WillReturnRows(issueRows().AddRow(issueRowValues("bd-a", "A")...))
			wispSubject := mock.ExpectQuery(detailBatchSubjectQuery("wisps", "?")).WithArgs("missing")
			if missing == "wisps" {
				wispSubject.WillReturnError(notExist)
			} else {
				wispSubject.WillReturnRows(issueRows())
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnRows(sqlmock.NewRows([]string{"1"}))
			mock.ExpectQuery(detailBatchEdgeQuery("dependencies", "?", false)).WithArgs("bd-a").
				WillReturnRows(sqlmock.NewRows([]string{"anchor", "far", "type"}))
			wispEdges := mock.ExpectQuery(detailBatchEdgeQuery("wisp_dependencies", "?", false)).WithArgs("bd-a")
			if missing == "wisp_dependencies" {
				wispEdges.WillReturnError(notExist)
			} else {
				wispEdges.WillReturnRows(sqlmock.NewRows([]string{"anchor", "far", "type"}))
			}
			for _, direction := range []publicops.EdgeDirection{publicops.EdgeDirectionOut, publicops.EdgeDirectionIn} {
				for _, plane := range edgeCountPlanes {
					mock.ExpectQuery(regexp.QuoteMeta(buildEdgeCountQuery(plane.dependencies, plane.sources, 1, publicops.EdgeCountRequest{Direction: direction}))).
						WithArgs("bd-a").WillReturnRows(sqlmock.NewRows([]string{"anchor", "n"}))
				}
			}
			mock.ExpectQuery(`SELECT issue_id, label FROM labels`).WithArgs("bd-a").
				WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
			mock.ExpectQuery(`SELECT issue_id, COUNT\(\*\) as cnt FROM comments`).WithArgs("bd-a").
				WillReturnRows(sqlmock.NewRows([]string{"issue_id", "cnt"}))
			out, err := ExecuteDetailBatch(context.Background(), db, publicops.DetailBatchRequest{IDs: []string{"bd-a", "missing"}}, DetailBatchEmbedded)
			if err != nil {
				t.Fatalf("missing optional %s failed the call: %v", missing, err)
			}
			if len(out.Items) != 2 || !out.Items[0].Found || out.Items[1].Found || out.Items[1].Issue != nil {
				t.Fatalf("items = %+v", out.Items)
			}
		})
	}
}

func TestDetailBatchRoutePolicyAndAssembly(t *testing.T) {
	for _, route := range []struct {
		name   string
		policy DetailBatchPolicy
	}{
		{"embedded", DetailBatchEmbedded},
		{"direct-server", DetailBatchDirectServer},
		{"uow", DetailBatchUOW},
	} {
		for _, full := range []bool{false, true} {
			t.Run(route.name+map[bool]string{false: "/counts", true: "/rows-brief"}[full], func(t *testing.T) {
				db, mock := newDetailBatchMock(t)
				ctx := context.Background()
				subjectValues := issueRowValues("bd-a", "Durable subject")
				farValues := issueRowValues("bd-b", "Wisp far end")
				for i, column := range issueColumns() {
					if column == "row_lock" {
						subjectValues[i] = int64(9007199254740993)
					}
					if column == "description" {
						farValues[i] = "heavy far-end text"
					}
				}
				mock.ExpectQuery(detailBatchSubjectQuery("issues", "?")).WithArgs("bd-a").
					WillReturnRows(issueRows().AddRow(subjectValues...))
				if route.policy != DetailBatchUOW {
					mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
					mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM wisps WHERE id IN (?)")).WithArgs("bd-a").
						WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-a"))
				}
				if route.policy != DetailBatchDirectServer {
					mock.ExpectQuery(detailBatchEdgeQuery("dependencies", "?", false)).WithArgs("bd-a").
						WillReturnRows(sqlmock.NewRows([]string{"anchor", "far", "type"}).AddRow("bd-a", "bd-b", types.DepParentChild))
				}
				mock.ExpectQuery(detailBatchEdgeQuery("wisp_dependencies", "?", false)).WithArgs("bd-a").
					WillReturnRows(sqlmock.NewRows([]string{"anchor", "far", "type"}).AddRow("bd-a", "bd-b", types.DepRelated))
				if full {
					if route.policy == DetailBatchDirectServer {
						mock.ExpectQuery(`(?s)FROM issues i JOIN dependencies d ON d.issue_id = i.id.*UNION ALL.*FROM wisps w JOIN wisp_dependencies d ON d.issue_id = w.id.*ORDER BY created_at, plane, subject_id, depends_on_wisp_id, edge_id`).
							WithArgs("bd-a", "bd-a").WillReturnRows(sqlmock.NewRows([]string{"subject_id", "title", "status", "priority", "issue_type", "close_reason", "type", "anchor", "created_at", "plane", "depends_on_wisp_id", "edge_id"}).
							AddRow("bd-b", "Durable far end", types.StatusOpen, 1, types.TypeTask, nil, types.DepParentChild, "bd-a", "2026-01-01", 0, nil, "edge-1").
							AddRow("bd-b", "Wisp far end", types.StatusOpen, 1, types.TypeTask, nil, types.DepParentChild, "bd-a", "2026-01-01", 1, nil, "edge-1"))
					} else {
						for _, table := range []string{"dependencies", "wisp_dependencies"} {
							mock.ExpectQuery(detailBatchEdgeQuery(table, "?", true)).WithArgs("bd-a").
								WillReturnRows(sqlmock.NewRows([]string{"anchor", "far", "type"}).AddRow("bd-a", "bd-b", types.DepParentChild))
						}
					}
				}
				// A separate far-end set finds bd-b in wisps, even though it was
				// not an anchor and cannot be in the subject wisp set.
				mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM wisps WHERE id IN (?)")).WithArgs("bd-b").
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-b"))
				mock.ExpectQuery(detailBatchSubjectQuery("wisps", "?")).WithArgs("bd-b").WillReturnRows(issueRows().AddRow(farValues...))
				mock.ExpectQuery(`SELECT issue_id, label FROM wisp_labels`).WithArgs("bd-b").
					WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}).AddRow("bd-b", "far-label"))
				for _, direction := range []publicops.EdgeDirection{publicops.EdgeDirectionOut, publicops.EdgeDirectionIn} {
					for _, plane := range edgeCountPlanes {
						mock.ExpectQuery(regexp.QuoteMeta(buildEdgeCountQuery(plane.dependencies, plane.sources, 1, publicops.EdgeCountRequest{Direction: direction}))).
							WithArgs("bd-a").WillReturnRows(sqlmock.NewRows([]string{"anchor", "n"}).AddRow("bd-a", 1))
					}
				}
				labelTable, commentTable, wantLabel, wantComments := "wisp_labels", "wisp_comments", "wisp-label", 2
				if route.policy == DetailBatchUOW {
					labelTable, commentTable, wantLabel, wantComments = "labels", "comments", "durable-label", 1
				}
				mock.ExpectQuery(`SELECT issue_id, label FROM ` + labelTable).WithArgs("bd-a").
					WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}).AddRow("bd-a", wantLabel))
				mock.ExpectQuery(`SELECT issue_id, COUNT\(\*\) as cnt FROM ` + commentTable).WithArgs("bd-a").
					WillReturnRows(sqlmock.NewRows([]string{"issue_id", "cnt"}).AddRow("bd-a", wantComments))
				if full {
					rows := sqlmock.NewRows([]string{"id", "issue_id", "author", "text", "created_at"})
					for i := 0; i < wantComments; i++ {
						rows.AddRow(commentTable+string(rune('a'+i)), "bd-a", "author", "text", time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC))
					}
					mock.ExpectQuery(`SELECT id, issue_id, author, text, created_at FROM ` + commentTable).WithArgs("bd-a").WillReturnRows(rows)
				}
				var wantNested []string
				if route.policy != DetailBatchUOW {
					// The stores' GetIssueInTx takes Issue.Labels from the durable subject row's plane.
					wantNested = []string{"durable-label"}
					mock.ExpectQuery(`SELECT issue_id, label FROM labels`).WithArgs("bd-a").
						WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}).AddRow("bd-a", "durable-label"))
				}
				out, err := ExecuteDetailBatch(ctx, db, publicops.DetailBatchRequest{
					IDs: []string{"bd-a"}, IncludeDependents: full, IncludeComments: full, BriefDeps: full,
				}, route.policy)
				if err != nil || len(out.Items) != 1 || !out.Items[0].Found {
					t.Fatalf("batch = %+v, %v", out, err)
				}
				details := out.Items[0].Issue
				if details.Title != "Durable subject" || details.Revision != "9007199254740993" || !reflect.DeepEqual(details.Labels, []string{wantLabel}) || *details.CommentCount != int64(wantComments) {
					t.Fatalf("subject/auxiliary plane/revision = %+v", details)
				}
				if !reflect.DeepEqual(details.Issue.Labels, wantNested) {
					t.Fatalf("nested labels = %v, want %v", details.Issue.Labels, wantNested)
				}
				wantDeps := 2
				if route.policy == DetailBatchDirectServer {
					wantDeps = 1
					if details.Parent != nil {
						t.Fatalf("direct-server used the durable outgoing plane: parent=%v", details.Parent)
					}
				} else if details.Parent == nil || *details.Parent != "bd-b" || details.Dependencies[0].DependencyType != types.DepParentChild || details.Dependencies[1].DependencyType != types.DepRelated {
					t.Fatalf("durable-then-wisp dependencies/parent = %+v", details)
				}
				if len(details.Dependencies) != wantDeps || *details.DependencyCount != 2 || *details.DependentCount != 2 {
					t.Fatalf("rows/counts = %+v", details)
				}
				dep := details.Dependencies[0]
				if dep.Title != "Wisp far end" || (dep.Description == "") != full || (!full && !reflect.DeepEqual(dep.Labels, []string{"far-label"})) {
					t.Fatalf("far-end hydration/brief projection = %+v", dep)
				}
				if full {
					if len(details.Dependents) != 2 || len(details.Comments) != wantComments || details.CommentsOmitted != nil {
						t.Fatalf("optional rows = %+v", details)
					}
					if details.Dependents[0].ID != "bd-b" || details.Dependents[1].ID != "bd-b" || details.Dependents[0].Description != "" {
						t.Fatalf("dependent multiplicity/shallow projection = %+v", details.Dependents)
					}
					if route.policy == DetailBatchDirectServer && details.Dependents[0].Title != "Durable far end" {
						t.Fatalf("server lost same-plane source hydration: %+v", details.Dependents)
					}
				} else if details.Dependents != nil || details.Comments != nil || details.CommentsOmitted == nil || !*details.CommentsOmitted {
					t.Fatalf("counts-only optional fields = %+v", details)
				}
			})
		}
	}
}
