// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/encode/bijection_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package encode

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/httpapi/spec"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// GATE 1: the reflection bijection.
//
// This is the ONLY defense against a silently dropped flag. A round-trip
// fixture cannot be one: a filter nobody thought to fixture round-trips
// perfectly by being absent from both sides, and the server can only reject
// parameters it RECEIVES — so the field an encoder never learned about is
// invisible from every direction except this one.
//
// It runs four ways, and each catches a different way the classification can go
// stale:
//
//	upstream -> here   every populatable field of every request shape is in the
//	                   encoder table. A field added to issueops.ListRequest
//	                   tomorrow fails here rather than shipping as a widened
//	                   result set.
//	here -> upstream   every table entry and every field-shaped ledger row names
//	                   a field that still exists. A renamed field fails rather
//	                   than leaving a rule that matches nothing.
//	document -> here   every query parameter the operation publishes is driven
//	                   by a field or explicitly reserved. A parameter added to
//	                   the wire fails rather than staying unreachable.
//	table -> encoder   populating one field at a time makes the encoder behave
//	                   the way the table says it does.
//
// COMPLETENESS IS THE TEST'S OWN INVARIANT. An empty table set, an empty table,
// an empty ledger and a ledger row nothing classifies are all failures here —
// otherwise the cheapest way to make this gate green would be to delete its
// input.

func TestEncoderTableClassifiesEveryRequestField(t *testing.T) {
	tables := Tables()
	if len(tables) == 0 {
		t.Fatal("the encoder table is empty; there is nothing for this gate to check")
	}

	seenPrimary := map[Op]bool{}
	for _, table := range tables {
		t.Run(tableName(table), func(t *testing.T) {
			if len(table.Fields) == 0 {
				t.Fatal("table has no field entries")
			}
			if table.Source == nil || table.Source.Kind() != reflect.Struct {
				t.Fatalf("Source %v is not a struct type", table.Source)
			}
			if table.Target == nil || table.Target.Kind() != reflect.Struct {
				t.Fatalf("Target %v is not a struct type", table.Target)
			}

			classified := map[string]bool{}
			for _, entry := range table.Fields {
				if classified[entry.Name] {
					t.Errorf("field %s is classified twice", entry.Name)
				}
				classified[entry.Name] = true
				checkEntry(t, table, entry)
			}

			for _, name := range populatableFields(table.Source) {
				if !classified[name] {
					t.Errorf("%s.%s is populatable and UNCLASSIFIED.\n"+
						"Add it to the %s table with a disposition, or refuse it with a divergence-ledger row.\n"+
						"An unclassified field is a filter this client would drop silently — see engdocs/design/http-client-backend.md D7 and D9 L12.",
						table.Source.Name(), name, tableName(table))
				}
			}
			for name := range classified {
				if _, ok := table.Source.FieldByName(name); !ok {
					t.Errorf("table entry %s.%s names a field that does not exist", table.Source.Name(), name)
				}
			}
		})

		if table.Primary {
			if seenPrimary[table.Op] {
				t.Errorf("operation %s has more than one primary table", table.Op)
			}
			seenPrimary[table.Op] = true
		}
	}

	for _, op := range []Op{OpListReadyWork, OpCountReadyWork, OpListIssues, OpQueryIssues, OpCountIssues, OpGetIssue} {
		if !seenPrimary[op] {
			t.Errorf("operation %s has no primary table, so nothing checks it against the document", op)
		}
	}
}

