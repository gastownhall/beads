package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/domain"
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
	{
		name:     "GetDependenciesWithMetadataInTx",
		withType: true,
		run: func(ctx context.Context, tx *sql.Tx, anchor string) ([]readerRow, error) {
			out, err := GetDependenciesWithMetadataInTx(ctx, tx, anchor)
			return metadataRows(out), err
		},
	},
	{
		name:    "GetDependentsInTx",
		inbound: true,
		run: func(ctx context.Context, tx *sql.Tx, anchor string) ([]readerRow, error) {
			out, err := GetDependentsInTx(ctx, tx, anchor)
			return issueRowsOf(out), err
		},
	},
	{
		name: "GetDependenciesInTx",
		run: func(ctx context.Context, tx *sql.Tx, anchor string) ([]readerRow, error) {
			out, err := GetDependenciesInTx(ctx, tx, anchor)
			return issueRowsOf(out), err
		},
	},
}

func issueRowsOf(in []*types.Issue) []readerRow {
	out := make([]readerRow, len(in))
	for i, iss := range in {
		out[i] = readerRow{id: iss.ID}
	}
	return out
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

// edgeCountEntryPoint is one direction of CountDependencyEdgesInTx, the count
// `bd show` prints beside the listing on every backend. It has to count what
// the listing returns, or show reports the gap as unresolvable edges.
type edgeCountEntryPoint struct {
	name string
	dir  domain.DepDirection
}

var edgeCountEntryPoints = []edgeCountEntryPoint{
	{name: "CountDependencyEdgesInTx/in", dir: domain.DepDirectionIn},
	{name: "CountDependencyEdgesInTx/out", dir: domain.DepDirectionOut},
}

// TestCountDependencyEdgesMissingWispPlaneIsNotAnError: with no
// wisp_dependencies the durable count is the whole answer, as it is for the
// listing, instead of an error that publishes a count of 0.
func TestCountDependencyEdgesMissingWispPlaneIsNotAnError(t *testing.T) {
	for _, ep := range edgeCountEntryPoints {
		t.Run(ep.name, func(t *testing.T) {
			_, mock, tx := beginMockTx(t)
			mock.ExpectQuery(`^SELECT count\(\*\) FROM dependencies WHERE `).
				WillReturnRows(scalarCountRows(3))
			mock.ExpectQuery(`^SELECT count\(\*\) FROM wisp_dependencies WHERE `).
				WillReturnError(tableNotFound("wisp_dependencies"))

			n, err := CountDependencyEdgesInTx(context.Background(), tx, "epic-1", ep.dir, nil)
			if err != nil {
				t.Fatalf("%s errored on a database with no wisp_dependencies: %v", ep.name, err)
			}
			if n != 3 {
				t.Fatalf("%s = %d, want 3 (the durable edges)", ep.name, n)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("%s: unmet sql expectations: %v", ep.name, err)
			}
		})
	}
}

// TestDependencyReadersBrokenPlaneIsAnError pins how narrow the tolerance is.
// Only the absence of wisp_dependencies means "no wisp edges"; a missing
// durable plane, a table hydration reads (leases through sqlbuild.LeaseJoin,
// wisp_labels for a wisp far end) or a failed connection is a broken database,
// and `bd show` must say so rather than print a short CHILDREN list.
func TestDependencyReadersBrokenPlaneIsAnError(t *testing.T) {
	wispChild := []readerRow{{id: "wisp-child", depType: string(types.DepParentChild)}}
	cases := []struct {
		name  string
		gone  error
		prime func(mock sqlmock.Sqlmock, ep depReaderEntryPoint, gone error)
	}{
		{
			name: "missing dependencies",
			gone: tableNotFound("dependencies"),
			prime: func(mock sqlmock.Sqlmock, ep depReaderEntryPoint, gone error) {
				mock.ExpectQuery(edgeQueryRegex(ep, "dependencies")).WillReturnError(gone)
			},
		},
		{
			name: "broken connection on the wisp plane",
			gone: errors.New("connection refused"),
			prime: func(mock sqlmock.Sqlmock, ep depReaderEntryPoint, gone error) {
				mock.ExpectQuery(edgeQueryRegex(ep, "dependencies")).WillReturnRows(edgeRows(ep, threeChildren))
				mock.ExpectQuery(edgeQueryRegex(ep, "wisp_dependencies")).WillReturnError(gone)
			},
		},
		{
			name: "missing leases during hydration",
			gone: tableNotFound("leases"),
			prime: func(mock sqlmock.Sqlmock, ep depReaderEntryPoint, gone error) {
				mock.ExpectQuery(edgeQueryRegex(ep, "dependencies")).WillReturnRows(edgeRows(ep, threeChildren))
				mock.ExpectQuery(edgeQueryRegex(ep, "wisp_dependencies")).WillReturnRows(edgeRows(ep, nil))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnError(sql.ErrNoRows)
				mock.ExpectQuery(regexp.QuoteMeta(" FROM issues " + sqlbuild.LeaseJoin("issues"))).WillReturnError(gone)
			},
		},
		{
			name: "missing wisp_labels during wisp hydration",
			gone: tableNotFound("wisp_labels"),
			prime: func(mock sqlmock.Sqlmock, ep depReaderEntryPoint, gone error) {
				mock.ExpectQuery(edgeQueryRegex(ep, "dependencies")).WillReturnRows(edgeRows(ep, nil))
				mock.ExpectQuery(edgeQueryRegex(ep, "wisp_dependencies")).WillReturnRows(edgeRows(ep, wispChild))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
					WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM wisps WHERE id IN (?)")).
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("wisp-child"))
				mock.ExpectQuery(regexp.QuoteMeta(" FROM wisps " + sqlbuild.LeaseJoin("wisps"))).
					WillReturnRows(issueRows().AddRow(issueRowValues("wisp-child", "wisp-child")...))
				mock.ExpectQuery(regexp.QuoteMeta("FROM wisp_labels")).WillReturnError(gone)
			},
		},
	}
	for _, tc := range cases {
		for _, ep := range depReaderEntryPoints {
			t.Run(tc.name+"/"+ep.name, func(t *testing.T) {
				_, mock, tx := beginMockTx(t)
				tc.prime(mock, ep, tc.gone)

				got, err := ep.run(context.Background(), tx, "epic-1")
				if err == nil {
					t.Fatalf("%s succeeded on a broken database, returning %v", ep.name, got)
				}
				if !errors.Is(err, tc.gone) {
					t.Fatalf("%s error is not the primed failure %v: %v", ep.name, tc.gone, err)
				}
			})
		}
	}
}

