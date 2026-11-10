package scripts_test

// Policy: every ordinary os/exec command construction in the console-spawn
// scopes must be wrapped in execwin.Hide, and the three long-lived database
// children must be pinned to their designated SysProcAttr helpers
// (GH#6076, bd-l5o8ld).
//
// Why: a Windows console-subsystem child started from a parent with no
// console gets a brand new console of its own — a visible window when
// Windows Terminal is the default terminal. bd runs console-less as an MCP
// server, a hook, or an agent harness, and every dolt, git or PowerShell
// command it then spawns would open a window. execwin.Hide suppresses that
// for ordinary short-lived children without touching a foreground bd (an
// attached parent keeps the command unchanged).
//
// Scope: the directories whose spawns are all ordinary (internal/doltserver,
// internal/doltversion, internal/storage/dbproxy, internal/storage/dolt,
// internal/storage/doltutil, internal/remotecache, internal/beads,
// internal/git — directory scopes include every descendant) and the
// individual files in cmd/bd and elsewhere that a console-less bd reaches
// with ordinary commands. A file scope covers exactly that file, never a
// sibling (TestConsoleSpawnScope pins it), and exclusion is not a claim that
// an excluded path is interactive — only that it is not guarded here.
//
// A construction is recognized when it is an os/exec Command or
// CommandContext call, under any import alias, directly inside a
// execwin.Hide(...) call, or exempted by longLivedSpawnSites below. The
// three long-lived children (bd db-proxy-child, the managed sql-server, the
// shared sql-server) are deliberately NOT Hide-wrapped: they need their
// attributes unconditionally, so each is pinned by path, function, command
// and variable to a designated helper that must be assigned to that
// command's SysProcAttr after construction and before Start; the helper's
// Windows half must delegate to execwin.BackgroundSysProcAttr
// (TestConsoleSpawnAllowlistMatching). An arbitrary attribute assignment
// elsewhere in a function exempts nothing.
//
// Also rejected, as bounded AST rules (not general control-flow analysis,
// and no claim about what third-party code spawns): constructor function
// values (exec.Command passed as a value), exec.Cmd composite literals,
// direct os/syscall StartProcess and windows CreateProcess calls, a later
// SysProcAttr assignment that would clobber a wrapped command's hidden
// flag, and — outside the pinned helpers' own platform halves — any
// mention of the DETACHED_PROCESS, CREATE_NEW_CONSOLE or CREATE_NO_WINDOW
// literals, which must go through internal/execwin. Unix-only assignments
// (Setpgid, Setsid, Pdeathsig) and the platform-helper implementations are
// preserved through the exact allowlist entries, which fail when stale.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// consoleSpawnScope is one guarded scope: dir includes every descendant
// file, file covers exactly that file.
type consoleSpawnScope struct {
	dir  string
	file string
}

var consoleSpawnScopes = []consoleSpawnScope{
	{dir: "internal/doltserver"},
	{dir: "internal/doltversion"},
	{dir: "internal/storage/dbproxy"},
	{dir: "internal/storage/dolt"},
	{dir: "internal/storage/doltutil"},
	{dir: "internal/remotecache"},
	{dir: "internal/beads"},
	{dir: "internal/git"},
	{file: "cmd/bd/main.go"},
	{file: "cmd/bd/compact.go"},
	{file: "cmd/bd/prime.go"},
	{file: "cmd/bd/agent_hook.go"},
	{file: "cmd/bd/proxied_server.go"},
	{file: "cmd/bd/hooks.go"},
	{file: "cmd/bd/export_auto.go"},
	{file: "cmd/bd/gate.go"},
	{file: "internal/metrics/spawn.go"},
	{file: "internal/hooks/hooks_windows.go"},
}

// consoleSpawnPlatformHalf pins one platform half of a long-lived helper:
// file must contain the require text inside (or at least after) its
// func helper() declaration, so a renamed or gutted half fails.
type consoleSpawnPlatformHalf struct {
	file    string
	require string
}

// longLivedSpawnSite exempts one long-lived construction and pins it: the
// file, the enclosing function, the variable the command is assigned to,
// the go/printer rendering of the call, the SysProcAttr helper that must
// be assigned to that variable after construction (and before Start, where
// the same function starts the command), the helper's Windows half (which
// must delegate to execwin.BackgroundSysProcAttr), and the non-Windows
// halves whose existing attributes must survive untouched.
type longLivedSpawnSite struct {
	file     string
	function string
	varName  string
	command  string
	helper   string
	winHalf  string
	// otherHalves are the non-Windows halves of the same helper; each is
	// pinned to keep what it already returns (Setpgid, Setsid, nil, ...).
	otherHalves []consoleSpawnPlatformHalf
}

