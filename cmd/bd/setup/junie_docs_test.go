package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Both Junie pages show the MCP entry bd setup junie writes and the launcher
// it needs, and neither still prescribes the nonexistent "bd mcp" or checks
// the default uvx setup with pip (GH#5563).
func TestJunieDocsShowTheUvxBeadsMCPEntry(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, page := range []string{
		"docs/integrations/junie.md",
		"integrations/junie/README.md",
	} {
		t.Run(page, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(page)))
			if err != nil {
				t.Fatalf("read %s: %v", page, err)
			}
			doc := string(data)
			for _, want := range []string{
				`"command": "uvx"`,
				`"args": ["beads-mcp"]`,
				"https://docs.astral.sh/uv/",
			} {
				if !strings.Contains(doc, want) {
					t.Errorf("%s does not show %q", page, want)
				}
			}
			for _, stale := range []string{
				`"args": ["mcp"]`,
				"pip show beads-mcp",
			} {
				if strings.Contains(doc, stale) {
					t.Errorf("%s still shows %q", page, stale)
				}
			}
		})
	}
}
