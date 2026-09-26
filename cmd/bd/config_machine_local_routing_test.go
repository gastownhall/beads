package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// literalConfigWriters are the writers that write exactly where they are told.
// That is deliberate (#6574's round-trip suite depends on it), which is why the
// ROUTING decision lives at the caller -- and why the callers have to be
// enumerable. Matching is by SELECTOR NAME, not by resolved import path: the
// broad direction is the safe one for a guard, because a false positive is
// visible in CI and a false negative is not.
//
// SetYAMLConfig is the adapter method in internal/storage/domain, which is
// wired to config.SetYamlConfig in fs_adapters.go. A caller reaching the
// tracked file through the adapter dirties it exactly as a direct call does,
// and a guard that only knew the direct spelling would not see it.
var literalConfigWriters = map[string]bool{
	"SetYamlConfig":        true,
	"SetYamlConfigInDir":   true,
	"UnsetYamlConfig":      true,
	"UnsetYamlConfigInDir": true,
	"SetYAMLConfig":        true,
	"SaveConfigValue":      true,
}

// TestNoLiteralWriterWritesAMachineLocalKey is the standing form of the audit
// that found this PR's two remaining defects.
//
// The registry now enforces the invariant at runtime from both sides -- the
// sidecar writers refuse a shared key, SaveConfigValue refuses a machine-local
// one -- but a runtime refusal only fires on a path a test actually walks, and
// `bd init --debug` writing dolt.debug to the tracked config.yaml was missed by
// every test in the tree. This check is syntactic, so it sees the call site
// whether or not anything executes it.
//
// The failure it prevents is not a crash. A machine-local key in the tracked
// file gives that key TWO homes, and the sidecar wins on read: `bd init --debug`
// wrote a tracked `true`, `bd config set dolt.debug false` wrote a sidecar
// `false` that won, and `bd config unset dolt.debug` cleared the sidecar and
// silently brought the tracked `true` back to life. Nothing reports an error at
// any step.
func TestNoLiteralWriterWritesAMachineLocalKey(t *testing.T) {
	root := repoRootForRoutingScan(t)

	type site struct {
		where  string
		writer string
		key    string
	}
	var offenders []site
	literalKeyCalls := 0
	keysSeen := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// A file this scan cannot parse is a file it cannot vouch for.
			t.Fatalf("parse %s: %v", path, perr)
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !literalConfigWriters[sel.Sel.Name] {
				return true
			}
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				key, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					continue
				}
				literalKeyCalls++
				keysSeen[key] = true
				if config.IsMachineLocalKey(key) {
					offenders = append(offenders, site{
						where:  rel + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line),
						writer: sel.Sel.Name,
						key:    key,
					})
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	// Scope assertions. An empty result here would otherwise read as "clean"
	// when it actually means the scan reached nothing -- a renamed writer, a
	// moved package, a walk rooted in the wrong directory.
	if literalKeyCalls == 0 {
		t.Fatal("the scan found no literal-key call to any writer in literalConfigWriters; it is not checking anything")
	}
	if !keysSeen["sync.remote"] {
		t.Fatalf("the scan did not reach cmd/bd's known sync.remote writers (saw %d literal-key calls); the walk root or the writer names are stale", literalKeyCalls)
	}

	for _, o := range offenders {
		t.Errorf("%s: %s writes machine-local key %q to the TRACKED config.yaml. "+
			"It must route through config.SetMachineLocalYamlConfig (or ...InDir), the way `bd config set` does: "+
			"a tracked value gives the key two homes, the sidecar wins on read, and unsetting the sidecar silently revives the tracked one. "+
			"If the key is genuinely shared project contract, take it out of config.MachineLocalKeys and move the docs and the other writers with it.",
			o.where, o.writer, o.key)
	}
}

// TestMachineLocalRegistryIsCoveredByTheRoutingScan pins that the scan is
// looking for the keys that exist, rather than a list that drifted out from
// under it. It fails if MachineLocalKeys is emptied or renamed wholesale.
func TestMachineLocalRegistryIsCoveredByTheRoutingScan(t *testing.T) {
	var keys []string
	for key := range config.MachineLocalKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		t.Fatal("MachineLocalKeys is empty; the routing scan can never find anything")
	}
	for _, key := range keys {
		if !config.IsMachineLocalKey(key) {
			t.Errorf("IsMachineLocalKey(%q) is false for a key in the registry", key)
		}
	}
}

// repoRootForRoutingScan walks up from the test's working directory to the
// module root, so the scan covers every package rather than just cmd/bd.
func repoRootForRoutingScan(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s; cannot root the scan", dir)
		}
		dir = parent
	}
}
