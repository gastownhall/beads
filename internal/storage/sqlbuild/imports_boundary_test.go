package sqlbuild_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePrefix = "github.com/steveyegge/beads/"

// TestImportBoundaries pins the layering around the shared SQL-builder seam
// (bd-6dnrw.46): sqlbuild is the bottom shared layer under both stacks, and
// sqlbuild imports domain (DefaultInfraTypes), so domain must never import
// sqlbuild or issueops — either edge closes an import cycle and breaks the
// extract-don't-unify structure. Only production files are checked; external
// test packages may import across the seam.
func TestImportBoundaries(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	rules := []struct {
		dir       string   // package dir relative to repo root
		allowed   []string // if set, beads-internal imports must be exactly one of these
		forbidden []string // if set, these beads-internal imports must not appear
	}{
		{
			dir: "internal/storage/sqlbuild",
			allowed: []string{
				"internal/storage",
				"internal/storage/domain",
				"internal/types",
			},
		},
		{
			dir: "internal/storage/domain",
			forbidden: []string{
				"internal/storage/issueops",
				"internal/storage/sqlbuild",
			},
		},
	}

	for _, rule := range rules {
		dir := filepath.Join(repoRoot, rule.dir)
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", rule.dir, err)
		}

		allowed := make(map[string]bool, len(rule.allowed))
		for _, a := range rule.allowed {
			allowed[modulePrefix+a] = true
		}

		for _, pkg := range pkgs {
			for filename, file := range pkg.Files {
				for _, imp := range file.Imports {
					path, err := strconv.Unquote(imp.Path.Value)
					if err != nil {
						t.Fatalf("%s: unquote import %s: %v", filename, imp.Path.Value, err)
					}
					if !strings.HasPrefix(path, modulePrefix) {
						continue
					}
					if rule.allowed != nil && !allowed[path] {
						t.Errorf("%s imports %s, outside the allowed set %v for %s",
							filepath.Base(filename), path, rule.allowed, rule.dir)
					}
					for _, f := range rule.forbidden {
						if path == modulePrefix+f {
							t.Errorf("%s imports %s, forbidden in %s (closes a cycle through sqlbuild -> domain)",
								filepath.Base(filename), path, rule.dir)
						}
					}
				}
			}
		}
	}
}
