package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

func TestJsonMetadata_NilReturnsEmptyObject(t *testing.T) {
	got := issueops.JSONMetadata(nil)
	if got != "{}" {
		t.Errorf("JSONMetadata(nil) = %q, want %q", got, "{}")
	}
}

func TestJsonMetadata_EmptyReturnsEmptyObject(t *testing.T) {
	got := issueops.JSONMetadata([]byte{})
	if got != "{}" {
		t.Errorf("JSONMetadata(empty) = %q, want %q", got, "{}")
	}
}

func TestJsonMetadata_ValidJSONPassesThrough(t *testing.T) {
	input := []byte(`{"key":"value"}`)
	got := issueops.JSONMetadata(input)
	if got != `{"key":"value"}` {
		t.Errorf("JSONMetadata(%q) = %q, want %q", input, got, `{"key":"value"}`)
	}
}

func TestJsonMetadata_InvalidJSONFallsBackToEmptyObject(t *testing.T) {
	input := []byte(`{not valid json`)
	got := issueops.JSONMetadata(input)
	if got != "{}" {
		t.Errorf("JSONMetadata(%q) = %q, want %q (should reject invalid JSON)", input, got, "{}")
	}
}