func checkEntry(t *testing.T, table Table, entry FieldEntry) {
	t.Helper()
	where := fmt.Sprintf("%s.%s", table.Source.Name(), entry.Name)

	switch entry.Disposition {
	case DispParam, DispPath:
		if entry.Param == "" {
			t.Errorf("%s: %s entry names no parameter", where, entry.Disposition)
		}
		if entry.Decoded == "" {
			t.Errorf("%s: %s entry names no decoded field", where, entry.Disposition)
			return
		}
		if _, ok := table.Target.FieldByName(entry.Decoded); !ok {
			t.Errorf("%s: decoded field %s.%s does not exist", where, table.Target.Name(), entry.Decoded)
		}
	case DispServerFixed:
		if entry.Fixed == "" {
			t.Errorf("%s: server-fixed entry names no value", where)
		}
		if entry.Why == "" {
			t.Errorf("%s: server-fixed entry carries no reason", where)
		}
	case DispClientSide:
		if entry.Why == "" {
			t.Errorf("%s: client-side entry carries no reason, so nothing says which machinery honors it", where)
		}
	case DispNested:
		if entry.Why == "" {
			t.Errorf("%s: nested entry carries no reason, so nothing says why the shape is delegated", where)
			return
		}
		if entry.Decoded == "" {
			t.Errorf("%s: nested entry names no decoded field", where)
			return
		}
		if _, ok := table.Target.FieldByName(entry.Decoded); !ok {
			t.Errorf("%s: decoded field %s.%s does not exist", where, table.Target.Name(), entry.Decoded)
		}
		// The delegation has to LAND somewhere. A nested entry naming a shape no
		// table classifies would read as coverage while classifying nothing —
		// the same leftover the ledger's own completeness arms exist to catch.
		field, ok := table.Source.FieldByName(entry.Name)
		if !ok {
			return // the missing-field arm above already reported it.
		}
		delegated := false
		for _, other := range Tables() {
			if other.Source == field.Type {
				delegated = true
				break
			}
		}
		if !delegated {
			t.Errorf("%s: nested entry delegates to %s, which no encoder table classifies", where, field.Type)
		}
	case DispDropped, DispRefused, DispInverted:
		if entry.Ledger == "" {
			t.Fatalf("%s: %s entry cites no divergence-ledger row", where, entry.Disposition)
		}
		if entry.Disposition == DispInverted && entry.Why == "" {
			t.Errorf("%s: inverted entry carries no reason, so nothing says which derivation re-produces it", where)
		}
		row := findRow(t, entry.Ledger)
		if row.Type != nil && (row.Type != table.Source || row.Field != entry.Name) {
			t.Errorf("%s: cites ledger row %s, which is about %s.%s", where, row.ID, row.Type.Name(), row.Field)
		}
		wantKind := KindRefuse
		if entry.Disposition == DispDropped {
			wantKind = KindDegrade
		}
		if row.Kind != wantKind {
			t.Errorf("%s: %s entry cites ledger row %s of kind %q, want %q", where, entry.Disposition, row.ID, row.Kind, wantKind)
		}
	default:
		t.Errorf("%s: unknown disposition %q", where, entry.Disposition)
	}
}

func TestLedgerRowsAreWellFormed(t *testing.T) {
	rows := Ledger()
	if len(rows) == 0 {
		t.Fatal("divergence ledger v1 is empty")
	}

	tests := testFunctionsInThisPackage(t)
	ids := map[string]bool{}
	for _, row := range rows {
		if row.ID == "" {
			t.Fatalf("a ledger row carries no id: %+v", row)
		}
		if ids[row.ID] {
			t.Errorf("duplicate ledger row id %q", row.ID)
		}
		ids[row.ID] = true

		switch row.Kind {
		case KindRefuse, KindDegrade, KindRetired:
		default:
			t.Errorf("%s: unknown kind %q", row.ID, row.Kind)
		}
		for name, value := range map[string]string{"What": row.What, "Why": row.Why, "SpecRow": row.SpecRow, "PinnedBy": row.PinnedBy} {
			if strings.TrimSpace(value) == "" {
				// PinnedBy especially: D9's whole discipline is that a ledger
				// row without a pin is a wish. A TODO naming the owning bead is
				// an acceptable pin; an empty string is not.
				t.Errorf("%s: %s is empty", row.ID, name)
			}
		}
		// A pin is a TEST or a bead, never a hopeful sentence. A row naming a
		// test that does not exist is exactly the shape of a ledger that has
		// drifted from the suite it claims to be held to.
		if !strings.HasPrefix(row.PinnedBy, "TODO(") && !tests[row.PinnedBy] {
			t.Errorf("%s: PinnedBy %q is neither a test in this package nor a TODO(<bead>)", row.ID, row.PinnedBy)
		}
		if (row.Type == nil) != (row.Field == "") {
			t.Errorf("%s: Type and Field must be set together (Type=%v Field=%q)", row.ID, row.Type, row.Field)
		}
		if row.Type != nil {
			if _, ok := row.Type.FieldByName(row.Field); !ok {
				t.Errorf("%s: names %s.%s, which does not exist — the field was renamed or removed and this row now matches nothing",
					row.ID, row.Type.Name(), row.Field)
			}
		}
	}
}

