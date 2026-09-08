package storage

import "testing"

func TestJSONMetadataPathSimpleKey(t *testing.T) {
	got := JSONMetadataPath("status")
	want := "$.status"
	if got != want {
		t.Errorf("JSONMetadataPath(%q) = %q, want %q", "status", got, want)
	}
}

func TestJSONMetadataPathDottedKey(t *testing.T) {
	got := JSONMetadataPath("gc.routed_to")
	want := `$."gc.routed_to"`
	if got != want {
		t.Errorf("JSONMetadataPath(%q) = %q, want %q", "gc.routed_to", got, want)
	}
}

func TestJSONMetadataPathMultipleDots(t *testing.T) {
	got := JSONMetadataPath("gc.scope.ref")
	want := `$."gc.scope.ref"`
	if got != want {
		t.Errorf("JSONMetadataPath(%q) = %q, want %q", "gc.scope.ref", got, want)
	}
}

func TestJSONMetadataPathUnderscoredKey(t *testing.T) {
	got := JSONMetadataPath("my_field")
	want := "$.my_field"
	if got != want {
		t.Errorf("JSONMetadataPath(%q) = %q, want %q", "my_field", got, want)
	}
}