var longLivedSpawnSites = []longLivedSpawnSite{
	{
		file:     "internal/doltserver/doltserver.go",
		function: "startLocked",
		varName:  "cmd",
		command:  "exec.Command(doltBin, cmdArgs...)",
		helper:   "procAttrDetached",
		winHalf:  "internal/doltserver/doltserver_windows.go",
		otherHalves: []consoleSpawnPlatformHalf{
			{file: "internal/doltserver/procattr_linux.go", require: "Setpgid"},
			{file: "internal/doltserver/procattr_other_unix.go", require: "Setpgid"},
		},
	},
	{
		file:     "internal/storage/dbproxy/proxy/endpoint.go",
		function: "forkExecChild",
		varName:  "cmd",
		command:  "exec.Command(self, args...)",
		helper:   "procAttrDetached",
		winHalf:  "internal/storage/dbproxy/proxy/endpoint_windows.go",
		otherHalves: []consoleSpawnPlatformHalf{
			{file: "internal/storage/dbproxy/proxy/endpoint_unix.go", require: "Setsid"},
		},
	},
	{
		file:     "internal/storage/dbproxy/server/doltserver.go",
		function: "startAttempt",
		varName:  "cmd",
		command:  "exec.CommandContext(managedCtx, s.doltBinExec, args...)",
		helper:   "managedServerSysProcAttr",
		winHalf:  "internal/storage/dbproxy/server/managed_server_windows.go",
		otherHalves: []consoleSpawnPlatformHalf{
			{file: "internal/storage/dbproxy/server/managed_server_other.go", require: "return nil"},
		},
	},
}

// consoleSpawnProblem is one violation, with the file and line it occurred
// at (line 0 for stale scope or allowlist entries, which name no line).
type consoleSpawnProblem struct {
	file string
	line int
	msg  string
}

type consoleSpawnHit struct {
	call     *ast.CallExpr
	render   string // go/printer text of the construction
	funcName string // enclosing top-level function, "" if none
	varName  string // variable the command is assigned to, "" if none
	line     int
}

type consoleSpawnAssign struct {
	pos      token.Pos
	varName  string
	callName string // helper called on the RHS, "" when not a plain call
	line     int
}

// consoleSpawnHideSpan is the token range of one execwin.Hide(...) call's
// argument list: a construction directly inside it is wrapped.
type consoleSpawnHideSpan struct {
	start, end token.Pos
}

type consoleSpawnStart struct {
	pos     token.Pos
	varName string
}

type consoleSpawnFuncCtx struct {
	name    string // enclosing top-level function name, "" for a bare func literal
	body    *ast.BlockStmt
	hits    []*consoleSpawnHit
	assigns []consoleSpawnAssign
	starts  []consoleSpawnStart
}

// consoleSpawnScan is one scan over a set of Go sources: the real
// repository (TestNoUnsuppressedConsoleSpawns,
// TestConsoleSpawnAllowlistMatching) or a fixture set
// (TestConsoleSpawnPolicyScan, TestConsoleSpawnScope).
type consoleSpawnScan struct {
	scopes    []consoleSpawnScope
	allowlist []longLivedSpawnSite
	files     map[string]string // repo-relative, slash-separated path -> source
	problems  []consoleSpawnProblem
	// scopeHits counts recognized constructions per scope entry; an entry
	// with none is stale. allowHits counts matches per allowlist entry; an
	// entry with zero is stale and one with more than one is ambiguous.
	scopeHits map[int]int
	allowHits map[int]int
}

func newConsoleSpawnScan(scopes []consoleSpawnScope, allowlist []longLivedSpawnSite, files map[string]string) *consoleSpawnScan {
	return &consoleSpawnScan{
		scopes:    scopes,
		allowlist: allowlist,
		files:     files,
		scopeHits: map[int]int{},
		allowHits: map[int]int{},
	}
}

// consoleSpawnExcluded reports whether rel is never scanned: test files
// and testdata.
func consoleSpawnExcluded(rel string) bool {
	return strings.HasSuffix(rel, "_test.go") || strings.Contains(rel, "/testdata/")
}

// consoleSpawnScopeOf returns the index of the scope entry covering rel.
func consoleSpawnScopeOf(scopes []consoleSpawnScope, rel string) (int, bool) {
	for i, sc := range scopes {
		if sc.file != "" {
			if sc.file == rel {
				return i, true
			}
			continue
		}
		if rel == sc.dir || strings.HasPrefix(rel, sc.dir+"/") {
			return i, true
		}
	}
	return 0, false
}

