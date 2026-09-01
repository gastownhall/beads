//go:build windows

package hooks

import (
	"context"
	"os/exec"
)

// Windows has no portable process-group equivalent for arbitrary configured
// executables. CommandContext kills the configured process at the timeout; a
// hook that launches detached children is therefore unsupported for admission
// control on Windows and should use a single executable that owns its lease.
func runPreWriteCommand(ctx context.Context, cmd *exec.Cmd) error {
	return cmd.Run()
}
