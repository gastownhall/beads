package fix

import (
	"fmt"
	"os"
	"path/filepath"
)

// Daemon fixes daemon issues (stale sockets, version mismatches, duplicates)
// by running bd daemons killall
func Daemon(path string) error {
	// Validate workspace
	if err := validateBeadsWorkspace(path); err != nil {
		return err
	}

	beadsDir := filepath.Join(path, ".beads")
	socketPath := filepath.Join(beadsDir, "bd.sock")

	// Check if there's actually a socket or daemon issue to fix
	hasSocket := false
	if _, err := os.Stat(socketPath); err == nil {
		hasSocket = true
	}

	if !hasSocket {
		// No socket, nothing to clean up
		return nil
	}

	// Get bd binary path
	bdBinary, err := getBdBinary()
	if err != nil {
		return err
	}

	// Run bd daemons killall to clean up stale daemons
	cmd := newBdCmd(bdBinary, "daemons", "killall")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to clean up daemons: %w", err)
	}

	return nil
}
