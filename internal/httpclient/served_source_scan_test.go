package httpclient

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// servedProviderCuratedRun is the -test.run of
// //internal/httpclient:httpclient_served_provider_test, verbatim
// (TestServedProviderLaneSelectsOnlySwitchedTests holds the two equal under
// go test; Bazel gives the test no BUILD file). The provider lane
// proves the Provider arm only for the tests it selects that reach the source
// switch; a selected test that stands up the shared composition (composition,
// served_test.go: always the Roles arm over embedded Dolt) or opens an
// embedded store itself would run on roles inside the provider lane and pass
// there without testing the arm the lane exists for.
const servedProviderCuratedRun = `^(TestServedDeleter|TestServedLifecycle|TestServedMetadataCAS|TestServedClaimer|TestServedReleaser|TestServedReadyClaimer|TestServedBatchCloser|TestServedTierServesFromTheSourceItNames)`

const servedEmbeddedDoltImport = "github.com/steveyegge/beads/internal/storage/embeddeddolt"

// servedRolesSourceHelpers are the functions that open embedded Dolt only on
// the roles side of the switch (newServedEnvExpecting calls them only when
// BEADS_HTTP_SERVED_SOURCE selects roles). The scan does not follow them.
var servedRolesSourceHelpers = map[string]bool{
	"servedRolesSource":       true,
	"servedTemplateWorkspace": true,
}

// servedImportNames is every name file imports path under (a file may import
// it twice, once aliased); blank and dot imports name nothing.
func servedImportNames(file *ast.File, path string) []string {
	var names []string
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, "`\"") != path {
			continue
		}
		switch {
		case imp.Name == nil:
			names = append(names, path[strings.LastIndex(path, "/")+1:])
		case imp.Name.Name != "_" && imp.Name.Name != ".":
			names = append(names, imp.Name.Name)
		}
	}
	return names
}

