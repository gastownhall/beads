package scripts_test

// Policy: no new ungated host `go` or `gofmt` exec under Bazel
// (//scripts:go_sources_test, which declares //:repo_go_srcs and
// //:repo_go_test_srcs, so a non-Go change leaves it cached).
//
// Background (f4-spec.md §9, a release-track planning note). Under `bazel test` the
// PATH is hermetic_bin: plus the strict action env
// (tools/bazel/test_env.sh); the hermetic pinned dolt resolves there, but
// `go` and `gofmt` do not — whether a bare `go` or `gofmt` on that PATH finds
// a host toolchain depends on the worker, so a bare exec of either succeeds
// on some workers and exits 127 on others (gastownhall/beads#7478 fixed one:
// TestRunCompactDoltTargetsOnlyAuthorizedActiveDatabase's fixture builder).
// `dolt` is deliberately NOT in scope here: the hermetic copy comes first on
// that PATH (test_env.sh), so a bare `exec.Command("dolt", ...)` or
// `exec.LookPath("dolt")` is not the worker-dependent failure this policy
// guards against, and the repository has on the order of 80 such call sites
// (internal/storage/dolt, cmd/bd, internal/testutil, ...); allowlisting them
// all here for no safety gain would make this policy file itself the
// un-reviewable thing. Extend bannedHostTools if that changes.
//
// Scope: every *_test.go file (repo-wide) plus the non-test .go files of
// internal/testutil, the one package dedicated to test helpers that other
// packages' tests import. Production code (cmd/bd's non-test files,
// internal/storage/..., internal/doltserver, ...) legitimately execs `dolt`
// (and `go`, for `bd preflight`/`bd doctor`) as part of what bd itself does;
// that is not in scope.
//
// A call site is gated, and so exempt, when:
//   - its tool-name argument is not a string literal (a runfiles-resolved
//     binary: bazeltest.RunfileEnv/Runfile/PrebuiltBD/testGo, or a variable
//     a helper returns only after such a check) — the scan below only looks
//     at literal arguments, so this case never reaches it;
//   - the enclosing function (or func literal) contains an `if` whose
//     condition names bazeltest.IsBazel() or tests os.Getenv("TEST_SRCDIR")
//     against "" (or a trusted prebuilt-lookup result, see below), AND whose
//     body aborts the function (return/continue/break/t.Fatal*/t.Skip*/
//     t.FailNow/panic/os.Exit) exactly when that condition is
//     true-under-Bazel (see condPolarity/blockAborts) — i.e. the enclosing
//     scope provably never reaches the exec while running under Bazel. This
//     abort-style gate must additionally dominate the exec (the `if`'s own
//     end position must be <= the exec's position): in straight-line code,
//     a gate that comes AFTER the exec in source order never runs before
//     the exec does, so it is not actually a gate (gastownhall/beads#7491
//     review, "M21"). Containment-style gating — pos lexically inside a
//     branch that only runs off Bazel — needs no such check: nesting
//     already guarantees the branch's statements run after its own `if`.
//     This is polarity-aware: `if !bazeltest.IsBazel() { t.Skip() }` (which
//     makes the function run ONLY under Bazel) is not a gate, because the
//     abort fires on the opposite condition (gastownhall/beads#7485 review
//     point 6);
//   - its "prebuilt binary path" comes from findPrebuiltBDBinary()/
//     bazeltest.PrebuiltBD(), checked against "" as above, ONLY when that
//     call's error result is bound to a named variable (not discarded via
//     `_`) that is itself checked with an aborting `if err != nil` — an
//     ignored error breaks the "never empty under Bazel" invariant the
//     check otherwise relies on (gastownhall/beads#7491 review, "M19"): see
//     prebuiltResultVars;
//   - the enclosing function is itself named (not a func literal) and is
//     called only from sites that are themselves gated by the rule above —
//     checked by scanning every other in-scope file for bare calls to that
//     name (helperIsGatedByAllCallers). This is what makes a shared builder
//     like goBuildBDCommand (no gate of its own, nine call sites) provably
//     safe without an allowlist entry that only asserts one caller's
//     behavior in prose (gastownhall/beads#7485 review point 4): a tenth,
//     ungated caller added later breaks this check, not just the comment;
//   - it is in hostToolExecAllowlist below, each entry naming the gating
//     mechanism the above cannot see, keyed by file + enclosing function +
//     an exact source-line snippet rather than a line number, so it survives
//     unrelated edits that shift lines (gastownhall/beads#7485 review point 5).
//
// Mutation check: TestHostToolExecPolicyScan's fixtures include a bare
// `exec.Command("go", "version")` with no gate and no allowlist entry, and
// assert the scan reports it; a second fixture pins the inverted-polarity
// case (`if !bazeltest.IsBazel() { t.Skip() }`) as still ungated; a third
// pins that a helper with one ungated caller among several is reported; a
// fourth pins that an exec textually before its own abort-style gate is
// reported (review M21); a fifth pins that a discarded PrebuiltBD()/
// findPrebuiltBDBinary() error defeats the "never empty under Bazel" check
// (review M19).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// bannedHostTools are the literal exec argument values this policy flags
// when ungated and unallowlisted. See the package comment above for why
// "dolt" is not here.
var bannedHostTools = map[string]bool{"go": true, "gofmt": true}

// hostToolExecHit is one call this scan found, gated or not.
type hostToolExecHit struct {
	line     int
	tool     string
	call     string // "exec.Command", "exec.CommandContext", "exec.LookPath"
	gated    bool
	funcName string // enclosing top-level func's name, or "" (func literal/none)
	snippet  string // trimmed source text of the hit's line, for allowlist matching
}

// funcScopes bundles the per-file lookups findHostToolExecCalls and
// findCallSitesOf share: the smallest function/func-literal body enclosing a
// position, whether that scope is gated, and the top-level function name (if
// any) owning a body.
type funcScopes struct {
	enclosing func(pos token.Pos) ast.Node
	funcName  func(scope ast.Node) string
	gated     func(scope ast.Node, pos token.Pos) bool
}

