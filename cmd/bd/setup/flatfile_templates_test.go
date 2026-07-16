package setup

import (
	"strings"
	"testing"
)

// BEADS-985z: flat-file workspaces must not receive Dolt sync directives in
// the static setup templates. The case-insensitive scan is an anchor-rot
// guard: a reworded template that stops matching a rewrite pair fails here
// instead of shipping `bd dolt push` guidance to flat-file users.
func TestFlatfileizeSetupTextRemovesDoltDirectives(t *testing.T) {
	templates := map[string]string{
		"aider-config":       aiderConfigTemplate,
		"aider-instructions": aiderBeadsInstructions,
		"aider-readme":       aiderReadmeTemplate,
		"junie-guidelines":   junieGuidelinesTemplate,
	}
	for name, tmpl := range templates {
		got := flatfileizeSetupText(tmpl)
		if idx := strings.Index(strings.ToLower(got), "dolt"); idx != -1 {
			start := idx - 60
			if start < 0 {
				start = 0
			}
			end := idx + 60
			if end > len(got) {
				end = len(got)
			}
			t.Errorf("%s: flatfileized template still mentions Dolt: ...%s...", name, got[start:end])
		}
	}
}
