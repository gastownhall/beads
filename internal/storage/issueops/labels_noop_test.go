package issueops

import (
	"database/sql"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func TestLabelMutationNoopsDoNotTouchIssueSnapshot(t *testing.T) {
	tests := []struct {
		name  string
		query string
		run   func(*testing.T, *sql.DB) error
	}{
		{
			name:  "duplicate add",
			query: "INSERT IGNORE INTO labels (issue_id, label) VALUES (?, ?)",
			run: func(t *testing.T, tx *sql.DB) error {
				return AddLabelInTx(t.Context(), tx, "labels", "events", "bd-test", "priority", "tester")
			},
		},
		{
			name:  "missing remove",
			query: "DELETE FROM labels WHERE issue_id = ? AND label = ?",
			run: func(t *testing.T, tx *sql.DB) error {
				return RemoveLabelInTx(t.Context(), tx, "labels", "events", "bd-test", "priority", "tester")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectExec(regexp.QuoteMeta(tt.query)).
				WithArgs("bd-test", "priority").
				WillReturnResult(sqlmock.NewResult(0, 0))
			if tt.name == "duplicate add" {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM issues WHERE id = ?")).
					WithArgs("bd-test").
					WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
			}

			if err := tt.run(t, db); err != nil {
				t.Fatalf("no-op mutation: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLabelMutationRejectsUnexpectedTable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	err = AddLabelInTx(t.Context(), db, "future_labels", "events", "bd-test", "priority", "tester")
	if err == nil || !regexp.MustCompile(`unexpected label table`).MatchString(err.Error()) {
		t.Fatalf("AddLabelInTx unexpected-table error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected SQL before table validation: %v", err)
	}
}
