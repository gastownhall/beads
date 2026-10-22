package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// depReaderEntryPoint is one of the readers that list an anchor's edges from
// both dependency planes and hydrate the issues at the far end (bd-cif6y4,
// #3445). Each one used to fail outright when wisp_dependencies was missing
// and to return an edge twice when it was stored in both planes.
type depReaderEntryPoint struct {
	name string
	// inbound reads the edges whose target is the anchor (dependents);
	// otherwise the edges whose source is the anchor (dependencies).
	inbound bool
	// withType is whether the reader selects the edge type alongside the far
	// end. The plain readers return issues only.
	withType bool
	run      func(ctx context.Context, tx *sql.Tx, anchor string) ([]readerRow, error)
}

// readerRow is one returned far end; depType is empty for the plain readers.
type readerRow struct {
	id, depType string
}

func (r readerRow) String() string {
	if r.depType == "" {
		return r.id
	}
	return r.id + ":" + r.depType
}

func metadataRows(in []*types.IssueWithDependencyMetadata) []readerRow {
	out := make([]readerRow, len(in))
	for i, d := range in {
		out[i] = readerRow{id: d.ID, depType: string(d.DependencyType)}
	}
	return out
}

var depReaderEntryPoints = []depReaderEntryPoint{
	{
		name:     "GetDependentsWithMetadataInTx",
		inbound:  true,
		withType: true,
		run: func(ctx context.Context, tx *sql.Tx, anchor string) ([]readerRow, error) {
			out, err := GetDependentsWithMetadataInTx(ctx, tx, anchor)
			return metadataRows(out), err
		},
	},
}

// edgeQueryRegex is the exact per-plane SQL each reader issues today.
func edgeQueryRegex(ep depReaderEntryPoint, table string) string {
	cols := "issue_id"
	where := DepTargetExpr + " = ?"
	if !ep.inbound {
		cols = DepTargetExpr + " AS depends_on_id"
		where = "issue_id = ?"
	}
	if ep.withType {
		cols += ", type"
	}
	return "^" + regexp.QuoteMeta("SELECT "+cols+" FROM "+table+" WHERE "+where) + "$"
}

func edgeRows(ep depReaderEntryPoint, edges []readerRow) *sqlmock.Rows {
	cols := []string{"far"}
	if ep.withType {
		cols = append(cols, "type")
	}
	rows := sqlmock.NewRows(cols)
	for _, e := range edges {
		if ep.withType {
			rows.AddRow(e.id, e.depType)
		} else {
			rows.AddRow(e.id)
		}
	}
	return rows
}

// expectDurableHydration primes GetIssuesByIDsInTx for ids that are all
// durable issues. The IN list is matched argument by argument, so a reader
// that hands hydration a repeated id fails here rather than being rescued by
// `IN (a, a)` collapsing the repeat.
func expectDurableHydration(mock sqlmock.Sqlmock, ids []string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
		WillReturnError(sql.ErrNoRows)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]driver.Value, len(ids))
	rows := issueRows()
	for i, id := range ids {
		args[i] = id
		rows.AddRow(issueRowValues(id, id)...)
	}
	mock.ExpectQuery("^" + regexp.QuoteMeta("SELECT "+IssueSelectColumns+" FROM issues "+sqlbuild.LeaseJoin("issues")+" WHERE id IN ("+placeholders+")") + "$").
		WithArgs(args...).
		WillReturnRows(rows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM labels WHERE issue_id IN (" + placeholders + ") ORDER BY issue_id, label")).
		WithArgs(args...).
		WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
}

func wantRows(ep depReaderEntryPoint, edges []readerRow) string {
	out := make([]readerRow, len(edges))
	for i, e := range edges {
		out[i] = e
		if !ep.withType {
			out[i].depType = ""
		}
	}
	return fmt.Sprint(out)
}

var threeChildren = []readerRow{
	{id: "child-1", depType: string(types.DepParentChild)},
	{id: "child-2", depType: string(types.DepParentChild)},
	{id: "child-3", depType: string(types.DepParentChild)},
}

func readerIDs(edges []readerRow) []string {
	ids := make([]string, len(edges))
	for i, e := range edges {
		ids[i] = e.id
	}
	return ids
}

// TestDependencyReadersMissingWispPlaneIsNotAnError: a server-mode working set
// reset drops the dolt-ignored wisp_dependencies table. The durable edges are
// still the whole answer, so `bd show <epic>` must list its children instead
// of silently losing the CHILDREN section and its rollup.
func TestDependencyReadersMissingWispPlaneIsNotAnError(t *testing.T) {
	for _, ep := range depReaderEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)
			mock.ExpectQuery(edgeQueryRegex(ep, "dependencies")).WithArgs("epic-1").
				WillReturnRows(edgeRows(ep, threeChildren))
			mock.ExpectQuery(edgeQueryRegex(ep, "wisp_dependencies")).WithArgs("epic-1").
				WillReturnError(tableNotFound("wisp_dependencies"))
			expectDurableHydration(mock, readerIDs(threeChildren))

			got, err := ep.run(context.Background(), tx, "epic-1")
			if err != nil {
				t.Fatalf("%s errored on a database with no wisp_dependencies: %v", ep.name, err)
			}
			if fmt.Sprint(got) != wantRows(ep, threeChildren) {
				t.Fatalf("%s = %v, want %s", ep.name, got, wantRows(ep, threeChildren))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("%s: unmet sql expectations: %v", ep.name, err)
			}
		})
	}
}

// TestDependencyReadersCrossTableDuplicateReturnedOnce: the same edge can sit
// in both planes (another clone's promotion pulled in next to a still-local
// wisp copy). It is one edge: `bd show <epic>` must list N children and k/N,
// not 2N and 2k/2N. The durable row wins, type included, and an edge that is
// only in wisp_dependencies is still returned.
func TestDependencyReadersCrossTableDuplicateReturnedOnce(t *testing.T) {
	wispOnly := readerRow{id: "wisp-child", depType: string(types.DepParentChild)}
	wispPlane := []readerRow{
		// A type-divergent copy of child-1: the durable parent-child must win.
		{id: "child-1", depType: string(types.DepBlocks)},
		threeChildren[1],
		threeChildren[2],
		wispOnly,
	}
	want := append(append([]readerRow{}, threeChildren...), wispOnly)

	for _, ep := range depReaderEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)
			mock.ExpectQuery(edgeQueryRegex(ep, "dependencies")).WithArgs("epic-1").
				WillReturnRows(edgeRows(ep, threeChildren))
			mock.ExpectQuery(edgeQueryRegex(ep, "wisp_dependencies")).WithArgs("epic-1").
				WillReturnRows(edgeRows(ep, wispPlane))
			expectDurableHydration(mock, readerIDs(want))

			got, err := ep.run(context.Background(), tx, "epic-1")
			if err != nil {
				t.Fatalf("%s: %v", ep.name, err)
			}
			if fmt.Sprint(got) != wantRows(ep, want) {
				t.Fatalf("%s = %v, want %s", ep.name, got, wantRows(ep, want))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("%s: unmet sql expectations: %v", ep.name, err)
			}
		})
	}
}
