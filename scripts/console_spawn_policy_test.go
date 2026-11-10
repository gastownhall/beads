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
	// otherHalves are the helper's non-Windows halves, each pinned to keep
	// the attributes it returns today, so the Windows change cannot drift
	// into the Unix process lifecycle.
	otherHalves []consoleSpawnOtherHalf
}

// consoleSpawnOtherHalf is one non-Windows half of a pin's helper: the
// SysProcAttr fields it must still set, or, with no fields, that its body is
// exactly `return nil`.
type consoleSpawnOtherHalf struct {
	file   string
	fields []string
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
		otherHalves: []consoleSpawnOtherHalf{
			{file: "internal/doltserver/procattr_linux.go", fields: []string{"Setpgid", "Pdeathsig"}},
			{file: "internal/doltserver/procattr_other_unix.go", fields: []string{"Setpgid"}},
		},
	},
	{
		file:       "internal/storage/dbproxy/proxy/endpoint.go",
		fn:         "forkExecChild",
		cmdVar:     "cmd",
		helper:     "procAttrDetached",
		helperFile: "internal/storage/dbproxy/proxy/endpoint_windows.go",
		otherHalves: []consoleSpawnOtherHalf{
			{file: "internal/storage/dbproxy/proxy/endpoint_unix.go", fields: []string{"Setsid"}},
		},
	},
	{
		file:       "internal/storage/dbproxy/server/doltserver.go",
		fn:         "startAttempt",
		cmdVar:     "cmd",
		helper:     "managedServerSysProcAttr",
		helperFile: "internal/storage/dbproxy/server/procattr_windows.go",
		otherHalves: []consoleSpawnOtherHalf{
			{file: "internal/storage/dbproxy/server/procattr_other.go"},
		},
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
	assign       token.Pos // the cmd.SysProcAttr = helper() statement, if found
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
	res.assign = pinAssign.Pos()

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

// checkOtherHalf verifies that a non-Windows half of pin.helper still sets
// each of half.fields, as a key of a SysProcAttr literal or an assignment to
// that field, or, with no fields, that its body is exactly `return nil`.
func checkOtherHalf(fset *token.FileSet, file *ast.File, pin consoleSpawnPin, half consoleSpawnOtherHalf) []string {
	fd := findFuncDecl(file, pin.helper)
	if fd == nil {
		return []string{half.file + ": " + pin.helper + " not found; update consoleSpawnPins"}
	}
	where := half.file + ":" + strconv.Itoa(fset.Position(fd.Pos()).Line) + ": " + pin.helper
	if len(half.fields) == 0 {
		if len(fd.Body.List) == 1 {
			if ret, ok := fd.Body.List[0].(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
				if id, ok := ast.Unparen(ret.Results[0]).(*ast.Ident); ok && id.Name == "nil" {
					return nil
				}
			}
		}
		return []string{where + ": body must stay exactly `return nil`: the child's process attributes off Windows are unchanged"}
	}
	set := map[string]bool{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.KeyValueExpr:
			if id, ok := node.Key.(*ast.Ident); ok {
				set[id.Name] = true
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok {
					set[sel.Sel.Name] = true
				}
			}
		}
		return true
	})
	var problems []string
	for _, field := range half.fields {
		if !set[field] {
			problems = append(problems, where+": no longer sets "+field+": the child's process attributes off Windows must stay unchanged")
		}
	}
	return problems
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
		for _, half := range pin.otherHalves {
			ofset, of, _ := parseRepoGoFile(t, root, half.file)
			for _, p := range checkOtherHalf(ofset, of, pin, half) {
				t.Error(p)
			}
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
			name:    "only one of two commands is pinned: the other cannot share the pin",
			body:    "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n\tcmd = exec.Command(\"dolt\", \"sql-server\")\n\t_ = cmd.Start()\n}\n",
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

	const unixHeader = "package p\n\nimport (\n\t\"os\"\n\t\"syscall\"\n)\n\nvar _ = os.Getenv\n\n"
	halves := []struct {
		name    string
		src     string
		fields  []string
		wantErr string // "" = accepted
	}{
		{
			name:   "keeps Setpgid and a conditionally assigned Pdeathsig",
			src:    "func procAttrDetached() *syscall.SysProcAttr {\n\tattr := &syscall.SysProcAttr{Setpgid: true}\n\tif os.Getenv(\"X\") == \"1\" {\n\t\tattr.Pdeathsig = syscall.SIGTERM\n\t}\n\treturn attr\n}\n",
			fields: []string{"Setpgid", "Pdeathsig"},
		},
		{
			name:    "dropped Pdeathsig",
			src:     "func procAttrDetached() *syscall.SysProcAttr {\n\treturn &syscall.SysProcAttr{Setpgid: true}\n}\n",
			fields:  []string{"Setpgid", "Pdeathsig"},
			wantErr: "no longer sets Pdeathsig",
		},
		{
			name:    "Setsid swapped for Setpgid",
			src:     "func procAttrDetached() *syscall.SysProcAttr {\n\treturn &syscall.SysProcAttr{Setpgid: true}\n}\n",
			fields:  []string{"Setsid"},
			wantErr: "no longer sets Setsid",
		},
		{
			name: "nil half stays nil",
			src:  "func procAttrDetached() *syscall.SysProcAttr {\n\treturn nil\n}\n",
		},
		{
			name:    "nil half grows attributes",
			src:     "func procAttrDetached() *syscall.SysProcAttr {\n\treturn &syscall.SysProcAttr{Setsid: true}\n}\n",
			wantErr: "exactly `return nil`",
		},
		{
			name:    "helper renamed away",
			src:     "func otherHelper() *syscall.SysProcAttr {\n\treturn nil\n}\n",
			wantErr: "procAttrDetached not found",
		},
	}
	for _, tc := range halves {
		t.Run("other half/"+tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "fixture_unix.go", unixHeader+tc.src, 0)
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			problems := checkOtherHalf(fset, f, pin, consoleSpawnOtherHalf{file: "fixture_unix.go", fields: tc.fields})
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
}

// consoleSpawnScopes are where bd starts commands from a context that may
// have no console: a directory (trailing slash; every file below it, nested
// packages included) or a single file, whose sibling files stay out of scope.
// This list IS the policy's scope: see the package comment.
var consoleSpawnScopes = []string{
	"internal/beads/",
	"internal/doltserver/",
	"internal/doltversion/",
	"internal/git/",
	"internal/remotecache/",
	"internal/storage/dbproxy/",
	"internal/storage/dolt/",
	"internal/storage/doltutil/",

	"cmd/bd/agent_hook.go",
	"cmd/bd/compact.go",
	"cmd/bd/export_auto.go",
	"cmd/bd/gate.go",
	"cmd/bd/hooks.go",
	"cmd/bd/main.go",
	"cmd/bd/prime.go",
	"cmd/bd/proxied_server.go",
	"internal/config/config.go",
	"internal/hooks/hooks_windows.go",
	"internal/metrics/spawn.go",
}

// consoleSpawnScopeOf returns the consoleSpawnScopes entry covering rel
// (repo-relative, slash-separated), if rel is a non-test Go source file in
// scope. Files of every platform are in: a Unix-only construction is wrapped
// too (Hide is the identity off Windows), so a file's build constraints can
// change without moving a command out from under the policy.
func consoleSpawnScopeOf(rel string) (string, bool) {
	if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
		return "", false
	}
	if strings.Contains("/"+rel, "/testdata/") {
		return "", false
	}
	for _, scope := range consoleSpawnScopes {
		if strings.HasSuffix(scope, "/") {
			if strings.HasPrefix(rel, scope) {
				return scope, true
			}
		} else if rel == scope {
			return scope, true
		}
	}
	return "", false
}

