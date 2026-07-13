package labelns

import (
	"reflect"
	"testing"
)

func TestParsePrefixes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"single with colon", "tier:", []string{"tier:"}},
		{"single without colon", "tier", []string{"tier:"}},
		{"mixed forms dedup", "tier,tier:", []string{"tier:"}},
		{"multiple with whitespace", " tier: , review ", []string{"tier:", "review:"}},
		{"drops empties and bare colon", ",,:,tier:", []string{"tier:"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePrefixes(tt.raw)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParsePrefixes(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestValidatePrefixes(t *testing.T) {
	valid := []string{"tier:", "tier", "tier:,review:", " tier , review: "}
	for _, raw := range valid {
		if _, err := ValidatePrefixes(raw); err != nil {
			t.Errorf("ValidatePrefixes(%q) unexpected error: %v", raw, err)
		}
	}
	invalid := []string{
		"",                // no prefixes at all
		",,",              // parses to nothing
		":",               // no name before colon
		"a:b:",            // interior colon
		"has space:",      // whitespace
		"provides:",       // reserved multi-valued namespace
		"provides",        // reserved, colon-less form
		"tier:,provides:", // reserved among valid entries
	}
	for _, raw := range invalid {
		if _, err := ValidatePrefixes(raw); err == nil {
			t.Errorf("ValidatePrefixes(%q) expected error, got nil", raw)
		}
	}
}

func TestMatch(t *testing.T) {
	prefixes := []string{"tier:", "review:"}
	if got := Match(prefixes, "tier:fable"); got != "tier:" {
		t.Errorf("Match(tier:fable) = %q, want tier:", got)
	}
	if got := Match(prefixes, "area:x"); got != "" {
		t.Errorf("Match(area:x) = %q, want empty", got)
	}
	if got := Match(nil, "tier:fable"); got != "" {
		t.Errorf("Match with no prefixes = %q, want empty", got)
	}
}

func TestConflicts(t *testing.T) {
	prefixes := []string{"tier:", "review:"}

	if got := Conflicts(prefixes, []string{"tier:fable", "review:opus", "area:x"}); len(got) != 0 {
		t.Errorf("expected no conflicts, got %v", got)
	}
	if got := Conflicts(prefixes, []string{"tier:fable", "tier:fable"}); len(got) != 0 {
		t.Errorf("duplicate of the same label should not conflict, got %v", got)
	}
	if got := Conflicts(nil, []string{"tier:fable", "tier:opus"}); len(got) != 0 {
		t.Errorf("no prefixes configured should never conflict, got %v", got)
	}

	got := Conflicts(prefixes, []string{"review:a", "tier:fable", "tier:opus", "review:b"})
	want := []Conflict{
		{Prefix: "tier:", Labels: []string{"tier:fable", "tier:opus"}},
		{Prefix: "review:", Labels: []string{"review:a", "review:b"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Conflicts = %v, want %v", got, want)
	}
}
