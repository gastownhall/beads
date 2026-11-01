package historyoffequiv

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Every column of every base table is dumped: there is no list of tables to
// keep up to date, so a table a later change starts writing is compared the
// moment it exists. Each column is cast to text in SQL by its declared type
// (datetimes to a fixed format with microseconds, JSON to its text, binary to
// hex), and the text is then normalized only where a value is not a function
// of the scenario:
//
//   - A datetime inside the run's window (the instants the scenario's writes
//     stamp from the wall clock) reads "<now>", and so does an RFC 3339
//     instant inside the window embedded in text (an event's snapshot of the
//     row it changed). NULL stays NULL, and an instant the scenario supplied
//     (fixedAt, decades outside the window) stays verbatim, so a write that
//     starts or stops stamping a column, or that overwrites a supplied
//     instant, still changes the digest.
//   - A JSON value is re-serialized with sorted keys, so two engines' (or two
//     versions') spacing and key order do not read as a difference.
//   - The columns in volatileColumns hold values minted from randomness or
//     derived from a wall-clock instant. Each reads "<volatile>" under its
//     rule, which keeps NULL, the empty string and the "none" value apart
//     from a minted one.
//
// Rows are compared as a sorted multiset per table.

type volatility int

const (
	// volatileAlways masks every non-empty value.
	volatileAlways volatility = iota
	// volatileNonZero masks every value but "0", the column's "none".
	volatileNonZero
	// volatileUUID masks a UUID-shaped value and keeps any other verbatim.
	volatileUUID
)

// volatileColumns are the columns whose values differ between two runs of the
// same scenario on the same code, each with why. They were found by running
// the scenario twice on each leg and diffing the dumps.
var volatileColumns = map[string]map[string]volatility{
	// An event's id is a digest over its content and its wall-clock
	// created_at (issueops.InsertDerivedEvent).
	"events":      {"id": volatileAlways},
	"wisp_events": {"id": volatileAlways},
	// A live comment's id is the same kind of digest; an imported comment
	// keeps the id it arrived with, which stays verbatim.
	"comments":      {"id": volatileUUID},
	"wisp_comments": {"id": volatileUUID},
	// row_lock is a fresh random CAS token on every write; 0 means none.
	"issues": {"row_lock": volatileNonZero},
	"wisps":  {"row_lock": volatileNonZero},
	// Per-session coordination tokens (dependency-coordination/v1/...,
	// issue-create/v1/...) are random; their keys are digests of the ids
	// involved and stay verbatim.
	"local_metadata": {"value": volatileAlways},
}

var (
	uuidRe    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	rfc3339Re = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
)

func maskVolatile(table, column string, v string) (string, bool) {
	rule, ok := volatileColumns[table][column]
	if !ok || v == "" {
		return v, false
	}
	switch rule {
	case volatileNonZero:
		if v == "0" {
			return v, false
		}
	case volatileUUID:
		if !uuidRe.MatchString(v) {
			return v, false
		}
	}
	return "<volatile>", true
}

// Two pseudo-tables carry the Dolt commit graph, compared like any table: the
// messages of the commits the scenario made, in order (a change to what a
// write commits, or whether it commits at all, shows up there), and the
// working set it left behind (a change to what a write stages shows up
// there).
const (
	doltLogTable    = "@dolt_log"
	doltStatusTable = "@dolt_status"
)

func doltLogCount(t *testing.T, ctx context.Context, q Query) int {
	t.Helper()
	rows, err := q(ctx, "SELECT CAST(COUNT(*) AS CHAR) FROM dolt_log")
	if err != nil {
		t.Fatalf("count dolt_log: %v", err)
	}
	n, err := strconv.Atoi(*rows[0][0])
	if err != nil {
		t.Fatalf("count dolt_log: %v", err)
	}
	return n
}