// consoleSpawnKind classifies one finding of the scan.
type consoleSpawnKind string

const (
	// Accepted.
	spawnHidden consoleSpawnKind = "hidden" // exec.Command(...) directly inside execwin.Hide(...)
	spawnPinned consoleSpawnKind = "pinned" // one of consoleSpawnPins' constructions

	// Rejected unless allowlisted.
	spawnUnwrapped    consoleSpawnKind = "construction not wrapped in execwin.Hide"
	spawnFuncValue    consoleSpawnKind = "os/exec constructor used as a function value"
	spawnCmdLiteral   consoleSpawnKind = "exec.Cmd literal"
	spawnStartProcess consoleSpawnKind = "direct process start"
	spawnAttrWrite    consoleSpawnKind = "SysProcAttr write outside a pin"
	spawnConsoleFlag  consoleSpawnKind = "console-creation flag"
	spawnDotImport    consoleSpawnKind = "dot import of os/exec"
)

func (k consoleSpawnKind) accepted() bool { return k == spawnHidden || k == spawnPinned }

// consoleSpawnHit is one finding of the scan.
type consoleSpawnHit struct {
	line     int
	kind     consoleSpawnKind
	call     string // e.g. "exec.Command", "os.StartProcess", "cmd.SysProcAttr"
	tool     string // the command-name argument when it is a string literal, else "<dynamic>"
	funcName string // enclosing top-level func's name, or "" (func literal/none)
	snippet  string // trimmed source text of the hit's line, for allowlist matching
}

// consoleSpawnPinSet holds the positions checkConsoleSpawnPin accepted in one
// file: the pinned constructions and their SysProcAttr assignments.
type consoleSpawnPinSet struct {
	constructions map[token.Pos]bool
	assigns       map[token.Pos]bool
}

// funcBody is one function or func-literal body, for naming the function a
// hit is in.
type funcBody struct {
	start, end token.Pos
	name       string
}

// collectFuncBodies returns every function and func-literal body in file.
// smallestEnclosingBody picks the innermost one, so a spawn inside a closure
// is attributed to the closure, not the outer function.
func collectFuncBodies(file *ast.File) []funcBody {
	var bodies []funcBody
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Body != nil {
				bodies = append(bodies, funcBody{node.Body.Pos(), node.Body.End(), node.Name.Name})
			}
		case *ast.FuncLit:
			if node.Body != nil {
				bodies = append(bodies, funcBody{node.Body.Pos(), node.Body.End(), ""})
			}
		}
		return true
	})
	return bodies
}

// smallestEnclosingBody returns the tightest body containing pos.
func smallestEnclosingBody(bodies []funcBody, pos token.Pos) (funcBody, bool) {
	best := funcBody{}
	found := false
	for _, b := range bodies {
		if pos < b.start || pos >= b.end {
			continue
		}
		if !found || (b.end-b.start) < (best.end-best.start) {
			best, found = b, true
		}
	}
	return best, found
}

