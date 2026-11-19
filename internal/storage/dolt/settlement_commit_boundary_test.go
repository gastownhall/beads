package dolt

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
)

func TestServerSettlementSQLCommitResponseLossIsIndeterminate(t *testing.T) {
	t.Run("pull merge settlement", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		mock.ExpectBegin()
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
			WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
			WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0")).
			WillReturnRows(sqlmock.NewRows([]string{"table"}))
		responseLoss := errors.New("pull settlement commit response lost")
		mock.ExpectCommit().WillReturnError(responseLoss)

		store := &DoltStore{db: db}
		err = store.settleMergeInTx(t.Context(), tx, nil)
		assertPublicCommitIndeterminate(t, err, responseLoss)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("full blocked recompute", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT DISTINCT table_name FROM dolt_status WHERE table_name IN (?,?)")).
			WithArgs("issues", "dependencies").
			WillReturnRows(sqlmock.NewRows([]string{"table_name"}))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM issues")).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM wisps")).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		responseLoss := errors.New("full recompute commit response lost")
		mock.ExpectCommit().WillReturnError(responseLoss)

		store := &DoltStore{db: db}
		changed, err := store.recomputeAllBlockedWithDB(t.Context(), db)
		if changed != 0 {
			t.Fatalf("recomputeAllBlockedWithDB() changed = %d, want 0", changed)
		}
		assertPublicCommitIndeterminate(t, err, responseLoss)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("scoped blocked recompute", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })

		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT value FROM metadata WHERE `key` = ?")).
			WithArgs("is_blocked_recompute_pending").
			WillReturnRows(sqlmock.NewRows([]string{"value"}))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM issues")).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM wisps")).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		responseLoss := errors.New("scoped recompute commit response lost")
		mock.ExpectCommit().WillReturnError(responseLoss)

		store := &DoltStore{db: db}
		err = store.recomputeBlockedTxWithDB(t.Context(), db, "")
		assertPublicCommitIndeterminate(t, err, responseLoss)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("CLI merge settlement", func(t *testing.T) {
		pool, poolMock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		t.Cleanup(func() { _ = pool.Close() })
		session := cliMergeSession(t)

		poolMock.ExpectQuery("SELECT active_branch()").WillReturnRows(activeBranchRow("main"))
		session.ExpectQuery("SELECT active_branch()").WillReturnRows(activeBranchRow("default"))
		session.ExpectExec("CALL DOLT_CHECKOUT(?)").WithArgs("main").WillReturnResult(sqlmock.NewResult(0, 0))
		session.ExpectExec("SET @@dolt_force_transaction_commit = 1").WillReturnResult(sqlmock.NewResult(0, 0))
		session.ExpectExec("SET @@dolt_allow_commit_conflicts = 1").WillReturnResult(sqlmock.NewResult(0, 0))
		session.ExpectBegin()
		session.ExpectQuery("CALL DOLT_MERGE(?)").WithArgs("origin/main").WillReturnRows(doltMergeRow(0))
		cliMergeSettleReads(session, "")
		responseLoss := errors.New("CLI merge settlement commit response lost")
		session.ExpectCommit().WillReturnError(responseLoss)
		session.ExpectClose()

		store := &DoltStore{db: pool, branch: "main"}
		err = store.mergeTrackingRefAfterCLIFetch(t.Context(), "origin/main")
		if !strings.Contains(err.Error(), "merge origin/main:") {
			t.Fatalf("CLI merge settlement error %v does not name the ref", err)
		}
		assertPublicCommitIndeterminate(t, err, responseLoss)
		if err := session.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet session expectations: %v", err)
		}
		if err := poolMock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet pool expectations: %v", err)
		}
	})
}

func TestCommitWorkingSetAfterSQLCommitResponseLossIsIndeterminate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(sqlmock.Sqlmock, error)
	}{
		{
			name: "DOLT_ADD",
			setup: func(mock sqlmock.Sqlmock, responseLoss error) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT s.table_name FROM dolt_status")).
					WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("issues"))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
					WithArgs("issues").
					WillReturnError(responseLoss)
			},
		},
		{
			name: "DOLT_COMMIT",
			setup: func(mock sqlmock.Sqlmock, responseLoss error) {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT s.table_name FROM dolt_status")).
					WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("issues"))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).
					WithArgs("issues").
					WillReturnRows(sqlmock.NewRows([]string{"status"}))
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
					WithArgs("bd: recompute is_blocked after pull", " <>").
					WillReturnError(responseLoss)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			responseLoss := errors.New(tc.name + " response lost after SQL commit")
			tc.setup(mock, responseLoss)
			store := &DoltStore{db: db}
			err = store.commitWorkingSetAfterSQLCommit(t.Context(), "bd: recompute is_blocked after pull", configExclude)
			assertPublicCommitIndeterminate(t, err, responseLoss)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sqlmock expectations: %v", err)
			}
		})
	}
}

func assertPublicCommitIndeterminate(t *testing.T, err, cause error) {
	t.Helper()
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want cause %v", err, cause)
	}
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("error = %v, want storage.ErrCommitIndeterminate", err)
	}
}
