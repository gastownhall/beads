package issueops

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// With versioned history on, the mint runs in the same transaction as the
// mutation that reaches it, so refusing at the mint IS refusing the write: a
// number outside the I-JSON range aborts the writer's own transaction and
// nothing it did is committed. The mint reads the whole issue, so the number
// can be in any field that holds one: in metadata, or in a gate's timeout,
// which encoding/json writes as a count of nanoseconds. The mint's reads run
// first and its two writes, the version row and the current_revision advance,
// come last, so a refusal must arrive after the reads and before either write.
//
// The mock scripts exactly the reads. It expects no INSERT into issue_versions
// and no UPDATE of issues.current_revision, so a mint that wrote before it
// refused would make the mock fail the call with an unexpected-statement error
// instead of the refusal asserted here. The caller's own rollback closes the
// transaction, as every store does when the mint returns an error.
//
// The refusal names the top-level field the number was found in, as well as the
// literal, so a person who reads it knows which part of the issue to fix.
func TestRecordVersionInTxRefusesANumberOutsideTheRangeAndWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		column  string
		value   driver.Value
		literal string
		field   string
	}{
		{"a number in metadata", "metadata", `{"ts":1727000000000000000}`, "1727000000000000000", "metadata"},
		{"a gate timeout past 2^53-1 nanoseconds", "timeout_ns", int64(2600 * time.Hour), "9360000000000000", "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "bd-9"
			_, mock, tx := beginMockTx(t)
			defer ScopeVersionedHistoryTransaction(tx, true)()

			values := issueRowValues(id, "carries an out-of-range number")
			for i, col := range issueColumns() {
				if col == tc.column {
					values[i] = tc.value
				}
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT " + IssueSelectColumns + " FROM issues " + sqlbuild.LeaseJoin("issues") + " WHERE id = ?")).
				WithArgs(id).
				WillReturnRows(issueRows().AddRow(values...))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT label FROM labels WHERE issue_id = ? ORDER BY label")).
				WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"label"}))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps LIMIT 1")).
				WillReturnRows(sqlmock.NewRows([]string{"1"}))
			mock.ExpectQuery(`FROM dependencies WHERE issue_id IN`).
				WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"issue_id", "depends_on_id", "type", "created_at", "created_by", "metadata", "thread_id"}))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT epoch FROM store_epoch WHERE id = 1")).
				WillReturnRows(sqlmock.NewRows([]string{"epoch"}).AddRow(1))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(revision), 0) + 1 FROM issue_versions WHERE issue_id = ?")).
				WithArgs(id).
				WillReturnRows(sqlmock.NewRows([]string{"next"}).AddRow(1))
			mock.ExpectRollback()

			err := RecordVersionInTx(context.Background(), tx, id, "actor")
			if !errors.Is(err, ErrIntegerNotRepresentable) {
				t.Fatalf("RecordVersionInTx = %v, want ErrIntegerNotRepresentable", err)
			}
			if !strings.Contains(err.Error(), id) {
				t.Errorf("refusal %q does not name the issue %s", err, id)
			}
			if !strings.Contains(err.Error(), tc.literal) {
				t.Errorf("refusal %q does not name the offending literal %s", err, tc.literal)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%q", tc.field)) {
				t.Errorf("refusal %q does not name the field %q the number was found in", err, tc.field)
			}
			if rbErr := tx.Rollback(); rbErr != nil {
				t.Fatalf("rolling back the writer's transaction: %v", rbErr)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("the mint read or wrote something other than the scripted reads: %v", err)
			}
		})
	}
}

// refusalClass names what an admission verdict is, so two functions can be
// compared for agreement without depending on their exact wording.
func refusalClass(err error) string {
	switch {
	case err == nil:
		return "admitted"
	case errors.Is(err, ErrIntegerNotRepresentable):
		return "number outside the I-JSON range"
	case strings.Contains(err.Error(), "Duplicate key"):
		return "duplicate key"
	default:
		return "other refusal"
	}
}

func issueWithTimeout(timeout time.Duration) types.Issue {
	return types.Issue{ID: "be-1", Title: "t", IssueType: types.TypeGate, Timeout: timeout}
}

