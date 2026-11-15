package fix

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

const (
	versionedHistoryPkg = "github.com/steveyegge/beads/internal/versionedhistory"
	// package main cannot be imported at all; the guard below is that the
	// activation this package applies lives in a package it CAN import, so the
	// repair handlers apply the identical rule as the command surface rather than
	// a copy of it.
	beadMutatingStoreFile = "store.go"
)

// TestVersionedHistoryDoctorRepairOpenerUsesTheSharedActivation holds the one
// construction site the cmd/bd construction guard cannot see. bd doctor is exempted
// from that guard as a package (its CHECKS are read-only), so the repair opener --
// which opens the store the bead-mutating repairs (stale-closed cleanup, pollution
// cleanup, the fresh-clone imports) write through -- is held to the property here.
//
// A repair that deletes or creates issues is an ordinary mutation for version
// history: an issue_versions trail with a silent gap where `bd doctor --fix` ran is
// the same failure the routed-write bug produced, one command over, and the more
// dangerous for being unattended.
//
// The check is structural for the same reason the cmd/bd guard is: activation
// failing is invisible from inside the process. It requires the activation that
// `activated` applies to come from internal/versionedhistory -- the ONE
// implementation of the rule, which this package can import and package main cannot
// be.
func TestVersionedHistoryDoctorRepairOpenerUsesTheSharedActivation(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, beadMutatingStoreFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", beadMutatingStoreFile, err)
	}

	imports := map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("unquote import %s: %v", spec.Path.Value, err)
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = path
	}

	var activated *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "activated" {
			activated = fn
		}
	}
	if activated == nil {
		t.Fatalf("%s has no `activated` function: the repair opener's activation moved and this guard no longer looks at it", beadMutatingStoreFile)
	}

	called := false
	ast.Inspect(activated, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && imports[ident.Name] == versionedHistoryPkg && sel.Sel.Name == "ActivateStore" {
			called = true
		}
		return true
	})
	if !called {
		t.Errorf("`activated` in %s never calls versionedhistory.ActivateStore: every repair that mutates issues through it "+
			"mints NO issue_versions row while `bd doctor --fix` reports success. Apply the shared activation there; "+
			"it is the same rule as the command surface and package main cannot be imported to reuse it.", beadMutatingStoreFile)
	}
}
