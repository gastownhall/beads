package scripts_test

// Policy: a command bd starts from a context that may have no console must not
// make Windows allocate a visible console window for it.
//
// Background. On Windows a console-subsystem child started by a process that
// has NO console of its own does not inherit one: the system allocates a brand
// new console for it. When Windows Terminal is registered as the default
// terminal application (defterm), that console is handed to Windows Terminal,
// which opens a real window that takes focus. For a short-lived child it is
// worse than cosmetic: the child can exit before Windows Terminal finishes
// attaching, so the handoff pipe is already closed and the launch reports
//
//	[error 2147942632 (0x800700E8) when launching `"...\dolt.exe" init']
//
// where 0x800700E8 is HRESULT_FROM_WIN32(ERROR_NO_DATA), "The pipe is being
// closed." (gastownhall/beads#6076, #6077.)
//
// Long-lived children. bd db-proxy-child, the proxy's managed dolt sql-server
// and the shared dolt sql-server outlive the command that starts them, so they
// get a hidden console of their own unconditionally:
// execwin.BackgroundSysProcAttr, CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP.
// Not DETACHED_PROCESS: that leaves the child with no console at all, and
// every console program it starts in turn (dolt runs `uname` on every start)
// then gets a fresh visible one. consoleSpawnPins names those three
// constructions exactly: file, function and command variable, the helper
// assigned to that command's SysProcAttr before it is started or handed on,
// and the Windows half of the helper, which must delegate to
// execwin.BackgroundSysProcAttr.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	osExecImportPath  = "os/exec"
	execwinImportPath = "github.com/steveyegge/beads/internal/execwin"
)

// consoleSpawnPin is one long-lived construction that sets its own console
// flags instead of going through execwin.Hide.
type consoleSpawnPin struct {
	file       string // repo-relative file holding the construction
	fn         string // enclosing function or method name
	cmdVar     string // the variable the constructed *exec.Cmd is assigned to
	helper     string // the package function assigned to cmdVar.SysProcAttr
	helperFile string // repo-relative file holding the helper's Windows half
}

// consoleSpawnPins are the only constructions in scope allowed to assign
// SysProcAttr: the three long-lived database children.
var consoleSpawnPins = []consoleSpawnPin{
	{
		file:       "internal/doltserver/doltserver.go",
		fn:         "startLocked",
		cmdVar:     "cmd",
		helper:     "procAttrDetached",
		helperFile: "internal/doltserver/doltserver_windows.go",
	},
	{
		file:       "internal/storage/dbproxy/proxy/endpoint.go",
		fn:         "forkExecChild",
		cmdVar:     "cmd",
		helper:     "procAttrDetached",
		helperFile: "internal/storage/dbproxy/proxy/endpoint_windows.go",
	},
	{
		file:       "internal/storage/dbproxy/server/doltserver.go",
		fn:         "startAttempt",
		cmdVar:     "cmd",
		helper:     "managedServerSysProcAttr",
		helperFile: "internal/storage/dbproxy/server/procattr_windows.go",
	},
}

// importName returns the name file refers to importPath by: its alias, or the
// last path element when it has none. "" means file does not import it, or
// imports it only for side effects. A dot import returns ".".
func importName(file *ast.File, importPath string) string {
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != importPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" {
				return ""
			}
			return imp.Name.Name
		}
		return p[strings.LastIndex(p, "/")+1:]
	}
	return ""
}

// isPkgCall reports whether call is pkg.<one of names>(...) for a package
// imported as pkg in the file.
func isPkgCall(call *ast.CallExpr, pkg string, names ...string) bool {
	if pkg == "" {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != pkg {
		return false
	}
	for _, n := range names {
		if sel.Sel.Name == n {
			return true
		}
	}
	return false
}

// isExecConstruction reports whether call is os/exec's Command or
// CommandContext under the file's name for os/exec.
func isExecConstruction(call *ast.CallExpr, execName string) bool {
	return isPkgCall(call, execName, "Command", "CommandContext")
}

// sysProcAttrTarget returns the variable whose SysProcAttr (or a field of it)
// lhs assigns, and whether lhs is such an assignment at all. The variable is
// "" when the receiver is not a plain identifier.
func sysProcAttrTarget(lhs ast.Expr) (string, bool) {
	for {
		sel, ok := lhs.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		if sel.Sel.Name == "SysProcAttr" {
			if id, ok := sel.X.(*ast.Ident); ok {
				return id.Name, true
			}
			return "", true
		}
		lhs = sel.X
	}
}

// findFuncDecl returns the top-level function or method named name.
func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Name.Name == name && fd.Body != nil {
			return fd
		}
	}
	return nil
}