// findConsoleSpawns classifies every way file can construct or start a
// process, or set a command's console flags. It resolves the file's own names
// for os/exec and internal/execwin, so an aliased import is still seen and a
// different package that happens to be called execwin is not trusted.
//
// It reuses literalArg and snippetAt from host_tool_exec_policy_test.go; both
// files belong to the same //scripts:go_sources_test target.
func findConsoleSpawns(fset *token.FileSet, file *ast.File, lines []string, pins consoleSpawnPinSet) []consoleSpawnHit {
	execName := importName(file, osExecImportPath)
	execwinName := importName(file, execwinImportPath)
	osName := importName(file, "os")
	syscallName := importName(file, "syscall")
	windowsName := importName(file, "golang.org/x/sys/windows")
	bodies := collectFuncBodies(file)

	var hits []consoleSpawnHit
	add := func(pos token.Pos, kind consoleSpawnKind, call, tool string) {
		body, _ := smallestEnclosingBody(bodies, pos)
		line := fset.Position(pos).Line
		hits = append(hits, consoleSpawnHit{
			line:     line,
			kind:     kind,
			call:     call,
			tool:     tool,
			funcName: body.name,
			snippet:  snippetAt(lines, line),
		})
	}

	if execName == "." {
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == osExecImportPath {
				add(imp.Pos(), spawnDotImport, "import", "")
			}
		}
	}

	// Calls that execwin.Hide wraps directly, and every selector used as a
	// call's function (anything else naming exec.Command is a function value).
	hiddenCalls := map[*ast.CallExpr]bool{}
	calledFuns := map[*ast.SelectorExpr]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			calledFuns[sel] = true
		}
		if isPkgCall(call, execwinName, "Hide") && len(call.Args) == 1 {
			if inner, ok := ast.Unparen(call.Args[0]).(*ast.CallExpr); ok {
				hiddenCalls[inner] = true
			}
		}
		return true
	})

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			switch {
			case isExecConstruction(node, execName):
				sel := node.Fun.(*ast.SelectorExpr)
				argIdx := 0
				if sel.Sel.Name == "CommandContext" {
					argIdx = 1
				}
				tool := "<dynamic>"
				if len(node.Args) > argIdx {
					if name, ok := literalArg(node.Args[argIdx]); ok {
						tool = name
					}
				}
				kind := spawnUnwrapped
				switch {
				case hiddenCalls[node]:
					kind = spawnHidden
				case pins.constructions[node.Pos()]:
					kind = spawnPinned
				}
				add(node.Pos(), kind, "exec."+sel.Sel.Name, tool)
			case isPkgCall(node, osName, "StartProcess"):
				add(node.Pos(), spawnStartProcess, "os.StartProcess", "")
			case isPkgCall(node, syscallName, "StartProcess", "ForkExec", "CreateProcess", "CreateProcessAsUser"):
				add(node.Pos(), spawnStartProcess, "syscall."+node.Fun.(*ast.SelectorExpr).Sel.Name, "")
			case isPkgCall(node, windowsName, "CreateProcess", "CreateProcessAsUser"):
				add(node.Pos(), spawnStartProcess, "windows."+node.Fun.(*ast.SelectorExpr).Sel.Name, "")
			}
		case *ast.SelectorExpr:
			if id, ok := node.X.(*ast.Ident); ok && execName != "" && id.Name == execName &&
				(node.Sel.Name == "Command" || node.Sel.Name == "CommandContext") && !calledFuns[node] {
				add(node.Pos(), spawnFuncValue, "exec."+node.Sel.Name, "")
			}
			if node.Sel.Name == "DETACHED_PROCESS" || node.Sel.Name == "CREATE_NEW_CONSOLE" {
				add(node.Pos(), spawnConsoleFlag, node.Sel.Name, "")
			}
		case *ast.CompositeLit:
			if sel, ok := node.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Cmd" {
				if id, ok := sel.X.(*ast.Ident); ok && execName != "" && id.Name == execName {
					add(node.Pos(), spawnCmdLiteral, "exec.Cmd{}", "")
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				v, ok := sysProcAttrTarget(lhs)
				if !ok || pins.assigns[node.Pos()] {
					continue
				}
				add(node.Pos(), spawnAttrWrite, v+".SysProcAttr", "")
			}
		}
		return true
	})
	return hits
}

// consoleSpawnAllowlistEntry is one justified exception: the enclosing
// function name (empty for a call at package scope or inside a func literal
// this policy cannot name) and the exact trimmed source line, rather than a
// line number, so an entry survives the file shifting lines around it.
type consoleSpawnAllowlistEntry struct {
	fn      string
	snippet string
	why     string
}

// consoleSpawnAllowlist is file (repo-relative, slash-separated) -> the entries
// justifying a rejected finding there. It is empty on purpose: every command
// in scope today is wrapped or pinned, and an entry here is a claim that one
// is not and is nonetheless safe. An entry that stops matching fails the test.
var consoleSpawnAllowlist = map[string][]consoleSpawnAllowlistEntry{}

// matchConsoleAllowlist finds the allowlist entry (if any) matching hit by
// enclosing function name and exact source-line snippet, returning its index
// for usage tracking.
func matchConsoleAllowlist(entries []consoleSpawnAllowlistEntry, hit consoleSpawnHit) (int, bool) {
	for i, e := range entries {
		if e.fn == hit.funcName && e.snippet == hit.snippet {
			return i, true
		}
	}
	return 0, false
}

