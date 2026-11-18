package scripts_test

// Policy: a spawn in a package that can run behind a console-less process must
// hide the console window Windows would otherwise allocate for it
// (gastownhall/beads#6076).
//
// Background. On Windows a console-subsystem child started by a process that
// has NO console of its own does not inherit one: the system allocates a brand
// new console for it. bd has no console whenever its stdout is redirected (an
// agent harness, a daemon, a CI step), and the db-proxy child is started with
// DETACHED_PROCESS, so it never has one. With Windows Terminal registered as
// the default terminal that console becomes a real window that takes focus,
// and a short-lived child such as `dolt init` can fail with 0x800700E8
// (HRESULT_FROM_WIN32(ERROR_NO_DATA)) before the handoff completes.
//
// internal/execwin.Hide ORs CREATE_NO_WINDOW into a command's creation flags
// (a no-op off Windows). A spawn is not wrong because of WHICH tool it runs
// but because of WHERE it runs, so the rule is name-agnostic: no tool name
// earns an exemption.
//
// Scope: the non-test .go files of consoleSpawnScope that go/build selects for
// GOOS=windows (build.Default with GOOS overridden, so `//go:build !windows`,
// `//go:build unix` and `_darwin.go` files are out; a bare `_unix.go` name is
// NOT a GOOS suffix and stays in unless its build line excludes Windows).
//
// What counts as a spawn: a call to Command/CommandContext through whatever
// name the file imports os/exec as (alias- and dot-import-aware), a reference
// to either as a function value, or an exec.Cmd{} composite literal.
//
// A spawn passes when:
//   - (a) it is the direct argument of execwin.Hide(...), matched by the
//     import path of internal/execwin rather than by the name "execwin"; when
//     that Hide(...) result is bound to a variable, no later SysProcAttr
//     assignment in the block may replace the attributes Hide set; or
//   - (b) it is bound to a variable v and an unconditional execwin.Hide(v)
//     statement follows in the same block, after every v.SysProcAttr
//     assignment and before any statement that starts v, passes it elsewhere
//     (an argument, a return, a closure) or otherwise reads it.
//
// There is deliberately no "the function assigns SysProcAttr" exemption: the
// shared dolt sql-server at internal/doltserver assigned procAttrDetached()
// (CREATE_NEW_PROCESS_GROUP only) and still opened a console window.

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const execwinImportPath = "github.com/steveyegge/beads/internal/execwin"

// consoleSpawn is one spawn the scan found. problem is empty when the spawn
// is hidden.
type consoleSpawn struct {
	line    int
	fn      string // enclosing top-level function ("Type.Method" for methods), "" at package scope
	snippet string // trimmed source text of the spawn's line
	problem string
}

// consoleSpawnScanner holds one file's import names.
type consoleSpawnScanner struct {
	execNames    map[string]bool // local names of os/exec
	execDot      bool            // os/exec is dot-imported
	execwinNames map[string]bool // local names of internal/execwin
	execwinDot   bool
}

func newConsoleSpawnScanner(file *ast.File) *consoleSpawnScanner {
	s := &consoleSpawnScanner{execNames: map[string]bool{}, execwinNames: map[string]bool{}}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		switch p {
		case "os/exec":
			if name == "." {
				s.execDot = true
			} else if name != "_" {
				s.execNames[name] = true
			}
		case execwinImportPath:
			if name == "." {
				s.execwinDot = true
			} else if name != "_" {
				s.execwinNames[name] = true
			}
		}
	}
	return s
}

// pkgMember reports whether e names member sel of a package imported under
// one of names (or dot-imported, when dot is set).
func pkgMember(e ast.Expr, names map[string]bool, dot bool, sel string) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && x.Sel.Name == sel && names[id.Name]
	case *ast.Ident:
		return dot && x.Name == sel
	}
	return false
}

func (s *consoleSpawnScanner) isExecCommandFunc(e ast.Expr) bool {
	return pkgMember(e, s.execNames, s.execDot, "Command") || pkgMember(e, s.execNames, s.execDot, "CommandContext")
}

