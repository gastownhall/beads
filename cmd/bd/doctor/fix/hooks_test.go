package fix

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDetectExternalHookManagers(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(dir string) error
		expected []string // Expected manager names
	}{
		{
			name: "no hook managers",
			setup: func(dir string) error {
				return nil
			},
			expected: nil,
		},
		{
			name: "lefthook.yml",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "lefthook.yml"), []byte("pre-commit:\n"), 0644)
			},
			expected: []string{"lefthook"},
		},
		{
			name: "lefthook.yaml",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "lefthook.yaml"), []byte("pre-commit:\n"), 0644)
			},
			expected: []string{"lefthook"},
		},
		{
			name: "lefthook.toml",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "lefthook.toml"), []byte("[pre-commit]\n"), 0644)
			},
			expected: []string{"lefthook"},
		},
		{
			name: "lefthook.json",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "lefthook.json"), []byte(`{"pre-commit":{}}`), 0644)
			},
			expected: []string{"lefthook"},
		},
		{
			name: ".lefthook.yml (hidden)",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, ".lefthook.yml"), []byte("pre-commit:\n"), 0644)
			},
			expected: []string{"lefthook"},
		},
		{
			name: ".config/lefthook.yml",
			setup: func(dir string) error {
				configDir := filepath.Join(dir, ".config")
				if err := os.MkdirAll(configDir, 0755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(configDir, "lefthook.yml"), []byte("pre-commit:\n"), 0644)
			},
			expected: []string{"lefthook"},
		},
		{
			name: ".husky directory",
			setup: func(dir string) error {
				return os.MkdirAll(filepath.Join(dir, ".husky"), 0755)
			},
			expected: []string{"husky"},
		},
		{
			name: ".pre-commit-config.yaml",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, ".pre-commit-config.yaml"), []byte("repos:\n"), 0644)
			},
			expected: []string{"pre-commit"},
		},
		{
			name: ".overcommit.yml",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, ".overcommit.yml"), []byte("PreCommit:\n"), 0644)
			},
			expected: []string{"overcommit"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tt.setup(dir); err != nil {
				t.Fatalf("setup failed: %v", err)
			}

			managers := DetectExternalHookManagers(dir)

			if len(tt.expected) == 0 {
				if len(managers) != 0 {
					t.Errorf("expected no managers, got %v", managers)
				}
				return
			}

			if len(managers) != len(tt.expected) {
				t.Errorf("expected %d managers, got %d", len(tt.expected), len(managers))
				return
			}

			for i, exp := range tt.expected {
				if managers[i].Name != exp {
					t.Errorf("expected manager %q, got %q", exp, managers[i].Name)
				}
			}
		})
	}
}

