package uow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// providerConstructors are the calls that make this package provision a
// shared-server workspace: each one starts, or attaches to, a detached
// `dolt sql-server` whose data directory is a t.TempDir().
var providerConstructors = map[string]bool{
	"NewDoltServerUOWProvider":         true,
	"NewExternalDoltServerUOWProvider": true,
}

// shutdownCallers are the only functions allowed to call proxy.Shutdown
// directly. Everything else goes through verifiedShutdownCleanup, which is
// the one that also asserts the daemon is gone.
var shutdownCallers = map[string]bool{
	"verifiedShutdownCleanup": true,
	// shutdownOnInterrupt is the SIGINT/SIGTERM handler: it runs in a
	// goroutine on its way to os.Exit, where no assertion could be reported
	// and no *testing.T is still live to report it to.
	"shutdownOnInterrupt": true,
}

// TestEveryServerFixtureRegistersVerifiedCleanup is the structural half of
// this package's leak defense.
//
// verifiedShutdownCleanup only helps the fixtures that call it, and the
// leak it exists to stop (wy-j2zc8q) came back three times precisely because
// the cleanup idiom was copy-pasted per fixture: nine near-identical
// t.Cleanup blocks, any of which could be written slightly differently — or
// omitted — in a tenth fixture without anything noticing until a dev box's
// process table showed a detached sql-server serving a directory deleted
// hours earlier.
//
// So the rule is checked against this package's own source: a test function
// that provisions a shared-server workspace must register the verified
// cleanup, and nothing outside the two sanctioned helpers may call
// proxy.Shutdown itself.
func TestEveryServerFixtureRegistersVerifiedCleanup(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package sources: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("no test sources parsed; the guard would pass vacuously")
	}

	provisioners := 0
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				calls := calledNames(fn)
				name := fn.Name.Name

				if calls["Shutdown"] && !shutdownCallers[name] {
					t.Errorf("%s: %s calls proxy.Shutdown directly; use verifiedShutdownCleanup(t, storeRootDir), "+
						"which also asserts the dolt sql-server pid is gone (wy-j2zc8q)",
						filepath.Base(path), name)
				}

				provisions := false
				for ctor := range providerConstructors {
					if calls[ctor] {
						provisions = true
						break
					}
				}
				if !provisions {
					continue
				}
				provisioners++
				if !calls["verifiedShutdownCleanup"] {
					t.Errorf("%s: %s provisions a shared-server workspace but never calls "+
						"verifiedShutdownCleanup(t, storeRootDir); a detached dolt sql-server will outlive the run (wy-j2zc8q)",
						filepath.Base(path), name)
				}
			}
		}
	}

	// Without this the guard would pass on a package whose fixtures had all
	// been renamed out from under providerConstructors — a green light for
	// exactly the thing it is watching.
	if provisioners == 0 {
		t.Fatalf("no function calls any of %v; the guard is looking for constructors this package no longer uses",
			sortedKeys(providerConstructors))
	}
}

// calledNames returns the set of function names called anywhere in fn's body,
// by their final identifier: both `Shutdown` from `proxy.Shutdown(...)` and
// the bare `verifiedShutdownCleanup(...)`.
//
// Matching the selector's last segment rather than the full `proxy.Shutdown`
// keeps the guard working if the import is ever aliased, at the price of also
// matching some other package's `Shutdown` — which is a false positive worth
// having in a rule about not shutting servers down by hand.
func calledNames(fn *ast.FuncDecl) map[string]bool {
	names := make(map[string]bool)
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			names[f.Name] = true
		case *ast.SelectorExpr:
			names[f.Sel.Name] = true
		}
		return true
	})
	return names
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
