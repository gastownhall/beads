package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/utils"
)

// walkWithDepth walks a directory tree with depth limiting
func walkWithDepth(root string, currentDepth, maxDepth int, fn func(path string, info os.FileInfo) error) error {
	if currentDepth > maxDepth {
		return nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		// Skip directories we can't read
		return nil
	}

	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}

		// Skip common directories that won't have beads databases
		if info.IsDir() {
			name := entry.Name()
			if strings.HasPrefix(name, ".") && name != ".beads" {
				continue // Skip hidden dirs except .beads
			}
			if name == "node_modules" || name == "vendor" || name == ".git" {
				continue
			}
			// Recurse into subdirectory
			if err := walkWithDepth(path, currentDepth+1, maxDepth, fn); err != nil {
				return err
			}
		} else {
			// Process file
			if err := fn(path, info); err != nil {
				return err
			}
		}
	}

	return nil
}

// DaemonInfo represents metadata about a discovered daemon
type DaemonInfo struct {
	WorkspacePath       string
	DatabasePath        string
	SocketPath          string
	PID                 int
	Version             string
	UptimeSeconds       float64
	LastActivityTime    string
	ExclusiveLockActive bool
	ExclusiveLockHolder string
	Alive               bool
	Error               string
}

// DiscoverDaemons discovers running bd daemons using the registry
// Falls back to filesystem scanning if searchRoots is explicitly provided (for compatibility)
func DiscoverDaemons(searchRoots []string) ([]DaemonInfo, error) {
	// If searchRoots is explicitly provided, use legacy filesystem scan
	// This maintains compatibility for any callers that explicitly specify paths
	if len(searchRoots) > 0 {
		return discoverDaemonsLegacy(searchRoots)
	}

	// Use registry-based discovery (instant, no filesystem scanning)
	registry, err := NewRegistry()
	if err != nil {
		// Fall back to legacy discovery if registry unavailable
		return discoverDaemonsLegacy(nil)
	}

	return registry.List()
}

// discoverDaemonsLegacy scans the filesystem for running bd daemons (legacy method)
// It searches common locations and uses the Status RPC endpoint to gather metadata
func discoverDaemonsLegacy(searchRoots []string) ([]DaemonInfo, error) {
	var daemons []DaemonInfo
	seen := make(map[string]bool)

	// If no search roots provided, use common locations
	if len(searchRoots) == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		searchRoots = []string{
			home,
			"/tmp",
		}
		// Also add current directory if in a git repo
		if cwd, err := os.Getwd(); err == nil {
			searchRoots = append(searchRoots, cwd)
		}
	}

	// Search for .beads/bd.sock files (limit depth to avoid traversing entire filesystem)
	for _, root := range searchRoots {
		maxDepth := 10 // Limit recursion depth
		if err := walkWithDepth(root, 0, maxDepth, func(path string, info os.FileInfo) error {
			// Skip if not a socket file
			if info.Name() != "bd.sock" {
				return nil
			}

			// Skip if already seen this socket
			if seen[path] {
				return nil
			}
			seen[path] = true

			// Try to connect and get status
			daemon := discoverDaemon(path)
			daemons = append(daemons, daemon)

			return nil
		}); err != nil {
			// Continue searching other roots even if one fails
			continue
		}
	}

	return daemons, nil
}

// discoverDaemon attempts to connect to a daemon socket and retrieve its status
func discoverDaemon(socketPath string) DaemonInfo {
	daemon := DaemonInfo{
		SocketPath: socketPath,
		Alive:      false,
	}

	// Fast probe: check daemon lock before attempting RPC if socket doesn't exist
	// This eliminates unnecessary connection attempts when no daemon is running
	// If socket exists, we proceed with RPC for backwards compatibility
	_, err := os.Stat(socketPath)
	socketExists := err == nil
	
	if !socketExists {
		beadsDir := filepath.Dir(socketPath)
		running, _ := lockfile.TryDaemonLock(beadsDir)
		if !running {
			daemon.Error = "daemon lock not held and socket missing"
			// Check for daemon-error file
			if errMsg := checkDaemonErrorFile(socketPath); errMsg != "" {
				daemon.Error = errMsg
			}
			return daemon
		}
	}

	// Try to connect with short timeout
	client, err := rpc.TryConnectWithTimeout(socketPath, 500*time.Millisecond)
	if err != nil {
		daemon.Error = fmt.Sprintf("failed to connect: %v", err)
		// Check for daemon-error file
		if errMsg := checkDaemonErrorFile(socketPath); errMsg != "" {
			daemon.Error = errMsg
		}
		return daemon
	}
	if client == nil {
		daemon.Error = "daemon not responding or unhealthy"
		// Check for daemon-error file
		if errMsg := checkDaemonErrorFile(socketPath); errMsg != "" {
			daemon.Error = errMsg
		}
		return daemon
	}
	defer func() { _ = client.Close() }()

	// Get status
	status, err := client.Status()
	if err != nil {
		daemon.Error = fmt.Sprintf("failed to get status: %v", err)
		return daemon
	}

	// Populate daemon info from status
	daemon.Alive = true
	daemon.WorkspacePath = status.WorkspacePath
	daemon.DatabasePath = status.DatabasePath
	daemon.PID = status.PID
	daemon.Version = status.Version
	daemon.UptimeSeconds = status.UptimeSeconds
	daemon.LastActivityTime = status.LastActivityTime
	daemon.ExclusiveLockActive = status.ExclusiveLockActive
	daemon.ExclusiveLockHolder = status.ExclusiveLockHolder

	return daemon
}