// pinCheck is the outcome of checking one pin against its construction file.
type pinCheck struct {
	construction token.Pos // the exec.Command call pinned, if found
	problems     []string
}

// checkConsoleSpawnPin verifies pin against the parsed construction file: the
// function holds exactly one construction assigned to pin.cmdVar; exactly one
// assignment to that command's SysProcAttr, of pin.helper(), after the
// construction; no other write to its SysProcAttr; and the assignment comes
// before the command is first started or handed to another function.
func checkConsoleSpawnPin(fset *token.FileSet, file *ast.File, pin consoleSpawnPin) pinCheck {
	var res pinCheck
	fail := func(pos token.Pos, format string, args ...any) {
		where := pin.file
		if pos.IsValid() {
			where = pin.file + ":" + strconv.Itoa(fset.Position(pos).Line)
		}
		res.problems = append(res.problems, where+": "+pin.fn+": "+fmt.Sprintf(format, args...))
	}
	fd := findFuncDecl(file, pin.fn)
	if fd == nil {
		fail(token.NoPos, "function not found; update consoleSpawnPins")
		return res
	}
	execName := importName(file, osExecImportPath)

	var constructions []*ast.CallExpr
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name != pin.cmdVar {
			return true
		}
		if call, ok := ast.Unparen(as.Rhs[0]).(*ast.CallExpr); ok && isExecConstruction(call, execName) {
			constructions = append(constructions, call)
		}
		return true
	})
	switch len(constructions) {
	case 0:
		fail(fd.Pos(), "no %s := exec.Command(...) construction; update consoleSpawnPins", pin.cmdVar)
		return res
	case 1:
	default:
		fail(constructions[1].Pos(), "%d constructions assigned to %s; a pin names exactly one", len(constructions), pin.cmdVar)
		return res
	}
	construction := constructions[0]
	res.construction = construction.Pos()

	var pinAssign *ast.AssignStmt
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			v, ok := sysProcAttrTarget(lhs)
			if !ok || v != pin.cmdVar {
				continue
			}
			isWhole := false
			if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "SysProcAttr" {
				isWhole = true
			}
			var rhs ast.Expr
			if len(as.Rhs) == len(as.Lhs) {
				rhs = as.Rhs[i]
			}
			call, isCall := ast.Unparen(rhs).(*ast.CallExpr)
			helperCall := false
			if isCall && len(call.Args) == 0 {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == pin.helper {
					helperCall = true
				}
			}
			switch {
			case !isWhole:
				fail(as.Pos(), "%s.SysProcAttr is modified after it is pinned; the pin must be the only write", pin.cmdVar)
			case as.Pos() < construction.Pos():
				fail(as.Pos(), "%s.SysProcAttr is assigned before the pinned construction", pin.cmdVar)
			case !helperCall:
				fail(as.Pos(), "%s.SysProcAttr is assigned something other than %s()", pin.cmdVar, pin.helper)
			case pinAssign != nil:
				fail(as.Pos(), "%s.SysProcAttr is assigned again after the pin (a later clobber)", pin.cmdVar)
			default:
				pinAssign = as
			}
		}
		return true
	})
	if pinAssign == nil {
		if len(res.problems) == 0 {
			fail(construction.Pos(), "%s.SysProcAttr is never assigned %s(): the child would start without its console flags", pin.cmdVar, pin.helper)
		}
		return res
	}

	// The first use that can start the command: a method call on it (Start,
	// Run, Output, ...) or passing it to another function (launchServer).
	firstUse := token.NoPos
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Pos() < construction.End() {
			return true
		}
		uses := false
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == pin.cmdVar {
				uses = true
			}
		}
		for _, arg := range call.Args {
			if id, ok := ast.Unparen(arg).(*ast.Ident); ok && id.Name == pin.cmdVar {
				uses = true
			}
		}
		if uses && (!firstUse.IsValid() || call.Pos() < firstUse) {
			firstUse = call.Pos()
		}
		return true
	})
	switch {
	case !firstUse.IsValid():
		fail(construction.Pos(), "%s is never started or handed on; update consoleSpawnPins", pin.cmdVar)
	case pinAssign.Pos() > firstUse:
		fail(pinAssign.Pos(), "%s.SysProcAttr is assigned only after %s is first started or handed on (line %d)",
			pin.cmdVar, pin.cmdVar, fset.Position(firstUse).Line)
	}
	return res
}