func TestCheckLefthookBdIntegration(t *testing.T) {
	tests := []struct {
		name                 string
		configFile           string
		configContent        string
		expectConfigured     bool
		expectHooksWithBd    []string
		expectHooksWithoutBd []string
		expectNotInConfig    []string
	}{
		{
			name:       "no config",
			configFile: "",
			// No file created
			expectConfigured: false,
		},
		{
			name:       "yaml with bd hooks run",
			configFile: "lefthook.yml",
			configContent: `
pre-commit:
  commands:
    bd:
      run: bd hooks run pre-commit
post-merge:
  commands:
    bd:
      run: bd hooks run post-merge
pre-push:
  commands:
    bd:
      run: bd hooks run pre-push
`,
			expectConfigured:  true,
			expectHooksWithBd: []string{"pre-commit", "post-merge", "pre-push"},
		},
		{
			name:       "yaml with partial bd integration",
			configFile: "lefthook.yml",
			configContent: `
pre-commit:
  commands:
    bd:
      run: bd hooks run pre-commit
    lint:
      run: eslint .
post-merge:
  commands:
    bd:
      run: echo "no bd here"
`,
			expectConfigured:     true,
			expectHooksWithBd:    []string{"pre-commit"},
			expectHooksWithoutBd: []string{"post-merge"},
			expectNotInConfig:    []string{"pre-push"},
		},
		{
			name:       "yaml without bd at all",
			configFile: "lefthook.yml",
			configContent: `
pre-commit:
  commands:
    lint:
      run: eslint .
`,
			expectConfigured:     false,
			expectHooksWithoutBd: []string{"pre-commit"},
			expectNotInConfig:    []string{"post-merge", "pre-push"},
		},
		{
			name:       "toml with bd hooks run",
			configFile: "lefthook.toml",
			configContent: `
[pre-commit.commands.bd]
run = "bd hooks run pre-commit"

[post-merge.commands.bd]
run = "bd hooks run post-merge"

[pre-push.commands.bd]
run = "bd hooks run pre-push"
`,
			expectConfigured:  true,
			expectHooksWithBd: []string{"pre-commit", "post-merge", "pre-push"},
		},
		{
			name:       "json with bd hooks run",
			configFile: "lefthook.json",
			configContent: `{
  "pre-commit": {
    "commands": {
      "bd": {
        "run": "bd hooks run pre-commit"
      }
    }
  },
  "post-merge": {
    "commands": {
      "bd": {
        "run": "bd hooks run post-merge"
      }
    }
  },
  "pre-push": {
    "commands": {
      "bd": {
        "run": "bd hooks run pre-push"
      }
    }
  }
}`,
			expectConfigured:  true,
			expectHooksWithBd: []string{"pre-commit", "post-merge", "pre-push"},
		},
		{
			name:       "hidden config .lefthook.yml",
			configFile: ".lefthook.yml",
			configContent: `
pre-commit:
  commands:
    bd:
      run: bd hooks run pre-commit
`,
			expectConfigured:  true,
			expectHooksWithBd: []string{"pre-commit"},
			expectNotInConfig: []string{"post-merge", "pre-push"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			if tt.configFile != "" {
				configPath := filepath.Join(dir, tt.configFile)
				// Handle .config/ subdirectory
				if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
					t.Fatalf("failed to create config dir: %v", err)
				}
				if err := os.WriteFile(configPath, []byte(tt.configContent), 0644); err != nil {
					t.Fatalf("failed to write config: %v", err)
				}
			}

			status := CheckLefthookBdIntegration(dir)

			if tt.configFile == "" {
				if status != nil {
					t.Errorf("expected nil status for no config, got %+v", status)
				}
				return
			}

			if status == nil {
				t.Fatal("expected non-nil status")
			}

			if status.Configured != tt.expectConfigured {
				t.Errorf("Configured: expected %v, got %v", tt.expectConfigured, status.Configured)
			}

			if !slicesEqual(status.HooksWithBd, tt.expectHooksWithBd) {
				t.Errorf("HooksWithBd: expected %v, got %v", tt.expectHooksWithBd, status.HooksWithBd)
			}

			if !slicesEqual(status.HooksWithoutBd, tt.expectHooksWithoutBd) {
				t.Errorf("HooksWithoutBd: expected %v, got %v", tt.expectHooksWithoutBd, status.HooksWithoutBd)
			}

			if !slicesEqual(status.HooksNotInConfig, tt.expectNotInConfig) {
				t.Errorf("HooksNotInConfig: expected %v, got %v", tt.expectNotInConfig, status.HooksNotInConfig)
			}
		})
	}
}