// hideArg returns the argument of an execwin.Hide(...) call, or nil when e is
// not one.
func (s *consoleSpawnScanner) hideArg(e ast.Expr) ast.Expr {
	call, ok := ast.Unparen(e).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || !pkgMember(call.Fun, s.execwinNames, s.execwinDot, "Hide") {
		return nil
	}
	return call.Args[0]
}

// isHideOf reports whether e is execwin.Hide(v) or execwin.Hide(&v).
func (s *consoleSpawnScanner) isHideOf(e ast.Expr, v string) bool {
	arg := s.hideArg(e)
	if arg == nil {
		return false
	}
	arg = ast.Unparen(arg)
	if u, ok := arg.(*ast.UnaryExpr); ok && u.Op == token.AND {
		arg = ast.Unparen(u.X)
	}
	id, ok := arg.(*ast.Ident)
	return ok && id.Name == v
}

// scanConsoleSpawns returns every spawn in file with its verdict.
func scanConsoleSpawns(fset *token.FileSet, file *ast.File, lines []string) []consoleSpawn {
	s := newConsoleSpawnScanner(file)
	if len(s.execNames) == 0 && !s.execDot {
		return nil
	}
	var spawns []consoleSpawn
	var stack []ast.Node
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		parent := ast.Node(nil)
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		stack = append(stack, n)

		var problem string
		switch x := n.(type) {
		case *ast.CallExpr:
			if !s.isExecCommandFunc(x.Fun) {
				return true
			}
			problem = s.judge(stack)
		case *ast.CompositeLit:
			if !pkgMember(x.Type, s.execNames, s.execDot, "Cmd") {
				return true
			}
			problem = s.judge(stack)
		case *ast.SelectorExpr, *ast.Ident:
			expr := n.(ast.Expr)
			if !s.isExecCommandFunc(expr) {
				return true
			}
			if call, ok := parent.(*ast.CallExpr); ok && call.Fun == expr {
				return true // the call itself is the spawn
			}
			if sel, ok := parent.(*ast.SelectorExpr); ok && sel.Sel == expr {
				return true // the Sel half of exec.Command, seen through the SelectorExpr
			}
			problem = "exec.Command/CommandContext taken as a function value; its commands cannot be checked, call it directly inside execwin.Hide(...)"
		default:
			return true
		}
		line := fset.Position(n.Pos()).Line
		spawns = append(spawns, consoleSpawn{
			line:    line,
			fn:      enclosingFuncName(stack),
			snippet: snippetAt(lines, line),
			problem: problem,
		})
		return true
	})
	return spawns
}

// enclosingFuncName names the top-level function in stack.
func enclosingFuncName(stack []ast.Node) string {
	for _, n := range stack {
		fd, ok := n.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return fd.Name.Name
		}
		t := fd.Recv.List[0].Type
		if st, ok := t.(*ast.StarExpr); ok {
			t = st.X
		}
		if ix, ok := t.(*ast.IndexExpr); ok {
			t = ix.X
		}
		if id, ok := t.(*ast.Ident); ok {
			return id.Name + "." + fd.Name.Name
		}
		return fd.Name.Name
	}
	return ""
}