func (s *consoleSpawnScan) problemf(file string, line int, format string, args ...any) {
	s.problems = append(s.problems, consoleSpawnProblem{file: file, line: line, msg: fmt.Sprintf(format, args...)})
}

// run scans every in-scope file and returns the problems found, including
// stale scope and allowlist entries.
func (s *consoleSpawnScan) run() []consoleSpawnProblem {
	for _, entry := range s.allowlist {
		if _, ok := s.files[entry.file]; !ok {
			s.problemf(entry.file, 0, "longLivedSpawnSites entry: file was not scanned (out of scope or missing)")
		}
	}
	for rel, src := range s.files {
		ix, ok := consoleSpawnScopeOf(s.scopes, rel)
		if !ok || consoleSpawnExcluded(rel) {
			continue
		}
		s.scanFile(rel, src, ix)
	}
	for i, sc := range s.scopes {
		if s.scopeHits[i] == 0 {
			name := sc.file
			if name == "" {
				name = sc.dir
			}
			s.problemf(name, 0, "console-spawn scope entry is stale: no os/exec command construction was found under it")
		}
	}
	for i, e := range s.allowlist {
		switch n := s.allowHits[i]; {
		case n == 0:
			s.problemf(e.file, 0, "longLivedSpawnSites entry (%s, func %s) no longer matches any construction; update or remove it", e.command, e.function)
		case n > 1:
			s.problemf(e.file, 0, "longLivedSpawnSites entry (%s, func %s) matches %d constructions; the pin is ambiguous", e.command, e.function, n)
		}
	}
	return s.problems
}

