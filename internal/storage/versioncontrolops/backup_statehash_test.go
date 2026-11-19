package versioncontrolops

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestStateHashWithHeadFallback pins both legs of the working-set-aware hash
// auto-export change detection uses: DOLT_HASHOF_DB() when the server answers
// it, and the HEAD commit hash when it does not (servers that predate the
// function). It is the conn-level twin of DoltStore.GetStateHash, extracted
// so the proxied-server maintenance path degrades identically (bd-todbq7) —
// the fallback is the leg that keeps auto-export from going permanently
// quiet on an old server, so a regression here is a silent staleness bug.
func TestStateHashWithHeadFallback(t *testing.T) {
	ctx := context.Background()

	t.Run("state hash when the server has it", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery(regexp.QuoteMeta("SELECT DOLT_HASHOF_DB()")).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("workroot1"))

		got, err := StateHashWithHeadFallback(ctx, db)
		if err != nil {
			t.Fatalf("StateHashWithHeadFallback: %v", err)
		}
		if got != "workroot1" {
			t.Fatalf("hash = %q, want workroot1 (the working-root hash, not HEAD)", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("head fallback on a server without DOLT_HASHOF_DB", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		mock.ExpectQuery(regexp.QuoteMeta("SELECT DOLT_HASHOF_DB()")).
			WillReturnError(context.DeadlineExceeded)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT DOLT_HASHOF('HEAD')")).
			WillReturnRows(sqlmock.NewRows([]string{"hash"}).AddRow("headcommit1"))

		got, err := StateHashWithHeadFallback(ctx, db)
		if err != nil {
			t.Fatalf("StateHashWithHeadFallback: %v", err)
		}
		if got != "headcommit1" {
			t.Fatalf("hash = %q, want headcommit1 (HEAD after the state-hash failure)", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}
