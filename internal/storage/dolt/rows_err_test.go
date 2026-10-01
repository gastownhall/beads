package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

// database/sql reports a read that died mid-result-set through Rows.Err only.
// Rows.Next returns false for that case exactly as it does for a clean end.
var errMidIteration = errors.New("connection lost mid-iteration")

type truncateTarget struct {
	match string
	after int
}

type truncDriver struct {
	target truncateTarget
}

func (d *truncDriver) Open(string) (driver.Conn, error) { return &truncConn{drv: d}, nil }

type truncConn struct {
	drv *truncDriver
}

func (c *truncConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not used by this stub")
}
func (c *truncConn) Close() error              { return nil }
func (c *truncConn) Begin() (driver.Tx, error) { return nil, errors.New("tx not used by this stub") }

func (c *truncConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	norm := strings.Join(strings.Fields(query), " ")
	cut := -1
	if c.drv.target.match != "" && strings.Contains(norm, c.drv.target.match) {
		cut = c.drv.target.after
	}

	switch {
	case strings.Contains(norm, "FROM wisps WHERE id"):
		return &stubRows{cols: []string{"1"}}, nil
	case strings.Contains(norm, "SELECT title FROM"):
		return &stubRows{cols: []string{"title"}}, nil
	case strings.Contains(norm, "SELECT issue_id FROM dependencies"):
		return &stubRows{
			cols: []string{"issue_id"},
			data: [][]driver.Value{{"bd-1"}, {"bd-2"}, {"bd-3"}, {"bd-4"}},
			cut:  cut,
		}, nil
	case strings.Contains(norm, "SELECT id, status FROM"):
		return &stubRows{
			cols: []string{"id", "status"},
			data: [][]driver.Value{
				{"bd-1", "closed"},
				{"bd-2", "closed"},
				{"bd-3", "open"},
				{"bd-4", "open"},
			},
			cut: cut,
		}, nil
	}
	return nil, errors.New("unexpected query: " + norm)
}

type stubRows struct {
	cols []string
	data [][]driver.Value
	cut  int
	sent int
}

func (r *stubRows) Columns() []string { return r.cols }
func (r *stubRows) Close() error      { return nil }

func (r *stubRows) Next(dest []driver.Value) error {
	if r.cut >= 0 && r.sent >= r.cut {
		return errMidIteration
	}
	if r.sent >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.sent])
	r.sent++
	return nil
}

// sql.Register panics on a repeated name, which a -count=2 run would hit.
var truncDriverSeq atomic.Int64

func storeWithTruncatedRead(t *testing.T, target truncateTarget) *DoltStore {
	t.Helper()
	name := fmt.Sprintf("beads-rowserr-stub-%d", truncDriverSeq.Add(1))
	sql.Register(name, &truncDriver{target: target})
	db, err := sql.Open(name, "stub")
	if err != nil {
		t.Fatalf("open stub db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &DoltStore{db: db}
}

func TestGetMoleculeProgressReportsTruncatedReads(t *testing.T) {
	cases := []struct {
		name   string
		target truncateTarget
	}{
		{
			name:   "child rows end early",
			target: truncateTarget{match: "SELECT issue_id FROM dependencies", after: 2},
		},
		{
			name:   "status rows end early",
			target: truncateTarget{match: "SELECT id, status FROM", after: 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := storeWithTruncatedRead(t, tc.target)

			stats, err := s.GetMoleculeProgress(context.Background(), "bd-molecule")
			if err == nil {
				t.Fatalf("truncated read returned no error: Total=%d Completed=%d, for a molecule with 4 children of which 2 are closed",
					stats.Total, stats.Completed)
			}
			if !errors.Is(err, errMidIteration) {
				t.Fatalf("error should wrap the driver failure, got %v", err)
			}
		})
	}
}
