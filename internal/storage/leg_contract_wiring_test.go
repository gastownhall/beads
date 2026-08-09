package storage

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// conformancePackage is the import path whose Run entrypoints each leg wires.
const conformancePackage = "github.com/steveyegge/beads/backend/conformance"

// neverSatisfiedTags are build tags nothing ever sets. A file behind one is in
// no build, so what it names is not wiring.
var neverSatisfiedTags = map[string]bool{"ignore": true, "never": true}

// unwiredContractEntrypoints waives, per leg, the role contracts that leg
// cannot run, with the reason it cannot. It answers the same rules the
// conformance package's own waiver list does: an entry has to name a real
// entrypoint and a real leg, carry a reason, and stop being waived the moment
// the leg wires it.
var unwiredContractEntrypoints = map[string]map[string]string{
	"dolt": {
		"RunBootstrapperRecordsExactlyOneHistoryEntry": bootstrapSplitWaiverReason,
	},
	"embeddeddolt": {
		"RunBootstrapperRecordsExactlyOneHistoryEntry": bootstrapSplitWaiverReason,
	},
	"uow": {
		"RunBootstrapperRecordsNoHistoryEntryOfItsOwn":                   bootstrapSplitWaiverReason,
		"RunIssueOperationsCreateReverseNonBlockingStagesConcreteTables": stagingWaiverReason,
		"RunIssueOperationsCreateParentChildRecomputesWaitsForClosure":   stagingWaiverReason,
	},
}

// bootstrapSplitWaiverReason covers the one pair of entrypoints that is a
// RATIFIED PER-LEG SPLIT rather than a gap.
//
// The bootstrap history contracts come in two halves that contradict each other
// on purpose: the store legs assert the role records NO entry, because `bd
// init`'s own commit records it and an in-role commit would double it, and the
// unit-of-work leg asserts EXACTLY ONE, because the proxied init route has no
// other commit point and a zero there would leave the identity unversioned.
// Each leg wires the half that is true of the front door it stands behind, and
// wiring the other half would assert a number that leg is right not to produce.
//
// This is the shape a lock like this has to be able to express. Every other
// entry here says "this leg cannot run that contract"; this one says "that
// contract is another leg's promise". Both stay checked: the pair is exhaustive
// across the three legs, and if either half stopped being wired anywhere the
// leg that owns it fails.
const bootstrapSplitWaiverReason = "the bootstrap history contracts are a ratified per-leg split — the store " +
	"legs pin zero because `bd init` commits the identity itself, the unit-of-work leg pins one because the " +
	"proxied route has no other commit point; each leg wires its own half and the other half is not its promise"

// stagingWaiverReason is why the two staging contracts stop at the two
// store-backed legs.
//
// Both assert what a create COMMITS: seed rows, commit them, dirty an unrelated
// durable row in the working set, create, then read `AS OF 'HEAD'` to prove the
// create staged its own tables without sweeping the dirty row in. That needs a
// caller-held working set and a Commit hook to close it, and the unit-of-work
// provider has neither by design — every unit of work commits itself, with its
// own message, so there is no uncommitted state for a caller to leave lying
// around and no separate commit for one to be swept into. Its fixture supplies
// neither Commit nor Exec (see uow/role_fixture_kit_test.go), and the two cases
// dereference Commit unguarded rather than skipping loudly.
//
// Wiring them would mean inventing a commit boundary this backend does not
// have, to assert a property it cannot violate. That is a change to the
// backend's test surface, not a missing line, so it belongs in a change of its
// own rather than arriving behind a wiring lock.
const stagingWaiverReason = "the unit-of-work provider commits every unit of work itself, so it has no " +
	"caller-held working set to stage into and no Commit hook to close one; these two cases assert what a " +
	"create sweeps into a commit the caller opened"

