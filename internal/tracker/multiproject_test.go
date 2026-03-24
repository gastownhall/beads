package tracker

import "testing"

func TestParseCommaSeparated(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"single", "abc", []string{"abc"}},
		{"two", "abc,def", []string{"abc", "def"}},
		{"whitespace", " abc , def , ghi ", []string{"abc", "def", "ghi"}},
		{"empty elements", "abc,,def,", []string{"abc", "def"}},
		{"empty string", "", []string{}},
		{"only commas", ",,,", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCommaSeparated(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("ParseCommaSeparated(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDeduplicateStrings(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{"no dupes", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"with dupes", []string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{"all same", []string{"x", "x", "x"}, []string{"x"}},
		{"empty", []string{}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeduplicateStrings(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("DeduplicateStrings(%v) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestResolveProjectIDs(t *testing.T) {
	tests := []struct {
		name      string
		cli       []string
		plural    string
		singular  string
		wantLen   int
		wantFirst string
	}{
		{"cli override", []string{"X", "Y"}, "A,B", "C", 2, "X"},
		{"plural config", nil, "A, B, C", "D", 3, "A"},
		{"singular fallback", nil, "", "D", 1, "D"},
		{"nothing", nil, "", "", 0, ""},
		{"cli dedup", []string{"A", "B", "A"}, "", "", 2, "A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveProjectIDs(tt.cli, tt.plural, tt.singular)
			if len(got) != tt.wantLen {
				t.Fatalf("got %v (len %d), want len %d", got, len(got), tt.wantLen)
			}
			if tt.wantLen > 0 && got[0] != tt.wantFirst {
				t.Errorf("first = %q, want %q", got[0], tt.wantFirst)
			}
		})
	}
}