func (s *consoleSpawnScan) scanFile(rel, src string, scopeIx int) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		s.problemf(rel, 0, "console-spawn scan could not parse: %v", err)
		return
	}

	// Resolve import aliases: the local name of every import (the alias
	// when there is one, the path's base otherwise), so any spelling of
	// os/exec and internal/execwin is recognized.
	imports := map[string]string{}
	for _, im := range file.Imports {
		importPath := strings.Trim(im.Path.Value, `"`)
		name := path.Base(importPath)
		if im.Name != nil {
			name = im.Name.Name
		}
		if name == "." || name == "_" {
			continue
		}
		imports[name] = importPath
	}
	var execPkg, execwinPkg string
	for name, importPath := range imports {
		switch importPath {
		case "os/exec":
			execPkg = name
		case "github.com/steveyegge/beads/internal/execwin":
			execwinPkg = name
		}
	}

	// The pinned helpers' own platform halves are the one place a scoped
	// file may name the console-creation flags; everywhere else they must
	// go through internal/execwin.
	pinnedHalf := false
	for _, e := range s.allowlist {
		if e.winHalf == rel {
			pinnedHalf = true
		}
	}
	if !pinnedHalf {
		for _, lit := range []string{"DETACHED_PROCESS", "CREATE_NEW_CONSOLE", "CREATE_NO_WINDOW"} {
			if strings.Contains(src, lit) {
				s.problemf(rel, 0, "mentions %s; console-creation flags must go through internal/execwin (execwin.Hide / execwin.BackgroundSysProcAttr)", lit)
			}
		}
	}

	if execPkg == "" {
		// No command construction is possible; the flag-literal checks
		// above still ran.
		return
	}

	// Collect the execwin.Hide(...) argument ranges: a construction counts
	// as wrapped when it sits directly inside one. Positions of every
	// expression used as a call's function distinguish a constructor
	// function value from a constructor call.
	var hideSpans []consoleSpawnHideSpan
	callFuns := map[token.Pos]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		callFuns[sel.Pos()] = true
		if execwinPkg == "" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == execwinPkg && sel.Sel.Name == "Hide" {
			hideSpans = append(hideSpans, consoleSpawnHideSpan{start: c.Lparen, end: c.Rparen})
		}
		return true
	})

	isCommandCall := func(sel *ast.SelectorExpr) bool {
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == execPkg && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext")
	}
	startProcessPkg := func(id *ast.Ident) (string, bool) {
		p, ok := imports[id.Name]
		if !ok {
			return "", false
		}
		return p, p == "os" || p == "syscall"
	}

	// Function contexts: every FuncDecl and func literal, each carrying
	// the top-level function enclosing it, so a construction inside a
	// closure is attributed to the named function for allowlist matching.
	var decls, lits, ctxs []*consoleSpawnFuncCtx
	ast.Inspect(file, func(n ast.Node) bool {
		switch fn := n.(type) {
		case *ast.FuncDecl:
			if fn.Body != nil {
				decls = append(decls, &consoleSpawnFuncCtx{name: fn.Name.Name, body: fn.Body})
			}
		case *ast.FuncLit:
			lits = append(lits, &consoleSpawnFuncCtx{body: fn.Body})
		}
		return true
	})
	for _, lit := range lits {
		for _, d := range decls {
			if d.body.Pos() <= lit.body.Pos() && lit.body.End() <= d.body.End() {
				lit.name = d.name
			}
		}
	}
	ctxs = append(ctxs, decls...)
	ctxs = append(ctxs, lits...)

	render := func(c *ast.CallExpr) string {
		var sb strings.Builder
		_ = printer.Fprint(&sb, fset, c)
		return sb.String()
	}

	for _, ctx := range ctxs {
		seen := map[token.Pos]bool{}
		ast.Inspect(ctx.body, func(n ast.Node) bool {
			switch st := n.(type) {
			case *ast.AssignStmt:
				for i, rhs := range st.Rhs {
					// A construction anywhere in the RHS (directly, or
					// nested inside execwin.Hide(...)) is attributed to
					// the variable it is assigned to, when there is one.
					var varName string
					if len(st.Lhs) == len(st.Rhs) {
						if id, ok := st.Lhs[i].(*ast.Ident); ok {
							varName = id.Name
						}
					}
					ast.Inspect(rhs, func(n ast.Node) bool {
						c, ok := n.(*ast.CallExpr)
						if !ok {
							return true
						}
						sel, ok := c.Fun.(*ast.SelectorExpr)
						if !ok || !isCommandCall(sel) || seen[c.Pos()] {
							return true
						}
						seen[c.Pos()] = true
						ctx.hits = append(ctx.hits, &consoleSpawnHit{
							call: c, render: render(c), funcName: ctx.name, varName: varName, line: fset.Position(c.Pos()).Line,
						})
						return true
					})
					if i < len(st.Lhs) && i < len(st.Rhs) {
						if sel, ok := st.Lhs[i].(*ast.SelectorExpr); ok && sel.Sel.Name == "SysProcAttr" {
							if id, ok := sel.X.(*ast.Ident); ok {
								assign := consoleSpawnAssign{pos: st.Pos(), varName: id.Name, line: fset.Position(st.Pos()).Line}
								if c, ok := st.Rhs[i].(*ast.CallExpr); ok {
									if fid, ok := c.Fun.(*ast.Ident); ok {
										assign.callName = fid.Name
									}
								}
								ctx.assigns = append(ctx.assigns, assign)
							}
						}
					}
				}
			case *ast.CallExpr:
				sel, ok := st.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch {
				case isCommandCall(sel) && !seen[st.Pos()]:
					seen[st.Pos()] = true
					ctx.hits = append(ctx.hits, &consoleSpawnHit{
						call: st, render: render(st), funcName: ctx.name, line: fset.Position(st.Pos()).Line,
					})
				case sel.Sel.Name == "StartProcess":
					if id, ok := sel.X.(*ast.Ident); ok {
						if _, ok := startProcessPkg(id); ok {
							s.problemf(rel, fset.Position(st.Pos()).Line, "direct os/syscall StartProcess in a console-spawn scope; build the command with os/exec and wrap it in execwin.Hide")
						}
					}
				case sel.Sel.Name == "CreateProcess":
					if id, ok := sel.X.(*ast.Ident); ok {
						if p, ok := imports[id.Name]; ok && p == "golang.org/x/sys/windows" {
							s.problemf(rel, fset.Position(st.Pos()).Line, "direct windows CreateProcess in a console-spawn scope; build the command with os/exec and wrap it in execwin.Hide")
						}
					}
				case sel.Sel.Name == "Start":
					if id, ok := sel.X.(*ast.Ident); ok {
						ctx.starts = append(ctx.starts, consoleSpawnStart{pos: st.Pos(), varName: id.Name})
					}
				}
			case *ast.SelectorExpr:
				if isCommandCall(st) && !callFuns[st.Pos()] {
					s.problemf(rel, fset.Position(st.Pos()).Line, "exec.Command used as a value in a console-spawn scope; construct and wrap the command where it runs (execwin.Hide(exec.Command(...)))")
				}
			case *ast.CompositeLit:
				if sel, ok := st.Type.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && sel.Sel.Name == "Cmd" && id.Name == execPkg {
						s.problemf(rel, fset.Position(st.Pos()).Line, "exec.Cmd composite literal in a console-spawn scope; use exec.Command and wrap it in execwin.Hide")
					}
				}
			}
			return true
		})

		for _, hit := range ctx.hits {
			if s.checkHit(rel, ctx, hit, hideSpans) {
				s.scopeHits[scopeIx]++
			}
		}
	}
}

