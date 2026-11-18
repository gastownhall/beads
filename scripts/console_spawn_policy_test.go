package scripts_test

// Policy: no spawn from a console-less bd may open a console window on
// Windows (//scripts:go_sources_test, which declares //:repo_go_srcs and
// //:repo_go_test_srcs, so a non-Go change leaves it cached).
//
// Background (gastownhall/beads#6076). bd runs children from contexts that
// have no console to hand down: an agent harness, daemon or CI step
// redirects bd's stdout, and the db-proxy child is DETACHED_PROCESS. Windows
// then allocates a fresh, visible console for every console-subsystem child,
// and with Windows Terminal registered as the default terminal a short-lived
// child such as `dolt init` can fail with 0x800700E8
// (HRESULT_FROM_WIN32(ERROR_NO_DATA)) before the terminal handoff completes.
//
// Scope: the non-test .go files under internal/doltserver, internal/doltversion
// and internal/storage/dbproxy/... that go/build selects for GOOS=windows
// (build.Context.MatchFile, never a tag reimplementation: doltserver_unix.go,
// listenerowner_darwin.go and friends drop out by the same machinery the
// compiler uses). Every exec.Command / exec.CommandContext call, and every
// exec.Cmd composite literal, is a recognised spawn there — whatever name the
// file imports os/exec under.
//
// A spawn passes when:
//   - it is the direct argument of execwin.Hide(...) (matched by import
//     path, so aliasing cannot smuggle a spawn past the rule), including the
//     `execwin.Hide(exec.Command(...)).Output()` form; or
//   - it is bound to a variable v, and an unconditional `execwin.Hide(v)`
//     statement follows in the same block, after every `v.SysProcAttr = ...`
//     assignment and before any statement that starts v or passes it on.
//
// There is deliberately no "the function assigns SysProcAttr" exemption: at
// base the shared dolt sql-server spawn assigned SysProcAttr with only
// CREATE_NEW_PROCESS_GROUP and still gave a console-less bd a visible
// console window. Assigning SysProcAttr is not evidence that CREATE_NO_WINDOW
// is among the flags.
//
// The rule is name-agnostic: no tool name earns an exemption, because the
// defect is where a spawn runs, not what it runs. The one justified exception
// is consoleSpawnAllowlist below, keyed by file + enclosing function + exact
// source-line snippet rather than a line number (the hostToolExecAllowlist
// precedent, so an entry survives unrelated edits that shift lines).
//
// Anti-disarm: every package in consoleSpawnScopePackages must contribute at
// least one Windows-buildable file and at least one recognised spawn, and
// every allowlist entry must still match a live unhidden spawn, so neither
// the scope nor the allowlist can be hollowed out silently.

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// execwinImportPath is the import path of the hide helper this policy
// recognizes. Matched by path, never by the local name a file binds it to.
const execwinImportPath = "github.com/steveyegge/beads/internal/execwin"

// consoleSpawnScanRoots lists the directory roots whose non-test .go files
// are scanned (recursively). These are the packages that spawn children from
// a console-less context: the shared dolt sql-server lifecycle, the dolt
// version probes, and the db-proxy child with its managed server.
var consoleSpawnScanRoots = []string{
	"internal/doltserver",
	"internal/doltversion",
	"internal/storage/dbproxy",
}

// consoleSpawnScopePackages lists the packages that must each stay non-vacuous
// in the scan (see the package comment's anti-disarm rules). Every package
// known to spawn is listed, so deleting its spawns (or its Windows-buildable
// files) fails the guard instead of quietly narrowing what it enforces.
var consoleSpawnScopePackages = []string{
	"internal/doltserver",
	"internal/doltversion",
	"internal/storage/dbproxy/proxy",
	"internal/storage/dbproxy/server",
}

// consoleSpawnAllowlistEntry is one justified exception: the enclosing
// function name (empty for a spawn no top-level function owns) and the exact
// trimmed source line, with the reason the spawn needs no execwin.Hide.
type consoleSpawnAllowlistEntry struct {
	fn      string
	snippet string
	why     string
}