func dumpAll(t *testing.T, ctx context.Context, q Query, windowStart, windowEnd time.Time, logBefore int) dump {
	t.Helper()
	tableRows, err := q(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	colRows, err := q(ctx, `SELECT table_name, column_name, data_type FROM information_schema.columns
		WHERE table_schema = DATABASE() ORDER BY table_name, ordinal_position`)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	type column struct{ name, dataType string }
	columns := map[string][]column{}
	for _, r := range colRows {
		columns[*r[0]] = append(columns[*r[0]], column{name: *r[1], dataType: strings.ToLower(*r[2])})
	}
	out := dump{Tables: map[string]tableDump{}}
	for _, r := range tableRows {
		table := *r[0]
		if strings.HasPrefix(table, "dolt_") {
			continue // Dolt's own system tables; the commit graph is read below.
		}
		cols := columns[table]
		if len(cols) == 0 {
			t.Fatalf("table %s has no columns in information_schema.columns", table)
		}
		exprs := make([]string, len(cols))
		names := make([]string, len(cols))
		for i, c := range cols {
			names[i] = c.name
			exprs[i] = textExpr(c.name, c.dataType)
		}
		rows, err := q(ctx, fmt.Sprintf("SELECT %s FROM `%s`", strings.Join(exprs, ", "), table))
		if err != nil {
			t.Fatalf("dump %s: %v", table, err)
		}
		for _, row := range rows {
			for i, c := range cols {
				row[i] = normalize(t, table, c.name, c.dataType, row[i], windowStart, windowEnd)
			}
		}
		sortRows(rows)
		out.Tables[table] = tableDump{Columns: names, Rows: rows}
	}

	// The scenario's commits, newest first as dolt_log lists them, numbered so
	// the sorted comparison keeps their order. The committer identity comes
	// from the environment (CI sets its own), so only the message is kept.
	logRows, err := q(ctx, "SELECT message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	made := len(logRows) - logBefore
	if made < 0 {
		t.Fatalf("dolt_log shrank from %d to %d commits during the scenario", logBefore, len(logRows))
	}
	var log [][]*string
	for i, r := range logRows[:made] {
		seq := fmt.Sprintf("%04d", made-i)
		log = append(log, []*string{&seq, r[0]})
	}
	sortRows(log)
	out.Tables[doltLogTable] = tableDump{Columns: []string{"seq", "message"}, Rows: log}

	status, err := q(ctx, "SELECT table_name, CAST(staged AS CHAR), status FROM dolt_status")
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	sortRows(status)
	out.Tables[doltStatusTable] = tableDump{Columns: []string{"table_name", "staged", "status"}, Rows: status}
	return out
}

func textExpr(name, dataType string) string {
	c := "`" + name + "`"
	switch dataType {
	case "datetime", "timestamp":
		return fmt.Sprintf("DATE_FORMAT(%s, '%%Y-%%m-%%d %%H:%%i:%%s.%%f')", c)
	case "date":
		return fmt.Sprintf("DATE_FORMAT(%s, '%%Y-%%m-%%d')", c)
	case "blob", "tinyblob", "mediumblob", "longblob", "binary", "varbinary":
		return fmt.Sprintf("HEX(%s)", c)
	default:
		return fmt.Sprintf("CAST(%s AS CHAR)", c)
	}
}

const datetimeLayout = "2006-01-02 15:04:05.000000"

func normalize(t *testing.T, table, column, dataType string, v *string, windowStart, windowEnd time.Time) *string {
	t.Helper()
	if v == nil {
		return nil
	}
	if masked, ok := maskVolatile(table, column, *v); ok {
		return &masked
	}
	switch dataType {
	case "datetime", "timestamp":
		at, err := time.Parse(datetimeLayout, *v)
		if err != nil {
			t.Fatalf("%s.%s: datetime %q: %v", table, column, *v, err)
		}
		if !at.Before(windowStart) && !at.After(windowEnd) {
			s := "<now>"
			return &s
		}
	case "json":
		var parsed any
		dec := json.NewDecoder(strings.NewReader(*v))
		dec.UseNumber() // keep integers past 2^53 exact
		if err := dec.Decode(&parsed); err != nil {
			return v
		}
		b, err := json.Marshal(parsed)
		if err != nil {
			t.Fatalf("%s.%s: re-marshal JSON: %v", table, column, err)
		}
		s := string(b)
		return maskInstants(&s, windowStart, windowEnd)
	}
	return maskInstants(v, windowStart, windowEnd)
}

// maskInstants replaces each RFC 3339 instant inside the run's window that v
// embeds with "<now>".
func maskInstants(v *string, windowStart, windowEnd time.Time) *string {
	if !rfc3339Re.MatchString(*v) {
		return v
	}
	s := rfc3339Re.ReplaceAllStringFunc(*v, func(m string) string {
		at, err := time.Parse(time.RFC3339Nano, m)
		if err != nil || at.Before(windowStart) || at.After(windowEnd) {
			return m
		}
		return "<now>"
	})
	return &s
}