func TestCheckHuskyBdIntegration(t *testing.T) {
	tests := []struct {
		name                 string
		hooks                map[string]string // hookName -> content
		expectConfigured     bool
		expectHooksWithBd    []string
		expectHooksWithoutBd []string
		expectNotInConfig    []string
	}{
		{
			name:             "no .husky directory",
			hooks:            nil,
			expectConfigured: false,
		},
		{
			name: "all hooks with bd",
			hooks: map[string]string{
				"pre-commit": "#!/bin/sh\nbd hooks run pre-commit\n",
				"post-merge": "#!/bin/sh\nbd hooks run post-merge\n",
				"pre-push":   "#!/bin/sh\nbd hooks run pre-push\n",
			},
			expectConfigured:  true,
			expectHooksWithBd: []string{"pre-commit", "post-merge", "pre-push"},
		},
		{
			name: "partial bd integration",
			hooks: map[string]string{
				"pre-commit": "#!/bin/sh\nbd hooks run pre-commit\n",
				"post-merge": "#!/bin/sh\necho 'no bd'\n",
			},
			expectConfigured:     true,
			expectHooksWithBd:    []string{"pre-commit"},
			expectHooksWithoutBd: []string{"post-merge"},
			expectNotInConfig:    []string{"pre-push"},
		},
		{
			name: "no bd at all",
			hooks: map[string]string{
				"pre-commit": "#!/bin/sh\neslint .\n",
			},
			expectConfigured:     false,
			expectHooksWithoutBd: []string{"pre-commit"},
			expectNotInConfig:    []string{"post-merge", "pre-push"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			if tt.hooks != nil {
				huskyDir := filepath.Join(dir, ".husky")
				if err := os.MkdirAll(huskyDir, 0755); err != nil {
					t.Fatalf("failed to create .husky: %v", err)
				}
				for hookName, content := range tt.hooks {
					if err := os.WriteFile(filepath.Join(huskyDir, hookName), []byte(content), 0755); err != nil {
						t.Fatalf("failed to write hook %s: %v", hookName, err)
					}
				}
			}

			status := CheckHuskyBdIntegration(dir)

			if tt.hooks == nil {
				if status != nil {
					t.Errorf("expected nil status for no .husky, got %+v", status)
				}
				return
			}

			if status == nil {
				t.Fatal("expected non-nil status")
			}

			if status.Configured != tt.expectConfigured {
				t.Errorf("Configured: expected %v, got %v", tt.expectConfigured, status.Configured)
			}

			if !slicesEqual(status.HooksWithBd, tt.expectHooksWithBd) {
				t.Errorf("HooksWithBd: expected %v, got %v", tt.expectHooksWithBd, status.HooksWithBd)
			}

			if !slicesEqual(status.HooksWithoutBd, tt.expectHooksWithoutBd) {
				t.Errorf("HooksWithoutBd: expected %v, got %v", tt.expectHooksWithoutBd, status.HooksWithoutBd)
			}

			if !slicesEqual(status.HooksNotInConfig, tt.expectNotInConfig) {
				t.Errorf("HooksNotInConfig: expected %v, got %v", tt.expectNotInConfig, status.HooksNotInConfig)
			}
		})
	}
}

func TestCheckPrecommitBdIntegration(t *testing.T) {
	tests := []struct {
		name                 string
		configContent        string
		expectNil            bool
		expectConfigured     bool
		expectHooksWithBd    []string
		expectHooksNotInConfig []string
	}{
		{
			name:      "no config",
			expectNil: true,
		},
		{
			name: "all hooks with bd",
			configContent: `repos:
  - repo: local
    hooks:
      - id: bd-pre-commit
        entry: bd hooks run pre-commit
        language: system
        stages: [pre-commit]
      - id: bd-post-merge
        entry: bd hooks run post-merge
        language: system
        stages: [post-merge]
      - id: bd-pre-push
        entry: bd hooks run pre-push
        language: system
        stages: [pre-push]
`,
			expectConfigured:  true,
			expectHooksWithBd: []string{"pre-commit", "post-merge", "pre-push"},
		},
		{
			name: "only pre-commit hook",
			configContent: `repos:
  - repo: local
    hooks:
      - id: bd-pre-commit
        entry: bd hooks run pre-commit
        language: system
`,
			expectConfigured:      true,
			expectHooksWithBd:     []string{"pre-commit"},
			expectHooksNotInConfig: []string{"post-merge", "pre-push"},
		},
		{
			name: "legacy stage names (pre-3.2.0)",
			configContent: `repos:
  - repo: local
    hooks:
      - id: bd-commit
        entry: bd hooks run pre-commit
        language: system
        stages: [commit]
      - id: bd-push
        entry: bd hooks run pre-push
        language: system
        stages: [push]
`,
			expectConfigured:      true,
			expectHooksWithBd:     []string{"pre-commit", "pre-push"},
			expectHooksNotInConfig: []string{"post-merge"},
		},
		{
			name: "no bd hooks at all",
			configContent: `repos:
  - repo: https://github.com/pre-commit/pre-commit-hooks
    rev: v4.5.0
    hooks:
      - id: trailing-whitespace
      - id: end-of-file-fixer
`,
			expectConfigured:      false,
			expectHooksNotInConfig: []string{"pre-commit", "post-merge", "pre-push"},
		},
		{
			name: "mixed bd and other hooks",
			configContent: `repos:
  - repo: https://github.com/pre-commit/pre-commit-hooks
    rev: v4.5.0
    hooks:
      - id: trailing-whitespace
  - repo: local
    hooks:
      - id: bd-pre-commit
        entry: bd hooks run pre-commit
        language: system
`,
			expectConfigured:      true,
			expectHooksWithBd:     []string{"pre-commit"},
			expectHooksNotInConfig: []string{"post-merge", "pre-push"},
		},
		{
			name: "empty repos list",
			configContent: `repos: []
`,
			expectConfigured:      false,
			expectHooksNotInConfig: []string{"pre-commit", "post-merge", "pre-push"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			if tt.configContent != "" {
				configPath := filepath.Join(dir, ".pre-commit-config.yaml")
				if err := os.WriteFile(configPath, []byte(tt.configContent), 0644); err != nil {
					t.Fatalf("failed to write config: %v", err)
				}
			}

			status := CheckPrecommitBdIntegration(dir)

			if tt.expectNil {
				if status != nil {
					t.Errorf("expected nil status, got %+v", status)
				}
				return
			}

			if status == nil {
				t.Fatal("expected non-nil status")
			}

			if status.Manager != "pre-commit" {
				t.Errorf("Manager: expected 'pre-commit', got %q", status.Manager)
			}

			if status.Configured != tt.expectConfigured {
				t.Errorf("Configured: expected %v, got %v", tt.expectConfigured, status.Configured)
			}

			if !slicesEqual(status.HooksWithBd, tt.expectHooksWithBd) {
				t.Errorf("HooksWithBd: expected %v, got %v", tt.expectHooksWithBd, status.HooksWithBd)
			}

			if !slicesEqual(status.HooksNotInConfig, tt.expectHooksNotInConfig) {
				t.Errorf("HooksNotInConfig: expected %v, got %v", tt.expectHooksNotInConfig, status.HooksNotInConfig)
			}
		})
	}
}

