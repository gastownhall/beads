package versioncontrolops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// GH#7890: the excluded-type DELETE is the heart of the federation filter, so
// it must fail closed: swallowing an error would publish the unfiltered tip,
// the leak the filter exists to prevent. These tests pin the fail-closed
// contract and the rows-affected and wisp SQL mapping of deleteExcludedIssues,
// and the peer-held limit notHeldByPeers adds to it.

var (
	matchDeleteByIssueType = regexp.QuoteMeta("DELETE FROM issues WHERE issue_type = ?")
	matchDeleteEphemeral   = regexp.QuoteMeta("DELETE FROM issues WHERE ephemeral = 1")
)

func newDeleteExcludedMock(t *testing.T) (DBConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func TestDeleteExcludedIssuesFailClosedOnError(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)

	// The DELETE fails mid-flight (dropped connection, lock timeout, ...).
	deleteErr := errors.New("Error 1317: query execution was interrupted")
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnError(deleteErr)

	_, err := deleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"}, "")
	if !errors.Is(err, deleteErr) {
		t.Fatalf("deleteExcludedIssues error = %v, want the DELETE failure wrapped (fail closed)", err)
	}
	if !strings.Contains(err.Error(), "delete excluded type chore") {
		t.Errorf("error %q does not name the failing exclude type", err)
	}
	// The wisp DELETE must never be issued after the failure: sqlmock fails
	// an unexpected Exec, and ExpectationsWereMet proves the chore one ran.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

// A failure partway through the exclude list still aborts: the rows already
// deleted are gone only from the temporary staging branch, which the caller
// discards once the error propagates.
func TestDeleteExcludedIssuesFailClosedMidList(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)

	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewResult(0, 2))
	deleteErr := errors.New("Error 1205: lock wait timeout exceeded")
	mock.ExpectExec(matchDeleteEphemeral).
		WillReturnError(deleteErr)

	n, err := deleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"}, "")
	if !errors.Is(err, deleteErr) {
		t.Fatalf("deleteExcludedIssues error = %v, want the wisp DELETE failure wrapped", err)
	}
	if n != 0 {
		t.Errorf("removed = %d on failure, want 0", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

// An unreadable rows-affected count fails closed too: the caller could not
// tell whether anything was filtered.
func TestDeleteExcludedIssuesFailClosedOnRowsAffectedError(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)

	rowsErr := errors.New("rows affected unavailable")
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewErrorResult(rowsErr))

	if _, err := deleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"}, ""); !errors.Is(err, rowsErr) {
		t.Fatalf("deleteExcludedIssues error = %v, want the RowsAffected failure wrapped", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

func TestDeleteExcludedIssuesReportsRowsAffected(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)

	// chore deletes two rows; the wisp safety net (ephemeral = 1) deletes one.
	// The expectations double as the SQL mapping assertion: a literal
	// issue_type = 'wisp' DELETE would not match the ephemeral expectation.
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(matchDeleteEphemeral).
		WillReturnResult(sqlmock.NewResult(0, 1))

	n, err := deleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"}, "")
	if err != nil {
		t.Fatalf("deleteExcludedIssues: %v", err)
	}
	if n != 3 {
		t.Errorf("removed = %d, want 3", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

func TestDeleteExcludedIssuesNothingMatchedReportsZero(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)

	// Nothing to filter: the caller must not mint the filtered commit or its
	// revert, and pushes the ordinary tip.
	mock.ExpectExec(matchDeleteByIssueType).
		WithArgs("chore").
		WillReturnResult(sqlmock.NewResult(0, 0))

	n, err := deleteExcludedIssues(context.Background(), db, []string{"chore"}, "")
	if err != nil {
		t.Fatalf("deleteExcludedIssues: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0 when no rows matched", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

const (
	peerTipHash  = "0123456789abcdefghijklmnopqrstuv"
	otherTipHash = "vutsrqponmlkjihgfedcba9876543210"
)

var (
	matchPeerList      = regexp.QuoteMeta("SELECT name FROM dolt_remotes")
	matchPeerTipLookup = regexp.QuoteMeta("SELECT hash FROM dolt_remote_branches WHERE name = ?")
)

// expectRemotes answers the remote listing with names, in that order.
func expectRemotes(mock sqlmock.Sqlmock, names ...string) {
	rows := sqlmock.NewRows([]string{"name"})
	for _, name := range names {
		rows.AddRow(name)
	}
	mock.ExpectQuery(matchPeerList).WillReturnRows(rows)
}

// expectPeerTip answers the lookup of ref with hash, or with no row when
// hash is empty (a remote never fetched).
func expectPeerTip(mock sqlmock.Sqlmock, ref, hash string) {
	rows := sqlmock.NewRows([]string{"hash"})
	if hash != "" {
		rows.AddRow(hash)
	}
	mock.ExpectQuery(matchPeerTipLookup).WithArgs(ref).WillReturnRows(rows)
}

func notHeldAt(hash string) string {
	return " AND id NOT IN (SELECT id FROM issues AS OF '" + hash + "')"
}

// The filter withholds only what no peer's fetched copy holds: deleting an
// excluded issue a peer holds would delete it from that peer when the tip
// reaches it, directly or through another peer. The destination's copy comes
// first, then every other remote's but origin's, which bd dolt push fills
// with everything. Each copy is read by commit hash, so a remote name never
// reaches the SQL text.
func TestNotHeldByPeersLimitsTheDeleteToIssuesNoPeerHolds(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)
	expectRemotes(mock, "origin", "town-c", "peer", "town-b")
	expectPeerTip(mock, "remotes/peer/main", peerTipHash)
	expectPeerTip(mock, "remotes/town-b/main", otherTipHash)
	expectPeerTip(mock, "remotes/town-c/main", "")
	notHeld := regexp.QuoteMeta(notHeldAt(peerTipHash) + notHeldAt(otherTipHash))
	mock.ExpectExec(matchDeleteByIssueType + notHeld + "$").
		WithArgs("chore").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(matchDeleteEphemeral + notHeld + "$").
		WillReturnResult(sqlmock.NewResult(0, 0))

	push := &FilteredPush{Source: "main", Peer: "peer"}
	clause, err := push.notHeldByPeers(context.Background(), db)
	if err != nil {
		t.Fatalf("notHeldByPeers: %v", err)
	}
	n, err := deleteExcludedIssues(context.Background(), db, []string{"chore", "wisp"}, clause)
	if err != nil {
		t.Fatalf("deleteExcludedIssues: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

// bd federation sync --peer origin pushes to origin: the destination's own
// copy counts even then, and no other copy is read twice.
func TestNotHeldByPeersReadsTheDestinationEvenWhenItIsOrigin(t *testing.T) {
	db, mock := newDeleteExcludedMock(t)
	expectRemotes(mock, "peer", "origin")
	expectPeerTip(mock, "remotes/origin/main", peerTipHash)
	expectPeerTip(mock, "remotes/peer/main", otherTipHash)

	clause, err := (&FilteredPush{Source: "main", Peer: "origin"}).notHeldByPeers(context.Background(), db)
	if err != nil {
		t.Fatalf("notHeldByPeers: %v", err)
	}
	if want := notHeldAt(peerTipHash) + notHeldAt(otherTipHash); clause != want {
		t.Errorf("notHeldByPeers = %q, want %q", clause, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected calls against the fake conn: %v", err)
	}
}

// With no peer copy to consult, every excluded issue is withheld.
func TestNotHeldByPeersWithoutAPeerCopyLimitsNothing(t *testing.T) {
	t.Run("no remote", func(t *testing.T) {
		db, mock := newDeleteExcludedMock(t)
		expectRemotes(mock)
		clause, err := (&FilteredPush{Source: "main"}).notHeldByPeers(context.Background(), db)
		if err != nil || clause != "" {
			t.Fatalf("notHeldByPeers = %q, %v; want no limit", clause, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unexpected calls against the fake conn: %v", err)
		}
	})
	t.Run("no peer branch fetched", func(t *testing.T) {
		db, mock := newDeleteExcludedMock(t)
		expectRemotes(mock, "peer", "origin", "town-b")
		expectPeerTip(mock, "remotes/peer/main", "")
		expectPeerTip(mock, "remotes/town-b/main", "")
		clause, err := (&FilteredPush{Source: "main", Peer: "peer"}).notHeldByPeers(context.Background(), db)
		if err != nil || clause != "" {
			t.Fatalf("notHeldByPeers = %q, %v; want no limit", clause, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unexpected calls against the fake conn: %v", err)
		}
	})
}

// A peer copy, or the list of peers, that cannot be read fails the push
// rather than guessing which rows the peers hold.
func TestNotHeldByPeersFailsClosed(t *testing.T) {
	push := &FilteredPush{Source: "main", Peer: "peer"}
	t.Run("remote listing error", func(t *testing.T) {
		db, mock := newDeleteExcludedMock(t)
		listErr := errors.New("Error 1105: connection reset")
		mock.ExpectQuery(matchPeerList).WillReturnError(listErr)
		if _, err := push.notHeldByPeers(context.Background(), db); !errors.Is(err, listErr) {
			t.Fatalf("notHeldByPeers error = %v, want the listing failure", err)
		}
	})
	t.Run("remote listing broken mid-read", func(t *testing.T) {
		db, mock := newDeleteExcludedMock(t)
		rowErr := errors.New("Error 1105: connection reset")
		mock.ExpectQuery(matchPeerList).WillReturnRows(
			sqlmock.NewRows([]string{"name"}).AddRow("peer").AddRow("town-b").RowError(1, rowErr))
		if _, err := push.notHeldByPeers(context.Background(), db); !errors.Is(err, rowErr) {
			t.Fatalf("notHeldByPeers error = %v, want the listing failure", err)
		}
	})
	t.Run("lookup error", func(t *testing.T) {
		db, mock := newDeleteExcludedMock(t)
		expectRemotes(mock, "peer", "town-b")
		lookupErr := errors.New("Error 1105: connection reset")
		expectPeerTip(mock, "remotes/peer/main", peerTipHash)
		mock.ExpectQuery(matchPeerTipLookup).WithArgs("remotes/town-b/main").WillReturnError(lookupErr)
		if _, err := push.notHeldByPeers(context.Background(), db); !errors.Is(err, lookupErr) {
			t.Fatalf("notHeldByPeers error = %v, want the lookup failure", err)
		}
	})
	t.Run("not a commit hash", func(t *testing.T) {
		db, mock := newDeleteExcludedMock(t)
		expectRemotes(mock, "peer", "town-b")
		expectPeerTip(mock, "remotes/peer/main", peerTipHash)
		expectPeerTip(mock, "remotes/town-b/main", "x' OR '1'='1")
		if _, err := push.notHeldByPeers(context.Background(), db); err == nil {
			t.Fatal("notHeldByPeers accepted a value that is not a commit hash")
		}
	})
}