// judge returns "" when the spawn at the top of stack is hidden, else why not.
func (s *consoleSpawnScanner) judge(stack []ast.Node) string {
	// Climb from the spawn through parentheses and, for a composite literal,
	// its address-of, to the expression its context sees.
	i := len(stack) - 1
	expr := stack[i].(ast.Expr)
	_, isLit := expr.(*ast.CompositeLit)
	for i > 0 {
		switch p := stack[i-1].(type) {
		case *ast.ParenExpr:
			expr, i = p, i-1
			continue
		case *ast.UnaryExpr:
			if isLit && p.Op == token.AND {
				expr, i = p, i-1
				continue
			}
		}
		break
	}

	hidden := false
	if i > 0 {
		if call, ok := stack[i-1].(*ast.CallExpr); ok && s.hideArg(call) == expr {
			// (a) execwin.Hide(spawn). If its result is bound, the binding
			// must not have its SysProcAttr replaced afterwards.
			hidden = true
			expr, i = call, i-1
			for i > 0 {
				p, ok := stack[i-1].(*ast.ParenExpr)
				if !ok {
					break
				}
				expr, i = p, i-1
			}
		}
	}

	v, stmt := boundVar(stack[:i], expr)
	if v == "" {
		if hidden {
			return ""
		}
		return "spawn is not hidden: wrap it in execwin.Hide(...), or bind it and call execwin.Hide(v) before it is started"
	}
	list, idx := enclosingStmtList(stack[:i], stmt)
	if list == nil {
		if hidden {
			return ""
		}
		return "spawn is bound to " + v + " outside a statement block, so no execwin.Hide(" + v + ") can follow it"
	}
	return s.followBinding(list[idx+1:], v, hidden)
}

// boundVar reports the variable expr is assigned to and the statement that
// assigns it, or "" when expr is not the right-hand side of a plain binding.
func boundVar(stack []ast.Node, expr ast.Expr) (string, ast.Stmt) {
	if len(stack) == 0 {
		return "", nil
	}
	switch p := stack[len(stack)-1].(type) {
	case *ast.AssignStmt:
		if len(p.Lhs) != len(p.Rhs) {
			return "", nil
		}
		for k, r := range p.Rhs {
			if r != expr {
				continue
			}
			if id, ok := p.Lhs[k].(*ast.Ident); ok && id.Name != "_" {
				return id.Name, p
			}
		}
	case *ast.ValueSpec:
		for k, r := range p.Values {
			if r == expr && k < len(p.Names) && p.Names[k].Name != "_" && len(stack) >= 3 {
				if ds, ok := stack[len(stack)-3].(*ast.DeclStmt); ok {
					return p.Names[k].Name, ds
				}
			}
		}
	}
	return "", nil
}

// enclosingStmtList returns the statement list stmt is a direct member of.
func enclosingStmtList(stack []ast.Node, stmt ast.Stmt) ([]ast.Stmt, int) {
	for j := len(stack) - 1; j >= 0; j-- {
		var list []ast.Stmt
		switch b := stack[j].(type) {
		case *ast.BlockStmt:
			list = b.List
		case *ast.CaseClause:
			list = b.Body
		case *ast.CommClause:
			list = b.Body
		default:
			continue
		}
		for k, st := range list {
			if st == stmt {
				return list, k
			}
		}
		return nil, 0
	}
	return nil, 0
}

// varUse classifies the references to v inside one statement.
type varUse struct {
	any       bool // v is referenced at all
	escape    bool // v is read, called, passed, returned or rebound
	attrWrite bool // v.SysProcAttr (or a field under it) is assigned
	hide      bool // an execwin.Hide(v) appears somewhere inside
}

// useOf classifies how st references v. Field writes `v.F = <expr not
// mentioning v>` are inert; everything else that mentions v is an escape.
func (s *consoleSpawnScanner) useOf(st ast.Stmt, v string) varUse {
	var u varUse
	var stack []ast.Node
	ast.Inspect(st, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		if e, ok := n.(ast.Expr); ok && s.isHideOf(e, v) {
			u.hide = true
		}
		id, ok := n.(*ast.Ident)
		if !ok || id.Name != v {
			return true
		}
		if len(stack) >= 2 {
			if sel, ok := stack[len(stack)-2].(*ast.SelectorExpr); ok && sel.Sel == id {
				return true // x.v: a field or method named v, not the variable
			}
		}
		u.any = true
		// Walk up a pure selector chain rooted at v: v.A.B...
		j := len(stack) - 1
		var expr ast.Expr = id
		var first string
		for j > 0 {
			sel, ok := stack[j-1].(*ast.SelectorExpr)
			if !ok || sel.X != expr {
				break
			}
			if first == "" {
				first = sel.Sel.Name
			}
			expr, j = sel, j-1
		}
		if first != "" && j > 0 {
			if as, ok := stack[j-1].(*ast.AssignStmt); ok && as.Tok != token.DEFINE {
				for _, l := range as.Lhs {
					if l == expr {
						if first == "SysProcAttr" {
							u.attrWrite = true
						}
						return true
					}
				}
			}
		}
		u.escape = true
		return true
	})
	return u
}

