// Package hooks provides a hook system for extensibility.
// Hooks are executable scripts in .beads/hooks/ that run after certain events.
package hooks

import (
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Event types
const (
	EventCreate = "create"
	EventUpdate = "update"
	EventClose  = "close"
)

// Hook file names
const (
	HookOnCreate = "on_create"
	HookOnUpdate = "on_update"
	HookOnClose  = "on_close"
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

	// Run asynchronously (ignore error as this is fire-and-forget)
	go func() {
		_ = r.runHook(hookPath, event, issue)
	}()
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
	default:
		return ""
	}
}
