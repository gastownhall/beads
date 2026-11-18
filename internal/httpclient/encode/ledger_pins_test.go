package encode

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// Ledger pins that live outside this package.
//
// The ledger is one artifact but its pins are not. The read-shape rows are
// pinned by the encoder sweep in this directory; the write-shape rows D8's
// refuse-not-drop enumeration owns are pinned where the refusal is raised,
// which is the client's role bodies in internal/httpclient; and the F- rows —
// whole commands and the flag modes of served commands — can only be pinned
// where a cobra tree or a bd-over-serve harness exists (cmd/bd, backend/http,
// backend/http/corpus). Forcing every pin into this directory would make those
// rows carry a TODO forever and a real pin indistinguishable from a missing one.
//
// A row pinned outside this package names its test the same bare way a local
// pin does; crossPackagePins below says which file declares it, and the gate
// parses exactly those files — never a whole package. Parsing is text, so a
// registered file's build tags do not matter.
//
// To pin a row with a test in another package:
//  1. Set the row's PinnedBy to the test's name.
//  2. Add {Test, File} to crossPackagePins.
//  3. Export File to //internal/httpclient/encode:__pkg__ from its own
//     package's BUILD.bazel (exports_files) and add the label to encode_test's
//     data, so the file exists in the test's runfiles under bazel test.

// crossPackagePin registers one test outside this package that a ledger row
// cites as its PinnedBy. File is repo-relative, slash-separated, and a
// _test.go file.
type crossPackagePin struct {
	Test string
	File string
}

// crossPackagePins is every ledger pin that lives outside this package. Each
// entry must be cited by at least one row, and its File must be in
// encode_test's data.
var crossPackagePins = []crossPackagePin{
	{Test: "TestOnlyOneShotCommandsMemoizeRemoteReads", File: "cmd/bd/serve_test.go"},
	{Test: "TestDeleteCascadeRefusesAServerWithoutContainmentCascade", File: "internal/httpclient/bulk_roles_test.go"},
	{Test: "TestSweepRefusesTheS4MembersWhenTheServerLacksTheCapability", File: "internal/httpclient/bulk_roles_test.go"},
	{Test: "TestSweepSendsTheS4MembersOnceTheServerAdvertisesThem", File: "internal/httpclient/bulk_roles_test.go"},
	{Test: "TestCreateRefusesTheGateMembersAgainstAServerWithoutTheToken", File: "internal/httpclient/gate_fields_test.go"},
	{Test: "TestUpdateRefusesTheGateMembersAgainstAServerWithoutTheToken", File: "internal/httpclient/gate_fields_test.go"},
}

// crossPackagePinDirs are the packages a ledger pin may live in. Adding one is
// a reviewed one-line edit.
var crossPackagePinDirs = []string{"backend/http", "backend/http/corpus", "cmd/bd", "internal/httpclient"}

// repoRootFromThisPackage is the repository root relative to this package.
// rules_go runs a test in its package directory inside the runfiles tree, so
// the same relative path works under go test and bazel test.
var repoRootFromThisPackage = filepath.Join("..", "..", "..")

func isTODOPin(pin string) bool {
	return strings.HasPrefix(pin, "TODO(")
}

// citedPins is every real (non-blank, non-TODO) PinnedBy in rows.
func citedPins(rows []Row) map[string]bool {
	cited := map[string]bool{}
	for _, row := range rows {
		if strings.TrimSpace(row.PinnedBy) == "" || isTODOPin(row.PinnedBy) {
			continue
		}
		cited[row.PinnedBy] = true
	}
	return cited
}