// TestEveryLegWiresEveryRoleContract fails when a backend leg skips a role
// contract the conformance package exports.
//
// The contracts are shared source: writing one is worth nothing until every leg
// runs it, and nothing about adding a Run entrypoint reminds an author to wire
// it into three test files. A leg missing one is invisible — its own suite
// still passes, and the entrypoint still has two other backends behind it, so
// the silence looks exactly like coverage.
//
// It reads source rather than running anything, so it holds for the legs whose
// suites need infrastructure this test does not have: the server-backed store
// needs a live sql-server and the embedded store needs cgo, and their wiring is
// checked here either way.
func TestEveryLegWiresEveryRoleContract(t *testing.T) {
	root := repositoryRoot(t)
	entrypoints := roleContractEntrypoints(t, filepath.Join(root, "backend", "conformance"))
	if len(entrypoints) == 0 {
		t.Fatal("the conformance package exports no role contract entrypoints; this test would pass vacuously")
	}
	legs := contractLegs(t, filepath.Join(root, "internal", "storage"))
	if len(legs) == 0 {
		t.Fatal("no package under internal/storage imports the conformance package; this test would pass vacuously")
	}
	known := map[string]bool{}
	for _, name := range entrypoints {
		known[name] = true
	}

	for _, leg := range legs {
		t.Run(leg, func(t *testing.T) {
			wired, excluded := conformanceEntrypointsWiredBy(t, filepath.Join(root, "internal", "storage", leg))
			waived := unwiredContractEntrypoints[leg]

			var missing []string
			names := 0
			for _, name := range entrypoints {
				_, isWaived := waived[name]
				if wired[name] {
					names++
				}
				switch {
				case wired[name] && isWaived:
					t.Errorf("%s is waived as unwired for %s but the leg runs it: "+
						"delete its entry from unwiredContractEntrypoints", name, leg)
				case !wired[name] && !isWaived:
					missing = append(missing, name)
				}
			}
			if len(missing) > 0 {
				detail := fmt.Sprintf(" (%d waived)", len(waived))
				if len(excluded) > 0 {
					detail += fmt.Sprintf(" (ignoring %d file(s) no build includes: %s)",
						len(excluded), strings.Join(excluded, ", "))
				}
				t.Errorf("%s names %d of the %d role contract entrypoints%s; it never names: %s",
					leg, names, len(entrypoints), detail, strings.Join(missing, ", "))
			}

			for _, name := range sortedNames(waived) {
				if !known[name] {
					t.Errorf("unwiredContractEntrypoints waives %q for %s, which is no contract entrypoint", name, leg)
					continue
				}
				if strings.TrimSpace(waived[name]) == "" {
					t.Errorf("unwiredContractEntrypoints waives %s for %s with no reason", name, leg)
				}
			}
		})
	}

	for _, leg := range sortedNames(unwiredContractEntrypoints) {
		if !sortedContains(legs, leg) {
			t.Errorf("unwiredContractEntrypoints waives entrypoints for %q, which is no backend leg", leg)
		}
	}
}

// repositoryRoot locates the module root from this file's own path.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

// contractLegs reports the packages under dir whose tests import the
// conformance package, which is what makes a package a backend leg.
//
// It is derived rather than listed because a fourth leg arriving beside a
// hand-written list stays outside every check here, which is the drift this
// file exists to catch. A package that only mentions the conformance package in
// a comment is not a leg and does not appear: the test is the import, not the
// word.
func contractLegs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var legs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, err := filepath.Glob(filepath.Join(dir, entry.Name(), "*_test.go"))
		if err != nil {
			t.Fatalf("globbing %s: %v", entry.Name(), err)
		}
		for _, file := range files {
			parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatalf("parsing %s: %v", file, err)
			}
			if conformanceImportName(parsed) != "" {
				legs = append(legs, entry.Name())
				break
			}
		}
	}
	sort.Strings(legs)
	return legs
}

// roleContractEntrypoints reports the role tier: every exported Run function
// the conformance package declares whose final parameter is one of its own
// Fixture types.
//
// The SHAPE is what makes an entrypoint role tier, not the file it sits in. An
// earlier version of this test read the *_contract.go filenames and missed the
// two staging cases in issue_operations_staging.go — which two of the three
// legs wire and one does not, exactly the drift this test is for. A fixture
// parameter is the tier's defining shape: RunAll and the audit suites take a
// Factory and are wired once per leg rather than case by case.
func roleContractEntrypoints(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	var names []string
	for _, pkg := range pkgs {
		fixtures := fixtureTypeNames(pkg)
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Run") || !ast.IsExported(fn.Name.Name) {
					continue
				}
				if fixtures[finalParamTypeName(fn)] {
					names = append(names, fn.Name.Name)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// fixtureTypeNames reports the package's own Fixture types.
func fixtureTypeNames(pkg *ast.Package) map[string]bool {
	names := map[string]bool{}
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !strings.HasSuffix(ts.Name.Name, "Fixture") {
					continue
				}
				names[ts.Name.Name] = true
			}
		}
	}
	return names
}

