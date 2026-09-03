package storage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRoleFiresHooksKnowsEveryHookFiringRole is the CLASS-level guard on
// RoleFiresHooks, and it exists because the instance-level pins were not enough.
//
// That switch is the only thing standing between a store-derived role and a
// `bd serve` that runs a user's subprocess per landed mutation: httpapi.Listen
// sweeps every configured role through it and refuses the ones it recognizes. A
// role the switch does not know about is ADMITTED — served, silently, firing the
// workspace's scripts for as long as the process is up.
//
// The switch's own comment used to say "every case that exists today is in the
// switch", and it was false four ways at once: the commenter, the ready claimer,
// the batch closer and the batch creator all wrapped a role and fired hooks with
// no case here. Each was found by hand, one at a time, by whoever happened to
// publish an operation over that role — which is a discovery process, not a
// guarantee, and it left the other three standing every time.
//
// So the property is asserted over the CLASS instead: a decorator that carries
// an issueOperationHooks is a decorator that fires them, and every one of them
// must have a case. The scan reads SOURCE rather than reflecting, for the reason
// backend/conformance's role census does: these types are unexported, they are
// never instantiated except through their accessor, and a reflective sweep would
// have to build every decorator to see one.
//
// IT ALSO CATCHES THE EMPTY CASE, which is the other way this guard has failed:
// a Go type switch does not fall through, so `case *hookMetadataCAS:` with
// nothing under it answers FALSE, and a rebase left exactly that for a while
// with the compiler, vet, lint and CI all green. A case counts here only if its
// body returns true.
func TestRoleFiresHooksKnowsEveryHookFiringRole(t *testing.T) {
	fired := hookFiringWrappers(t)
	if len(fired) == 0 {
		t.Fatal("the scan found no hook-firing decorators at all; it has stopped reading what it thinks it reads")
	}
	known := roleFiresHooksCases(t)

	if missing := missingFrom(fired, known); len(missing) > 0 {
		t.Errorf("these decorators fire the workspace's hooks and RoleFiresHooks answers FALSE for them: %v\n"+
			"each one is a role httpapi.Listen would ADMIT and serve, running a user's subprocess per landed mutation.\n"+
			"Add a `case *<type>: return true` to RoleFiresHooks, and pin it from both ends the way the others are.",
			missing)
	}
	if extra := missingFrom(known, fired); len(extra) > 0 {
		t.Errorf("RoleFiresHooks names types that no longer carry an issueOperationHooks: %v\n"+
			"either the decorator stopped firing hooks and the case is dead, or the scan has drifted", extra)
	}
}

// hookFiringWrappers is every decorator type in this package that holds an
// issueOperationHooks — which is what "this value fires the workspace's hooks"
// is spelled as, and the one property all ten of them share.
func hookFiringWrappers(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("hook_*.go")
	if err != nil {
		t.Fatalf("glob the decorator files: %v", err)
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			structType, ok := spec.Type.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}
			for _, field := range structType.Fields.List {
				if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == "issueOperationHooks" {
					out[spec.Name.Name] = true
					return true
				}
			}
			return true
		})
	}
	return out
}

// roleFiresHooksCases is every type RoleFiresHooks answers TRUE for, read off
// the switch itself. A case whose body does not return true does not count: an
// empty one answers false, and that has shipped.
func roleFiresHooksCases(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "hook_decorator.go", nil, 0)
	if err != nil {
		t.Fatalf("parse hook_decorator.go: %v", err)
	}

	out := map[string]bool{}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "RoleFiresHooks" || fn.Recv != nil {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			clause, ok := n.(*ast.CaseClause)
			if !ok || !clauseReturnsTrue(clause) {
				return true
			}
			for _, expr := range clause.List {
				star, ok := expr.(*ast.StarExpr)
				if !ok {
					continue
				}
				if ident, ok := star.X.(*ast.Ident); ok {
					out[ident.Name] = true
				}
			}
			return true
		})
	}
	if !found {
		t.Fatal("RoleFiresHooks is not a function in hook_decorator.go; this scan proves nothing")
	}
	return out
}

// clauseReturnsTrue reports whether a case clause answers true, which is the
// only thing that makes it a guard rather than a comment with a colon on it.
func clauseReturnsTrue(clause *ast.CaseClause) bool {
	for _, stmt := range clause.Body {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		if ident, ok := ret.Results[0].(*ast.Ident); ok && ident.Name == "true" {
			return true
		}
	}
	return false
}

func missingFrom(want, have map[string]bool) []string {
	var out []string
	for name := range want {
		if !have[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