// consoleSpawnAllowlist is file (repo-relative, slash-separated) -> entries.
// The db-proxy child is the one spawn that must NOT be hidden: it is started
// DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP (proxy/endpoint_windows.go
// procAttrDetached), a detached process has no console for Windows to
// allocate a window from, and CREATE_NO_WINDOW is ignored next to
// DETACHED_PROCESS — while every console child the proxy child itself starts
// (dolt config/init, its managed sql-server) is inside this scan's scope and
// hidden. See execwin's package doc for the trap.
var consoleSpawnAllowlist = map[string][]consoleSpawnAllowlistEntry{
	"internal/storage/dbproxy/proxy/endpoint.go": {
		{
			fn:      "forkExecChild",
			snippet: `cmd := exec.Command(self, args...)`,
			why:     "DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP: no console to allocate a window from, CREATE_NO_WINDOW is ignored next to DETACHED_PROCESS, and its console children are in scope and hidden",
		},
	},
}

// consoleSpawnHit is one spawn this scan found, hidden or not.
type consoleSpawnHit struct {
	line    int
	kind    string // "exec.Command", "exec.CommandContext", "exec.Cmd literal"
	fn      string // enclosing top-level function's name, or ""
	snippet string // trimmed source text of the hit's line, for allowlist matching
	hidden  bool
	reason  string // why it is not hidden, when it is not
}

// consoleSpawnInput is one candidate file: repo-relative path, an on-disk
// path for build.Context.MatchFile (which reads build constraints itself),
// and the source bytes.
type consoleSpawnInput struct {
	rel string
	abs string
	src []byte
}

// windowsBuildContext returns a build.Context that selects files the way
// bd's Windows build does: GOOS=windows, pure Go (the gms_pure_go build).
func windowsBuildContext() *build.Context {
	ctx := build.Default // copy
	ctx.GOOS = "windows"
	ctx.CgoEnabled = false
	return &ctx
}

// consoleSpawnInScope reports whether rel (repo-relative, slash-separated)
// sits under one of consoleSpawnScanRoots.
func consoleSpawnInScope(rel string) bool {
	for _, root := range consoleSpawnScanRoots {
		if strings.HasPrefix(rel, root+"/") {
			return true
		}
	}
	return false
}

// scopePackageOf returns the consoleSpawnScopePackages entry rel belongs to,
// or "" when rel is under a scan root but not under a scope package (its
// files are still scanned; they just do not count toward any package's
// anti-disarm quota).
func scopePackageOf(rel string) string {
	for _, pkg := range consoleSpawnScopePackages {
		if strings.HasPrefix(rel, pkg+"/") {
			return pkg
		}
	}
	return ""
}

// localImportName resolves the name a file's code uses for an import path:
// the alias if there is one, "." for a dot import, the path's last element
// otherwise. blank reports an "_" import (imported, never referenced), and
// imported is false when the path is not imported at all.
func localImportName(f *ast.File, path string) (name string, dot, imported bool) {
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil || p != path {
			continue
		}
		if spec.Name == nil {
			base := path
			if i := strings.LastIndexByte(base, '/'); i >= 0 {
				base = base[i+1:]
			}
			return base, false, true
		}
		switch spec.Name.Name {
		case "_":
			return "", false, true
		case ".":
			return "", true, true
		default:
			return spec.Name.Name, false, true
		}
	}
	return "", false, false
}

// consoleSpawnFileScan carries the per-file state the scan needs: how os/exec
// and internal/execwin are named in this file, the source ranges of
// execwin.Hide(...) arguments (a spawn inside one is wrapped), and the hits.
type consoleSpawnFileScan struct {
	fset  *token.FileSet
	file  *ast.File
	lines []string

	execName        string // local name of os/exec
	execDot         bool
	execImported    bool
	execwinName     string // local name of internal/execwin
	execwinDot      bool
	execwinImported bool

	hideCover []token.Pos // pairs: lo, hi per Hide argument
	hits      []*consoleSpawnHit
	byPos     map[token.Pos]*consoleSpawnHit
	funcDecls []funcRange
}

type funcRange struct {
	name   string
	lo, hi token.Pos
}

func scanConsoleSpawnSource(rel string, src []byte) ([]consoleSpawnHit, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, err
	}
	s := &consoleSpawnFileScan{
		fset:  fset,
		file:  file,
		lines: strings.Split(string(src), "\n"),
		byPos: map[token.Pos]*consoleSpawnHit{},
	}
	s.execName, s.execDot, s.execImported = localImportName(file, "os/exec")
	s.execwinName, s.execwinDot, s.execwinImported = localImportName(file, execwinImportPath)
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
			s.funcDecls = append(s.funcDecls, funcRange{name: fn.Name.Name, lo: fn.Pos(), hi: fn.End()})
		}
	}
	s.collectHideCover()
	s.collectSpawns()
	s.trackBindings()
	hits := make([]consoleSpawnHit, 0, len(s.hits))
	for _, h := range s.hits {
		hits = append(hits, *h)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].line < hits[j].line })
	return hits, nil
}

