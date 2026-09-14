package bdpwire

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The package is stdlib-only by design (doc.go), and this is what enforces it
// rather than requesting it: .golangci.yml's depguard covers the boundaries
// internal/httpapi already has, but a rule for this package would live outside
// it, and this slice changes nothing outside it. A standard-library import
// path has no dot in its first element; anything else is a module.
func TestPackageImportsOnlyTheStandardLibrary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") {
				offenders = append(offenders, name+": "+path)
			}
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("non-stdlib imports in bdpwire:\n  %s\nbdpwire is a leaf beneath internal/httpapi and a future client; it must not import graphops, internal/storage, or any module — see doc.go",
			strings.Join(offenders, "\n  "))
	}
}
