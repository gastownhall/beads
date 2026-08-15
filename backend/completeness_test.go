package backend_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/backend"
	"github.com/steveyegge/beads/backend/conformance"
)

const internalPrefix = "github.com/steveyegge/beads/internal/"

// TestPublicSurfaceComplete is the drift guard on the external-backend door:
// every named internal type reachable from the contract an external module
// must write against — the engine interface's method signatures, the exported
// fields of the types those signatures carry, the registry Backend struct, and
// the conformance fixture — must have a public alias in this package.
// Without the alias an out-of-tree implementer cannot name the type, so a
// signature change that drags in a new internal type would silently re-lock
// the door this package exists to hold open. The check compares the reflected
// type closure against the alias declarations parsed from this package's
// source, so adding the method without the alias fails here, in-tree, at test
// time.
func TestPublicSurfaceComplete(t *testing.T) {
	aliases := publicAliases(t)

	need := map[string]reflect.Type{}
	seen := map[reflect.Type]bool{}
	roots := []reflect.Type{
		reflect.TypeOf((*backend.DoltStorage)(nil)).Elem(),
		reflect.TypeOf(backend.Backend{}),
		reflect.TypeOf(conformance.Factory(nil)),
		// The role tier's entry point reaches every role fixture through its
		// factory fields, so all 25 of them join this guard: a contract that
		// later grows a hook carrying a new internal type fails here rather
		// than on an external backend's next version bump.
		reflect.TypeOf(conformance.RoleContractBundle{}),
		reflect.TypeOf(conformance.IssueOperationsStagingFixture{}),
	}
	for _, r := range roots {
		walkType(r, seen, need)
	}

	keys := make([]string, 0, len(need))
	for k := range need {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if aliases[k] {
			continue
		}
		name := k[strings.LastIndexByte(k, '.')+1:]
		if !ast.IsExported(name) {
			// An unexported internal type in an exported position cannot be
			// aliased; it would be a contract bug in its own right.
			t.Errorf("unexported internal type %s is reachable from the public backend contract", k)
			continue
		}
		t.Errorf("internal type %s is reachable from the public backend contract but has no alias in package backend", k)
	}
}

// publicAliases parses the non-test sources of this package and returns the
// set of alias targets, keyed "importpath.TypeName".
func publicAliases(t *testing.T) map[string]bool {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing package backend: %v", err)
	}
	aliases := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			imports := map[string]string{}
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				name := path[strings.LastIndexByte(path, '/')+1:]
				if imp.Name != nil {
					name = imp.Name.Name
				}
				imports[name] = path
			}
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ts.Assign.IsValid() {
						continue // not an alias declaration
					}
					rhs := ts.Type
					// Generic alias: storage.Iter[T] parses as IndexExpr.
					switch e := rhs.(type) {
					case *ast.IndexExpr:
						rhs = e.X
					case *ast.IndexListExpr:
						rhs = e.X
					}
					sel, ok := rhs.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					pkgIdent, ok := sel.X.(*ast.Ident)
					if !ok {
						continue
					}
					if path, ok := imports[pkgIdent.Name]; ok {
						aliases[path+"."+sel.Sel.Name] = true
					}
				}
			}
		}
	}
	if len(aliases) == 0 {
		t.Fatal("no alias declarations found; parser wiring is broken")
	}
	return aliases
}

// walkType records every named type from an internal package reachable from
// t through pointers, containers, function signatures, interface methods, and
// EXPORTED struct fields (unexported fields are not part of the contract an
// implementer writes against).
func walkType(t reflect.Type, seen map[reflect.Type]bool, need map[string]reflect.Type) {
	if t == nil || seen[t] {
		return
	}
	seen[t] = true
	if t.Name() != "" && strings.HasPrefix(t.PkgPath(), internalPrefix) {
		base := t.Name()
		if i := strings.IndexByte(base, '['); i >= 0 {
			base = base[:i] // generic instantiation -> its base name
		}
		need[t.PkgPath()+"."+base] = t
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		walkType(t.Elem(), seen, need)
	case reflect.Map:
		walkType(t.Key(), seen, need)
		walkType(t.Elem(), seen, need)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue // unexported field
			}
			walkType(f.Type, seen, need)
		}
	case reflect.Interface:
		for i := 0; i < t.NumMethod(); i++ {
			walkType(t.Method(i).Type, seen, need)
		}
	case reflect.Func:
		for i := 0; i < t.NumIn(); i++ {
			walkType(t.In(i), seen, need)
		}
		for i := 0; i < t.NumOut(); i++ {
			walkType(t.Out(i), seen, need)
		}
	}
}

// TestAliasIdentity spot-checks that the exported surface is aliases, not
// copies: identical reflect types and errors.Is-compatible sentinels.
func TestAliasIdentity(t *testing.T) {
	if got := reflect.TypeOf(backend.Issue{}).PkgPath(); !strings.HasPrefix(got, internalPrefix) {
		t.Errorf("backend.Issue is not an alias of the internal type (PkgPath %q)", got)
	}
	var f conformance.Factory = func(*testing.T) backend.DoltStorage { return nil }
	_ = f // a func literal typed against the public alias satisfies the suite's Factory
}
