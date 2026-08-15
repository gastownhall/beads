package issueops

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func newBlockedStateResultMock(t *testing.T) (sqlmock.Sqlmock, DBTX) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sqlmock expectations: %v", err)
		}
		_ = db.Close()
	})
	return mock, db
}

func expectBlockedStatePass(mock sqlmock.Sqlmock, table, alias string, mark, unmark driver.Result) {
	mock.ExpectExec(regexp.QuoteMeta("UPDATE " + table + " " + alias + " SET " + alias + ".is_blocked = 1")).
		WillReturnResult(mark)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE " + table + " " + alias + " SET " + alias + ".is_blocked = 0")).
		WillReturnResult(unmark)
}

func TestRecomputeIsBlockedInTxWithResult(t *testing.T) {
	t.Run("issue rows changed", func(t *testing.T) {
		mock, db := newBlockedStateResultMock(t)
		expectBlockedStatePass(mock, "issues", "i", sqlmock.NewResult(0, 1), sqlmock.NewResult(0, 0))
		expectBlockedStatePass(mock, "issues", "i", sqlmock.NewResult(0, 0), sqlmock.NewResult(0, 0))

		result, err := RecomputeIsBlockedInTxWithResult(context.Background(), db, []string{"issue-1"}, nil)
		if err != nil {
			t.Fatalf("RecomputeIsBlockedInTxWithResult: %v", err)
		}
		if !result.IssueRowsChanged || result.WispRowsChanged {
			t.Fatalf("result = %+v, want issue change only", result)
		}
	})

	t.Run("wisp rows changed", func(t *testing.T) {
		mock, db := newBlockedStateResultMock(t)
		expectBlockedStatePass(mock, "wisps", "w", sqlmock.NewResult(0, 0), sqlmock.NewResult(0, 1))
		expectBlockedStatePass(mock, "wisps", "w", sqlmock.NewResult(0, 0), sqlmock.NewResult(0, 0))

		result, err := RecomputeIsBlockedInTxWithResult(context.Background(), db, nil, []string{"wisp-1"})
		if err != nil {
			t.Fatalf("RecomputeIsBlockedInTxWithResult: %v", err)
		}
		if result.IssueRowsChanged || !result.WispRowsChanged {
			t.Fatalf("result = %+v, want wisp change only", result)
		}
	})

	t.Run("no rows changed", func(t *testing.T) {
		mock, db := newBlockedStateResultMock(t)
		expectBlockedStatePass(mock, "issues", "i", sqlmock.NewResult(0, 0), sqlmock.NewResult(0, 0))
		expectBlockedStatePass(mock, "wisps", "w", sqlmock.NewResult(0, 0), sqlmock.NewResult(0, 0))

		result, err := RecomputeIsBlockedInTxWithResult(
			context.Background(), db, []string{"issue-1"}, []string{"wisp-1"},
		)
		if err != nil {
			t.Fatalf("RecomputeIsBlockedInTxWithResult: %v", err)
		}
		if result.IssueRowsChanged || result.WispRowsChanged {
			t.Fatalf("result = %+v, want no changes", result)
		}
	})
}

func TestRunMarkUnmarkBatchedInTxPropagatesRowsAffectedErrors(t *testing.T) {
	sentinel := errors.New("rows affected unavailable")
	for _, phase := range []string{"mark", "unmark"} {
		t.Run(phase, func(t *testing.T) {
			mock, db := newBlockedStateResultMock(t)
			if phase == "mark" {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE issues i SET i.is_blocked = 1")).
					WillReturnResult(sqlmock.NewErrorResult(sentinel))
			} else {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE issues i SET i.is_blocked = 1")).
					WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec(regexp.QuoteMeta("UPDATE issues i SET i.is_blocked = 0")).
					WillReturnResult(sqlmock.NewErrorResult(sentinel))
			}

			_, err := runMarkUnmarkBatchedInTx(
				context.Background(), db, markBlockedTemplateForIssues(), unmarkBlockedTemplateForIssues(), []string{"issue-1"},
			)
			if !errors.Is(err, sentinel) {
				t.Fatalf("err = %v, want rows-affected error", err)
			}
		})
	}
}