// The check the pre-enable scan runs and the check the mint runs must never
// disagree, or a store could pass the scan, be switched on, and then refuse the
// first write to the row the scan cleared. So the check is the mint's own two
// steps behind one exported name, and this pins that they agree on every shape
// of issue.
//
// The domain is the WHOLE issue, because that is what the mint canonicalizes and
// what the scan hands the check. Metadata covers a number past the range, a
// fraction rounding past it, duplicate keys at the top level and nested,
// ordinary values, null, empty and an empty object, and text that is not JSON at
// all. The other field that can reach the range is a gate's timeout, a
// time.Duration that encoding/json writes as nanoseconds: 2^53-1 ns is about
// 104.25 days, so 2500h is admitted and 2600h is not.
func TestCheckIssueVersionableAgreesWithTheMint(t *testing.T) {
	const maxExact = 1<<53 - 1 // 9007199254740991 ns, the last whole nanosecond count in range
	for _, tc := range []struct {
		name  string
		issue types.Issue
		want  string
	}{
		{"number past the range", issueWithMetadata(`{"ts":1727000000000000000}`), "number outside the I-JSON range"},
		{"fraction rounding past the range", issueWithMetadata(`{"n":9007199254740993.5}`), "number outside the I-JSON range"},
		{"overflowing exponent", issueWithMetadata(`{"n":1e400}`), "number outside the I-JSON range"},
		{"number nested in an array", issueWithMetadata(`{"a":[1,{"b":-9007199254740993}]}`), "number outside the I-JSON range"},
		{"duplicate key", issueWithMetadata(`{"k":1,"k":2}`), "duplicate key"},
		{"nested duplicate key", issueWithMetadata(`{"a":{"k":1,"k":2}}`), "duplicate key"},
		{"ordinary values", issueWithMetadata(`{"a":1,"b":[0.1,2,"x"],"c":{"d":null}}`), "admitted"},
		{"number at the bound", issueWithMetadata(`{"n":9007199254740991}`), "admitted"},
		{"tiny magnitude", issueWithMetadata(`{"n":1e-400}`), "admitted"},
		{"null", issueWithMetadata(`null`), "admitted"},
		{"empty object", issueWithMetadata(`{}`), "admitted"},
		{"empty", issueWithMetadata(``), "admitted"},
		{"text that is not JSON", issueWithMetadata(`{"a":`), "other refusal"},

		{"no timeout", issueWithTimeout(0), "admitted"},
		{"timeout 2500h", issueWithTimeout(2500 * time.Hour), "admitted"},
		{"timeout 2600h", issueWithTimeout(2600 * time.Hour), "number outside the I-JSON range"},
		{"timeout 8760h", issueWithTimeout(8760 * time.Hour), "number outside the I-JSON range"},
		{"negative timeout -2600h", issueWithTimeout(-2600 * time.Hour), "number outside the I-JSON range"},
		{"timeout of exactly 2^53-1 ns", issueWithTimeout(time.Duration(maxExact)), "admitted"},
		{"timeout of exactly 2^53 ns", issueWithTimeout(time.Duration(maxExact + 1)), "number outside the I-JSON range"},
		{"timeout in range beside ordinary metadata", func() types.Issue {
			issue := issueWithTimeout(time.Hour)
			issue.Metadata = json.RawMessage(`{"ok":1}`)
			return issue
		}(), "admitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := CheckIssueVersionable(&tc.issue)
			_, mint := canonicalDurableState(tc.issue)
			if got := refusalClass(check); got != tc.want {
				t.Errorf("CheckIssueVersionable = %v (%s), want %s", check, got, tc.want)
			}
			if got := refusalClass(mint); got != tc.want {
				t.Errorf("canonicalDurableState = %v (%s), want %s", mint, got, tc.want)
			}
			if refusalClass(check) != refusalClass(mint) {
				t.Errorf("the pre-enable check and the mint disagree: check %v, mint %v", check, mint)
			}
		})
	}
}