// isHideCall reports whether call is this file's execwin.Hide(...).
func (s *consoleSpawnFileScan) isHideCall(call *ast.CallExpr) bool {
	if !s.execwinImported || len(call.Args) != 1 {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		id, ok := fn.X.(*ast.Ident)
		return ok && !s.execwinDot && fn.Sel.Name == "Hide" && id.Name == s.execwinName
	case *ast.Ident:
		return s.execwinDot && fn.Name == "Hide"
	}
	return false
}

// collectHideCover records the source ranges of execwin.Hide arguments: a
// spawn lexically inside one is wrapped.
func (s *consoleSpawnFileScan) collectHideCover() {
	ast.Inspect(s.file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !s.isHideCall(call) {
			return true
		}
		s.hideCover = append(s.hideCover, call.Args[0].Pos(), call.Args[0].End())
		return true
	})
}

func (s *consoleSpawnFileScan) inHideCover(pos token.Pos) bool {
	for i := 0; i+1 < len(s.hideCover); i += 2 {
		if s.hideCover[i] <= pos && pos < s.hideCover[i+1] {
			return true
		}
	}
	return false
}

// spawnKind reports whether call constructs an *exec.Cmd through this file's
// os/exec name, and with which kind label. Alias-aware: only calls through
// the name os/exec is actually bound to count, so an unrelated package
// imported as "exec" is not misread.
func (s *consoleSpawnFileScan) spawnKind(call *ast.CallExpr) (string, bool) {
	var sel string
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		if !s.execImported || s.execDot {
			return "", false
		}
		id, ok := fn.X.(*ast.Ident)
		if !ok || id.Name != s.execName {
			return "", false
		}
		sel = fn.Sel.Name
	case *ast.Ident:
		if !s.execImported || !s.execDot {
			return "", false
		}
		sel = fn.Name
	default:
		return "", false
	}
	switch sel {
	case "Command":
		return "exec.Command", true
	case "CommandContext":
		return "exec.CommandContext", true
	}
	return "", false
}

// cmdLiteralKind reports whether lit is an exec.Cmd composite literal through
// this file's os/exec name.
func (s *consoleSpawnFileScan) cmdLiteralKind(lit *ast.CompositeLit) (string, bool) {
	switch t := lit.Type.(type) {
	case *ast.SelectorExpr:
		if !s.execImported || s.execDot || t.Sel.Name != "Cmd" {
			return "", false
		}
		if id, ok := t.X.(*ast.Ident); ok && id.Name == s.execName {
			return "exec.Cmd literal", true
		}
	case *ast.Ident:
		if s.execImported && s.execDot && t.Name == "Cmd" {
			return "exec.Cmd literal", true
		}
	}
	return "", false
}

func (s *consoleSpawnFileScan) enclosingTopLevelFunc(pos token.Pos) string {
	for _, fr := range s.funcDecls {
		if fr.lo <= pos && pos <= fr.hi {
			return fr.name
		}
	}
	return ""
}

func (s *consoleSpawnFileScan) addHit(pos token.Pos, kind string) *consoleSpawnHit {
	line := s.fset.Position(pos).Line
	hit := &consoleSpawnHit{
		line:    line,
		kind:    kind,
		fn:      s.enclosingTopLevelFunc(pos),
		snippet: snippetForLine(s.lines, line),
		hidden:  s.inHideCover(pos),
		reason:  "no execwin.Hide anywhere: a console-less parent (redirected stdout, detached db-proxy child) makes Windows allocate a visible console window for it",
	}
	s.hits = append(s.hits, hit)
	s.byPos[pos] = hit
	return hit
}

func snippetForLine(lines []string, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}

// collectSpawns finds every spawn call and Cmd literal in the file, marking
// direct execwin.Hide wraps as hidden.
func (s *consoleSpawnFileScan) collectSpawns() {
	ast.Inspect(s.file, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.CallExpr:
			if kind, ok := s.spawnKind(e); ok {
				s.addHit(e.Pos(), kind)
			}
		case *ast.CompositeLit:
			if kind, ok := s.cmdLiteralKind(e); ok {
				s.addHit(e.Pos(), kind)
			}
		}
		return true
	})
}

