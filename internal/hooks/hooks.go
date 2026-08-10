// Package hooks provides a hook system for extensibility.
// Hooks are executable scripts in .beads/hooks/ that run after certain events.
package hooks

import (
	"os"
	"path/filepath"
	"sync"
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
	// inFlight counts the hooks Run started and has not finished. A bd
	// command is short: it fires its hooks after the commit and returns, and
	// the process exit takes every goroutine with it — including one that has
	// not reached exec yet. Wait is how a caller gives them their moment.
	inFlight sync.WaitGroup
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

	// Run asynchronously (ignore error as this is fire-and-forget).
	// runHook is the same body RunSync runs, so the async path is under the
	// same per-hook timeout and the same process-group kill on expiry.
	r.inFlight.Add(1)
	go func() {
		defer r.inFlight.Done()
		_ = r.runHook(hookPath, event, issue) // Best effort: hook failures should not block the triggering operation
	}()
}

// Wait blocks until every hook Run started has finished, or until timeout,
// and reports whether they all finished.
//
// It exists because fire-and-forget is a promise about the MUTATION, not about
// the hook: a committed write must never fail because a script did, but a
// script that never ran at all is a hook that silently did not fire. A CLI
// process fires its hooks after the commit and then returns from main, and the
// exit takes the goroutines with it. Calling this at teardown gives them the
// window; the timeout is what keeps the promise, since a hung script must delay
// the command's exit by a bounded amount rather than forever.
//
// Callers should pass a budget no larger than the per-hook timeout: a hook that
// outlives its own timeout is being killed anyway.
func (r *Runner) Wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		r.inFlight.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// Timeout is the per-hook budget, which is also the largest budget Wait can
// usefully be given.
func (r *Runner) Timeout() time.Duration { return r.timeout }

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

// maxOutputBytes is the maximum number of bytes captured from hook stdout/stderr
// before truncation. Keeps span attributes reasonably sized.
const maxOutputBytes = 1024

// truncateOutput truncates hook output to maxOutputBytes, appending a note when truncated.
func truncateOutput(s string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	return s[:maxOutputBytes] + "... (truncated)"
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