// checkBackgroundHelper verifies that helperFile's pin.helper is exactly
// `return execwin.BackgroundSysProcAttr()`, so the Windows flags of every
// long-lived child come from one place and cannot drift back to
// DETACHED_PROCESS or to CREATE_NEW_PROCESS_GROUP alone.
func checkBackgroundHelper(fset *token.FileSet, file *ast.File, pin consoleSpawnPin) []string {
	fd := findFuncDecl(file, pin.helper)
	if fd == nil {
		return []string{pin.helperFile + ": " + pin.helper + " not found"}
	}
	where := pin.helperFile + ":" + strconv.Itoa(fset.Position(fd.Pos()).Line) + ": " + pin.helper
	if len(fd.Body.List) != 1 {
		return []string{where + ": body must be exactly `return execwin.BackgroundSysProcAttr()`"}
	}
	ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return []string{where + ": body must be exactly `return execwin.BackgroundSysProcAttr()`"}
	}
	call, ok := ast.Unparen(ret.Results[0]).(*ast.CallExpr)
	if !ok || len(call.Args) != 0 || !isPkgCall(call, importName(file, execwinImportPath), "BackgroundSysProcAttr") {
		return []string{where + ": does not return execwin.BackgroundSysProcAttr(), so the child's Windows console flags are not CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP"}
	}
	return nil
}

// parseRepoGoFile parses a repo-relative Go source file under root.
func parseRepoGoFile(t *testing.T, root, rel string) (*token.FileSet, *ast.File, []byte) {
	t.Helper()
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
	return fset, f, data
}

// TestConsoleSpawnLongLivedPins checks the three long-lived constructions in
// the real tree: each is pinned to its helper before it starts, and each
// helper's Windows half delegates to execwin.BackgroundSysProcAttr.
func TestConsoleSpawnLongLivedPins(t *testing.T) {
	root := sourceRepoRoot(t)
	for _, pin := range consoleSpawnPins {
		fset, f, _ := parseRepoGoFile(t, root, pin.file)
		for _, p := range checkConsoleSpawnPin(fset, f, pin).problems {
			t.Error(p)
		}
		hfset, hf, _ := parseRepoGoFile(t, root, pin.helperFile)
		for _, p := range checkBackgroundHelper(hfset, hf, pin) {
			t.Error(p)
		}
	}
}