// consoleSpawnFileResult is the policy's verdict on one file.
type consoleSpawnFileResult struct {
	problems []string // each names file:line and what is wrong there
	accepted int      // wrapped or pinned commands recognised
	stale    []consoleSpawnAllowlistEntry
}

// checkConsoleSpawnFile applies the whole policy to one parsed file: each of
// pins (all naming rel) must hold, and every finding must be accepted or
// matched by an entry of allow, each of which must match something. The
// repo-wide test and the fixture tests share it, so the fixtures exercise the
// exact decision the real tree gets.
func checkConsoleSpawnFile(fset *token.FileSet, file *ast.File, src []byte, rel string,
	pins []consoleSpawnPin, allow []consoleSpawnAllowlistEntry) consoleSpawnFileResult {
	var res consoleSpawnFileResult
	set := consoleSpawnPinSet{constructions: map[token.Pos]bool{}, assigns: map[token.Pos]bool{}}
	for _, pin := range pins {
		pc := checkConsoleSpawnPin(fset, file, pin)
		res.problems = append(res.problems, pc.problems...)
		if pc.construction.IsValid() {
			set.constructions[pc.construction] = true
		}
		if pc.assign.IsValid() {
			set.assigns[pc.assign] = true
		}
	}
	matched := map[int]bool{}
	for _, hit := range findConsoleSpawns(fset, file, strings.Split(string(src), "\n"), set) {
		if hit.kind.accepted() {
			res.accepted++
			continue
		}
		if idx, ok := matchConsoleAllowlist(allow, hit); ok {
			matched[idx] = true
			continue
		}
		res.problems = append(res.problems, fmt.Sprintf("%s:%d: %s: %s(%q): on Windows a console-subsystem "+
			"child of a console-less bd gets a brand-new console, which Windows Terminal shows as a "+
			"window that takes focus and which can fail a short-lived child with 0x800700E8 "+
			"(gastownhall/beads#6076); construct it as execwin.Hide(exec.Command(...)), or add a "+
			"justified consoleSpawnAllowlist entry", rel, hit.line, hit.kind, hit.call, hit.tool))
	}
	for idx, entry := range allow {
		if !matched[idx] {
			res.stale = append(res.stale, entry)
		}
	}
	return res
}

// TestNoUnsuppressedConsoleSpawns is the policy itself: in every in-scope file,
// whatever platform it builds for, each ordinary command is constructed directly
// inside execwin.Hide, the three long-lived ones are pinned exactly, and
// nothing else starts a process or touches a command's console flags.
func TestNoUnsuppressedConsoleSpawns(t *testing.T) {
	root := sourceRepoRoot(t)
	relFiles := repoFiles(t, root, 1000)

	scannedPerScope := map[string]int{}
	commandsPerScope := map[string]int{}
	pinsSeen := map[int]bool{}
	allowScanned := map[string]bool{}
	for _, rel := range relFiles {
		scope, ok := consoleSpawnScopeOf(rel)
		if !ok {
			continue
		}
		fset, f, data := parseRepoGoFile(t, root, rel)
		scannedPerScope[scope]++

		var pins []consoleSpawnPin
		for i, pin := range consoleSpawnPins {
			if pin.file != rel {
				continue
			}
			pinsSeen[i] = true
			pins = append(pins, pin)
			hfset, hf, _ := parseRepoGoFile(t, root, pin.helperFile)
			for _, p := range checkBackgroundHelper(hfset, hf, pin) {
				t.Error(p)
			}
			for _, half := range pin.otherHalves {
				ofset, of, _ := parseRepoGoFile(t, root, half.file)
				for _, p := range checkOtherHalf(ofset, of, pin, half) {
					t.Error(p)
				}
			}
		}

		allowScanned[rel] = true
		res := checkConsoleSpawnFile(fset, f, data, rel, pins, consoleSpawnAllowlist[rel])
		commandsPerScope[scope] += res.accepted
		for _, p := range res.problems {
			t.Error(p)
		}
		for _, entry := range res.stale {
			t.Errorf("consoleSpawnAllowlist entry %s (func %q, snippet %q) no longer matches a "+
				"rejected finding; remove it", rel, entry.fn, entry.snippet)
		}
	}

	for i, pin := range consoleSpawnPins {
		if !pinsSeen[i] {
			t.Errorf("consoleSpawnPins entry %s (func %s) names a file that was not scanned: out of scope "+
				"or renamed", pin.file, pin.fn)
		}
	}
	for rel := range consoleSpawnAllowlist {
		if !allowScanned[rel] {
			t.Errorf("consoleSpawnAllowlist names %s, which was not scanned; remove its entries", rel)
		}
	}
	// An empty or mis-filtered walk, or a scan that quietly stopped
	// recognising commands (an aliased import, a helper hiding the call),
	// would pass everything above while checking nothing. Every scope entry
	// constructs commands today, so none at all is a broken scope or scan.
	for _, scope := range consoleSpawnScopes {
		if scannedPerScope[scope] == 0 {
			t.Errorf("no file was scanned for scope entry %s: the entry is stale (renamed or deleted), "+
				"or the walk is broken", scope)
			continue
		}
		if commandsPerScope[scope] == 0 {
			t.Errorf("the scan recognised no wrapped or pinned command under %s although it has "+
				"files: the scan is broken or the entry is stale (this check exists "+
				"so the policy cannot be disarmed silently)", scope)
		}
	}
}

