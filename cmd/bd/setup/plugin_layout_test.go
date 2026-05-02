package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPluginLayoutUsesSharedBeadsRoot(t *testing.T) {
	root := filepath.Join("..", "..", "..")

	var claudeMarketplace struct {
		Plugins []struct {
			Source string `json:"source"`
		} `json:"plugins"`
	}
	readJSONFile(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), &claudeMarketplace)
	if len(claudeMarketplace.Plugins) != 1 {
		t.Fatalf("expected one Claude marketplace plugin, got %d", len(claudeMarketplace.Plugins))
	}
	if got := claudeMarketplace.Plugins[0].Source; got != "./plugins/beads" {
		t.Fatalf("Claude marketplace source = %q, want ./plugins/beads", got)
	}

	var claudeManifest struct {
		Skills   string `json:"skills"`
		Commands string `json:"commands"`
		Agents   string `json:"agents"`
	}
	readJSONFile(t, filepath.Join(root, "plugins", "beads", ".claude-plugin", "plugin.json"), &claudeManifest)
	if claudeManifest.Skills != "./skills/" {
		t.Fatalf("Claude skills path = %q, want ./skills/", claudeManifest.Skills)
	}
	if claudeManifest.Commands != "./skills/beads/commands/" {
		t.Fatalf("Claude commands path = %q, want ./skills/beads/commands/", claudeManifest.Commands)
	}
	if claudeManifest.Agents != "./skills/beads/agents/" {
		t.Fatalf("Claude agents path = %q, want ./skills/beads/agents/", claudeManifest.Agents)
	}

	var codexManifest struct {
		Skills string `json:"skills"`
	}
	readJSONFile(t, filepath.Join(root, "plugins", "beads", ".codex-plugin", "plugin.json"), &codexManifest)
	if codexManifest.Skills != "./skills/" {
		t.Fatalf("Codex manifest skills path = %q, want ./skills/", codexManifest.Skills)
	}

	requireRepoFile(t, root, "plugins", "beads", "skills", "beads", "SKILL.md")
	requireRepoFile(t, root, "plugins", "beads", "skills", "beads", "agents", "openai.yaml")
	requireRepoFile(t, root, "plugins", "beads", "skills", "beads", "agents", "task-agent.md")
	requireRepoFile(t, root, "plugins", "beads", "skills", "beads", "commands", "ready.md")
}

func readJSONFile(t *testing.T, path string, dest interface{}) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func requireRepoFile(t *testing.T, root string, parts ...string) {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	} else if info.IsDir() {
		t.Fatalf("expected file %s, got directory", path)
	}
}
