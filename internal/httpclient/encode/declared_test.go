package encode

import (
	"errors"
	"reflect"
	"testing"
)

// TestADeclaredEmptyFieldRefuses pins the reading a Declared entry gets. The
// default (populated) treats an empty slice as absent, and that is right for a
// label list; it was wrong for the execution context, whose empty value is a
// restriction: a declared-empty `--ctx ”` crossed the wire as "no context" and
// the server answered with exactly the rows the caller declared it cannot run.
// Every Declared entry must refuse on the empty non-nil value, and the nil
// value must still encode (it is "not declared").
func TestADeclaredEmptyFieldRefuses(t *testing.T) {
	seen := 0
	for _, table := range Tables() {
		for _, entry := range table.Fields {
			if !entry.Declared {
				continue
			}
			seen++
			t.Run(tableName(table)+"/"+entry.Name, func(t *testing.T) {
				if entry.Disposition != DispRefused {
					t.Fatalf("Declared is a reading of DispRefused; this entry is %v", entry.Disposition)
				}
				base := baselineFor(t, table)
				source := reflect.New(table.Source).Elem()
				source.Set(base)
				field := source.FieldByName(entry.Name)
				if field.Kind() != reflect.Slice && field.Kind() != reflect.Map {
					t.Fatalf("Declared is for a nil-aware slice or map; %s is a %s", entry.Name, field.Kind())
				}
				if _, err := Encode(table.Op, table.Shape, source.Interface()); err != nil {
					t.Fatalf("the nil (undeclared) value must encode, got %v", err)
				}
				field.Set(reflect.MakeSlice(field.Type(), 0, 0))
				_, err := Encode(table.Op, table.Shape, source.Interface())
				if !errors.Is(err, ErrRefused) {
					t.Fatalf("the declared-EMPTY value encoded (%v), want ErrRefused citing %s", err, entry.Ledger)
				}
				var refused *RefusedError
				if !errors.As(err, &refused) || refused.Row.ID != entry.Ledger {
					t.Errorf("refusal = %v, want row %s", err, entry.Ledger)
				}
			})
		}
	}
	if seen < 4 {
		t.Fatalf("%d Declared entries in the tables, want the four Ctx readings (ReadyRequest, ListRequest, WorkFilter, IssueFilter)", seen)
	}
}