// The scan runs the check over every issue the mint would version and skips
// the ones it would not, by the mint's own rule (IsWisp): ephemeral rows and
// no-history rows are never versioned, so a poisoned value in one can never
// abort a write and is not an obstacle to switching history on. The scan
// reports what it found in the order it was given, names each offender with
// the refusal the mint would return and the field it was found in, and finds
// nothing in a clean store.
//
// A gate whose timeout is over 105 days is the case that motivated checking the
// whole issue: its timeout is past 2^53-1 ns, so the mint refuses every write to
// it, and a scan that looked only at metadata cleared the switch over it.
func TestFindUnversionableFindsWhatTheMintWouldRefuse(t *testing.T) {
	oversize := json.RawMessage(`{"ts":1727000000000000000}`)
	overlong := 105 * 24 * time.Hour
	issues := []*types.Issue{
		{ID: "bd-1", Metadata: oversize},
		{ID: "bd-2", Metadata: json.RawMessage(`{"k":1,"k":2}`)},
		{ID: "bd-3", Metadata: json.RawMessage(`{"ok":1}`)},
		{ID: "bd-4", Ephemeral: true, Metadata: oversize},
		{ID: "bd-5", NoHistory: true, Metadata: oversize},
		{ID: "bd-6"},
		{ID: "bd-7", Metadata: json.RawMessage(`{"n":9007199254740993.5}`)},
		{ID: "bd-8", Ephemeral: true, WispPlaneOverride: boolPtr(false), Metadata: oversize},
		{ID: "bd-9", WispPlaneOverride: boolPtr(true), Metadata: oversize},
		{ID: "bd-10", IssueType: types.TypeGate, Timeout: overlong},
		{ID: "bd-11", IssueType: types.TypeGate, Timeout: 2500 * time.Hour},
		{ID: "bd-12", IssueType: types.TypeGate, Ephemeral: true, Timeout: overlong},
		{ID: "bd-13", IssueType: types.TypeGate, Timeout: -overlong},
	}

	got := FindUnversionable(issues)

	want := []struct {
		id    string
		class string
		field string
	}{
		{"bd-1", "number outside the I-JSON range", "metadata"},
		{"bd-2", "duplicate key", "metadata"},
		{"bd-7", "number outside the I-JSON range", "metadata"},
		// The override decides which plane the mint routes an issue to, and the
		// scan follows it: bd-8 is versioned despite Ephemeral, bd-9 is not.
		{"bd-8", "number outside the I-JSON range", "metadata"},
		{"bd-10", "number outside the I-JSON range", "timeout"},
		{"bd-13", "number outside the I-JSON range", "timeout"},
	}
	if len(got) != len(want) {
		t.Fatalf("FindUnversionable found %d issues %v, want %d", len(got), unversionableIDs(got), len(want))
	}
	for i, w := range want {
		if got[i].ID != w.id {
			t.Fatalf("offender %d is %s, want %s (order of the input); got %v", i, got[i].ID, w.id, unversionableIDs(got))
		}
		if class := refusalClass(got[i].Err); class != w.class {
			t.Errorf("%s: refusal class %q (%v), want %q", w.id, class, got[i].Err, w.class)
		}
		if got[i].Field != w.field {
			t.Errorf("%s: field %q, want %q", w.id, got[i].Field, w.field)
		}
		if !strings.Contains(got[i].Err.Error(), fmt.Sprintf("%q", w.field)) {
			t.Errorf("%s: refusal %q does not name the field %q", w.id, got[i].Err, w.field)
		}
	}

	clean := []*types.Issue{
		{ID: "bd-1", Metadata: json.RawMessage(`{"ok":1,"f":0.1}`)},
		{ID: "bd-2"},
		{ID: "bd-3", Metadata: json.RawMessage(`null`)},
		{ID: "bd-4", Ephemeral: true, Metadata: oversize},
		{ID: "bd-5", IssueType: types.TypeGate, Timeout: 2500 * time.Hour},
		{ID: "bd-6", IssueType: types.TypeGate, Ephemeral: true, Timeout: overlong},
	}
	if found := FindUnversionable(clean); len(found) != 0 {
		t.Fatalf("a store with nothing the mint would refuse reported %v", unversionableIDs(found))
	}
	if found := FindUnversionable(nil); len(found) != 0 {
		t.Fatalf("no issues reported %v", unversionableIDs(found))
	}
}

func unversionableIDs(found []UnversionableIssue) []string {
	ids := make([]string, 0, len(found))
	for _, f := range found {
		ids = append(ids, f.ID)
	}
	return ids
}

// The scan does not hand the check the issue the mint sees. It reads a lite
// projection (the heavy text columns are left out, labels are not loaded) and
// never loads dependencies, which the mint adds before it canonicalizes. That is
// sound only while everything the scan leaves out is free of numbers: a number in
// a field the scan never loads is one it can never refuse and the mint will. The
// next three tests are how that stays true. They fail, with the way out, the day
// a field is added that breaks it.

