package scripts_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every Go test that deliberately skips itself under Bazel (a t.Skip guarded
// by TEST_SRCDIR or bazeltest.IsBazel(), directly or through a helper it
// calls) has a `skip` entry in tools/bazel/equivalence_allowlist.txt.
//
// The PR run of the Bazel lane cannot tell a test that skips under Bazel from
// one that runs: only nightly.yml's skip-parity check (equivalence.py
// --go-test-json) can, after the merge, and a missing entry turns that
// nightly red (as TestBazelGatedLanesNeverRetryFlakyTests and
// TestCheckShardCoverageScript did on 2026-10-02). The entries are also what
// keeps those tests running under go test where the Bazel lane replaces a
// go test job. This check finds them before the merge, from the source.
//
// Only top-level tests are considered: a subtest's skip is invisible to
// equivalence.py, which compares top-level tests.
func TestBazelOnlySkipsAreAllowlisted(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("walks every _test.go file in the source checkout; runs under go test")
	}
	root := sourceRepoRoot(t)
	entries := readSkipAllowlist(t, filepath.Join(root, "tools", "bazel", "equivalence_allowlist.txt"))

	found, err := bazelSkippingTests(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 8 {
		t.Fatalf("found only %d Bazel-skipping tests (%v); the scan is broken", len(found), found)
	}
	for _, f := range found {
		if !skipAllowlisted(entries, f.pkg, f.test) {
			t.Errorf("%s %s skips under Bazel (%s) but tools/bazel/equivalence_allowlist.txt has no `%s %s skip  # why` entry",
				f.pkg, f.test, f.where, f.pkg, f.test)
		}
	}
}

// The scan itself, on fixtures: direct guards of either form, a skip helper,
// and the shapes it must not report.
func TestBazelSkippingTestsScan(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, src string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n")
	write("a/a_test.go", `package a

import (
	"os"
	"testing"

	"example.com/m/bazeltest"
)

func skipUnderBazel(t *testing.T) {
	if bazeltest.IsBazel() {
		t.Skip("no")
	}
}

func TestEnvGuard(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") != "" {
		t.Skip("runfiles")
	}
}

func TestIsBazelGuard(t *testing.T) {
	if bazeltest.IsBazel() {
		t.Skipf("%s", "x")
	}
}

func TestViaHelper(t *testing.T) {
	skipUnderBazel(t)
}

func TestLaterGuard(t *testing.T) {
	_ = 1
	if srcdir := os.Getenv("TEST_SRCDIR"); srcdir != "" {
		t.SkipNow()
	}
}

func TestGoTestOnlySkip(t *testing.T) {
	if os.Getenv("TEST_SRCDIR") == "" {
		t.Skip("only under go test")
	}
}

func TestNotBazel(t *testing.T) {
	if !bazeltest.IsBazel() {
		t.Skip("go test only")
	}
}

func TestSubtestOnly(t *testing.T) {
	t.Run("x", func(t *testing.T) {
		if bazeltest.IsBazel() {
			t.Skip("subtest")
		}
	})
}

func TestNoSkip(t *testing.T) {
	if bazeltest.IsBazel() {
		return
	}
}

func helperTakingM(m *testing.M) {}
`)
	write("node_modules/x/x_test.go", "package x\nimport \"testing\"\nfunc TestIgnored(t *testing.T) { if bazeltest.IsBazel() { t.Skip() } }\n")
	write("sub/go.mod", "module example.com/sub\n")
	write("sub/s_test.go", "package s\nimport \"testing\"\nfunc TestOtherModule(t *testing.T) { if bazeltest.IsBazel() { t.Skip() } }\n")

	found, err := bazelSkippingTests(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range found {
		got = append(got, f.pkg+" "+f.test)
	}
	sort.Strings(got)
	want := []string{"a TestEnvGuard", "a TestIsBazelGuard", "a TestLaterGuard", "a TestViaHelper"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("scan found %v, want %v", got, want)
	}

	entries := []skipEntry{{"a", "TestEnv*"}, {"a", "TestIsBazelGuard"}}
	for test, want := range map[string]bool{"TestEnvGuard": true, "TestIsBazelGuard": true, "TestViaHelper": false} {
		if got := skipAllowlisted(entries, "a", test); got != want {
			t.Errorf("skipAllowlisted(a, %s) = %v, want %v", test, got, want)
		}
	}
}

type bazelSkip struct{ pkg, test, where string }

type skipEntry struct{ pkg, test string }

// readSkipAllowlist: the allowlist's `skip` entries, in equivalence.py's
// format (<package dir> <test glob> skip  # why).
func readSkipAllowlist(t *testing.T, file string) []skipEntry {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []skipEntry
	for _, line := range strings.Split(string(data), "\n") {
		body, _, _ := strings.Cut(line, "#")
		f := strings.Fields(body)
		if len(f) == 3 && f[2] == "skip" {
			out = append(out, skipEntry{f[0], f[1]})
		}
	}
	return out
}

// skipAllowlisted matches like equivalence.py's allowed() (fnmatch, which
// path.Match equals for these names).
func skipAllowlisted(entries []skipEntry, pkg, test string) bool {
	for _, e := range entries {
		pm, _ := path.Match(e.pkg, pkg)
		tm, _ := path.Match(e.test, test)
		if pm && tm {
			return true
		}
	}
	return false
}

var bazelGuardTestFunc = regexp.MustCompile(`^Test([^a-z].*)?$`)

// bazelSkippingTests parses every _test.go file of the module rooted at
// root (not nested modules, node_modules, .git, .beads or bazel-* output
// trees) and returns the top-level tests that skip under Bazel.
func bazelSkippingTests(root string) ([]bazelSkip, error) {
	byDir := map[string][]*ast.File{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == ".git" || name == "node_modules" || name == ".beads" || strings.HasPrefix(name, "bazel-")) {
				return filepath.SkipDir
			}
			if p != root {
				if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		byDir[filepath.Dir(p)] = append(byDir[filepath.Dir(p)], f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []bazelSkip
	for dir, files := range byDir {
		rel, _ := filepath.Rel(root, dir)
		pkg := filepath.ToSlash(rel)
		// Package-level helpers (per package name: _test packages share the
		// directory) that skip under Bazel.
		helpers := map[string]bool{}
		for _, f := range files {
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil && hasBazelSkip(fn.Body) {
					helpers[f.Name.Name+"."+fn.Name.Name] = true
				}
			}
		}
		for _, f := range files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Body == nil || !bazelGuardTestFunc.MatchString(fn.Name.Name) || !takesTestingT(fn) {
					continue
				}
				where := ""
				if hasBazelSkip(fn.Body) {
					where = "guarded t.Skip"
				} else if h := calledHelper(fn.Body, f.Name.Name, helpers); h != "" {
					where = "via " + h
				}
				if where != "" {
					out = append(out, bazelSkip{pkg, fn.Name.Name, where})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pkg+" "+out[i].test < out[j].pkg+" "+out[j].test })
	return out, nil
}

func takesTestingT(fn *ast.FuncDecl) bool {
	params := fn.Type.Params.List
	if len(params) != 1 {
		return false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "T"
}

// hasBazelSkip: an if statement outside any closure whose condition is
// true under Bazel (TEST_SRCDIR non-empty, or bazeltest.IsBazel()) and
// whose body calls a Skip method.
func hasBazelSkip(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.IfStmt:
			if bazelCond(n.Cond) && callsSkip(n.Body) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func bazelCond(cond ast.Expr) bool {
	switch c := cond.(type) {
	case *ast.ParenExpr:
		return bazelCond(c.X)
	case *ast.CallExpr:
		sel, ok := c.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "IsBazel"
	case *ast.BinaryExpr:
		if c.Op == token.LAND {
			return bazelCond(c.X) || bazelCond(c.Y)
		}
		if c.Op != token.NEQ {
			return false
		}
		lit, ok := c.Y.(*ast.BasicLit)
		if !ok || lit.Value != `""` {
			return false
		}
		return mentionsTestSrcdir(c.X)
	}
	return false
}

// mentionsTestSrcdir: os.Getenv("TEST_SRCDIR") itself, or a variable (the
// `if srcdir := os.Getenv("TEST_SRCDIR"); srcdir != ""` form; such a
// variable is only ever bound that way in these tests).
func mentionsTestSrcdir(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.CallExpr:
		for _, a := range x.Args {
			if lit, ok := a.(*ast.BasicLit); ok && lit.Value == `"TEST_SRCDIR"` {
				return true
			}
		}
	case *ast.Ident:
		if x.Obj != nil {
			if as, ok := x.Obj.Decl.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
				return mentionsTestSrcdir(as.Rhs[0])
			}
		}
	}
	return false
}

func callsSkip(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Skip" || sel.Sel.Name == "Skipf" || sel.Sel.Name == "SkipNow") {
				found = true
			}
		}
		return !found
	})
	return found
}

// calledHelper: a skip helper of the same package that the test calls
// outside any closure.
func calledHelper(body *ast.BlockStmt, pkgName string, helpers map[string]bool) string {
	name := ""
	ast.Inspect(body, func(n ast.Node) bool {
		if name != "" {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && helpers[pkgName+"."+id.Name] {
				name = id.Name
			}
		}
		return true
	})
	return name
}
