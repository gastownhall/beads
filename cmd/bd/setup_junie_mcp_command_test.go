package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/setup"
)

// TestSetupJunieMCPConfigInvokesExistingCommand checks that the MCP server
// entry written by `bd setup junie` invokes a bd subcommand that exists.
func TestSetupJunieMCPConfigInvokesExistingCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := setup.InstallJunie(); err != nil {
		t.Fatalf("InstallJunie: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".junie", "mcp", "mcp.json"))
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse mcp.json: %v", err)
	}

	for name, server := range cfg.MCPServers {
		if server.Command != "bd" {
			continue
		}
		cmd, _, err := rootCmd.Find(server.Args)
		if err != nil || cmd == rootCmd {
			t.Errorf("mcpServers.%s runs `bd %v`, which is not a bd command (err: %v)", name, server.Args, err)
		}
	}
}
