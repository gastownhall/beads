package main

// bd cook resolves its argument as a formula name from the search paths and,
// failing that, as a formula file path. The resolver behind bd mol pour, wisp,
// bond and seed accepted only a name, so a formula file outside every search
// path cooked but would not pour. Both now go through loadFormulaByNameOrPath.
// These tests pin the shared lookup, its name-before-path precedence, which
// error survives a double miss, and the pour/bond entry points that depend on
// it. Every fixture lives under t.TempDir() and every parser gets explicit
// search paths, so the repository's own .beads/formulas never takes part.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
)

const pathFallbackFormulaTOML = `formula = "path-fallback-resolver-test"
version = 1
type = "workflow"

[[steps]]
id = "only-step"
title = "Step reachable by path only"
`

// writePathFallbackFormula writes the fixture in a directory that is not a
// formula search path, so only the file-path lookup can find it.
func writePathFallbackFormula(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sub", "path-fallback-resolver-test.formula.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(pathFallbackFormulaTOML), 0o600); err != nil {
		t.Fatalf("write formula fixture: %v", err)
	}
	return p
}

// emptySearchPath returns a directory with no formulas in it, so a parser
// given it can never satisfy a name lookup.
func emptySearchPath(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func TestLoadFormulaByNameOrPath_PathFallback(t *testing.T) {
	path := writePathFallbackFormula(t)
	parser := formula.NewParser(emptySearchPath(t))

	// The name is still unknown to the registry: the path lookup adds a way
	// in, it does not widen what a name resolves to.
	if _, err := loadFormulaByNameOrPath(parser, "path-fallback-resolver-test"); err == nil {
		t.Fatal("expected name lookup to fail with an empty search path")
	}

	f, err := loadFormulaByNameOrPath(parser, path)
	if err != nil {
		t.Fatalf("path lookup failed: %v", err)
	}
	if f.Formula != "path-fallback-resolver-test" {
		t.Errorf("formula name = %q, want path-fallback-resolver-test", f.Formula)
	}
	if f.Source != path {
		t.Errorf("formula Source = %q, want %q", f.Source, path)
	}
}

func TestLoadFormulaByNameOrPath_RegistryWins(t *testing.T) {
	// The same formula name in the registry and in a stray file in the
	// working directory: the registry copy wins, because the name lookup
	// runs first.
	registryDir := filepath.Join(t.TempDir(), ".beads", "formulas")
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registryTOML := strings.Replace(pathFallbackFormulaTOML,
		`title = "Step reachable by path only"`,
		`title = "Registry copy wins"`, 1)
	if err := os.WriteFile(filepath.Join(registryDir, "path-fallback-resolver-test.formula.toml"),
		[]byte(registryTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	parser := formula.NewParser(registryDir)
	f, err := loadFormulaByNameOrPath(parser, "path-fallback-resolver-test")
	if err != nil {
		t.Fatalf("registry lookup failed: %v", err)
	}
	if len(f.Steps) != 1 || !strings.Contains(f.Steps[0].Title, "Registry copy wins") {
		t.Errorf("expected the registry copy to win; got steps %+v", f.Steps)
	}
}

// When both lookups miss, the error names what the argument was written as:
// a bare name reports the registry miss (pour, wisp and bond fall through to
// a proto or issue lookup on that and keep their "not found" message), a path
// reports the file error (a typo in a path, or a broken formula file, is
// named as such instead of as a registry miss).
func TestLoadFormulaByNameOrPath_ErrorNamesWhatWasTried(t *testing.T) {
	parser := formula.NewParser(emptySearchPath(t))

	_, err := loadFormulaByNameOrPath(parser, "no-such-formula")
	if err == nil || !strings.Contains(err.Error(), "not found in search paths") {
		t.Errorf("bare name: want the registry miss, got %v", err)
	}

	missing := filepath.Join(t.TempDir(), "missing.formula.toml")
	_, err = loadFormulaByNameOrPath(parser, missing)
	if err == nil || strings.Contains(err.Error(), "not found in search paths") {
		t.Errorf("missing path: want the file error, got %v", err)
	}

	broken := filepath.Join(t.TempDir(), "broken.formula.toml")
	if err := os.WriteFile(broken, []byte("formula = \"broken\"\n[[steps]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = loadFormulaByNameOrPath(parser, broken)
	if err == nil || !strings.Contains(err.Error(), "parse ") {
		t.Errorf("broken file: want the parse error, got %v", err)
	}
}

// TestResolveAndCookFormulaWithVars_AcceptsPath is the contract pour, wisp,
// seed and bond's materialization rely on: a formula file outside every
// search path cooks, and an argument that is neither a name nor a file still
// fails, so pour's proto-ID fallback stays reachable.
func TestResolveAndCookFormulaWithVars_AcceptsPath(t *testing.T) {
	path := writePathFallbackFormula(t)
	searchPaths := []string{emptySearchPath(t)}

	sg, err := resolveAndCookFormulaWithVars(path, searchPaths, nil)
	if err != nil {
		t.Fatalf("resolveAndCookFormulaWithVars(%q) failed: %v", path, err)
	}
	if sg == nil || sg.Root == nil {
		t.Fatal("expected a cooked subgraph")
	}
	if sg.Root.ID != "path-fallback-resolver-test" {
		t.Errorf("root ID = %q, want the formula name", sg.Root.ID)
	}

	missing := filepath.Join(t.TempDir(), "missing.formula.toml")
	if _, err := resolveAndCookFormulaWithVars(missing, searchPaths, nil); err == nil {
		t.Fatal("expected failure for a nonexistent path")
	}
}

// bd mol bond resolves its operands twice: once to describe them (the dry-run
// and the pre-materialization discovery) and once to cook them. Both passes
// must take a formula file path, or the bond is refused before the cook runs.
func TestMolBondResolversAcceptFormulaPath(t *testing.T) {
	isolateBeadsDirForTest(t)
	t.Setenv("GT_ROOT", emptySearchPath(t))
	path := writePathFallbackFormula(t)
	ctx := context.Background()
	reader := &noIssuesMolReader{}

	issue, name, err := resolveOrDescribe(ctx, reader, path, nil)
	if err != nil {
		t.Fatalf("resolveOrDescribe(%q): %v", path, err)
	}
	if issue != nil || name != "path-fallback-resolver-test" {
		t.Errorf("resolveOrDescribe = (%v, %q), want (nil, path-fallback-resolver-test)", issue, name)
	}

	sg, cooked, err := resolveOrCookToSubgraph(ctx, reader, path, nil)
	if err != nil {
		t.Fatalf("resolveOrCookToSubgraph(%q): %v", path, err)
	}
	if !cooked || sg == nil || sg.Root == nil || sg.Root.ID != "path-fallback-resolver-test" {
		t.Errorf("resolveOrCookToSubgraph: cooked=%v subgraph=%+v, want a cooked subgraph rooted at the formula", cooked, sg)
	}
}
