package scripts_test

// Policy: a spawn in a package that can run behind a console-less process must
// suppress the console window Windows would otherwise allocate for it.
//
// Background. On Windows a console-subsystem child started by a process that
// has NO console of its own does not inherit one: the system allocates a brand
// new console for it. When Windows Terminal is registered as the default
// terminal application (defterm -- HKCU\Console\%%Startup carries its
// DelegationConsole/DelegationTerminal CLSIDs, {2EACA947-...} and
// {E12CFF52-...}), that console is handed to Windows Terminal, which opens a
// real window that takes focus. For a short-lived child it is worse than
// cosmetic: the child can exit before Windows Terminal finishes attaching, so
// the handoff pipe is already closed and the launch reports
//
//	[error 2147942632 (0x800700E8) when launching `"...\dolt.exe" init']
//
// where 0x800700E8 is HRESULT_FROM_WIN32(ERROR_NO_DATA), "The pipe is being
// closed." (gastownhall/beads#6076, fixed by gastownhall/beads#6077.)
//
// bd reaches that state on its ordinary paths, which is why this is a policy
// and not a one-off fix. bd itself often has no console (an MCP server, an
// agent harness with redirected stdio), and every console child it then
// starts would pop a window; so would anything below a child that was given no
// console. That is why the long-lived children -- bd db-proxy-child and the
// dolt sql-servers -- get CREATE_NO_WINDOW rather than DETACHED_PROCESS: a
// hidden console is inherited by their own children (dolt runs `uname` on
// every start), while no console makes each of them open one. A spawn site is
// therefore not wrong because of WHICH tool it runs -- dolt, git, powershell,
// netstat, or a path held in a variable -- but because of WHERE it runs, so
// unlike the host-tool policy beside this file the scan is deliberately
// name-agnostic: no tool name earns an exemption.
//
// Scope: the non-test .go files of the packages that spawn from a context which
// may have no console -- internal/doltserver, internal/doltversion and
// internal/storage/dbproxy. Files that cannot build for Windows are out of
// scope, decided by go/build rather than by reading tags here: there is no
// console window to suppress on those platforms, and demanding the Windows
// helper in them would be noise (`//go:build !windows` in doltserver_unix.go,
// the `_darwin.go` sweep and listener files, `//go:build unix` in the dbproxy
// proxy). Production code elsewhere (cmd/bd, internal/storage/dolt, ...)
// legitimately spawns with a console of its own; the day a new package starts
// spawning from a console-less context, add its prefix to
// consoleSpawnPackages -- that list is the policy's whole statement of scope.
// shell-script and Bazel layers are not in scope: this is about Go process
// creation.
//
// A spawn is accepted when:
//   - it is lexically wrapped in execwin.Hide(...), the shared helper
//     (internal/execwin) that ORs in CREATE_NO_WINDOW on Windows and is a
//     strict no-op elsewhere; or
//   - its enclosing function assigns SysProcAttr on some command, which is how
//     the long-lived children state their flags explicitly (CREATE_NO_WINDOW
//     with CREATE_NEW_PROCESS_GROUP for the proxy child, the managed
//     sql-server and the shared server; each has a Windows test pinning that
//     it is not DETACHED_PROCESS); or
//   - it is in consoleSpawnAllowlist below, each entry naming the reason the
//     two rules above cannot see.
//
// Known limitation, recorded rather than papered over: detection is lexical.
// `cmd := exec.Command(...)` followed later by `execwin.Hide(cmd)` in the same
// function is reported, even though it is safe, because the wrapper is not
// around the call. Prefer the inline form the repository already uses
// (`execwin.Hide(exec.Command("git", "config", "user.name")).Output()`); a hit
// in the splintered form needs an allowlist entry. A file gated on a custom
// build tag other than GOOS/GOARCH is likewise out of scope, since the go/build
// context below does not declare those tags.
//
// Mutation check: TestConsoleSpawnPolicyScan's fixtures include a bare
// exec.Command("dolt", "version") with neither wrapper nor SysProcAttr and
// assert the scan reports it; others pin that execwin.Hide(...) and an explicit
// SysProcAttr assignment are both accepted, that a dynamic (non-literal) tool
// name does NOT earn an exemption, that a SysProcAttr assignment in a DIFFERENT
// function does not excuse a hit, that a spawn inside a func literal is judged
// against the closure rather than the function around it, and that the
// allowlist matches by enclosing function plus exact source line.
// TestConsoleSpawnScope pins that a
// `//go:build !windows` file and a `_darwin.go` file are out of scope while
// their Windows-building siblings are in it.
//
// Counts keep the repository-wide scan honest as well as assertions, so it
// cannot be disarmed silently: each in-scope package must contribute
// Windows-buildable files AND
// at least one recognised spawn, and every allowlist entry must still match a
// live hit.

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// consoleSpawnPackages are the package prefixes whose spawns run behind a
// process that may have no console of its own. This list IS the policy's
// scope: see the package comment.
var consoleSpawnPackages = []string{
	"internal/doltserver/",
	"internal/doltversion/",
	"internal/storage/dbproxy/",
}

