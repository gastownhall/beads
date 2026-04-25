package main

import (
	"context"
	"testing"
)

func TestCodexEnvironmentDetection(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "none", want: false},
		{name: "thread id", env: map[string]string{"CODEX_THREAD_ID": "thread"}, want: true},
		{name: "sandbox", env: map[string]string{"CODEX_SANDBOX": "seatbelt"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CODEX_THREAD_ID", "")
			t.Setenv("CODEX_SANDBOX", "")
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			if got := isCodexEnvironment(); got != tt.want {
				t.Fatalf("isCodexEnvironment() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCodexMCPListHasBeads(t *testing.T) {
	tests := []struct {
		name string
		json string
		want bool
	}{
		{
			name: "server name",
			json: `[{"name":"beads","enabled":true,"disabled_reason":null,"transport":{"type":"stdio","command":"python","args":[]}}]`,
			want: true,
		},
		{
			name: "server command",
			json: `[{"name":"tasks","enabled":true,"disabled_reason":null,"transport":{"type":"stdio","command":"beads-mcp","args":[]}}]`,
			want: true,
		},
		{
			name: "server arg",
			json: `[{"name":"tasks","enabled":true,"disabled_reason":null,"transport":{"type":"stdio","command":"python","args":["-m","beads_mcp"]}}]`,
			want: true,
		},
		{
			name: "disabled",
			json: `[{"name":"beads","enabled":false,"disabled_reason":null,"transport":{"type":"stdio","command":"beads-mcp","args":[]}}]`,
			want: false,
		},
		{
			name: "disabled reason",
			json: `[{"name":"beads","enabled":true,"disabled_reason":"requirements","transport":{"type":"stdio","command":"beads-mcp","args":[]}}]`,
			want: false,
		},
		{
			name: "other server",
			json: `[{"name":"slackmcp","enabled":true,"disabled_reason":null,"transport":{"type":"stdio","command":"slackmcp","args":[]}}]`,
			want: false,
		},
		{
			name: "invalid json",
			json: `not json`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexMCPListHasBeads([]byte(tt.json)); got != tt.want {
				t.Fatalf("codexMCPListHasBeads() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsCodexMCPActiveUsesCodexList(t *testing.T) {
	original := codexMCPList
	defer func() { codexMCPList = original }()

	called := false
	codexMCPList = func(ctx context.Context) ([]byte, error) {
		called = true
		return []byte(`[{"name":"beads","enabled":true,"disabled_reason":null,"transport":{"type":"stdio","command":"beads-mcp","args":[]}}]`), nil
	}

	if !isCodexMCPActive() {
		t.Fatal("expected Codex MCP detection to find beads server")
	}
	if !called {
		t.Fatal("expected Codex MCP list command to be called")
	}
}
