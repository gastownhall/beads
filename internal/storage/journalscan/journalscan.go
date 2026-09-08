// Package journalscan provides the static-analysis primitives the events
// journal completeness guards share. Both the issueops seam and the domain/db
// unit-of-work seam must journal every mutation that writes a work-bead table;
// their guard tests detect such mutators STRUCTURALLY — by the DML a function
// executes — rather than by matching on method-name prefixes, which could let a
// mutator named off-pattern ship un-journaled. This package holds the parsing,
// bead-table DML detection, and call-graph fixpoint those guards run.
package journalscan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

// FuncInfo captures the call/DML shape of one top-level function or method,
// keyed by a package-unique name (receiver-qualified for methods).
type FuncInfo struct {
	Recv       string   // receiver type name ("" for free functions)
	Name       string   // bare method/function name
	Exported   bool     // the bare name is exported
	Params     []string // declared parameter names, in order ("" for an unnamed one)
	IdentCalls []string // intra-package bare-identifier calls (free functions)
	SelCalls   []string // selector calls, by selector name (x.Foo -> "Foo")
	Calls      []Call   // every call, bare or selector, with its arguments
	OwnBeadDML bool     // body issues INSERT/UPDATE/DELETE against a bead table
}

// Call is one call expression in a function body: the called name (a bare
// identifier, or the selector name of x.Foo) and its arguments, each reduced
// to the bare identifier or literal it passes — "" for any other expression.
// The predeclared false and true read as "false" and "true"; a false or true
// that names a declaration of the package's own instead — a shadow, whether
// the parser resolved it within its file or some file declares the name at
// package level — is "" like any other expression the scanner cannot vouch
// for, so a shadow never reads as the literal. A caller forwarding its own
// parameter reads as that parameter's name, which is what lets a guard tell
// "switched off here" from "left to the caller".
type Call struct {
	Name string
	Args []string
}

// AllCallNames returns every called name, both bare-identifier and selector.
func (f *FuncInfo) AllCallNames() []string {
	return append(append([]string{}, f.IdentCalls...), f.SelCalls...)
}

// CallsAnyOf reports whether the function calls any name in set (bare or selector).
func (f *FuncInfo) CallsAnyOf(set map[string]bool) bool {
	for _, c := range f.AllCallNames() {
		if set[c] {
			return true
		}
	}
	return false
}

// ParamIndex returns the position of f's parameter named name, or -1 when f
// declares none by that name.
func (f *FuncInfo) ParamIndex(name string) int {
	for i, p := range f.Params {
		if p == name {
			return i
		}
	}
	return -1
}

// ReceiverTypeName returns the bare type name of a method receiver
// (e.g. *fooImpl -> fooImpl).
func ReceiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// ParsePackage parses dir's non-test .go files and returns one FuncInfo per
// top-level function/method, keyed by "Recv.Name" (or "Name" for free funcs).
func ParsePackage(dir string) (map[string]*FuncInfo, error) {
	fset := token.NewFileSet()
	// Mode 0 keeps the parser's per-file identifier resolution on. It is what
	// lets argNames tell the predeclared false and true from a shadow of the
	// same name; under SkipObjectResolution every shadow would read as the
	// literal. TestCallsOnlyWithLiteralFalse pins this.
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, err
	}
	// That resolution stops at the file: a use in one file of a false another
	// file declares at package level stays unresolved, exactly like the
	// predeclared one, though to the compiler it denotes the declaration.
	// Fold every file's package-level declarations in so argNames refuses
	// those too (TestPackageLevelShadowCrossesFiles).
	shadowed := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for name := range predeclaredBools {
				if file.Scope != nil && file.Scope.Lookup(name) != nil {
					shadowed[name] = true
				}
			}
		}
	}
	out := map[string]*FuncInfo{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				f := &FuncInfo{Name: fn.Name.Name, Exported: fn.Name.IsExported(), Params: paramNames(fn.Type)}
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					f.Recv = ReceiverTypeName(fn.Recv.List[0].Type)
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					switch node := n.(type) {
					case *ast.CallExpr:
						switch fun := node.Fun.(type) {
						case *ast.Ident:
							f.IdentCalls = append(f.IdentCalls, fun.Name)
							f.Calls = append(f.Calls, Call{Name: fun.Name, Args: argNames(node.Args, shadowed)})
						case *ast.SelectorExpr:
							f.SelCalls = append(f.SelCalls, fun.Sel.Name)
							f.Calls = append(f.Calls, Call{Name: fun.Sel.Name, Args: argNames(node.Args, shadowed)})
						}
					case *ast.BasicLit:
						if node.Kind == token.STRING && SQLWritesBeadTable(node.Value) {
							f.OwnBeadDML = true
						}
					}
					return true
				})
				key := f.Name
				if f.Recv != "" {
					key = f.Recv + "." + f.Name
				}
				out[key] = f
			}
		}
	}
	return out, nil
}

