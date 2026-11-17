// Package analyzertest runs an analyzer over a small self-contained package
// written to a temporary directory. Unlike x/tools' analysistest it needs no
// `go` command or GOPATH layout, so it works the same under `go test` and in
// a Bazel sandbox.
//
// Imports resolve to empty packages: a fixture may import any path (depguard
// only reads the import declarations), but cannot use what it imports.
package analyzertest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"sort"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

// Diagnostic is a reported finding, positioned by file base name and line.
type Diagnostic struct {
	File    string
	Line    int
	Message string
}

// Run writes files (base name to source) into dir/<pkgPath> of a fresh
// temporary directory, type-checks them as one package, runs a, and returns
// its diagnostics sorted by position.
func Run(t *testing.T, a *analysis.Analyzer, pkgPath string, files map[string]string) []Diagnostic {
	t.Helper()
	dir := filepath.Join(t.TempDir(), filepath.FromSlash(pkgPath))
	return run(t, a, pkgPath, dir, dir, files)
}

// RunInRepo is Run for a package at relDir ("" for the root) of a fresh
// temporary repository. The test's working directory becomes the repository
// root and file names are repository-relative, as in a nogo action, where
// the execution root is the repository root.
func RunInRepo(t *testing.T, a *analysis.Analyzer, relDir string, files map[string]string) []Diagnostic {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	pkgPath := path.Join("example.com/repo", relDir)
	return run(t, a, pkgPath, filepath.Join(root, filepath.FromSlash(relDir)), filepath.FromSlash(relDir), files)
}

func run(t *testing.T, a *analysis.Analyzer, pkgPath, dir, nameDir string, files map[string]string) []Diagnostic {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var syntax []*ast.File
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var paths []string
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(files[name]), 0o600); err != nil {
			t.Fatal(err)
		}
		fileName := filepath.Join(nameDir, name)
		f, err := parser.ParseFile(fset, fileName, files[name], parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		syntax = append(syntax, f)
		paths = append(paths, fileName)
	}
	info := &types.Info{
		Types:        map[ast.Expr]types.TypeAndValue{},
		Instances:    map[*ast.Ident]types.Instance{},
		Defs:         map[*ast.Ident]types.Object{},
		Uses:         map[*ast.Ident]types.Object{},
		Implicits:    map[ast.Node]types.Object{},
		Selections:   map[*ast.SelectorExpr]*types.Selection{},
		Scopes:       map[ast.Node]*types.Scope{},
		FileVersions: map[*ast.File]string{},
	}
	sizes := types.SizesFor("gc", "amd64")
	conf := types.Config{GoVersion: "go1.26", Sizes: sizes, Importer: emptyImporter{}}
	tpkg, err := conf.Check(pkgPath, fset, syntax, info)
	if err != nil {
		t.Fatalf("type-checking %s: %v", pkgPath, err)
	}
	pkg := &packages.Package{
		ID:              pkgPath,
		Name:            tpkg.Name(),
		PkgPath:         pkgPath,
		GoFiles:         paths,
		CompiledGoFiles: paths,
		Fset:            fset,
		Syntax:          syntax,
		Types:           tpkg,
		TypesInfo:       info,
		TypesSizes:      sizes,
		Imports:         map[string]*packages.Package{},
	}
	graph, err := checker.Analyze([]*analysis.Analyzer{a}, []*packages.Package{pkg}, nil)
	if err != nil {
		t.Fatalf("analyzing: %v", err)
	}
	var out []Diagnostic
	for _, act := range graph.Roots {
		if act.Err != nil {
			t.Fatalf("%s: %v", act.Analyzer.Name, act.Err)
		}
		for _, d := range act.Diagnostics {
			p := fset.Position(d.Pos)
			out = append(out, Diagnostic{File: filepath.Base(p.Filename), Line: p.Line, Message: d.Message})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// emptyImporter resolves every import to an empty, complete package.
type emptyImporter struct{}

func (emptyImporter) Import(importPath string) (*types.Package, error) {
	pkg := types.NewPackage(importPath, path.Base(importPath))
	pkg.MarkComplete()
	return pkg, nil
}

// Equal fails t unless got equals want.
func Equal(t *testing.T, got, want []Diagnostic) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("diagnostics = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("diagnostic %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