// TestNoLedgerRowIsUnknownToTheEncoderTable is the other half of the ledger
// bijection: a row about a field of a shape this package encodes must be a
// field that shape's table actually classifies.
//
// Without it the ledger could accumulate rows for fields nobody encodes, and a
// reader auditing "is this refusal real?" would have no way to tell a live rule
// from a leftover. Rows about the WRITE shapes (D8's refuse-not-drop
// enumeration) are deliberately exempt: this package encodes no request bodies,
// and their reflection check above is what keeps them honest.
func TestNoLedgerRowIsUnknownToTheEncoderTable(t *testing.T) {
	encoded := map[reflect.Type]map[string]bool{}
	for _, table := range Tables() {
		if encoded[table.Source] == nil {
			encoded[table.Source] = map[string]bool{}
		}
		for _, entry := range table.Fields {
			encoded[table.Source][entry.Name] = true
		}
	}

	for _, row := range Ledger() {
		if row.Type == nil {
			continue
		}
		fields, ok := encoded[row.Type]
		if !ok {
			continue // a write-shape row; see the doc comment.
		}
		if !fields[row.Field] {
			t.Errorf("ledger row %s is about %s.%s, which no encoder table classifies",
				row.ID, row.Type.Name(), row.Field)
		}
	}
}

// TestEveryFieldShapedLedgerRowIsCitedByTheTable is the ledger's other
// completeness direction, and the one this package's doc comment claimed before
// anything checked it: "a row nothing references fails the gate too".
//
// The check above proves a row's FIELD is still classified. This proves the ROW
// is still reached — that some table entry names it — which is what separates a
// live rule from a leftover. Without it the ledger accumulates rows for
// dispositions that changed underneath them, and a reader auditing "is this
// refusal real?" has no way to tell.
//
// Rows about the WRITE shapes stay exempt for the reason the check above exempts
// them: this package encodes no request bodies, so no table of its could cite
// one. Their completeness against the wire's own member lists is a guard the
// lifecycle wiring owns (D8 refuse-not-drop).
func TestEveryFieldShapedLedgerRowIsCitedByTheTable(t *testing.T) {
	// Rows a table cannot cite because the rule is about a VALUE rather than a
	// field's presence, so the encoder raises them from code.
	raisedFromCode := map[string]string{
		"E-IssueFilter.IDs@bound": "the bound is on the LENGTH of the id set, which a per-field disposition cannot express; PlanSearch raises it",
	}

	cited := map[string]bool{}
	sources := map[reflect.Type]bool{}
	for _, table := range Tables() {
		sources[table.Source] = true
		for _, entry := range table.Fields {
			if entry.Ledger != "" {
				cited[entry.Ledger] = true
			}
		}
	}

	for _, row := range Ledger() {
		if row.Type == nil || row.Kind == KindRetired || !sources[row.Type] {
			continue
		}
		if cited[row.ID] || raisedFromCode[row.ID] != "" {
			continue
		}
		t.Errorf("ledger row %s (%s.%s) is cited by no encoder-table entry.\n"+
			"Either a disposition stopped referring to it — in which case the row is a leftover — or it is raised from code and belongs in raisedFromCode with the reason.",
			row.ID, row.Type.Name(), row.Field)
	}
}

// TestEveryEncoderTableParameterIsPublished reads the document as the authority
// it is declared to be: internal/httpapi/spec/openapi.v0.yaml and routes.go are
// the SOLE surface authority, so a parameter this client sends that the
// operation does not publish is a 400 waiting to happen, and a parameter the
// operation publishes that no field drives is a filter this client cannot
// express and has not admitted to.
func TestEveryEncoderTableParameterIsPublished(t *testing.T) {
	published := loadPublishedParameters(t)

	for _, table := range Tables() {
		t.Run(tableName(table), func(t *testing.T) {
			want, ok := published[table.Op]
			if !ok {
				t.Fatalf("the document publishes no operation %q", table.Op)
			}

			sent := map[string]bool{}
			for _, entry := range table.Fields {
				if entry.Disposition != DispParam {
					continue
				}
				if !want[entry.Param] {
					t.Errorf("%s sends %q, which %s does not publish: %v",
						entry.Name, entry.Param, table.Op, sortedKeys(want))
				}
				sent[entry.Param] = true
			}
			for _, reserved := range table.Reserved {
				if !want[reserved.Name] {
					t.Errorf("reserved parameter %q is not published by %s", reserved.Name, table.Op)
				}
				if strings.TrimSpace(reserved.Why) == "" {
					t.Errorf("reserved parameter %q carries no reason", reserved.Name)
				}
				sent[reserved.Name] = true
			}

			if !table.Primary {
				// A bridge maps a legacy filter onto a subset of an operation
				// it does not own, so it owes the containment check above and
				// not the coverage one below.
				return
			}
			for name := range want {
				if !sent[name] {
					t.Errorf("%s publishes %q and no field of %s drives it.\n"+
						"Map it, or list it in the table's Reserved set with the reason no request field can.",
						table.Op, name, table.Source.Name())
				}
			}
		})
	}
}