// TestConsoleSpawnScope pins which files the policy reads: directory entries
// cover their descendants, file entries cover only that file and never its
// siblings, test files and testdata are out, and files of every platform are
// in and scanned.
func TestConsoleSpawnScope(t *testing.T) {
	root := sourceRepoRoot(t)

	inScope := []struct{ rel, scope string }{
		{"internal/doltserver/doltserver.go", "internal/doltserver/"},
		{"internal/doltserver/doltserver_windows.go", "internal/doltserver/"},
		{"internal/doltversion/probe.go", "internal/doltversion/"},
		{"internal/storage/dbproxy/server/doltserver.go", "internal/storage/dbproxy/"},
		{"internal/storage/dbproxy/proxy/endpoint.go", "internal/storage/dbproxy/"},
		{"internal/storage/dolt/store.go", "internal/storage/dolt/"},
		{"internal/beads/context.go", "internal/beads/"},
		{"internal/git/gitdir.go", "internal/git/"},
		{"cmd/bd/main.go", "cmd/bd/main.go"},
		{"cmd/bd/compact.go", "cmd/bd/compact.go"},
		{"cmd/bd/proxied_server.go", "cmd/bd/proxied_server.go"},
		{"cmd/bd/hooks.go", "cmd/bd/hooks.go"},
		{"cmd/bd/export_auto.go", "cmd/bd/export_auto.go"},
		{"cmd/bd/gate.go", "cmd/bd/gate.go"},
		{"internal/config/config.go", "internal/config/config.go"},
		{"internal/hooks/hooks_windows.go", "internal/hooks/hooks_windows.go"},
		{"internal/metrics/spawn.go", "internal/metrics/spawn.go"},
	}
	for _, tc := range inScope {
		if got, ok := consoleSpawnScopeOf(tc.rel); !ok || got != tc.scope {
			t.Errorf("consoleSpawnScopeOf(%s) = %q, %v; want %q, true", tc.rel, got, ok, tc.scope)
		}
	}

	for _, rel := range []string{
		// Siblings of file entries, the four reviewed ones included.
		"cmd/bd/dolt.go",
		"cmd/bd/hook.go",
		"cmd/bd/hooks_extra.go",
		"cmd/bd/proxied_server_unix.go",
		"cmd/bd/export.go",
		"cmd/bd/gate_check.go",
		"cmd/bd/prime_stealth.go",
		"internal/config/yaml_config.go",
		"internal/hooks/hooks.go",
		"internal/hooks/hooks_unix.go",
		"internal/metrics/metrics.go",
		// A directory entry is not a name prefix.
		"internal/doltfake/fake.go",
		"internal/gitfoo/x.go",
		"internal/storage/doltutilx/x.go",
		"internal/storage/dbproxyx/x.go",
		// Tests, testdata and non-Go files.
		"internal/doltserver/doltserver_test.go",
		"cmd/bd/hooks_test.go",
		"internal/git/testdata/fixture.go",
		"internal/storage/dbproxy/server/testdata/x/y.go",
		"internal/git/README.md",
	} {
		if got, ok := consoleSpawnScopeOf(rel); ok {
			t.Errorf("consoleSpawnScopeOf(%s) = %q, true; want out of scope", rel, got)
		}
	}

	// Every platform's files are in scope and scanned, not only those that
	// build for Windows: a Unix- or Darwin-only command must be wrapped too,
	// so a file's build constraints never move a command out of the policy.
	for _, rel := range []string{
		"internal/doltserver/doltserver_windows.go",
		"internal/hooks/hooks_windows.go",
		"internal/doltserver/doltserver_unix.go",
		"internal/doltserver/listenerowner_darwin.go",
		"internal/doltserver/sweep_darwin.go",
		"internal/storage/dbproxy/proxy/process_executable_darwin.go",
		"internal/storage/dbproxy/proxy/unverified_process_darwin.go",
	} {
		if _, ok := consoleSpawnScopeOf(rel); !ok {
			t.Errorf("%s: out of scope, want in scope", rel)
			continue
		}
		fset, f, data := parseRepoGoFile(t, root, rel)
		if res := checkConsoleSpawnFile(fset, f, data, rel, nil, consoleSpawnAllowlist[rel]); res.accepted == 0 || len(res.problems) != 0 {
			t.Errorf("%s: accepted %d, problems %q; want its commands recognised as wrapped", rel, res.accepted, res.problems)
		}
	}
}

// fixturePin pins the construction in a fixture's start function.
var fixturePin = consoleSpawnPin{file: "fixture.go", fn: "start", cmdVar: "cmd", helper: "procAttrDetached", helperFile: "fixture_windows.go"}

// scanConsoleFixture parses src as fixture.go and returns the policy's
// verdict on it plus the kind of every finding, in source order.
func scanConsoleFixture(t *testing.T, src string, pins []consoleSpawnPin) (consoleSpawnFileResult, []consoleSpawnKind) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	res := checkConsoleSpawnFile(fset, f, []byte(src), "fixture.go", pins, nil)
	set := consoleSpawnPinSet{constructions: map[token.Pos]bool{}, assigns: map[token.Pos]bool{}}
	for _, pin := range pins {
		pc := checkConsoleSpawnPin(fset, f, pin)
		set.constructions[pc.construction] = true
		set.assigns[pc.assign] = true
	}
	var kinds []consoleSpawnKind
	for _, hit := range findConsoleSpawns(fset, f, strings.Split(src, "\n"), set) {
		kinds = append(kinds, hit.kind)
	}
	return res, kinds
}