// TestCountDependencyEdgesBrokenPlaneIsAnError is the same guard for the
// count: the wisp leg's NOT IN subquery reads dependencies, so a missing
// dependencies table reported from that leg is not the optional plane either.
func TestCountDependencyEdgesBrokenPlaneIsAnError(t *testing.T) {
	cases := []struct {
		name          string
		durable, wisp error
	}{
		{name: "missing dependencies", durable: tableNotFound("dependencies")},
		{name: "missing dependencies under the wisp leg", wisp: tableNotFound("dependencies")},
		{name: "broken connection on the wisp leg", wisp: errors.New("connection refused")},
	}
	for _, tc := range cases {
		for _, ep := range edgeCountEntryPoints {
			t.Run(tc.name+"/"+ep.name, func(t *testing.T) {
				_, mock, tx := beginMockTx(t)
				gone := tc.durable
				if gone != nil {
					mock.ExpectQuery(`^SELECT count\(\*\) FROM dependencies WHERE `).WillReturnError(gone)
				} else {
					gone = tc.wisp
					mock.ExpectQuery(`^SELECT count\(\*\) FROM dependencies WHERE `).WillReturnRows(scalarCountRows(3))
					mock.ExpectQuery(`^SELECT count\(\*\) FROM wisp_dependencies WHERE `).WillReturnError(gone)
				}

				n, err := CountDependencyEdgesInTx(context.Background(), tx, "epic-1", ep.dir, nil)
				if err == nil {
					t.Fatalf("%s succeeded on a broken database, returning %d", ep.name, n)
				}
				if !errors.Is(err, gone) {
					t.Fatalf("%s error is not the primed failure %v: %v", ep.name, gone, err)
				}
			})
		}
	}
}
