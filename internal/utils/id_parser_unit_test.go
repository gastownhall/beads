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
