package conformance

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestRoleContractCasesCoverEveryContractCase is the drift guard on the role
// tier's dispatch table, and the reason RunRoleContracts can be trusted as an
// external backend's whole semantic gate.
//
// Without it, a contract case added by a later PR would run on the three
// in-tree legs (whose wiring files a reviewer sees) and silently not run
// through the bundle — a suite that quietly stops covering what it claims to,
// which is the same defect class as a test with correct assertions on a fixture
// that cannot fail, one level up. So the expected set is DERIVED from the
// package source rather than written down: every top-level func named Run* that
// takes (*testing.T, context.Context, <name>Fixture) is a role-tier case, and
// the entry points that take a Factory instead — RunAll, RunAudit and friends,
// RunPortableMethods, RunSearchPaging, RunDeferredReads — are not.
//
// The table's own half of the comparison is derived too: roleCases resolves
// each function VALUE back to its declared name (runFuncName), so a row cannot
// name one case and dispatch another.
func TestRoleContractCasesCoverEveryContractCase(t *testing.T) {
	want := parseRoleContractCases(t)
	if len(want) == 0 {
		t.Fatal("parsed no role-tier cases from the package source; the parser wiring is broken")
	}

	var got []string
	for _, contract := range roleContractCases {
		got = append(got, contract.caseNames...)
	}

	inTable := index(got)
	inSource := index(want)
	for _, name := range want {
		if _, ok := inTable[name]; !ok {
			t.Errorf("%s is a role contract case but no roleContractCases row dispatches it; "+
				"an external backend running RunRoleContracts would never see it", name)
		}
	}
	for _, name := range got {
		if _, ok := inSource[name]; !ok {
			t.Errorf("roleContractCases dispatches %q, which is not a role contract case in this package", name)
		}
	}
	for name, n := range inTable {
		if n > 1 {
			t.Errorf("roleContractCases dispatches %s %d times", name, n)
		}
	}
	if t.Failed() {
		return
	}

	// Order matters as much as coverage: the cases run in the order their
	// contract file declares them, which is the order the unit-of-work leg's
	// runners use and the order its config-writing cases depend on.
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roleContractCases is out of declaration order at position %d: table has %s, source has %s",
				i, got[i], want[i])
		}
	}
}

// TestRoleContractCasesMatchTheBundleFields closes the other direction: a
// bundle field with no table row would be a factory nobody ever calls, and a
// row reading the wrong field would run one role's cases against another's
// fixture — both silent, both invisible to the coverage guard above.
func TestRoleContractCasesMatchTheBundleFields(t *testing.T) {
	bundleType := reflect.TypeOf(RoleContractBundle{})

	rows := map[string]roleContract{}
	for _, contract := range roleContractCases {
		if _, dup := rows[contract.role]; dup {
			t.Errorf("two roleContractCases rows claim the bundle field %s", contract.role)
		}
		rows[contract.role] = contract
		if _, ok := bundleType.FieldByName(contract.role); !ok {
			t.Errorf("roleContractCases row %q names no RoleContractBundle field", contract.role)
		}
		if len(contract.caseNames) == 0 {
			t.Errorf("roleContractCases row %q dispatches no cases", contract.role)
		}
		if contract.accessors == "" {
			t.Errorf("roleContractCases row %q names no accessor, so its skip message cannot point "+
				"a partial backend at the allowlist entry it owes", contract.role)
		}
	}

	for i := range bundleType.NumField() {
		field := bundleType.Field(i)
		contract, ok := rows[field.Name]
		if !ok {
			t.Errorf("RoleContractBundle.%s has no roleContractCases row, so nothing ever calls it", field.Name)
			continue
		}
		// Only this field is supplied, so only this row may report itself
		// supplied. A copy-pasted field picker fails here.
		bundle := reflect.New(bundleType).Elem()
		bundle.Field(i).Set(nonNilFactory(field.Type))
		supplied := bundle.Interface().(RoleContractBundle)
		if !contract.supplied(supplied) {
			t.Errorf("roleContractCases row %q does not read RoleContractBundle.%s", contract.role, field.Name)
		}
		for _, other := range roleContractCases {
			if other.role != contract.role && other.supplied(supplied) {
				t.Errorf("roleContractCases row %q reads RoleContractBundle.%s, which belongs to row %q",
					other.role, field.Name, contract.role)
			}
		}
	}
}

