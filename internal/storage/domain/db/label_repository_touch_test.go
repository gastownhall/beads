package db

import (
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/domain"
)

// TestLabelSQLRepositoryMutationsTouchIssueSnapshot pins the uow repository's
// GH#5442 statement order: a real label write stamps the owning row's
// updated_at before the derived event is recorded, and a label a create
// persists with the issue (PreserveUpdatedAt) leaves the accepted timestamp
// alone.
func TestLabelSQLRepositoryMutationsTouchIssueSnapshot(t *testing.T) {
	tests := []struct {
		name         string
		delete       bool
		useWisps     bool
		preserveTime bool
		labelTable   string
		eventTable   string
		issueTable   string
	}{
		{name: "insert issue label", labelTable: "labels", eventTable: "events", issueTable: "issues"},
		{name: "create constituent label preserves accepted timestamp", preserveTime: true, labelTable: "labels", eventTable: "events", issueTable: "issues"},
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
			if !tt.preserveTime {
				mock.ExpectExec("UPDATE "+tt.issueTable+" SET updated_at = \\? WHERE id = \\?").
					WithArgs(sqlmock.AnyArg(), "bd-test").
					WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectQuery("SELECT id FROM " + tt.eventTable).
				WillReturnRows(sqlmock.NewRows([]string{"id"}))
			mock.ExpectExec("INSERT INTO " + tt.eventTable).WillReturnResult(sqlmock.NewResult(1, 1))

			repo := NewLabelSQLRepository(db)
			if tt.delete {
				err = repo.Delete(t.Context(), "bd-test", "priority", "tester", domain.LabelOpts{UseWispsTable: tt.useWisps})
			} else {
				err = repo.Insert(t.Context(), "bd-test", "priority", "tester", domain.LabelOpts{UseWispsTable: tt.useWisps, PreserveUpdatedAt: tt.preserveTime})
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