func TestBdHookPatternMatching(t *testing.T) {
	tests := []struct {
		content string
		matches bool
	}{
		{"bd hooks run pre-commit", true},
		{"bd  hooks  run pre-commit", true},
		{"bd hooks run post-merge", true},
		{`bd hooks run pre-push "$@"`, true},
		{"if command -v bd; then bd hooks run pre-commit; fi", true},
		{"# bd hooks run is recommended", true},
		// Word boundary tests - should NOT match partial words
		{"kbd hooks runner", false}, // 'kbd' contains 'bd' but is different word
		{"bd_hooks_run", false},     // underscores make different tokens
		{"bd sync", false},
		{"bd export", false},
		{".beads/", false},
		{"eslint .", false},
		{"echo hello", false},
	}

	for _, tt := range tests {
		t.Run(tt.content, func(t *testing.T) {
			if got := bdHookPattern.MatchString(tt.content); got != tt.matches {
				t.Errorf("bdHookPattern.MatchString(%q) = %v, want %v", tt.content, got, tt.matches)
			}
		})
	}
}

func TestDetectActiveHookManager(t *testing.T) {
	tests := []struct {
		name        string
		hookContent string
		expected    string
	}{
		{
			name:        "lefthook signature",
			hookContent: "#!/bin/sh\n# lefthook\nexec lefthook run pre-commit\n",
			expected:    "lefthook",
		},
		{
			name:        "husky signature",
			hookContent: "#!/bin/sh\n. \"$(dirname \"$0\")/_/husky.sh\"\nnpm test\n",
			expected:    "husky",
		},
		{
			name:        "pre-commit framework signature",
			hookContent: "#!/usr/bin/env bash\n# PRE_COMMIT hook\npre-commit run --all-files\n",
			expected:    "pre-commit",
		},
		{
			name:        "prek run signature",
			hookContent: "#!/bin/sh\nprek run pre-commit\n",
			expected:    "prek",
		},
		{
			name:        "prek hook-impl signature",
			hookContent: "#!/usr/bin/env bash\nexec prek hook-impl --hook-type=pre-commit\n",
			expected:    "prek",
		},
		{
			name:        "simple-git-hooks signature",
			hookContent: "#!/bin/sh\n# simple-git-hooks\nnpm run lint\n",
			expected:    "simple-git-hooks",
		},
		{
			name:        "no manager signature",
			hookContent: "#!/bin/sh\necho 'custom hook'\n",
			expected:    "",
		},
		{
			name:        "empty hook",
			hookContent: "",
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()

			// Initialize real git repo
			cmd := exec.Command("git", "init")
			cmd.Dir = dir
			if err := cmd.Run(); err != nil {
				t.Fatalf("failed to init git repo: %v", err)
			}

			// Write hook file
			if tt.hookContent != "" {
				hooksDir := filepath.Join(dir, ".git", "hooks")
				hookPath := filepath.Join(hooksDir, "pre-commit")
				if err := os.WriteFile(hookPath, []byte(tt.hookContent), 0755); err != nil {
					t.Fatalf("failed to write hook: %v", err)
				}
			}

			result := DetectActiveHookManager(dir)
			if result != tt.expected {
				t.Errorf("DetectActiveHookManager() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestDetectActiveHookManager_CustomHooksPath(t *testing.T) {
	dir := t.TempDir()

	// Initialize real git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create custom hooks directory outside .git
	customHooksDir := filepath.Join(dir, "my-hooks")
	if err := os.MkdirAll(customHooksDir, 0755); err != nil {
		t.Fatalf("failed to create custom hooks dir: %v", err)
	}

	// Write hook in custom location with lefthook signature
	hookContent := "#!/bin/sh\n# lefthook\nexec lefthook run pre-commit\n"
	if err := os.WriteFile(filepath.Join(customHooksDir, "pre-commit"), []byte(hookContent), 0755); err != nil {
		t.Fatalf("failed to write hook: %v", err)
	}

	// Set core.hooksPath via git config
	configCmd := exec.Command("git", "config", "core.hooksPath", "my-hooks")
	configCmd.Dir = dir
	if err := configCmd.Run(); err != nil {
		t.Fatalf("failed to set core.hooksPath: %v", err)
	}

	result := DetectActiveHookManager(dir)
	if result != "lefthook" {
		t.Errorf("DetectActiveHookManager() with core.hooksPath = %q, want %q", result, "lefthook")
	}
}

func TestHasBdInCommands(t *testing.T) {
	tests := []struct {
		name     string
		section  interface{}
		expected bool
	}{
		{
			name: "bd hooks run in commands",
			section: map[string]interface{}{
				"commands": map[string]interface{}{
					"bd": map[string]interface{}{
						"run": "bd hooks run pre-commit",
					},
				},
			},
			expected: true,
		},
		{
			name: "no bd in commands",
			section: map[string]interface{}{
				"commands": map[string]interface{}{
					"lint": map[string]interface{}{
						"run": "eslint .",
					},
				},
			},
			expected: false,
		},
		{
			name: "bd mentioned in comment not run field",
			section: map[string]interface{}{
				"commands": map[string]interface{}{
					"lint": map[string]interface{}{
						"run":  "eslint .",
						"tags": "bd hooks run should be added",
					},
				},
			},
			expected: false,
		},
		{
			name:     "nil section",
			section:  nil,
			expected: false,
		},
		{
			name:     "non-map section",
			section:  "string value",
			expected: false,
		},
		{
			name: "no commands key",
			section: map[string]interface{}{
				"parallel": true,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasBdInCommands(tt.section)
			if result != tt.expected {
				t.Errorf("hasBdInCommands() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// slicesEqual compares two string slices for equality (order-insensitive)
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aMap := make(map[string]bool)
	for _, v := range a {
		aMap[v] = true
	}
	for _, v := range b {
		if !aMap[v] {
			return false
		}
	}
	return true
}

func TestCheckExternalHookManagerIntegration(t *testing.T) {
	tests := []struct {
		name                string
		setup               func(dir string) error
		expectNil           bool
		expectManager       string
		expectConfigured    bool
		expectDetectionOnly bool
	}{
		{
			name: "no managers",
			setup: func(dir string) error {
				return nil
			},
			expectNil: true,
		},
		{
			name: "lefthook with bd integration",
			setup: func(dir string) error {
				config := `pre-commit:
  commands:
    bd:
      run: bd hooks run pre-commit
`
				return os.WriteFile(filepath.Join(dir, "lefthook.yml"), []byte(config), 0644)
			},
			expectNil:           false,
			expectManager:       "lefthook",
			expectConfigured:    true,
			expectDetectionOnly: false,
		},
		{
			name: "husky with bd integration",
			setup: func(dir string) error {
				huskyDir := filepath.Join(dir, ".husky")
				if err := os.MkdirAll(huskyDir, 0755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(huskyDir, "pre-commit"), []byte("#!/bin/sh\nbd hooks run pre-commit\n"), 0755)
			},
			expectNil:           false,
			expectManager:       "husky",
			expectConfigured:    true,
			expectDetectionOnly: false,
		},
		{
			name: "pre-commit framework without bd",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, ".pre-commit-config.yaml"), []byte("repos:\n"), 0644)
			},
			expectNil:           false,
			expectManager:       "pre-commit",
			expectConfigured:    false,
			expectDetectionOnly: false, // pre-commit is now fully supported
		},
		{
			name: "pre-commit framework with bd integration",
			setup: func(dir string) error {
				config := `repos:
  - repo: local
    hooks:
      - id: bd-pre-commit
        entry: bd hooks run pre-commit
        language: system
`
				return os.WriteFile(filepath.Join(dir, ".pre-commit-config.yaml"), []byte(config), 0644)
			},
			expectNil:           false,
			expectManager:       "pre-commit",
			expectConfigured:    true,
			expectDetectionOnly: false,
		},
		{
			name: "unsupported manager (overcommit)",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, ".overcommit.yml"), []byte("PreCommit:\n"), 0644)
			},
			expectNil:           false,
			expectManager:       "overcommit",
			expectConfigured:    false,
			expectDetectionOnly: true,
		},
		{
			name: "lefthook without bd",
			setup: func(dir string) error {
				config := `pre-commit:
  commands:
    lint:
      run: eslint .
`
				return os.WriteFile(filepath.Join(dir, "lefthook.yml"), []byte(config), 0644)
			},
			expectNil:           false,
			expectManager:       "lefthook",
			expectConfigured:    false,
			expectDetectionOnly: false, // lefthook IS supported, we can verify its config
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tt.setup(dir); err != nil {
				t.Fatalf("setup failed: %v", err)
			}

			result := CheckExternalHookManagerIntegration(dir)

			if tt.expectNil {
				if result != nil {
					t.Errorf("expected nil, got %+v", result)
				}
				return
			}

			if result == nil {
				t.Fatal("expected non-nil result")
			}

			if result.Manager != tt.expectManager {
				t.Errorf("Manager: expected %q, got %q", tt.expectManager, result.Manager)
			}

			if result.Configured != tt.expectConfigured {
				t.Errorf("Configured: expected %v, got %v", tt.expectConfigured, result.Configured)
			}

			if result.DetectionOnly != tt.expectDetectionOnly {
				t.Errorf("DetectionOnly: expected %v, got %v", tt.expectDetectionOnly, result.DetectionOnly)
			}
		})
	}
}

func TestMultipleManagersDetected(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(dir string) error
		expectManager string
	}{
		{
			name: "lefthook and husky both present - lefthook wins by priority",
			setup: func(dir string) error {
				// Create lefthook config
				config := `pre-commit:
  commands:
    bd:
      run: bd hooks run pre-commit
`
				if err := os.WriteFile(filepath.Join(dir, "lefthook.yml"), []byte(config), 0644); err != nil {
					return err
				}
				// Create husky directory
				huskyDir := filepath.Join(dir, ".husky")
				if err := os.MkdirAll(huskyDir, 0755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(huskyDir, "pre-commit"), []byte("#!/bin/sh\nbd hooks run pre-commit\n"), 0755)
			},
			expectManager: "lefthook",
		},
		{
			name: "husky only",
			setup: func(dir string) error {
				huskyDir := filepath.Join(dir, ".husky")
				if err := os.MkdirAll(huskyDir, 0755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(huskyDir, "pre-commit"), []byte("#!/bin/sh\nbd hooks run pre-commit\n"), 0755)
			},
			expectManager: "husky",
		},
		{
			name: "multiple unsupported managers",
			setup: func(dir string) error {
				// overcommit and yorkie (both unsupported)
				if err := os.WriteFile(filepath.Join(dir, ".overcommit.yml"), []byte("PreCommit:\n"), 0644); err != nil {
					return err
				}
				return os.MkdirAll(filepath.Join(dir, ".yorkie"), 0755)
			},
			expectManager: "overcommit, yorkie", // Falls through to basic status
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tt.setup(dir); err != nil {
				t.Fatalf("setup failed: %v", err)
			}

			result := CheckExternalHookManagerIntegration(dir)
			if result == nil {
				t.Fatal("expected non-nil result")
			}

			if result.Manager != tt.expectManager {
				t.Errorf("Manager: expected %q, got %q", tt.expectManager, result.Manager)
			}
		})
	}
}

