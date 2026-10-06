package issueops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
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
	// failHistory fails the history read with that 1-based number with
	// historyErr, after its rows when failAtRowsErr is set.
	failHistory   int
	historyErr    error
	failAtRowsErr bool
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
		return &refsRows{columns: []string{"commit_hash", "date"}, values: d.commits}, nil
	case strings.Contains(query, "AS OF"):
		d.counts["as_of"]++
		if d.asOfErr != nil {
			return nil, d.asOfErr
		}
		return &refsRows{columns: []string{"id", "external_ref"}, values: d.atCommit}, nil
	case strings.Contains(query, "dolt_history_issues"):
		d.counts["history"]++
		failing := d.counts["history"] == d.failHistory
		if failing && !d.failAtRowsErr {
			return nil, d.historyErr
		}
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
		rows := &refsRows{columns: []string{"id", "external_ref", "commit_date"}, values: out}
		if failing {
			rows.err = d.historyErr
		}
		return rows, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", query)
}

type refsRows struct {
	columns []string
	values  [][]driver.Value
	err     error
}

func (r *refsRows) Columns() []string { return r.columns }
func (r *refsRows) Close() error      { return nil }
func (r *refsRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		if r.err != nil {
			return r.err
		}
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
			got, unanswered, err := PreviousExternalRefsInTx(context.Background(), db, tc.ids, refsAsOf, queryBatchSize)
			if err != nil || unanswered != nil {
				t.Fatalf("PreviousExternalRefsInTx: %v, unanswered %v", err, unanswered)
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

func TestPreviousExternalRefsInTxHistoryReadFails(t *testing.T) {
	for _, atRowsErr := range []bool{false, true} {
		t.Run(fmt.Sprintf("at_rows_err=%v", atRowsErr), func(t *testing.T) {
			readErr := errors.New("i/o timeout")
			d := &refsDriver{
				commits:  [][]driver.Value{commitRow("abcdef0123456789abcdef0123456789", refsNewer)},
				atCommit: [][]driver.Value{{"bd-at", "at-commit"}},
				history: []historyRow{
					{"bd-at", "at-commit", refsNewer},
					{"bd-1", "one", refsOlder},
					{"bd-3", "three", refsOlder},
					{"bd-5", "five", refsOlder},
				},
				failHistory:   2,
				historyErr:    readErr,
				failAtRowsErr: atRowsErr,
			}
			// History chunks of two: [bd-1 bd-2] [bd-3 bd-4] [bd-5]; the second fails.
			got, unanswered, err := PreviousExternalRefsInTx(context.Background(), openRefsDB(t, d), []string{"bd-1", "bd-at", "bd-2", "bd-3", "bd-4", "bd-5"}, refsAsOf, 2)
			if !errors.Is(err, readErr) {
				t.Fatalf("err = %v, want %v", err, readErr)
			}
			if want := []string{"bd-3", "bd-4", "bd-5"}; !slices.Equal(unanswered, want) {
				t.Errorf("unanswered = %v, want %v", unanswered, want)
			}
			if want := map[string]string{"bd-at": "at-commit", "bd-1": "one"}; !maps.Equal(got, want) {
				t.Errorf("refs = %v, want %v", got, want)
			}
			if d.counts["history"] != 2 {
				t.Errorf("%d history reads, want 2: none after the failed one", d.counts["history"])
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
	got, _, err := PreviousExternalRefsInTx(context.Background(), openRefsDB(t, d), []string{"bd-1"}, refsAsOf, queryBatchSize)
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
			got, _, err := PreviousExternalRefsInTx(context.Background(), openRefsDB(t, d), ids, refsAsOf, chunk)
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