// scanServedProviderCurated returns how many top-level tests run selects, and
// one violation for each selected test that reaches composition or
// embeddeddolt.Open other than through the source switch. Reaching is a
// fixpoint over every reference to a package-level function or method name,
// so a helper passed as a value (t.Run(name, helper)) counts as reached.
func scanServedProviderCurated(files map[string]*ast.File, run *regexp.Regexp) (int, []string) {
	bodies := map[string][]*ast.BlockStmt{}
	embeddedNames := map[*ast.BlockStmt][]string{}
	for _, name := range seamSortedNames(files) {
		f := files[name]
		imps := servedImportNames(f, servedEmbeddedDoltImport)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			bodies[fd.Name.Name] = append(bodies[fd.Name.Name], fd.Body)
			embeddedNames[fd.Body] = imps
		}
	}
	// A function is tainted if its body opens embedded Dolt or names
	// composition, or names a tainted function.
	refs := map[string]map[string]bool{}
	tainted := map[string]bool{"composition": true}
	for name, list := range bodies {
		refs[name] = map[string]bool{}
		if servedRolesSourceHelpers[name] {
			continue
		}
		for _, body := range list {
			imps := embeddedNames[body]
			ast.Inspect(body, func(n ast.Node) bool {
				switch e := n.(type) {
				case *ast.CallExpr:
					for _, imp := range imps {
						if seamIsCall(e, imp, "Open") {
							tainted[name] = true
						}
					}
				case *ast.SelectorExpr:
					if _, ok := bodies[e.Sel.Name]; ok {
						refs[name][e.Sel.Name] = true
					}
				case *ast.Ident:
					if _, ok := bodies[e.Name]; ok {
						refs[name][e.Name] = true
					}
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for name, rs := range refs {
			if tainted[name] || servedRolesSourceHelpers[name] {
				continue
			}
			for r := range rs {
				if tainted[r] && !servedRolesSourceHelpers[r] {
					tainted[name] = true
					changed = true
					break
				}
			}
		}
	}
	selected := 0
	var violations []string
	names := make([]string, 0, len(bodies))
	for name := range bodies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasPrefix(name, "Test") || !run.MatchString(name) {
			continue
		}
		selected++
		if tainted[name] {
			violations = append(violations, name+" is selected by the provider lane's -test.run but reaches composition or embeddeddolt.Open outside the source switch: it would run on the Roles arm over embedded Dolt in the provider lane; build it on newServedEnv/newServedEnvExpecting or leave it out of the curated set")
		}
	}
	return selected, violations
}

// servedProviderCuratedFloor is well under the curated set's size (160 when
// the lane was admitted), so the scan fails if the regex or the parse stops
// selecting the tests it guards.
const servedProviderCuratedFloor = 100

func TestServedProviderLaneSelectsOnlySwitchedTests(t *testing.T) {
	switch build, err := os.ReadFile("BUILD.bazel"); {
	case errors.Is(err, fs.ErrNotExist):
		t.Log("no BUILD.bazel in the test's directory (a Bazel run): the -test.run equality is checked under go test")
	case err != nil:
		t.Fatalf("read BUILD.bazel: %v", err)
	case !strings.Contains(string(build), "\"-test.run="+servedProviderCuratedRun+"\","):
		t.Errorf("httpclient_served_provider_test's -test.run is not servedProviderCuratedRun %q: update the constant with the rule, or the scan guards a different set than the lane runs", servedProviderCuratedRun)
	}
	_, files := seamParsePackage(t, ".")
	selected, violations := scanServedProviderCurated(files, regexp.MustCompile(servedProviderCuratedRun))
	for _, v := range violations {
		t.Error(v)
	}
	if selected < servedProviderCuratedFloor {
		t.Errorf("the provider lane's -test.run selects %d tests here, want at least %d: the scan guards less than it should", selected, servedProviderCuratedFloor)
	}
	t.Logf("curated tests scanned: %d", selected)
}

func TestTheCuratedScanCatchesARolesOnlyTest(t *testing.T) {
	base := "package httpclient\n" +
		"import \"testing\"\n" +
		"func composition(t *testing.T) {}\n" +
		"func newServedEnvExpecting(t *testing.T) { servedRolesSource(t) }\n" +
		"func servedRolesSource(t *testing.T) { servedTemplateWorkspace() }\n" +
		"func servedTemplateWorkspace() { embeddeddolt.Open(nil, \"\", \"\", \"\") }\n" +
		"func TestServedDeleterSwitched(t *testing.T) { newServedEnvExpecting(t) }\n"
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"switched only", "", 0},
		{"direct composition", "func TestServedClaimerShared(t *testing.T) { composition(t) }\n", 1},
		{"composition through a helper", "func helper(t *testing.T) { composition(t) }\nfunc TestServedReleaserViaHelper(t *testing.T) { helper(t) }\n", 1},
		{"helper passed as a value", "func sub(t *testing.T) { composition(t) }\nfunc TestServedLifecycleUpdateSub(t *testing.T) { t.Run(\"x\", sub) }\n", 1},
		{"direct embedded open", "func TestServedBatchCloserEmbedded(t *testing.T) { embeddeddolt.Open(nil, \"\", \"\", \"\") }\n", 1},
		{"aliased embedded open", "func TestServedMetadataCASAliased(t *testing.T) { ed.Open(nil, \"\", \"\", \"\") }\n", 1},
		{"unselected test may share", "func TestServedListerShared(t *testing.T) { composition(t) }\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			files := map[string]*ast.File{}
			srcs := map[string]string{
				"served_base_test.go": strings.Replace(base, "import \"testing\"\n", "import (\n\"testing\"\n\""+servedEmbeddedDoltImport+"\"\n)\n", 1),
				"served_case_test.go": "package httpclient\nimport (\n\"testing\"\ned \"" + servedEmbeddedDoltImport + "\"\n\"" + servedEmbeddedDoltImport + "\"\n)\nvar _ = ed.Open\nvar _ = embeddeddolt.Open\nvar _ testing.T\n" + tc.src,
			}
			for name, src := range srcs {
				f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
				if err != nil {
					t.Fatalf("parse %s: %v", name, err)
				}
				files[name] = f
			}
			selected, violations := scanServedProviderCurated(files, regexp.MustCompile(servedProviderCuratedRun))
			if len(violations) != tc.want {
				t.Errorf("violations %q, want %d", violations, tc.want)
			}
			if selected == 0 {
				t.Error("the scan selected no tests")
			}
		})
	}
}