// newFuncScopes builds the body/gating lookups for file.
func newFuncScopes(file *ast.File) *funcScopes {
	var bodies []*ast.BlockStmt
	owner := map[*ast.BlockStmt]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil {
				bodies = append(bodies, fn.Body)
				owner[fn.Body] = fn.Name.Name
			}
		case *ast.FuncLit:
			bodies = append(bodies, fn.Body)
		}
		return true
	})

	enclosing := func(pos token.Pos) ast.Node {
		var best *ast.BlockStmt
		for _, b := range bodies {
			if b.Pos() <= pos && pos <= b.End() {
				if best == nil || (b.End()-b.Pos()) < (best.End()-best.Pos()) {
					best = b
				}
			}
		}
		if best != nil {
			return best
		}
		return file
	}

	funcName := func(scope ast.Node) string {
		if b, ok := scope.(*ast.BlockStmt); ok {
			return owner[b]
		}
		return ""
	}

	gated := func(scope ast.Node, pos token.Pos) bool {
		prebuiltVars := prebuiltResultVars(scope)
		found := false
		ast.Inspect(scope, func(n ast.Node) bool {
			// Do not descend into a nested func literal: its gate (or lack
			// of one) belongs to that closure's own scope, not to pos's
			// enclosing scope. Without this, a gate inside a sibling
			// t.Run(...) closure (or any other func literal) would be
			// treated as covering an exec outside it entirely
			// (gastownhall/beads#7485 review, "sibling closures").
			if _, ok := n.(*ast.FuncLit); ok && n != scope {
				return false
			}
			ifStmt, ok := n.(*ast.IfStmt)
			if !ok {
				return true
			}
			bazelWhenTrue, matched := condPolarity(ifStmt.Cond, prebuiltVars)
			if !matched {
				return true
			}
			// Abort style: the if's body aborts (never falls through to the
			// rest of the enclosing scope) exactly when ifStmt.Cond is true.
			// If Cond is true precisely when running under Bazel, the rest
			// of the scope -- wherever pos sits in it, PROVIDED pos is not
			// itself inside this same Bazel-true branch -- is reachable
			// only off Bazel: a real gate. The !within(ifStmt.Body, pos)
			// guard makes that provision exact: `if bazeltest.IsBazel() {
			// exec.Command("go", ...); return }` has bazelWhenTrue=true
			// and an aborting body, but the exec sits INSIDE that same
			// true-under-Bazel branch, before its own return -- it runs
			// under Bazel every time, so this must not set found
			// (gastownhall/beads#7485 review, "M17"). If Cond is true
			// precisely when NOT under Bazel (the inverted case), the
			// rest of the scope is reachable ONLY under Bazel -- the
			// opposite of a gate -- so this must not set found either.
			//
			// ifStmt.End() <= pos requires the gate to dominate the exec:
			// the if must finish (textually) before pos starts. Straight-
			// line code executes in source order, so an exec statement
			// that runs BEFORE this if is not protected by it, no matter
			// how the if's own body aborts (gastownhall/beads#7491 review,
			// "M21" -- an exec placed before its gate is not actually
			// gated). This is deliberately still a lexical-order check, not
			// full control-flow analysis: a gate that textually precedes
			// pos anywhere in scope (including across unrelated sibling
			// statements, or inside a loop that runs before pos) still
			// counts, matching the "mechanism exists somewhere earlier in
			// this scope" looseness the rest of this policy uses.
			if bazelWhenTrue && blockAborts(ifStmt.Body) && !within(ifStmt.Body, pos) && ifStmt.End() <= pos {
				found = true
			}
			// Containment style: pos sits inside a branch that only runs
			// off Bazel -- the if's Body when Cond is true off Bazel, or
			// its Else when Cond is true under Bazel -- regardless of
			// whether that branch aborts (e.g. `if !bazeltest.IsBazel() {
			// ...exec... }` with no return at all, a common real pattern:
			// cmd/bd/test_repo_beads_guard_test.go's GOCACHE/GOMODCACHE
			// probes). An inverted containment (pos inside the branch that
			// runs UNDER Bazel) must not set found -- that is review point
			// 6's bug.
			if !bazelWhenTrue && within(ifStmt.Body, pos) {
				found = true
			}
			// ifStmt.Else is either a *ast.BlockStmt (a plain `else { ... }`)
			// or another *ast.IfStmt (an `else if` link in a chain); either
			// way its whole span -- including a further-nested else-if's own
			// body -- is reached only when ifStmt.Cond is false, so a single
			// Pos/End containment check on the Else node covers the chain.
			if ifStmt.Else != nil && bazelWhenTrue && within(ifStmt.Else, pos) {
				found = true
			}
			return true
		})
		return found
	}

	return &funcScopes{enclosing: enclosing, funcName: funcName, gated: gated}
}

