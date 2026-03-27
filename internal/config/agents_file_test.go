package config

import (
	"strings"
	"testing"
)

func TestAgentsFile(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		restore := envSnapshot(t)
		defer restore()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		got := AgentsFile()
		if got != DefaultAgentsFile {
			t.Errorf("AgentsFile() = %q, want %q", got, DefaultAgentsFile)
		}
	})

	t.Run("returns configured value", func(t *testing.T) {
		restore := envSnapshot(t)
		defer restore()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		Set("agents.file", "BEADS.md")
		t.Cleanup(func() { Set("agents.file", "") })
		got := AgentsFile()
		if got != "BEADS.md" {
			t.Errorf("AgentsFile() = %q, want %q", got, "BEADS.md")
		}
	})
}

func TestValidateAgentsFile(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{name: "valid default", input: "AGENTS.md", wantErr: false},
		{name: "valid custom", input: "BEADS.md", wantErr: false},
		{name: "valid lowercase", input: "agents.md", wantErr: false},
		{name: "valid with hyphens", input: "my-agents.md", wantErr: false},
		{name: "valid with underscores", input: "my_agents.md", wantErr: false},
		{name: "valid double dots in name", input: "foo..bar.md", wantErr: false},

		{name: "empty", input: "", wantErr: true, errMsg: "must not be empty"},
		{name: "absolute path", input: "/etc/passwd.md", wantErr: true, errMsg: "path separators"},
		{name: "traversal", input: "../etc/agents.md", wantErr: true, errMsg: "path separators"},
		{name: "forward slash", input: "docs/AGENTS.md", wantErr: true, errMsg: "path separators"},
		{name: "backslash", input: "docs\\AGENTS.md", wantErr: true, errMsg: "path separators"},
		{name: "wrong extension txt", input: "AGENTS.txt", wantErr: true, errMsg: ".md extension"},
		{name: "wrong extension sh", input: "setup.sh", wantErr: true, errMsg: ".md extension"},
		{name: "no extension", input: "AGENTS", wantErr: true, errMsg: ".md extension"},
		{name: "too long", input: string(make([]byte, 256)) + ".md", wantErr: true, errMsg: "255 characters"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAgentsFile(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateAgentsFile(%q) = nil, want error containing %q", tc.input, tc.errMsg)
				}
				if tc.errMsg != "" && !strings.Contains(err.Error(), tc.errMsg) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.errMsg)
				}
			} else {
				if err != nil {
					t.Fatalf("ValidateAgentsFile(%q) = %v, want nil", tc.input, err)
				}
			}
		})
	}
}

func TestSafeAgentsFile(t *testing.T) {
	t.Run("returns default for invalid config", func(t *testing.T) {
		restore := envSnapshot(t)
		defer restore()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		// Simulate a maliciously edited config with path separators
		Set("agents.file", "../evil.md")
		t.Cleanup(func() { Set("agents.file", "") })
		got := SafeAgentsFile()
		if got != DefaultAgentsFile {
			t.Errorf("SafeAgentsFile() = %q, want %q for invalid config", got, DefaultAgentsFile)
		}
	})

	t.Run("returns valid custom value", func(t *testing.T) {
		restore := envSnapshot(t)
		defer restore()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		Set("agents.file", "BEADS.md")
		t.Cleanup(func() { Set("agents.file", "") })
		got := SafeAgentsFile()
		if got != "BEADS.md" {
			t.Errorf("SafeAgentsFile() = %q, want %q", got, "BEADS.md")
		}
	})

	t.Run("returns default when unset", func(t *testing.T) {
		restore := envSnapshot(t)
		defer restore()
		if err := Initialize(); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		got := SafeAgentsFile()
		if got != DefaultAgentsFile {
			t.Errorf("SafeAgentsFile() = %q, want %q", got, DefaultAgentsFile)
		}
	})
}