// FindDaemonByWorkspace finds a daemon serving a specific workspace
func FindDaemonByWorkspace(workspacePath string) (*DaemonInfo, error) {
	// Determine the correct .beads directory location
	// For worktrees, .beads is in the main repository root, not the worktree
	beadsDir := findBeadsDirForWorkspace(workspacePath)

	// Try short socket path first (GH#1001 - avoids macOS 104-char limit)
	// This is computed from the workspace path, not the beads dir
	mainWorkspace := filepath.Dir(beadsDir) // Get workspace from .beads dir
	shortSocketPath := rpc.ShortSocketPath(mainWorkspace)
	if _, err := os.Stat(shortSocketPath); err == nil {
		daemon := discoverDaemon(shortSocketPath)
		if daemon.Alive {
			return &daemon, nil
		}
	}

	// Try legacy socket path in .beads directory (backwards compatibility)
	legacySocketPath := filepath.Join(beadsDir, "bd.sock")
	if legacySocketPath != shortSocketPath {
		if _, err := os.Stat(legacySocketPath); err == nil {
			daemon := discoverDaemon(legacySocketPath)
			if daemon.Alive {
				return &daemon, nil
			}
		}
	}

	// Fall back to discovering all daemons
	daemons, err := DiscoverDaemons([]string{workspacePath})
	if err != nil {
		return nil, err
	}

	for _, daemon := range daemons {
		// Use PathsEqual for case-insensitive comparison on macOS/Windows (GH#869)
		if utils.PathsEqual(daemon.WorkspacePath, workspacePath) && daemon.Alive {
			return &daemon, nil
		}
	}

	return nil, fmt.Errorf("no daemon found for workspace: %s", workspacePath)
}

// findBeadsDirForWorkspace determines the correct .beads directory for a workspace
// For worktrees, this is the main repository root; for regular repos, it's the workspace itself
func findBeadsDirForWorkspace(workspacePath string) string {
	// Change to the workspace directory to check if it's a worktree
	originalDir, err := os.Getwd()
	if err != nil {
		return filepath.Join(workspacePath, ".beads") // fallback
	}
	defer func() {
		_ = os.Chdir(originalDir) // restore original directory
	}()

	if err := os.Chdir(workspacePath); err != nil {
		return filepath.Join(workspacePath, ".beads") // fallback
	}

	// Check if we're in a git worktree
	cmd := exec.Command("git", "rev-parse", "--git-dir", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return filepath.Join(workspacePath, ".beads") // fallback
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) >= 2 {
		gitDir := strings.TrimSpace(lines[0])
		commonDir := strings.TrimSpace(lines[1])

		// If git-dir != git-common-dir, we're in a worktree
		if gitDir != commonDir {
			// Worktree: .beads is in main repo root (parent of git-common-dir)
			mainRepoRoot := filepath.Dir(commonDir)
			return filepath.Join(mainRepoRoot, ".beads")
		}
	}

	// Regular repository: .beads is in the workspace
	return filepath.Join(workspacePath, ".beads")
}

// checkDaemonErrorFile checks for a daemon-error file in the .beads directory
func checkDaemonErrorFile(socketPath string) string {
	// Socket path is typically .beads/bd.sock, so get the parent dir
	beadsDir := filepath.Dir(socketPath)
	errFile := filepath.Join(beadsDir, "daemon-error")
	
	data, err := os.ReadFile(errFile)
	if err != nil {
		return ""
	}
	
	return string(data)
}

