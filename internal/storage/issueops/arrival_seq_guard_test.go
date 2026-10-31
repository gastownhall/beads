package issueops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dolthub/vitess/go/vt/sqlparser"
)

// Structural guards for arrival_seq, the create sequence the journal stamps on
// a wisp row (types.Issue.ArrivalSeq). Its whole contract — one writer, never
// client-settable, never moved — rests on WHERE the column may be written, so
// these hold that by reading the source rather than by trusting each new
// statement to remember.

// arrivalSeqWriters are the only places SQL that writes arrival_seq may live,
// keyed by path relative to internal/storage, each with the enclosing
// top-level declarations allowed to hold it ("" is a package-level const or
// var, which is where the DDL lives).
var arrivalSeqWriters = map[string]map[string]bool{
	// The stamp itself, and the heal that raises the counter past the stamps.
	"issueops/journal.go": {"stampArrivalSeqInTx": true, "healEventSeqCounter": true},
	// The CLI-bundle substitute for migration 0073.
	"schema/cli_migrations.go": {"": true},
}

// sqlWriteVerbRe is the conservative fallback for SQL-looking literals the
// tokenizer cannot finish, including incomplete dynamically formatted SQL.
var sqlWriteVerbRe = regexp.MustCompile(`(?i)\b(?:UPDATE|INSERT|REPLACE|ALTER|CREATE)\b`)

// writesArrivalSeq recognizes a write keyword and the column identifier in
// the same SQL statement. Tokenization excludes ordinary comments and quoted
// values, but retains backtick identifiers and executable MySQL comments.
// Keywords can occur after format verbs or inside statement fragments. SQL
// tokens also enable a conservative fallback on lexical errors; prose such as
// "arrival_seq, the journal's create stamp" errors before reaching one.
// This remains a literal-level guard: it does not reconstruct SQL assembled
// across literals or prove that the column is an assignment target.
func writesArrivalSeq(text string) bool {
	if !strings.Contains(strings.ToLower(text), "arrival_seq") {
		return false
	}
	tokens := sqlparser.NewStringTokenizer(text)
	sql, write, column := false, false, false
	for {
		kind, value := tokens.Scan()
		switch kind {
		case 0:
			return false
		case sqlparser.COMMENT:
			continue
		case ';':
			sql, write, column = false, false, false
			continue
		case sqlparser.LEX_ERROR:
			// A lexical error is not proof of safety. Preserve the old
			// conservative check once the literal looks like SQL.
			return sql && sqlWriteVerbRe.MatchString(text)
		}
		switch kind {
		case sqlparser.UPDATE, sqlparser.INSERT, sqlparser.REPLACE,
			sqlparser.ALTER, sqlparser.CREATE, sqlparser.SELECT,
			sqlparser.WITH, sqlparser.EXPLAIN, sqlparser.ON, '(':
			sql = true
		}
		switch kind {
		case sqlparser.UPDATE, sqlparser.INSERT, sqlparser.REPLACE, sqlparser.ALTER, sqlparser.CREATE:
			write = true
		case sqlparser.ID:
			column = column || strings.EqualFold(string(value), "arrival_seq")
		}
		if write && column {
			return true
		}
	}
}

