package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

func TestRunConfigCloseHooks_NoHooks(t *testing.T) {
	// Create a temp dir without any config
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Change to the temp dir and initialize config
	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}

	// Re-initialize config
	if err := config.Initialize(); err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	issue := &types.Issue{ID: "bd-test", Title: "Test Issue"}
	ctx := context.Background()

	// Should not panic with no hooks
	RunConfigCloseHooks(ctx, issue)
}

func TestRunConfigCloseHooks_ExecutesCommand(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	outputFile := filepath.Join(tmpDir, "hook_output.txt")

	// Create config.yaml with a close hook
	configContent := `hooks:
  on_close:
    - name: test-hook
      command: echo "$BEAD_ID $BEAD_TITLE" > ` + outputFile + `
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Change to the temp dir and initialize config
	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}

	// Re-initialize config
	if err := config.Initialize(); err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	issue := &types.Issue{
		ID:          "bd-abc1",
		Title:       "Test Issue",
		IssueType:   types.TypeBug,
		Priority:    1,
		CloseReason: "Fixed",
	}
	ctx := context.Background()

	RunConfigCloseHooks(ctx, issue)

	// Wait for hook to complete
	time.Sleep(100 * time.Millisecond)

	// Verify output
	output, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	expected := "bd-abc1 Test Issue"
	if !strings.Contains(string(output), expected) {
		t.Errorf("Hook output = %q, want to contain %q", string(output), expected)
	}
}

func TestRunConfigCloseHooks_EnvVars(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	outputFile := filepath.Join(tmpDir, "env_output.txt")

	// Create config.yaml with a close hook that outputs all env vars
	configContent := `hooks:
  on_close:
    - name: env-check
      command: echo "ID=$BEAD_ID TYPE=$BEAD_TYPE PRIORITY=$BEAD_PRIORITY REASON=$BEAD_CLOSE_REASON" > ` + outputFile + `
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Change to the temp dir and initialize config
	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}

	// Re-initialize config
	if err := config.Initialize(); err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	issue := &types.Issue{
		ID:          "bd-xyz9",
		Title:       "Bug Fix",
		IssueType:   types.TypeFeature,
		Priority:    2,
		CloseReason: "Completed",
	}
	ctx := context.Background()

	RunConfigCloseHooks(ctx, issue)

	// Wait for hook to complete
	time.Sleep(100 * time.Millisecond)

	// Verify output contains all env vars
	output, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	outputStr := string(output)
	checks := []string{
		"ID=bd-xyz9",
		"TYPE=feature",
		"PRIORITY=2",
		"REASON=Completed",
	}

	for _, check := range checks {
		if !strings.Contains(outputStr, check) {
			t.Errorf("Hook output = %q, want to contain %q", outputStr, check)
		}
	}
}

func TestRunConfigCloseHooks_HookFailure(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	successFile := filepath.Join(tmpDir, "success.txt")

	// Create config.yaml with a failing hook followed by a succeeding one
	configContent := `hooks:
  on_close:
    - name: failing-hook
      command: exit 1
    - name: success-hook
      command: echo "success" > ` + successFile + `
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Change to the temp dir and initialize config
	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}

	// Re-initialize config
	if err := config.Initialize(); err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	issue := &types.Issue{ID: "bd-test", Title: "Test"}
	ctx := context.Background()

	// Should not panic even with failing hook
	RunConfigCloseHooks(ctx, issue)

	// Wait for hooks to complete
	time.Sleep(100 * time.Millisecond)

	// Verify second hook still ran
	output, err := os.ReadFile(successFile)
	if err != nil {
		t.Fatalf("Second hook should have run despite first failing: %v", err)
	}

	if !strings.Contains(string(output), "success") {
		t.Error("Second hook did not produce expected output")
	}
}

func TestGetCloseHooks(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	// Create config.yaml with multiple hooks
	configContent := `hooks:
  on_close:
    - name: first-hook
      command: echo first
    - name: second-hook
      command: echo second
    - command: echo unnamed
`
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// Change to the temp dir and initialize config
	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}

	// Re-initialize config
	if err := config.Initialize(); err != nil {
		t.Fatalf("Failed to initialize config: %v", err)
	}

	hooks := config.GetCloseHooks()

	if len(hooks) != 3 {
		t.Fatalf("Expected 3 hooks, got %d", len(hooks))
	}

	if hooks[0].Name != "first-hook" || hooks[0].Command != "echo first" {
		t.Errorf("First hook = %+v, want name=first-hook, command=echo first", hooks[0])
	}

	if hooks[1].Name != "second-hook" || hooks[1].Command != "echo second" {
		t.Errorf("Second hook = %+v, want name=second-hook, command=echo second", hooks[1])
	}

	if hooks[2].Name != "" || hooks[2].Command != "echo unnamed" {
		t.Errorf("Third hook = %+v, want name='', command=echo unnamed", hooks[2])
	}
}