// condPolarity reports, for a condition expr, whether expr is true exactly
// when running under Bazel (bazelWhenTrue), and whether expr is a condition
// this policy recognizes at all (matched). prebuiltVars names local variables
// already known (via prebuiltResultVars) to hold a findPrebuiltBDBinary()/
// bazeltest.PrebuiltBD() result: under Bazel that call either errors (an
// abort in its own right, wherever the caller checks it) or returns a
// non-empty path (bazeltest.PrebuiltBD, internal/testutil/bazeltest/
// bazeltest.go), so it is never "" under Bazel. Recognized forms:
//   - bazeltest.IsBazel() (or any x.IsBazel()): true under Bazel.
//   - !<recognized> / (<recognized>): negation/parens flip/pass through.
//   - os.Getenv("TEST_SRCDIR") != "" : true under Bazel; == "" : true off Bazel.
//   - prebuiltVar != "" : true under Bazel; == "" : true off Bazel.
//   - a && / || of two recognized sub-conditions that agree on polarity (a
//     mixed-polarity compound isn't a single-condition gate, so it is left
//     unmatched rather than guessed at).
func condPolarity(cond ast.Expr, prebuiltVars map[string]bool) (bazelWhenTrue bool, matched bool) {
	switch e := cond.(type) {
	case *ast.ParenExpr:
		return condPolarity(e.X, prebuiltVars)
	case *ast.UnaryExpr:
		if e.Op == token.NOT {
			pol, ok := condPolarity(e.X, prebuiltVars)
			if ok {
				return !pol, true
			}
		}
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "IsBazel" {
			return true, true
		}
	case *ast.BinaryExpr:
		nonEmptyUnderBazel := isTestSrcdirGetenv(e.X) || isPrebuiltVarIdent(e.X, prebuiltVars) ||
			isTestSrcdirGetenv(e.Y) || isPrebuiltVarIdent(e.Y, prebuiltVars)
		if nonEmptyUnderBazel && (isEmptyStringLit(e.X) || isEmptyStringLit(e.Y)) {
			switch e.Op {
			case token.NEQ:
				return true, true
			case token.EQL:
				return false, true
			}
		}
		if e.Op == token.LAND || e.Op == token.LOR {
			lp, lok := condPolarity(e.X, prebuiltVars)
			rp, rok := condPolarity(e.Y, prebuiltVars)
			// A mixed compound -- one side recognized, the other not -- is
			// left unmatched rather than guessed at, for EITHER operator
			// (review: "mixed compounds with one unrecognized side are
			// taken as a gate", against this function's own doc comment).
			// An earlier revision returned the recognized side's polarity
			// in that case, which is unsound in both directions:
			//   - `IsBazel() && testing.Short()` (lok=true, rok=false)
			//     taken as bazelWhenTrue=true lets `if IsBazel() &&
			//     testing.Short() { t.Skip() }` count as containment-style
			//     gated even though, without -short, the condition is false
			//     under Bazel and the exec still runs;
			//   - `os.Getenv("TEST_SRCDIR") == "" || os.Getenv(
			//     "FORCE_HOST_GO") != ""` (lok=true, rok=false, since the
			//     FORCE_HOST_GO side names no recognized Bazel signal) taken
			//     as bazelWhenTrue=false/matched=true lets abort-style
			//     wrongly fire when FORCE_HOST_GO is unset off-Bazel.
			// The sound rule is operator-dependent (containment needs
			// &&-with-unknown to be unmatched; abort needs ||-with-unknown
			// to be unmatched), so requiring BOTH sides recognized, for
			// both operators, is what the doc comment already promised.
			if lok && rok && lp == rp {
				return lp, true
			}
		}
	}
	return false, false
}

// isPrebuiltVarIdent reports whether e is a bare identifier naming a variable
// prebuiltResultVars recognized as a findPrebuiltBDBinary()/
// bazeltest.PrebuiltBD() result.
func isPrebuiltVarIdent(e ast.Expr, prebuiltVars map[string]bool) bool {
	id, ok := e.(*ast.Ident)
	return ok && prebuiltVars[id.Name]
}

// prebuiltResultVars scans scope for assignments whose right-hand side is a
// call to findPrebuiltBDBinary() or (any x.)PrebuiltBD(), returning the set
// of local names bound to the call's first result -- the "prebuilt binary
// path, or \"\"" value -- for assignments where the call's error result is
// actually checked. A caller that checks `name != ""` and aborts when true
// is gated: see condPolarity.
//
// The "never empty under Bazel" invariant documented on condPolarity only
// holds when err == nil: both findPrebuiltBDBinary and bazeltest.PrebuiltBD
// can return ("", non-nil) under Bazel too (an unset/misconfigured runfile
// env var resolves through RunfileEnv, which errors rather than silently
// returning ""). A caller that discards the error -- `bd, _ :=
// findPrebuiltBDBinary()` -- loses that invariant: a real lookup failure
// under Bazel now also lands in the "bd == \"\"" branch, which a caller
// relying on this scan would otherwise treat as "off Bazel only" and fall
// through to an ungated host `go build`. Such a discarded-error var must
// NOT be trusted here, so its "bd == \"\""/"bd != \"\"" checks fall through
// to condPolarity's unmatched case and the exec they guard is reported
// ungated (gastownhall/beads#7491 review, "M19"). A var is only trusted when
// the error is bound to a named identifier AND that identifier is checked
// (`if err != nil { <aborts> }`) somewhere in the same scope -- matching the
// fix PR #7491 applied to cmd/bd/scripttest_test.go by hand.
func prebuiltResultVars(scope ast.Node) map[string]bool {
	vars := map[string]bool{}
	ast.Inspect(scope, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 || len(assign.Lhs) != 2 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		isPrebuiltCall := false
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			isPrebuiltCall = fn.Name == "findPrebuiltBDBinary"
		case *ast.SelectorExpr:
			isPrebuiltCall = fn.Sel.Name == "PrebuiltBD"
		}
		if !isPrebuiltCall {
			return true
		}
		id, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || id.Name == "_" {
			return true
		}
		errID, ok := assign.Lhs[1].(*ast.Ident)
		if !ok || errID.Name == "_" || errID.Name == "" {
			// Error discarded (or not a plain identifier): the "never
			// empty under Bazel" invariant cannot be trusted. See the
			// func comment ("M19").
			return true
		}
		if errorCheckedAndAborts(scope, errID.Name) {
			vars[id.Name] = true
		}
		return true
	})
	return vars
}