// TestOnlyTheIssueOperationsContractsBuildAFixturePerCase pins which contracts
// pay for a fresh workspace per case. It is a two-way pin: relaxing one of the
// two to a shared fixture asserts a safety no leg has ever demonstrated, and
// tightening another to per-case quietly multiplies a supplier's setup cost by
// its case count — thirty-five, for the dependency editor.
func TestOnlyTheIssueOperationsContractsBuildAFixturePerCase(t *testing.T) {
	perCase := map[string]bool{"IssueOperations": true, "IssueOperationsStaging": true}
	for _, contract := range roleContractCases {
		want := oncePerRole
		if perCase[contract.role] {
			want = oncePerCase
		}
		if contract.policy != want {
			t.Errorf("%s builds its fixture %v, want %v", contract.role, contract.policy, want)
		}
	}
}

// TestRoleContractSubtestNamesAreUnique pins the names cases are addressable
// under. subtestName strips the role where the case carries it, so two cases in
// one contract could in principle collapse onto one -run address.
func TestRoleContractSubtestNamesAreUnique(t *testing.T) {
	for _, contract := range roleContractCases {
		seen := map[string]string{}
		for _, name := range contract.caseNames {
			short := subtestName(contract.role, name)
			if short == "" {
				t.Errorf("%s reduces to an empty subtest name under role %s", name, contract.role)
				continue
			}
			if prev, dup := seen[short]; dup {
				t.Errorf("%s and %s both address as %s/%s", prev, name, contract.role, short)
			}
			seen[short] = name
		}
	}
}

// probeFixture stands in for a role fixture in the tests that exercise the
// dispatch machinery itself. The log travels through the fixture, so what it
// records also says which fixture value each case was handed.
type probeFixture struct {
	mark string
	log  *[]string
}

func RunProbeFirst(t *testing.T, ctx context.Context, fixture probeFixture) {
	*fixture.log = append(*fixture.log, "First "+fixture.mark+" "+t.Name())
}

func RunProbeSecond(t *testing.T, ctx context.Context, fixture probeFixture) {
	*fixture.log = append(*fixture.log, "Second "+fixture.mark+" "+t.Name())
}

// TestRoleCasesHonorsTheFixturePolicy is the only test that drives a contract
// with a factory actually supplied: it pins the per-field instantiation policy
// the bundle's whole isolation story rests on — once per role for most, once
// per case for the two issue-operations fields — plus declaration order and the
// subtest name each case addresses under.
func TestRoleCasesHonorsTheFixturePolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy fixturePolicy
		want   []string
	}{
		{"OncePerRole", oncePerRole, []string{"First fixture-1", "Second fixture-1"}},
		{"OncePerCase", oncePerCase, []string{"First fixture-1", "Second fixture-2"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var log []string
			builds := 0
			contract := roleCases("Probe", "Probe()", test.policy,
				func(RoleContractBundle) func(t *testing.T) *probeFixture {
					return func(*testing.T) *probeFixture {
						builds++
						return &probeFixture{mark: fmt.Sprintf("fixture-%d", builds), log: &log}
					}
				},
				RunProbeFirst, RunProbeSecond)

			contract.run(t, context.Background(), RoleContractBundle{})

			if len(log) != len(test.want) {
				t.Fatalf("ran %d cases (%v), want %d", len(log), log, len(test.want))
			}
			for i, entry := range log {
				if !strings.HasPrefix(entry, test.want[i]+" ") {
					t.Errorf("case %d ran as %q, want it to run %q", i, entry, test.want[i])
				}
				// RunProbeFirst addresses as Probe/First, the role stripped.
				caseName, _, _ := strings.Cut(test.want[i], " ")
				if !strings.HasSuffix(entry, "/"+caseName) {
					t.Errorf("case %d addressed as %q, want a subtest name ending /%s", i, entry, caseName)
				}
			}
		})
	}
}

const roleBundleProbeEnv = "BEADS_ROLE_BUNDLE_PROBE"

