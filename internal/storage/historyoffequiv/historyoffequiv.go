// Package historyoffequiv is the versioned-history flag-off differential
// (gastownhall/beads#6132, #6135: "flag-off must remain byte-identical to
// today, proven by differential test").
//
// Each storage leg (embedded Dolt, Dolt server, unit of work) runs one
// scripted mutation set through its own front doors on a store whose
// versioned history was never turned on, the default. Run then dumps every
// column of every table, digests each table, and compares the digests with
// the leg's golden. The goldens were RECORDED FROM THE CODE BEFORE THE
// VERSIONED-HISTORY STACK (origin/main 4a67d0d028, the parent of #6943). The
// schema added since, listed in schemaAdditions under the rule spelled out
// there, is the only difference the comparison tolerates; any other
// difference in any table is a flag-off behavior change and fails the test.
//
// # Maintaining the goldens
//
// Run records instead of checking when GoldenDirEnv is set. Recording and
// checking prepare a run the same way: each schemaAdditions entry the
// database has is checked under the rule and set aside before any table is
// digested. A golden recorded from any tree whose flag-off storage behavior
// matches the committed goldens is therefore byte-identical to them apart
// from recorded_from, and recording from the current tree is also how a
// failure is read: diff its full dump against one recorded from the base.
//
//   - A migration whose schema flag-off leaves untouched (nullable columns
//     that stay NULL, tables that stay empty) appends an entry to
//     schemaAdditions in its own PR: those columns and tables, and its
//     bookkeeping rows. The goldens do not change.
//   - A deliberate change to what flag-off storage writes, including a
//     migration that writes rows or values while history is off,
//     re-records the affected legs from its PR's tree. The PR shows the
//     golden diff, and that the full dumps differ from a recording of its
//     base only where the change means them to.
//   - A change to the scenario re-records every leg. Record it from
//     4a67d0d028 while the harness still builds there (copy this package
//     and the three legs' test files into a worktree of that commit), so
//     the new steps are anchored to the pre-stack code too; once it no
//     longer builds there, from the PR's base.
//   - A Dolt upgrade that changes only how the engine renders a stored
//     value (a JSON or datetime cast) re-records the legs that run the new
//     engine from the PR's tree, and the PR shows that the full dumps
//     differ from a recording of its base, made with the old engine, only
//     in that rendering. The embedded leg runs the engine go.mod pins; the
//     server and unit-of-work legs run the dolt binary the test finds (CI:
//     DOLT_VERSION in tools/bazel/dolt.bzl).
//
// recorded_from names the tree a golden was recorded from: a commit, or a
// commit and the PR on top of it.
package historyoffequiv

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Prefix is the issue prefix every scenario id carries. A leg configures its
// store with it before Run.
const Prefix = "ho"

// Query runs one read-only statement against the leg's database (the one its
// store writes) and returns every row, each value as text and nil for SQL
// NULL. Run casts every column to text in the statement itself, so a leg's
// driver never decides how a value is formatted.
type Query func(ctx context.Context, query string) ([][]*string, error)

// Step is one mutation of the scenario, named for failure messages and for
// the golden's step list.
type Step struct {
	Name string
	Run  func(ctx context.Context) error
}

// Leg is what a backend hands Run. Its store is constructed the way the leg's
// tests construct one and is never handed SetVersionedHistoryEnabled, so
// "history off" is the default a store starts with rather than a switch
// turned off (FR-7).
type Leg struct {
	// Name selects the golden: golden/<Name>.json.
	Name string
	// Roles is the leg's role-accessor surface. Every leg drives the same
	// SharedSteps through it.
	Roles Roles
	// Wake runs the leg's lazy defer-wake sweep, the one write no role
	// exposes.
	Wake func(ctx context.Context) error
	// Extra are the leg's own steps, run after the shared ones: the store
	// legs' storage.DoltStorage methods (DoltStorageSteps), the unit-of-work
	// leg's domain upsert.
	Extra []Step
	// Query reads the leg's database.
	Query Query
}

// GoldenDirEnv switches Run from checking to recording: set to a directory,
// Run writes the leg's golden there instead of comparing, with a full dump
// of the rows it digested beside it (<leg>.full.json.txt) for diffing two
// recordings by hand. RecordedFromEnv names the tree being recorded from;
// Run copies it into the golden. When to re-record is under "Maintaining the
// goldens" in the package doc.
const (
	GoldenDirEnv    = "HISTORYOFFEQUIV_GOLDEN_DIR"
	RecordedFromEnv = "HISTORYOFFEQUIV_RECORDED_FROM"
)