// errorCheckedAndAborts reports whether scope contains an `if errName !=
// nil { <aborts> }` (in either operand order), where the if's body aborts
// per blockAborts. It does not descend into a nested func literal, matching
// newFuncScopes' gated closure's own scoping rule: an error check belongs to
// the scope it is lexically in, not to an unrelated sibling closure.
func errorCheckedAndAborts(scope ast.Node, errName string) bool {
	found := false
	ast.Inspect(scope, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok && n != scope {
			return false
		}
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		if isErrNotNilCheck(ifStmt.Cond, errName) && blockAborts(ifStmt.Body) {
			found = true
		}
		return true
	})
	return found
}

// isErrNotNilCheck reports whether cond is `errName != nil` or `nil !=
// errName`.
func isErrNotNilCheck(cond ast.Expr, errName string) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	isErrIdent := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == errName
	}
	isNilIdent := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && id.Name == "nil"
	}
	return (isErrIdent(bin.X) && isNilIdent(bin.Y)) || (isErrIdent(bin.Y) && isNilIdent(bin.X))
}

func isTestSrcdirGetenv(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Getenv" {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(lit.Value)
	return err == nil && v == "TEST_SRCDIR"
}

// within reports whether pos lies within node's source range.
func within(node ast.Node, pos token.Pos) bool {
	return node.Pos() <= pos && pos <= node.End()
}

func isEmptyStringLit(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(lit.Value)
	return err == nil && v == ""
}

// blockAborts reports whether body unconditionally stops control flow from
// reaching whatever follows it: a return/break/continue, or a call that
// itself never returns to the caller in a way that matters here
// (t.Fatal*/t.Skip*/t.FailNow, panic, os.Exit). It inspects the whole body,
// not just its top-level statements, so `if x { t.Skip() }` inside a nested
// block still counts -- matching the same permissive, non-control-flow-exact
// spirit the rest of this policy uses (see the package comment).
func blockAborts(body *ast.BlockStmt) bool {
	aborts := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.ReturnStmt:
			aborts = true
		case *ast.BranchStmt:
			if s.Tok == token.BREAK || s.Tok == token.CONTINUE {
				aborts = true
			}
		case *ast.CallExpr:
			switch fn := s.Fun.(type) {
			case *ast.Ident:
				if fn.Name == "panic" {
					aborts = true
				}
			case *ast.SelectorExpr:
				switch fn.Sel.Name {
				case "Skip", "SkipNow", "Skipf", "Fatal", "Fatalf", "FailNow":
					aborts = true
				case "Exit":
					if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Name == "os" {
						aborts = true
					}
				}
			}
		}
		return true
	})
	return aborts
}

func literalArg(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// findHostToolExecCalls scans a parsed file for exec.Command / CommandContext
// / LookPath calls whose tool-name argument is a string literal naming a
// bannedHostTools entry, and reports, for each, whether its enclosing
// function (or func literal) is gated (see the package comment), its
// enclosing top-level function name, and its line's trimmed source text.
func findHostToolExecCalls(fset *token.FileSet, file *ast.File, lines []string) []hostToolExecHit {
	scopes := newFuncScopes(file)

	var hits []hostToolExecHit
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "exec" {
			return true
		}
		argIdx := -1
		switch sel.Sel.Name {
		case "Command", "LookPath":
			argIdx = 0
		case "CommandContext":
			argIdx = 1
		default:
			return true
		}
		if len(call.Args) <= argIdx {
			return true
		}
		tool, ok := literalArg(call.Args[argIdx])
		if !ok || !bannedHostTools[tool] {
			return true
		}
		line := fset.Position(call.Pos()).Line
		scope := scopes.enclosing(call.Pos())
		hits = append(hits, hostToolExecHit{
			line:     line,
			tool:     tool,
			call:     "exec." + sel.Sel.Name,
			gated:    scopes.gated(scope, call.Pos()),
			funcName: scopes.funcName(scope),
			snippet:  snippetAt(lines, line),
		})
		return true
	})
	return hits
}

// findCallSitesOf scans file for bare calls `name(...)` (an *ast.Ident, not a
// selector -- how a same-package helper like goBuildBDCommand is called) and
// reports whether each call site's enclosing function/func-literal is gated.
func findCallSitesOf(file *ast.File, name string) []bool {
	scopes := newFuncScopes(file)
	var gatedPerSite []bool
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != name {
			return true
		}
		gatedPerSite = append(gatedPerSite, scopes.gated(scopes.enclosing(call.Pos()), call.Pos()))
		return true
	})
	return gatedPerSite
}

// helperIsGatedByAllCallers reports whether every bare call to funcName,
// across every file in files, is itself gated -- and how many call sites it
// found. A helper with zero call sites is not considered gated this way (it
// is either dead code or the scan is being asked about the wrong name); the
// caller should fall back to requiring a direct gate or allowlist entry.
func helperIsGatedByAllCallers(files map[string]*ast.File, funcName string) (gated bool, callSites int) {
	if funcName == "" {
		return false, 0
	}
	for _, f := range files {
		for _, siteGated := range findCallSitesOf(f, funcName) {
			callSites++
			if !siteGated {
				return false, callSites
			}
		}
	}
	return callSites > 0, callSites
}