func TestWritesArrivalSeq(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		{"reported prose", "arrival_seq, the events journal's create stamp (BD-5)", false},
		{"column list fragment", "id, title, arrival_seq", false},
		{"read", "SELECT arrival_seq FROM wisps", false},
		{"comment only", "/* UPDATE wisps SET arrival_seq = 1 */", false},
		{"leading comment", "/* arrival_seq CREATE */ SELECT 1", false},
		{"line comment", "-- UPDATE wisps SET arrival_seq = 1\nSELECT 1", false},
		{"hash comment", "# UPDATE wisps SET arrival_seq = 1\nSELECT 1", false},
		{"column in comment", "UPDATE wisps SET title = ? /* arrival_seq */", false},
		{"write in comment", "SELECT arrival_seq /* UPDATE */ FROM wisps", false},
		{"column in value", "UPDATE wisps SET title = 'arrival_seq'", false},
		{"write in value", "SELECT 'UPDATE', arrival_seq FROM wisps", false},
		{"both in value", "SELECT 'UPDATE arrival_seq'", false},
		{"double quoted value", `UPDATE wisps SET title = "arrival_seq"`, false},
		{"escaped value", `UPDATE wisps SET title = 'it\'s arrival_seq'`, false},
		{"doubled quote", "UPDATE wisps SET title = 'it''s arrival_seq'", false},
		{"longer identifier", "UPDATE wisps SET old_arrival_seq = 1", false},
		{"unrelated statements", "SELECT arrival_seq FROM wisps; UPDATE wisps SET title = ?", false},
		// These unauthorized writes are negative controls: making the
		// classifier always return false must fail these fixtures.
		{"update", "UPDATE wisps SET arrival_seq = ?", true},
		{"insert", "INSERT INTO wisps (id, arrival_seq) VALUES (?, ?)", true},
		{"replace", "REPLACE INTO wisps (id, arrival_seq) VALUES (?, ?)", true},
		{"alter", "ALTER TABLE wisps ADD COLUMN arrival_seq BIGINT", true},
		{"create table", "CREATE TABLE wisps (arrival_seq BIGINT)", true},
		{"create index", "CREATE UNIQUE INDEX stamp ON wisps (arrival_seq)", true},
		{"backtick identifier", "UPDATE `wisps` SET `arrival_seq` = ?", true},
		{"mixed case", "  update wisps set ARRIVAL_SEQ = ?", true},
		{"comment before write", "/* note */ -- note\nUPDATE wisps SET arrival_seq = ?", true},
		{"cte update", "WITH ids AS (SELECT id FROM wisps) UPDATE wisps SET arrival_seq = ?", true},
		{"dynamic table", "UPDATE %s SET arrival_seq = ?", true},
		{"format prefix update", "%s UPDATE wisps SET arrival_seq = ?", true},
		{"format prefix insert", "%s INSERT INTO wisps (arrival_seq) VALUES (?)", true},
		{"format prefix replace", "%s REPLACE INTO wisps (arrival_seq) VALUES (?)", true},
		{"format prefix alter", "%s ALTER TABLE wisps ADD arrival_seq BIGINT", true},
		{"format prefix create", "%s CREATE TABLE wisps (arrival_seq BIGINT)", true},
		{"closing paren upsert fragment", ") ON DUPLICATE KEY UPDATE arrival_seq = VALUES(arrival_seq)", true},
		{"values upsert fragment", "VALUES (?, ?) ON DUPLICATE KEY UPDATE arrival_seq = ?", true},
		{"alias upsert fragment", "AS incoming ON DUPLICATE KEY UPDATE arrival_seq = incoming.arrival_seq", true},
		{"duplicate key fragment", "DUPLICATE KEY UPDATE arrival_seq = ?", true},
		{"into upsert fragment", "INTO wisps (arrival_seq) VALUES (?) ON DUPLICATE KEY UPDATE arrival_seq = ?", true},
		{"comma values fragment", ", (?) ON DUPLICATE KEY UPDATE arrival_seq = ?", true},
		{"format prefix comment", "%s /* UPDATE arrival_seq */ SELECT 1", false},
		{"format prefix quoted column", "%s UPDATE wisps SET title = 'arrival_seq'", false},
		{"fragment quoted write", "VALUES ('UPDATE'), arrival_seq", false},
		{"fragment separate statements", "VALUES (arrival_seq); UPDATE wisps SET title = ?", false},
		{"dynamic upsert suffix", "ON DUPLICATE KEY UPDATE arrival_seq = VALUES(arrival_seq)", true},
		{"incomplete write", "INSERT INTO wisps (arrival_seq,", true},
		{"lexical error before column", "UPDATE \x00 wisps SET arrival_seq = ?", true},
		{"unterminated value", "UPDATE wisps SET title = 'arrival_seq", true},
		{"later statement", "SELECT 1; UPDATE wisps SET arrival_seq = ?", true},
		{"ddl bundle", "ALTER TABLE issues ADD COLUMN arrival_seq BIGINT; ALTER TABLE wisps ADD COLUMN arrival_seq BIGINT; CREATE UNIQUE INDEX stamp ON wisps (arrival_seq)", true},
		{"executable comment", "/*! UPDATE wisps SET arrival_seq = 1 */", true},
		{"counter heal", "UPDATE bd_events_seq SET next_seq = GREATEST(next_seq, (SELECT MAX(arrival_seq) FROM wisps))", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := writesArrivalSeq(tc.sql); got != tc.want {
				t.Errorf("writesArrivalSeq(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

// TestArrivalSeqIsWrittenOnlyByTheJournalSeam scans every non-test Go file
// under internal/storage for SQL-looking literals that name arrival_seq as
// an identifier and carry a write keyword. It fails on any outside
// arrivalSeqWriters, and proves every allowed writer was still reached.
func TestArrivalSeqIsWrittenOnlyByTheJournalSeam(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var scanned int
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		scanned++
		if rel == "historyoffequiv/historyoffequiv.go" {
			t.Logf("scanning reported prose in %s", rel)
		}
		for _, decl := range file.Decls {
			owner := ""
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					text = lit.Value
				}
				if !writesArrivalSeq(text) {
					return true
				}
				if arrivalSeqWriters[rel][owner] {
					seen[rel+":"+owner] = true
					return true
				}
				t.Errorf("%s: SQL writing arrival_seq in %s (%q): only the journal's create seam writes it "+
					"(issueops.stampArrivalSeqInTx), so no caller-supplied value can ever be stored",
					fset.Position(lit.Pos()), rel, owner)
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning internal/storage: %v", err)
	}
	if scanned == 0 {
		t.Fatal("scanned no Go files under internal/storage; the guard is not running")
	}
	t.Logf("scanned %d non-test storage Go files", scanned)
	// Each allowed writer must still be one, so the allowlist cannot outlive
	// the code it names — and a missing one proves the scan did not reach it
	// (under Bazel, a package dropped from this test's data).
	for file, owners := range arrivalSeqWriters {
		for owner := range owners {
			if !seen[file+":"+owner] {
				t.Errorf("allowed arrival_seq writer %s:%q writes no arrival_seq SQL (or was not scanned); remove it or restore the scan", file, owner)
			}
		}
	}
}

// TestIssueInsertAndUpsertColumnsExcludeArrivalSeq pins the generic row write
// out of the column: every create, import and plane move funnels through
// issueInsertSQL (and its upsert clause), so the column's absence there is
// what keeps a value on the Go struct from ever being stored, and what clears
// it on a row moved between planes.
func TestIssueInsertAndUpsertColumnsExcludeArrivalSeq(t *testing.T) {
	for _, table := range []string{"issues", "wisps"} {
		for _, stale := range []bool{false, true} {
			if sql := issueInsertSQL(table, 2, "ON DUPLICATE KEY UPDATE "+issueUpsertAssignments(table, stale)); strings.Contains(sql, "arrival_seq") {
				t.Errorf("the %s insert/upsert (reject stale %v) names arrival_seq:\n%s", table, stale, sql)
			}
		}
	}
	for _, col := range issueUpsertColumns {
		if col == "arrival_seq" {
			t.Error("issueUpsertColumns lists arrival_seq")
		}
	}
}

// TestOnlyRecordEventInTxRecordsCreates pins the stamp's single entry: the
// only call to insertEventRow that may ask for a stamp, or record a create, is
// the one in recordIssueEventInTx (RecordEventInTx's body, which
// RecordCreateEventInTx shares) — where the decision "a create of an unstamped
// wisp row" is made once. A second emitter recording creates would either
// stamp under rules of its own or journal creates that never stamp.
func TestOnlyRecordEventInTxRecordsCreates(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "journal.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); !ok || ident.Name != "insertEventRow" {
				return true
			}
			calls++
			if len(call.Args) != 9 {
				t.Fatalf("%s: insertEventRow takes 9 arguments here, got %d — update this guard with the signature", fset.Position(call.Pos()), len(call.Args))
			}
			if fn.Name.Name == "recordIssueEventInTx" {
				return true
			}
			if op, ok := call.Args[2].(*ast.Ident); ok && op.Name == "EventCreate" {
				t.Errorf("%s: %s records a create; only recordIssueEventInTx may", fset.Position(call.Pos()), fn.Name.Name)
			}
			if stamp, ok := call.Args[8].(*ast.Ident); !ok || stamp.Name != "false" {
				t.Errorf("%s: %s asks insertEventRow for a stamp; only recordIssueEventInTx may", fset.Position(call.Pos()), fn.Name.Name)
			}
			return true
		})
	}
	if calls < 2 {
		t.Fatalf("found %d insertEventRow calls in journal.go; the guard is not seeing the emitters", calls)
	}
}
