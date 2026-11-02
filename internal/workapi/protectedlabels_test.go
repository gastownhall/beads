package workapi

import (
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestParseProtectedLabelsValue covers the one parser of the
// wisp.protected_labels value: the JSON array `bd config set` writes, the
// comma-separated form, and the refusals. The same function gates what may be
// stored (ValidateSettingWrite) and what a GC run reads, so a value it
// accepts must read back exactly as written.
func TestParseProtectedLabelsValue(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "empty unsets", value: "", want: nil},
		{name: "blank unsets", value: "   ", want: nil},
		{name: "empty array protects nothing", value: "[]", want: nil},
		{name: "json array", value: `["keep:mail","escalation"]`, want: []string{"keep:mail", "escalation"}},
		{name: "csv", value: "keep:mail,escalation", want: []string{"keep:mail", "escalation"}},
		{name: "entries are trimmed", value: `["keep:mail", "escalation"]`, want: []string{"keep:mail", "escalation"}},
		{name: "duplicates collapse", value: `["a","a","b"]`, want: []string{"a", "b"}},
		{name: "case is significant and preserved", value: `["Keep:Mail","keep:mail"]`, want: []string{"Keep:Mail", "keep:mail"}},
		{name: "punctuation survives in JSON", value: `["a,b","c[d","e\"f"]`, want: []string{"a,b", "c[d", "e\"f"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseProtectedLabelsValue(tc.value)
			if err != nil {
				t.Fatalf("ParseProtectedLabelsValue(%q): %v", tc.value, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseProtectedLabelsValue(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}

	refused := []struct {
		name  string
		value string
	}{
		{name: "malformed json array", value: `["keep:mail"`},
		{name: "non-string elements", value: `[1,2]`},
		{name: "json names no label", value: `[""]`},
		{name: "csv names no label", value: ","},
		{name: "whitespace inside a json name", value: `["keep mail"]`},
		{name: "whitespace inside a csv name", value: "keep mail,a"},
		{name: "a bare bracketed word is not a list", value: "[keep:mail]"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParseProtectedLabelsValue(tc.value); err == nil {
				t.Errorf("ParseProtectedLabelsValue(%q) = %q, want an error", tc.value, got)
			}
		})
	}
}

// TestValidateSettingWriteWispProtectedLabels pins the write-time gate: a
// value the sweep's parser accepts stores verbatim (REPLACES, not extends —
// storing a new list is the whole setting), and one it refuses is a typed
// validation error, on every route that writes the settings plane, so a
// stored value always parses when a GC run reads it.
func TestValidateSettingWriteWispProtectedLabels(t *testing.T) {
	accepted := []string{"", "[]", `["keep:mail"]`, "keep:mail,escalation"}
	for _, value := range accepted {
		stored, err := ValidateSettingWrite(SettingKeyWispProtectedLabels, value)
		if err != nil {
			t.Errorf("ValidateSettingWrite(%s, %q): %v", SettingKeyWispProtectedLabels, value, err)
			continue
		}
		if stored != value {
			t.Errorf("ValidateSettingWrite(%s, %q) stored %q, want the value verbatim", SettingKeyWispProtectedLabels, value, stored)
		}
	}

	refused := []string{`["keep:mail"`, `[1,2]`, "keep mail"}
	for _, value := range refused {
		_, err := ValidateSettingWrite(SettingKeyWispProtectedLabels, value)
		if err == nil {
			t.Errorf("ValidateSettingWrite(%s, %q) accepted; want refused", SettingKeyWispProtectedLabels, value)
			continue
		}
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("ValidateSettingWrite(%s, %q) = %v, want an issueops.ErrValidation", SettingKeyWispProtectedLabels, value, err)
		}
	}
}
