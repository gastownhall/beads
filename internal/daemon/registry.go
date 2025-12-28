package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

// RegistryEntry represents a daemon entry in the registry
type RegistryEntry struct {
	WorkspacePath string    `json:"workspace_path"`
	SocketPath    string    `json:"socket_path"`
	DatabasePath  string    `json:"database_path"`
	PID           int       `json:"pid"`
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at"`
}

// Registry manages the global daemon registry file
type Registry struct {
	path     string
	lockPath string
	mu       sync.Mutex // in-process mutex (cross-process uses file lock)
}

// NewRegistry creates a new registry instance
// The registry is stored in ~/.beads/registry.json
func NewRegistry() (*Registry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	beadsDir := filepath.Join(home, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create .beads directory: %w", err)
	}

	registryPath := filepath.Join(beadsDir, "registry.json")
	lockPath := filepath.Join(beadsDir, "registry.lock")
	return &Registry{path: registryPath, lockPath: lockPath}, nil
}

// withFileLock executes fn while holding an exclusive file lock on the registry.
// This provides cross-process synchronization for read-modify-write operations.
func (r *Registry) withFileLock(fn func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Open or create lock file
	// nolint:gosec // G304: controlled path from config
	lockFile, err := os.OpenFile(r.lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to open lock file: %w", err)
	}
	defer func() { _ = lockFile.Close() }()

	// Acquire exclusive lock (blocking)
	if err := lockfile.FlockExclusiveBlocking(lockFile); err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	defer func() { _ = lockfile.FlockUnlock(lockFile) }()

	return fn()
}

// readEntriesLocked reads all entries from the registry file.
// Caller must hold the file lock.
// Handles missing, empty, or corrupted registry files gracefully.
func (r *Registry) readEntriesLocked() ([]RegistryEntry, error) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []RegistryEntry{}, nil
		}
		return nil, fmt.Errorf("failed to read registry: %w", err)
	}

	// Handle empty file or file with only whitespace/null bytes
	// This can happen if the file was created but never written to, or was corrupted
	trimmed := make([]byte, 0, len(data))
	for _, b := range data {
		if b != 0 && b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			trimmed = append(trimmed, b)
		}
	}
	if len(trimmed) == 0 {
		return []RegistryEntry{}, nil
	}

	var entries []RegistryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		// If registry is corrupted, treat as empty rather than failing
		// A corrupted registry just means we'll need to rediscover daemons
		return []RegistryEntry{}, nil
	}

	return entries, nil
}

// writeEntriesLocked writes all entries to the registry file atomically.
// Caller must hold the file lock.
func (r *Registry) writeEntriesLocked(entries []RegistryEntry) error {
	// Ensure we always write an array, never null
	if entries == nil {
		entries = []RegistryEntry{}
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal registry: %w", err)
	}

	// Write atomically: write to temp file, then rename
	dir := filepath.Dir(r.path)
	tmpFile, err := os.CreateTemp(dir, "registry-*.json.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Write data to temp file
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Sync to disk before rename
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, r.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// readEntries reads all entries from the registry file (with locking).
func (r *Registry) readEntries() ([]RegistryEntry, error) {
	var entries []RegistryEntry
	err := r.withFileLock(func() error {
		var readErr error
		entries, readErr = r.readEntriesLocked()
		return readErr
	})
	return entries, err
}

// writeEntries writes all entries to the registry file (with locking).
func (r *Registry) writeEntries(entries []RegistryEntry) error {
	return r.withFileLock(func() error {
		return r.writeEntriesLocked(entries)
	})
}

// Register adds a daemon to the registry
func (r *Registry) Register(entry RegistryEntry) error {
	return r.withFileLock(func() error {
		entries, err := r.readEntriesLocked()
		if err != nil {
			return err
		}

		// Remove any existing entry for this workspace or PID
		filtered := []RegistryEntry{}
		for _, e := range entries {
			if e.WorkspacePath != entry.WorkspacePath && e.PID != entry.PID {
				filtered = append(filtered, e)
			}
		}

		// Add new entry
		filtered = append(filtered, entry)

		return r.writeEntriesLocked(filtered)
	})
}

// Unregister removes a daemon from the registry
func (r *Registry) Unregister(workspacePath string, pid int) error {
	return r.withFileLock(func() error {
		entries, err := r.readEntriesLocked()
		if err != nil {
			return err
		}

		// Filter out entries matching workspace or PID
		filtered := []RegistryEntry{}
		for _, e := range entries {
			if e.WorkspacePath != workspacePath && e.PID != pid {
				filtered = append(filtered, e)
			}
		}

		return r.writeEntriesLocked(filtered)
	})
}

// List returns all daemons from the registry, automatically cleaning up stale entries
func (r *Registry) List() ([]DaemonInfo, error) {
	var daemons []DaemonInfo

	err := r.withFileLock(func() error {
		entries, err := r.readEntriesLocked()
		if err != nil {
			return err
		}

		var aliveEntries []RegistryEntry

		for _, entry := range entries {
			// Check if process is still alive
			if !isProcessAlive(entry.PID) {
				// Stale entry - skip and don't add to alive list
				continue
			}

			// Process is alive, add to both lists
			aliveEntries = append(aliveEntries, entry)

			// Try to connect and get current status
			daemon := discoverDaemon(entry.SocketPath)

			// If connection failed but process is alive, still include basic info
			if !daemon.Alive {
				daemon.Alive = true // Process exists, socket just might not be ready
				daemon.WorkspacePath = entry.WorkspacePath
				daemon.DatabasePath = entry.DatabasePath
				daemon.SocketPath = entry.SocketPath
				daemon.PID = entry.PID
				daemon.Version = entry.Version
			}

			daemons = append(daemons, daemon)
		}

		// Clean up stale entries from registry
		if len(aliveEntries) != len(entries) {
			if err := r.writeEntriesLocked(aliveEntries); err != nil {
				// Log warning but don't fail - we still have the daemon list
				fmt.Fprintf(os.Stderr, "Warning: failed to cleanup stale registry entries: %v\n", err)
			}
		}

		return nil
	})

	return daemons, err
}

// Clear removes all entries from the registry (for testing)
func (r *Registry) Clear() error {
	return r.writeEntries([]RegistryEntry{})
}
