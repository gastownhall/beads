package workapi

import (
	"reflect"
	"testing"
)

// TestParseTypeList pins the one parse of a list-valued type setting
// (types.custom, types.retained): the JSON array `bd config set` writes, and
// the legacy comma-separated form, elements trimmed and empties dropped. A
// value that is not a JSON array of strings is read as the comma form; the
// write-time validation of types.retained (ValidateSettingWrite) is what keeps
// such a value from being stored for that key.
func TestParseTypeList(t *testing.T) {
	for _, test := range []struct {
		value string
		want  []string
	}{
		{"", nil},
		{"   ", nil},
		{"op", []string{"op"}},
		{"op,fact", []string{"op", "fact"}},
		{" op , ,fact,op ", []string{"op", "fact", "op"}},
		{`["op","fact"]`, []string{"op", "fact"}},
		{`[" op ",""]`, []string{"op"}},
		{`[]`, nil},
		{`["op"`, []string{`["op"`}},
		{"a b,c", []string{"a b", "c"}},
		{",", []string{}},
	} {
		if got := ParseTypeList(test.value); !reflect.DeepEqual(got, test.want) {
			t.Errorf("ParseTypeList(%q) = %#v, want %#v", test.value, got, test.want)
		}
	}
}

// TestParseCommaSeparatedList pins the comma form on its own: nil for an empty
// value, otherwise the trimmed non-empty entries (an empty, non-nil slice when
// there are none).
func TestParseCommaSeparatedList(t *testing.T) {
	for _, test := range []struct {
		value string
		want  []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{" a , b ,", []string{"a", "b"}},
		{" , ", []string{}},
	} {
		if got := ParseCommaSeparatedList(test.value); !reflect.DeepEqual(got, test.want) {
			t.Errorf("ParseCommaSeparatedList(%q) = %#v, want %#v", test.value, got, test.want)
		}
	}
}
