package issueops

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// coordinateSQL is the prefix of the shard coordination write every guarded
// create and every retaining sweep makes before it reads or deletes.
var coordinateSQL = regexp.QuoteMeta("REPLACE INTO local_metadata (`key`, value) VALUES")

// coordinationKeysFor returns the distinct, sorted shard keys of ids: the
// argument order CoordinateIDCreatesInTx writes them in.
func coordinationKeysFor(ids ...string) []string {
	var keys []string
	for _, id := range ids {
		key := issueCreateCoordinationKey(id)
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// expectCoordination expects one coordination write of exactly keys, in order.
func expectCoordination(mock sqlmock.Sqlmock, keys []string) {
	args := make([]driver.Value, 0, 2*len(keys))
	for _, key := range keys {
		args = append(args, key, sqlmock.AnyArg())
	}
	mock.ExpectExec(coordinateSQL).WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, int64(len(keys))))
}

// TestCoordinateIDCreatesWritesEachShardOnceInShardOrder pins the lock shape:
// one cell per shard however many ids share it, in sorted key order (two
// writers locking overlapping shard sets in the same order cannot deadlock),
// in one statement, and nothing at all for no ids.
func TestCoordinateIDCreatesWritesEachShardOnceInShardOrder(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	ids := []string{"bd-zz", "bd-aa", "bd-zz", "", "bd-mm"}
	expectCoordination(mock, coordinationKeysFor("bd-zz", "bd-aa", "bd-mm"))
	if err := CoordinateIDCreatesInTx(context.Background(), db, ids); err != nil {
		t.Fatalf("CoordinateIDCreatesInTx() = %v", err)
	}
	if err := CoordinateIDCreatesInTx(context.Background(), db, []string{""}); err != nil {
		t.Fatalf("CoordinateIDCreatesInTx(no ids) = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRefuseRetiredIDCoordinatesBeforeItsProbe pins the upsert guard's order:
// the shard cell is written BEFORE the occupancy probe. Under READ COMMITTED
// that write waits for a retaining sweep holding the same cell, so the probe
// that follows sees the sweep's tombstone instead of the row it deleted.
func TestRefuseRetiredIDCoordinatesBeforeItsProbe(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	expectCoordination(mock, coordinationKeysFor("bd-1"))
	mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).WithArgs("bd-1", "bd-1", "bd-1", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"src", "closed_at"}))
	if err := RefuseRetiredIDInTx(context.Background(), db, "bd-1"); err != nil {
		t.Fatalf("RefuseRetiredIDInTx() = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpsertCreateCoordinatesBeforeItsRetiredProbe pins the same order on the
// upsert-shaped create body (CreateOnly false, explicit id): the stores'
// CreateIssue, a transaction's create, a one-row batch.
func TestUpsertCreateCoordinatesBeforeItsRetiredProbe(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	expectCoordination(mock, coordinationKeysFor("bd-1"))
	mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).WithArgs("bd-1", "bd-1", "bd-1", "bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"src", "closed_at"}).AddRow("issues", nil))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM wisps WHERE id = ?")).WithArgs("bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	bc := &BatchContext{Opts: storage.BatchCreateOptions{SkipPrefixValidation: true}}
	issue := &types.Issue{ID: "bd-1", Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if _, _, outcome, err := prepareCreateIssueInTx(context.Background(), db, bc, issue, "actor"); err != nil || outcome != prepareWrite {
		t.Fatalf("prepareCreateIssueInTx() = %v, %v; want prepareWrite, nil", outcome, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUpsertBatchCoordinatesEveryExplicitIDBeforeItsReads pins the batch
// order: an upsert batch locks the shard of every explicit id in one write
// BEFORE its up-front presence and tombstone reads, so those reads cannot
// predate a racing sweep's commit; the rows it then prepares do not lock again.
func TestUpsertBatchCoordinatesEveryExplicitIDBeforeItsReads(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	issues := []*types.Issue{
		{ID: "bd-1", Title: "a", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bd-2", Title: "b", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{Title: "generated", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	expectCoordination(mock, coordinationKeysFor("bd-1", "bd-2"))
	for _, table := range []string{"issues", "wisps"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM " + table + " WHERE id IN (?,?)")).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
	}
	for _, table := range IDTombstoneTables() {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id, closed_at FROM " + table + " WHERE id IN (?,?)")).
			WillReturnRows(sqlmock.NewRows([]string{"id", "closed_at"}))
	}
	cache, err := newCreateBatchCache(context.Background(), db, issues, storage.BatchCreateOptions{})
	if err != nil {
		t.Fatalf("newCreateBatchCache() = %v", err)
	}
	if !cache.coordinated {
		t.Fatal("an upsert batch's cache does not record that it coordinated its explicit ids")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	// A prepared row of this batch answers from the cache and writes no second
	// coordination cell: any statement here is unexpected and fails it.
	bc := &BatchContext{Opts: storage.BatchCreateOptions{SkipPrefixValidation: true}, cache: cache}
	if _, _, outcome, err := prepareCreateIssueInTx(context.Background(), db, bc, issues[0], "actor"); err != nil || outcome != prepareWrite {
		t.Fatalf("prepareCreateIssueInTx(batch row) = %v, %v; want prepareWrite, nil", outcome, err)
	}
}

// TestCreateOnlyBatchCoordinatesEveryIDOnceBeforeItsReads pins the
// create-only batch's lock order: every id the batch already names is locked
// in ONE sorted write before the up-front reads, never one item at a time in
// item order. A retaining sweep, an upsert batch and an import lock large
// sorted cell sets; a batch that took its cells in item order could hold a
// later cell while waiting on an earlier one and deadlock against them (40P01
// on Postgres, which Store.CreateIssues does not retry). It still reads no
// tombstones up front: each row meets the create-only probe.
func TestCreateOnlyBatchCoordinatesEveryIDOnceBeforeItsReads(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	issues := []*types.Issue{
		{ID: "bd-zz", Title: "a", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "bd-aa", Title: "b", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	expectCoordination(mock, coordinationKeysFor("bd-zz", "bd-aa"))
	for _, table := range []string{"issues", "wisps"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM " + table + " WHERE id IN (?,?)")).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
	}
	cache, err := newCreateBatchCache(context.Background(), db, issues, storage.BatchCreateOptions{CreateOnly: true})
	if err != nil {
		t.Fatalf("newCreateBatchCache(CreateOnly) = %v", err)
	}
	if !cache.coordinated {
		t.Fatal("a CreateOnly batch's cache does not record that it coordinated its ids")
	}
	if cache.retired != nil {
		t.Fatalf("a CreateOnly batch read tombstones up front (%v); its rows each meet the create-only probe", cache.retired)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	// A row of this batch runs the create-only probe and writes no second
	// cell: the only statement it may make is the occupancy read.
	mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).WithArgs("bd-zz", "bd-zz", "bd-zz", "bd-zz").
		WillReturnRows(sqlmock.NewRows([]string{"src", "closed_at"}))
	bc := &BatchContext{Opts: storage.BatchCreateOptions{CreateOnly: true, SkipPrefixValidation: true}, cache: cache}
	if _, _, outcome, err := prepareCreateIssueInTx(context.Background(), db, bc, issues[0], "actor"); err != nil || outcome != prepareWrite {
		t.Fatalf("prepareCreateIssueInTx(batch row) = %v, %v; want prepareWrite, nil", outcome, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCreateOnlyRowOutsideTheUpFrontSetStillCoordinates pins that the
// skipped lock is only skipped for an id the batch locked up front: a row
// whose id the cache never saw (a generated id) takes its own cell before
// its probe. The probe is the row's last read: it found no row in either
// plane, so the cross-plane collision check does not read the sibling plane
// again (TestCreateOnlyCreateReadsEachPlaneOnce).
func TestCreateOnlyRowOutsideTheUpFrontSetStillCoordinates(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	cache := &createBatchCache{
		probed:      map[string]bool{"bd-1": true},
		present:     map[string]map[string]bool{"issues": {}, "wisps": {}},
		coordinated: true,
	}
	expectCoordination(mock, coordinationKeysFor("bd-2"))
	mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).WithArgs("bd-2", "bd-2", "bd-2", "bd-2").
		WillReturnRows(sqlmock.NewRows([]string{"src", "closed_at"}))
	bc := &BatchContext{Opts: storage.BatchCreateOptions{CreateOnly: true, SkipPrefixValidation: true}, cache: cache}
	issue := &types.Issue{ID: "bd-2", Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if _, _, outcome, err := prepareCreateIssueInTx(context.Background(), db, bc, issue, "actor"); err != nil || outcome != prepareWrite {
		t.Fatalf("prepareCreateIssueInTx() = %v, %v; want prepareWrite, nil", outcome, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestApplyBatchCoordinatesItsExplicitCreateIDsFirst pins the apply-batch
// lock order: before its first item, an apply-batch locks the shard of every
// explicit create id it carries in one sorted write, so its per-item creates
// meet cells the transaction already holds. The statement after the lock is
// left unexpected: the test is about what comes first.
func TestApplyBatchCoordinatesItsExplicitCreateIDsFirst(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	mock.ExpectBegin()
	expectCoordination(mock, coordinationKeysFor("bd-zz", "bd-aa"))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	plan := storage.ApplyBatchPlan{Actor: "actor", Items: []publicops.ApplyItem{
		{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Issue: &types.Issue{ID: "bd-zz", Title: "a"}}},
		{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Issue: &types.Issue{Title: "generated"}}},
		{Kind: publicops.ItemCreate, Create: &publicops.CreateItem{Issue: &types.Issue{ID: "bd-aa", Title: "b"}}},
	}}
	if _, _, err := ApplyBatchInTx(context.Background(), tx, plan); err == nil {
		t.Fatal("ApplyBatchInTx() = nil; the mock has nothing after the lock, so the first item must fail")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRetainingSweepCoordinatesBeforeItDeletes pins the sweep half of the
// lock: the tombstone plan a retaining sweep builds BEFORE its delete starts
// by locking the shard of every id it will retire. An upsert that probed
// first holds the cell, so the SERIALIZABLE sweep fails and retries and sees
// the upserted row; an upsert that comes second waits for the sweep's commit.
func TestRetainingSweepCoordinatesBeforeItDeletes(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	closedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	kept := []*types.Issue{{ID: "bd-2", ClosedAt: &closedAt}, {ID: "bd-1", ClosedAt: &closedAt}}
	expectCoordination(mock, coordinationKeysFor("bd-1", "bd-2"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).WillReturnRows(sqlmock.NewRows([]string{"1"}))
	wisp, durable, err := sweepRetiredIDsInTx(context.Background(), db, kept)
	if err != nil {
		t.Fatalf("sweepRetiredIDsInTx() = %v", err)
	}
	if len(wisp) != 0 || len(durable) != 2 {
		t.Fatalf("sweepRetiredIDsInTx() = %v wisp, %v durable; want both durable", wisp, durable)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestPersistenceMoveCoordinatesBeforeItLocatesTheRow pins the move's lock
// order: a persistence move re-inserts the row into the other plane, so it
// takes the id's shard cell before it reads where the row lives. A move that
// came second behind a retaining sweep of the same row then reads after the
// sweep's commit; one that came first makes the sweep conflict and retry.
func TestPersistenceMoveCoordinatesBeforeItLocatesTheRow(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	expectCoordination(mock, coordinationKeysFor("bd-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM wisps WHERE id = ?")).WithArgs("bd-1").
		WillReturnError(context.Canceled)
	current := &types.Issue{ID: "bd-1", Status: types.StatusClosed}
	if _, err := MoveIssuePersistenceInTx(context.Background(), db, current, types.PersistenceModeEphemeral, "actor"); err == nil {
		t.Fatal("MoveIssuePersistenceInTx() = nil; the mock fails the plane read")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestPersistenceMoveOfAVanishedRowIsNotFound pins that a move whose row is in
// neither plane once it holds the cell — a retaining sweep deleted it first —
// refuses with ErrNotFound and writes nothing, instead of inserting the
// caller's earlier snapshot into the target plane beside the new tombstone.
func TestPersistenceMoveOfAVanishedRowIsNotFound(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	expectCoordination(mock, coordinationKeysFor("bd-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM wisps WHERE id = ?")).WithArgs("bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM issues WHERE id = ?")).WithArgs("bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	current := &types.Issue{ID: "bd-1", Status: types.StatusClosed}
	_, err := MoveIssuePersistenceInTx(context.Background(), db, current, types.PersistenceModeEphemeral, "actor")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("MoveIssuePersistenceInTx(vanished row) = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestPromoteCoordinatesBeforeItReadsTheWisp pins the same order on wisp
// promotion, which re-inserts the wisp into the issues plane.
func TestPromoteCoordinatesBeforeItReadsTheWisp(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	expectCoordination(mock, coordinationKeysFor("bd-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).WithArgs("bd-1").
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	if err := PromoteFromEphemeralInTx(context.Background(), db, "bd-1", "actor"); err == nil {
		t.Fatal("PromoteFromEphemeralInTx() = nil; the mock ends after the wisp probe")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRenameOntoARetiredIDIsRefusedBeforeItWrites pins the rename guard: the
// identity change takes the NEW id's cell and probes it before anything else,
// so a rename onto a retired id answers *IdempotencyExpiredError and writes
// nothing — not the row, not the journal capture, not the edges.
func TestRenameOntoARetiredIDIsRefusedBeforeItWrites(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	mock.ExpectBegin()
	expectCoordination(mock, coordinationKeysFor("bd-new"))
	mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).WithArgs("bd-new", "bd-new", "bd-new", "bd-new").
		WillReturnRows(sqlmock.NewRows([]string{"src", "closed_at"}).AddRow("id_tombstones", idRetentionClosedAt))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = UpdateIssueIDInTx(context.Background(), tx, "bd-old", "bd-new", &types.Issue{ID: "bd-old", Title: "t"}, "actor")
	var expired *publicops.IdempotencyExpiredError
	if !errors.As(err, &expired) || expired.ID != "bd-new" || !expired.ClosedAt.Equal(idRetentionClosedAt) {
		t.Fatalf("UpdateIssueIDInTx(onto retired) = %v, want *IdempotencyExpiredError naming bd-new", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
