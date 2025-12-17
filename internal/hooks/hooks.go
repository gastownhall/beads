// Package hooks provides a hook system for extensibility.
// Hooks are executable scripts in .beads/hooks/ that run after certain events.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Event types
const (
	EventCreate  = "create"
	EventUpdate  = "update"
	EventClose   = "close"
	EventMessage = "message"
)

// Hook file names
const (
	HookOnCreate  = "on_create"
	HookOnUpdate  = "on_update"
	HookOnClose   = "on_close"
	HookOnMessage = "on_message"
)

// Runner handles hook execution
type Runner struct {
	hooksDir string
	timeout  time.Duration
}

// NewRunner creates a new hook runner.
// hooksDir is typically .beads/hooks/ relative to workspace root.
func NewRunner(hooksDir string) *Runner {
	return &Runner{
		hooksDir: hooksDir,
		timeout:  10 * time.Second,
	}
}

// NewRunnerFromWorkspace creates a hook runner for a workspace.
func NewRunnerFromWorkspace(workspaceRoot string) *Runner {
	return NewRunner(filepath.Join(workspaceRoot, ".beads", "hooks"))
}

// Run executes a hook if it exists.
// Runs asynchronously - returns immediately, hook runs in background.
func (r *Runner) Run(event string, issue *types.Issue) {
	hookName := eventToHook(event)
	if hookName == "" {
		return
	}

	hookPath := filepath.Join(r.hooksDir, hookName)

	// Check if hook exists and is executable
	info, err := os.Stat(hookPath)
	if err != nil || info.IsDir() {
		return // Hook doesn't exist, skip silently
	}

	// Check if executable (Unix)
	if info.Mode()&0111 == 0 {
		return // Not executable, skip
	}

	// Run asynchronously
	go r.runHook(hookPath, event, issue)
}

// RunSync executes a hook synchronously and returns any error.
// Useful for testing or when you need to wait for the hook.
func (r *Runner) RunSync(event string, issue *types.Issue) error {
	hookName := eventToHook(event)
	if hookName == "" {
		return nil
	}

	hookPath := filepath.Join(r.hooksDir, hookName)

	// Check if hook exists and is executable
	info, err := os.Stat(hookPath)
	if err != nil || info.IsDir() {
		return nil // Hook doesn't exist, skip silently
	}

	if info.Mode()&0111 == 0 {
		return nil // Not executable, skip
	}

	return r.runHook(hookPath, event, issue)
}

func (r *Runner) runHook(hookPath, event string, issue *types.Issue) error {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()

	// Prepare JSON data for stdin
	issueJSON, err := json.Marshal(issue)
	if err != nil {
		return err
	}

	// Create command: hook_script <issue_id> <event_type>
	// #nosec G204 -- hookPath is from controlled .beads/hooks directory
	cmd := exec.CommandContext(ctx, hookPath, issue.ID, event)
	cmd.Stdin = bytes.NewReader(issueJSON)

	// Capture output for debugging (but don't block on it)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run the hook
	err = cmd.Run()
	if err != nil {
		// Log error but don't fail - hooks shouldn't break beads
		// In production, this could go to a log file
		return err
	}

	return nil
}

// HookExists checks if a hook exists for an event
func (r *Runner) HookExists(event string) bool {
	hookName := eventToHook(event)
	if hookName == "" {
		return false
	}

	hookPath := filepath.Join(r.hooksDir, hookName)
	info, err := os.Stat(hookPath)
	if err != nil || info.IsDir() {
		return false
	}

	return info.Mode()&0111 != 0
}

func eventToHook(event string) string {
	switch event {
	case EventCreate:
		return HookOnCreate
	case EventUpdate:
		return HookOnUpdate
	case EventClose:
		return HookOnClose
	case EventMessage:
		return HookOnMessage
	default:
		return ""
	}
}
