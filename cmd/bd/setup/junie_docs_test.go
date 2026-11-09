package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Both Junie pages show the MCP entry bd setup junie writes and the launcher
// it needs, and neither still prescribes the nonexistent "bd mcp" or checks
// the default uvx setup with pip (GH#5563). The entry each page shows is
// compared with what junieMCPConfig generates, so a generator change fails
// here until both pages show it.
func TestJunieDocsShowTheUvxBeadsMCPEntry(t *testing.T) {
	generated, err := json.Marshal(junieMCPConfig())
	if err != nil {
		t.Fatal(err)
	}
	var want interface{}
	if err := json.Unmarshal(generated, &want); err != nil {
		t.Fatal(err)
	}
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
			entry := junieDocsMCPEntry(t, page, doc)
			var got interface{}
			if err := json.Unmarshal([]byte(entry), &got); err != nil {
				t.Fatalf("%s: the MCP Config entry is not JSON: %v\n%s", page, err, entry)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s shows the MCP entry %s, want the one bd setup junie writes: %s",
					page, strings.TrimSpace(entry), generated)
			}
			if !strings.Contains(doc, "https://docs.astral.sh/uv/") {
				t.Errorf("%s does not show %q", page, "https://docs.astral.sh/uv/")
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

// junieDocsMCPEntry returns the first JSON block under the page's MCP Config
// heading: the entry setup writes. The installed-binary alternative the pages
// also show comes after it.
func junieDocsMCPEntry(t *testing.T, page, doc string) string {
	t.Helper()
	_, section, ok := strings.Cut(doc, "### MCP Config")
	if !ok {
		t.Fatalf("%s has no MCP Config heading", page)
	}
	_, entry, ok := strings.Cut(section, "```json")
	if !ok {
		t.Fatalf("%s has no JSON block under its MCP Config heading", page)
	}
	entry, _, ok = strings.Cut(entry, "```")
	if !ok {
		t.Fatalf("%s has an unclosed JSON block under its MCP Config heading", page)
	}
	return entry
}