// isHideStmt reports whether st is exactly `execwin.Hide(v)`,
// `_ = execwin.Hide(v)` or `v = execwin.Hide(v)`.
func (s *consoleSpawnScanner) isHideStmt(st ast.Stmt, v string) bool {
	switch x := st.(type) {
	case *ast.ExprStmt:
		return s.isHideOf(x.X, v)
	case *ast.AssignStmt:
		if len(x.Lhs) != 1 || len(x.Rhs) != 1 || x.Tok != token.ASSIGN || !s.isHideOf(x.Rhs[0], v) {
			return false
		}
		id, ok := x.Lhs[0].(*ast.Ident)
		return ok && (id.Name == "_" || id.Name == v)
	}
	return false
}

// followBinding walks the statements after v's binding and returns "" when v
// is hidden before it is started or passed on.
func (s *consoleSpawnScanner) followBinding(rest []ast.Stmt, v string, hidden bool) string {
	reassigned := false
	for _, st := range rest {
		if s.isHideStmt(st, v) {
			hidden = true
			continue
		}
		u := s.useOf(st, v)
		if !u.any {
			continue
		}
		if u.escape {
			switch {
			case hidden:
				return ""
			case u.hide:
				return "execwin.Hide(" + v + ") does not run unconditionally in " + v + "'s block; call it as a plain statement before " + v + " is started"
			case reassigned:
				return v + ".SysProcAttr is assigned after execwin.Hide(" + v + "), replacing CREATE_NO_WINDOW; call Hide after the last SysProcAttr assignment"
			default:
				return v + " is started, passed on or read before execwin.Hide(" + v + "); call Hide right before that"
			}
		}
		if u.attrWrite {
			if hidden {
				reassigned = true
			}
			hidden = false
		}
	}
	if hidden {
		return ""
	}
	if reassigned {
		return v + ".SysProcAttr is assigned after execwin.Hide(" + v + "), replacing CREATE_NO_WINDOW; call Hide after the last SysProcAttr assignment"
	}
	return "spawn bound to " + v + " is never hidden by an unconditional execwin.Hide(" + v + ") in its block"
}

// consoleSpawnPolicy is the guard's configuration; the repository's is
// consoleSpawnRepoPolicy, and the fixture tests inject their own.
type consoleSpawnPolicy struct {
	// scope lists repo-relative package directories; a "/..." suffix
	// includes every package below it.
	scope []string
}

// inScope reports whether rel (repo-relative, slash-separated) is a non-test
// Go file in one of p.scope.
func (p consoleSpawnPolicy) inScope(rel string) bool {
	if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
		return false
	}
	dir := path.Dir(rel)
	for _, s := range p.scope {
		if base, ok := strings.CutSuffix(s, "/..."); ok {
			if dir == base || strings.HasPrefix(dir, base+"/") {
				return true
			}
		} else if dir == s {
			return true
		}
	}
	return false
}

// consoleSpawnFile is one scanned file's result.
type consoleSpawnFile struct {
	rel    string
	spawns []consoleSpawn
}

// windowsFileContext selects files the way `GOOS=windows go build` would.
func windowsFileContext() build.Context {
	ctx := build.Default
	ctx.GOOS = "windows"
	ctx.GOARCH = "amd64"
	ctx.CgoEnabled = false
	return ctx
}

