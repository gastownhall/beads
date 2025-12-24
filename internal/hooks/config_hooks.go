// Package hooks provides a hook system for extensibility.
// This file implements config-based hooks defined in .beads/config.yaml.

package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

// RunConfigCloseHooks executes all on_close hooks from config.yaml.
// Hook commands receive issue data via environment variables:
//   - BEAD_ID: Issue ID (e.g., bd-abc1)
//   - BEAD_TITLE: Issue title
//   - BEAD_TYPE: Issue type (task, bug, feature, etc.)
//   - BEAD_PRIORITY: Priority (0-4)
//   - BEAD_CLOSE_REASON: Close reason if provided
//
// Hooks run synchronously but failures are logged as warnings and don't
// block the close operation.
func RunConfigCloseHooks(ctx context.Context, issue *types.Issue) {
	hooks := config.GetCloseHooks()
	if len(hooks) == 0 {
		return
	}

	// Build environment variables for hooks
	env := append(os.Environ(),
		"BEAD_ID="+issue.ID,
		"BEAD_TITLE="+issue.Title,
		"BEAD_TYPE="+string(issue.IssueType),
		"BEAD_PRIORITY="+strconv.Itoa(issue.Priority),
		"BEAD_CLOSE_REASON="+issue.CloseReason,
	)

	timeout := 10 * time.Second

	for _, hook := range hooks {
		hookCtx, cancel := context.WithTimeout(ctx, timeout)

		// #nosec G204 -- command comes from user's config file
		cmd := exec.CommandContext(hookCtx, "sh", "-c", hook.Command)
		cmd.Env = env
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		err := cmd.Run()
		cancel()

		if err != nil {
			// Log warning but don't fail the close
			name := hook.Name
			if name == "" {
				name = hook.Command
			}
			fmt.Fprintf(os.Stderr, "Warning: close hook %q failed: %v\n", name, err)
		}
	}
}
