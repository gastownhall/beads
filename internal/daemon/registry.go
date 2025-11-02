package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
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
	path string
	mu   sync.Mutex
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
	return &Registry{path: registryPath}, nil
}

// readEntries reads all entries from the registry file
func (r *Registry) readEntries() ([]RegistryEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []RegistryEntry{}, nil
		}
		return nil, fmt.Errorf("failed to read registry: %w", err)
	}

	var entries []RegistryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse registry: %w", err)
	}

	return entries, nil
}

// writeEntries writes all entries to the registry file
func (r *Registry) writeEntries(entries []RegistryEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Ensure we always write an array, never null
	if entries == nil {
		entries = []RegistryEntry{}
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal registry: %w", err)
	}

	// nolint:gosec // G306: Registry file needs to be readable for daemon discovery
	if err := os.WriteFile(r.path, data, 0644); err != nil {
		return fmt.Errorf("failed to write registry: %w", err)
	}

	return nil
}

// Register adds a daemon to the registry
func (r *Registry) Register(entry RegistryEntry) error {
	entries, err := r.readEntries()
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

	return r.writeEntries(filtered)
}

// Unregister removes a daemon from the registry
func (r *Registry) Unregister(workspacePath string, pid int) error {
	entries, err := r.readEntries()
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

	return r.writeEntries(filtered)
}

// List returns all daemons from the registry, automatically cleaning up stale entries
func (r *Registry) List() ([]DaemonInfo, error) {
	entries, err := r.readEntries()
	if err != nil {
		return nil, err
	}

	var daemons []DaemonInfo
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
		if err := r.writeEntries(aliveEntries); err != nil {
			// Log warning but don't fail - we still have the daemon list
			fmt.Fprintf(os.Stderr, "Warning: failed to cleanup stale registry entries: %v\n", err)
		}
	}

	return daemons, nil
}

// Clear removes all entries from the registry (for testing)
func (r *Registry) Clear() error {
	return r.writeEntries([]RegistryEntry{})
}
