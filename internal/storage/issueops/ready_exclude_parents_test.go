package issueops

import (
	"errors"
	"slices"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// GetOpenChildParentIDsInTx is the one resolver behind the flagged ready
// read's parent set on BOTH stacks (the classic one directly, the union one
// through domain/db's own buildReadyWorkPredicates), so its statement shape
// is a cross-stack contract: the combined union — sqlbuild's rendering of
// the close gate's open-children count turned into the id projection — when
// the wisp plane exists, the DURABLE LEG ALONE when a database legitimately
// lacks the plane (the exact tolerance countOpenChildrenForTargetInTx grants),
// and a typed refusal for anything that is not a family's main table.
func TestGetOpenChildParentIDsInTxResolvesTheCloseGateSet(t *testing.T) {
	t.Run("combined union, sorted", func(t *testing.T) {
		for _, family := range []struct{ table, column string }{
			{"issues", "depends_on_issue_id"},
			{"wisps", "depends_on_wisp_id"},
		} {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer func() { _ = db.Close() }()

			combined := sqlbuild.OpenChildParentIDsSQL(family.table)
			if !strings.Contains(combined, family.column) {
				t.Fatalf("OpenChildParentIDsSQL(%q) does not select %s", family.table, family.column)
			}
			// ONE statement, both child families: the wisp plane exists here.
			mock.ExpectQuery("SELECT DISTINCT dependency").
				WillReturnRows(sqlmock.NewRows([]string{family.column}).
					AddRow("xp-2").AddRow("xp-1"))

			got, err := GetOpenChildParentIDsInTx(t.Context(), db, family.table)
			if err != nil {
				t.Fatalf("GetOpenChildParentIDsInTx(%s): %v", family.table, err)
			}
			if want := []string{"xp-1", "xp-2"}; !slices.Equal(got, want) {
				t.Fatalf("ids = %v, want %v (sorted, as the NOT IN fold renders)", got, want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("the resolver must issue exactly the combined union: %v", err)
			}
		}
	})

	t.Run("missing wisp plane falls back to the durable leg", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer func() { _ = db.Close() }()

		// The combined union names the wisp tables, so it is the statement a
		// pre-migration database fails on — MySQL/Dolt error 1146, as the
		// driver reports it (dberrors.IsTableNotExist's typed branch).
		mock.ExpectQuery("SELECT DISTINCT dependency").
			WillReturnError(errors.New("Table 'test.wisp_dependencies' doesn't exist"))
		// The durable leg names no wisp table; its selection of the wisps
		// family's typed COLUMN is cross-plane children recorded against a
		// wisp parent, which the close gate counts too.
		mock.ExpectQuery("FROM dependencies AS dependency").
			WillReturnRows(sqlmock.NewRows([]string{"depends_on_wisp_id"}).AddRow("xp-1"))

		got, err := GetOpenChildParentIDsInTx(t.Context(), db, "wisps")
		if err != nil {
			t.Fatalf("GetOpenChildParentIDsInTx(wisps) on a wisp-plane-less database: %v", err)
		}
		if want := []string{"xp-1"}; !slices.Equal(got, want) {
			t.Fatalf("ids = %v, want %v", got, want)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expected the combined union and then the durable leg: %v", err)
		}
	})

	t.Run("a non-family table is a typed refusal", func(t *testing.T) {
		db, _, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock.New: %v", err)
		}
		defer func() { _ = db.Close() }()

		_, err = GetOpenChildParentIDsInTx(t.Context(), db, "labels")
		if err == nil || !strings.Contains(err.Error(), "unsupported family table") {
			t.Fatalf("labels is not a family's main table; got %v, want a typed refusal", err)
		}
	})
}