// windowsBuildContext decides which files could be built for Windows. A file
// this does not match has no console window to suppress.
var windowsBuildContext = build.Context{GOOS: "windows", GOARCH: "amd64"}

// consoleSpawnHit is one exec spawn this scan found.
type consoleSpawnHit struct {
	line     int
	tool     string // the tool-name argument when it is a string literal, else "<dynamic>"
	call     string // "exec.Command" or "exec.CommandContext"
	hidden   bool   // lexically wrapped in execwin.Hide(...)
	pinned   bool   // enclosing function assigns SysProcAttr
	funcName string // enclosing top-level func's name, or "" (func literal/none)
	snippet  string // trimmed source text of the hit's line, for allowlist matching
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
// justifying an unsuppressed spawn there. It is empty on purpose: every spawn
// in scope today routes through execwin.Hide or sets SysProcAttr, and an entry
// here is a claim that one does not and is nonetheless safe.
var consoleSpawnAllowlist = map[string][]consoleSpawnAllowlistEntry{}

// consoleSpawnInScope reports whether rel (repo-relative, slash-separated) is
// a non-test Go file in one of consoleSpawnPackages. Whether it can build for
// Windows is a second, separate question answered by windowsBuildContext.
func consoleSpawnInScope(rel string) bool {
	if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
		return false
	}
	for _, prefix := range consoleSpawnPackages {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// funcBody is one function or func-literal body, for deciding which assignment
// and which spawn share a scope.
type funcBody struct {
	start, end token.Pos
	name       string
}

// collectFuncBodies returns every function and func-literal body in file.
// smallestEnclosingBody picks the innermost one, so a spawn inside a closure
// is judged against the closure, not the outer function.
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

// findConsoleSpawns reports every exec.Command / exec.CommandContext call in
// file, marking for each whether it is wrapped in execwin.Hide(...) and
// whether its enclosing function assigns SysProcAttr.
//
// It reuses literalArg and snippetAt from host_tool_exec_policy_test.go; both
// files belong to the same //scripts:go_sources_test target.
func findConsoleSpawns(fset *token.FileSet, file *ast.File, lines []string) []consoleSpawnHit {
	bodies := collectFuncBodies(file)

	// Positions of call expressions that execwin.Hide wraps, and the bodies
	// that assign SysProcAttr anywhere inside them.
	hiddenCalls := map[token.Pos]bool{}
	pinnedBodies := map[token.Pos]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "execwin" || sel.Sel.Name != "Hide" || len(node.Args) == 0 {
				return true
			}
			hiddenCalls[node.Args[0].Pos()] = true
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "SysProcAttr" {
					continue
				}
				if body, ok := smallestEnclosingBody(bodies, node.Pos()); ok {
					pinnedBodies[body.start] = true
				}
			}
		}
		return true
	})

	var hits []consoleSpawnHit
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
		argIdx := 0
		switch sel.Sel.Name {
		case "Command":
			argIdx = 0
		case "CommandContext":
			argIdx = 1
		default:
			return true
		}
		tool := "<dynamic>"
		if len(call.Args) > argIdx {
			if name, ok := literalArg(call.Args[argIdx]); ok {
				tool = name
			}
		}
		body, _ := smallestEnclosingBody(bodies, call.Pos())
		line := fset.Position(call.Pos()).Line
		hits = append(hits, consoleSpawnHit{
			line:     line,
			tool:     tool,
			call:     "exec." + sel.Sel.Name,
			hidden:   hiddenCalls[call.Pos()],
			pinned:   pinnedBodies[body.start],
			funcName: body.name,
			snippet:  snippetAt(lines, line),
		})
		return true
	})
	return hits
}

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

