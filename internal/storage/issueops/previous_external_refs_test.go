package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"testing"
	"time"
)

type historyRow struct {
	id   string
	ref  any
	date time.Time
}

type refsDriver struct {
	commits  [][]driver.Value
	atCommit [][]driver.Value
	asOfErr  error
	history  []historyRow
	counts   map[string]int
	maxIDs   int
}

func (d *refsDriver) Open(string) (driver.Conn, error) { return &refsConn{d}, nil }

type refsConn struct{ d *refsDriver }

func (c *refsConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (c *refsConn) Close() error                        { return nil }
func (c *refsConn) Begin() (driver.Tx, error)           { return nil, errors.New("no transactions") }

func (c *refsConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	d := c.d
	switch {
	case strings.Contains(query, "dolt_log"):
		d.counts["dolt_log"]++
		return &refsRows{[]string{"commit_hash", "date"}, d.commits}, nil
	case strings.Contains(query, "AS OF"):
		d.counts["as_of"]++
		if d.asOfErr != nil {
			return nil, d.asOfErr
		}
		return &refsRows{[]string{"id", "external_ref"}, d.atCommit}, nil
	case strings.Contains(query, "dolt_history_issues"):
		d.counts["history"]++
		d.maxIDs = max(d.maxIDs, len(args)-1)
		asked := make(map[string]bool, len(args))
		for _, a := range args[:len(args)-1] {
			asked[a.Value.(string)] = true
		}
		asOf := args[len(args)-1].Value.(time.Time)
		var out [][]driver.Value
		for _, r := range d.history {
			if asked[r.id] && !r.date.After(asOf) {
				out = append(out, []driver.Value{r.id, r.ref, r.date})
			}
		}
		return &refsRows{[]string{"id", "external_ref", "commit_date"}, out}, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", query)
}

type refsRows struct {
	columns []string
	values  [][]driver.Value
}

func (r *refsRows) Columns() []string { return r.columns }
func (r *refsRows) Close() error      { return nil }
func (r *refsRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

var refsDriverSeq int

func openRefsDB(t *testing.T, d *refsDriver) *sql.DB {
	t.Helper()
	d.counts = map[string]int{}
	refsDriverSeq++
	name := fmt.Sprintf("issueops-previous-external-refs-%d", refsDriverSeq)
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

var (
	refsAsOf  = time.Date(2026, time.September, 16, 12, 0, 0, 0, time.UTC)
	refsNewer = refsAsOf.Add(-time.Hour)
	refsOlder = refsAsOf.Add(-2 * time.Hour)
)

func commitRow(hash string, date time.Time) []driver.Value { return []driver.Value{hash, date} }

func TestPreviousExternalRefsInTx(t *testing.T) {
	const c1, c2 = "abcdef0123456789abcdef0123456789", "0123456789abcdef0123456789abcdef"
	history := []historyRow{
		{"bd-hit", "history-hit", refsNewer},
		{"bd-gone", "gone-new", refsNewer},
		{"bd-gone", "gone-old", refsOlder},
		{"bd-null", nil, refsNewer},
		{"bd-later", "later", refsAsOf.Add(time.Hour)},
	}
	for _, tc := range []struct {
		name    string
		d       *refsDriver
		ids     []string
		want    map[string]string
		queries map[string]int
	}{
		{
			name: "ids at the newest commit are answered from it",
			d: &refsDriver{
				commits:  [][]driver.Value{commitRow(c1, refsNewer), commitRow(c2, refsOlder)},
				atCommit: [][]driver.Value{{"bd-hit", "snapshot-hit"}, {"bd-other", "other"}},
				history:  history,
			},
			ids:     []string{"bd-hit", "bd-gone", "bd-null", "bd-later", "bd-never", "bd-hit"},
			want:    map[string]string{"bd-hit": "snapshot-hit", "bd-gone": "gone-new", "bd-null": ""},
			queries: map[string]int{"dolt_log": 1, "as_of": 1, "history": 1},
		},
		{
			name: "a tie at the newest date sends every id to history",
			d: &refsDriver{
				commits:  [][]driver.Value{commitRow(c1, refsNewer), commitRow(c2, refsNewer)},
				atCommit: [][]driver.Value{{"bd-hit", "snapshot-hit"}},
				history:  history,
			},
			ids:     []string{"bd-hit", "bd-gone"},
			want:    map[string]string{"bd-hit": "history-hit", "bd-gone": "gone-new"},
			queries: map[string]int{"dolt_log": 1, "history": 1},
		},
		{
			name: "no issues table at the newest commit",
			d: &refsDriver{
				commits: [][]driver.Value{commitRow(c1, refsNewer)},
				asOfErr: errors.New("Error 1146: table not found: issues"),
				history: history,
			},
			ids:     []string{"bd-hit"},
			want:    map[string]string{"bd-hit": "history-hit"},
			queries: map[string]int{"dolt_log": 1, "as_of": 1, "history": 1},
		},
		{
			name:    "no commit at or before asOf",
			d:       &refsDriver{history: history},
			ids:     []string{"bd-hit", "bd-gone"},
			want:    map[string]string{},
			queries: map[string]int{"dolt_log": 1},
		},
		{
			name:    "no ids",
			d:       &refsDriver{commits: [][]driver.Value{commitRow(c1, refsNewer)}},
			want:    map[string]string{},
			queries: map[string]int{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openRefsDB(t, tc.d)
			got, err := PreviousExternalRefsInTx(context.Background(), db, tc.ids, refsAsOf, queryBatchSize)
			if err != nil {
				t.Fatalf("PreviousExternalRefsInTx: %v", err)
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("refs = %v, want %v", got, tc.want)
			}
			if !maps.Equal(tc.d.counts, tc.queries) {
				t.Errorf("queries = %v, want %v", tc.d.counts, tc.queries)
			}
		})
	}
}

func TestPreviousExternalRefsInTxTieKeepsFirstRow(t *testing.T) {
	d := &refsDriver{
		commits: [][]driver.Value{commitRow("abcdef0123456789abcdef0123456789", refsNewer), commitRow("0123456789abcdef0123456789abcdef", refsNewer)},
		history: []historyRow{
			{"bd-1", "older", refsOlder},
			{"bd-1", "first", refsNewer},
			{"bd-1", "second", refsNewer},
		},
	}
	got, err := PreviousExternalRefsInTx(context.Background(), openRefsDB(t, d), []string{"bd-1"}, refsAsOf, queryBatchSize)
	if err != nil || got["bd-1"] != "first" {
		t.Fatalf("PreviousExternalRefsInTx = (%v, %v), want bd-1 = first", got, err)
	}
}

func TestPreviousExternalRefsInTxQueryCount(t *testing.T) {
	for _, chunk := range []int{queryBatchSize, 16} {
		t.Run(fmt.Sprintf("chunk_%d", chunk), func(t *testing.T) {
			d := &refsDriver{commits: [][]driver.Value{commitRow("abcdef0123456789abcdef0123456789", refsNewer)}}
			var ids []string
			for i := range 5000 {
				id := fmt.Sprintf("bd-%d", i)
				ids = append(ids, id)
				if i%2 == 0 {
					d.atCommit = append(d.atCommit, []driver.Value{id, "at-commit"})
				} else {
					d.history = append(d.history, historyRow{id, "history", refsOlder})
				}
			}
			got, err := PreviousExternalRefsInTx(context.Background(), openRefsDB(t, d), ids, refsAsOf, chunk)
			if err != nil {
				t.Fatalf("PreviousExternalRefsInTx: %v", err)
			}
			if len(got) != len(ids) || got["bd-0"] != "at-commit" || got["bd-1"] != "history" {
				t.Fatalf("got %d refs (bd-0=%q, bd-1=%q), want %d", len(got), got["bd-0"], got["bd-1"], len(ids))
			}
			misses := len(ids) / 2
			want := map[string]int{"dolt_log": 1, "as_of": 1, "history": (misses + chunk - 1) / chunk}
			if !maps.Equal(d.counts, want) {
				t.Fatalf("queries = %v, want %v", d.counts, want)
			}
			if d.maxIDs > chunk {
				t.Fatalf("a history read carried %d ids, want at most %d", d.maxIDs, chunk)
			}
		})
	}
}