// TestEncoderHonorsEveryTableDisposition drives the encoders one field at a
// time and asserts each behaves the way the table declares.
//
// One field at a time, against a baseline, because that is the only way to
// attribute a difference: a request with two fields set tells you nothing about
// which one produced which parameter.
func TestEncoderHonorsEveryTableDisposition(t *testing.T) {
	for _, table := range Tables() {
		base := baselineFor(t, table)
		baseline, err := Encode(table.Op, table.Shape, base.Interface())
		if err != nil {
			t.Fatalf("%s: the baseline source does not encode: %v", tableName(table), err)
		}

		for _, entry := range table.Fields {
			t.Run(tableName(table)+"/"+entry.Name, func(t *testing.T) {
				source := reflect.New(table.Source).Elem()
				source.Set(base)
				populate(t, source.FieldByName(entry.Name))

				got, err := Encode(table.Op, table.Shape, source.Interface())

				switch entry.Disposition {
				// A derived default is driven exactly as a refusal: the sweep's
				// sentinel value is by construction not one any intent derives,
				// so the inversion must reject it and cite the same row.
				case DispRefused, DispInverted:
					var refusal *RefusedError
					if !errors.As(err, &refusal) {
						t.Fatalf("a populated %s encoded without a refusal (err=%v, params=%v).\n"+
							"A dropped filter widens the result set invisibly — see D7 and ledger row %s.",
							entry.Name, err, got.Params, entry.Ledger)
					}
					if !errors.Is(err, ErrRefused) {
						t.Errorf("the refusal does not match ErrRefused")
					}
					if refusal.Row.ID != entry.Ledger {
						t.Errorf("refusal cites ledger row %q, table says %q", refusal.Row.ID, entry.Ledger)
					}
				case DispParam:
					if err != nil {
						t.Fatalf("a populated %s failed to encode: %v", entry.Name, err)
					}
					if _, ok := got.Params[entry.Param]; !ok {
						t.Errorf("a populated %s produced no %q parameter (params=%v)", entry.Name, entry.Param, got.Params)
					}
				case DispPath:
					if err != nil {
						t.Fatalf("a populated %s failed to encode: %v", entry.Name, err)
					}
					if len(got.PathIDs) == 0 || got.PathIDs[0] == "" {
						t.Errorf("a populated %s produced no path id (%v)", entry.Name, got.PathIDs)
					}
				case DispServerFixed, DispClientSide, DispDropped:
					if err != nil {
						t.Fatalf("a populated %s failed to encode: %v", entry.Name, err)
					}
					if got.Params.Encode() != baseline.Params.Encode() {
						t.Errorf("a populated %s changed the query string (%q, baseline %q); a %s field must send nothing",
							entry.Name, got.Params.Encode(), baseline.Params.Encode(), entry.Disposition)
					}
				case DispNested:
					// The opposite assertion to the three above: a delegated
					// shape must REACH the wire. A builder that forgot it would
					// answer a perfectly well-formed number about the whole
					// workspace, and nothing downstream could tell.
					if err != nil {
						t.Fatalf("a populated %s failed to encode: %v", entry.Name, err)
					}
					if got.Params.Encode() == baseline.Params.Encode() {
						t.Errorf("a populated %s sent nothing (query %q is the baseline); the delegated shape never reached the wire",
							entry.Name, got.Params.Encode())
					}
				}
			})
		}
	}
}

