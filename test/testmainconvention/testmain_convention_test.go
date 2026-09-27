// Package testmainconvention enforces a structural rule discovered by
// be-5kkk6: a TestMain that both defers testutil.TerminateDoltContainer and
// calls os.Exit in its own function body never runs that cleanup, because
// os.Exit terminates the process immediately and skips every deferred call.
// Three TestMain functions had this shape and leaked 101 containers over six
// days before anything caught it. The fix used elsewhere in this repo is the
// testMainInner pattern: TestMain shrinks to `os.Exit(testMainInner(m))`,
// and testMainInner — not TestMain — holds the defer and returns an int on
// every path, so deferred cleanup runs before the outer os.Exit.
package testmainconvention

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func repoRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

// isTestMain reports whether decl is `func TestMain(m *testing.M)`.
func isTestMain(decl *ast.FuncDecl) bool {
	if decl.Name.Name != "TestMain" || decl.Recv != nil || decl.Body == nil {
		return false
	}
	params := decl.Type.Params
	if params == nil || len(params.List) != 1 {
		return false
	}
	star, ok := params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	return ok && pkgIdent.Name == "testing" && sel.Sel.Name == "M"
}

// isOsExitCall reports whether expr is a call to os.Exit.
func isOsExitCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	return ok && pkgIdent.Name == "os" && sel.Sel.Name == "Exit"
}

// isTerminateDoltContainerCall reports whether expr calls a method or
// function named TerminateDoltContainer, independent of the receiver/package
// identifier so an import alias can't hide a match.
func isTerminateDoltContainerCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "TerminateDoltContainer"
}

// bodyHasDefer reports whether a defer statement anywhere in body's own
// scope (not inside a nested func literal) defers a call matching match.
func bodyHasDefer(body *ast.BlockStmt, match func(ast.Expr) bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		if d, ok := n.(*ast.DeferStmt); ok && match(d.Call) {
			found = true
		}
		return true
	})
	return found
}

// bodyHasCall reports whether a call matching match appears anywhere in
// body's own scope (not inside a nested func literal), whether as a bare
// statement or nested inside another statement such as an if-block.
func bodyHasCall(body *ast.BlockStmt, match func(ast.Expr) bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if _, isLit := n.(*ast.FuncLit); isLit {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && match(call) {
			found = true
		}
		return true
	})
	return found
}

// TestNoTestMainDefersTerminateDoltContainerBeforeOsExit is a repo-wide
// structural regression test for be-5kkk6. See the package doc for the bug.
func TestNoTestMainDefersTerminateDoltContainerBeforeOsExit(t *testing.T) {
	root := repoRoot()
	var violations []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("relativize %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !isTestMain(fn) {
				continue
			}
			hasDefer := bodyHasDefer(fn.Body, isTerminateDoltContainerCall)
			hasExit := bodyHasCall(fn.Body, isOsExitCall)
			if hasDefer && hasExit {
				violations = append(violations, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("TestMain defers TerminateDoltContainer and also calls os.Exit in the same function body — os.Exit skips deferred calls, so the container is never cleaned up. Convert to the testMainInner pattern (TestMain reduces to os.Exit(testMainInner(m)); the defer and every os.Exit call site move inside testMainInner as returns). Violations:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
