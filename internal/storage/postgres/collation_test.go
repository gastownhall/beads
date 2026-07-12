package postgres

import "testing"

func TestCollationParityOK(t *testing.T) {
	cases := []struct {
		collation string
		want      bool
	}{
		{"C", true},
		{"POSIX", true},
		{"c", true},       // case-insensitive
		{"posix", true},   // case-insensitive
		{" C ", true},     // trimmed
		{"C.UTF-8", true}, // C.* family sorts by code point
		{"C.utf8", true},
		{"en_US.UTF-8", false},
		{"en_US.utf8", false},
		{"en_GB.UTF-8", false},
		{"und-x-icu", false}, // ICU root
		{"de_DE.UTF-8", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := collationParityOK(tc.collation); got != tc.want {
			t.Errorf("collationParityOK(%q) = %v, want %v", tc.collation, got, tc.want)
		}
	}
}
