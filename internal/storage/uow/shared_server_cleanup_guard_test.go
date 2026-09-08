package uow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
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

// errorAssertions mark a constructor call the test expects to FAIL. The
// *_ValidationErrors tests call a provider constructor with deliberately
// invalid arguments and assert on the rejection; the constructor returns
// before it starts anything, so those functions have no daemon to clean up.
var errorAssertions = map[string]bool{
	"Error":         true, // require.Error / assert.Error
	"ErrorContains": true,
}

// TestEveryServerFixtureRegistersVerifiedCleanup is the structural half of
// this package's leak defense.
//
// verifiedShutdownCleanup only helps the fixtures that call it, and the leak
// it exists to stop (wy-j2zc8q) came back three times precisely because the
// cleanup idiom was copy-pasted per fixture: nine near-identical t.Cleanup
// blocks, any of which could be written slightly differently — or omitted —
// in a tenth fixture without anything noticing until a dev box's process
// table showed a detached sql-server serving a directory deleted hours
// earlier. So the rule is checked against this package's own source.
//
// Three rules, over the functions that take a *testing.T (only those can
// register a cleanup at all — a harness METHOD like openProvider reuses a
// root whose owner already registered one, so it is not the place to look):
//
//	A. shutdownOnInterrupt and verifiedShutdownCleanup come as a pair. They
//	   cover the two ways a fixture's server outlives it — a signal, and a
//	   returning run — and a fixture copied from another with one half
//	   dropped is the realistic regression.
//	B. Only the two sanctioned helpers call proxy.Shutdown; every fixture
//	   goes through verifiedShutdownCleanup, which also asserts the pid died.
//	C. A function that calls a provider constructor registers the cleanup,
//	   unless it asserts the constructor REJECTED its arguments.
//
// What it does NOT catch: a fixture that provisions a workspace through some
// future path this file does not name. That is what the non-vacuity counters
// at the bottom are for — they fail if a rule stopped matching anything,
// rather than letting the guard go quietly green on a package it no longer
// understands.
func TestEveryServerFixtureRegistersVerifiedCleanup(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package sources: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("no test sources parsed; every rule below would pass vacuously")
	}

	var paired, provisioning int
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			base := filepath.Base(path)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				name := fn.Name.Name
				calls := calledNames(fn)

				// Rule B applies to every function, helper or not.
				if calls["Shutdown"] && !shutdownCallers[name] {
					t.Errorf("%s: %s calls proxy.Shutdown directly; use verifiedShutdownCleanup(t, storeRootDir), "+
						"which also asserts the dolt sql-server pid is gone (wy-j2zc8q)", base, name)
				}
				if !takesTestingT(fn) || shutdownCallers[name] {
					continue
				}

				// Rule A.
				switch {
				case calls["shutdownOnInterrupt"] && !calls["verifiedShutdownCleanup"]:
					t.Errorf("%s: %s guards a store root with shutdownOnInterrupt but never registers "+
						"verifiedShutdownCleanup(t, storeRootDir); its dolt sql-server survives a run that "+
						"ends normally (wy-j2zc8q)", base, name)
				case calls["verifiedShutdownCleanup"] && !calls["shutdownOnInterrupt"]:
					t.Errorf("%s: %s registers verifiedShutdownCleanup but never calls "+
						"shutdownOnInterrupt(t, storeRootDir); its dolt sql-server survives Ctrl-C (wy-j2zc8q)",
						base, name)
				case calls["shutdownOnInterrupt"]:
					paired++
				}

				// Rule C.
				if !callsAny(calls, providerConstructors) {
					continue
				}
				if callsAny(calls, errorAssertions) {
					continue // asserts the constructor rejected its arguments
				}
				provisioning++
				if !calls["verifiedShutdownCleanup"] {
					t.Errorf("%s: %s provisions a shared-server workspace but never calls "+
						"verifiedShutdownCleanup(t, storeRootDir); a detached dolt sql-server will outlive "+
						"the run (wy-j2zc8q)", base, name)
				}
			}
		}
	}

	// Non-vacuity. Each rule is an assertion about functions that exist; if
	// the fixtures were renamed out from under the name sets above, every
	// loop body would stop running and the guard would pass while watching
	// nothing.
	if paired == 0 {
		t.Fatal("no function pairs shutdownOnInterrupt with verifiedShutdownCleanup; rule A matched nothing")
	}
	if provisioning == 0 {
		t.Fatalf("no *testing.T function calls any of %v; rule C is watching constructors this package "+
			"no longer uses", sortedKeys(providerConstructors))
	}
}

// takesTestingT reports whether fn accepts a *testing.T. Only such a function
// can register a t.Cleanup, so only such a function can be held to the rules
// above: openProvider and friends are methods on a harness whose constructor
// already owns the root's cleanup.
func takesTestingT(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "testing" && sel.Sel.Name == "T" {
			return true
		}
	}
	return false
}

// calledNames returns the set of function names called anywhere in fn's body,
// by their final identifier: both `Shutdown` from `proxy.Shutdown(...)` and
// the bare `verifiedShutdownCleanup(...)`. Calls inside closures — the
// t.Run(…, func(t *testing.T){…}) subtests these fixtures use — are included,
// and attributed to the enclosing declaration.
//
// Matching the selector's last segment rather than the full `proxy.Shutdown`
// keeps the guard working if the import is ever aliased, at the price of also
// matching some other package's `Shutdown` — a false positive worth having in
// a rule about not shutting servers down by hand.
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

func callsAny(calls, want map[string]bool) bool {
	for name := range want {
		if calls[name] {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