// scanConsoleSpawnFiles parses every in-scope, Windows-buildable file of
// relFiles under root.
func (p consoleSpawnPolicy) scanFiles(root string, relFiles []string) ([]consoleSpawnFile, error) {
	ctx := windowsFileContext()
	var out []consoleSpawnFile
	for _, rel := range relFiles {
		if !p.inScope(rel) {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		ok, err := ctx.MatchFile(filepath.Dir(abs), filepath.Base(abs))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, abs, data, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		out = append(out, consoleSpawnFile{rel: rel, spawns: scanConsoleSpawns(fset, f, strings.Split(string(data), "\n"))})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].rel < out[b].rel })
	return out, nil
}

// check runs the policy over relFiles under root and returns one message per
// violation, each starting with file:line or naming what is wrong.
func (p consoleSpawnPolicy) check(root string, relFiles []string) ([]string, error) {
	files, err := p.scanFiles(root, relFiles)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, f := range files {
		for _, sp := range f.spawns {
			if sp.problem == "" {
				continue
			}
			problems = append(problems, f.rel+":"+strconv.Itoa(sp.line)+": "+sp.problem+" (GH#6076: on Windows a console-less bd gives this child a visible console)")
		}
	}
	return problems, nil
}

// writeConsoleSpawnFixtures writes files (repo-relative path -> source) under
// a fresh root and returns it with the sorted relative paths.
func writeConsoleSpawnFixtures(t *testing.T, files map[string]string) (string, []string) {
	t.Helper()
	root := t.TempDir()
	var rels []string
	for rel, src := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	return root, rels
}

// problemsFor returns the problems reported for rel.
func problemsFor(problems []string, rel string) []string {
	var out []string
	for _, p := range problems {
		if strings.HasPrefix(p, rel+":") {
			out = append(out, p)
		}
	}
	return out
}