// trackBindings walks every block's direct statement list, following each
// spawn bound to a variable v: an unconditional `execwin.Hide(v)` statement
// after every `v.SysProcAttr = ...` assignment and before v is started or
// passed on hides it.
func (s *consoleSpawnFileScan) trackBindings() {
	ast.Inspect(s.file, func(n ast.Node) bool {
		if b, ok := n.(*ast.BlockStmt); ok {
			s.scanBlockStatements(b)
		}
		return true
	})
}

// boundSpawn is the per-block tracking state of one spawn bound to a
// variable.
type boundSpawn struct {
	hit    *consoleSpawnHit
	hidden bool
	done   bool // referenced after the hide (or escaped): stop updating
}

func (s *consoleSpawnFileScan) scanBlockStatements(b *ast.BlockStmt) {
	vars := map[string]*boundSpawn{}
	for _, stmt := range b.List {
		boundHere := map[string]bool{}
		// Bindings: `v := <spawn>` (or `v = <spawn>`). A spawn wrapped at
		// construction was already marked hidden by collectSpawns.
		if assign, ok := stmt.(*ast.AssignStmt); ok && len(assign.Lhs) == len(assign.Rhs) {
			for i, lhs := range assign.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name == "_" {
					continue
				}
				call, ok := assign.Rhs[i].(*ast.CallExpr)
				if !ok {
					continue
				}
				if _, is := s.spawnKind(call); is && !s.inHideCover(call.Pos()) {
					if hit := s.byPos[call.Pos()]; hit != nil {
						vars[id.Name] = &boundSpawn{hit: hit}
						boundHere[id.Name] = true
					}
				}
			}
		}
		for name, st := range vars {
			if boundHere[name] || st.done {
				continue
			}
			s.applyStatement(name, st, stmt)
		}
	}
}

// applyStatement updates one bound spawn's tracking for stmt, which is a
// direct statement of the block after the binding.
func (s *consoleSpawnFileScan) applyStatement(v string, st *boundSpawn, stmt ast.Stmt) {
	// The one recognized hide: an unconditional expression statement.
	if es, ok := stmt.(*ast.ExprStmt); ok {
		if call, ok := es.X.(*ast.CallExpr); ok && s.isHideCall(call) {
			if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == v {
				st.hidden = true
				st.hit.hidden = true
				st.hit.reason = ""
				return
			}
		}
	}
	// `v.Field = ...` configures the command; it is not an escape. But a
	// SysProcAttr assignment AFTER the hide replaces the SysProcAttr the
	// hide mutated, dropping CREATE_NO_WINDOW.
	if assign, ok := stmt.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 {
		if sel, ok := assign.Lhs[0].(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == v {
				if sel.Sel.Name == "SysProcAttr" && st.hidden {
					st.hidden = false
					st.hit.hidden = false
					st.hit.reason = "SysProcAttr is assigned after execwin.Hide(" + v + "), replacing the attr the hide marked CREATE_NO_WINDOW"
				}
				return
			}
		}
	}
	// Any other mention of v starts it or passes it on.
	if identReferences(stmt, v) {
		if st.hidden {
			st.done = true // running or passing on a hidden command is fine
			return
		}
		if s.containsHideOf(stmt, v) {
			st.hit.reason = "execwin.Hide(" + v + ") appears only inside a nested or conditional statement, not unconditionally in this block"
		} else {
			st.hit.reason = v + " is started or passed on before any execwin.Hide(" + v + ")"
		}
		st.done = true
	}
}

// containsHideOf reports whether stmt contains an execwin.Hide(v) call
// anywhere inside it (nested, conditional — the shapes that do not count).
func (s *consoleSpawnFileScan) containsHideOf(stmt ast.Node, v string) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || !s.isHideCall(call) {
			return true
		}
		if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == v {
			found = true
		}
		return true
	})
	return found
}

// identReferences reports whether the identifier v is referenced by n,
// ignoring selector names (`x.v` is a field of x, not a use of v).
func identReferences(n ast.Node, v string) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		if found {
			return false
		}
		switch e := node.(type) {
		case *ast.SelectorExpr:
			if identReferences(e.X, v) {
				found = true
			}
			return false // do not read Sel as an identifier use
		case *ast.Ident:
			if e.Name == v {
				found = true
			}
		}
		return !found
	})
	return found
}

