package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"
)

const codexMCPListTimeout = 5 * time.Second

// isCodexEnvironment detects bd commands launched from Codex tool execution.
var isCodexEnvironment = func() bool {
	for _, key := range []string{
		"CODEX_THREAD_ID",
		"CODEX_SANDBOX",
	} {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

// codexMCPList runs Codex's own MCP config listing in the current working directory.
var codexMCPList = func(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "codex", "mcp", "list", "--json")
	return cmd.Output()
}

type codexMCPServer struct {
	Name           string         `json:"name"`
	Enabled        *bool          `json:"enabled"`
	DisabledReason interface{}    `json:"disabled_reason"`
	Transport      codexTransport `json:"transport"`
	EnabledTools   []string       `json:"enabled_tools"`
	DisabledTools  []string       `json:"disabled_tools"`
	StartupTimeout *float64       `json:"startup_timeout_sec"`
	ToolTimeout    *float64       `json:"tool_timeout_sec"`
	Authentication string         `json:"auth_status"`
}

type codexTransport struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	CWD     *string           `json:"cwd"`
	URL     string            `json:"url"`
}

func isCodexMCPActive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), codexMCPListTimeout)
	defer cancel()

	data, err := codexMCPList(ctx)
	if err != nil {
		return false
	}
	return codexMCPListHasBeads(data)
}

func codexMCPListHasBeads(data []byte) bool {
	var servers []codexMCPServer
	if err := json.Unmarshal(data, &servers); err != nil {
		return false
	}
	for _, server := range servers {
		if server.Enabled != nil && !*server.Enabled {
			continue
		}
		if server.DisabledReason != nil {
			continue
		}
		if codexMCPServerMatchesBeads(server) {
			return true
		}
	}
	return false
}

func codexMCPServerMatchesBeads(server codexMCPServer) bool {
	if containsBeads(server.Name) ||
		containsBeads(server.Transport.Command) ||
		containsBeads(server.Transport.URL) {
		return true
	}
	for _, arg := range server.Transport.Args {
		if containsBeads(arg) {
			return true
		}
	}
	for key, value := range server.Transport.Env {
		if containsBeads(key) || containsBeads(value) {
			return true
		}
	}
	return false
}

func containsBeads(value string) bool {
	return strings.Contains(strings.ToLower(value), "beads")
}
