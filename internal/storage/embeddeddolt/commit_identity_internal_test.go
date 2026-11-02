//go:build cgo

package embeddeddolt

import "testing"

// TestCleanCommitIdentityField pins the normalization the store applies to
// identity fields before handing them to Dolt's author parser (GH#7628).
// The parser matches an unanchored "([^)]+) \<([^)]+)", so every structural
// character has to go and whitespace has to flatten before the field can
// identify anyone reliably.
func TestCleanCommitIdentityField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		field string
		want  string
	}{
		{"plain name unchanged", "Alice Author", "Alice Author"},
		{"email unchanged", "alice@example.com", "alice@example.com"},
		{"angle brackets become spaces", "Alice <x>", "Alice x"},
		{"parentheses become spaces", "Alice (bot)", "Alice bot"},
		{"newlines flatten", "Alice\nAuthor", "Alice Author"},
		{"carriage return and tab flatten", "Alice\rA\tb", "Alice A b"},
		{"runs collapse to one space", "  Alice   (bot)  <x>  ", "Alice bot x"},
		{"only structure empties out", "<>()\n\t", ""},
		{"empty stays empty", "", ""},
	} {
		if got := cleanCommitIdentityField(tc.field); got != tc.want {
			t.Errorf("%s: cleanCommitIdentityField(%q) = %q, want %q", tc.name, tc.field, got, tc.want)
		}
	}
}