// matchConsoleSpawnEntry finds the allowlist entry (if any) matching hit by
// enclosing function name and exact source-line snippet, returning its index
// for usage tracking.
func matchConsoleSpawnEntry(entries []consoleSpawnAllowlistEntry, hit consoleSpawnHit) (int, bool) {
	for i, e := range entries {
		if e.fn == hit.fn && e.snippet == hit.snippet {
			return i, true
		}
	}
	return 0, false
}

// checkConsoleSpawnPolicy runs the policy over inputs and returns every
// finding: unhidden unallowlisted spawns (file:line, with the failed
// condition), scope packages that went vacuous, and allowlist entries that no
// longer match a live spawn.
func checkConsoleSpawnPolicy(t *testing.T, allowlist map[string][]consoleSpawnAllowlistEntry, inputs []consoleSpawnInput) []string {
	t.Helper()
	bctx := windowsBuildContext()
	scannedFiles := map[string]int{}
	spawns := map[string]int{}
	matchedEntry := map[string]map[int]bool{}
	var findings []string
	for _, in := range inputs {
		if !consoleSpawnInScope(in.rel) || !strings.HasSuffix(in.rel, ".go") || strings.HasSuffix(in.rel, "_test.go") {
			continue
		}
		pkg := scopePackageOf(in.rel)
		buildable, err := bctx.MatchFile(filepath.Dir(in.abs), filepath.Base(in.abs))
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: cannot decide Windows-buildability (%v); fix the build tags", in.rel, err))
			continue
		}
		if !buildable {
			continue
		}
		if pkg != "" {
			scannedFiles[pkg]++
		}
		hits, err := scanConsoleSpawnSource(in.rel, in.src)
		if err != nil {
			findings = append(findings, fmt.Sprintf("%s: parse failure hides its spawns from this scan: %v", in.rel, err))
			continue
		}
		for _, h := range hits {
			if pkg != "" {
				spawns[pkg]++
			}
			if h.hidden {
				continue
			}
			if idx, ok := matchConsoleSpawnEntry(allowlist[in.rel], h); ok {
				if matchedEntry[in.rel] == nil {
					matchedEntry[in.rel] = map[int]bool{}
				}
				matchedEntry[in.rel][idx] = true
				continue
			}
			findings = append(findings, fmt.Sprintf(
				"%s:%d: %s in %s is not hidden: %s — wrap the constructor in execwin.Hide (import %s), or assign SysProcAttr first and then call execwin.Hide(cmd) before starting it",
				in.rel, h.line, h.kind, h.fn, h.reason, execwinImportPath))
		}
	}
	for _, pkg := range consoleSpawnScopePackages {
		if scannedFiles[pkg] == 0 {
			findings = append(findings, fmt.Sprintf(
				"%s: contributes no Windows-buildable non-test Go file to the console-spawn scan; the scope would be vacuous — fix the walk or widen the scope", pkg))
		}
		if spawns[pkg] == 0 {
			findings = append(findings, fmt.Sprintf(
				"%s: contributes no recognised spawn to the console-spawn scan; the rule would enforce nothing there — investigate before touching the scope", pkg))
		}
	}
	rels := make([]string, 0, len(allowlist))
	for rel := range allowlist {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		for idx, e := range allowlist[rel] {
			if !matchedEntry[rel][idx] {
				findings = append(findings, fmt.Sprintf(
					"consoleSpawnAllowlist entry for %s (func %q, snippet %q) matches no live unhidden spawn; update or remove it", rel, e.fn, e.snippet))
			}
		}
	}
	sort.Strings(findings)
	return findings
}

// TestConsoleSpawnPolicy scans the real tree: the non-test Go files of the
// packages that spawn from a console-less context, selected for Windows the
// way bd's Windows build selects them, and fails on any spawn that is neither
// hidden nor allowlisted. See the package comment.
func TestConsoleSpawnPolicy(t *testing.T) {
	root := sourceRepoRoot(t)
	var inputs []consoleSpawnInput
	for _, rel := range repoFiles(t, root, 1000) {
		if !consoleSpawnInScope(rel) {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		src, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		inputs = append(inputs, consoleSpawnInput{rel: rel, abs: abs, src: src})
	}
	if len(inputs) < 10 {
		t.Fatalf("found only %d in-scope files under %s; the walk is broken", len(inputs), root)
	}
	for _, f := range checkConsoleSpawnPolicy(t, consoleSpawnAllowlist, inputs) {
		t.Error(f)
	}
}