// schemaAddition is schema added since the pre-stack code the goldens are
// anchored to that flag-off storage leaves untouched, grouped by the change
// that added it.
type schemaAddition struct {
	// By names the change, for failure messages.
	By string
	// Columns are its nullable columns, by table.
	Columns map[string][]string
	// Tables are its tables.
	Tables []string
	// Migrations are the migration runner's bookkeeping rows for its own
	// migrations, by bookkeeping table (each keyed by its version column).
	Migrations map[string][]string
}

// schemaAdditions is THE ONLY DIFFERENCE from the goldens this test
// tolerates. The rule, applied to every run before any table is digested,
// whether the run records a golden or checks one:
//
//  1. A listed column, where the database has it, must be NULL in every
//     row; it is then left out of its table's dump, so the rest of every row
//     still has to match the golden byte for byte. A listed column holding
//     any value fails: with versioned history off nothing may write it.
//  2. A listed table, where the database has it, must be empty; it is then
//     left out.
//  3. A listed bookkeeping row, where the database has it, must be there
//     once; it is then left out. Every other row of a bookkeeping table is
//     compared like any table's.
//
// After that, checking demands the golden's tables and columns exactly, and
// every table's digest over the golden's columns must equal the golden's.
// Nothing else is excused: not a value, not a row, not a row count. A later
// migration whose schema flag-off leaves untouched appends its own entry
// here (see "Maintaining the goldens" in the package doc).
var schemaAdditions = []schemaAddition{{
	By: "the versioned-history stack (#6943, #6661, #6664)",
	Columns: map[string][]string{
		// Migration 0070: the participation_generation write fence (#6943).
		// A create with history off leaves it NULL, indistinguishable from
		// a legacy row (design §16.2b, FR-7).
		"issues": {"participation_generation"},
		// Migration 0070 mirrors it on the wisps table, where only a
		// demotion made with history on writes it, to carry the row's
		// participation through the wisps plane (carryParticipationInTx).
		"wisps": {"participation_generation"},
		// Migration 0071 (#6661).
		"issue_versions": {"removed_restriction"},
	},
	// Migration 0072 (#6664).
	Tables: []string{"epoch_minted_addresses"},
	// 0070-0072 on the main series, and ignored/0028 (the wisps half of
	// 0070) on the dolt_ignored series.
	Migrations: map[string][]string{
		"schema_migrations":         {"70", "71", "72"},
		"ignored_schema_migrations": {"28"},
	},
}, {
	By: "arrival_seq, the events journal's create stamp (BD-5)",
	Columns: map[string][]string{
		// Migration 0073: carried on the replicated plane for shape only and
		// never written there.
		"issues": {"arrival_seq"},
		// Migration 0073 and ignored/0029: written only by the events
		// journal's create seam (issueops.stampArrivalSeqInTx), which runs
		// only while the workspace journals, so it stays NULL here.
		"wisps": {"arrival_seq"},
	},
	// 0073 on the main series, and ignored/0029 (its wisps twin, with the
	// unique index and the seq-counter raise) on the dolt_ignored series.
	Migrations: map[string][]string{
		"schema_migrations":         {"73"},
		"ignored_schema_migrations": {"29"},
	},
}}

// runWindow widens the span of the run on each side to bound the wall-clock
// instants the scenario's writes stamp. It is generous so that no time zone a
// DATETIME may be read back in, and no rounding of a sub-second instant, can
// put such an instant outside it. The scenario's own timestamps are decades
// away (fixedAt), so a column whose value moved from a scenario-supplied
// instant to "now" (or back) still shows up in the digest.
const runWindow = 36 * time.Hour

// Run applies the shared scenario and the leg's extra steps, then checks
// every table against the leg's golden (or records it).
func Run(t *testing.T, leg Leg) {
	t.Helper()
	if leg.Name == "" || leg.Roles == nil || leg.Wake == nil || leg.Query == nil {
		t.Fatalf("historyoffequiv.Run: leg %q needs a Name, Roles, Wake and Query", leg.Name)
	}
	ctx := context.Background()
	before := doltLogCount(t, ctx, leg.Query)
	start := time.Now().UTC()
	steps := append(SharedSteps(leg.Roles, leg.Wake), leg.Extra...)
	var names []string
	for _, step := range steps {
		if err := step.Run(ctx); err != nil {
			t.Fatalf("step %q: %v", step.Name, err)
		}
		names = append(names, step.Name)
	}
	end := time.Now().UTC()
	got := setAsideAdditions(t, dumpAll(t, ctx, leg.Query, start.Add(-runWindow), end.Add(runWindow), before))
	got.Steps = names

	if dir := os.Getenv(GoldenDirEnv); dir != "" {
		if t.Failed() {
			t.Fatalf("not recording golden/%s.json: the run breaks the schemaAdditions rule (above)", leg.Name)
		}
		writeGolden(t, dir, leg.Name, got)
		return
	}
	checkGolden(t, leg.Name, got)
}

