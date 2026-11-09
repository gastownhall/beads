package dolt

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// bd-2e1byb (GH#5581): a failed custom-config resolve must not be cached as
// the store's authoritative vocabulary. After one failed read, the next call
// must retry and see the custom status that the database actually holds.
func TestCustomConfigCacheDoesNotCacheFailedResolve(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// First load: the read transaction cannot start.
	mock.ExpectBegin().WillReturnError(errors.New("simulated read failure"))
	// Second load: the database answers with one custom status and one type.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, category FROM custom_statuses ORDER BY name")).
		WillReturnRows(sqlmock.NewRows([]string{"name", "category"}).AddRow("review", "active"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name FROM custom_types ORDER BY name")).
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("spike"))
	mock.ExpectRollback()

	store := &DoltStore{db: db}
	ctx := context.Background()

	first, firstErr := store.GetCustomStatuses(ctx)
	t.Logf("first GetCustomStatuses = %v, err = %v", first, firstErr)

	got, err := store.GetCustomStatuses(ctx)
	if err != nil {
		t.Fatalf("second GetCustomStatuses: %v", err)
	}
	if !slices.Contains(got, "review") {
		t.Fatalf("second GetCustomStatuses = %v, want it to contain %q: the failed first resolve was cached as the vocabulary", got, "review")
	}
}
