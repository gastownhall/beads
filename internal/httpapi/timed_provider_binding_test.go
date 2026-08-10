package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestEveryTimedProviderAccessorBindsToTheWrapper is the regression pin for the
// hazard every accessor on timedProvider documents and only two of them could
// previously fail on.
//
// THE HAZARD. timedProvider is the one type in this codebase where a
// CONSTRUCTOR beats an accessor: `uow.NewSweeper(p)` binds the role to the
// wrapper, so every unit of work it opens goes through timedProvider.NewUOW and
// lands in the request's uow_ms. `p.inner.Sweeper()` would return a role bound
// to the untimed provider, and the measurement would silently read zero — a
// change the comment on IssueReader records a reviewer proposing "for
// symmetry".
//
// WHY THIS TEST IS STRUCTURAL. The behavioral pins are per route, and only two
// of the roles have one. The rest — including every WRITE role, whose
// transactions are the longest this server runs — have none, so the symmetric
// refactor could be applied to them with the whole package green.
//
// Reading the binding off the AST covers all of them at once. It says nothing
// about what the roles DO, only that each is constructed over the receiver, so
// it is not a replacement for the behavioral pins.
func TestEveryTimedProviderAccessorBindsToTheWrapper(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "claim.go", nil, 0)
	if err != nil {
		t.Fatalf("parse claim.go: %v", err)
	}

	checked := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		ident, ok := fn.Recv.List[0].Type.(*ast.Ident)
		if !ok || ident.Name != "timedProvider" {
			continue
		}
		// NewUOW is the layer itself and Close is lifecycle; neither constructs
		// a role. Everything else on this type is an accessor.
		if fn.Name.Name == "NewUOW" || fn.Name.Name == "Close" {
			continue
		}
		recv := "p"
		if len(fn.Recv.List[0].Names) == 1 {
			recv = fn.Recv.List[0].Names[0].Name
		}

		call := soleReturnedCall(fn)
		if call == nil {
			t.Errorf("timedProvider.%s does not return a single constructor call; if this accessor now "+
				"does something else, say why here — the whole type exists to bind roles to the timing wrapper",
				fn.Name.Name)
			continue
		}
		checked++

		if len(call.Args) != 1 {
			t.Errorf("timedProvider.%s: constructor takes %d arguments, want 1 (the wrapper)", fn.Name.Name, len(call.Args))
			continue
		}
		arg, ok := call.Args[0].(*ast.Ident)
		if !ok || arg.Name != recv {
			t.Errorf("timedProvider.%s constructs its role over %s, not over the receiver %q. "+
				"That binds it to the UNTIMED provider: every unit of work it opens is invisible to "+
				"this request's uow_ms, which logs 0.000 forever with the suite still green. "+
				"Read the hazard note on IssueReader before changing this",
				fn.Name.Name, exprText(call.Args[0]), recv)
		}
	}

	// The count is asserted so that deleting accessors, or renaming the type,
	// cannot make this pass by having nothing to check.
	if checked < 15 {
		t.Errorf("checked %d timedProvider accessors, want at least 15: the roles this surface answers from "+
			"all bind here, so a smaller number means some are no longer being read", checked)
	}
}

// soleReturnedCall returns the call in `return f(x)` when that is the whole
// body, and nil otherwise.
func soleReturnedCall(fn *ast.FuncDecl) *ast.CallExpr {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return nil
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return nil
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return nil
	}
	return call
}

// exprText renders just enough of an expression to name it in a failure.
func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		return exprText(v.Fun) + "(...)"
	default:
		return "an expression that is not the receiver"
	}
}
