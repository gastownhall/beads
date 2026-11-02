package workapi

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestValidateSettingWriteProtectedLabels pins that wisp.protected_labels, the
// list a destructive command consults before deleting, is refused at write
// time when it would not read back as the list the operator wrote. wisp gc
// refuses to run on such a value, so storing it would disable GC until fixed.
func TestValidateSettingWriteProtectedLabels(t *testing.T) {
	const key = "wisp.protected_labels"
	for _, value := range []string{
		"",
		"   ",
		"[]",
		"keep:mail",
		" keep:mail , keep:alert ",
		`["keep:mail","keep:alert"]`,
		`["say:\"hi\"","[x]"]`,
	} {
		if _, err := ValidateSettingWrite(key, value); err != nil {
			t.Errorf("ValidateSettingWrite(%s, %q) = %v, want it accepted", key, value, err)
		}
	}
	for _, test := range []struct{ value, wantInError string }{
		{"[keep:mail]", "JSON array"},
		{`["keep:mail"`, "JSON array"},
		{`[1]`, "JSON array"},
		{`["a",2]`, "JSON array"},
		{"keep mail", "keep mail"},
		{`["keep mail"]`, "keep mail"},
		{",", "names no label"},
		{" , ", "names no label"},
		{`[""]`, "names no label"},
	} {
		_, err := ValidateSettingWrite(key, test.value)
		if !errors.Is(err, issueops.ErrValidation) {
			t.Errorf("ValidateSettingWrite(%s, %q) error = %v, want ErrValidation", key, test.value, err)
			continue
		}
		if !strings.Contains(err.Error(), test.wantInError) {
			t.Errorf("ValidateSettingWrite(%s, %q) error = %q, want it to name %q", key, test.value, err, test.wantInError)
		}
	}
}

// TestParseProtectedLabels pins the one read of wisp.protected_labels: both
// list forms, trimmed, deduplicated in first-seen order, case and punctuation
// preserved; unset, blank and [] protect nothing.
func TestParseProtectedLabels(t *testing.T) {
	for _, test := range []struct {
		value string
		want  []string
	}{
		{"", nil},
		{"  ", nil},
		{"[]", nil},
		{"keep:mail", []string{"keep:mail"}},
		{"b, a ,b,,A", []string{"b", "a", "A"}},
		{`[" keep:mail ","keep:mail","x\"y"]`, []string{"keep:mail", `x"y`}},
	} {
		got, err := ParseProtectedLabels(test.value)
		if err != nil {
			t.Errorf("ParseProtectedLabels(%q) error = %v", test.value, err)
			continue
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("ParseProtectedLabels(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}