// finalParamTypeName reports the package-local type name of a function's last
// parameter, or "" when it has none or names a type from elsewhere.
func finalParamTypeName(fn *ast.FuncDecl) string {
	params := fn.Type.Params
	if params == nil || len(params.List) == 0 {
		return ""
	}
	last := params.List[len(params.List)-1].Type
	if star, ok := last.(*ast.StarExpr); ok {
		last = star.X
	}
	if ident, ok := last.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// conformanceEntrypointsWiredBy reports the conformance entrypoints a leg's
// test sources name, resolved through each file's own import of the conformance
// package so a renamed import still counts. It also reports the files it
// refused to read.
//
// It counts every reference rather than only calls, because a leg is free to
// wire an entrypoint as a value: the unit-of-work leg drives five of its roles
// from a table whose rows hold `run: conformance.RunX` and call it later
// through the field. Counting calls alone read those ninety-one contracts as
// unwired.
//
// A file behind a build tag nothing sets is REFUSED: `//go:build ignore` over a
// file naming every entrypoint would otherwise satisfy this lock with source no
// build compiles. Ordinary constraints — cgo, integration — are counted,
// because a contract wired behind one is still wired.
func conformanceEntrypointsWiredBy(t *testing.T, dir string) (map[string]bool, []string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatalf("globbing %s: %v", dir, err)
	}
	wired := map[string]bool{}
	var excluded []string
	for _, path := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		if namesNeverSatisfiedTag(parsed) {
			excluded = append(excluded, filepath.Base(path))
			continue
		}
		local := conformanceImportName(parsed)
		if local == "" {
			continue
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			selector, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkgName, ok := selector.X.(*ast.Ident); ok && pkgName.Name == local {
				wired[selector.Sel.Name] = true
			}
			return true
		})
	}
	sort.Strings(excluded)
	return wired, excluded
}

// namesNeverSatisfiedTag reports whether a file's build constraint can be
// satisfied by no build at all.
func namesNeverSatisfiedTag(file *ast.File) bool {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !constraint.IsGoBuild(comment.Text) {
				continue
			}
			expr, err := constraint.Parse(comment.Text)
			if err != nil {
				continue
			}
			if !satisfiable(expr) {
				return true
			}
		}
	}
	return false
}

// satisfiable reports whether some build sets tags that make expr true, taking
// the never-set tags as the only ones that cannot be turned on.
//
// It asks about satisfiability rather than evaluating with every tag set true,
// which is what an earlier draft did and got wrong: under that oracle
// `integration && !windows` reads as false, and five real integration files
// were dropped from the wiring count for having a negation in them.
func satisfiable(expr constraint.Expr) bool {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return !neverSatisfiedTags[e.Tag]
	case *constraint.NotExpr:
		return falsifiable(e.X)
	case *constraint.AndExpr:
		return satisfiable(e.X) && satisfiable(e.Y)
	case *constraint.OrExpr:
		return satisfiable(e.X) || satisfiable(e.Y)
	}
	return true
}

// falsifiable reports whether some build leaves expr false. Any tag can be left
// unset, so a bare tag always can be.
func falsifiable(expr constraint.Expr) bool {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		return true
	case *constraint.NotExpr:
		return satisfiable(e.X)
	case *constraint.AndExpr:
		return falsifiable(e.X) || falsifiable(e.Y)
	case *constraint.OrExpr:
		return falsifiable(e.X) && falsifiable(e.Y)
	}
	return true
}

// conformanceImportName reports the name a file refers to the conformance
// package by, or "" when it does not import it.
func conformanceImportName(file *ast.File) string {
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		if path != conformancePackage {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return path[strings.LastIndexByte(path, '/')+1:]
	}
	return ""
}

// sortedNames returns a map's keys in a deterministic order.
func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// sortedContains reports whether a sorted slice holds want.
func sortedContains(names []string, want string) bool {
	index := sort.SearchStrings(names, want)
	return index < len(names) && names[index] == want
}
