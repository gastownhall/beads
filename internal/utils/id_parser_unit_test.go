package utils

import "testing"

func TestPartialIDSearchPartUsesLastHyphenSuffix(t *testing.T) {
	got, ok := partialIDSearchPart("hacker-news-ko4")
	if !ok {
		t.Fatal("partialIDSearchPart returned ok=false")
	}
	if got != "ko4" {
		t.Fatalf("search part = %q, want %q", got, "ko4")
	}
}

func TestPartialIDSearchPartKeepsPlainAndHierarchicalIDs(t *testing.T) {
	tests := []string{"abc123", "abc123.1", "bd-abc123.1"}
	for _, input := range tests {
		got, ok := partialIDSearchPart(input)
		if !ok {
			t.Fatalf("partialIDSearchPart(%q) returned ok=false", input)
		}
		want := input
		if input == "bd-abc123.1" {
			want = "abc123.1"
		}
		if got != want {
			t.Fatalf("partialIDSearchPart(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPartialIDSearchPartRejectsInvalidSearchText(t *testing.T) {
	for _, input := range []string{"", "bd abc", "bd:abc", "bd/abc"} {
		if got, ok := partialIDSearchPart(input); ok {
			t.Fatalf("partialIDSearchPart(%q) = %q, true; want false", input, got)
		}
	}
}

func TestHashSegmentPrefixMatch(t *testing.T) {
	tests := []struct {
		issueHash string
		hashPart  string
		want      bool
	}{
		// Direct prefix: plain hash starts with query
		{"oqfe9", "oqf", true},
		// Exact match
		{"oqf", "oqf", true},
		// The gcy-g4o repro: wisp compound hash must NOT match
		{"wisp-goqfo", "oqf", false},
		// Wisp last-segment match (t3st matches wisp-t3st)
		{"wisp-t3st", "t3st", true},
		// Partial prefix of last segment
		{"wisp-t3st", "t3", true},
		// No match
		{"abc123", "xyz", false},
	}
	for _, tt := range tests {
		got := hashSegmentPrefixMatch(tt.issueHash, tt.hashPart)
		if got != tt.want {
			t.Errorf("hashSegmentPrefixMatch(%q, %q) = %v; want %v", tt.issueHash, tt.hashPart, got, tt.want)
		}
	}
}

// TestHashSegmentPrefixMatchPlainHashPrefixOnly pins the intentional
// behavior change from strings.Contains to prefix-only matching: a plain
// (non-wisp, no-dash) hash matches a prefix fragment but no longer matches
// a tail or middle fragment. Previously "f8e9" (tail of "a3f8e9") would
// match via substring; that broadening was the source of the gcy-g4o
// wrong-bead resolution and is deliberately removed.
func TestHashSegmentPrefixMatchPlainHashPrefixOnly(t *testing.T) {
	tests := []struct {
		issueHash string
		hashPart  string
		want      bool
	}{
		// Prefix fragments of a plain hash still match
		{"a3f8e9", "a3", true},
		{"a3f8e9", "a3f8", true},
		// Tail fragment no longer matches (old Contains behavior)
		{"a3f8e9", "f8e9", false},
		// Middle fragment no longer matches
		{"a3f8e9", "3f8", false},
	}
	for _, tt := range tests {
		got := hashSegmentPrefixMatch(tt.issueHash, tt.hashPart)
		if got != tt.want {
			t.Errorf("hashSegmentPrefixMatch(%q, %q) = %v; want %v", tt.issueHash, tt.hashPart, got, tt.want)
		}
	}
}
