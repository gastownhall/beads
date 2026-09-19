package dolt

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// coordinationFields are the only two patch fields an assignee/status re-read
// can prove. Every other settable field must count as a non-coordination patch,
// so a verify-by-re-read cannot launder its indeterminate commit into success.
var coordinationFields = map[string]bool{"Assignee": true, "Status": true}

// isFieldPatch reports whether a struct field is an issueops.Field[T], detected
// by a bool member named "Set". MetadataPatch.Set is a map and LabelPatch has no
// Set member, so both nested patches are correctly excluded here and pinned
// instead by the count guard and the explicit nested cases below.
func isFieldPatch(fv reflect.Value) bool {
	if fv.Kind() != reflect.Struct {
		return false
	}
	set := fv.FieldByName("Set")
	return set.IsValid() && set.Kind() == reflect.Bool
}

// TestHasNonCoordinationPatchClassifiesEveryScalarField reflects over every
// scalar issueops.Field[T] on IssuePatch, sets it in isolation, and asserts the
// classifier agrees with the coordination set. A new scalar field added to
// IssuePatch (and wired into ExecuteUpdate) but not into
// nonCoordinationPatchSignals reclassifies a mixed update as coordination-only;
// this test fails the moment that field appears.
func TestHasNonCoordinationPatchClassifiesEveryScalarField(t *testing.T) {
	typ := reflect.TypeOf(issueops.IssuePatch{})
	scalarSeen := 0
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		probe := reflect.New(typ).Elem()
		fv := probe.Field(i)
		if !isFieldPatch(fv) {
			continue
		}
		scalarSeen++
		fv.FieldByName("Set").SetBool(true)
		patch, ok := probe.Interface().(issueops.IssuePatch)
		if !ok {
			t.Fatalf("probe is not an issueops.IssuePatch")
		}
		got := hasNonCoordinationPatch(patch)
		want := !coordinationFields[field.Name]
		if got != want {
			t.Errorf("hasNonCoordinationPatch with only %s set = %v, want %v", field.Name, got, want)
		}
	}
	if scalarSeen == 0 {
		t.Fatal("no scalar Field[T] members discovered on IssuePatch; reflection probe is broken")
	}
}

// TestNonCoordinationPatchSignalsCountMatchesStruct guards against LabelPatch or
// MetadataPatch gaining a sub-field that is wired into updates but not into
// nonCoordinationPatchSignals — drift the scalar reflection above cannot see
// because those sub-fields are not Field[T]. The expected length is derived from
// the struct definitions, so it tracks the code rather than a hand-copied count.
func TestNonCoordinationPatchSignalsCountMatchesStruct(t *testing.T) {
	patchType := reflect.TypeOf(issueops.IssuePatch{})
	probe := reflect.New(patchType).Elem()
	scalar := 0
	for i := 0; i < patchType.NumField(); i++ {
		if isFieldPatch(probe.Field(i)) {
			scalar++
		}
	}
	labelFields := reflect.TypeOf(issueops.LabelPatch{}).NumField()
	metadataFields := reflect.TypeOf(issueops.MetadataPatch{}).NumField()
	want := (scalar - len(coordinationFields)) + labelFields + metadataFields

	got := len(nonCoordinationPatchSignals(issueops.IssuePatch{}))
	if got != want {
		t.Fatalf("nonCoordinationPatchSignals length = %d, want %d "+
			"(%d scalar Field[T] − %d coordination + %d LabelPatch + %d MetadataPatch); "+
			"add the new patch field to nonCoordinationPatchSignals",
			got, want, scalar, len(coordinationFields), labelFields, metadataFields)
	}
}

// TestHasNonCoordinationPatchNestedAndCoordinationCases pins the LabelPatch and
// MetadataPatch sub-fields (which the scalar reflection cannot set generically)
// plus the coordination-only and empty baselines the guard relies on.
func TestHasNonCoordinationPatchNestedAndCoordinationCases(t *testing.T) {
	raw := json.RawMessage(`{"k":"v"}`)
	cases := []struct {
		name  string
		patch issueops.IssuePatch
		want  bool
	}{
		{"empty", issueops.IssuePatch{}, false},
		{"assignee only", issueops.IssuePatch{Assignee: issueops.Field[string]{Set: true}}, false},
		{"status only", issueops.IssuePatch{Status: issueops.Field[issueops.Status]{Set: true}}, false},
		{"assignee and status", issueops.IssuePatch{
			Assignee: issueops.Field[string]{Set: true},
			Status:   issueops.Field[issueops.Status]{Set: true},
		}, false},
		{"labels add", issueops.IssuePatch{Labels: issueops.LabelPatch{Add: []string{"x"}}}, true},
		{"labels remove", issueops.IssuePatch{Labels: issueops.LabelPatch{Remove: []string{"x"}}}, true},
		{"labels replace", issueops.IssuePatch{Labels: issueops.LabelPatch{Replace: issueops.Field[[]string]{Set: true}}}, true},
		{"metadata set", issueops.IssuePatch{Metadata: issueops.MetadataPatch{Set: map[string]json.RawMessage{"k": raw}}}, true},
		{"metadata unset", issueops.IssuePatch{Metadata: issueops.MetadataPatch{Unset: []string{"k"}}}, true},
		{"metadata replace", issueops.IssuePatch{Metadata: issueops.MetadataPatch{Replace: issueops.Field[json.RawMessage]{Set: true}}}, true},
		{"metadata merge", issueops.IssuePatch{Metadata: issueops.MetadataPatch{Merge: issueops.Field[json.RawMessage]{Set: true}}}, true},
		{"coordination plus title", issueops.IssuePatch{
			Assignee: issueops.Field[string]{Set: true},
			Status:   issueops.Field[issueops.Status]{Set: true},
			Title:    issueops.Field[string]{Set: true},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasNonCoordinationPatch(tc.patch); got != tc.want {
				t.Errorf("hasNonCoordinationPatch(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