// TestConsoleSpawnPolicyScan pins the scan's accept/reject decisions on
// in-memory fixtures, so a regression in the analysis is caught without
// touching the real tree. The first seven cases are #7572's, adapted: an
// assignment to SysProcAttr no longer excuses a construction, only an exact
// consoleSpawnPins entry does.
func TestConsoleSpawnPolicyScan(t *testing.T) {
	const (
		execImport    = "import \"os/exec\"\n\n"
		execwinImport = "import (\n\t\"os/exec\"\n\n\t\"github.com/steveyegge/beads/internal/execwin\"\n)\n\n"
	)
	cases := []struct {
		name      string
		src       string // after "package p\n\n"
		pins      []consoleSpawnPin
		want      []consoleSpawnKind // every finding, in source order
		wantProbs int                // problems: rejected findings plus pin failures
		wantInPin string             // a pin problem containing this, if any
	}{
		{
			name:      "bare spawn with neither wrapper nor SysProcAttr is reported",
			src:       execImport + "func probe() {\n\tout, _ := exec.Command(\"dolt\", \"version\").Output()\n\t_ = out\n}\n",
			want:      []consoleSpawnKind{spawnUnwrapped},
			wantProbs: 1,
		},
		{
			name: "execwin.Hide wrapper is accepted",
			src:  execwinImport + "func probe() {\n\tout, _ := execwin.Hide(exec.Command(\"dolt\", \"version\")).Output()\n\t_ = out\n}\n",
			want: []consoleSpawnKind{spawnHidden},
		},
		{
			name: "CommandContext wrapped in Hide is accepted, and the tool name is the one after ctx",
			src: "import (\n\t\"context\"\n\t\"os/exec\"\n\n\t\"github.com/steveyegge/beads/internal/execwin\"\n)\n\n" +
				"func probe(ctx context.Context, path string) {\n\tcmd := execwin.Hide(exec.CommandContext(ctx, path, \"version\"))\n\t_ = cmd.Run()\n}\n",
			want: []consoleSpawnKind{spawnHidden},
		},
		{
			name: "explicit SysProcAttr assignment in the enclosing function no longer excuses the spawn",
			src:  execImport + "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n\t_ = cmd.Start()\n}\n",
			// Not a consoleSpawnPins entry: both the bare construction and
			// the attribute write are rejected.
			want:      []consoleSpawnKind{spawnUnwrapped, spawnAttrWrite},
			wantProbs: 2,
		},
		{
			name:      "a non-literal tool name earns no exemption",
			src:       execImport + "func locate(bin string) {\n\tout, _ := exec.Command(bin, \"version\").Output()\n\t_ = out\n}\n",
			want:      []consoleSpawnKind{spawnUnwrapped},
			wantProbs: 1,
		},
		{
			name: "SysProcAttr in a DIFFERENT function does not excuse the spawn",
			src: execImport + "func configure(cmd *exec.Cmd) {\n\tcmd.SysProcAttr = procAttrHidden()\n}\n\n" +
				"func probe() {\n\tout, _ := exec.Command(\"dolt\", \"version\").Output()\n\t_ = out\n}\n",
			want:      []consoleSpawnKind{spawnAttrWrite, spawnUnwrapped},
			wantProbs: 2,
		},
		{
			name: "a spawn inside a closure is judged on its own",
			src: execImport + "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n" +
				"\tgo func() {\n\t\t_ = exec.Command(\"git\", \"config\", \"user.name\").Run()\n\t}()\n\t_ = cmd.Start()\n}\n",
			pins:      []consoleSpawnPin{fixturePin},
			want:      []consoleSpawnKind{spawnPinned, spawnUnwrapped},
			wantProbs: 1,
		},

		{
			name: "the exact pinned construction is accepted",
			src:  execImport + "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n\t_ = cmd.Start()\n}\n",
			pins: []consoleSpawnPin{fixturePin},
			want: []consoleSpawnKind{spawnPinned},
		},
		{
			name: "one of two commands pinned: the other is still rejected",
			src: execImport + "func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n" +
				"\tprobe := exec.Command(\"dolt\", \"version\")\n\t_ = probe.Run()\n\t_ = cmd.Start()\n}\n",
			pins:      []consoleSpawnPin{fixturePin},
			want:      []consoleSpawnKind{spawnPinned, spawnUnwrapped},
			wantProbs: 1,
		},
		{
			name: "a late clobber of a pin fails at the clobber",
			src: "import (\n\t\"os/exec\"\n\t\"syscall\"\n)\n\n" +
				"func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n" +
				"\tcmd.SysProcAttr = &syscall.SysProcAttr{}\n\t_ = cmd.Start()\n}\n",
			pins:      []consoleSpawnPin{fixturePin},
			want:      []consoleSpawnKind{spawnPinned, spawnAttrWrite},
			wantProbs: 2,
			wantInPin: "fixture.go:11: start: cmd.SysProcAttr is assigned something other than procAttrDetached()",
		},
		{
			name:      "a pin with the wrong helper fails",
			src:       "import (\n\t\"os/exec\"\n\t\"syscall\"\n)\n\nfunc start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = &syscall.SysProcAttr{}\n\t_ = cmd.Start()\n}\n",
			pins:      []consoleSpawnPin{fixturePin},
			want:      []consoleSpawnKind{spawnPinned, spawnAttrWrite},
			wantProbs: 2,
			wantInPin: "something other than procAttrDetached()",
		},
		{
			name: "an unrelated attribute write on a hidden command is rejected",
			src: "import (\n\t\"os/exec\"\n\t\"syscall\"\n\n\t\"github.com/steveyegge/beads/internal/execwin\"\n)\n\n" +
				"func probe() {\n\tcmd := execwin.Hide(exec.Command(\"git\", \"status\"))\n\tcmd.SysProcAttr = &syscall.SysProcAttr{}\n\t_ = cmd.Run()\n}\n",
			want:      []consoleSpawnKind{spawnHidden, spawnAttrWrite},
			wantProbs: 1,
		},
		{
			name: "aliased os/exec and execwin imports are resolved",
			src: "import (\n\tosexec \"os/exec\"\n\n\tew \"github.com/steveyegge/beads/internal/execwin\"\n)\n\n" +
				"func probe() {\n\t_ = ew.Hide(osexec.Command(\"git\", \"status\")).Run()\n\t_ = osexec.Command(\"git\", \"log\").Run()\n}\n",
			want:      []consoleSpawnKind{spawnHidden, spawnUnwrapped},
			wantProbs: 1,
		},
		{
			name: "a Hide from another package earns nothing",
			src: "import (\n\t\"os/exec\"\n\n\texecwin \"example.com/other\"\n)\n\n" +
				"func probe() {\n\t_ = execwin.Hide(exec.Command(\"git\", \"status\")).Run()\n}\n",
			want:      []consoleSpawnKind{spawnUnwrapped},
			wantProbs: 1,
		},
		{
			name: "Hide must wrap the construction directly",
			src: execwinImport + "func probe() {\n\tcmd := exec.Command(\"git\", \"status\")\n\t_ = execwin.Hide(cmd).Run()\n" +
				"\t_ = execwin.Hide(prepare(exec.Command(\"git\", \"log\"))).Run()\n}\n",
			want:      []consoleSpawnKind{spawnUnwrapped, spawnUnwrapped},
			wantProbs: 2,
		},
		{
			name:      "a constructor used as a function value is rejected",
			src:       execImport + "var newCmd = exec.Command\n\nfunc probe() {\n\t_ = newCmd(\"git\", \"status\").Run()\n}\n",
			want:      []consoleSpawnKind{spawnFuncValue},
			wantProbs: 1,
		},
		{
			name:      "an exec.Cmd literal is rejected",
			src:       execImport + "func probe() {\n\t_ = (&exec.Cmd{Path: \"git\", Args: []string{\"git\", \"status\"}}).Run()\n}\n",
			want:      []consoleSpawnKind{spawnCmdLiteral},
			wantProbs: 1,
		},
		{
			name: "direct process starts are rejected",
			src: "import (\n\t\"os\"\n\t\"syscall\"\n)\n\n" +
				"func probe() {\n\t_, _ = os.StartProcess(\"git\", nil, &os.ProcAttr{})\n\t_, _, _ = syscall.StartProcess(\"git\", nil, nil)\n}\n",
			want:      []consoleSpawnKind{spawnStartProcess, spawnStartProcess},
			wantProbs: 2,
		},
		{
			name: "a console-creation flag is rejected even on a hidden command",
			src: "import (\n\t\"os/exec\"\n\t\"syscall\"\n\n\t\"github.com/steveyegge/beads/internal/execwin\"\n\t\"golang.org/x/sys/windows\"\n)\n\n" +
				"func probe() {\n\tcmd := execwin.Hide(exec.Command(\"git\", \"status\"))\n" +
				"\tcmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}\n\t_ = cmd.Run()\n}\n",
			want:      []consoleSpawnKind{spawnHidden, spawnAttrWrite, spawnConsoleFlag},
			wantProbs: 2,
		},
		{
			name: "a direct windows.CreateProcess is rejected in a file without os/exec, aliased import included",
			src: "import (\n\t\"syscall\"\n\n\tw \"golang.org/x/sys/windows\"\n)\n\n" +
				"func probe(app *uint16, si *syscall.StartupInfo, pi *syscall.ProcessInformation) {\n" +
				"\t_ = w.CreateProcess(app, nil, nil, nil, false, 0, nil, nil, si, pi)\n" +
				"\t_ = syscall.CreateProcess(app, nil, nil, nil, false, 0, nil, nil, si, pi)\n}\n",
			want:      []consoleSpawnKind{spawnStartProcess, spawnStartProcess},
			wantProbs: 2,
		},
		{
			name:      "a dot import of os/exec is rejected",
			src:       "import . \"os/exec\"\n\nfunc probe() {\n\t_ = Command(\"git\", \"status\").Run()\n}\n",
			want:      []consoleSpawnKind{spawnDotImport},
			wantProbs: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, kinds := scanConsoleFixture(t, "package p\n\n"+tc.src, tc.pins)
			if fmt.Sprint(kinds) != fmt.Sprint(tc.want) {
				t.Errorf("findings = %v, want %v", kinds, tc.want)
			}
			if len(res.problems) != tc.wantProbs {
				t.Errorf("problems = %d, want %d:\n%s", len(res.problems), tc.wantProbs, strings.Join(res.problems, "\n"))
			}
			if tc.wantInPin != "" && !strings.Contains(strings.Join(res.problems, "\n"), tc.wantInPin) {
				t.Errorf("problems = %q, want one containing %q", res.problems, tc.wantInPin)
			}
		})
	}

	// Removing one real wrapper from each of the four files the review
	// added to scope must fail the policy at that line.
	root := sourceRepoRoot(t)
	for _, rel := range []string{"cmd/bd/proxied_server.go", "cmd/bd/hooks.go", "cmd/bd/export_auto.go", "cmd/bd/gate.go"} {
		t.Run("unwrapping the first Hide in "+rel+" fails", func(t *testing.T) {
			fset, f, data := parseRepoGoFile(t, root, rel)
			if res := checkConsoleSpawnFile(fset, f, data, rel, nil, nil); len(res.problems) != 0 || res.accepted == 0 {
				t.Fatalf("unmodified %s: accepted %d, problems %q; want some accepted and none rejected", rel, res.accepted, res.problems)
			}
			execwinName := importName(f, execwinImportPath)
			var hide *ast.CallExpr
			ast.Inspect(f, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && hide == nil && isPkgCall(call, execwinName, "Hide") {
					hide = call
				}
				return hide == nil
			})
			if hide == nil {
				t.Fatalf("%s has no execwin.Hide call", rel)
			}
			off := func(p token.Pos) int { return fset.Position(p).Offset }
			inner := hide.Args[0]
			spliced := string(data[:off(hide.Pos())]) + string(data[off(inner.Pos()):off(inner.End())]) + string(data[off(hide.End()):])
			wantLine := fset.Position(hide.Pos()).Line

			sfset := token.NewFileSet()
			sf, err := parser.ParseFile(sfset, rel, spliced, 0)
			if err != nil {
				t.Fatalf("parse spliced %s: %v", rel, err)
			}
			res := checkConsoleSpawnFile(sfset, sf, []byte(spliced), rel, nil, nil)
			want := fmt.Sprintf("%s:%d: %s", rel, wantLine, spawnUnwrapped)
			if len(res.problems) != 1 || !strings.HasPrefix(res.problems[0], want) {
				t.Fatalf("problems = %q, want exactly one starting %q", res.problems, want)
			}
		})
	}
}

