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

// sqlWriteVerbRe matches a statement that changes data or schema.
var sqlWriteVerbRe = regexp.MustCompile(`(?i)\b(?:UPDATE|INSERT|REPLACE|ALTER|CREATE)\b`)

// TestArrivalSeqIsWrittenOnlyByTheJournalSeam scans every non-test Go file
// under internal/storage for a string literal that both names arrival_seq and
// carries a write verb, and fails on any outside arrivalSeqWriters. A read
// (the column lists the scanners use) names no write verb, so it passes; a new
// INSERT, upsert or UPDATE that binds the column — the way a caller-supplied
// value would reach storage — does not.
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
				if !strings.Contains(strings.ToLower(text), "arrival_seq") || !sqlWriteVerbRe.MatchString(text) {
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
