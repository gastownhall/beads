package utils

import "testing"

func TestNaturalCompareIDs(t *testing.T) {
	tests := []struct {
		a, b string
		want int // <0, 0, >0
	}{
		{"bd-1", "bd-2", -1},
		{"bd-2", "bd-10", -1},     // numeric: 2 < 10
		{"bd-E.4", "bd-E.10", -1}, // suffix: 4 < 10
		{"bd-E.10", "bd-E.4", 1},
		{"bd-E.4", "bd-E.4", 0},
		{"bd-A.1", "bd-B.1", -1}, // alpha prefix
		{"bd-1.1", "bd-1.2", -1},
		{"bd-1.9", "bd-1.10", -1}, // 9 < 10
	}
	for _, tt := range tests {
		got := NaturalCompareIDs(tt.a, tt.b)
		if (tt.want < 0 && got >= 0) || (tt.want > 0 && got <= 0) || (tt.want == 0 && got != 0) {
			t.Errorf("NaturalCompareIDs(%q, %q) = %d, want sign %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSplitIDSegments(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"bd-1", []string{"bd", "1"}},
		{"bd-E.10", []string{"bd", "E", "10"}},
		{"bd-1.2.3", []string{"bd", "1", "2", "3"}},
	}
	for _, tt := range tests {
		got := splitIDSegments(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("splitIDSegments(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitIDSegments(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}