// CleanupStaleSockets removes socket files and PID files for dead daemons
func CleanupStaleSockets(daemons []DaemonInfo) (int, error) {
	cleaned := 0
	for _, daemon := range daemons {
		if !daemon.Alive && daemon.SocketPath != "" {
			// Remove stale socket file
			if err := os.Remove(daemon.SocketPath); err != nil {
				if !os.IsNotExist(err) {
					return cleaned, fmt.Errorf("failed to remove stale socket %s: %w", daemon.SocketPath, err)
				}
			} else {
				cleaned++
			}

			// Also remove associated PID file if it exists
			socketDir := filepath.Dir(daemon.SocketPath)
			pidFile := filepath.Join(socketDir, "daemon.pid")
			if err := os.Remove(pidFile); err != nil {
				// Ignore errors for PID file - it may not exist
				if !os.IsNotExist(err) {
					// Log warning but don't fail
				}
			}
		}
	}
	return cleaned, nil
}

// StopDaemon gracefully stops a daemon by sending shutdown command via RPC
// Falls back to SIGTERM if RPC fails
func StopDaemon(daemon DaemonInfo) error {
	if !daemon.Alive {
		return fmt.Errorf("daemon is not running")
	}

	// Try graceful shutdown via RPC first
	client, err := rpc.TryConnectWithTimeout(daemon.SocketPath, 500*time.Millisecond)
	if err == nil && client != nil {
		defer func() { _ = client.Close() }()
		if err := client.Shutdown(); err == nil {
			// Wait a bit for daemon to shut down
			time.Sleep(200 * time.Millisecond)
			return nil
		}
	}

	// Fallback to SIGTERM if RPC failed
	return killProcess(daemon.PID)
}

// KillAllFailure represents a failure to kill a specific daemon
type KillAllFailure struct {
	Workspace string `json:"workspace"`
	PID       int    `json:"pid"`
	Error     string `json:"error"`
}

// KillAllResults contains results from KillAllDaemons
type KillAllResults struct {
	Stopped  int              `json:"stopped"`
	Failed   int              `json:"failed"`
	Failures []KillAllFailure `json:"failures,omitempty"`
}

// KillAllDaemons stops all provided daemons, using force if RPC/SIGTERM fail
func KillAllDaemons(daemons []DaemonInfo, force bool) KillAllResults {
	results := KillAllResults{
		Failures: []KillAllFailure{},
	}

	for _, daemon := range daemons {
		if !daemon.Alive {
			continue
		}

		if err := stopDaemonWithTimeout(daemon); err != nil {
			if force {
				// Try force kill
				if err := forceKillProcess(daemon.PID); err != nil {
					results.Failed++
					results.Failures = append(results.Failures, KillAllFailure{
						Workspace: daemon.WorkspacePath,
						PID:       daemon.PID,
						Error:     err.Error(),
					})
					continue
				}
			} else {
				results.Failed++
				results.Failures = append(results.Failures, KillAllFailure{
					Workspace: daemon.WorkspacePath,
					PID:       daemon.PID,
					Error:     err.Error(),
				})
				continue
			}
		}
		results.Stopped++
	}

	return results
}

// stopDaemonWithTimeout tries RPC shutdown, then SIGTERM with timeout, then SIGKILL
func stopDaemonWithTimeout(daemon DaemonInfo) error {
	// Try RPC shutdown first (2 second timeout)
	client, err := rpc.TryConnectWithTimeout(daemon.SocketPath, 2*time.Second)
	if err == nil && client != nil {
		defer func() { _ = client.Close() }()
		if err := client.Shutdown(); err == nil {
			// Wait and verify process died
			time.Sleep(500 * time.Millisecond)
			if !isProcessAlive(daemon.PID) {
				return nil
			}
		}
	}

	// Try graceful kill with 3 second timeout
	if err := killProcess(daemon.PID); err != nil {
		return fmt.Errorf("kill process failed: %w", err)
	}

	// Wait up to 3 seconds for process to die
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if !isProcessAlive(daemon.PID) {
			return nil
		}
	}

	// Graceful kill timeout, try force kill with 1 second timeout
	if err := forceKillProcess(daemon.PID); err != nil {
		return fmt.Errorf("force kill failed: %w", err)
	}

	// Wait up to 1 second for process to die
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		if !isProcessAlive(daemon.PID) {
			return nil
		}
	}

	return fmt.Errorf("process %d did not die after force kill", daemon.PID)
}
