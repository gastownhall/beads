package dolt

import "testing"

func TestJsonMetadata_NilReturnsEmptyObject(t *testing.T) {
	got := jsonMetadata(nil)
	if got != "{}" {
		t.Errorf("jsonMetadata(nil) = %q, want %q", got, "{}")
	}
}

func TestJsonMetadata_EmptyReturnsEmptyObject(t *testing.T) {
	got := jsonMetadata([]byte{})
	if got != "{}" {
		t.Errorf("jsonMetadata(empty) = %q, want %q", got, "{}")
	}
}

func TestJsonMetadata_ValidJSONPassesThrough(t *testing.T) {
	input := []byte(`{"key":"value"}`)
	got := jsonMetadata(input)
	if got != `{"key":"value"}` {
		t.Errorf("jsonMetadata(%q) = %q, want %q", input, got, `{"key":"value"}`)
	}
}

func TestJsonMetadata_InvalidJSONFallsBackToEmptyObject(t *testing.T) {
	input := []byte(`{not valid json`)
	got := jsonMetadata(input)
	if got != "{}" {
		t.Errorf("jsonMetadata(%q) = %q, want %q (should reject invalid JSON)", input, got, "{}")
	}
}
