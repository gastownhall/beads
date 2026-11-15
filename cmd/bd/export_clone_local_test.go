package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestStripCloneLocalFieldsDropsArrivalSeq pins what an export record carries:
// arrival_seq is the events-journal seq of a row's create record in THIS
// clone, so JSONL interchange — a git-tracked file other clones read — never
// writes it.
func TestStripCloneLocalFieldsDropsArrivalSeq(t *testing.T) {
	issue := &types.Issue{ID: "bd-1", Title: "t", NoHistory: true, ArrivalSeq: 42}
	stripCloneLocalFields(issue)
	raw, err := json.Marshal(&exportIssueRecord{RecordType: "issue", IssueWithCounts: &types.IssueWithCounts{Issue: issue}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "arrival_seq") {
		t.Errorf("an export record carries arrival_seq: %s", raw)
	}
}

// TestEveryExportRecordBuilderStripsCloneLocalFields holds every export path
// to the strip: each function in this package that builds an
// exportIssueRecord must call stripCloneLocalFields, so a new export path
// cannot start writing a clone-local value into interchange unnoticed.
func TestEveryExportRecordBuilderStripsCloneLocalFields(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var builders int
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var builds, strips bool
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CompositeLit:
					if ident, ok := n.Type.(*ast.Ident); ok && ident.Name == "exportIssueRecord" {
						builds = true
					}
				case *ast.CallExpr:
					if ident, ok := n.Fun.(*ast.Ident); ok && ident.Name == "stripCloneLocalFields" {
						strips = true
					}
				}
				return true
			})
			if !builds {
				continue
			}
			builders++
			if !strips {
				t.Errorf("%s: %s builds an exportIssueRecord without calling stripCloneLocalFields", name, fn.Name.Name)
			}
		}
	}
	if builders < 3 {
		t.Fatalf("found %d export record builders; the guard is not seeing the export paths", builders)
	}
}