// checkHit classifies one construction as wrapped, pinned, or a problem.
// It reports whether the construction is recognized.
func (s *consoleSpawnScan) checkHit(rel string, ctx *consoleSpawnFuncCtx, hit *consoleSpawnHit, hideSpans []consoleSpawnHideSpan) bool {
	for _, sp := range hideSpans {
		// CallExpr.End() is Rparen+1, so a construction nested directly in
		// Hide(...) ends exactly at the Hide call's closing paren: compare
		// the closing parens, not End().
		if hit.call.Pos() > sp.start && hit.call.Rparen < sp.end {
			// Wrapped. A later attribute assignment would clobber the
			// hidden-console flag execwin.Hide just set.
			if hit.varName != "" {
				for _, a := range ctx.assigns {
					if a.varName == hit.varName && a.pos > hit.call.Pos() {
						s.problemf(rel, hit.line, "%s's SysProcAttr is assigned again after execwin.Hide (line %d); that overwrites the hidden-console flag", hit.varName, a.line)
					}
				}
			}
			return true
		}
	}
	for i, e := range s.allowlist {
		if e.file != rel || e.function != hit.funcName || e.command != hit.render || e.varName != hit.varName {
			continue
		}
		s.allowHits[i]++
		s.checkPinned(rel, ctx, hit, i)
		return true
	}
	s.problemf(rel, hit.line, "unwrapped os/exec construction (%s); wrap it in execwin.Hide, or pin it in longLivedSpawnSites with a reason", hit.render)
	return false
}

// checkPinned verifies a long-lived construction receives its designated
// helper after construction and before Start.
func (s *consoleSpawnScan) checkPinned(rel string, ctx *consoleSpawnFuncCtx, hit *consoleSpawnHit, ix int) {
	e := s.allowlist[ix]
	var helperPos token.Pos
	var haveHelper bool
	for _, a := range ctx.assigns {
		if a.varName != e.varName || a.pos <= hit.call.Pos() {
			continue
		}
		if a.callName == e.helper {
			if !haveHelper || a.pos < helperPos {
				helperPos = a.pos
			}
			haveHelper = true
			continue
		}
		if a.callName != "" {
			s.problemf(rel, hit.line, "long-lived spawn (%s): %s's SysProcAttr comes from %s; it must use %s", e.command, e.varName, a.callName, e.helper)
		}
	}
	if !haveHelper {
		s.problemf(rel, hit.line, "long-lived spawn (%s) never assigns %s.SysProcAttr = %s() after construction", e.command, e.varName, e.helper)
		return
	}
	for _, st := range ctx.starts {
		if st.varName == e.varName && st.pos < helperPos {
			s.problemf(rel, hit.line, "long-lived spawn (%s) is started before %s.SysProcAttr = %s() is assigned", e.command, e.varName, e.helper)
		}
	}
}

// consoleSpawnSource builds the fixture source of one demo package file:
// imports plus the given declarations.
func consoleSpawnSource(imports, decls string) string {
	return "package demo\n\nimport (\n" + imports + ")\n\n" + decls
}

const (
	consoleSpawnStdImports = "\t\"os/exec\""
	// syscall is imported by the clobber fixtures' attribute assignments;
	// parse-only, so its absence from the import list is fine.
	consoleSpawnWinImports = "\t\"os/exec\"\n\tew \"github.com/steveyegge/beads/internal/execwin\""
)

// consoleSpawnScanFixtures runs the scan over one fixture file with the
// standard demo scope and returns its problems.
func consoleSpawnScanFixtures(t *testing.T, src string, allowlist []longLivedSpawnSite) []consoleSpawnProblem {
	t.Helper()
	scan := newConsoleSpawnScan(
		[]consoleSpawnScope{{dir: "demo"}},
		allowlist,
		map[string]string{"demo/file.go": src},
	)
	return scan.run()
}