func TestManagerNames(t *testing.T) {
	tests := []struct {
		managers []ExternalHookManager
		expected string
	}{
		{nil, ""},
		{[]ExternalHookManager{}, ""},
		{[]ExternalHookManager{{Name: "lefthook"}}, "lefthook"},
		{[]ExternalHookManager{{Name: "lefthook"}, {Name: "husky"}}, "lefthook, husky"},
		{[]ExternalHookManager{{Name: "a"}, {Name: "b"}, {Name: "c"}}, "a, b, c"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := ManagerNames(tt.managers)
			if result != tt.expected {
				t.Errorf("ManagerNames() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestCheckManagerBdIntegration(t *testing.T) {
	tests := []struct {
		name          string
		managerName   string
		setup         func(dir string) error
		expectNil     bool
		expectManager string
	}{
		{
			name:        "unknown manager returns nil",
			managerName: "unknown-manager",
			setup:       func(dir string) error { return nil },
			expectNil:   true,
		},
		{
			name:        "lefthook integration",
			managerName: "lefthook",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "lefthook.yml"), []byte("pre-commit:\n  commands:\n    bd:\n      run: bd hooks run pre-commit\n"), 0644)
			},
			expectNil:     false,
			expectManager: "lefthook",
		},
		{
			name:        "husky integration",
			managerName: "husky",
			setup: func(dir string) error {
				huskyDir := filepath.Join(dir, ".husky")
				if err := os.MkdirAll(huskyDir, 0755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(huskyDir, "pre-commit"), []byte("bd hooks run pre-commit"), 0755)
			},
			expectNil:     false,
			expectManager: "husky",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tt.setup(dir); err != nil {
				t.Fatalf("setup failed: %v", err)
			}

			result := checkManagerBdIntegration(tt.managerName, dir)

			if tt.expectNil {
				if result != nil {
					t.Errorf("expected nil, got %+v", result)
				}
				return
			}

			if result == nil {
				t.Fatal("expected non-nil result")
			}

			if result.Manager != tt.expectManager {
				t.Errorf("Manager: expected %q, got %q", tt.expectManager, result.Manager)
			}
		})
	}
}

func TestDetectExternalHookManagers_MultiplePresentInOrder(t *testing.T) {
	dir := t.TempDir()

	// Create configs for multiple managers
	// lefthook (priority 0)
	if err := os.WriteFile(filepath.Join(dir, "lefthook.yml"), []byte("pre-commit:\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// husky (priority 1)
	if err := os.MkdirAll(filepath.Join(dir, ".husky"), 0755); err != nil {
		t.Fatal(err)
	}
	// pre-commit (priority 2)
	if err := os.WriteFile(filepath.Join(dir, ".pre-commit-config.yaml"), []byte("repos:\n"), 0644); err != nil {
		t.Fatal(err)
	}

	managers := DetectExternalHookManagers(dir)

	if len(managers) != 3 {
		t.Fatalf("expected 3 managers, got %d: %v", len(managers), managers)
	}

	// Verify order matches priority
	expectedOrder := []string{"lefthook", "husky", "pre-commit"}
	for i, exp := range expectedOrder {
		if managers[i].Name != exp {
			t.Errorf("managers[%d]: expected %q, got %q", i, exp, managers[i].Name)
		}
	}
}
