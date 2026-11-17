package dolt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestInitSchemaAsksForTheSessionDatabaseOnce pins the direct open's two callers
// of one question: the store reads DATABASE() to name the migration lock, and
// the convergence probe must not then ask the same session again.
func TestInitSchemaAsksForTheSessionDatabaseOnce(t *testing.T) {
	var sent []string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actual string) error {
		sent = append(sent, actual)
		return nil
	})))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	stop := errors.New("stop here")
	mock.ExpectQuery("name").WillReturnRows(sqlmock.NewRows([]string{"DATABASE()"}).AddRow("testdb"))
	mock.ExpectQuery("probe").WillReturnError(stop)
	mock.ExpectQuery("lock").WillReturnError(stop)

	if _, err := initSchemaOnDBWithBootstrapHeal(context.Background(), db, nil, ""); err == nil {
		t.Fatal("initSchemaOnDBWithBootstrapHeal() error = nil, want the scripted failure")
	}
	asked := 0
	for _, q := range sent {
		if strings.Contains(q, "SELECT DATABASE()") {
			asked++
		}
	}
	if asked != 1 {
		t.Fatalf("SELECT DATABASE() sent %d times, want once: %q", asked, sent)
	}
}