func TestConsoleSpawnPolicyScan(t *testing.T) {
	pinned := longLivedSpawnSite{
		file:     "demo/file.go",
		function: "f",
		varName:  "cmd",
		command:  `exec.Command("dolt", "serve")`,
		helper:   "bgAttr",
		winHalf:  "demo/none_windows.go",
	}
	pinnedDecls := `func bgAttr() *syscall.SysProcAttr { return nil }

func f() {
	cmd := exec.Command("dolt", "serve")
	cmd.SysProcAttr = bgAttr()
	_ = cmd
}`
	for _, tc := range []struct {
		name      string
		src       string
		allowlist []longLivedSpawnSite
		want      string // substring of the expected problem, "" for none
	}{
		{
			name: "wrapped construction under aliased imports passes",
			src: consoleSpawnSource(consoleSpawnWinImports, `func f() {
	cmd := ew.Hide(exec.Command("git", "status"))
	_ = cmd
}`),
			want: "",
		},
		{
			name: "unwrapped construction fails",
			src: consoleSpawnSource(consoleSpawnStdImports, `func f() {
	cmd := exec.Command("git", "status")
	_ = cmd
}`),
			want: "unwrapped os/exec construction",
		},
		{
			name: "wrapped construction with a later attribute clobber fails",
			src: consoleSpawnSource(consoleSpawnWinImports, `func f() {
	cmd := ew.Hide(exec.Command("git", "status"))
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	_ = cmd
}`),
			want: "assigned again after execwin.Hide",
		},
		{
			name: "a later attribute assignment to another variable passes",
			src: consoleSpawnSource(consoleSpawnWinImports, `func f() {
	var other exec.Cmd
	cmd := ew.Hide(exec.Command("git", "status"))
	other.SysProcAttr = &syscall.SysProcAttr{}
	_, _ = cmd, other
}`),
			want: "",
		},
		{
			name: "two commands with only one wrapped fails for the other",
			src: consoleSpawnSource(consoleSpawnWinImports, `func f() {
	cmd := ew.Hide(exec.Command("git", "status"))
	plain := exec.Command("git", "log")
	_, _ = cmd, plain
}`),
			want: `unwrapped os/exec construction (exec.Command("git", "log"))`,
		},
		{
			name:      "pinned construction with its designated helper passes",
			src:       consoleSpawnSource(consoleSpawnStdImports, pinnedDecls),
			allowlist: []longLivedSpawnSite{pinned},
			want:      "",
		},
		{
			name: "pinned construction with the wrong helper fails",
			src: consoleSpawnSource(consoleSpawnStdImports, `func bgAttr() *syscall.SysProcAttr { return nil }

func otherAttr() *syscall.SysProcAttr { return nil }

func f() {
	cmd := exec.Command("dolt", "serve")
	cmd.SysProcAttr = otherAttr()
	_ = cmd
}`),
			allowlist: []longLivedSpawnSite{pinned},
			want:      "it must use bgAttr",
		},
		{
			name: "pinned construction with no attribute assignment fails",
			src: consoleSpawnSource(consoleSpawnStdImports, `func f() {
	cmd := exec.Command("dolt", "serve")
	_ = cmd
}`),
			allowlist: []longLivedSpawnSite{pinned},
			want:      "never assigns cmd.SysProcAttr = bgAttr()",
		},
		{
			name: "an attribute assignment to another variable exempts nothing",
			src: consoleSpawnSource(consoleSpawnStdImports, `func bgAttr() *syscall.SysProcAttr { return nil }

func f() {
	cmd := exec.Command("dolt", "serve")
	other := exec.Command("dolt", "other")
	other.SysProcAttr = bgAttr()
	_, _ = cmd, other
}`),
			allowlist: []longLivedSpawnSite{pinned},
			want:      "never assigns cmd.SysProcAttr = bgAttr()",
		},
		{
			name: "pinned construction started before its helper fails",
			src: consoleSpawnSource(consoleSpawnStdImports, `func bgAttr() *syscall.SysProcAttr { return nil }

func f() {
	cmd := exec.Command("dolt", "serve")
	_ = cmd.Start()
	cmd.SysProcAttr = bgAttr()
	_ = cmd.Wait()
}`),
			allowlist: []longLivedSpawnSite{pinned},
			want:      "started before cmd.SysProcAttr = bgAttr() is assigned",
		},
		{
			name: "constructor function value fails",
			src: consoleSpawnSource(consoleSpawnStdImports, `func f() {
	build := exec.Command
	_ = build("git", "status")
}`),
			want: "used as a value",
		},
		{
			name: "exec.Cmd composite literal fails",
			src: consoleSpawnSource(consoleSpawnStdImports, `func f() {
	cmd := &exec.Cmd{}
	_ = cmd
}`),
			want: "composite literal",
		},
		{
			name: "direct os.StartProcess fails",
			src: consoleSpawnSource("\t\"os\"\n\t\"os/exec\"", `func f() {
	_, _, _ = os.StartProcess("git", nil, &os.ProcAttr{})
}`),
			want: "direct os/syscall StartProcess",
		},
		{
			name: "a console-creation flag literal fails",
			src: consoleSpawnSource(consoleSpawnStdImports, `func f() {
	// DETACHED_PROCESS would leave the child console-less.
	_ = exec.Command("git", "status")
}`),
			want: "mentions DETACHED_PROCESS",
		},
		{
			name: "the process-group flag is not forbidden",
			src: consoleSpawnSource(consoleSpawnWinImports, `func f() {
	// CREATE_NEW_PROCESS_GROUP is the analog of Setpgid.
	cmd := ew.Hide(exec.Command("git", "status"))
	_ = cmd
}`),
			want: "",
		},
		{
			name: "a parse error fails",
			src:  "package demo\n\nfunc {",
			want: "could not parse",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := consoleSpawnScanFixtures(t, tc.src, tc.allowlist)
			if tc.want == "" {
				for _, p := range problems {
					t.Errorf("unexpected problem %s:%d: %s", p.file, p.line, p.msg)
				}
				return
			}
			for _, p := range problems {
				if strings.Contains(p.msg, tc.want) {
					return
				}
			}
			t.Errorf("scan found no problem containing %q; problems: %v", tc.want, problems)
		})
	}

	// Stale and ambiguous allowlist entries fail on their own.
	scan := newConsoleSpawnScan(
		[]consoleSpawnScope{{dir: "demo"}},
		[]longLivedSpawnSite{pinned},
		map[string]string{"demo/file.go": consoleSpawnSource(consoleSpawnStdImports, `func g() {
	_ = exec.Command("git", "status")
}`)},
	)
	wantStale := false
	for _, p := range scan.run() {
		if strings.Contains(p.msg, "no longer matches") {
			wantStale = true
		}
	}
	if !wantStale {
		t.Error("a stale allowlist entry (no matching construction) was not reported")
	}

	scan = newConsoleSpawnScan(
		[]consoleSpawnScope{{dir: "demo"}},
		[]longLivedSpawnSite{pinned},
		map[string]string{"demo/file.go": consoleSpawnSource(consoleSpawnStdImports, `func bgAttr() *syscall.SysProcAttr { return nil }

func f() {
	cmd := exec.Command("dolt", "serve")
	cmd.SysProcAttr = bgAttr()
	cmd = exec.Command("dolt", "serve")
	cmd.SysProcAttr = bgAttr()
	_ = cmd
}`)},
	)
	wantAmbiguous := false
	for _, p := range scan.run() {
		if strings.Contains(p.msg, "matches 2") {
			wantAmbiguous = true
		}
	}
	if !wantAmbiguous {
		t.Error("an allowlist entry matching two constructions was not reported as ambiguous")
	}
}

