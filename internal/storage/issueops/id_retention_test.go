package issueops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func newIDRetentionMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

var idRetentionClosedAt = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// TestEnsureIssueIDAvailableReadsOccupancyInOneStatement pins the guard's
// shape: the shard coordination write, then ONE statement across both row
// tables and both tombstone tables (one snapshot on every isolation level),
// answered as live, retired, or free — a live row winning over a tombstone.
func TestEnsureIssueIDAvailableReadsOccupancyInOneStatement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rows    *sqlmock.Rows
		wantErr func(error) bool
	}{
		{"free", sqlmock.NewRows([]string{"src", "closed_at"}), func(err error) bool { return err == nil }},
		{
			"live",
			sqlmock.NewRows([]string{"src", "closed_at"}).AddRow("wisps", nil),
			func(err error) bool { return errors.Is(err, storage.ErrAlreadyExists) },
		},
		{
			"retired",
			sqlmock.NewRows([]string{"src", "closed_at"}).AddRow("id_tombstones", idRetentionClosedAt),
			func(err error) bool {
				var expired *publicops.IdempotencyExpiredError
				return errors.As(err, &expired) && expired.ID == "bd-1" && expired.ClosedAt.Equal(idRetentionClosedAt) &&
					!errors.Is(err, storage.ErrAlreadyExists)
			},
		},
		{
			"live wins over a tombstone",
			sqlmock.NewRows([]string{"src", "closed_at"}).
				AddRow("wisp_id_tombstones", idRetentionClosedAt).
				AddRow("issues", idRetentionClosedAt),
			func(err error) bool {
				return errors.Is(err, storage.ErrAlreadyExists) && !errors.Is(err, publicops.ErrIdempotencyExpired)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newIDRetentionMock(t)
			mock.ExpectExec(regexp.QuoteMeta("REPLACE INTO local_metadata")).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).
				WithArgs("bd-1", "bd-1", "bd-1", "bd-1").
				WillReturnRows(tc.rows)
			err := EnsureIssueIDAvailableInTx(context.Background(), db, "bd-1")
			if !tc.wantErr(err) {
				t.Fatalf("EnsureIssueIDAvailableInTx() = %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestRefuseRetiredIDIgnoresALiveRow pins the upsert guard: it refuses only a
// retired id with no live row, leaving a live id to the upsert it guards.
func TestRefuseRetiredIDIgnoresALiveRow(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	expectCoordination(mock, coordinationKeysFor("bd-1"))
	mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).
		WillReturnRows(sqlmock.NewRows([]string{"src", "closed_at"}).AddRow("id_tombstones", idRetentionClosedAt).AddRow("issues", nil))
	if err := RefuseRetiredIDInTx(context.Background(), db, "bd-1"); err != nil {
		t.Fatalf("RefuseRetiredIDInTx(live and retired) = %v, want nil", err)
	}
	expectCoordination(mock, coordinationKeysFor("bd-1"))
	mock.ExpectQuery(regexp.QuoteMeta(idOccupancySQL)).
		WillReturnRows(sqlmock.NewRows([]string{"src", "closed_at"}).AddRow("wisp_id_tombstones", idRetentionClosedAt))
	if err := RefuseRetiredIDInTx(context.Background(), db, "bd-1"); !errors.Is(err, publicops.ErrIdempotencyExpired) {
		t.Fatalf("RefuseRetiredIDInTx(retired) = %v, want ErrIdempotencyExpired", err)
	}
	if err := RefuseRetiredIDInTx(context.Background(), db, ""); err != nil {
		t.Fatalf("RefuseRetiredIDInTx(\"\") = %v, want nil without a read", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func retiredIDs(prefix string, n int) []RetiredID {
	out := make([]RetiredID, n)
	for i := range out {
		out[i] = RetiredID{ID: fmt.Sprintf("%s-%d", prefix, i), ClosedAt: idRetentionClosedAt.Add(500 * time.Millisecond)}
	}
	return out
}

// TestRetireIDsRoutesEachIDToItsPlaneAndChunks pins the write: wisp ids into
// the clone-local table, durable ids into the versioned one, INSERT IGNORE so
// a re-retire is a no-op, closed_at truncated to the second so two clones
// write the same row, and multi-row statements of at most queryBatchSize.
func TestRetireIDsRoutesEachIDToItsPlaneAndChunks(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	wisp := retiredIDs("bd-wisp", 2)
	durable := retiredIDs("bd", queryBatchSize+1)
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO wisp_id_tombstones (id, closed_at) VALUES (?, ?), (?, ?)")).
		WithArgs("bd-wisp-0", idRetentionClosedAt, "bd-wisp-1", idRetentionClosedAt).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO id_tombstones (id, closed_at) VALUES " + strings.Repeat("(?, ?), ", queryBatchSize-1) + "(?, ?)")).
		WillReturnResult(sqlmock.NewResult(0, queryBatchSize))
	mock.ExpectExec(regexp.QuoteMeta("INSERT IGNORE INTO id_tombstones (id, closed_at) VALUES (?, ?)")).
		WithArgs(fmt.Sprintf("bd-%d", queryBatchSize), idRetentionClosedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := RetireIDsInTx(context.Background(), db, wisp, durable); err != nil {
		t.Fatalf("RetireIDsInTx() = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRetireIDsRefusesATombstoneWithoutItsFacts pins that a tombstone is never
// written without an id or a closed_at: either would be a different row on
// every clone that wrote it.
func TestRetireIDsRefusesATombstoneWithoutItsFacts(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	if err := RetireIDsInTx(context.Background(), db, nil, []RetiredID{{ID: "bd-1"}}); err == nil {
		t.Fatal("RetireIDsInTx(zero closed_at) = nil, want an error")
	}
	if err := RetireIDsInTx(context.Background(), db, []RetiredID{{ClosedAt: idRetentionClosedAt}}, nil); err == nil {
		t.Fatal("RetireIDsInTx(empty id) = nil, want an error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("a refused retire touched the database: %v", err)
	}
}

// TestRetiredIDsReadsBothTablesInChunks pins the batch read the create cache
// uses: both tombstone tables, IN-lists of at most queryBatchSize ids, the
// first table's answer winning for an id in both.
func TestRetiredIDsReadsBothTablesInChunks(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	ids := make([]string, queryBatchSize+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("bd-%d", i)
	}
	for _, table := range IDTombstoneTables() {
		rows := sqlmock.NewRows([]string{"id", "closed_at"})
		if table == IDTombstoneTable {
			rows.AddRow("bd-3", idRetentionClosedAt)
		} else {
			rows.AddRow("bd-3", idRetentionClosedAt.Add(time.Hour))
		}
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id, closed_at FROM " + table + " WHERE id IN (" + strings.Repeat("?,", queryBatchSize-1) + "?)")).WillReturnRows(rows)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id, closed_at FROM " + table + " WHERE id IN (?)")).
			WithArgs(fmt.Sprintf("bd-%d", queryBatchSize)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "closed_at"}))
	}
	retired, err := RetiredIDsInTx(context.Background(), db, ids)
	if err != nil {
		t.Fatalf("RetiredIDsInTx() = %v", err)
	}
	if len(retired) != 1 || !retired["bd-3"].Equal(idRetentionClosedAt) {
		t.Fatalf("RetiredIDsInTx() = %v, want only bd-3 at %s", retired, idRetentionClosedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGenerateIssueIDSkipsARetiredCandidate pins the hash-mode generator: the
// tombstone check rides the one collision read it already made, and a
// candidate it reports taken is passed over for the next nonce.
func TestGenerateIssueIDSkipsARetiredCandidate(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	issue := &types.Issue{Title: "t", Description: "d", CreatedAt: idRetentionClosedAt}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
		WithArgs("issue_id_mode").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("adaptive length unavailable"))
	taken := regexp.QuoteMeta("SELECT (SELECT COUNT(*) FROM issues WHERE id = ?) + (SELECT COUNT(*) FROM id_tombstones WHERE id = ?) + (SELECT COUNT(*) FROM wisp_id_tombstones WHERE id = ?)")
	mock.ExpectQuery(taken).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery(taken).WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", issue, "actor")
	if err != nil {
		t.Fatalf("GenerateIssueIDInTable() = %v", err)
	}
	if !strings.HasPrefix(id, "bd-") {
		t.Fatalf("GenerateIssueIDInTable() = %q", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGenerateIssueIDCounterModeWalksPastARetiredValue pins counter mode: a
// counter value a sweep retired is skipped and the counter advanced again, and
// each candidate costs one tombstone read, not one per tombstone table.
func TestGenerateIssueIDCounterModeWalksPastARetiredValue(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM config WHERE `key` = ?")).
		WithArgs("issue_id_mode").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow("counter"))
	expectCounter := func(next int, retired bool) {
		mock.ExpectExec(regexp.QuoteMeta("UPDATE issue_counter SET last_id = last_id + 1 WHERE prefix = ?")).
			WithArgs("bd").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT last_id FROM issue_counter WHERE prefix = ?")).
			WithArgs("bd").WillReturnRows(sqlmock.NewRows([]string{"last_id"}).AddRow(next))
		id := fmt.Sprintf("bd-%d", next)
		count := 0
		if retired {
			count = 1
		}
		// One statement per candidate reads both tombstone tables.
		mock.ExpectQuery(regexp.QuoteMeta(idRetiredSQL)).WithArgs(id, id).
			WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(count))
	}
	expectCounter(5, true)
	expectCounter(6, false)
	id, err := GenerateIssueIDInTable(context.Background(), db, "issues", "bd", &types.Issue{}, "actor")
	if err != nil {
		t.Fatalf("GenerateIssueIDInTable(counter) = %v", err)
	}
	if id != "bd-6" {
		t.Fatalf("GenerateIssueIDInTable(counter) = %q, want bd-6 past the retired bd-5", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestIDTakenRefusesAnUnknownTable pins that the one formatted table name is
// checked rather than trusted.
func TestIDTakenRefusesAnUnknownTable(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	if _, err := IDTakenInTx(context.Background(), db, "issues; DROP TABLE x", "bd-1"); err == nil {
		t.Fatal("IDTakenInTx(unknown table) = nil error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestGetNextChildIDCountsARetiredChildInTheHighWaterRead pins the child id
// mint: the parent's live children and both tombstone tables are read in ONE
// statement (one snapshot on every isolation level), and a retired suffix
// above both the counter and every live child is taken, so the mint passes it.
func TestGetNextChildIDCountsARetiredChildInTheHighWaterRead(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs("bd-p").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT last_child FROM child_counters WHERE parent_id = ?")).
		WithArgs("bd-p").WillReturnRows(sqlmock.NewRows([]string{"last_child"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta(fmt.Sprintf(usedChildIDsSQL, "issues", IDTombstoneTable, WispIDTombstoneTable))).
		WithArgs("bd-p", "bd-p", "bd-p", "bd-p", "bd-p", "bd-p").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("bd-p.2").AddRow("bd-p.4"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)")).
		WithArgs("bd-p", 5, 5).WillReturnResult(sqlmock.NewResult(0, 1))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	id, err := GetNextChildIDTx(context.Background(), tx, "bd-p")
	if err != nil {
		t.Fatalf("GetNextChildIDTx() = %v", err)
	}
	if id != "bd-p.5" {
		t.Fatalf("GetNextChildIDTx() = %q, want bd-p.5 past the retired bd-p.4", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestUsedChildIDsRefusesAnUnknownTable pins that the one formatted row table
// is checked rather than trusted.
func TestUsedChildIDsRefusesAnUnknownTable(t *testing.T) {
	db, mock := newIDRetentionMock(t)
	if _, err := UsedChildIDsInTx(context.Background(), db, "issues; DROP TABLE x", "bd-p"); err == nil {
		t.Fatal("UsedChildIDsInTx(unknown table) = nil error")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