// dump is every table's normalized rows.
type dump struct {
	Steps  []string
	Tables map[string]tableDump
}

type tableDump struct {
	Columns []string
	Rows    [][]*string
}

type golden struct {
	RecordedFrom string                 `json:"recorded_from"`
	Steps        []string               `json:"steps"`
	Tables       map[string]goldenTable `json:"tables"`
}

type goldenTable struct {
	Columns []string `json:"columns"`
	Rows    int      `json:"rows"`
	SHA256  string   `json:"sha256"`
}

//go:embed golden/*.json
var goldenFS embed.FS

// setAsideAdditions applies the schemaAdditions rule to a run's dump: it
// checks every listed column, table and bookkeeping row the database has,
// reporting each breach, and returns the dump without them. Recording and
// checking both digest only what it returns.
func setAsideAdditions(t *testing.T, d dump) dump {
	t.Helper()
	columns := map[string]map[string]string{} // table -> column -> By
	tables := map[string]string{}
	migrations := map[string]map[string]string{} // table -> version -> By
	for _, a := range schemaAdditions {
		for table, cols := range a.Columns {
			for _, c := range cols {
				if columns[table] == nil {
					columns[table] = map[string]string{}
				}
				columns[table][c] = a.By
			}
		}
		for _, table := range a.Tables {
			tables[table] = a.By
		}
		for table, versions := range a.Migrations {
			for _, v := range versions {
				if migrations[table] == nil {
					migrations[table] = map[string]string{}
				}
				migrations[table][v] = a.By
			}
		}
	}

	out := dump{Steps: d.Steps, Tables: map[string]tableDump{}}
	for _, table := range sortedKeys(d.Tables) {
		td := d.Tables[table]
		if by, ok := tables[table]; ok {
			if len(td.Rows) != 0 {
				t.Errorf("table %s, added by %s, has %d rows with versioned history off, want none:\n%s",
					table, by, len(td.Rows), formatRows(td.Columns, td.Rows))
			}
			continue
		}
		var keep []int
		for i, c := range td.Columns {
			by, listed := columns[table][c]
			if !listed {
				keep = append(keep, i)
				continue
			}
			for _, row := range td.Rows {
				if row[i] != nil {
					t.Errorf("%s.%s, added by %s, holds %q with versioned history off, want NULL in every row; row: %s",
						table, c, by, *row[i], formatRow(row))
				}
			}
		}
		kept := tableDump{Columns: make([]string, len(keep))}
		for j, i := range keep {
			kept.Columns[j] = td.Columns[i]
		}
		version := slices.Index(kept.Columns, "version")
		if migrations[table] != nil && version < 0 {
			t.Errorf("bookkeeping table %s has no version column: %v", table, td.Columns)
		}
		seen := map[string]int{}
		for _, row := range td.Rows {
			projected := make([]*string, len(keep))
			for j, i := range keep {
				projected[j] = row[i]
			}
			if version >= 0 && projected[version] != nil {
				if _, listed := migrations[table][*projected[version]]; listed {
					seen[*projected[version]]++
					continue
				}
			}
			kept.Rows = append(kept.Rows, projected)
		}
		for _, v := range sortedKeys(seen) {
			if seen[v] != 1 {
				t.Errorf("%s has %d rows for migration %s, added by %s, want at most 1", table, seen[v], v, migrations[table][v])
			}
		}
		sortRows(kept.Rows)
		out.Tables[table] = kept
	}
	return out
}