func TestConsoleSpawnScope(t *testing.T) {
	unwrapped := consoleSpawnSource(consoleSpawnStdImports, `func f() {
	_ = exec.Command("git", "status")
}`)
	wrapped := consoleSpawnSource(consoleSpawnWinImports, `func f() {
	_ = ew.Hide(exec.Command("git", "status"))
}`)
	for _, tc := range []struct {
		name   string
		scopes []consoleSpawnScope
		files  map[string]string
		want   string // "" for no problems
	}{
		{
			name:   "directory scope includes descendants",
			scopes: []consoleSpawnScope{{dir: "demo/pkg"}},
			files:  map[string]string{"demo/pkg/sub/inner.go": unwrapped},
			want:   "unwrapped os/exec construction",
		},
		{
			name:   "file scope does not leak to siblings",
			scopes: []consoleSpawnScope{{file: "demo/cmd.go"}},
			files:  map[string]string{"demo/cmd.go": wrapped, "demo/other.go": unwrapped},
			want:   "",
		},
		{
			name:   "test files are not scanned",
			scopes: []consoleSpawnScope{{dir: "demo"}},
			files:  map[string]string{"demo/ok.go": wrapped, "demo/a_test.go": unwrapped},
			want:   "",
		},
		{
			name:   "testdata is not scanned",
			scopes: []consoleSpawnScope{{dir: "demo"}},
			files:  map[string]string{"demo/ok.go": wrapped, "demo/testdata/fixture.go": unwrapped},
			want:   "",
		},
		{
			name:   "Windows-only files are scanned on every platform",
			scopes: []consoleSpawnScope{{dir: "demo"}},
			files:  map[string]string{"demo/spawn_windows.go": unwrapped},
			want:   "unwrapped os/exec construction",
		},
		{
			name:   "a scope entry with a recognized construction passes",
			scopes: []consoleSpawnScope{{dir: "demo"}},
			files:  map[string]string{"demo/file.go": wrapped},
			want:   "",
		},
		{
			name:   "a stale scope entry fails",
			scopes: []consoleSpawnScope{{dir: "demo"}, {file: "demo/none.go"}},
			files:  map[string]string{"demo/file.go": wrapped},
			want:   "scope entry is stale",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scan := newConsoleSpawnScan(tc.scopes, nil, tc.files)
			problems := scan.run()
			if tc.want == "" {
				for _, p := range problems {
					t.Errorf("unexpected problem %s:%d: %s", p.file, p.line, p.msg)
				}
				return
			}
			for _, p := range problems {
				if strings.Contains(p.msg, tc.want) {
					return
				}
			}
			t.Errorf("scan found no problem containing %q; problems: %v", tc.want, problems)
		})
	}
}