// TestConsoleSpawnAllowlistMatching pins that an entry matches by enclosing
// function plus exact source line, excuses only that finding, and is reported
// stale once nothing matches it -- the repo-wide test fails on one.
func TestConsoleSpawnAllowlistMatching(t *testing.T) {
	src := "package p\n\nimport \"os/exec\"\n\n" +
		"func probe() {\n\tout, _ := exec.Command(\"dolt\", \"version\").Output()\n\t_ = out\n}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	hits := findConsoleSpawns(fset, f, strings.Split(src, "\n"), consoleSpawnPinSet{})
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(hits))
	}
	hit := hits[0]
	if hit.funcName != "probe" {
		t.Errorf("funcName = %q, want %q", hit.funcName, "probe")
	}
	if hit.tool != "dolt" {
		t.Errorf("tool = %q, want %q", hit.tool, "dolt")
	}

	entry := consoleSpawnAllowlistEntry{fn: "probe", snippet: hit.snippet, why: "fixture"}
	entries := []consoleSpawnAllowlistEntry{entry}
	if _, ok := matchConsoleAllowlist(entries, hit); !ok {
		t.Errorf("entry keyed by func %q and snippet %q did not match", hit.funcName, hit.snippet)
	}
	if _, ok := matchConsoleAllowlist(entries, consoleSpawnHit{funcName: "other", snippet: hit.snippet}); ok {
		t.Errorf("entry matched a hit in a different function")
	}
	if _, ok := matchConsoleAllowlist(entries, consoleSpawnHit{funcName: hit.funcName, snippet: "something else"}); ok {
		t.Errorf("entry matched a hit with a different source line")
	}

	if res := checkConsoleSpawnFile(fset, f, []byte(src), "fixture.go", nil, entries); len(res.problems) != 0 || len(res.stale) != 0 {
		t.Errorf("matching entry: problems %q, stale %v; want neither", res.problems, res.stale)
	}
	// The same entry after the line is fixed: nothing left to excuse, so it
	// is stale.
	fixed := strings.Replace(src, "exec.Command(\"dolt\", \"version\")", "execwin.Hide(exec.Command(\"dolt\", \"version\"))", 1)
	fixed = strings.Replace(fixed, "import \"os/exec\"", "import (\n\t\"os/exec\"\n\n\t\"github.com/steveyegge/beads/internal/execwin\"\n)", 1)
	ffset := token.NewFileSet()
	ff, err := parser.ParseFile(ffset, "fixture.go", fixed, 0)
	if err != nil {
		t.Fatalf("parse fixed fixture: %v", err)
	}
	res := checkConsoleSpawnFile(ffset, ff, []byte(fixed), "fixture.go", nil, entries)
	if len(res.problems) != 0 || len(res.stale) != 1 || res.stale[0] != entry {
		t.Errorf("fixed line: problems %q, stale %v; want no problems and the entry stale", res.problems, res.stale)
	}
}