func snippetAt(lines []string, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

// hostToolAllowlistEntry is one justified exception: the enclosing function
// name (empty for a call at package scope or inside a func literal this
// policy cannot name) and the exact trimmed source line, rather than a line
// number, so an entry survives the file shifting lines around it
// (gastownhall/beads#7485 review point 5).
type hostToolAllowlistEntry struct {
	fn      string
	snippet string
	why     string
}

// hostToolExecAllowlist is file (repo-relative, slash-separated) -> the
// entries justifying an ungated literal go/gofmt exec there. Every entry
// here is a helper reached only when bazeltest.PrebuiltBD()/
// findPrebuiltBDBinary() (which itself calls bazeltest.IsBazel() under the
// hood) already reported no prebuilt bd binary -- under Bazel that call
// never returns "", so the exec below it is unreachable there, just not
// through a mention this scan's textual check can find in the same
// function. goBuildBDCommand itself (nine call sites, cmd/bd/*_test.go) is
// deliberately NOT here: it has no gate of its own, so it is instead
// verified by helperIsGatedByAllCallers, which checks every call site
// directly rather than trusting a comment about them (review point 4).
// Note: internal/storage/uow/doltserver_provider_test.go and
// cmd/bd/migrate_dolt_mode_frontdoor_integration_test.go used to have entries
// here for their `go build` fallbacks; both are now recognized directly
// (prebuiltResultVars/condPolarity: each checks its own
// findPrebuiltBDBinary()/bazeltest.PrebuiltBD() result `!= ""` and returns),
// so they need no allowlist entry.
var hostToolExecAllowlist = map[string][]hostToolAllowlistEntry{
	"internal/storage/embeddeddolt/concurrency_test.go": {
		{
			fn:      "TestConcurrencyMultiProcess",
			snippet: `build := exec.CommandContext(ctx, "go", "test",`,
			why:     "in the else branch of `if testBin := os.Getenv(\"BEADS_TEST_EMBEDDED_TEST_BINARY\"); testBin != \"\"`; unset only off Bazel",
		},
	},
	"tests/regression/regression_test.go": {
		{
			fn:      "buildCandidate",
			snippet: `cmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", outPath, "./cmd/bd")`,
			why:     "buildCandidate's only caller gates it on bazeltest.PrebuiltBD() returning \"\"",
		},
	},
}

// hostToolExecInScope reports whether rel (repo-relative, slash-separated)
// is in this policy's scope: see the package comment.
func hostToolExecInScope(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	return strings.HasPrefix(rel, "internal/testutil/") && strings.HasSuffix(rel, ".go")
}

func TestHostToolExecPolicyScan(t *testing.T) {
	parse := func(t *testing.T, src string) (*token.FileSet, *ast.File, []string) {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "fixture.go", src, 0)
		if err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		return fset, f, strings.Split(src, "\n")
	}

	t.Run("ungated literal go exec is reported, unqated", func(t *testing.T) {
		fset, f, lines := parse(t, `package p

import "os/exec"

func build() {
	exec.Command("go", "version")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit (mutation check)", hits)
		}
	})

	t.Run("CommandContext and LookPath also match", func(t *testing.T) {
		fset, f, lines := parse(t, `package p

import (
	"context"
	"os/exec"
)

func a(ctx context.Context) {
	exec.CommandContext(ctx, "go", "build")
}

func b() {
	exec.LookPath("gofmt")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 2 || hits[0].gated || hits[1].gated {
			t.Fatalf("hits = %+v, want two ungated hits", hits)
		}
	})

	t.Run("dolt is out of scope", func(t *testing.T) {
		fset, f, lines := parse(t, `package p

import "os/exec"

func d() {
	exec.Command("dolt", "version")
}
`)
		if hits := findHostToolExecCalls(fset, f, lines); len(hits) != 0 {
			t.Fatalf("hits = %+v, want none (dolt is not a bannedHostTools entry)", hits)
		}
	})

	t.Run("a variable tool name is not flagged", func(t *testing.T) {
		fset, f, lines := parse(t, `package p

import "os/exec"

func build(goBin string) {
	exec.Command(goBin, "build")
}
`)
		if hits := findHostToolExecCalls(fset, f, lines); len(hits) != 0 {
			t.Fatalf("hits = %+v, want none (not a literal)", hits)
		}
	})

	t.Run("same-function IsBazel gate, textually before the exec", func(t *testing.T) {
		fset, f, lines := parse(t, `package p

import "os/exec"

func build() {
	if bazeltest.IsBazel() {
		return
	}
	exec.Command("go", "build")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || !hits[0].gated {
			t.Fatalf("hits = %+v, want one gated hit", hits)
		}
	})

	t.Run("same-function IsBazel gate, textually after the exec, does not count (mutation check, review M21)", func(t *testing.T) {
		// The exec runs, in source order, before the gate's own abort ever
		// has a chance to fire -- the gate does not dominate the exec, so
		// it must not be treated as safe (gastownhall/beads#7491 review,
		// "M21"). An earlier revision was deliberately position-agnostic
		// here ("does the function contain this mechanism", not
		// control-flow aware about lexical order); that was the bug.
		fset, f, lines := parse(t, `package p

import "os/exec"

func build() {
	exec.Command("go", "build")
	if bazeltest.IsBazel() {
		panic("unreachable")
	}
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit: the gate comes after the exec, "+
				"so it does not dominate it", hits)
		}
	})

	t.Run("exec inside the Bazel-true branch, before its own abort, is not gated (mutation check, review M17)", func(t *testing.T) {
		// `if bazeltest.IsBazel() { exec.Command("go", ...); return }` runs
		// the exec every time Bazel is true -- the opposite of safe. The old
		// abort-style check only looked at whether ifStmt.Body aborts
		// anywhere in it, not at whether pos (the exec) sits inside that
		// same true-under-Bazel body ahead of the abort.
		fset, f, lines := parse(t, `package p

import "os/exec"

func build() {
	if bazeltest.IsBazel() {
		exec.Command("go", "build")
		return
	}
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit: the exec runs every time Bazel is true, "+
				"before the branch's own return", hits)
		}
	})

	t.Run("a gate inside a sibling closure does not cover an exec outside it (mutation check, review sibling-closures)", func(t *testing.T) {
		// The gate lives inside a t.Run func literal; the exec is a sibling
		// statement in the outer test function, not reached by descending
		// into that closure.
		//
		// Named siblingClosureFixture, not TestSiblingClosure: see the
		// "inverted IsBazel skip" fixture below for why a column-0
		// "func TestXxx(t *testing.T)" inside this raw string would trip
		// tools/bazel/equivalence.py's line-based scan.
		fset, f, lines := parse(t, `package p

import "os/exec"

func siblingClosureFixture(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		if bazeltest.IsBazel() {
			return
		}
	})
	exec.Command("go", "build")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit: the gate is inside a sibling closure, "+
				"not the outer function's own scope", hits)
		}
	})

	t.Run("TEST_SRCDIR gate", func(t *testing.T) {
		fset, f, lines := parse(t, `package p

import (
	"os"
	"os/exec"
)

func build() {
	if os.Getenv("TEST_SRCDIR") != "" {
		return
	}
	exec.LookPath("go")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || !hits[0].gated {
			t.Fatalf("hits = %+v, want one gated hit", hits)
		}
	})

	t.Run("inverted TEST_SRCDIR check is not a gate", func(t *testing.T) {
		fset, f, lines := parse(t, `package p

import (
	"os"
	"os/exec"
)

func build() {
	if os.Getenv("TEST_SRCDIR") == "" {
		return
	}
	exec.LookPath("go")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit: this function returns OFF Bazel and only "+
				"reaches the exec UNDER Bazel, which is backwards", hits)
		}
	})

	t.Run("inverted IsBazel skip is not a gate (mutation check, review point 6)", func(t *testing.T) {
		// `if !bazeltest.IsBazel() { t.Skip() }` makes the enclosing test run
		// ONLY under Bazel: the exact opposite of a gate. The old scan saw
		// "IsBazel" mentioned anywhere in the function and called that
		// gated; this fixture pins that it no longer does.
		// Named to avoid matching tools/bazel/equivalence.py's
		// `^func\s+(Test\w*)\s*\(\s*\w+\s+\*testing\.T\s*\)` line scan: that
		// regex reads this file's raw text (it doesn't compile it), so a
		// column-0 "func TestXxx(t *testing.T)" inside this fixture string
		// would be counted as a real top-level test the equivalence check
		// then expects Bazel to have run.
		fset, f, lines := parse(t, `package p

import "os/exec"

func bazelOnlyFixture(t *testing.T) {
	if !bazeltest.IsBazel() {
		t.Skip("bazel only")
	}
	exec.Command("go", "build")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit: this test runs ONLY under Bazel, "+
				"so the exec is never off the hermetic PATH", hits)
		}
	})

	t.Run("a mixed && compound with one unrecognized side is not a gate (mutation check, review item C)", func(t *testing.T) {
		// `IsBazel() && testing.Short()` is true under Bazel only when
		// -short was also passed: an earlier revision of condPolarity
		// treated the unrecognized `testing.Short()` side as absent and
		// returned IsBazel()'s own polarity (bazelWhenTrue=true), which
		// wrongly makes this containment-style-gate the exec below for
		// every run, including a non-short run under Bazel where the
		// condition is false and the exec is NOT skipped.
		fset, f, lines := parse(t, `package p

import "os/exec"

func build(t *testing.T) {
	if bazeltest.IsBazel() && extraCondition() {
		t.Skip()
	}
	exec.Command("go", "build")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit: the compound is mixed-polarity "+
				"(one side unrecognized), so it must not be treated as a gate", hits)
		}
	})

	t.Run("a mixed || compound with one unrecognized side is not a gate (mutation check, review item C)", func(t *testing.T) {
		// `os.Getenv("TEST_SRCDIR") == "" || os.Getenv("FORCE_HOST_GO") !=
		// ""` has a recognized left side (true off-Bazel) and an
		// unrecognized right side. An earlier revision returned the
		// recognized side's polarity (bazelWhenTrue=false, matched=true),
		// which wrongly makes this abort-style-gate the exec: with
		// FORCE_HOST_GO unset, off-Bazel, the condition is already true and
		// aborts, so the abort-style rule would (wrongly) call the exec
		// below safe under Bazel too, when in fact FORCE_HOST_GO could be
		// set under Bazel and reach it there.
		fset, f, lines := parse(t, `package p

import (
	"os"
	"os/exec"
)

func build() {
	if os.Getenv("TEST_SRCDIR") == "" || os.Getenv("FORCE_HOST_GO") != "" {
		return
	}
	exec.Command("go", "build")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit: the compound is mixed-polarity "+
				"(one side unrecognized), so it must not be treated as a gate", hits)
		}
	})

	t.Run("exec placed before its gate, in sequence (not nested), is reported ungated (mutation check, review M21)", func(t *testing.T) {
		// This is NOT the M17 false-safe (an exec inside the SAME
		// true-under-Bazel branch, ahead of that branch's own abort): here
		// the exec is a sibling statement, textually before an unrelated
		// `if` later in the function that aborts under Bazel. A prior
		// revision treated this as gated -- deliberately position-agnostic,
		// "it does not check that the `if` lexically precedes the call" --
		// but that is unsound: in straight-line code the exec runs, every
		// time, before the later `if` gets a chance to abort anything
		// (gastownhall/beads#7491 review, "M21" -- "an exec placed before
		// its gate" is not actually gated). The gate must dominate the
		// exec: ifStmt.End() <= pos, enforced in the abort-style check.
		fset, f, lines := parse(t, `package p

import "os/exec"

func build() {
	exec.Command("go", "build")
	if bazeltest.IsBazel() {
		return
	}
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit: the gate comes after the exec, "+
				"so it does not dominate it", hits)
		}
	})

	t.Run("an exec reached only through a function-value helper is not recognized -- documented limitation, not a bug (review item C)", func(t *testing.T) {
		// helperIsGatedByAllCallers (via findCallSitesOf) only recognizes a
		// bare `name(...)` call (an *ast.Ident). A call through a function
		// VALUE -- a variable or struct field holding the helper, or the
		// helper passed to another function and invoked indirectly -- is
		// not a call site findCallSitesOf's walk can name, so
		// helperIsGatedByAllCallers reports zero call sites for it (not
		// gated), even when every real invocation happens to be gated.
		// This under-approximates safety (it would require an allowlist
		// entry it does not actually need), not over-approximates it, so
		// it is a usability gap, not a soundness hole IN ISOLATION -- but
		// see the next fixture (review M22): mixed with even one call site
		// findCallSitesOf DOES see and that one happens to be gated, the
		// invisible function-value call site's ungated-ness is silently
		// lost, and the combined result flips to "gated". That combined
		// case is a soundness hole, not merely a usability gap.
		_, helperFile, _ := parse(t, `package p

import "os/exec"

func helper() {
	exec.Command("go", "build")
}
`)
		_, callerFile, _ := parse(t, `package p

func caller() {
	if bazeltest.IsBazel() {
		return
	}
	fn := helper
	fn()
}
`)
		files := map[string]*ast.File{"helper.go": helperFile, "caller.go": callerFile}
		gated, n := helperIsGatedByAllCallers(files, "helper")
		if gated || n != 0 {
			t.Fatalf("helperIsGatedByAllCallers = (%v, %d), want (false, 0): pinning that a "+
				"function-value call site is invisible to this scan (documented limitation)", gated, n)
		}
	})

	t.Run("a helper with one gated direct call and one ungated function-value call is wrongly reported gated (review M22, pinned false-safe)", func(t *testing.T) {
		// This is the soundness hole the previous fixture's doc comment
		// understated as "not a soundness hole": findCallSitesOf only ever
		// sees callerDirect's bare `helper()` call (gated=true). It cannot
		// see callerIndirect's `fn := helper; fn()` call at all, so that
		// call site -- reached with NO gate -- never lowers the combined
		// verdict. helperIsGatedByAllCallers returns gated=true from a
		// single visible, gated call site, even though helper's exec also
		// runs, unconditionally, through callerIndirect. A caller of this
		// helper that relies on helperIsGatedByAllCallers("helper") to
		// decide the exec needs no allowlist entry is told it is safe when
		// it is not. Pinned as a known, documented limitation (fixing it
		// for real needs points-to analysis for a same-package helper
		// value), not asserted to be correct.
		_, helperFile, _ := parse(t, `package p

import "os/exec"

func helper() {
	exec.Command("go", "build")
}
`)
		_, callerFile, _ := parse(t, `package p

func callerDirect() {
	if bazeltest.IsBazel() {
		return
	}
	helper()
}

func callerIndirect() {
	fn := helper
	fn()
}
`)
		files := map[string]*ast.File{"helper.go": helperFile, "caller.go": callerFile}
		gated, n := helperIsGatedByAllCallers(files, "helper")
		if !gated || n != 1 {
			t.Fatalf("helperIsGatedByAllCallers = (%v, %d), want (true, 1): pinning the false-safe "+
				"(review M22) -- callerIndirect's ungated function-value call site is invisible, "+
				"so the one visible, gated call site wins even though the exec is not always gated", gated, n)
		}
	})

	t.Run("TEST_SRCDIR gate", func(t *testing.T) {
		fset, f, lines := parse(t, `package p

import (
	"os"
	"os/exec"
)

func build() {
	if os.Getenv("TEST_SRCDIR") != "" {
		return
	}
	exec.LookPath("go")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || !hits[0].gated {
			t.Fatalf("hits = %+v, want one gated hit", hits)
		}
	})

	t.Run("a sibling function's gate does not cover this one", func(t *testing.T) {
		fset, f, lines := parse(t, `package p

import "os/exec"

func caller() {
	if bazeltest.IsBazel() {
		return
	}
	helper()
}

func helper() {
	exec.Command("go", "build")
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit (the gate is in a different function)", hits)
		}
	})

	t.Run("helper gated by call sites when every caller gates it", func(t *testing.T) {
		_, helperFile, _ := parse(t, `package p

import "os/exec"

func helper() {
	exec.Command("go", "build")
}
`)
		_, callerFile, _ := parse(t, `package p

func caller() {
	if bazeltest.IsBazel() {
		return
	}
	helper()
}

func other() {
	if bazeltest.IsBazel() {
		return
	}
	helper()
}
`)
		files := map[string]*ast.File{"helper.go": helperFile, "caller.go": callerFile}
		gated, n := helperIsGatedByAllCallers(files, "helper")
		if !gated || n != 2 {
			t.Fatalf("helperIsGatedByAllCallers = (%v, %d), want (true, 2)", gated, n)
		}
	})

	t.Run("helper NOT gated when any one caller is ungated (mutation check, review point 4)", func(t *testing.T) {
		_, helperFile, _ := parse(t, `package p

import "os/exec"

func helper() {
	exec.Command("go", "build")
}
`)
		_, callerFile, _ := parse(t, `package p

func caller() {
	if bazeltest.IsBazel() {
		return
	}
	helper()
}

func newUngatedCaller() {
	helper()
}
`)
		files := map[string]*ast.File{"helper.go": helperFile, "caller.go": callerFile}
		gated, n := helperIsGatedByAllCallers(files, "helper")
		if gated || n != 2 {
			t.Fatalf("helperIsGatedByAllCallers = (%v, %d), want (false, 2): a new ungated caller "+
				"must make the helper unsafe again", gated, n)
		}
	})

	t.Run("a helper with no call sites found is not considered gated", func(t *testing.T) {
		files := map[string]*ast.File{}
		gated, n := helperIsGatedByAllCallers(files, "helper")
		if gated || n != 0 {
			t.Fatalf("helperIsGatedByAllCallers = (%v, %d), want (false, 0)", gated, n)
		}
	})

	t.Run("a discarded PrebuiltBD error defeats the gate (mutation check, review M19)", func(t *testing.T) {
		// `bd, _ := bazeltest.PrebuiltBD()` throws away the error. Under
		// Bazel, PrebuiltBD can return ("", non-nil) -- not just
		// (non-empty, nil) -- when the runfile env var is unset or
		// unusable, so "bd == \"\"" is no longer exclusively an off-Bazel
		// signal: a real lookup failure under Bazel reaches this branch
		// too, and the exec below it would then run under Bazel.
		fset, f, lines := parse(t, `package p

import "os/exec"

func build() {
	bd, _ := bazeltest.PrebuiltBD()
	if bd == "" {
		exec.Command("go", "build")
	}
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit: the discarded error "+
				"defeats the \"never empty under Bazel\" invariant", hits)
		}
	})

	t.Run("a PrebuiltBD error bound but never checked also defeats the gate (mutation check, review M19)", func(t *testing.T) {
		// Naming the error is not enough on its own -- it must actually be
		// checked (an aborting `if err != nil`) for the "never empty under
		// Bazel" invariant to hold.
		fset, f, lines := parse(t, `package p

import "os/exec"

func build() {
	bd, err := bazeltest.PrebuiltBD()
	_ = err
	if bd == "" {
		exec.Command("go", "build")
	}
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || hits[0].gated {
			t.Fatalf("hits = %+v, want one ungated hit: the error is bound but never "+
				"checked, so the invariant still does not hold", hits)
		}
	})

	t.Run("a checked PrebuiltBD error keeps the gate working (regression check, review M19)", func(t *testing.T) {
		// The fixed shape: the error is bound to a named variable and
		// checked with an aborting `if err != nil`, matching the real fix
		// applied to cmd/bd/scripttest_test.go in #7491. This must stay
		// gated so the M19 fix does not turn every real, correctly-checked
		// prebuilt-lookup call site into a new scan failure.
		fset, f, lines := parse(t, `package p

import "os/exec"

func build() {
	bd, err := bazeltest.PrebuiltBD()
	if err != nil {
		panic(err)
	}
	if bd == "" {
		exec.Command("go", "build")
	}
}
`)
		hits := findHostToolExecCalls(fset, f, lines)
		if len(hits) != 1 || !hits[0].gated {
			t.Fatalf("hits = %+v, want one gated hit: the error is checked and aborts, "+
				"so the invariant holds", hits)
		}
	})

	t.Run("hostToolExecInScope", func(t *testing.T) {
		for rel, want := range map[string]bool{
			"cmd/bd/compact_gc_target_test.go":              true,
			"internal/testutil/localdoltserver.go":          true,
			"internal/testutil/integration/harness.go":      true,
			"internal/testutil/bazeltest/bazeltest_test.go": true,
			"cmd/bd/compact.go":                             false,
			"internal/storage/dolt/store.go":                false,
			"internal/doltserver/doltserver.go":             false,
			"scripts/ci/go-list-test-names/main.go":         false,
		} {
			if got := hostToolExecInScope(rel); got != want {
				t.Errorf("hostToolExecInScope(%q) = %v, want %v", rel, got, want)
			}
		}
	})
}

// TestNoUngatedHostToolExecsUnderBazel scans the real tree (every *_test.go
// file and internal/testutil's non-test .go files) and fails on an
// unallowlisted, ungated literal go/gofmt exec. See the package comment.
func TestNoUngatedHostToolExecsUnderBazel(t *testing.T) {
	root := sourceRepoRoot(t)
	relFiles := repoFiles(t, root, 1000)

	type parsed struct {
		file *ast.File
		hits []hostToolExecHit
	}
	byFile := map[string]*ast.File{}
	results := map[string]*parsed{}
	scanned := 0
	for _, rel := range relFiles {
		if !hostToolExecInScope(rel) {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, data, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		scanned++
		lines := strings.Split(string(data), "\n")
		hits := findHostToolExecCalls(fset, f, lines)
		byFile[rel] = f
		results[rel] = &parsed{file: f, hits: hits}
	}
	if scanned < 1000 {
		t.Fatalf("scanned only %d in-scope files; the walk is broken", scanned)
	}

	seen := map[string]map[int]bool{} // rel -> allowlist entry index -> matched
	for rel, p := range results {
		for _, hit := range p.hits {
			if hit.gated {
				continue
			}
			if idx, ok := matchAllowlist(hostToolExecAllowlist[rel], hit); ok {
				if seen[rel] == nil {
					seen[rel] = map[int]bool{}
				}
				seen[rel][idx] = true
				continue
			}
			if gated, n := helperIsGatedByAllCallers(byFile, hit.funcName); gated && n > 0 {
				continue
			}
			t.Errorf("%s:%d: ungated %s(%q): under `bazel test` the hermetic-first PATH may lack a host %s "+
				"toolchain, so this can exit 127 on some workers (f4-spec.md §9); gate it on bazeltest.IsBazel()/"+
				"TEST_SRCDIR, resolve the binary through bazeltest.RunfileEnv/PrebuiltBD, ensure every caller of "+
				"its enclosing function gates it, or add a justified hostToolExecAllowlist entry", rel, hit.line, hit.call, hit.tool, hit.tool)
		}
	}
	for rel, entries := range hostToolExecAllowlist {
		if _, scannedFile := results[rel]; !scannedFile {
			t.Errorf("hostToolExecAllowlist entry for %s: file was not scanned (not in scope or missing)", rel)
			continue
		}
		for idx, entry := range entries {
			if !seen[rel][idx] {
				t.Errorf("hostToolExecAllowlist entry %s (func %q, snippet %q) no longer matches an "+
					"ungated host-tool exec; remove it", rel, entry.fn, entry.snippet)
			}
		}
	}
}

// matchAllowlist finds the allowlist entry (if any) matching hit by enclosing
// function name and exact source-line snippet, returning its index for
// usage-tracking.
func matchAllowlist(entries []hostToolAllowlistEntry, hit hostToolExecHit) (int, bool) {
	for i, e := range entries {
		if e.fn == hit.funcName && e.snippet == hit.snippet {
			return i, true
		}
	}
	return 0, false
}