// paramNames flattens a signature's parameter list into one entry per
// parameter, so a position in it lines up with a position in a call's
// argument list: "a, b int" is two entries, and an unnamed parameter is "".
func paramNames(sig *ast.FuncType) []string {
	if sig == nil || sig.Params == nil {
		return nil
	}
	var names []string
	for _, field := range sig.Params.List {
		if len(field.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	return names
}

// predeclaredBools are the names argNames reads as the boolean literals when
// nothing in the package declares them.
var predeclaredBools = map[string]bool{"false": true, "true": true}

// argNames reduces call arguments to the bare identifier or literal each one
// passes; anything else (a call, a selector, a composite literal) is "". So
// is a false or true that does not denote the predeclared identifier: one the
// parser resolved to a declaration in its own file (a.Obj != nil — a local, a
// parameter, a package-level declaration in the same file) or whose name some
// file declares at package level (shadowed, from ParsePackage).
func argNames(args []ast.Expr, shadowed map[string]bool) []string {
	names := make([]string, len(args))
	for i, arg := range args {
		switch a := arg.(type) {
		case *ast.Ident:
			if predeclaredBools[a.Name] && (a.Obj != nil || shadowed[a.Name]) {
				continue
			}
			names[i] = a.Name
		case *ast.BasicLit:
			names[i] = a.Value
		}
	}
	return names
}

// Resolve maps a called bare name to the function keys it can denote: the
// free function of that name, if any, then every method of that name in key
// order (name-based resolution, sufficient for a guard). It is the one
// resolution Fixpoint and CallsOnlyWithLiteralFalse use, exported so a guard
// reasoning about a called name's targets — whether a switch-off is
// unambiguous, whether a live edge reaches the seam through a method — sees
// exactly the keys the fixpoint follows.
func Resolve(fns map[string]*FuncInfo, name string) []string {
	var keys []string
	if _, ok := fns[name]; ok {
		keys = append(keys, name)
	}
	var methods []string
	for key, f := range fns {
		if f.Recv != "" && f.Name == name {
			methods = append(methods, key)
		}
	}
	sort.Strings(methods)
	return append(keys, methods...)
}

// CallsOnlyWithLiteralFalse reports whether f calls callee, and every one of
// those calls passes the predeclared identifier false for callee's boolean
// parameter named gate — the argument callee's own body reads as "skip the
// gated write". A guard that follows call edges to find who reaches a seam
// uses it to drop the edges a caller has explicitly switched off, so a
// composite mutation that runs its constituents with the gate off and
// performs the gated write once itself is not credited with the one it told
// them to skip.
//
// What is checked is the reduced argument (see Call): the bare name false,
// counted as the predeclared identifier only when it resolves to no
// declaration of the package's own. Go lets a local, a parameter or a
// package-level declaration shadow false, and any such shadow reads as an
// unknown expression, never as a switch-off. A consumer can pin that its
// package declares no such shadow at all, so that the name-based reading is
// exact there rather than merely conservative; issueops does, in
// TestFalseIsNotShadowedInIssueops.
//
// The edge stays live (false is returned) whenever the switch-off is not
// certain: callee is unknown or declares no parameter named gate, f never
// calls it, at least one of f's calls passes anything but the literal (true,
// a variable, f's own parameter of that name, a shadowed false, an
// expression), or a call is too short to line up with the signature. callee
// resolves the way Fixpoint resolves edges — Resolve: the free function of
// that name and every method of that name — and every resolution that
// declares the gate must see false. A resolution that declares no gate is
// not consulted, so when a gated function shares its name with an ungated
// one a true result drops the fixpoint's edge to the ungated one too; a
// consumer relying on the switch-off should pin that no literal-false
// callee's name is shared that way (issueops does, in
// TestLiteralFalseCalleesResolveUnambiguously).
func CallsOnlyWithLiteralFalse(fns map[string]*FuncInfo, f *FuncInfo, callee, gate string) bool {
	var gateAt []int
	for _, key := range Resolve(fns, callee) {
		if i := fns[key].ParamIndex(gate); i >= 0 {
			gateAt = append(gateAt, i)
		}
	}
	if len(gateAt) == 0 {
		return false
	}
	called := false
	for _, call := range f.Calls {
		if call.Name != callee {
			continue
		}
		called = true
		for _, i := range gateAt {
			if i >= len(call.Args) || call.Args[i] != "false" {
				return false
			}
		}
	}
	return called
}

// Fixpoint returns the set of function keys for which seed is true or which
// (transitively) call a name for which it becomes true, following edges. A
// called bare name resolves to a free function of that name and to any method
// with that name (Resolve: name-based resolution, sufficient for a guard).
func Fixpoint(fns map[string]*FuncInfo, seed func(*FuncInfo) bool, edges func(*FuncInfo) []string) map[string]bool {
	got := map[string]bool{}
	for key, f := range fns {
		if seed(f) {
			got[key] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for key, f := range fns {
			if got[key] {
				continue
			}
			for _, callee := range edges(f) {
				for _, ck := range Resolve(fns, callee) {
					if got[ck] {
						got[key] = true
						changed = true
						break
					}
				}
				if got[key] {
					break
				}
			}
		}
	}
	return got
}

// BeadTables are the work-bead tables a mutation must be journaled for.
var BeadTables = []string{
	"issues", "wisps",
	"dependencies", "wisp_dependencies",
	"labels", "wisp_labels",
	"comments", "wisp_comments",
}

// indexedVerb matches an explicit-argument-index format verb (%[1]s), which is
// the same templated table name as %s as far as this detector is concerned. It
// is normalized away before matching so a mutator cannot slip past the guard by
// reusing one format argument.
var indexedVerb = regexp.MustCompile(`%\[[0-9]+\]`)

// SQLWritesBeadTable reports whether a SQL string literal issues an
// INSERT / UPDATE / DELETE against a work-bead table, whether the table name is
// literal (INSERT INTO issues) or templated (INSERT INTO %s / INSERT INTO %[1]s
// — which in the mutation seams always routes to a bead table via table-routing
// helpers).
func SQLWritesBeadTable(lit string) bool {
	s := strings.ToUpper(lit)
	s = strings.ReplaceAll(s, "`", "")
	s = indexedVerb.ReplaceAllString(s, "%")
	s = strings.Join(strings.Fields(s), " ") // collapse whitespace/newlines
	targets := []string{"%S"}
	for _, tbl := range BeadTables {
		targets = append(targets, strings.ToUpper(tbl))
	}
	for _, tbl := range targets {
		if strings.Contains(s, "INSERT INTO "+tbl+" ") ||
			strings.Contains(s, "INSERT IGNORE INTO "+tbl+" ") ||
			strings.Contains(s, "REPLACE INTO "+tbl+" ") ||
			strings.Contains(s, "UPDATE "+tbl+" ") ||
			strings.Contains(s, "DELETE FROM "+tbl+" ") {
			return true
		}
	}
	return false
}
