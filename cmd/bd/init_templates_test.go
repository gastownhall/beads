package main

import (
	"strings"
	"testing"
)

// TestRenderInitConfigYAMLOmitsNoDB: the config.yaml bd init writes does not
// advertise no-db or a JSONL-only backend. Issues live in Dolt; JSONL is an
// export (GH#7387).
func TestRenderInitConfigYAMLOmitsNoDB(t *testing.T) {
	body := string(proxiedInitConfigYAMLBody(t.TempDir()))
	if body == "" {
		t.Fatal("no init config.yaml template rendered")
	}
	for _, unwanted := range []string{"no-db", "JSONL-only"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("init config.yaml template mentions %q:\n%s", unwanted, body)
		}
	}
}