// TestConsoleSpawnPinScan pins the pin checks' accept/reject decisions on
// in-memory fixtures.
func TestConsoleSpawnPinScan(t *testing.T) {
	pin := consoleSpawnPin{file: "fixture.go", fn: "start", cmdVar: "cmd", helper: "procAttrDetached", helperFile: "fixture_windows.go"}
	header := "package p\n\nimport (\n\t\"os/exec\"\n\t\"syscall\"\n)\n\nvar _ syscall.SysProcAttr\n\n"
	cases := []struct {
		name    string
		body    string
		wantErr string // "" = accepted
	}{
		{
			name: "pinned before Start is accepted",
			body: "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n\t_ = cmd.Start()\n}\n",
		},
		{
			name: "pinned before being handed to a launcher is accepted",
			body: "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.Dir = \"x\"\n\tcmd.SysProcAttr = procAttrDetached()\n\tlaunch(cmd)\n}\n",
		},
		{
			name:    "never pinned",
			body:    "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\t_ = cmd.Start()\n}\n",
			wantErr: "never assigned procAttrDetached()",
		},
		{
			name:    "wrong helper",
			body:    "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = &syscall.SysProcAttr{}\n\t_ = cmd.Start()\n}\n",
			wantErr: "something other than procAttrDetached()",
		},
		{
			name:    "late replacement after the pin",
			body:    "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n\tcmd.SysProcAttr = other()\n\t_ = cmd.Start()\n}\n",
			wantErr: "something other than procAttrDetached()",
		},
		{
			name:    "pinned twice",
			body:    "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n\tcmd.SysProcAttr = procAttrDetached()\n\t_ = cmd.Start()\n}\n",
			wantErr: "assigned again after the pin",
		},
		{
			name:    "flags modified after the pin",
			body:    "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n\tcmd.SysProcAttr.CreationFlags |= 8\n\t_ = cmd.Start()\n}\n",
			wantErr: "modified after it is pinned",
		},
		{
			name:    "pinned only after Start",
			body:    "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\t_ = cmd.Start()\n\tcmd.SysProcAttr = procAttrDetached()\n}\n",
			wantErr: "only after cmd is first started",
		},
		{
			name: "only one of two commands is pinned: the other cannot share the pin",
			body: "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n\tcmd = exec.Command(\"dolt\", \"sql-server\")\n\t_ = cmd.Start()\n}\n",
			wantErr: "2 constructions assigned to cmd",
		},
		{
			name:    "an attribute set on a different command does not pin this one",
			body:    "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tother := exec.Command(\"dolt\", \"version\")\n\tother.SysProcAttr = procAttrDetached()\n\t_ = cmd.Start()\n}\n",
			wantErr: "never assigned procAttrDetached()",
		},
		{
			name:    "construction missing",
			body:    "func start() {\n\t_ = exec.Command(\"dolt\", \"sql-server\").Start()\n}\n",
			wantErr: "no cmd := exec.Command",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := header + tc.body
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "fixture.go", src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			problems := checkConsoleSpawnPin(fset, f, pin).problems
			if tc.wantErr == "" {
				if len(problems) != 0 {
					t.Fatalf("problems = %q, want none", problems)
				}
				return
			}
			if !strings.Contains(strings.Join(problems, "\n"), tc.wantErr) {
				t.Fatalf("problems = %q, want one containing %q", problems, tc.wantErr)
			}
		})
	}

	helpers := []struct {
		name    string
		src     string
		wantErr bool
	}{
		{
			name: "delegates to execwin.BackgroundSysProcAttr",
			src:  "package p\n\nimport (\n\t\"syscall\"\n\n\t\"github.com/steveyegge/beads/internal/execwin\"\n)\n\nfunc procAttrDetached() *syscall.SysProcAttr {\n\t// CREATE_NO_WINDOW|CREATE_NEW_PROCESS_GROUP\n\treturn execwin.BackgroundSysProcAttr()\n}\n",
		},
		{
			name: "aliased execwin import is resolved",
			src:  "package p\n\nimport (\n\t\"syscall\"\n\n\tew \"github.com/steveyegge/beads/internal/execwin\"\n)\n\nfunc procAttrDetached() *syscall.SysProcAttr {\n\treturn ew.BackgroundSysProcAttr()\n}\n",
		},
		{
			name:    "DETACHED_PROCESS literal",
			src:     "package p\n\nimport (\n\t\"syscall\"\n\n\t\"golang.org/x/sys/windows\"\n)\n\nfunc procAttrDetached() *syscall.SysProcAttr {\n\treturn &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}\n}\n",
			wantErr: true,
		},
		{
			name:    "a same-named function from another package",
			src:     "package p\n\nimport (\n\t\"syscall\"\n\n\texecwin \"example.com/other\"\n)\n\nfunc procAttrDetached() *syscall.SysProcAttr {\n\treturn execwin.BackgroundSysProcAttr()\n}\n",
			wantErr: true,
		},
		{
			name:    "delegates, then modifies",
			src:     "package p\n\nimport (\n\t\"syscall\"\n\n\t\"github.com/steveyegge/beads/internal/execwin\"\n)\n\nfunc procAttrDetached() *syscall.SysProcAttr {\n\ta := execwin.BackgroundSysProcAttr()\n\ta.CreationFlags |= 8\n\treturn a\n}\n",
			wantErr: true,
		},
	}
	for _, tc := range helpers {
		t.Run("helper/"+tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "fixture_windows.go", tc.src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			problems := checkBackgroundHelper(fset, f, pin)
			if (len(problems) != 0) != tc.wantErr {
				t.Fatalf("problems = %q, wantErr %v", problems, tc.wantErr)
			}
		})
	}
}
