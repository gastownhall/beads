package docsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// junieGeneratedMCPConfig is what `bd setup junie` writes to
// .junie/mcp/mcp.json (cmd/bd/setup/junie.go junieMCPConfig). bd has no
// `mcp` subcommand, so the server is the separate beads-mcp package launched
// through uvx (GH#5563).
const junieGeneratedMCPConfig = `{"mcpServers":{"beads":{"command":"uvx","args":["beads-mcp"]}}}`

// TestJunieDocsDescribeTheGeneratedMCPConfig keeps both Junie guides in step
// with the MCP config bd setup junie generates: the example matches it, the
// uv prerequisite and the PATH Junie needs are stated, the manually installed
// beads-mcp alternative is explained, and neither guide offers the
// nonexistent `bd mcp` as a launch command.
func TestJunieDocsDescribeTheGeneratedMCPConfig(t *testing.T) {
	root := repoRoot()
	var want interface{}
	if err := json.Unmarshal([]byte(junieGeneratedMCPConfig), &want); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{
		"docs/integrations/junie.md",
		"integrations/junie/README.md",
	} {
		t.Run(rel, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("reading %s: %v", rel, err)
			}
			doc := string(data)

			section, ok := docSection(doc, "### MCP Config")
			if !ok {
				t.Fatalf("%s missing the MCP Config section", rel)
			}
			block, ok := firstJSONFence(section)
			if !ok {
				t.Fatalf("%s MCP Config section has no ```json example", rel)
			}
			var got interface{}
			if err := json.Unmarshal([]byte(block), &got); err != nil {
				t.Fatalf("%s MCP Config example is not JSON: %v\n%s", rel, err, block)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s MCP Config example = %s, want the generated config %s", rel, block, junieGeneratedMCPConfig)
			}

			prose := flattenProse(section)
			for _, c := range []struct{ name, substr string }{
				{"uv install link", "docs.astral.sh/uv"},
				{"uvx on Junie's PATH", "uvx must be on the path junie"},
				{"manually installed beads-mcp alternative", "set command to beads-mcp"},
			} {
				if !strings.Contains(prose, c.substr) {
					t.Errorf("%s MCP Config section missing the %s: expected to find %q", rel, c.name, c.substr)
				}
			}

			flat := flattenProse(doc)
			for _, bad := range []string{"bd mcp", `"args": ["mcp"]`} {
				if strings.Contains(flat, bad) {
					t.Errorf("%s still presents the nonexistent %q launch command", rel, bad)
				}
			}
		})
	}
}

// firstJSONFence returns the body of the first ```json fenced block in s.
func firstJSONFence(s string) (string, bool) {
	var (
		out    []string
		inside bool
	)
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inside {
			inside = trimmed == "```json"
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			return strings.Join(out, "\n"), true
		}
		out = append(out, line)
	}
	return "", false
}