// TestNoUnsuppressedConsoleSpawns is the policy itself: every in-scope spawn
// under a package that can run console-less must suppress the console window.
func TestNoUnsuppressedConsoleSpawns(t *testing.T) {
	root := sourceRepoRoot(t)
	relFiles := repoFiles(t, root, 1000)

	scannedPerPrefix := map[string]int{}
	spawnsPerPrefix := map[string]int{}
	seen := map[string]map[int]bool{} // rel -> allowlist entry index -> matched
	for _, rel := range relFiles {
		if !consoleSpawnInScope(rel) {
			continue
		}
		dir, name := filepath.Split(rel)
		buildsForWindows, err := windowsBuildContext.MatchFile(filepath.Join(root, filepath.FromSlash(dir)), name)
		if err != nil {
			t.Fatalf("evaluate build constraints for %s: %v", rel, err)
		}
		if !buildsForWindows {
			continue // cannot build for Windows: no console window to suppress
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
		filePrefix := ""
		for _, prefix := range consoleSpawnPackages {
			if strings.HasPrefix(rel, prefix) {
				scannedPerPrefix[prefix]++
				if filePrefix == "" {
					filePrefix = prefix
				}
			}
		}

		lines := strings.Split(string(data), "\n")
		hits := findConsoleSpawns(fset, f, lines)
		spawnsPerPrefix[filePrefix] += len(hits)
		for _, hit := range hits {
			if hit.hidden || hit.pinned {
				continue
			}
			if idx, ok := matchConsoleAllowlist(consoleSpawnAllowlist[rel], hit); ok {
				if seen[rel] == nil {
					seen[rel] = map[int]bool{}
				}
				seen[rel][idx] = true
				continue
			}
			t.Errorf("%s:%d: unsuppressed %s(%q): on Windows a console-subsystem child of a console-less "+
				"parent gets a brand-new console, which Windows Terminal shows as a window that takes "+
				"focus and which can fail a short-lived child with 0x800700E8 (gastownhall/beads#6076); "+
				"wrap it in execwin.Hide(...), set its SysProcAttr explicitly, or add a justified "+
				"consoleSpawnAllowlist entry", rel, hit.line, hit.call, hit.tool)
		}
	}

	// An empty or mis-filtered walk would pass every scan above. Each package
	// in scope must actually have contributed Windows-buildable files.
	for _, prefix := range consoleSpawnPackages {
		if scannedPerPrefix[prefix] == 0 {
			t.Errorf("no in-scope file was scanned under %s: the scope list or the Windows build "+
				"filter is broken, or the package was renamed", prefix)
		}
		// Scanning files is not the same as recognising spawns: if the AST
		// detection quietly stops matching (an aliased exec import, a helper
		// that hides the call), every file still parses and the policy passes
		// while checking nothing. Every package in scope spawns today, so no
		// hit at all here is a broken scan, not a package that became safe.
		if spawnsPerPrefix[prefix] == 0 {
			t.Errorf("the scan recognised no exec spawn anywhere under %s although it has "+
				"Windows-buildable files: the scan is broken, not the package (this check exists "+
				"so the policy cannot be disarmed silently)", prefix)
		}
	}
	for rel, entries := range consoleSpawnAllowlist {
		for idx, entry := range entries {
			if !seen[rel][idx] {
				t.Errorf("consoleSpawnAllowlist entry %s (func %q, snippet %q) no longer matches an "+
					"unsuppressed spawn; remove it", rel, entry.fn, entry.snippet)
			}
		}
	}
}

// TestConsoleSpawnScope pins the scope decision itself, since a scope that
// silently matches nothing disarms every check above.
func TestConsoleSpawnScope(t *testing.T) {
	root := sourceRepoRoot(t)

	cases := []struct {
		rel  string
		want bool // builds for Windows and is therefore in scope
	}{
		// Windows-building spawn sites: the policy applies.
		{"internal/doltserver/doltserver.go", true},
		{"internal/doltserver/doltserver_windows.go", true},
		{"internal/doltserver/gc_config.go", true},
		{"internal/doltversion/probe.go", true},
		{"internal/storage/dbproxy/server/doltserver.go", true},
		{"internal/storage/dbproxy/proxy/endpoint.go", true},
		// Platform files with no console to suppress.
		{"internal/doltserver/doltserver_unix.go", false},
		{"internal/doltserver/sweep_darwin.go", false},
		{"internal/doltserver/procattr_linux.go", false},
		{"internal/storage/dbproxy/proxy/endpoint_unix.go", false},
	}
	for _, tc := range cases {
		if !consoleSpawnInScope(tc.rel) {
			t.Errorf("%s: consoleSpawnInScope = false, want true", tc.rel)
			continue
		}
		dir, name := filepath.Split(tc.rel)
		got, err := windowsBuildContext.MatchFile(filepath.Join(root, filepath.FromSlash(dir)), name)
		if err != nil {
			t.Fatalf("evaluate build constraints for %s: %v", tc.rel, err)
		}
		if got != tc.want {
			t.Errorf("%s: builds for windows = %v, want %v", tc.rel, got, tc.want)
		}
	}

	// Out-of-scope shapes: test files and packages that spawn with a console
	// of their own are not this policy's business.
	for _, rel := range []string{
		"internal/doltserver/doltserver_test.go",
		"cmd/bd/dolt.go",
		"internal/storage/dolt/store.go",
	} {
		if consoleSpawnInScope(rel) {
			t.Errorf("%s: consoleSpawnInScope = true, want false", rel)
		}
	}
}

// TestConsoleSpawnPolicyScan pins the scan's accept/reject decisions on
// in-memory fixtures, so a regression in the analysis is caught without
// touching the real tree.
func TestConsoleSpawnPolicyScan(t *testing.T) {
	parse := func(t *testing.T, src string) (*token.FileSet, *ast.File, []string) {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "fixture.go", src, 0)
		if err != nil {
			t.Fatalf("parse fixture: %v", err)
		}
		return fset, f, strings.Split(src, "\n")
	}
	unsuppressed := func(hits []consoleSpawnHit) []consoleSpawnHit {
		var out []consoleSpawnHit
		for _, h := range hits {
			if !h.hidden && !h.pinned {
				out = append(out, h)
			}
		}
		return out
	}

	cases := []struct {
		name         string
		src          string
		wantUnsupprn int
		wantHidden   int
		wantPinned   int
	}{
		{
			name: "bare spawn with neither wrapper nor SysProcAttr is reported",
			src: "package p\n\nimport \"os/exec\"\n\n" +
				"func probe() {\n\tout, _ := exec.Command(\"dolt\", \"version\").Output()\n\t_ = out\n}\n",
			wantUnsupprn: 1,
		},
		{
			name: "execwin.Hide wrapper is accepted",
			src: "package p\n\nimport (\n\t\"os/exec\"\n\n\t\"github.com/steveyegge/beads/internal/execwin\"\n)\n\n" +
				"func probe() {\n\tout, _ := execwin.Hide(exec.Command(\"dolt\", \"version\")).Output()\n\t_ = out\n}\n",
			wantHidden: 1,
		},
		{
			name: "CommandContext wrapped in Hide is accepted, and the tool name is the one after ctx",
			src: "package p\n\nimport (\n\t\"context\"\n\t\"os/exec\"\n\n\t\"github.com/steveyegge/beads/internal/execwin\"\n)\n\n" +
				"func probe(ctx context.Context, path string) {\n\tcmd := execwin.Hide(exec.CommandContext(ctx, path, \"version\"))\n\t_ = cmd.Run()\n}\n",
			wantHidden: 1,
		},
		{
			name: "explicit SysProcAttr assignment in the enclosing function is accepted",
			src: "package p\n\nimport \"os/exec\"\n\n" +
				"func start() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n\t_ = cmd.Start()\n}\n",
			wantPinned: 1,
		},
		{
			name: "a non-literal tool name earns no exemption",
			src: "package p\n\nimport \"os/exec\"\n\n" +
				"func locate(bin string) {\n\tout, _ := exec.Command(bin, \"version\").Output()\n\t_ = out\n}\n",
			wantUnsupprn: 1,
		},
		{
			name: "SysProcAttr in a DIFFERENT function does not excuse the spawn",
			src: "package p\n\nimport \"os/exec\"\n\n" +
				"func configure(cmd *exec.Cmd) {\n\tcmd.SysProcAttr = procAttrHidden()\n}\n\n" +
				"func probe() {\n\tout, _ := exec.Command(\"dolt\", \"version\").Output()\n\t_ = out\n}\n",
			wantUnsupprn: 1,
			// configure holds no spawn, so no hit is pinned: the file's only
			// spawn is probe's, and it stays unsuppressed even though an
			// assignment exists one function away.
			wantPinned: 0,
		},
		{
			name: "a spawn inside a closure is judged against the closure",
			src: "package p\n\nimport \"os/exec\"\n\n" +
				"func outer() {\n\tcmd := exec.Command(\"dolt\", \"sql-server\")\n\tcmd.SysProcAttr = procAttrDetached()\n\tgo func() {\n\t\t_ = exec.Command(\"git\", \"config\", \"user.name\").Run()\n\t}()\n\t_ = cmd.Start()\n}\n",
			wantUnsupprn: 1,
			// outer's own spawn is pinned by the assignment above it; the
			// closure's spawn is judged against the closure, which pins
			// nothing, so it is the unsuppressed one. Both counts cover
			// every hit in the file.
			wantPinned: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset, f, lines := parse(t, tc.src)
			hits := findConsoleSpawns(fset, f, lines)
			got := map[string]int{"hidden": 0, "pinned": 0, "unsuppressed": 0}
			for _, h := range hits {
				switch {
				case h.hidden:
					got["hidden"]++
				case h.pinned:
					got["pinned"]++
				default:
					got["unsuppressed"]++
				}
			}
			// wantPinned counts hits that the scanner saw as pinned, including
			// any that are pinned but not the subject of the case; the cases
			// above are written so the counts are exact.
			if got["hidden"] != tc.wantHidden || got["pinned"] != tc.wantPinned || got["unsuppressed"] != tc.wantUnsupprn {
				t.Errorf("scan = %v (hidden=%d pinned=%d unsuppressed=%d over %d hit(s)); want hidden=%d pinned=%d unsuppressed=%d",
					got, got["hidden"], got["pinned"], got["unsuppressed"], len(hits), tc.wantHidden, tc.wantPinned, tc.wantUnsupprn)
				for _, h := range hits {
					t.Logf("  hit line %d %s(%q) hidden=%v pinned=%v func=%q", h.line, h.call, h.tool, h.hidden, h.pinned, h.funcName)
				}
			}
			if len(unsuppressed(hits)) != tc.wantUnsupprn {
				t.Errorf("unsuppressed = %d, want %d", len(unsuppressed(hits)), tc.wantUnsupprn)
			}
		})
	}
}

// TestConsoleSpawnAllowlistMatching pins that an entry matches by enclosing
// function plus exact source line, and that a stale entry (no longer matching)
// is detectable -- the repo-wide test fails on one.
func TestConsoleSpawnAllowlistMatching(t *testing.T) {
	src := "package p\n\nimport \"os/exec\"\n\n" +
		"func probe() {\n\tout, _ := exec.Command(\"dolt\", \"version\").Output()\n\t_ = out\n}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	hits := findConsoleSpawns(fset, f, strings.Split(src, "\n"))
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

	entries := []consoleSpawnAllowlistEntry{
		{fn: "probe", snippet: hit.snippet, why: "fixture"},
	}
	if _, ok := matchConsoleAllowlist(entries, hit); !ok {
		t.Errorf("entry keyed by func %q and snippet %q did not match", hit.funcName, hit.snippet)
	}
	if _, ok := matchConsoleAllowlist(entries, consoleSpawnHit{funcName: "other", snippet: hit.snippet}); ok {
		t.Errorf("entry matched a hit in a different function")
	}
	if _, ok := matchConsoleAllowlist(entries, consoleSpawnHit{funcName: hit.funcName, snippet: "something else"}); ok {
		t.Errorf("entry matched a hit with a different source line")
	}
}
