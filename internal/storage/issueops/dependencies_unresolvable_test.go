package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// bd-uschhs (#5850): the combined outgoing read. Every test here drives
// GetDependenciesWithUnresolvableInTx against sqlmock, so the exact queries
// it issues — anchor edges from both planes, one batched hydration, record
// queries only for the far ends that missed — are pinned, not assumed.

type anchorEdgeRow struct {
	farID, depType string
}

func anchorRows(edges []anchorEdgeRow) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"depends_on_id", "type"})
	for _, e := range edges {
		rows.AddRow(e.farID, e.depType)
	}
	return rows
}

// expectOutgoingAnchorEdges pins the anchor-edge read: durable plane first,
// then the wisp plane.
func expectOutgoingAnchorEdges(mock sqlmock.Sqlmock, anchor string, durable, wisp []anchorEdgeRow) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + DepTargetExpr + " AS depends_on_id, type FROM dependencies WHERE issue_id = ?")).
		WithArgs(anchor).
		WillReturnRows(anchorRows(durable))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT " + DepTargetExpr + " AS depends_on_id, type FROM wisp_dependencies WHERE issue_id = ?")).
		WithArgs(anchor).
		WillReturnRows(anchorRows(wisp))
}

func stringArgs(ids []string) []driver.Value {
	args := make([]driver.Value, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// expectEdgeHydration mocks GetIssuesByIDsInTx for the anchor's far ids: the
// wisps probe answers empty, each queryBatchSize batch of ids gets one issues
// query returning rows only for the hydrated ids, and each batch that
// hydrated anything gets one labels query over those hydrated ids.
func expectEdgeHydration(mock sqlmock.Sqlmock, farIDs []string, hydrated map[string]string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
		WillReturnError(sql.ErrNoRows)
	for start := 0; start < len(farIDs); start += queryBatchSize {
		end := start + queryBatchSize
		if end > len(farIDs) {
			end = len(farIDs)
		}
		batch := farIDs[start:end]
		rows := issueRows()
		var batchHydrated []string
		for _, id := range batch {
			if title, ok := hydrated[id]; ok {
				rows.AddRow(issueRowValues(id, title)...)
				batchHydrated = append(batchHydrated, id)
			}
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id IN (" + placeholders + ")")).
			WithArgs(stringArgs(batch)...).
			WillReturnRows(rows)
		if len(batchHydrated) > 0 {
			// The labels query runs over the WHOLE batch's ids (the same
			// inClause and args as the issues query), not just the hydrated
			// ones.
			mock.ExpectQuery(regexp.QuoteMeta("SELECT issue_id, label FROM labels WHERE issue_id IN (" + placeholders + ") ORDER BY issue_id, label")).
				WithArgs(stringArgs(batch)...).
				WillReturnRows(sqlmock.NewRows([]string{"issue_id", "label"}))
		}
	}
}

// unresolvedRecordsQueryRegex matches one batched stored-record query for n
// missed far ids against the named dependency table.
func unresolvedRecordsQueryRegex(table string, n int) string {
	placeholders := regexp.QuoteMeta(strings.TrimSuffix(strings.Repeat("?,", n), ","))
	return `(?s)SELECT issue_id, ` + regexp.QuoteMeta(DepTargetExpr) +
		` AS depends_on_id, type, created_at, created_by, metadata, thread_id\s+FROM ` +
		regexp.QuoteMeta(table) + ` WHERE issue_id = \? AND ` + regexp.QuoteMeta(DepTargetExpr) + ` IN \(` + placeholders + `\)`
}

func storedRecordRowValues(anchor string, rec *types.Dependency) []driver.Value {
	return []driver.Value{anchor, rec.DependsOnID, rec.Type, rec.CreatedAt, rec.CreatedBy, rec.Metadata, rec.ThreadID}
}

// unresolvedRecordPlan says what one dependency table's stored-record batch
// query answers: the records it returns, or the error it fails with (a nil
// plan means no rows, no error).
type unresolvedRecordPlan struct {
	recs     map[string]*types.Dependency
	queryErr error
}

// expectUnresolvedRecordBatches mocks the stored-record fetch for the missed
// far ids: the durable table first, then the wisp table, batch by batch.
func expectUnresolvedRecordBatches(mock sqlmock.Sqlmock, anchor string, missedIDs []string, durable, wisp unresolvedRecordPlan) {
	for _, table := range []struct {
		name string
		plan unresolvedRecordPlan
	}{
		{"dependencies", durable},
		{"wisp_dependencies", wisp},
	} {
		for start := 0; start < len(missedIDs); start += queryBatchSize {
			end := start + queryBatchSize
			if end > len(missedIDs) {
				end = len(missedIDs)
			}
			batch := missedIDs[start:end]
			expect := mock.ExpectQuery(unresolvedRecordsQueryRegex(table.name, len(batch))).
				WithArgs(append([]driver.Value{anchor}, stringArgs(batch)...)...)
			if table.plan.queryErr != nil {
				expect.WillReturnError(table.plan.queryErr)
				break
			}
			rows := dependencyRows()
			for _, id := range batch {
				if rec, ok := table.plan.recs[id]; ok {
					rows.AddRow(storedRecordRowValues(anchor, rec)...)
				}
			}
			expect.WillReturnRows(rows)
		}
	}
}

// TestGetDependenciesWithUnresolvableInTxSeparatesLocalFromMissing is AC-1's
// core: a local target hydrates into resolved, a missing target (both the
// cross-repo and the `external:` shapes) comes back as its exact stored
// record with type and metadata, and the old metadata reader equals the
// combined read's resolved portion.
func TestGetDependenciesWithUnresolvableInTxSeparatesLocalFromMissing(t *testing.T) {
	t.Parallel()

	const anchor = "ud-1"
	const local = "ud-2"
	missedIDs := []string{"external:liveop:liveop-kmf", "liveop-kmf"}

	stored := map[string]*types.Dependency{
		"external:liveop:liveop-kmf": {
			IssueID: anchor, DependsOnID: "external:liveop:liveop-kmf",
			Type: types.DepTracks, CreatedAt: time.Unix(1700000000, 0).UTC(),
			CreatedBy: "porter", Metadata: `{"score":0.9}`, ThreadID: "thread-1",
		},
		"liveop-kmf": {
			IssueID: anchor, DependsOnID: "liveop-kmf",
			Type: types.DepBlocks, CreatedAt: time.Unix(1700000001, 0).UTC(),
			CreatedBy: "porter", Metadata: "", ThreadID: "",
		},
	}

	anchorEdges := []anchorEdgeRow{
		{local, "blocks"},
		{"external:liveop:liveop-kmf", "tracks"},
		{"liveop-kmf", "blocks"},
	}
	farIDs := []string{local, "external:liveop:liveop-kmf", "liveop-kmf"}
	hydrated := map[string]string{local: "Local blocker"}

	_, mock, tx := beginMockTx(t)
	expectOutgoingAnchorEdges(mock, anchor, anchorEdges, nil)
	expectEdgeHydration(mock, farIDs, hydrated)
	expectUnresolvedRecordBatches(mock, anchor, missedIDs,
		unresolvedRecordPlan{recs: stored},
		unresolvedRecordPlan{})

	resolved, unresolvable, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, anchor)
	if err != nil {
		t.Fatalf("GetDependenciesWithUnresolvableInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}

	if len(resolved) != 1 || resolved[0].ID != local || resolved[0].DependencyType != types.DepBlocks {
		t.Fatalf("resolved = %+v, want exactly the one local edge", resolved)
	}
	if len(unresolvable) != 2 {
		t.Fatalf("unresolvable = %+v, want the two missing targets", unresolvable)
	}
	// Sorted by DependsOnID: "external:..." < "liveop-kmf".
	for i, want := range []string{"external:liveop:liveop-kmf", "liveop-kmf"} {
		got := unresolvable[i]
		wantRec := stored[want]
		if got.DependsOnID != want || got.IssueID != anchor || got.Type != wantRec.Type ||
			!got.CreatedAt.Equal(wantRec.CreatedAt) || got.CreatedBy != wantRec.CreatedBy ||
			got.Metadata != wantRec.Metadata || got.ThreadID != wantRec.ThreadID {
			t.Fatalf("unresolvable[%d] = %+v, want the exact stored record for %q", i, got, want)
		}
	}

	// The old metadata reader equals the combined read's resolved portion.
	_, mock, tx = beginMockTx(t)
	expectOutgoingAnchorEdges(mock, anchor, anchorEdges, nil)
	expectEdgeHydration(mock, farIDs, hydrated)
	old, err := GetDependenciesWithMetadataInTx(context.Background(), tx, anchor)
	if err != nil {
		t.Fatalf("GetDependenciesWithMetadataInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations (old reader): %v", err)
	}
	if len(old) != 1 || !reflect.DeepEqual(old, resolved) {
		t.Fatalf("old reader = %+v, want the combined read's resolved portion %+v", old, resolved)
	}
}

// TestGetDependenciesWithUnresolvableInTxSkipsRecordQueriesWithoutMisses: a
// fully local anchor issues no stored-record query at all — sqlmock fails
// any unexpected query, so ExpectationsWereMet is the assertion.
func TestGetDependenciesWithUnresolvableInTxSkipsRecordQueriesWithoutMisses(t *testing.T) {
	t.Parallel()

	const anchor, local = "ud-1", "ud-2"
	_, mock, tx := beginMockTx(t)
	expectOutgoingAnchorEdges(mock, anchor, []anchorEdgeRow{{local, "blocks"}}, nil)
	expectEdgeHydration(mock, []string{local}, map[string]string{local: "Local blocker"})

	resolved, unresolvable, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, anchor)
	if err != nil {
		t.Fatalf("GetDependenciesWithUnresolvableInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ID != local {
		t.Fatalf("resolved = %+v, want the one local edge", resolved)
	}
	if unresolvable != nil {
		t.Fatalf("unresolvable = %+v, want nil with no misses", unresolvable)
	}
}

// TestGetDependenciesWithUnresolvableInTxEmptyAnchor: no outgoing edges
// answers two nil lists and never queries the issue plane.
func TestGetDependenciesWithUnresolvableInTxEmptyAnchor(t *testing.T) {
	t.Parallel()

	_, mock, tx := beginMockTx(t)
	expectOutgoingAnchorEdges(mock, "ud-1", nil, nil)

	resolved, unresolvable, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, "ud-1")
	if err != nil {
		t.Fatalf("GetDependenciesWithUnresolvableInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	if resolved != nil || unresolvable != nil {
		t.Fatalf("resolved = %+v, unresolvable = %+v, want both nil", resolved, unresolvable)
	}
}

// TestGetDependenciesWithUnresolvableInTxDurablePrecedenceForDuplicateFarEnd:
// the same far id in both planes yields ONE record, and the durable row's
// stamps win — the same precedence anchorEdgesInTx applies to the edges.
func TestGetDependenciesWithUnresolvableInTxDurablePrecedenceForDuplicateFarEnd(t *testing.T) {
	t.Parallel()

	const anchor = "ud-1"
	const far = "external:liveop:liveop-kmf"
	durable := &types.Dependency{IssueID: anchor, DependsOnID: far, Type: types.DepTracks, CreatedAt: time.Unix(1700000000, 0).UTC(), CreatedBy: "durable-author", Metadata: `{"from":"durable"}`, ThreadID: "thread-d"}
	wispCopy := &types.Dependency{IssueID: anchor, DependsOnID: far, Type: types.DepTracks, CreatedAt: time.Unix(1800000000, 0).UTC(), CreatedBy: "wisp-author", Metadata: `{"from":"wisp"}`, ThreadID: "thread-w"}

	_, mock, tx := beginMockTx(t)
	expectOutgoingAnchorEdges(mock, anchor,
		[]anchorEdgeRow{{far, "tracks"}},
		[]anchorEdgeRow{{far, "tracks"}})
	expectEdgeHydration(mock, []string{far}, nil)
	expectUnresolvedRecordBatches(mock, anchor, []string{far},
		unresolvedRecordPlan{recs: map[string]*types.Dependency{far: durable}},
		unresolvedRecordPlan{recs: map[string]*types.Dependency{far: wispCopy}})

	resolved, unresolvable, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, anchor)
	if err != nil {
		t.Fatalf("GetDependenciesWithUnresolvableInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("resolved = %+v, want none", resolved)
	}
	if len(unresolvable) != 1 {
		t.Fatalf("unresolvable = %+v, want one record per far-end id", unresolvable)
	}
	got := unresolvable[0]
	if got.CreatedAt != durable.CreatedAt || got.CreatedBy != "durable-author" || got.Metadata != `{"from":"durable"}` || got.ThreadID != "thread-d" {
		t.Fatalf("record = %+v, want the durable row's stamps %+v", got, durable)
	}
}

// TestGetDependenciesWithUnresolvableInTxToleratesMissingWispDependencyTable:
// the wisp_dependencies table can vanish mid-session (dolt-ignored, dropped
// by a server working-set reset, bd-cif6y4). A wisp-plane edge the anchor saw
// still comes back — built from the anchor edge's own far id and type, with
// no invented stamps.
func TestGetDependenciesWithUnresolvableInTxToleratesMissingWispDependencyTable(t *testing.T) {
	t.Parallel()

	const anchor = "ud-1"
	const far = "liveop-kmf"

	_, mock, tx := beginMockTx(t)
	expectOutgoingAnchorEdges(mock, anchor, nil, []anchorEdgeRow{{far, "blocks"}})
	expectEdgeHydration(mock, []string{far}, nil)
	expectUnresolvedRecordBatches(mock, anchor, []string{far},
		unresolvedRecordPlan{},
		unresolvedRecordPlan{queryErr: errors.New("Error 1146: Table 'db.wisp_dependencies' doesn't exist")})

	resolved, unresolvable, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, anchor)
	if err != nil {
		t.Fatalf("GetDependenciesWithUnresolvableInTx: %v (want the missing wisp table tolerated)", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("resolved = %+v, want none", resolved)
	}
	if len(unresolvable) != 1 {
		t.Fatalf("unresolvable = %+v, want the anchor-seen edge to survive the table drop", unresolvable)
	}
	got := unresolvable[0]
	if got.DependsOnID != far || got.Type != types.DepBlocks || got.IssueID != anchor {
		t.Fatalf("record = %+v, want the anchor edge {far:%q type:%q}", got, far, types.DepBlocks)
	}
	if !got.CreatedAt.IsZero() || got.CreatedBy != "" || got.Metadata != "" || got.ThreadID != "" {
		t.Fatalf("record = %+v, want no synthesized stamps", got)
	}
}

// TestGetDependenciesWithUnresolvableInTxPropagatesRecordQueryError: a
// failure of the mandatory durable-plane record query is returned, not
// swallowed into a short list.
func TestGetDependenciesWithUnresolvableInTxPropagatesRecordQueryError(t *testing.T) {
	t.Parallel()

	const anchor = "ud-1"
	const far = "liveop-kmf"

	_, mock, tx := beginMockTx(t)
	expectOutgoingAnchorEdges(mock, anchor, []anchorEdgeRow{{far, "blocks"}}, nil)
	expectEdgeHydration(mock, []string{far}, nil)
	expectUnresolvedRecordBatches(mock, anchor, []string{far},
		unresolvedRecordPlan{queryErr: errors.New("connection refused")},
		unresolvedRecordPlan{})

	_, _, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, anchor)
	if err == nil {
		t.Fatal("GetDependenciesWithUnresolvableInTx: want the durable record query error returned")
	}
}

// TestGetDependenciesWithUnresolvableInTxBatchesMissedIDs: more missed far
// ids than one IN clause holds still yields one record per id — the record
// fetch batches at queryBatchSize like the hydration does.
func TestGetDependenciesWithUnresolvableInTxBatchesMissedIDs(t *testing.T) {
	t.Parallel()

	const anchor = "ud-1"
	n := queryBatchSize + 1
	farIDs := make([]string, n)
	for i := range farIDs {
		farIDs[i] = fmt.Sprintf("liveop-miss-%04d", i)
	}

	_, mock, tx := beginMockTx(t)
	anchorEdges := make([]anchorEdgeRow, n)
	for i, id := range farIDs {
		anchorEdges[i] = anchorEdgeRow{farID: id, depType: "blocks"}
	}
	expectOutgoingAnchorEdges(mock, anchor, anchorEdges, nil)
	expectEdgeHydration(mock, farIDs, nil)
	expectUnresolvedRecordBatches(mock, anchor, farIDs,
		unresolvedRecordPlan{}, unresolvedRecordPlan{})

	resolved, unresolvable, err := GetDependenciesWithUnresolvableInTx(context.Background(), tx, anchor)
	if err != nil {
		t.Fatalf("GetDependenciesWithUnresolvableInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
	if len(resolved) != 0 {
		t.Fatalf("resolved = %+v, want none", resolved)
	}
	if len(unresolvable) != n {
		t.Fatalf("unresolvable count = %d, want %d (one per missed far id)", len(unresolvable), n)
	}
	for i, rec := range unresolvable {
		if rec.DependsOnID != farIDs[i] || rec.Type != types.DepBlocks || rec.IssueID != anchor {
			t.Fatalf("unresolvable[%d] = %+v, want {far:%q type:%q}", i, rec, farIDs[i], types.DepBlocks)
		}
	}
}