// TestTheExactIDsFanOutIsBounded pins the one refusal the disposition sweep
// cannot reach by populating a single field: the bound is on the LENGTH of a
// set the sweep populates with one element.
func TestTheExactIDsFanOutIsBounded(t *testing.T) {
	ids := make([]string, MaxExactIDs)
	for i := range ids {
		ids[i] = fmt.Sprintf("bd-%04d", i)
	}

	plan, err := PlanSearch(types.IssueFilter{IDs: ids}, ZeroVocabulary)
	if err != nil {
		t.Fatalf("%d ids refused: %v", MaxExactIDs, err)
	}
	if plan.Shape != SearchExactIDs || len(plan.IDs) != MaxExactIDs {
		t.Fatalf("plan = %+v, want the exact-ids shape carrying %d ids", plan, MaxExactIDs)
	}

	var refusal *RefusedError
	if _, err := PlanSearch(types.IssueFilter{IDs: append(ids, "bd-over")}, ZeroVocabulary); !errors.As(err, &refusal) {
		t.Fatalf("%d ids encoded without a refusal: %v", MaxExactIDs+1, err)
	} else if refusal.Row.ID != "E-IssueFilter.IDs@bound" {
		t.Errorf("refusal cites %q, want E-IssueFilter.IDs@bound", refusal.Row.ID)
	}
}

// TestASearchFilterMatchingNeitherShapeRefuses covers the two shape decisions
// the per-field sweep cannot express: the empty filter, which means EVERYTHING
// and is the most dangerous drop on this surface, and the id-plus-parent pair,
// which is neither shape.
func TestASearchFilterMatchingNeitherShapeRefuses(t *testing.T) {
	parent := "bd-parent"
	for _, tc := range []struct {
		name   string
		filter types.IssueFilter
		row    string
	}{
		{"the empty filter", types.IssueFilter{}, "E-IssueFilter.noShape"},
		{"ids and a parent together", types.IssueFilter{IDs: []string{"bd-1"}, ParentID: &parent}, "E-IssueFilter.shapeConflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refusal *RefusedError
			plan, err := PlanSearch(tc.filter, ZeroVocabulary)
			if !errors.As(err, &refusal) {
				t.Fatalf("encoded as %+v without a refusal (err=%v)", plan, err)
			}
			if refusal.Row.ID != tc.row {
				t.Errorf("refusal cites %q, want %q", refusal.Row.ID, tc.row)
			}
		})
	}
}

// TestAReadyRequestNamingNoPolicySendsHybrid is the one normalization the
// encoder performs, and it is here because getting it wrong is silent: an
// omitted `sort` is `priority` to the server and hybrid to the storage layer,
// and the two answer with different item SETS once a limit truncates.
func TestAReadyRequestNamingNoPolicySendsHybrid(t *testing.T) {
	for _, req := range []issueops.ReadyRequest{{}, {Sort: ""}} {
		got, err := ReadyParams(req)
		if err != nil {
			t.Fatalf("ReadyParams: %v", err)
		}
		if got.Get("sort") != "hybrid" {
			t.Errorf("sort = %q, want hybrid", got.Get("sort"))
		}
	}
	// The derived-default status is what the encoder drops; an empty status now
	// refuses, so the minimal ready filter this normalization runs on carries it.
	got, err := ReadyBridgeParams(types.WorkFilter{Status: types.StatusOpen})
	if err != nil {
		t.Fatalf("ReadyBridgeParams: %v", err)
	}
	if got.Get("sort") != "hybrid" {
		t.Errorf("bridge sort = %q, want hybrid", got.Get("sort"))
	}
}

// populatableFields lists the fields of a request shape a caller can set,
// walking embedded structs the way a caller reaches promoted fields.
//
// Unexported fields are excluded because no caller outside their package can
// set one, which is what "populatable" means here.
func populatableFields(rt reflect.Type) []string {
	var out []string
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous {
			et := f.Type
			for et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				out = append(out, populatableFields(et)...)
				continue
			}
		}
		out = append(out, f.Name)
	}
	return out
}

// populate sets a field to a value a caller could plausibly have set, so the
// encoder sees it as present.
func populate(t *testing.T, v reflect.Value) {
	t.Helper()
	if !v.CanSet() {
		t.Fatalf("cannot set a %s", v.Type())
	}
	switch {
	case v.Type() == reflect.TypeOf(time.Time{}):
		v.Set(reflect.ValueOf(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)))
		return
	case v.Kind() == reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		populate(t, v.Elem())
		return
	case v.Kind() == reflect.Struct:
		// A nested request shape (DispNested), populated WHOLE rather than one
		// member deep: the assertion the sweep makes about it is that the
		// delegated encoding reached the wire, and a shape whose one populated
		// member happened to be a client-side one would satisfy that vacuously.
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				populate(t, v.Field(i))
			}
		}
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("gate-sentinel")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		populate(t, elem)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), elem))
	case reflect.Map:
		key, value := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		populate(t, key)
		populate(t, value)
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(key, value)
	default:
		t.Fatalf("no sentinel for a %s (%s)", v.Kind(), v.Type())
	}
}

