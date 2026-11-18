package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/types"
)

// TestPooledReadStaleConnectionRetry pins what the read-timeout policy
// (TestPooledReadTimeoutPolicy) leaves alone: a pooled connection the server
// already reaped fails fast with invalid connection, and the managed retry
// still recovers it on a fresh connection; a slow read that succeeds is just
// a success.
func TestPooledReadStaleConnectionRetry(t *testing.T) {
	ctx := context.Background()

	t.Run("fast invalid connection then success retries", func(t *testing.T) {
		s, d := newPooledReadStore(t, testPoolReadDeadline, searchCountsHandler(func(call int) (driver.Rows, error) {
			if call <= 2 { // the first attempt's counts query
				return nil, mysql.ErrInvalidConn
			}
			return &pooledReadRows{}, nil
		}))
		if _, err := s.SearchIssuesWithCounts(ctx, "", types.IssueFilter{}); err != nil {
			t.Fatalf("SearchIssuesWithCounts after a stale connection: %v", err)
		}
		if got := d.Begins(); got != 2 {
			t.Errorf("read transaction opened %d times, want 2 (one retry)", got)
		}
	})

	t.Run("fast invalid connection then success retries each helper", func(t *testing.T) {
		for _, h := range []struct {
			name string
			run  func(*DoltStore) error
		}{
			{"queryContext", func(s *DoltStore) error {
				rows, err := s.queryContext(ctx, "SELECT id FROM issues")
				if err == nil {
					_ = rows.Close()
				}
				return err
			}},
			{"queryRowContext", func(s *DoltStore) error {
				var v int64
				return s.queryRowContext(ctx, func(row *sql.Row) error { return row.Scan(&v) }, "SELECT 1")
			}},
		} {
			s, d := newPooledReadStore(t, testPoolReadDeadline, func(_ string, call int) (driver.Rows, error) {
				if call == 1 {
					return nil, mysql.ErrInvalidConn
				}
				return &pooledReadRows{values: []driver.Value{int64(1)}}, nil
			})
			if err := h.run(s); err != nil {
				t.Errorf("%s after a stale connection: %v", h.name, err)
			}
			if got := len(d.Queries()); got != 2 {
				t.Errorf("%s sent %d queries, want 2 (one retry)", h.name, got)
			}
		}
	})

	t.Run("slow successful read stays successful", func(t *testing.T) {
		s, _ := newPooledReadStore(t, testPoolReadDeadline, func(string, int) (driver.Rows, error) {
			time.Sleep(testSlowFailure)
			return &pooledReadRows{values: []driver.Value{int64(7)}}, nil
		})
		var v int64
		if err := s.queryRowContext(ctx, func(row *sql.Row) error { return row.Scan(&v) }, "SELECT 7"); err != nil || v != 7 {
			t.Errorf("slow successful read = (%d, %v), want (7, nil)", v, err)
		}
	})

}
