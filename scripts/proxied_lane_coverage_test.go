package scripts_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// proxiedServerEnv is the variable only //cmd/bd:bd_proxied_test sets: a
// cmd/bd test that skips without it runs in that lane or in none.
const proxiedServerEnv = "BEADS_TEST_PROXIED_SERVER"

// TestProxiedGatedTestsRunInTheProxiedLane pins ga-vnycm2.43: every top-level
// cmd/bd test that skips unless BEADS_TEST_PROXIED_SERVER=1 — directly, or
// through any chain of helpers (requireSharedProxiedServer,
// requireProxiedServerEnv, newSharedProxiedProject, ...) — must be selected
// by .github/scripts/proxied-test-shard.sh, the proxied lane's only test
// selection. The script discovers tests by name prefix
// (TestProxiedServer*/TestServerMode*) alone, so a gated test named otherwise
// skips in every CI lane and its pass means nothing: seven did, until they
// were renamed into the prefix (TestProxiedIfRevision* and
// TestProxiedDeleteIfRevisionSingleWinner became TestProxiedServer*).
//
// The gate is recognized structurally: an `if os.Getenv("BEADS_TEST_PROXIED_SERVER")
// != "1" { ... }` statement. A compound condition (one that also accepts a
// second lane's variable, e.g. the managed-local lane's) is not a proxied-only
// gate and is not followed.
func TestProxiedGatedTestsRunInTheProxiedLane(t *testing.T) {
	requireAutofixBash(t)
	root := sourceRepoRoot(t)

	gated, err := gatedTests(filepath.Join(root, "cmd/bd"), proxiedServerEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(gated) < 50 {
		t.Fatalf("found only %d cmd/bd tests gated on %s; did the gate helpers change shape?", len(gated), proxiedServerEnv)
	}

	listed, err := laneListedTests(root, ".github/scripts/proxied-test-shard.sh", bazelProxiedShardCount(t))
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for _, name := range gated {
		if !listed[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d cmd/bd test(s) skip unless %s=1 but .github/scripts/proxied-test-shard.sh never selects them, so no CI lane runs them; widen its discovery (and gen_proxied_shard_manifest.py's) or rename the test:\n  %s",
			len(missing), proxiedServerEnv, strings.Join(missing, "\n  "))
	}
}

// TestProxiedGatedTestsDetector pins the call-graph walk above against a
// fixture, so a regression in the detector cannot silently turn the guard
// into a no-op.
func TestProxiedGatedTestsDetector(t *testing.T) {
	dir := t.TempDir()
	// Every fixture line is indented one tab, so repo scanners that find
	// tests by a line-anchored `^func TestX(t *testing.T)` (equivalence.py)
	// do not mistake the fixture's functions for this package's tests.
	const src = `package main

	import (
		"os"
		"testing"
	)

	func requireGate(t *testing.T) {
		if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" {
			t.Skip("gated")
		}
	}

	func newProject(t *testing.T) { requireGate(t) }

	func requireEither(t *testing.T) {
		if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" && os.Getenv("OTHER") != "1" {
			t.Skip("either lane")
		}
	}

	type harness struct{}

	func (harness) start(t *testing.T) { newProject(t) }

	func TestDirect(t *testing.T)     { requireGate(t) }
	func TestTransitive(t *testing.T) { newProject(t) }
	func TestMethod(t *testing.T)     { harness{}.start(t) }
	func TestInSubtest(t *testing.T) {
		t.Run("x", func(t *testing.T) { newProject(t) })
	}
	func TestInline(t *testing.T) {
		if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" {
			t.Skip("gated")
		}
	}
	func TestEitherLane(t *testing.T) { requireEither(t) }
	func TestUngated(t *testing.T)    {}
`
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := gatedTests(dir, proxiedServerEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"TestDirect", "TestInSubtest", "TestInline", "TestMethod", "TestTransitive"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("gatedTests = %v, want %v", got, want)
	}
}

// TestLaneListedTestsReportsShardScriptStderr pins that a shard script's
// refusal reaches the lane guards' failure. The scripts explain every refusal
// (a manifest entry naming no test, an invalid shard) only on stderr and exit
// 1, so a failure that reported the exit status alone would not say what to
// fix.
func TestLaneListedTestsReportsShardScriptStderr(t *testing.T) {
	requireHostTool(t, "bash")
	root := t.TempDir()
	const refusal = "Shard manifest entry does not match a discovered test: TestRenamedAway"
	script := "echo \"Shard $1/$2: running 1 test(s)\"\n" +
		"echo '  TestListed'\n" +
		"if [ \"$1\" = 2 ]; then echo '" + refusal + "' >&2; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(root, "shard.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := laneListedTests(root, "shard.sh", 2)
	if err == nil {
		t.Fatal("laneListedTests: want an error for the shard the script refused, got nil")
	}
	// The shard and the refusal are checked apart: bash can write to stderr
	// before the script runs (a setlocale warning for an LC_ALL naming an
	// uninstalled locale, or output from a BASH_ENV file), and that lands
	// between the exit status and the refusal. The refusal is only on the
	// script's stderr, never in the stdout the error also carries.
	if !strings.Contains(err.Error(), "shard.sh 2 2: exit status 1") {
		t.Errorf("laneListedTests error does not name the refusing shard and its exit status:\n%v", err)
	}
	if !strings.Contains(err.Error(), refusal) {
		t.Errorf("laneListedTests error does not carry the script's stderr %q:\n%v", refusal, err)
	}
}

// gatedTests returns, sorted, the top-level `func TestX(t *testing.T)`
// declarations in dir's _test.go files that reach an env-only gate (see
// isOnlyGate) through calls (functions and methods alike, by name, closures included). Matching
// callees by bare name over-approximates, which errs toward demanding the
// lane run a test rather than missing one.
func gatedTests(dir, env string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	calls := map[string]map[string]bool{}
	gated := map[string]bool{}
	var tests []string
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if fn.Recv == nil && strings.HasPrefix(name, "Test") && takesTestingT(fn) {
				tests = append(tests, name)
			}
			if calls[name] == nil {
				calls[name] = map[string]bool{}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.IfStmt:
					if isOnlyGate(n.Cond, env) {
						gated[name] = true
					}
				case *ast.CallExpr:
					switch fun := n.Fun.(type) {
					case *ast.Ident:
						calls[name][fun.Name] = true
					case *ast.SelectorExpr:
						calls[name][fun.Sel.Name] = true
					}
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for caller, callees := range calls {
			if gated[caller] {
				continue
			}
			for callee := range callees {
				if gated[callee] {
					gated[caller] = true
					changed = true
					break
				}
			}
		}
	}
	var out []string
	for _, name := range tests {
		if gated[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// isOnlyGate reports whether cond is exactly os.Getenv(env) != "1", where env
// is a lane's variable (BEADS_TEST_PROXIED_SERVER, BEADS_TEST_EMBEDDED_DOLT).
func isOnlyGate(cond ast.Expr, env string) bool {
	be, ok := cond.(*ast.BinaryExpr)
	if !ok || be.Op != token.NEQ {
		return false
	}
	if lit, ok := be.Y.(*ast.BasicLit); !ok || lit.Value != `"1"` {
		return false
	}
	call, ok := be.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Getenv" {
		return false
	}
	arg, ok := call.Args[0].(*ast.BasicLit)
	return ok && arg.Value == strconv.Quote(env)
}

// laneListedTests runs a lane's shard script list-only for every shard of the
// Bazel lane's split and returns every test name any shard selects. A shard
// the script refuses is reported with what the script wrote to stderr, where
// the shard scripts explain every refusal.
func laneListedTests(root, script string, shards int) (map[string]bool, error) {
	outs, errs := make([][]byte, shards+1), make([]error, shards+1)
	var wg sync.WaitGroup
	for k := 1; k <= shards; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			cmd := exec.Command("bash", script, strconv.Itoa(k), strconv.Itoa(shards))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "BEADS_TEST_SHARD_LIST_ONLY=1")
			outs[k], errs[k] = cmd.Output()
		}(k)
	}
	wg.Wait()
	listed := map[string]bool{}
	for k := 1; k <= shards; k++ {
		if err := errs[k]; err != nil {
			msg := err.Error()
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
					msg += ": " + stderr
				}
			}
			return nil, fmt.Errorf("%s %d %d: %s\n%s", script, k, shards, msg, outs[k])
		}
		for _, line := range strings.Split(string(outs[k]), "\n") {
			name, ok := strings.CutPrefix(line, "  ")
			if ok && strings.HasPrefix(name, "Test") && !strings.ContainsAny(name, " :") {
				listed[name] = true
			}
		}
	}
	return listed, nil
}
