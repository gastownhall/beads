package db

import (
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/domain"
)

func TestLabelSQLRepositoryMutationsTouchIssueSnapshot(t *testing.T) {
	tests := []struct {
		name       string
		delete     bool
		useWisps   bool
		labelTable string
		eventTable string
		issueTable string
	}{
		{name: "insert issue label", labelTable: "labels", eventTable: "events", issueTable: "issues"},
		{name: "delete wisp label", delete: true, useWisps: true, labelTable: "wisp_labels", eventTable: "wisp_events", issueTable: "wisps"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })

			mutation := "INSERT IGNORE INTO " + tt.labelTable
			if tt.delete {
				mutation = "DELETE FROM " + tt.labelTable
			}
			mock.ExpectExec(regexp.QuoteMeta(mutation)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("SELECT id FROM " + tt.eventTable).
				WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectExec("INSERT INTO " + tt.eventTable).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectExec("UPDATE "+tt.issueTable+" SET updated_at = \\?, row_lock = \\? WHERE id = \\?").
				WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "bd-test").
				WillReturnResult(sqlmock.NewResult(0, 1))

			repo := NewLabelSQLRepository(db)
			if tt.delete {
				err = repo.Delete(t.Context(), "bd-test", "priority", "tester", domain.LabelOpts{UseWispsTable: tt.useWisps})
			} else {
				err = repo.Insert(t.Context(), "bd-test", "priority", "tester", domain.LabelOpts{UseWispsTable: tt.useWisps})
			}
			if err != nil {
				t.Fatalf("label mutation: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("missing snapshot touch: %v", err)
			}
		})
	}
}
