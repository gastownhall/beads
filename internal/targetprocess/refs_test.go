package targetprocess

import "testing"

func TestExternalRefs(t *testing.T) {
	t.Parallel()

	baseURL := "https://example.tpondemand.com"
	ref := "https://example.tpondemand.com/api/v1/Assignables/123"

	if !IsExternalRef(ref, baseURL) {
		t.Fatalf("expected %q to be recognized", ref)
	}
	if got := ExtractIdentifier(ref); got != "123" {
		t.Fatalf("expected identifier 123, got %q", got)
	}
	if got := ExtractIdentifier("targetprocess:77"); got != "77" {
		t.Fatalf("expected identifier 77, got %q", got)
	}
}