// baselineFor is the smallest source value each table can encode. The two
// SearchIssues shapes need their selector set before anything else about them
// can be observed, because the shape decides which fields are readable at all.
func baselineFor(t *testing.T, table Table) reflect.Value {
	t.Helper()
	base := reflect.New(table.Source).Elem()
	switch table.Shape {
	case "searchExactIDs":
		base.FieldByName("IDs").Set(reflect.ValueOf([]string{"bd-baseline"}))
	case "searchParentWalk":
		parent := "bd-baseline"
		base.FieldByName("ParentID").Set(reflect.ValueOf(&parent))
	case "workFilterBridge":
		// The ready bridge's Status is a DERIVED DEFAULT, not a refusal: the
		// encoder recognizes StatusOpen — the value workapi.BuildReadyFilter
		// stamps on every ready filter — as the server's own default and drops
		// it, and refuses every other status including the empty one (the storage
		// layer's open+in_progress default, which the ready wire cannot state).
		// So the smallest source this table can encode carries StatusOpen,
		// exactly as the walk's smallest carries its parent selector.
		base.FieldByName("Status").Set(reflect.ValueOf(types.StatusOpen))
	}
	return base
}

func tableName(table Table) string {
	if table.Shape == "" {
		return string(table.Op)
	}
	return string(table.Op) + "/" + table.Shape
}

func findRow(t *testing.T, id string) Row {
	t.Helper()
	for _, row := range Ledger() {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("no divergence-ledger row %q", id)
	return Row{}
}

// testFunctionsInThisPackage collects the test names a ledger row may cite as
// its pin.
//
// Parsed out of the sources rather than kept as a list, for the reason
// everything else here is derived rather than restated: a second copy is a
// second thing that can be wrong.
//
// It reads THIS package, the store package above it, and cmd/bd, because the
// ledger is one artifact and its pins are not. The read-shape rows are pinned by
// the encoder sweep in this directory; the write-shape rows D8's refuse-not-drop
// enumeration owns are pinned where the refusal is raised, which is the store's
// role bodies; and the F- rows — whole commands and the flag modes of served
// commands — can only be pinned where a cobra tree exists. Scanning only this
// directory would have forced those rows to carry a TODO forever and made a real
// pin indistinguishable from a missing one.
//
// cmd/bd's files are enterprise-tagged and this test is not. That is fine and
// deliberate: parsing is text, and the same reasoning the skew sweep records
// applies — the build-constrained files are precisely the ones that carry these
// pins.
func testFunctionsInThisPackage(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, dir := range []string{".", "..", filepath.Join("..", "..", "..", "cmd", "bd")} {
		collectTestFunctions(t, dir, names)
	}
	if len(names) == 0 {
		t.Fatal("found no test functions to check ledger pins against")
	}
	return names
}

func collectTestFunctions(t *testing.T, dir string, into map[string]bool) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				into[fn.Name.Name] = true
			}
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// loadPublishedParameters reads the query parameters of every operation out of
// the wire contract itself.
//
// The document rather than a hand-kept list, because a hand-kept list is the
// second copy this whole gate exists to avoid; and the document rather than the
// handlers, because the document is what the source-of-truth ordering names as
// the surface authority.
//
// Read through spec.OpenAPIV0() (a go:embed'd []byte) rather than a relative
// os.ReadFile path: the embed already carries this exact file as a build
// input of internal/httpapi/spec, so this gate's own package dependency is
// what makes the document reachable under `bazel test`'s sandbox -- no
// `data` attribute naming the YAML's filesystem path is needed here at all.
func loadPublishedParameters(t *testing.T) map[Op]map[string]bool {
	t.Helper()
	raw := spec.OpenAPIV0()
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
			Parameters  []struct {
				Name string `yaml:"name"`
				In   string `yaml:"in"`
			} `yaml:"parameters"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse the wire contract: %v", err)
	}

	out := map[Op]map[string]bool{}
	for _, methods := range doc.Paths {
		for method, operation := range methods {
			if operation.OperationID == "" || !slices.Contains([]string{"get", "post", "patch", "delete", "put"}, method) {
				continue
			}
			names := map[string]bool{}
			for _, p := range operation.Parameters {
				if p.In == "query" {
					names[p.Name] = true
				}
			}
			out[Op(operation.OperationID)] = names
		}
	}
	if len(out) == 0 {
		t.Fatal("the wire contract parsed to no operations")
	}
	return out
}