// TestConsoleSpawnPolicyScan pins the scanner's rule on fixtures, one file per
// shape, each in a single package of an injected scope.
func TestConsoleSpawnPolicyScan(t *testing.T) {
	const hdr = "package fix\n\nimport (\n\t\"context\"\n\t\"os/exec\"\n\t\"syscall\"\n\n\t\"github.com/steveyegge/beads/internal/execwin\"\n)\n\n" +
		"var _ = context.Background\nvar _ syscall.SysProcAttr\nvar _ = execwin.Hide\n\n"
	cases := []struct {
		name       string
		file       string
		src        string
		wantReason string // "" means not reported
	}{
		{
			name:       "unwrapped spawn",
			file:       "unwrapped.go",
			src:        hdr + "func f() { _ = exec.Command(\"anything\").Run() }\n",
			wantReason: "not hidden",
		},
		{
			name: "SysProcAttr assigned after Hide",
			file: "attr_after_hide.go",
			src: hdr + `func f() error {
	cmd := exec.Command("dolt", "sql-server")
	execwin.Hide(cmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	return cmd.Start()
}
`,
			wantReason: "SysProcAttr is assigned after execwin.Hide",
		},
		{
			name: "SysProcAttr assigned after an inline Hide",
			file: "attr_after_inline_hide.go",
			src: hdr + `func f() error {
	cmd := execwin.Hide(exec.Command("dolt", "sql-server"))
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	return cmd.Start()
}
`,
			wantReason: "SysProcAttr is assigned after execwin.Hide",
		},
		{
			name: "Hide only inside an if",
			file: "hide_in_if.go",
			src: hdr + `func f(quiet bool) error {
	cmd := exec.Command("dolt", "init")
	if quiet {
		execwin.Hide(cmd)
	}
	return cmd.Run()
}
`,
			wantReason: "does not run unconditionally",
		},
		{
			name: "Hide after Start",
			file: "hide_after_start.go",
			src: hdr + `func f() error {
	cmd := exec.Command("dolt", "sql-server")
	if err := cmd.Start(); err != nil {
		return err
	}
	execwin.Hide(cmd)
	return nil
}
`,
			wantReason: "before execwin.Hide",
		},
		{
			name: "Hide after the cmd is passed on",
			file: "hide_after_pass.go",
			src: hdr + `func launch(*exec.Cmd) error { return nil }

func f() error {
	cmd := exec.Command("dolt", "sql-server")
	err := launch(cmd)
	execwin.Hide(cmd)
	return err
}
`,
			wantReason: "before execwin.Hide",
		},
		{
			name: "aliased os/exec import",
			file: "aliased.go",
			src: "package fix\n\nimport osexec \"os/exec\"\n\n" +
				"func f() ([]byte, error) { return osexec.Command(\"netstat\", \"-aon\").Output() }\n",
			wantReason: "not hidden",
		},
		{
			name: "dot-imported os/exec",
			file: "dot.go",
			src: "package fix\n\nimport . \"os/exec\"\n\n" +
				"func f() ([]byte, error) { return Command(\"netstat\", \"-aon\").Output() }\n",
			wantReason: "not hidden",
		},
		{
			name:       "exec.Cmd literal",
			file:       "literal.go",
			src:        hdr + "func f() error { return (&exec.Cmd{Path: \"dolt\"}).Run() }\n",
			wantReason: "not hidden",
		},
		{
			name:       "exec.Command as a function value",
			file:       "funcvalue.go",
			src:        hdr + "var mk = exec.Command\n",
			wantReason: "function value",
		},
		{
			name: "Hide from another package that is also named execwin",
			file: "impostor.go",
			src: "package fix\n\nimport (\n\t\"os/exec\"\n\n\t\"example.com/other/execwin\"\n)\n\n" +
				"func f() error { return execwin.Hide(exec.Command(\"dolt\", \"init\")).Run() }\n",
			wantReason: "not hidden",
		},
		{
			name: "wrapped spawn",
			file: "wrapped.go",
			src: hdr + `func f(ctx context.Context) ([]byte, error) {
	if out, err := execwin.Hide(exec.CommandContext(ctx, "git", "config", "user.name")).Output(); err == nil {
		return out, nil
	}
	cmd := execwin.Hide(exec.Command("dolt", "init"))
	cmd.Dir = "x"
	return cmd.CombinedOutput()
}
`,
		},
		{
			name: "wrapped spawn through an aliased execwin import",
			file: "wrapped_alias.go",
			src: "package fix\n\nimport (\n\t\"os/exec\"\n\n\tew \"github.com/steveyegge/beads/internal/execwin\"\n)\n\n" +
				"func f() error { return ew.Hide(&exec.Cmd{Path: \"dolt\"}).Run() }\n",
		},
		{
			name: "bound-then-hidden spawn",
			file: "bound_hidden.go",
			src: hdr + `func f() error {
	cmd := exec.Command("dolt", "sql-server")
	cmd.Dir = "x"
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	execwin.Hide(cmd)
	cmd.Env = []string{"A=1"}
	return cmd.Start()
}
`,
		},
		{
			name: "spawn in a //go:build !windows file",
			file: "notwindows.go",
			src:  "//go:build !windows\n\n" + hdr + "func g() error { return exec.Command(\"lsof\").Run() }\n",
		},
		{
			name: "spawn in a _unix.go file",
			file: "probe_unix.go",
			src:  "//go:build unix\n\n" + hdr + "func h() error { return exec.Command(\"ps\").Run() }\n",
		},
		{
			name: "spawn in a _darwin.go file",
			file: "sweep_darwin.go",
			src:  hdr + "func k() error { return exec.Command(\"ps\").Run() }\n",
		},
	}

	files := map[string]string{}
	for _, c := range cases {
		files["fix/"+c.file] = c.src
	}
	root, rels := writeConsoleSpawnFixtures(t, files)
	problems, err := consoleSpawnPolicy{scope: []string{"fix"}}.check(root, rels)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := problemsFor(problems, "fix/"+c.file)
			if c.wantReason == "" {
				if len(got) != 0 {
					t.Fatalf("reported, want accepted:\n%s", strings.Join(got, "\n"))
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d reports, want 1:\n%s", len(got), strings.Join(got, "\n"))
			}
			if !strings.Contains(got[0], c.wantReason) {
				t.Fatalf("report %q does not say %q", got[0], c.wantReason)
			}
		})
	}
}