// fieldByJSONName finds the exported field of the struct type typ that
// encoding/json writes under name.
func fieldByJSONName(typ reflect.Type, name string) (reflect.StructField, bool) {
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		jsonName := strings.Split(tag, ",")[0]
		if jsonName == "" {
			jsonName = f.Name
		}
		if jsonName == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// heavyColumnProblems maps each column to its field of the issue type and reports
// every one that is missing or that can hold a number. A string or a slice of
// strings cannot: encoding/json writes both without a JSON number in them.
func heavyColumnProblems(columns []string, issue reflect.Type) []string {
	var problems []string
	for _, col := range columns {
		f, ok := fieldByJSONName(issue, col)
		if !ok {
			problems = append(problems, fmt.Sprintf("column %q has no %s field written under that name: map it, then say whether it can hold a number", col, issue))
			continue
		}
		numberFree := f.Type.Kind() == reflect.String ||
			(f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.String)
		if !numberFree {
			problems = append(problems, fmt.Sprintf("column %q is %s.%s (%s), which can hold a number the scan never reads but the mint refuses: load it in the scan or classify it lite", col, issue, f.Name, f.Type))
		}
	}
	return problems
}

// TestTheScanProjectionOmitsOnlyNumberFreeFields pins that every column the
// scan's lite projection leaves out (HeavyDropList) is a string or a list of
// strings on the issue.
func TestTheScanProjectionOmitsOnlyNumberFreeFields(t *testing.T) {
	if problems := heavyColumnProblems(HeavyDropList, reflect.TypeOf(types.Issue{})); len(problems) > 0 {
		t.Fatalf("the scan's projection leaves out a column that can hold a number:\n  %s", strings.Join(problems, "\n  "))
	}
}

// The guard above would pass vacuously if it never found anything to refuse, so
// it is run once against columns that must be refused: numbers, metadata (a
// verbatim JSON document), a column that maps to nothing, and the columns that
// really are number-free.
func TestTheHeavyColumnGuardRefusesWhatCanHoldANumber(t *testing.T) {
	issue := reflect.TypeOf(types.Issue{})
	for _, col := range []string{"priority", "estimated_minutes", "timeout", "metadata", "created_at_unix"} {
		if problems := heavyColumnProblems([]string{col}, issue); len(problems) != 1 {
			t.Errorf("column %q: got %d problems %v, want exactly one", col, len(problems), problems)
		}
	}
	for _, col := range []string{"description", "design", "acceptance_criteria", "notes", "waiters", "payload"} {
		if problems := heavyColumnProblems([]string{col}, issue); len(problems) != 0 {
			t.Errorf("column %q is number-free but was refused: %v", col, problems)
		}
	}
	problems := heavyColumnProblems([]string{"priority"}, issue)
	if len(problems) == 1 && !strings.Contains(problems[0], "load it in the scan or classify it lite") {
		t.Errorf("the refusal %q does not say how to resolve it", problems[0])
	}
}

var (
	timeType       = reflect.TypeOf(time.Time{})
	rawMessageType = reflect.TypeOf(json.RawMessage(nil))
)

// numberBearingPaths walks typ the way encoding/json does and returns the path of
// everything that can write a JSON number or a value this walk cannot see into:
// an integer, unsigned integer or float (a time.Duration is an int64), a
// json.RawMessage, an interface, a map. It recurses through slices, arrays,
// pointers and structs, skips what json:"-" leaves out and the unexported fields,
// and treats a time.Time as the string it is written as.
func numberBearingPaths(typ reflect.Type, path string, onPath map[reflect.Type]bool) []string {
	switch typ {
	case timeType:
		return nil
	case rawMessageType:
		return []string{path + " is a json.RawMessage, passed through verbatim"}
	}
	switch typ.Kind() {
	case reflect.Bool, reflect.String:
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return []string{fmt.Sprintf("%s is a %s", path, typ)}
	case reflect.Interface, reflect.Map:
		return []string{fmt.Sprintf("%s is a %s", path, typ.Kind())}
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return numberBearingPaths(typ.Elem(), path, onPath)
	case reflect.Struct:
		if onPath[typ] {
			return nil
		}
		onPath[typ] = true
		defer delete(onPath, typ)
		var found []string
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() || f.Tag.Get("json") == "-" {
				continue
			}
			found = append(found, numberBearingPaths(f.Type, path+"."+f.Name, onPath)...)
		}
		return found
	default:
		return []string{fmt.Sprintf("%s is a %s, which this walk cannot classify", path, typ.Kind())}
	}
}

