package main

import (
	"strings"
	"testing"
)

// TestRenderInitConfigYAMLOmitsNoDB pins GH#4135 AC-5: the init config.yaml
// template no longer mentions the retired no-db mode. no-db never selected a
// storage backend — issues live in the Dolt database and the JSONL export is
// an export — so advertising it in every new workspace's config.yaml invites
// users to set a setting that does nothing (GH#4135 retires it; bd config set
// no-db now refuses, and a no-db: true in an existing config.yaml is ignored
// with a warning).
func TestRenderInitConfigYAMLOmitsNoDB(t *testing.T) {
	rendered := string(renderInitConfigYAML())
	for _, mention := range []string{"no-db", "JSONL-only"} {
		if strings.Contains(rendered, mention) {
			t.Errorf("init config.yaml template still mentions %q:\n%s", mention, rendered)
		}
	}
}