// consoleSpawnFuncBody returns the source of the function named signature,
// from its declaration through its closing brace.
func consoleSpawnFuncBody(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	if start < 0 {
		t.Fatalf("%s not found in:\n%s", signature, src)
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end >= 0 {
		body = body[:end]
	}
	return body
}

func TestConsoleSpawnAllowlistMatching(t *testing.T) {
	root := bazelPolicyRoot(t)
	for _, e := range longLivedSpawnSites {
		t.Run(e.file, func(t *testing.T) {
			src := readPolicyFile(t, root, filepath.FromSlash(e.file))

			// The entry matches exactly one construction in the real
			// file, and that construction carries its designated helper.
			scan := newConsoleSpawnScan([]consoleSpawnScope{{file: e.file}}, []longLivedSpawnSite{e}, map[string]string{e.file: src})
			problems := scan.run()
			if len(problems) != 0 || scan.allowHits[0] != 1 {
				t.Errorf("entry did not match exactly one pinned construction (allowHits=%d): %v", scan.allowHits[0], problems)
			}

			// A renamed entry goes stale, so the pin fails when it rots.
			wrong := e
			wrong.function = "doesNotExist"
			scan = newConsoleSpawnScan([]consoleSpawnScope{{file: e.file}}, []longLivedSpawnSite{wrong}, map[string]string{e.file: src})
			stale := false
			for _, p := range scan.run() {
				if strings.Contains(p.msg, "no longer matches") {
					stale = true
				}
			}
			if !stale {
				t.Error("a renamed allowlist entry was not reported stale")
			}

			// The Windows half delegates to BackgroundSysProcAttr and
			// never names DETACHED_PROCESS.
			winSrc := readPolicyFile(t, root, filepath.FromSlash(e.winHalf))
			winBody := consoleSpawnFuncBody(t, winSrc, "func "+e.helper+"()")
			if !strings.Contains(winBody, "BackgroundSysProcAttr") {
				t.Errorf("%s: %s does not delegate to execwin.BackgroundSysProcAttr:\n%s", e.winHalf, e.helper, winBody)
			}
			if strings.Contains(winBody, "DETACHED_PROCESS") {
				t.Errorf("%s: %s names DETACHED_PROCESS; it must leave the child a hidden console:\n%s", e.winHalf, e.helper, winBody)
			}

			// The non-Windows halves keep what they already return.
			for _, half := range e.otherHalves {
				halfSrc := readPolicyFile(t, root, filepath.FromSlash(half.file))
				halfBody := consoleSpawnFuncBody(t, halfSrc, "func "+e.helper+"()")
				if !strings.Contains(halfBody, half.require) {
					t.Errorf("%s: %s no longer keeps %s:\n%s", half.file, e.helper, half.require, halfBody)
				}
			}
		})
	}
}

// TestNoUnsuppressedConsoleSpawns runs the guard over the real repository.
func TestNoUnsuppressedConsoleSpawns(t *testing.T) {
	root := bazelPolicyRoot(t)
	files := map[string]string{}
	for _, rel := range repoFiles(t, root, 1000) {
		if !strings.HasSuffix(rel, ".go") {
			continue
		}
		if _, ok := consoleSpawnScopeOf(consoleSpawnScopes, rel); !ok {
			continue
		}
		files[rel] = readPolicyFile(t, root, filepath.FromSlash(rel))
	}
	for _, e := range longLivedSpawnSites {
		if _, ok := files[e.file]; !ok {
			t.Fatalf("longLivedSpawnSites entry %s: not found in the repository listing", e.file)
		}
	}
	scan := newConsoleSpawnScan(consoleSpawnScopes, longLivedSpawnSites, files)
	for _, p := range scan.run() {
		if p.line > 0 {
			t.Errorf("%s:%d: %s", p.file, p.line, p.msg)
		} else {
			t.Errorf("%s: %s", p.file, p.msg)
		}
	}
}
