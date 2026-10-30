package dolt

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// moleculeProgressLost is the driver error the sqlmock fixtures below deliver
// mid-iteration: Next() returns false the same way for exhaustion and for
// failure, so only Rows.Err() separates the two.
var moleculeProgressLost = errors.New("connection lost mid-iteration")

// moleculeProgressMockStore returns a DoltStore over sqlmock with the two
// reads GetMoleculeProgress issues before its dependency scan registered:
// the wisps existence probe (mol-1 is not a wisp, so routing stays on
// issues/dependencies) and the molecule title.
func moleculeProgressMockStore(t *testing.T) (*DoltStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT title FROM issues WHERE id = ?")).
		WillReturnRows(sqlmock.NewRows([]string{"title"}).AddRow("Molecule"))
	return &DoltStore{db: db}, mock
}

// expectMoleculeProgressReads registers the child-ID read and the status
// batch read (a single batch) for a four-child molecule.
func expectMoleculeProgressReads(mock sqlmock.Sqlmock, children, statuses *sqlmock.Rows) {
	mock.ExpectQuery("SELECT issue_id FROM dependencies").WillReturnRows(children)
	mock.ExpectQuery("SELECT id, status FROM issues WHERE id IN").WillReturnRows(statuses)
}

// requireMoleculeProgressReadError asserts the contract every truncated read
// must meet: no partial rollup, and an error wrapping both the driver error
// and ErrQuery.
func requireMoleculeProgressReadError(t *testing.T, stats *types.MoleculeProgressStats, err, cause error) {
	t.Helper()
	if err == nil {
		t.Fatalf("GetMoleculeProgress: err=nil stats=%+v; want an error wrapping %v",
			stats, cause)
	}
	if stats != nil {
		t.Fatalf("GetMoleculeProgress: stats=%+v alongside error %v; want nil stats",
			stats, err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("GetMoleculeProgress error %v; want it to wrap the driver error %v",
			err, cause)
	}
	if !errors.Is(err, ErrQuery) {
		t.Fatalf("GetMoleculeProgress error %v; want it to wrap ErrQuery", err)
	}
}

// TestGetMoleculeProgressReportsTruncatedReads pins that a result set which
// ends early (the driver reports an error after some rows) surfaces as an
// error instead of a progress rollup built from whichever rows arrived.
func TestGetMoleculeProgressReportsTruncatedReads(t *testing.T) {
	t.Run("children truncated", func(t *testing.T) {
		store, mock := moleculeProgressMockStore(t)
		children := sqlmock.NewRows([]string{"issue_id"}).
			AddRow("c1").AddRow("c2").AddRow("c3").AddRow("c4")
		children.RowError(2, moleculeProgressLost)
		// The status read never runs once the child-ID read reports its
		// error; the registered rows are what makes the base bug come back
		// as a nil error with a partial rollup instead of an unexpected
		// query, so expectations are deliberately not asserted here.
		statuses := sqlmock.NewRows([]string{"id", "status"}).
			AddRow("c1", "closed").AddRow("c2", "closed")
		expectMoleculeProgressReads(mock, children, statuses)

		stats, err := store.GetMoleculeProgress(context.Background(), "mol-1")
		requireMoleculeProgressReadError(t, stats, err, moleculeProgressLost)
	})

	t.Run("status truncated", func(t *testing.T) {
		store, mock := moleculeProgressMockStore(t)
		children := sqlmock.NewRows([]string{"issue_id"}).
			AddRow("c1").AddRow("c2").AddRow("c3").AddRow("c4")
		statuses := sqlmock.NewRows([]string{"id", "status"}).
			AddRow("c1", "closed").AddRow("c2", "closed").
			AddRow("c3", "in_progress").AddRow("c4", "open")
		statuses.RowError(2, moleculeProgressLost)
		expectMoleculeProgressReads(mock, children, statuses)

		stats, err := store.GetMoleculeProgress(context.Background(), "mol-1")
		requireMoleculeProgressReadError(t, stats, err, moleculeProgressLost)
	})

	t.Run("status truncated in a later batch", func(t *testing.T) {
		store, mock := moleculeProgressMockStore(t)
		children := sqlmock.NewRows([]string{"issue_id"})
		for i := 1; i <= queryBatchSize+1; i++ {
			children.AddRow(fmt.Sprintf("c%d", i))
		}
		mock.ExpectQuery("SELECT issue_id FROM dependencies").WillReturnRows(children)
		// The first status batch (200 ids) completes; the second holds one
		// row whose delivery fails, so the check must run inside the batch
		// loop, not only after the last batch.
		firstBatch := sqlmock.NewRows([]string{"id", "status"})
		for i := 1; i <= queryBatchSize; i++ {
			firstBatch.AddRow(fmt.Sprintf("c%d", i), "open")
		}
		mock.ExpectQuery("SELECT id, status FROM issues WHERE id IN").
			WillReturnRows(firstBatch)
		lastBatch := sqlmock.NewRows([]string{"id", "status"}).AddRow("c201", "open")
		lastBatch.RowError(0, moleculeProgressLost)
		mock.ExpectQuery("SELECT id, status FROM issues WHERE id IN").
			WillReturnRows(lastBatch)

		stats, err := store.GetMoleculeProgress(context.Background(), "mol-1")
		requireMoleculeProgressReadError(t, stats, err, moleculeProgressLost)
	})

	t.Run("complete read", func(t *testing.T) {
		store, mock := moleculeProgressMockStore(t)
		children := sqlmock.NewRows([]string{"issue_id"}).
			AddRow("c1").AddRow("c2").AddRow("c3").AddRow("c4")
		statuses := sqlmock.NewRows([]string{"id", "status"}).
			AddRow("c1", "closed").AddRow("c2", "closed").
			AddRow("c3", "in_progress").AddRow("c4", "open")
		expectMoleculeProgressReads(mock, children, statuses)

		stats, err := store.GetMoleculeProgress(context.Background(), "mol-1")
		if err != nil {
			t.Fatalf("GetMoleculeProgress: %v", err)
		}
		if stats.Total != 4 || stats.Completed != 2 || stats.InProgress != 1 {
			t.Fatalf("rollup Total=%d Completed=%d InProgress=%d; want 4/2/1",
				stats.Total, stats.Completed, stats.InProgress)
		}
		if stats.CurrentStepID != "c3" {
			t.Fatalf("CurrentStepID=%q; want c3", stats.CurrentStepID)
		}
		if stats.MoleculeTitle != "Molecule" {
			t.Fatalf("MoleculeTitle=%q; want Molecule", stats.MoleculeTitle)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})
}