// checkGolden compares a set-aside dump with golden/<leg>.json.
func checkGolden(t *testing.T, leg string, got dump) {
	t.Helper()
	b, err := goldenFS.ReadFile("golden/" + leg + ".json")
	if err != nil {
		t.Fatalf("no golden for leg %q: record one with %s (see the package doc): %v", leg, GoldenDirEnv, err)
	}
	var want golden
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatalf("golden/%s.json: %v", leg, err)
	}
	from := fmt.Sprintf("golden/%s.json (recorded from %s)", leg, want.RecordedFrom)
	if !slices.Equal(got.Steps, want.Steps) {
		t.Fatalf("the scenario's steps differ from those %s ran, so its digests no longer describe this scenario; "+
			"re-record it (\"Maintaining the goldens\" in the package doc):\ngot:  %q\nwant: %q",
			from, got.Steps, want.Steps)
	}
	unexplained := "; a migration whose schema flag-off leaves untouched lists it in schemaAdditions, " +
		"and a change to what flag-off writes re-records the goldens (\"Maintaining the goldens\" in the package doc)"
	for _, table := range sortedKeys(got.Tables) {
		if _, ok := want.Tables[table]; !ok {
			t.Errorf("table %s is not in %s and not in schemaAdditions%s:\n%s",
				table, from, unexplained, formatRows(got.Tables[table].Columns, got.Tables[table].Rows))
		}
	}
	for _, table := range sortedKeys(want.Tables) {
		wt := want.Tables[table]
		gt, ok := got.Tables[table]
		if !ok {
			t.Errorf("table %s is in %s but not in this database", table, from)
			continue
		}
		index := map[string]int{}
		for i, c := range gt.Columns {
			index[c] = i
		}
		ok = true
		for _, c := range wt.Columns {
			if _, present := index[c]; !present {
				t.Errorf("%s.%s is in %s but not in this database", table, c, from)
				ok = false
			}
		}
		for _, c := range gt.Columns {
			if !slices.Contains(wt.Columns, c) {
				t.Errorf("%s.%s is not in %s and not in schemaAdditions%s", table, c, from, unexplained)
				ok = false
			}
		}
		if !ok {
			continue
		}
		projected := make([][]*string, 0, len(gt.Rows))
		for _, row := range gt.Rows {
			p := make([]*string, len(wt.Columns))
			for j, c := range wt.Columns {
				p[j] = row[index[c]]
			}
			projected = append(projected, p)
		}
		sortRows(projected)
		if d := digest(projected); d != wt.SHA256 || len(projected) != wt.Rows {
			t.Errorf("%s differs from %s: got %d rows %s, want %d rows %s; rows now (columns %v):\n%s",
				table, from, len(projected), d, wt.Rows, wt.SHA256, wt.Columns, formatRows(wt.Columns, projected))
		}
	}
}

// writeGolden records a set-aside dump as golden/<leg>.json in dir.
func writeGolden(t *testing.T, dir, leg string, d dump) {
	t.Helper()
	g := golden{RecordedFrom: os.Getenv(RecordedFromEnv), Steps: d.Steps, Tables: map[string]goldenTable{}}
	if g.RecordedFrom == "" {
		t.Fatalf("%s is set but %s is not: name the tree this golden is recorded from", GoldenDirEnv, RecordedFromEnv)
	}
	full := map[string][]string{}
	for table, td := range d.Tables {
		g.Tables[table] = goldenTable{Columns: td.Columns, Rows: len(td.Rows), SHA256: digest(td.Rows)}
		full[table] = append(full[table], strings.Join(td.Columns, " | "))
		for _, row := range td.Rows {
			full[table] = append(full[table], formatRow(row))
		}
	}
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, leg+".json"), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	fb, _ := json.MarshalIndent(full, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, leg+".full.json.txt"), fb, 0o600)
}

func digest(rows [][]*string) string {
	h := sha256.New()
	for _, row := range rows {
		h.Write([]byte(formatRow(row)))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func formatRow(row []*string) string {
	parts := make([]string, len(row))
	for i, v := range row {
		if v == nil {
			parts[i] = "NULL"
		} else {
			parts[i] = strconv.Quote(*v)
		}
	}
	return strings.Join(parts, " ")
}

func formatRows(columns []string, rows [][]*string) string {
	lines := []string{"  " + strings.Join(columns, " | ")}
	for _, row := range rows {
		lines = append(lines, "  "+formatRow(row))
	}
	return strings.Join(lines, "\n")
}

func sortRows(rows [][]*string) {
	sort.SliceStable(rows, func(i, j int) bool { return formatRow(rows[i]) < formatRow(rows[j]) })
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SQLQuery adapts a leg that reads through database/sql. open returns a
// handle on the leg's database and a function that releases it.
func SQLQuery(open func(ctx context.Context) (*sql.DB, func(), error)) Query {
	return func(ctx context.Context, query string) ([][]*string, error) {
		db, release, err := open(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		var out [][]*string
		for rows.Next() {
			raw := make([]sql.NullString, len(cols))
			dest := make([]any, len(cols))
			for i := range raw {
				dest[i] = &raw[i]
			}
			if err := rows.Scan(dest...); err != nil {
				return nil, err
			}
			row := make([]*string, len(cols))
			for i, v := range raw {
				if v.Valid {
					s := v.String
					row[i] = &s
				}
			}
			out = append(out, row)
		}
		return out, rows.Err()
	}
}

// AnyRows converts rows of driver values (a raw-SQL use case's result) to
// Query's shape.
func AnyRows(rows [][]any) [][]*string {
	out := make([][]*string, 0, len(rows))
	for _, r := range rows {
		row := make([]*string, len(r))
		for i, v := range r {
			switch v := v.(type) {
			case nil:
			case []byte:
				s := string(v)
				row[i] = &s
			case string:
				s := v
				row[i] = &s
			default:
				s := fmt.Sprint(v)
				row[i] = &s
			}
		}
		out = append(out, row)
	}
	return out
}
