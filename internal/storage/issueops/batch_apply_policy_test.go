package issueops

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage"
)

func TestApplyClosePolicyRefusesOpenExternallyBlockedIssue(t *testing.T) {
	const id = "be-consumer"
	policy := storage.NewBatchClosePolicy(map[string][]string{id: {"external:p:c"}})
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM issues WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("open"))

	err := applyClosePolicy(context.Background(), tx, policy, id, false)
	if !errors.Is(err, storage.ErrCloseBlocked) {
		t.Fatalf("applyClosePolicy = %v, want ErrCloseBlocked", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestApplyClosePolicyAllowsForceWithoutReading(t *testing.T) {
	const id = "be-consumer"
	policy := storage.NewBatchClosePolicy(map[string][]string{id: {"external:p:c"}})
	_, mock, tx := beginMockTx(t)

	if err := applyClosePolicy(context.Background(), tx, policy, id, true); err != nil {
		t.Fatalf("applyClosePolicy with Force = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected SQL: %v", err)
	}
}

func TestApplyClosePolicyLeavesIdempotentReCloseAlone(t *testing.T) {
	const id = "be-consumer"
	policy := storage.NewBatchClosePolicy(map[string][]string{id: {"external:p:c"}})
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM issues WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("closed"))

	if err := applyClosePolicy(context.Background(), tx, policy, id, false); err != nil {
		t.Fatalf("applyClosePolicy on closed issue = %v, want nil", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestApplyClosePolicyLeavesMissingIssueToNotFound(t *testing.T) {
	const id = "be-missing"
	policy := storage.NewBatchClosePolicy(map[string][]string{id: {"external:p:c"}})
	_, mock, tx := beginMockTx(t)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM issues WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM wisps WHERE id = ?")).
		WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"status"}))

	if err := applyClosePolicy(context.Background(), tx, policy, id, false); err != nil {
		t.Fatalf("applyClosePolicy on missing issue = %v, want nil so the backend reports not-found", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