// isTopLevelTest applies go test's own rule: a function, not a method, named
// Test or TestXxx where Xxx does not start with a lowercase letter, taking
// exactly one *testing.T.
func isTopLevelTest(fn *ast.FuncDecl) bool {
	if fn.Recv != nil {
		return false
	}
	name := fn.Name.Name
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	if rest := name[len("Test"):]; rest != "" {
		r := []rune(rest)[0]
		if r >= 'a' && r <= 'z' {
			return false
		}
	}
	params := fn.Type.Params.List
	if len(params) != 1 || len(params[0].Names) > 1 {
		return false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "T"
}

// topLevelTests parses one file and returns the names of its top-level tests.
func topLevelTests(fset *token.FileSet, filename string) (map[string]bool, error) {
	file, err := parser.ParseFile(fset, filename, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && isTopLevelTest(fn) {
			names[fn.Name.Name] = true
		}
	}
	return names, nil
}

// resolveCrossPackagePins checks every registry entry and returns the ones
// that resolve to a real declaration (test name -> File), plus a sorted list
// of problems. Only the registered files are read.
func resolveCrossPackagePins(root string, pins []crossPackagePin, dirs []string, local, cited map[string]bool) (map[string]string, []string) {
	resolved := map[string]string{}
	var problems []string
	report := func(pin crossPackagePin, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("crossPackagePins {%s, %s}: ", pin.Test, pin.File)+fmt.Sprintf(format, args...))
	}

	seen := map[string]bool{}
	byFile := map[string][]crossPackagePin{}
	var files []string
	for _, pin := range pins {
		if seen[pin.Test] {
			report(pin, "the test is registered twice")
			continue
		}
		seen[pin.Test] = true
		if !strings.HasPrefix(pin.Test, "Test") {
			report(pin, "Test must name a TestXxx function")
			continue
		}
		if pin.File == "" || path.IsAbs(pin.File) || path.Clean(pin.File) != pin.File ||
			strings.HasPrefix(pin.File, "../") || strings.Contains(pin.File, `\`) ||
			!strings.HasSuffix(pin.File, "_test.go") {
			report(pin, "File must be a clean, repo-relative, slash-separated path to a _test.go file")
			continue
		}
		if dir := path.Dir(pin.File); !slices.Contains(dirs, dir) {
			report(pin, "%s is not a package a ledger pin may live in (%s); adding one is a reviewed edit to crossPackagePinDirs",
				dir, strings.Join(dirs, ", "))
			continue
		}
		if local[pin.Test] {
			report(pin, "this package declares a test of the same name, and a bare PinnedBy resolves to that one")
			continue
		}
		if !cited[pin.Test] {
			report(pin, "no ledger row's PinnedBy names it; delete the entry, its encode_test data label and its export")
			continue
		}
		if _, ok := byFile[pin.File]; !ok {
			files = append(files, pin.File)
		}
		byFile[pin.File] = append(byFile[pin.File], pin)
	}

	fset := token.NewFileSet()
	for _, file := range files {
		declared, err := topLevelTests(fset, filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			report(byFile[file][0], "%v (under bazel test, a registered file must be exported to "+
				"//internal/httpclient/encode:__pkg__ by its own BUILD.bazel and listed in encode_test's data)", err)
			continue
		}
		for _, pin := range byFile[file] {
			if !declared[pin.Test] {
				report(pin, "%s declares no top-level func %s(t *testing.T); it was renamed, moved or deleted, "+
					"so the rows citing it are pinned by nothing", pin.File, pin.Test)
				continue
			}
			resolved[pin.Test] = pin.File
		}
	}
	sort.Strings(problems)
	return resolved, problems
}

// pinProblem is the ledger's pin rule: a pin is a TODO naming a bead, a test
// in this package, or a test the registry resolved. Anything else — a hopeful
// sentence, a renamed test, a test in an unregistered file — is a problem.
func pinProblem(row Row, local map[string]bool, external map[string]string) string {
	if isTODOPin(row.PinnedBy) || local[row.PinnedBy] || external[row.PinnedBy] != "" {
		return ""
	}
	return fmt.Sprintf("%s: PinnedBy %q is neither a test in this package, a test registered in crossPackagePins "+
		"(ledger_pins_test.go), nor a TODO(<bead>)", row.ID, row.PinnedBy)
}

// TestCrossPackageLedgerPinsResolveToCitedTests holds the registry to the
// files it names: every entry is unique, cited by a row, inside an allowed
// package, not shadowed by a local test, and declared as a top-level test in
// exactly its registered file. An empty registry is legal.
func TestCrossPackageLedgerPinsResolveToCitedTests(t *testing.T) {
	_, problems := resolveCrossPackagePins(repoRootFromThisPackage, crossPackagePins, crossPackagePinDirs,
		localTestFunctions(t), citedPins(Ledger()))
	for _, problem := range problems {
		t.Error(problem)
	}
}

// assertProblems requires exactly len(want) problems, each want substring
// appearing in some problem.
func assertProblems(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d problems, want %d:\n  got:  %q\n  want: %q", len(got), len(want), got, want)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if strings.Contains(g, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no problem mentions %q; got %q", w, got)
		}
	}
}

// TestCrossPackagePinRegistryRuleFires is the registry gate's own
// falsification: synthetic files under a temp root prove each check still
// tells a real cross-package pin from a missing, renamed, misplaced, stale or
// shadowed one, independent of what the real registry currently says.
func TestCrossPackagePinRegistryRuleFires(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Every fixture line is indented one tab, so repo scanners that find
	// tests by a line-anchored `^func TestX(t *testing.T)` (equivalence.py)
	// do not mistake the fixture's functions for this package's tests.
	write("backend/http/e2e_pin_test.go", `package http

	import "testing"

	type suite struct{}

	func TestRealPin(t *testing.T)            {}
	func TestNotATest(n int)                  {}
	func (suite) TestMethod(t *testing.T)     {}
	`)
	write("cmd/bd/other_test.go", `package main

	import "testing"

	func TestOther(t *testing.T) {}
	`)
	write("internal/storage/outside_test.go", `package storage

	import "testing"

	func TestOutside(t *testing.T) {}
	`)
	dirs := []string{"backend/http", "cmd/bd"}
	const pinFile = "backend/http/e2e_pin_test.go"

	cases := []struct {
		name         string
		pins         []crossPackagePin
		local        map[string]bool
		cited        []string
		wantResolved []string
		want         []string
	}{
		{name: "valid", pins: []crossPackagePin{{"TestRealPin", pinFile}}, cited: []string{"TestRealPin"},
			wantResolved: []string{"TestRealPin"}},
		{name: "nonexistent test", pins: []crossPackagePin{{"TestGone", pinFile}}, cited: []string{"TestGone"},
			want: []string{"declares no top-level func TestGone"}},
		{name: "method is not a test", pins: []crossPackagePin{{"TestMethod", pinFile}}, cited: []string{"TestMethod"},
			want: []string{"declares no top-level func TestMethod"}},
		{name: "wrong signature is not a test", pins: []crossPackagePin{{"TestNotATest", pinFile}}, cited: []string{"TestNotATest"},
			want: []string{"declares no top-level func TestNotATest"}},
		{name: "missing file", pins: []crossPackagePin{{"TestRealPin", "backend/http/missing_test.go"}}, cited: []string{"TestRealPin"},
			want: []string{"must be exported to //internal/httpclient/encode:__pkg__"}},
		{name: "uncited entry", pins: []crossPackagePin{{"TestOther", "cmd/bd/other_test.go"}},
			want: []string{"no ledger row's PinnedBy names it"}},
		{name: "local shadow", pins: []crossPackagePin{{"TestRealPin", pinFile}}, local: map[string]bool{"TestRealPin": true},
			cited: []string{"TestRealPin"}, want: []string{"declares a test of the same name"}},
		{name: "disallowed package", pins: []crossPackagePin{{"TestOutside", "internal/storage/outside_test.go"}},
			cited: []string{"TestOutside"}, want: []string{"is not a package a ledger pin may live in"}},
		{name: "duplicate entry", pins: []crossPackagePin{{"TestRealPin", pinFile}, {"TestRealPin", pinFile}},
			cited: []string{"TestRealPin"}, wantResolved: []string{"TestRealPin"}, want: []string{"registered twice"}},
		{name: "unclean path", pins: []crossPackagePin{{"TestRealPin", "backend/http/../http/e2e_pin_test.go"}},
			cited: []string{"TestRealPin"}, want: []string{"File must be"}},
		{name: "not a test file", pins: []crossPackagePin{{"TestRealPin", "backend/http/e2e_pin.go"}},
			cited: []string{"TestRealPin"}, want: []string{"File must be"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cited := map[string]bool{}
			for _, name := range tc.cited {
				cited[name] = true
			}
			local := tc.local
			if local == nil {
				local = map[string]bool{}
			}
			resolved, problems := resolveCrossPackagePins(root, tc.pins, dirs, local, cited)
			names := make([]string, 0, len(resolved))
			for name := range resolved {
				names = append(names, name)
			}
			sort.Strings(names)
			if !slices.Equal(names, tc.wantResolved) {
				t.Errorf("resolved %v, want %v", names, tc.wantResolved)
			}
			assertProblems(t, problems, tc.want)
		})
	}
}

// TestLedgerPinRuleFires is TestLedgerRowsAreWellFormed's pin rule's own
// falsification: a pin that is neither local, registered nor a TODO fails.
func TestLedgerPinRuleFires(t *testing.T) {
	local := map[string]bool{"TestLocal": true}
	external := map[string]string{"TestElsewhere": "backend/http/e2e_pin_test.go"}
	for _, pin := range []string{"TestLocal", "TestElsewhere", "TODO(bd-x): later"} {
		if problem := pinProblem(Row{ID: "R", PinnedBy: pin}, local, external); problem != "" {
			t.Errorf("PinnedBy %q: unexpected problem %q", pin, problem)
		}
	}
	for _, pin := range []string{"TestNowhere", "a hopeful sentence", "TestElsewhereRenamed"} {
		if problem := pinProblem(Row{ID: "R", PinnedBy: pin}, local, external); problem == "" {
			t.Errorf("PinnedBy %q: accepted, want a problem", pin)
		}
	}
}

// ledgerTODOPinCeiling is how many ledger rows may carry a TODO(...) pin
// instead of a real test. Frozen at origin/main 473aa09fa1: 85 of 236 rows
// (all 12 F-, 36 of 39 L, 37 of 43 W-). It only ever goes down: pinning a row
// with a real test deletes its ID from todoPinnedRowsAtFreeze and lowers this
// by one.
const ledgerTODOPinCeiling = 85

// todoPinnedRowsAtFreeze is WHICH rows were TODO-pinned when the ratchet froze,
// sorted. Recording the set, not just the count, means pinning one row and
// adding a new TODO row cannot hide behind an unchanged total.
var todoPinnedRowsAtFreeze = []string{
	"F-blocked",
	"F-close",
	"F-count",
	"F-mol-ready",
	"F-partial-id",
	"F-ready-explain",
	"F-ready-gated",
	"F-ready-mol",
	"F-serve",
	"F-show-children",
	"F-show-refs",
	"F-show-thread",
	"L-apply-ref",
	"L-apply-snapshot",
	"L-batchcreate-bound",
	"L-batchcreate-inbatch",
	"L-batchcreate-target",
	"L-claim-prose",
	"L-close-cap",
	"L-comment-author",
	"L-config-bounds",
	"L-create-notfound",
	"L-create-prefix",
	"L-cycles-tracks",
	"L-delete-bound",
	"L-delete-dependents",
	"L-delete-notfound",
	"L-dep-endpoint",
	"L-edgecount-bound",
	"L-events-paging",
	"L-release-notclaimed",
	"L-release-notowner",
	"L-update-fieldlen",
	"L10",
	"L11",
	"L13",
	"L14",
	"L15",
	"L16",
	"L18",
	"L2",
	"L3",
	"L4",
	"L5",
	"L6",
	"L7",
	"L8",
	"L9",
	"W-AddDependenciesRequest.SkipPerEdgeCycleCheck",
	"W-ApplyPatch.ParentID",
	"W-BatchCreateItem.Issue",
	"W-ClaimRequest.Wisp",
	"W-CloseBatchRequest.ClaimNext",
	"W-CloseRequest.ExpectedVersion",
	"W-CreateBatchRequest.ForceIDPrefix",
	"W-CreateBatchRequest.Provenance",
	"W-CreateDependency.Metadata",
	"W-CreateDependency.Reverse",
	"W-CreateDependency.ThreadID",
	"W-CreateItem.Issue",
	"W-CreateRequest.IDPrefix",
	"W-CreateRequest.Issue",
	"W-DeleteRequest.ExpectedVersion",
	"W-DepAddItem.HasSpawner",
	"W-DepAddItem.ThreadID",
	"W-IssuePatch.Assignee",
	"W-IssuePatch.ClosedBySession",
	"W-IssuePatch.Labels",
	"W-IssuePatch.Metadata",
	"W-IssuePatch.Owner",
	"W-IssuePatch.ParentID",
	"W-IssuePatch.Persistence",
	"W-IssuePatch.SpecID",
	"W-IssuePatch.Status",
	"W-ReopenRequest.ExpectedVersion",
	"W-ReopenRequest.Provenance",
	"W-UpdateRequest.Claim",
	"W-UpdateRequest.ExpectedAssignee",
	"W-UpdateRequest.ExpectedStatus",
	"W-UpdateRequest.ExpectedVersion",
	"W-UpdateRequest.ForceAssigneeTransfer",
	"W-UpdateRequest.ForceClosePolicy",
	"W-UpdateRequest.ForceNotesOverwrite",
	"W-UpdateRequest.IssuePlaneOnly",
	"W-UpdateRequest.Provenance",
}

// todoPinRatchetProblems compares the TODO-pinned rows with the frozen set and
// ceiling and returns the problems, sorted.
func todoPinRatchetProblems(rows []Row, frozen []string, ceiling int) []string {
	var problems []string
	if len(frozen) != ceiling {
		problems = append(problems, fmt.Sprintf("todoPinnedRowsAtFreeze lists %d rows but ledgerTODOPinCeiling is %d; "+
			"they are one fact, and both only ever go down", len(frozen), ceiling))
	}
	onList := map[string]bool{}
	for _, id := range frozen {
		if onList[id] {
			problems = append(problems, fmt.Sprintf("todoPinnedRowsAtFreeze lists %s twice", id))
		}
		onList[id] = true
	}
	todo := map[string]bool{}
	for _, row := range rows {
		if !isTODOPin(row.PinnedBy) {
			continue
		}
		todo[row.ID] = true
		if !onList[row.ID] {
			problems = append(problems, fmt.Sprintf("%s is TODO-pinned and was not when the ratchet froze; a new or "+
				"re-pinned row needs a real test as its PinnedBy (in this package, or registered in crossPackagePins)", row.ID))
		}
	}
	if len(todo) > ceiling {
		problems = append(problems, fmt.Sprintf("%d ledger rows are TODO-pinned, above the frozen ceiling of %d", len(todo), ceiling))
	}
	for id := range onList {
		if !todo[id] {
			problems = append(problems, fmt.Sprintf("%s is on todoPinnedRowsAtFreeze but is no longer TODO-pinned; "+
				"delete it and lower ledgerTODOPinCeiling by one, so the TODO population cannot grow back", id))
		}
	}
	sort.Strings(problems)
	return problems
}

// TestLedgerTODOPinsNeverGrow freezes the TODO-pinned ledger rows, in the
// TestPretokenBaselinesNeverGrow pattern: a ledger row whose pin is a TODO is
// a promise, not a test, and the number of those only ever goes down. A new
// row arrives with a real test as its pin; a row that gains one leaves the
// frozen set and lowers the ceiling.
func TestLedgerTODOPinsNeverGrow(t *testing.T) {
	for _, problem := range todoPinRatchetProblems(Ledger(), todoPinnedRowsAtFreeze, ledgerTODOPinCeiling) {
		t.Error(problem)
	}
}

// TestLedgerTODOPinRatchetFires is the ratchet's own falsification: synthetic
// rows prove it still catches growth, a swap (pin one, add one) and a stale
// frozen list, independent of what the real ledger currently says.
func TestLedgerTODOPinRatchetFires(t *testing.T) {
	const todo = "TODO(bd-synthetic): no test yet"
	rows := func(pins ...string) []Row {
		out := make([]Row, 0, len(pins))
		for i, pin := range pins {
			out = append(out, Row{ID: string(rune('A' + i)), PinnedBy: pin})
		}
		return out
	}
	cases := []struct {
		name    string
		rows    []Row
		frozen  []string
		ceiling int
		want    []string
	}{
		{name: "unchanged", rows: rows(todo, todo, "TestReal"), frozen: []string{"A", "B"}, ceiling: 2},
		{name: "grown", rows: rows(todo, todo, todo), frozen: []string{"A", "B"}, ceiling: 2,
			want: []string{"3 ledger rows are TODO-pinned, above the frozen ceiling of 2", "C is TODO-pinned and was not"}},
		{name: "swap keeps the count", rows: rows(todo, "TestReal", todo), frozen: []string{"A", "B"}, ceiling: 2,
			want: []string{"B is on todoPinnedRowsAtFreeze", "C is TODO-pinned and was not"}},
		{name: "pinned but not removed", rows: rows(todo, "TestReal"), frozen: []string{"A", "B"}, ceiling: 2,
			want: []string{"B is on todoPinnedRowsAtFreeze"}},
		{name: "list and ceiling disagree", rows: []Row{{ID: "A", PinnedBy: todo}, {ID: "B", PinnedBy: todo}, {ID: "D", PinnedBy: todo}},
			frozen: []string{"A", "B", "D"}, ceiling: 2,
			want: []string{"3 ledger rows are TODO-pinned, above the frozen ceiling of 2", "todoPinnedRowsAtFreeze lists 3 rows but ledgerTODOPinCeiling is 2"}},
		{name: "duplicate list entry", rows: rows(todo, todo), frozen: []string{"A", "A"}, ceiling: 2,
			want: []string{"todoPinnedRowsAtFreeze lists A twice", "B is TODO-pinned and was not"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertProblems(t, todoPinRatchetProblems(tc.rows, tc.frozen, tc.ceiling), tc.want)
		})
	}
}