// TestRoleContractBundleAllNilSkipsEveryContract proves the partial-backend
// promise from the empty end: a bundle that supplies nothing runs nothing,
// SKIPS every contract with a message naming the accessor the backend then owes
// its unsupported allowlist, and passes.
//
// "Passes" is the part that has to be watched. A suite that credits a backend
// for a role it never ran is exactly what the nil field is supposed to prevent,
// so the assertion is on the child process's verbose output — a role that
// quietly PASSED rather than skipped is indistinguishable from a skip in this
// process, and is the failure this test exists to catch.
func TestRoleContractBundleAllNilSkipsEveryContract(t *testing.T) {
	if os.Getenv(roleBundleProbeEnv) == "allnil" {
		RunRoleContracts(t, context.Background(), RoleContractBundle{})
		return
	}

	out, err := runProbeChild(t, "allnil")
	if err != nil {
		t.Fatalf("an all-nil bundle must pass, not fail: %v\n%s", err, out)
	}
	text := out

	for _, contract := range roleContractCases {
		if !strings.Contains(text, "--- SKIP: "+t.Name()+"/"+contract.role+" ") {
			t.Errorf("%s did not SKIP against an all-nil bundle", contract.role)
		}
		if !strings.Contains(text, "RoleContractBundle."+contract.role+" is nil") {
			t.Errorf("%s skipped without naming the bundle field a backend must fill", contract.role)
		}
		if !strings.Contains(text, contract.accessors) {
			t.Errorf("%s skipped without naming %s, so a partial backend is not told what it owes "+
				"its unsupported allowlist", contract.role, contract.accessors)
		}
	}
	for _, verdict := range []string{"--- PASS: ", "--- FAIL: "} {
		if strings.Contains(text, verdict+t.Name()+"/") {
			t.Errorf("a contract reported %sagainst an all-nil bundle; nothing may run without a fixture\n%s",
				strings.TrimPrefix(verdict, "--- "), text)
		}
	}
}

// TestRoleCasesFailsWhenAFactoryReturnsNil pins the other half of the nil
// convention. A nil FIELD narrows what the backend claims; a nil FIXTURE from a
// field the backend did fill is a broken supplier, and quietly skipping it
// would let a wiring bug read as a modest capability claim.
func TestRoleCasesFailsWhenAFactoryReturnsNil(t *testing.T) {
	if os.Getenv(roleBundleProbeEnv) == "nilfixture" {
		var log []string
		roleCases("Probe", "Probe()", oncePerRole,
			func(RoleContractBundle) func(t *testing.T) *probeFixture {
				return func(*testing.T) *probeFixture { return nil }
			},
			RunProbeFirst,
		).run(t, context.Background(), RoleContractBundle{})
		if len(log) != 0 {
			t.Errorf("a case ran against a nil fixture")
		}
		return
	}

	out, err := runProbeChild(t, "nilfixture")
	if err == nil {
		t.Fatalf("a factory returning nil must fail the contract, not pass it:\n%s", out)
	}
	if !strings.Contains(out, "returned a nil fixture") {
		t.Errorf("the failure did not name the broken factory:\n%s", out)
	}
	if strings.Contains(out, "--- SKIP: "+t.Name()) {
		t.Errorf("a nil fixture SKIPPED; a broken supplier must fail rather than narrow what the "+
			"backend is held to:\n%s", out)
	}
}

// runProbeChild re-runs the calling test in a child process with mode set, so
// its subtest verdicts can be read rather than merged into this process's own.
func runProbeChild(t *testing.T, mode string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), roleBundleProbeEnv+"="+mode)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// parseRoleContractCases returns every role-tier case declared in this
// package's non-test sources, in filename then declaration order — the order
// roleContractCases is written in.
func parseRoleContractCases(t *testing.T) []string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, filepath.Dir(thisFile), func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing package conformance: %v", err)
	}
	var cases []string
	for _, pkg := range pkgs {
		files := make([]string, 0, len(pkg.Files))
		for name := range pkg.Files {
			files = append(files, name)
		}
		sort.Strings(files)
		for _, name := range files {
			for _, decl := range pkg.Files[name].Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Run") {
					continue
				}
				if takesFixture(fn) {
					cases = append(cases, fn.Name.Name)
				}
			}
		}
	}
	return cases
}

// takesFixture reports whether fn has the role-tier case signature:
// (t *testing.T, ctx context.Context, fixture <name>Fixture).
func takesFixture(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 3 {
		return false
	}
	params := fn.Type.Params.List
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok || !isQualified(star.X, "testing", "T") {
		return false
	}
	if !isQualified(params[1].Type, "context", "Context") {
		return false
	}
	fixture, ok := params[2].Type.(*ast.Ident)
	return ok && strings.HasSuffix(fixture.Name, "Fixture")
}

func isQualified(expr ast.Expr, pkg, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg
}

// nonNilFactory builds a factory of the given field type that returns a zero
// fixture. It is never called; the tests only need the field to be non-nil.
func nonNilFactory(factoryType reflect.Type) reflect.Value {
	return reflect.MakeFunc(factoryType, func([]reflect.Value) []reflect.Value {
		return []reflect.Value{reflect.New(factoryType.Out(0).Elem())}
	})
}

func index(names []string) map[string]int {
	counts := make(map[string]int, len(names))
	for _, name := range names {
		counts[name]++
	}
	return counts
}