// TestWhatTheMintAddsAndTheScanDoesNotLoadCarriesNoNumber covers the other half
// of what the scan leaves out. The mint adds the issue's dependencies before it
// canonicalizes, and the scan reads without labels, so a number anywhere in a
// Dependency, a Comment, a BondRef or a label would be refused by the mint and
// invisible to the scan. None carries one today; a field that could (a Weight
// int on Dependency) fails here, and the person who adds it decides whether the
// scan has to load it.
func TestWhatTheMintAddsAndTheScanDoesNotLoadCarriesNoNumber(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"types.Dependency", reflect.TypeOf(types.Dependency{})},
		{"types.Comment", reflect.TypeOf(types.Comment{})},
		{"types.BondRef", reflect.TypeOf(types.BondRef{})},
		{"a label", reflect.TypeOf("")},
		{"the labels of an issue", reflect.TypeOf([]string(nil))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if found := numberBearingPaths(tc.typ, tc.name, map[reflect.Type]bool{}); len(found) > 0 {
				t.Fatalf("a field the mint writes and the scan does not load can hold a number:\n  %s", strings.Join(found, "\n  "))
			}
		})
	}
}

// The walk is run against shapes it must flag and shapes it must not, for the
// same reason as the guard above.
func TestTheNumberFreeWalkFlagsEveryNumberBearingShape(t *testing.T) {
	type weighted struct {
		Weight int `json:"weight"`
	}
	type unsigned struct{ N uint32 }
	type scored struct{ Score float64 }
	type waiting struct{ Wait time.Duration }
	type verbatim struct{ Blob json.RawMessage }
	type anything struct{ Value any }
	type keyed struct{ Extra map[string]string }
	type nested struct{ Deps []*weighted }
	type tagged struct {
		Name    string    `json:"name"`
		Tags    []string  `json:"tags"`
		At      time.Time `json:"at"`
		Skipped int       `json:"-"`
		_       int
	}
	type recursive struct{ Next *recursive }

	for name, typ := range map[string]reflect.Type{
		"an int":                      reflect.TypeOf(weighted{}),
		"an unsigned integer":         reflect.TypeOf(unsigned{}),
		"a float":                     reflect.TypeOf(scored{}),
		"a time.Duration":             reflect.TypeOf(waiting{}),
		"a json.RawMessage":           reflect.TypeOf(verbatim{}),
		"an interface":                reflect.TypeOf(anything{}),
		"a map":                       reflect.TypeOf(keyed{}),
		"an int behind slice+pointer": reflect.TypeOf(nested{}),
	} {
		if found := numberBearingPaths(typ, "x", map[reflect.Type]bool{}); len(found) == 0 {
			t.Errorf("%s was not flagged", name)
		}
	}
	for name, typ := range map[string]reflect.Type{
		"strings, a time, a skipped int and an unexported int": reflect.TypeOf(tagged{}),
		"a type that contains itself":                          reflect.TypeOf(recursive{}),
	} {
		if found := numberBearingPaths(typ, "x", map[reflect.Type]bool{}); len(found) != 0 {
			t.Errorf("%s was flagged: %v", name, found)
		}
	}
}

// There is ONE check, CheckIssueVersionable, and the scan and the mint both go
// through it. A check over a part of the issue, the metadata alone as there once
// was, can pass what the mint refuses, and an exported one invites reuse by the
// next caller (a doctor check, say) that then inherits the gap. So no exported
// function in the package may be named for versionability beside the check and
// the scan that calls it.
func TestNoExportedCheckOverASubPartOfTheIssueExistsBesideCheckIssueVersionable(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse issueops package: %v", err)
	}
	allowed := map[string]bool{"CheckIssueVersionable": true, "FindUnversionable": true}
	seen := map[string]bool{}
	var stray []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !fn.Name.IsExported() {
					continue
				}
				if !strings.Contains(strings.ToLower(fn.Name.Name), "versionable") {
					continue
				}
				seen[fn.Name.Name] = true
				if !allowed[fn.Name.Name] {
					stray = append(stray, fmt.Sprintf("%s (%s)", fn.Name.Name, fset.Position(fn.Pos())))
				}
			}
		}
	}
	sort.Strings(stray)
	if len(stray) > 0 {
		t.Errorf("an exported check over part of the issue exists beside CheckIssueVersionable: %s", strings.Join(stray, ", "))
	}
	for name := range allowed {
		if !seen[name] {
			t.Errorf("%s was not found: this guard no longer sees the check it protects", name)
		}
	}
}
