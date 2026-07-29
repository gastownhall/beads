package doctor

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

func TestDoctorDependencyTargetExprMatchesCanonical(t *testing.T) {
	if doctorDependencyTargetExpr != issueops.DepTargetExpr {
		t.Fatalf("doctorDependencyTargetExpr = %q, want %q", doctorDependencyTargetExpr, issueops.DepTargetExpr)
	}
}

func TestCheckMailThreadIntegrityRequiresBothDependencyThreadColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("TABLE_NAME IN ('dependencies', 'wisp_dependencies')")).
		WillReturnRows(sqlmock.NewRows([]string{"has_thread_id"}).AddRow(false))

	check := checkMailThreadIntegrity(db)
	if check.Status != StatusOK {
		t.Fatalf("Status = %q, want %q: %s", check.Status, StatusOK, check.Message)
	}
	if !strings.Contains(check.Message, "N/A") {
		t.Fatalf("Message = %q, want N/A schema message", check.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckMailThreadIntegrityWarnsWhenSchemaGateFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("TABLE_NAME IN ('dependencies', 'wisp_dependencies')")).
		WillReturnError(errors.New("information schema unavailable"))

	check := checkMailThreadIntegrity(db)
	if check.Status != StatusWarning {
		t.Fatalf("Status = %q, want %q: %s", check.Status, StatusWarning, check.Message)
	}
	if !strings.Contains(check.Message, "Unable to check thread integrity schema") {
		t.Fatalf("Message = %q, want schema warning", check.Message)
	}
	if !strings.Contains(check.Detail, "information schema unavailable") {
		t.Fatalf("Detail = %q, want query error", check.Detail)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckMailThreadIntegrityRunsUnionWhenBothTablesHaveThreadID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("TABLE_NAME IN ('dependencies', 'wisp_dependencies')")).
		WillReturnRows(sqlmock.NewRows([]string{"has_thread_id"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT d.thread_id, COUNT(*) as refs")).
		WillReturnRows(sqlmock.NewRows([]string{"thread_id", "refs"}))

	check := checkMailThreadIntegrity(db)
	if check.Status != StatusOK {
		t.Fatalf("Status = %q, want %q: %s", check.Status, StatusOK, check.Message)
	}
	if !strings.Contains(check.Message, "All thread references valid") {
		t.Fatalf("Message = %q, want valid thread message", check.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckMailThreadIntegrityWarnsForOrphanedUnionRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("TABLE_NAME IN ('dependencies', 'wisp_dependencies')")).
		WillReturnRows(sqlmock.NewRows([]string{"has_thread_id"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT d.thread_id, COUNT(*) as refs")).
		WillReturnRows(sqlmock.NewRows([]string{"thread_id", "refs"}).AddRow("missing-thread", 2))

	check := checkMailThreadIntegrity(db)
	if check.Status != StatusWarning {
		t.Fatalf("Status = %q, want %q: %s", check.Status, StatusWarning, check.Message)
	}
	if !strings.Contains(check.Message, "Found 2 orphaned thread references") {
		t.Fatalf("Message = %q, want orphan warning", check.Message)
	}
	if !strings.Contains(check.Detail, "missing-thread (2 refs)") {
		t.Fatalf("Detail = %q, want missing thread detail", check.Detail)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
